package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fiatjaf.com/nostr"
	"github.com/coder/websocket"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func query(ctx context.Context, c *websocket.Conn, filter map[string]any) ([]nostr.Event, error) {
	sub := "local-query"
	if err := send(ctx, c, []any{"REQ", sub, filter}); err != nil {
		return nil, err
	}
	defer send(ctx, c, []any{"CLOSE", sub})
	result := []nostr.Event{}
	for {
		_, b, err := c.Read(ctx)
		if err != nil {
			return nil, errors.New("relay query failed")
		}
		var m []json.RawMessage
		if json.Unmarshal(b, &m) != nil || len(m) < 2 {
			continue
		}
		var label, id string
		json.Unmarshal(m[0], &label)
		json.Unmarshal(m[1], &id)
		if id != sub {
			continue
		}
		switch label {
		case "EOSE":
			return result, nil
		case "CLOSED":
			return nil, errors.New("relay query rejected")
		case "EVENT":
			if len(m) != 3 {
				return nil, errors.New("invalid relay record")
			}
			var e nostr.Event
			if json.Unmarshal(m[2], &e) != nil || !e.CheckID() || !e.VerifySignature() {
				return nil, errors.New("invalid signed relay record")
			}
			result = append(result, e)
		}
	}
}

func api(dir, method, path string, body any) ([]byte, int, error) {
	key, err := readKey(dir, "operator-secret")
	if err != nil {
		return nil, 0, err
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, 0, err
	}
	if body == nil {
		b = nil
	}
	publicBase := strings.Replace(strings.Replace(relayPublicURL(), "wss:", "https:", 1), "ws:", "http:", 1)
	connectBase := strings.Replace(strings.Replace(relayURL(), "wss:", "https:", 1), "ws:", "http:", 1)
	target := strings.TrimRight(publicBase, "/") + path
	event := nostr.Event{Kind: 27235, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"u", target}, {"method", method}}}
	if len(b) > 0 {
		digest := sha256.Sum256(b)
		event.Tags = append(event.Tags, nostr.Tag{"payload", hex.EncodeToString(digest[:])})
	}
	if err := event.Sign(key); err != nil {
		return nil, 0, err
	}
	auth, _ := json.Marshal(event)
	req, err := http.NewRequest(method, strings.TrimRight(connectBase, "/")+path, bytes.NewReader(b))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Nostr "+base64.StdEncoding.EncodeToString(auth))
	if host := os.Getenv("LAPIS_LOCAL_RELAY_HOST"); host != "" {
		req.Host = host
		req.Header.Set("X-Forwarded-Proto", strings.SplitN(publicBase, ":", 2)[0])
	}
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, 0, errors.New("relay admin request failed")
	}
	defer response.Body.Close()
	var result json.RawMessage
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return nil, response.StatusCode, errors.New("invalid admin response")
	}
	return result, response.StatusCode, nil
}

func doctor(dir string) error {
	var config struct {
		Owner string `json:"owner"`
		Repo  string `json:"repo"`
	}
	b, err := os.ReadFile(filepath.Join(dir, "github-bootstrap.json"))
	if err != nil {
		return err
	}
	if json.Unmarshal(b, &config) != nil {
		return errors.New("invalid GitHub configuration")
	}
	b, status, err := api(dir, "GET", "/api/forge/repositories/"+config.Owner+"/"+config.Repo+"/health", nil)
	if err != nil {
		return err
	}
	var health struct {
		State string `json:"state"`
	}
	if status != 200 || json.Unmarshal(b, &health) != nil || health.State != "ready" {
		return errors.New("GitHub provider is not ready")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := dial(ctx)
	if err != nil {
		return err
	}
	defer c.CloseNow()
	_, err = query(ctx, c, map[string]any{"kinds": []int{31777}, "limit": 1})
	return err
}
