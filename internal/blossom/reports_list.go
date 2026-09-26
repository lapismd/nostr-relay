package blossom

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"fiatjaf.com/nostr"
)

type ReportedBlob struct {
	Event      nostr.Event `json:"event"`
	Status     string      `json:"status"`
	ReviewedBy *string     `json:"reviewed_by,omitempty"`
	ReviewedAt *int64      `json:"reviewed_at,omitempty"`
}

func handleReportList(w http.ResponseWriter, r *http.Request, options Options) {
	auth, err := authorize(r, options.ServiceURL, "list", "", false)
	if err != nil || auth == nil {
		http.Error(w, "list authorization required or invalid", http.StatusUnauthorized)
		return
	}
	if options.CanReviewReports == nil || !options.CanReviewReports(auth.PubKey) {
		http.Error(w, "report review forbidden", http.StatusForbidden)
		return
	}
	if options.ListReports == nil {
		http.Error(w, "report list unavailable", http.StatusServiceUnavailable)
		return
	}
	query := r.URL.Query()
	limit := 100
	for key, values := range query {
		if key != "limit" && key != "cursor" || len(values) != 1 {
			http.Error(w, "invalid report list query", http.StatusBadRequest)
			return
		}
	}
	if query.Has("limit") {
		limit, err = strconv.Atoi(query.Get("limit"))
		if err != nil || limit < 1 || limit > 500 {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return
		}
	}
	cursor := query.Get("cursor")
	if query.Has("cursor") && !blobHash.MatchString(cursor) {
		http.Error(w, "invalid cursor", http.StatusBadRequest)
		return
	}
	reports, err := options.ListReports(r, limit, cursor)
	if errors.Is(err, ErrCursorNotFound) {
		http.Error(w, "cursor not found", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, "failed to list reports", http.StatusServiceUnavailable)
		return
	}
	if reports == nil {
		reports = []ReportedBlob{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(reports)
}
