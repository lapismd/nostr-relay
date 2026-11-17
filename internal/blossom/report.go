package blossom

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"time"

	"fiatjaf.com/nostr"
)

const maxReportBytes = 64 << 10

var blobHash = regexp.MustCompile(`^[0-9a-f]{64}$`)

func handleReport(w http.ResponseWriter, r *http.Request, save func(nostr.Event) error) {
	if save == nil {
		http.Error(w, "report storage unavailable", http.StatusServiceUnavailable)
		return
	}
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, maxReportBytes+1))
	if err != nil || len(body) > maxReportBytes {
		http.Error(w, "report body too large or unreadable", http.StatusRequestEntityTooLarge)
		return
	}
	var event nostr.Event
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&event); err != nil {
		http.Error(w, "invalid report event", http.StatusBadRequest)
		return
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		http.Error(w, "invalid report body", http.StatusBadRequest)
		return
	}
	if event.Kind != nostr.KindReporting || !event.CheckID() || !event.VerifySignature() ||
		event.CreatedAt > nostr.Timestamp(time.Now().Unix()+60) {
		http.Error(w, "invalid report event", http.StatusBadRequest)
		return
	}
	validHash := false
	for _, tag := range event.Tags {
		if len(tag) > 1 && tag[0] == "x" {
			if !blobHash.MatchString(tag[1]) {
				http.Error(w, "invalid report hash", http.StatusBadRequest)
				return
			}
			validHash = true
		}
	}
	if !validHash {
		http.Error(w, "report requires a blob hash", http.StatusBadRequest)
		return
	}
	if err := save(event); err != nil {
		http.Error(w, "failed to store report", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
