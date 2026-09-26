package blossom

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"

	"fiatjaf.com/nostr"
)

var historyCursor = regexp.MustCompile(`^[0-9]+:[1-9][0-9]*$`)

type AssetHistoryEntry struct {
	Cursor        string `json:"cursor"`
	Owner         string `json:"owner"`
	SHA256        string `json:"sha256"`
	Action        string `json:"action"`
	ReferenceType string `json:"reference_type,omitempty"`
	ReferenceKey  string `json:"reference_key,omitempty"`
	Slot          string `json:"slot,omitempty"`
	SourceEventID string `json:"source_event_id,omitempty"`
	OccurredAt    int64  `json:"occurred_at"`
}

type AssetHistoryPage struct {
	Items      []AssetHistoryEntry `json:"items"`
	NextCursor string              `json:"next_cursor,omitempty"`
}

func handleHistoryList(w http.ResponseWriter, r *http.Request, options Options) {
	auth, err := authorize(r, options.ServiceURL, "list", "", false)
	if err != nil || auth == nil {
		http.Error(w, "history authorization required or invalid", http.StatusUnauthorized)
		return
	}
	query := r.URL.Query()
	for key, values := range query {
		if key != "owner" && key != "limit" && key != "cursor" || len(values) != 1 {
			http.Error(w, "invalid history query", http.StatusBadRequest)
			return
		}
	}
	owner := auth.PubKey
	if query.Has("owner") {
		owner, err = nostr.PubKeyFromHex(query.Get("owner"))
		if err != nil {
			http.Error(w, "invalid history owner", http.StatusBadRequest)
			return
		}
	}
	if options.CanViewHistory == nil || !options.CanViewHistory(auth.PubKey, owner) {
		http.Error(w, "history access forbidden", http.StatusForbidden)
		return
	}
	limit := 100
	if query.Has("limit") {
		limit, err = strconv.Atoi(query.Get("limit"))
		if err != nil || limit < 1 || limit > 500 {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return
		}
	}
	cursor := query.Get("cursor")
	if cursor != "" && !historyCursor.MatchString(cursor) {
		http.Error(w, "invalid cursor", http.StatusBadRequest)
		return
	}
	if options.ListHistory == nil {
		http.Error(w, "history unavailable", http.StatusServiceUnavailable)
		return
	}
	page, err := options.ListHistory(r, owner, limit, cursor)
	if errors.Is(err, ErrCursorNotFound) {
		http.Error(w, "cursor not found", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, "failed to list history", http.StatusServiceUnavailable)
		return
	}
	if page.Items == nil {
		page.Items = []AssetHistoryEntry{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(page)
}
