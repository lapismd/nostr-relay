package buzzcompat

import (
	"encoding/json"
	"fiatjaf.com/nostr"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"zooid/buzzagents/protocol"
)

func (h *Handler) handleQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	ctx, ok := h.authenticate(w, r, body)
	if !ok {
		return
	}

	filters, err := decodeQueryFilters(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid filters: "+err.Error())
		return
	}

	events := make([]nostr.Event, 0)
	seen := make(map[nostr.ID]struct{})
	for _, queryFilter := range filters {
		if reject, message := h.backend.AuthorizeQuery(ctx, queryFilter.Filter); reject {
			writeError(w, http.StatusForbidden, message)
			return
		}
	}
	if h.backend.EphemeralQuery != nil && isPresenceQuery(filters) {
		presenceFilters := make([]nostr.Filter, 0, len(filters))
		for _, queryFilter := range filters {
			presenceFilters = append(presenceFilters, queryFilter.Filter)
		}
		events, err := h.backend.EphemeralQuery(ctx, presenceFilters)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		sortEvents(events)
		if len(events) > queryLimit {
			events = events[:queryLimit]
		}
		writeJSON(w, http.StatusOK, events)
		return
	}
	for _, queryFilter := range filters {
		query := h.backend.Query(ctx, queryFilter.Filter)
		if queryFilter.BeforeID != "" {
			if h.backend.QueryBefore == nil {
				writeError(w, http.StatusBadRequest, "before_id is not supported")
				return
			}
			query = h.backend.QueryBefore(ctx, queryFilter.Filter, queryFilter.BeforeID)
		}
		for event := range query {
			if _, duplicate := seen[event.ID]; duplicate {
				continue
			}
			seen[event.ID] = struct{}{}
			events = append(events, event)
		}
	}
	sortEvents(events)
	if len(events) > queryLimit {
		events = events[:queryLimit]
	}
	writeJSON(w, http.StatusOK, events)
}

func isPresenceQuery(filters []queryFilter) bool {
	if len(filters) == 0 {
		return false
	}
	for _, queryFilter := range filters {
		filter := queryFilter.Filter
		if queryFilter.BeforeID != "" || len(filter.Kinds) != 1 || len(filter.Authors) == 0 {
			return false
		}
		if filter.Kinds[0] != protocol.KindPresence && filter.Kinds[0] != protocol.KindPresenceSnapshot {
			return false
		}
	}
	return true
}

func sortEvents(events []nostr.Event) {
	sort.Slice(events, func(left, right int) bool {
		if events[left].CreatedAt != events[right].CreatedAt {
			return events[left].CreatedAt > events[right].CreatedAt
		}
		return events[left].ID.Hex() < events[right].ID.Hex()
	})
}

func (h *Handler) handleCount(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	ctx, ok := h.authenticate(w, r, body)
	if !ok {
		return
	}

	var filters []nostr.Filter
	if err := decodeJSON(body, &filters); err != nil {
		writeError(w, http.StatusBadRequest, "invalid filters: "+err.Error())
		return
	}

	for _, filter := range filters {
		if reject, message := h.backend.AuthorizeQuery(ctx, filter); reject {
			writeError(w, http.StatusForbidden, message)
			return
		}
	}
	if h.backend.CountMany == nil {
		writeError(w, http.StatusNotImplemented, "count is not supported")
		return
	}
	result, err := h.backend.CountMany(ctx, filters)
	if err != nil {
		status := http.StatusInternalServerError
		if strings.HasPrefix(err.Error(), "restricted:") || strings.HasPrefix(err.Error(), "auth-required:") {
			status = http.StatusForbidden
		} else if strings.HasPrefix(err.Error(), "invalid:") {
			status = http.StatusBadRequest
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func decodeQueryFilters(body []byte) ([]queryFilter, error) {
	var raw []json.RawMessage
	if err := decodeJSON(body, &raw); err != nil {
		return nil, err
	}
	filters := make([]queryFilter, 0, len(raw))
	for index, item := range raw {
		var filter nostr.Filter
		if err := json.Unmarshal(item, &filter); err != nil {
			return nil, fmt.Errorf("filter %d: %w", index, err)
		}
		var extension struct {
			BeforeID string `json:"before_id"`
		}
		if err := json.Unmarshal(item, &extension); err != nil {
			return nil, fmt.Errorf("filter %d: %w", index, err)
		}
		if extension.BeforeID != "" {
			if filter.Until == 0 {
				return nil, fmt.Errorf("filter %d: before_id requires until", index)
			}
			if _, err := nostr.IDFromHex(extension.BeforeID); err != nil {
				return nil, fmt.Errorf("filter %d: invalid before_id", index)
			}
		}
		filters = append(filters, queryFilter{Filter: filter, BeforeID: extension.BeforeID})
	}
	return filters, nil
}

func (h *Handler) handleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	ctx, ok := h.authenticate(w, r, body)
	if !ok {
		return
	}

	var event nostr.Event
	if err := decodeJSON(body, &event); err != nil {
		writeError(w, http.StatusBadRequest, "invalid event: "+err.Error())
		return
	}
	if !event.CheckID() {
		writeJSON(w, http.StatusBadRequest, submitResponse{EventID: event.ID, Message: "invalid: id is computed incorrectly"})
		return
	}
	if !event.VerifySignature() {
		writeJSON(w, http.StatusBadRequest, submitResponse{EventID: event.ID, Message: "invalid: signature is invalid"})
		return
	}
	if protocol.IsWebSocketOnly(event.Kind) {
		writeJSON(w, http.StatusBadRequest, submitResponse{EventID: event.ID, Message: "invalid: ephemeral Buzz events require WebSocket EVENT"})
		return
	}

	skipBroadcast, err := h.backend.Publish(ctx, event)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, submitResponse{EventID: event.ID, Message: err.Error()})
		return
	}
	if !skipBroadcast {
		h.backend.Broadcast(event)
	}
	writeJSON(w, http.StatusOK, submitResponse{EventID: event.ID, Accepted: true, Message: "saved"})
}
