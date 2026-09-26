package zooid

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"time"

	"fiatjaf.com/nostr"
	upstream "fiatjaf.com/nostr/nipb7/blossom"
	lapisblossom "zooid/internal/blossom"
)

var blobHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (store *BlossomStore) listBlobs(r *http.Request, owner nostr.PubKey, query lapisblossom.ListQuery) ([]lapisblossom.ListedBlob, error) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	labels, err := store.tagsForOwner(ctx, owner)
	if err != nil {
		return nil, err
	}
	rows, err := GetDb().QueryContext(ctx,
		fmt.Sprintf("SELECT created_at, tags FROM %s WHERE kind = 24242 AND pubkey = $1 AND content = '' AND sig = repeat('0', 128)", store.Schema.Prefix("events")), owner.Hex())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]lapisblossom.ListedBlob, 0)
	for rows.Next() {
		var timestamp int64
		var raw string
		if err := rows.Scan(&timestamp, &raw); err != nil {
			return nil, err
		}
		var tags nostr.Tags
		if json.Unmarshal([]byte(raw), &tags) != nil {
			continue
		}
		x, mimeType, sizeTag := tags.Find("x"), tags.Find("type"), tags.Find("size")
		if len(x) < 2 || len(mimeType) < 2 || len(sizeTag) < 2 || !blobHashPattern.MatchString(x[1]) {
			continue
		}
		size, err := strconv.Atoi(sizeTag[1])
		if err != nil || size < 0 {
			continue
		}
		descriptor := upstream.BlobDescriptor{
			SHA256: x[1], Type: mimeType[1], Size: size, Uploaded: nostr.Timestamp(timestamp),
			URL: blossomPublicURL(store.Config) + "/" + x[1] + upstream.GetExtension(mimeType[1]),
		}
		values := map[string][]string{"m": {descriptor.Type}, "x": {descriptor.SHA256}, "size": {strconv.Itoa(descriptor.Size)}}
		for key, entries := range labels[descriptor.SHA256] {
			values[key] = entries
		}
		if !matchesBlossomTags(values, query.Tags) {
			continue
		}
		result = append(result, lapisblossom.ListedBlob{
			BlobDescriptor: descriptor,
			NIP94:          [][]string{{"url", descriptor.URL}, {"m", descriptor.Type}, {"x", descriptor.SHA256}, {"size", strconv.Itoa(descriptor.Size)}},
			Lapis:          labels[descriptor.SHA256],
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Uploaded != result[j].Uploaded {
			return result[i].Uploaded > result[j].Uploaded
		}
		return result[i].SHA256 < result[j].SHA256
	})
	start := 0
	if query.Cursor != "" {
		start = -1
		for i, item := range result {
			if item.SHA256 == query.Cursor {
				start = i + 1
				break
			}
		}
		if start < 0 {
			return nil, lapisblossom.ErrCursorNotFound
		}
	}
	end := min(start+query.Limit, len(result))
	return result[start:end], nil
}

func matchesBlossomTags(values, filters map[string][]string) bool {
	for key, wanted := range filters {
		matched := false
		for _, actual := range values[key] {
			for _, expected := range wanted {
				if actual == expected {
					matched = true
					break
				}
			}
		}
		if !matched {
			return false
		}
	}
	return true
}
