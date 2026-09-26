package zooid

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"fiatjaf.com/nostr"
)

var (
	assetHash       = regexp.MustCompile(`^[0-9a-f]{64}$`)
	blossomAssetURI = regexp.MustCompile(`^([0-9a-f]{64})\.[a-zA-Z0-9]+$`)
)

type profileAssetReference struct {
	Hash          string
	SourceEventID string
}

func (store *BlossomStore) backfillProfileReferences(ctx context.Context) error {
	for event := range store.Events.QueryEvents(nostr.Filter{Kinds: []nostr.Kind{nostr.KindProfileMetadata}}, 0) {
		if err := store.projectProfileReferences(ctx, event); err != nil {
			return err
		}
	}
	return nil
}

func (store *BlossomStore) projectProfileReferences(ctx context.Context, event nostr.Event) error {
	if event.Kind != nostr.KindProfileMetadata {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := GetDb().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	owner := event.PubKey.Hex()
	var currentID string
	err = tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT id FROM %s WHERE kind=$1 AND pubkey=$2
		ORDER BY created_at DESC, id ASC LIMIT 1`, store.Schema.Prefix("events")), int(nostr.KindProfileMetadata), owner).Scan(&currentID)
	if errors.Is(err, sql.ErrNoRows) || err == nil && currentID != event.ID.Hex() {
		return nil
	}
	if err != nil {
		return err
	}
	desired := store.profileAssetHashes(event.Content)
	for slot, hash := range desired {
		var owned bool
		if err := tx.QueryRowContext(ctx, fmt.Sprintf("SELECT EXISTS(SELECT 1 FROM %s WHERE pubkey=$1 AND sha256=$2)", store.Schema.Prefix("blossom_blobs")), owner, hash).Scan(&owned); err != nil {
			return err
		}
		if !owned {
			delete(desired, slot)
		}
	}
	current, err := store.loadProfileReferences(ctx, tx, owner)
	if err != nil {
		return err
	}
	for _, slot := range []string{"picture", "banner"} {
		previous, hadPrevious := current[slot]
		hash, wanted := desired[slot]
		if hadPrevious && (!wanted || previous.Hash != hash) {
			if _, err := tx.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE reference_type='profile' AND reference_key=$1 AND slot=$2", store.Schema.Prefix("asset_references")), owner, slot); err != nil {
				return err
			}
			if err := store.recordAssetHistory(ctx, tx, assetHistoryRecord{
				Owner: owner, SHA256: previous.Hash, Action: assetReferenceRemoved,
				ReferenceType: "profile", ReferenceKey: owner, Slot: slot,
				SourceEventID: event.ID.Hex(), OccurredAt: int64(event.CreatedAt),
			}); err != nil {
				return err
			}
		}
		if !wanted {
			continue
		}
		if hadPrevious && previous.Hash == hash {
			_, err = tx.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET source_event_id=$1, source_created_at=$2
				WHERE reference_type='profile' AND reference_key=$3 AND slot=$4`, store.Schema.Prefix("asset_references")),
				event.ID.Hex(), int64(event.CreatedAt), owner, slot)
			if err != nil {
				return err
			}
			continue
		}
		_, err = tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s
			(owner_pubkey, reference_type, reference_key, slot, sha256, source_event_id, source_created_at)
			VALUES ($1, 'profile', $1, $2, $3, $4, $5)`, store.Schema.Prefix("asset_references")),
			owner, slot, hash, event.ID.Hex(), int64(event.CreatedAt))
		if err != nil {
			return err
		}
		if err := store.recordAssetHistory(ctx, tx, assetHistoryRecord{
			Owner: owner, SHA256: hash, Action: assetReferenceAdded,
			ReferenceType: "profile", ReferenceKey: owner, Slot: slot,
			SourceEventID: event.ID.Hex(), OccurredAt: int64(event.CreatedAt),
		}); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (store *BlossomStore) loadProfileReferences(ctx context.Context, tx *sql.Tx, owner string) (map[string]profileAssetReference, error) {
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(`SELECT slot, sha256, source_event_id FROM %s
		WHERE reference_type='profile' AND reference_key=$1 FOR UPDATE`, store.Schema.Prefix("asset_references")), owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]profileAssetReference)
	for rows.Next() {
		var slot string
		var reference profileAssetReference
		if err := rows.Scan(&slot, &reference.Hash, &reference.SourceEventID); err != nil {
			return nil, err
		}
		result[slot] = reference
	}
	return result, rows.Err()
}

func (store *BlossomStore) removeProfileReferences(ctx context.Context, event nostr.Event) error {
	if event.Kind != nostr.KindProfileMetadata {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := GetDb().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	owner := event.PubKey.Hex()
	var currentExists bool
	if err := tx.QueryRowContext(ctx, fmt.Sprintf("SELECT EXISTS(SELECT 1 FROM %s WHERE kind=$1 AND pubkey=$2)", store.Schema.Prefix("events")), int(nostr.KindProfileMetadata), owner).Scan(&currentExists); err != nil {
		return err
	}
	if currentExists {
		return nil
	}
	current, err := store.loadProfileReferences(ctx, tx, owner)
	if err != nil {
		return err
	}
	for slot, reference := range current {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE reference_type='profile' AND reference_key=$1 AND slot=$2", store.Schema.Prefix("asset_references")), owner, slot); err != nil {
			return err
		}
		if err := store.recordAssetHistory(ctx, tx, assetHistoryRecord{
			Owner: owner, SHA256: reference.Hash, Action: assetReferenceRemoved,
			ReferenceType: "profile", ReferenceKey: owner, Slot: slot,
			SourceEventID: event.ID.Hex(), OccurredAt: time.Now().Unix(),
		}); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (store *BlossomStore) profileAssetHashes(content string) map[string]string {
	var profile struct {
		Picture string `json:"picture"`
		Banner  string `json:"banner"`
	}
	if json.Unmarshal([]byte(content), &profile) != nil {
		return map[string]string{}
	}
	result := make(map[string]string)
	for slot, value := range map[string]string{"picture": profile.Picture, "banner": profile.Banner} {
		if hash := store.localAssetHash(strings.TrimSpace(value)); hash != "" {
			result[slot] = hash
		}
	}
	return result
}

func (store *BlossomStore) localAssetHash(value string) string {
	parsed, err := url.Parse(value)
	if err != nil {
		return ""
	}
	if strings.EqualFold(parsed.Scheme, "blossom") {
		if match := blossomAssetURI.FindStringSubmatch(parsed.Opaque); len(match) == 2 {
			return match[1]
		}
		return ""
	}
	base, err := url.Parse(blossomPublicURL(store.Config))
	if err != nil || !strings.EqualFold(parsed.Scheme, base.Scheme) || !strings.EqualFold(parsed.Host, base.Host) || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return ""
	}
	prefix := strings.TrimRight(base.Path, "/") + "/"
	path := strings.TrimPrefix(parsed.Path, prefix)
	if path == parsed.Path || len(path) < 64 {
		return ""
	}
	hash := path[:64]
	if !assetHash.MatchString(hash) || len(path) > 64 && path[64] != '.' {
		return ""
	}
	return hash
}
