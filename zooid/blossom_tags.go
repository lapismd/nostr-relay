package zooid

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"time"

	"fiatjaf.com/nostr"
	upstream "fiatjaf.com/nostr/nipb7/blossom"
)

var blossomLabelValue = regexp.MustCompile(`^[a-z0-9]+(?:[-:.][a-z0-9]+)*$`)

func (store *BlossomStore) saveUploadTags(auth nostr.Event, descriptor upstream.BlobDescriptor) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := GetDb().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var inserted bool
	err = tx.QueryRowContext(ctx, fmt.Sprintf("INSERT INTO %s (pubkey, sha256, uploaded_at, mime_type, size) VALUES ($1, $2, $3, $4, $5) ON CONFLICT (pubkey, sha256) DO NOTHING RETURNING true", store.Schema.Prefix("blossom_blobs")),
		auth.PubKey.Hex(), descriptor.SHA256, int64(descriptor.Uploaded), descriptor.Type, descriptor.Size).Scan(&inserted)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if inserted {
		if err := store.recordAssetHistory(ctx, tx, assetHistoryRecord{
			Owner: auth.PubKey.Hex(), SHA256: descriptor.SHA256,
			Action: assetUploaded, OccurredAt: int64(descriptor.Uploaded),
		}); err != nil {
			return err
		}
	}
	table := store.Schema.Prefix("blossom_tags")
	for source, key := range map[string]string{"lapis-purpose": "purpose", "lapis-plugin": "plugin", "lapis-slot": "slot"} {
		for tag := range auth.Tags.FindAll(source) {
			if len(tag) != 2 || len(tag[1]) > 128 || !blossomLabelValue.MatchString(tag[1]) {
				return fmt.Errorf("invalid %s declaration", source)
			}
			_, err := tx.ExecContext(ctx, fmt.Sprintf("INSERT INTO %s (pubkey, sha256, key, value) VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING", table), auth.PubKey.Hex(), descriptor.SHA256, key, tag[1])
			if err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (store *BlossomStore) tagsForOwner(ctx context.Context, pubkey nostr.PubKey) (map[string]map[string][]string, error) {
	rows, err := GetDb().QueryContext(ctx, fmt.Sprintf("SELECT sha256, key, value FROM %s WHERE pubkey = $1", store.Schema.Prefix("blossom_tags")), pubkey.Hex())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]map[string][]string)
	for rows.Next() {
		var hash, key, value string
		if err := rows.Scan(&hash, &key, &value); err != nil {
			return nil, err
		}
		if result[hash] == nil {
			result[hash] = make(map[string][]string)
		}
		result[hash][key] = append(result[hash][key], value)
	}
	return result, rows.Err()
}

func (store *BlossomStore) removeUploadTags(auth nostr.Event, hash string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := GetDb().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx,
		fmt.Sprintf("DELETE FROM %s WHERE pubkey=$1 AND sha256=$2", store.Schema.Prefix("blossom_tags")),
		auth.PubKey.Hex(), hash)
	if err != nil {
		return err
	}
	var deleted bool
	err = tx.QueryRowContext(ctx,
		fmt.Sprintf("DELETE FROM %s WHERE pubkey=$1 AND sha256=$2 RETURNING true", store.Schema.Prefix("blossom_blobs")),
		auth.PubKey.Hex(), hash).Scan(&deleted)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if deleted {
		if err := store.recordAssetHistory(ctx, tx, assetHistoryRecord{
			Owner: auth.PubKey.Hex(), SHA256: hash, Action: assetDeleted,
			OccurredAt: time.Now().Unix(),
		}); err != nil {
			return err
		}
	}
	return tx.Commit()
}
