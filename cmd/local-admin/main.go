package main

import (
	"context"
	"encoding/json"
	"errors"
	"fiatjaf.com/nostr"
	"fmt"
	"github.com/coder/websocket"
	"os"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "local-admin:", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 3 {
		return errors.New("usage: local-admin {init-keys|validate-keys|backup|restore|github|doctor} <credential-directory>")
	}
	dir := os.Args[2]
	switch os.Args[1] {
	case "init-keys":
		return initKeys(dir)
	case "backup", "restore":
		return keytrRecords(dir, os.Args[1])
	case "github":
		return github(dir)
	case "doctor":
		return doctor(dir)
	case "validate-keys":
		return validateKeys(dir)
	}
	return errors.New("unknown operation")
}

func relayURL() string {
	if s := os.Getenv("LAPIS_LOCAL_RELAY_URL"); s != "" {
		return s
	}
	return "wss://local-relay.lapis.md"
}

func relayPublicURL() string {
	if s := os.Getenv("LAPIS_LOCAL_RELAY_PUBLIC_URL"); s != "" {
		return s
	}
	return relayURL()
}

func dial(ctx context.Context) (*websocket.Conn, error) {
	options := (*websocket.DialOptions)(nil)
	if host := os.Getenv("LAPIS_LOCAL_RELAY_HOST"); host != "" {
		options = &websocket.DialOptions{Host: host}
	}
	c, _, err := websocket.Dial(ctx, relayURL(), options)
	if err != nil {
		return nil, errors.New("cannot connect to relay")
	}
	c.SetReadLimit(4 << 20)
	return c, nil
}

func send(ctx context.Context, c *websocket.Conn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.Write(ctx, websocket.MessageText, b)
}

func publish(ctx context.Context, c *websocket.Conn, e nostr.Event) error {
	if err := send(ctx, c, []any{"EVENT", e}); err != nil {
		return err
	}
	for {
		_, b, err := c.Read(ctx)
		if err != nil {
			return errors.New("relay publication did not complete")
		}
		var m []json.RawMessage
		if json.Unmarshal(b, &m) != nil || len(m) < 3 {
			continue
		}
		var label, id string
		json.Unmarshal(m[0], &label)
		json.Unmarshal(m[1], &id)
		if label == "OK" && id == e.ID.Hex() {
			var ok bool
			json.Unmarshal(m[2], &ok)
			if !ok {
				copies, err := query(ctx, c, map[string]any{"ids": []string{e.ID.Hex()}})
				if err != nil || len(copies) != 1 || copies[0].ID != e.ID {
					return errors.New("relay rejected publication")
				}
			}
			return nil
		}
	}
}
