package zooid

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	lapisblossom "zooid/internal/blossom"
)

const (
	assetUploaded         = "uploaded"
	assetReferenceAdded   = "reference-added"
	assetReferenceRemoved = "reference-removed"
	assetDeleteBlocked    = "delete-blocked"
	assetDeleted          = "deleted"
)

type assetHistoryRecord struct {
	Owner         string
	SHA256        string
	Action        string
	ReferenceType string
	ReferenceKey  string
	Slot          string
	SourceEventID string
	OccurredAt    int64
}

func (store *BlossomStore) recordAssetHistory(ctx context.Context, tx *sql.Tx, record assetHistoryRecord) error {
	_, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s
		(owner_pubkey, sha256, action, reference_type, reference_key, slot, source_event_id, occurred_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, store.Schema.Prefix("asset_history")),
		record.Owner, record.SHA256, record.Action, nullableAssetValue(record.ReferenceType),
		nullableAssetValue(record.ReferenceKey), nullableAssetValue(record.Slot),
		nullableAssetValue(record.SourceEventID), record.OccurredAt)
	return err
}

func nullableAssetValue(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (store *BlossomStore) canDeleteAsset(ctx context.Context, auth nostr.Event, hash string) (bool, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := GetDb().BeginTx(ctx, nil)
	if err != nil {
		return false, "", err
	}
	defer tx.Rollback()
	owner := auth.PubKey.Hex()
	var owns bool
	if err := tx.QueryRowContext(ctx, fmt.Sprintf("SELECT EXISTS(SELECT 1 FROM %s WHERE pubkey=$1 AND sha256=$2)", store.Schema.Prefix("blossom_blobs")), owner, hash).Scan(&owns); err != nil {
		return false, "", err
	}
	if !owns {
		return false, "asset ownership is required for deletion", nil
	}
	var referenceType, referenceKey, slot, sourceEventID string
	err = tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT reference_type, reference_key, slot, source_event_id
		FROM %s WHERE owner_pubkey=$1 AND sha256=$2 ORDER BY reference_type, reference_key, slot LIMIT 1`,
		store.Schema.Prefix("asset_references")), owner, hash).Scan(&referenceType, &referenceKey, &slot, &sourceEventID)
	if errors.Is(err, sql.ErrNoRows) {
		return true, "", nil
	}
	if err != nil {
		return false, "", err
	}
	if err := store.recordAssetHistory(ctx, tx, assetHistoryRecord{
		Owner: owner, SHA256: hash, Action: assetDeleteBlocked,
		ReferenceType: referenceType, ReferenceKey: referenceKey, Slot: slot,
		SourceEventID: sourceEventID, OccurredAt: time.Now().Unix(),
	}); err != nil {
		return false, "", err
	}
	if err := tx.Commit(); err != nil {
		return false, "", err
	}
	return false, fmt.Sprintf("asset is retained by an active %s %s reference", referenceType, slot), nil
}

func (store *BlossomStore) listAssetHistory(r *http.Request, owner nostr.PubKey, limit int, cursor string) (lapisblossom.AssetHistoryPage, error) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	table := store.Schema.Prefix("asset_history")
	where := "owner_pubkey=$1"
	args := []any{owner.Hex()}
	if cursor != "" {
		occurredAt, id, err := parseAssetHistoryCursor(cursor)
		if err != nil {
			return lapisblossom.AssetHistoryPage{}, lapisblossom.ErrCursorNotFound
		}
		var exists bool
		if err := GetDb().QueryRowContext(ctx, fmt.Sprintf("SELECT EXISTS(SELECT 1 FROM %s WHERE owner_pubkey=$1 AND occurred_at=$2 AND id=$3)", table), owner.Hex(), occurredAt, id).Scan(&exists); err != nil {
			return lapisblossom.AssetHistoryPage{}, err
		}
		if !exists {
			return lapisblossom.AssetHistoryPage{}, lapisblossom.ErrCursorNotFound
		}
		where += " AND (occurred_at < $2 OR (occurred_at = $2 AND id < $3))"
		args = append(args, occurredAt, id)
	}
	args = append(args, limit+1)
	rows, err := GetDb().QueryContext(ctx, fmt.Sprintf(`SELECT id, owner_pubkey, sha256, action,
		COALESCE(reference_type, ''), COALESCE(reference_key, ''), COALESCE(slot, ''),
		COALESCE(source_event_id, ''), occurred_at FROM %s WHERE %s
		ORDER BY occurred_at DESC, id DESC LIMIT $%d`, table, where, len(args)), args...)
	if err != nil {
		return lapisblossom.AssetHistoryPage{}, err
	}
	defer rows.Close()
	items := make([]lapisblossom.AssetHistoryEntry, 0, limit+1)
	for rows.Next() {
		var id int64
		var item lapisblossom.AssetHistoryEntry
		if err := rows.Scan(&id, &item.Owner, &item.SHA256, &item.Action, &item.ReferenceType,
			&item.ReferenceKey, &item.Slot, &item.SourceEventID, &item.OccurredAt); err != nil {
			return lapisblossom.AssetHistoryPage{}, err
		}
		item.Cursor = fmt.Sprintf("%d:%d", item.OccurredAt, id)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return lapisblossom.AssetHistoryPage{}, err
	}
	page := lapisblossom.AssetHistoryPage{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		page.NextCursor = page.Items[len(page.Items)-1].Cursor
	}
	return page, nil
}

func parseAssetHistoryCursor(cursor string) (int64, int64, error) {
	parts := strings.Split(cursor, ":")
	if len(parts) != 2 {
		return 0, 0, errors.New("invalid cursor")
	}
	occurredAt, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || occurredAt < 0 {
		return 0, 0, errors.New("invalid cursor time")
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || id < 1 {
		return 0, 0, errors.New("invalid cursor id")
	}
	return occurredAt, id, nil
}
