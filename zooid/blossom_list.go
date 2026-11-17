package zooid

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	upstream "fiatjaf.com/nostr/nipb7/blossom"
	lapisblossom "zooid/internal/blossom"
)

func (store *BlossomStore) listBlobs(r *http.Request, owner nostr.PubKey, query lapisblossom.ListQuery) ([]lapisblossom.ListedBlob, error) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	table := store.Schema.Prefix("blossom_blobs")
	where, args := blossomListWhere(store.Schema.Prefix("blossom_tags"), owner, query.Tags)
	if query.Cursor != "" {
		cursorArgs := append(append([]any(nil), args...), query.Cursor)
		var timestamp int64
		err := GetDb().QueryRowContext(ctx,
			fmt.Sprintf("SELECT b.uploaded_at FROM %s b WHERE %s AND b.sha256 = $%d", table, where, len(cursorArgs)), cursorArgs...).Scan(&timestamp)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, lapisblossom.ErrCursorNotFound
		}
		if err != nil {
			return nil, err
		}
		where += fmt.Sprintf(" AND (b.uploaded_at < $%d OR (b.uploaded_at = $%d AND b.sha256 > $%d))", len(args)+1, len(args)+1, len(args)+2)
		args = append(args, timestamp, query.Cursor)
	}
	args = append(args, query.Limit)
	rows, err := GetDb().QueryContext(ctx,
		fmt.Sprintf("SELECT b.sha256, b.mime_type, b.size, b.uploaded_at FROM %s b WHERE %s ORDER BY b.uploaded_at DESC, b.sha256 ASC LIMIT $%d", table, where, len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]lapisblossom.ListedBlob, 0)
	hashes := make([]string, 0)
	for rows.Next() {
		var hash, mediaType string
		var size, uploaded int64
		if err := rows.Scan(&hash, &mediaType, &size, &uploaded); err != nil {
			return nil, err
		}
		extension := upstream.GetExtension(mediaType)
		if extension == "" {
			extension = ".bin"
		}
		descriptor := upstream.BlobDescriptor{
			SHA256: hash, Type: mediaType, Size: int(size), Uploaded: nostr.Timestamp(uploaded),
			URL: blossomPublicURL(store.Config) + "/" + hash + extension,
		}
		result = append(result, lapisblossom.ListedBlob{
			BlobDescriptor: descriptor,
			NIP94:          [][]string{{"url", descriptor.URL}, {"m", mediaType}, {"x", hash}, {"size", strconv.FormatInt(size, 10)}},
		})
		hashes = append(hashes, hash)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	labels, err := store.tagsForBlobs(ctx, owner, hashes)
	if err != nil {
		return nil, err
	}
	for i := range result {
		result[i].Lapis = labels[result[i].SHA256]
	}
	return result, nil
}

func blossomListWhere(tagTable string, owner nostr.PubKey, filters map[string][]string) (string, []any) {
	args := []any{owner.Hex()}
	parts := []string{"b.pubkey = $1"}
	keys := make([]string, 0, len(filters))
	for key := range filters {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		values := filters[key]
		var column string
		switch key {
		case "m":
			column = "b.mime_type"
		case "x":
			column = "b.sha256"
		case "size":
			column = "b.size::text"
		default:
			args = append(args, key)
			matches := make([]string, 0, len(values))
			for _, value := range values {
				args = append(args, value)
				matches = append(matches, fmt.Sprintf("t.value = $%d", len(args)))
			}
			parts = append(parts, fmt.Sprintf("EXISTS (SELECT 1 FROM %s t WHERE t.pubkey = b.pubkey AND t.sha256 = b.sha256 AND t.key = $%d AND (%s))", tagTable, len(args)-len(values), strings.Join(matches, " OR ")))
			continue
		}
		matches := make([]string, 0, len(values))
		for _, value := range values {
			args = append(args, value)
			matches = append(matches, fmt.Sprintf("%s = $%d", column, len(args)))
		}
		parts = append(parts, "("+strings.Join(matches, " OR ")+")")
	}
	return strings.Join(parts, " AND "), args
}

func (store *BlossomStore) tagsForBlobs(ctx context.Context, owner nostr.PubKey, hashes []string) (map[string]map[string][]string, error) {
	result := make(map[string]map[string][]string)
	if len(hashes) == 0 {
		return result, nil
	}
	args := make([]any, 0, len(hashes)+1)
	args = append(args, owner.Hex())
	markers := make([]string, 0, len(hashes))
	for _, hash := range hashes {
		args = append(args, hash)
		markers = append(markers, fmt.Sprintf("$%d", len(args)))
	}
	rows, err := GetDb().QueryContext(ctx,
		fmt.Sprintf("SELECT sha256, key, value FROM %s WHERE pubkey = $1 AND sha256 IN (%s) ORDER BY sha256, key, value", store.Schema.Prefix("blossom_tags"), strings.Join(markers, ",")), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
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
