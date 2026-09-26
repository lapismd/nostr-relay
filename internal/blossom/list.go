package blossom

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"fiatjaf.com/nostr"
	upstream "fiatjaf.com/nostr/nipb7/blossom"
)

var ErrCursorNotFound = errors.New("cursor not found")

type ListQuery struct {
	Cursor string
	Limit  int
	Tags   map[string][]string
}

type ListedBlob struct {
	upstream.BlobDescriptor
	NIP94 [][]string          `json:"nip94"`
	Lapis map[string][]string `json:"lapis,omitempty"`
}

func parseListQuery(r *http.Request) (ListQuery, error) {
	query := ListQuery{Limit: 100, Tags: make(map[string][]string)}
	values := r.URL.Query()
	for key, entries := range values {
		switch {
		case key == "cursor":
			if len(entries) != 1 || !blobHash.MatchString(entries[0]) {
				return query, fmt.Errorf("invalid cursor")
			}
			query.Cursor = entries[0]
		case key == "limit":
			if len(entries) != 1 {
				return query, fmt.Errorf("invalid limit")
			}
			limit, err := strconv.Atoi(entries[0])
			if err != nil || limit < 1 || limit > 1000 {
				return query, fmt.Errorf("invalid limit")
			}
			query.Limit = limit
		case strings.HasPrefix(key, "tag."):
			name := strings.TrimPrefix(key, "tag.")
			if name != "m" && name != "x" && name != "size" && name != "purpose" && name != "plugin" && name != "slot" {
				return query, fmt.Errorf("unsupported tag filter")
			}
			if len(entries) == 0 {
				return query, fmt.Errorf("empty tag filter")
			}
			for _, entry := range entries {
				if entry == "" || len(entry) > 128 {
					return query, fmt.Errorf("invalid tag filter")
				}
			}
			query.Tags[name] = entries
		default:
			return query, fmt.Errorf("unsupported list parameter")
		}
	}
	return query, nil
}

func handleList(w http.ResponseWriter, r *http.Request, options Options, owner nostr.PubKey) {
	if options.CanList != nil && !options.CanList(owner) {
		http.Error(w, "unauthorized", http.StatusForbidden)
		return
	}
	if options.ListBlobs == nil {
		http.Error(w, "list unavailable", http.StatusServiceUnavailable)
		return
	}
	query, err := parseListQuery(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	result, err := options.ListBlobs(r, owner, query)
	if errors.Is(err, ErrCursorNotFound) {
		http.Error(w, "cursor not found", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, "failed to list blobs", http.StatusServiceUnavailable)
		return
	}
	if result == nil {
		result = []ListedBlob{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}
