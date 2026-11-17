package zooid

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"fiatjaf.com/nostr"
	lapisblossom "zooid/internal/blossom"
)

func (store *BlossomStore) saveReport(event nostr.Event) error {
	encoded, err := json.Marshal(event)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = GetDb().ExecContext(ctx,
		fmt.Sprintf("INSERT INTO %s (event_id, pubkey, created_at, event_json) VALUES ($1, $2, $3, $4) ON CONFLICT (event_id) DO NOTHING", store.Schema.Prefix("blossom_reports")),
		event.ID.Hex(), event.PubKey.Hex(), int64(event.CreatedAt), encoded)
	return err
}

func (store *BlossomStore) listReports(r *http.Request, limit int, cursor string) ([]lapisblossom.ReportedBlob, error) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	table := store.Schema.Prefix("blossom_reports")
	query := fmt.Sprintf("SELECT event_json, status, reviewed_by, reviewed_at FROM %s ORDER BY created_at DESC, event_id DESC LIMIT $1", table)
	args := []any{limit}
	if cursor != "" {
		var timestamp int64
		err := GetDb().QueryRowContext(ctx, fmt.Sprintf("SELECT created_at FROM %s WHERE event_id = $1", table), cursor).Scan(&timestamp)
		if err == sql.ErrNoRows {
			return nil, lapisblossom.ErrCursorNotFound
		}
		if err != nil {
			return nil, err
		}
		query = fmt.Sprintf("SELECT event_json, status, reviewed_by, reviewed_at FROM %s WHERE (created_at, event_id) < ($2, $3) ORDER BY created_at DESC, event_id DESC LIMIT $1", table)
		args = append(args, timestamp, cursor)
	}
	rows, err := GetDb().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]lapisblossom.ReportedBlob, 0)
	for rows.Next() {
		var raw []byte
		var entry lapisblossom.ReportedBlob
		var reviewer sql.NullString
		var reviewedAt sql.NullInt64
		if err := rows.Scan(&raw, &entry.Status, &reviewer, &reviewedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &entry.Event); err != nil {
			return nil, err
		}
		if reviewer.Valid {
			entry.ReviewedBy = &reviewer.String
		}
		if reviewedAt.Valid {
			entry.ReviewedAt = &reviewedAt.Int64
		}
		result = append(result, entry)
	}
	return result, rows.Err()
}
