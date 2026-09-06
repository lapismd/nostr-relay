package khatru

import (
	"context"
	"errors"

	"fiatjaf.com/nostr"
)

func (rl *Relay) handleRequest(ctx context.Context, id string, ws *WebSocket, filter nostr.Filter) error {
	// then check if we'll reject this filter (we apply this after overwriting
	// because we may, for example, remove some things from the incoming filters
	// that we know we don't support, and then if the end result is an empty
	// filter we can just reject it)
	if nil != rl.OnRequest {
		if reject, msg := rl.OnRequest(ctx, filter); reject {
			return errors.New(nostr.NormalizeOKMessage(msg, "blocked"))
		}
	}

	if filter.LimitZero {
		// don't do any queries, just subscribe to future events
		return nil
	}

	// run the function to query events
	if nil != rl.QueryStored {
		for event := range rl.QueryStored(ctx, filter) {
			if nil != ws.WriteJSON(nostr.EventEnvelope{SubscriptionID: &id, Event: event}) {
				break
			}
		}
	}

	return nil
}
