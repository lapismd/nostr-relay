package khatru

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip45"
)

// CountResult is the result of counting the union of one or more NIP-45
// filters. HyperLogLog contains the 256 registers defined by NIP-45 when the
// request is eligible for HLL aggregation.
type CountResult struct {
	Count       uint64
	Approximate bool
	HyperLogLog []byte
}

type countRequestEnvelope struct {
	SubscriptionID string
	Filters        []nostr.Filter
}

type countResponseEnvelope struct {
	SubscriptionID string
	Result         CountResult
}

type countResponsePayload struct {
	Count       uint64 `json:"count"`
	Approximate bool   `json:"approximate,omitempty"`
	HyperLogLog string `json:"hll,omitempty"`
}

func (envelope countResponseEnvelope) MarshalJSON() ([]byte, error) {
	payload := countResponsePayload{
		Count:       envelope.Result.Count,
		Approximate: envelope.Result.Approximate,
	}
	if len(envelope.Result.HyperLogLog) > 0 {
		if len(envelope.Result.HyperLogLog) != 256 {
			return nil, fmt.Errorf("invalid HLL register count %d", len(envelope.Result.HyperLogLog))
		}
		payload.HyperLogLog = hex.EncodeToString(envelope.Result.HyperLogLog)
	}
	return json.Marshal([]any{"COUNT", envelope.SubscriptionID, payload})
}

func envelopeLabel(message string) string {
	firstQuote := strings.IndexByte(message, '"')
	if firstQuote < 0 {
		return ""
	}
	remainder := message[firstQuote+1:]
	secondQuote := strings.IndexByte(remainder, '"')
	if secondQuote < 0 {
		return ""
	}
	return remainder[:secondQuote]
}

func parseCountRequest(message string) (countRequestEnvelope, error) {
	var raw []json.RawMessage
	if err := json.Unmarshal([]byte(message), &raw); err != nil {
		return countRequestEnvelope{}, fmt.Errorf("invalid JSON: %w", err)
	}
	if len(raw) < 3 {
		return countRequestEnvelope{}, errors.New("missing filters")
	}

	var label string
	if err := json.Unmarshal(raw[0], &label); err != nil || label != "COUNT" {
		return countRequestEnvelope{}, errors.New("invalid COUNT label")
	}

	var request countRequestEnvelope
	if err := json.Unmarshal(raw[1], &request.SubscriptionID); err != nil {
		return request, errors.New("subscription ID must be a string")
	}
	if request.SubscriptionID == "" || len(request.SubscriptionID) > 64 {
		return request, errors.New("subscription ID must contain 1 to 64 characters")
	}

	request.Filters = make([]nostr.Filter, 0, len(raw)-2)
	for index, filterJSON := range raw[2:] {
		var responseShape struct {
			Count *uint64 `json:"count"`
		}
		if err := json.Unmarshal(filterJSON, &responseShape); err == nil && responseShape.Count != nil {
			return request, fmt.Errorf("filter %d is a COUNT response, not a filter", index)
		}

		var filter nostr.Filter
		if err := json.Unmarshal(filterJSON, &filter); err != nil {
			return request, fmt.Errorf("filter %d: %w", index, err)
		}
		request.Filters = append(request.Filters, filter)
	}

	return request, nil
}

func normalizeCountReason(message, fallback string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		message = "count request refused"
	}
	if prefix, _, found := strings.Cut(message, ": "); found {
		switch prefix {
		case "auth-required", "restricted", "invalid", "error":
			return message
		}
	}
	return fallback + ": " + message
}

func (rl *Relay) handleRawCountMessage(ctx context.Context, ws *WebSocket, message string) {
	request, err := parseCountRequest(message)
	if err != nil {
		ws.WriteJSON(nostr.ClosedEnvelope{
			SubscriptionID: request.SubscriptionID,
			Reason:         normalizeCountReason(err.Error(), "invalid"),
		})
		return
	}

	if rl.CountMany == nil && rl.Count == nil && rl.CountHLL == nil {
		ws.WriteJSON(nostr.ClosedEnvelope{
			SubscriptionID: request.SubscriptionID,
			Reason:         "error: this relay does not support NIP-45",
		})
		return
	}

	countCtx := context.WithValue(ctx, subscriptionIdKey, request.SubscriptionID)
	if rl.OnCountMany != nil {
		if reject, reason := rl.OnCountMany(countCtx, request.Filters); reject {
			rl.closeCountRequest(ctx, ws, request.SubscriptionID, normalizeCountReason(reason, "restricted"))
			return
		}
	} else if rl.OnCount != nil {
		for _, filter := range request.Filters {
			if reject, reason := rl.OnCount(countCtx, filter); reject {
				rl.closeCountRequest(ctx, ws, request.SubscriptionID, normalizeCountReason(reason, "restricted"))
				return
			}
		}
	}

	result, err := rl.executeCount(countCtx, request.Filters)
	if err != nil {
		rl.closeCountRequest(ctx, ws, request.SubscriptionID, normalizeCountReason(err.Error(), "error"))
		return
	}
	if len(request.Filters) > 1 && len(result.HyperLogLog) > 0 {
		firstOffset := nip45.HyperLogLogEventPubkeyOffsetForFilter(request.Filters[0])
		for _, filter := range request.Filters[1:] {
			if firstOffset < 0 || nip45.HyperLogLogEventPubkeyOffsetForFilter(filter) != firstOffset {
				result.HyperLogLog = nil
				break
			}
		}
	}

	if err := ws.WriteJSON(countResponseEnvelope{SubscriptionID: request.SubscriptionID, Result: result}); err != nil {
		rl.Log.Printf("failed to write COUNT response: %v", err)
	}
}

func (rl *Relay) closeCountRequest(ctx context.Context, ws *WebSocket, subscriptionID, reason string) {
	if strings.HasPrefix(reason, "auth-required:") {
		RequestAuth(ctx)
	}
	ws.WriteJSON(nostr.ClosedEnvelope{SubscriptionID: subscriptionID, Reason: reason})
}

func (rl *Relay) executeCount(ctx context.Context, filters []nostr.Filter) (CountResult, error) {
	if rl.CountMany != nil {
		return rl.CountMany(ctx, filters)
	}
	if len(filters) != 1 {
		return CountResult{}, errors.New("multiple filters require CountMany support")
	}

	filter := filters[0]
	if offset := nip45.HyperLogLogEventPubkeyOffsetForFilter(filter); offset >= 0 && rl.CountHLL != nil {
		count, hll, err := rl.CountHLL(ctx, filter, offset)
		if err != nil {
			return CountResult{}, err
		}
		result := CountResult{Count: uint64(count)}
		if hll != nil && hll.Count() > 0 {
			result.HyperLogLog = hll.GetRegisters()
		}
		return result, nil
	}
	if rl.Count == nil {
		return CountResult{}, errors.New("the configured count hook cannot serve this filter")
	}
	count, err := rl.Count(ctx, filter)
	return CountResult{Count: uint64(count)}, err
}
