package agentbunker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	"fiatjaf.com/nostr"
	"github.com/coder/websocket"
)

func (s *Service) serveWebSocket(w http.ResponseWriter, r *http.Request) {
	originPatterns := make([]string, 0, len(s.Config.AllowedBrowserOrigins))
	for _, origin := range s.Config.AllowedBrowserOrigins {
		if parsed, err := url.Parse(origin); err == nil && parsed.Host != "" {
			originPatterns = append(originPatterns, parsed.Host)
		}
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: originPatterns,
	})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	ctx := context.Background()
	conn.SetReadLimit(256 << 10)
	subscriptions := map[string]struct{}{}
	for {
		_, payload, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var raw []json.RawMessage
		if json.Unmarshal(payload, &raw) != nil || len(raw) == 0 {
			_ = writeWS(ctx, conn, []any{"NOTICE", "invalid message"})
			continue
		}
		var kind string
		if json.Unmarshal(raw[0], &kind) != nil {
			continue
		}
		switch kind {
		case "REQ":
			if len(raw) < 2 {
				continue
			}
			var id string
			if json.Unmarshal(raw[1], &id) != nil || id == "" {
				continue
			}
			subscriptions[id] = struct{}{}
			_ = writeWS(ctx, conn, []any{"EOSE", id})
		case "CLOSE":
			if len(raw) > 1 {
				var id string
				_ = json.Unmarshal(raw[1], &id)
				delete(subscriptions, id)
			}
		case "EVENT":
			if len(raw) != 2 {
				continue
			}
			var event nostr.Event
			if json.Unmarshal(raw[1], &event) != nil {
				continue
			}
			response, handleErr := s.HandleNIP46(ctx, event)
			if handleErr != nil {
				_ = writeWS(ctx, conn, []any{"OK", event.ID.Hex(), false, "restricted: request rejected"})
				continue
			}
			_ = writeWS(ctx, conn, []any{"OK", event.ID.Hex(), true, ""})
			for id := range subscriptions {
				if err = writeWS(ctx, conn, []any{"EVENT", id, response}); err != nil {
					return
				}
			}
		}
	}
}

func writeWS(ctx context.Context, conn *websocket.Conn, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return conn.Write(writeCtx, websocket.MessageText, payload)
}
