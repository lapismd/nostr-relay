// Local development administration. Secrets are read from files, never argv.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"github.com/coder/websocket"
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
func writePrivate(path string, data []byte) error {
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return errors.New("refusing credential symlink")
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".local-admin-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
func readKey(dir, name string) (nostr.SecretKey, error) {
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return nostr.SecretKey{}, fmt.Errorf("missing %s", name)
	}
	key, err := nostr.SecretKeyFromHex(strings.TrimSpace(string(data)))
	if err != nil {
		return key, fmt.Errorf("invalid %s", name)
	}
	return key, nil
}
func initKeys(dir string) error {
	for _, prefix := range []string{"relay", "operator", "github-bridge"} {
		secretPath := filepath.Join(dir, prefix+"-secret")
		if _, err := os.Stat(secretPath); os.IsNotExist(err) {
			key := nostr.Generate()
			if err := writePrivate(secretPath, []byte(key.Hex()+"\n")); err != nil {
				return err
			}
		}
		key, err := readKey(dir, prefix+"-secret")
		if err != nil {
			return err
		}
		pubPath := filepath.Join(dir, prefix+"-pubkey")
		existing, err := os.ReadFile(pubPath)
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("cannot read %s public key", prefix)
		}
		if err == nil && strings.TrimSpace(string(existing)) != key.Public().Hex() {
			return fmt.Errorf("%s keypair does not match", prefix)
		}
		if os.IsNotExist(err) {
			if err := writePrivate(pubPath, []byte(key.Public().Hex()+"\n")); err != nil {
				return err
			}
		}
	}
	fixturePath := filepath.Join(dir, "fixture-secrets.json")
	if _, err := os.Stat(fixturePath); os.IsNotExist(err) {
		operator, err := readKey(dir, "operator-secret")
		if err != nil {
			return err
		}
		keys := map[string]string{"operator": operator.Hex()}
		for _, name := range []string{"communityOne", "communityTwo", "publisher", "curator"} {
			keys[name] = nostr.Generate().Hex()
		}
		data, _ := json.Marshal(map[string]any{"schema": "lapis.registry.local-fixture-secrets/1", "keys": keys})
		if err := writePrivate(fixturePath, data); err != nil {
			return err
		}
	}
	var fixture struct {
		Keys map[string]string `json:"keys"`
	}
	data, err := os.ReadFile(fixturePath)
	if err != nil {
		return err
	}
	if json.Unmarshal(data, &fixture) != nil {
		return errors.New("invalid fixture identities")
	}
	public := map[string]string{}
	for _, name := range []string{"operator", "communityOne", "communityTwo", "publisher", "curator"} {
		key, err := nostr.SecretKeyFromHex(fixture.Keys[name])
		if err != nil {
			return errors.New("invalid fixture identity")
		}
		public[name] = key.Public().Hex()
	}
	data, _ = json.Marshal(public)
	publicPath := filepath.Join(dir, "fixture-public.json")
	if _, err := os.Stat(publicPath); os.IsNotExist(err) {
		if err := writePrivate(publicPath, data); err != nil {
			return err
		}
	}
	readerPath := filepath.Join(dir, "materializer-reader.key")
	existing, err := os.ReadFile(readerPath)
	if os.IsNotExist(err) {
		if err := writePrivate(readerPath, []byte(fixture.Keys["communityOne"]+"\n")); err != nil {
			return err
		}
	} else if err != nil || strings.TrimSpace(string(existing)) != fixture.Keys["communityOne"] {
		return errors.New("materializer reader does not match its fixture identity")
	}
	return validateKeys(dir)
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
func keytrRecords(dir, action string) error {
	backup := filepath.Join(dir, "backups", "keytr.json")
	input := backup
	if action == "backup" {
		input = filepath.Join(dir, "backups", "keytr.pending.json")
	}
	b, err := os.ReadFile(input)
	if err != nil {
		return errors.New("signed Keytr backup input is missing")
	}
	var events []nostr.Event
	if json.Unmarshal(b, &events) != nil || events == nil {
		return errors.New("invalid Keytr backup")
	}
	seen := map[nostr.ID]bool{}
	for _, e := range events {
		if e.Kind != 31777 || !e.CheckID() || !e.VerifySignature() || seen[e.ID] {
			return errors.New("invalid or duplicate signed Keytr backup record")
		}
		seen[e.ID] = true
	}
	if action == "backup" {
		archive := filepath.Join(dir, "backups", "keytr-"+time.Now().UTC().Format("20060102T150405.000000000Z")+".json")
		if err := writePrivate(archive, b); err != nil {
			return err
		}
		if err := writePrivate(backup, b); err != nil {
			return err
		}
		fmt.Printf("Verified backup of %d signed Keytr records.\n", len(events))
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	c, err := dial(ctx)
	if err != nil {
		return err
	}
	defer c.CloseNow()
	for _, e := range events {
		if err := publish(ctx, c, e); err != nil {
			return err
		}
	}
	// Require actual retained records, not just publication acknowledgements.
	for _, e := range events {
		copies, err := query(ctx, c, map[string]any{"ids": []string{e.ID.Hex()}})
		if err != nil || len(copies) != 1 || copies[0].ID != e.ID {
			return errors.New("Keytr restore verification failed")
		}
	}
	fmt.Printf("Restored and verified %d signed Keytr records.\n", len(events))
	return nil
}
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
func github(dir string) error {
	var config struct {
		Connection   map[string]any `json:"connection"`
		Integration  map[string]any `json:"integration"`
		Announcement nostr.Event    `json:"announcement"`
		Owner        string         `json:"owner"`
		Repo         string         `json:"repo"`
	}
	b, err := os.ReadFile(filepath.Join(dir, "github-bootstrap.json"))
	if err != nil {
		return errors.New("github-bootstrap.json is missing; capture the existing binding first")
	}
	if json.Unmarshal(b, &config) != nil {
		return errors.New("invalid GitHub bootstrap configuration")
	}
	key, err := readKey(dir, "operator-secret")
	if err != nil {
		return err
	}
	if config.Owner != key.Public().Hex() || config.Announcement.PubKey != key.Public() || config.Announcement.Kind != 30617 || config.Announcement.Tags.GetD() != config.Repo {
		return errors.New("repository owner does not match copied operator key")
	}
	if !config.Announcement.CheckID() || !config.Announcement.VerifySignature() {
		return errors.New("invalid captured repository announcement")
	}
	response, status, err := api(dir, "POST", "/api/forge/connections/github", config.Connection)
	if err != nil {
		return err
	}
	if status != 201 && status != 200 && status != 409 {
		return fmt.Errorf("GitHub connection bootstrap returned HTTP %d", status)
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(response, &raw) != nil {
		return errors.New("invalid connection response")
	}
	var id string
	json.Unmarshal(raw["id"], &id)
	for _, name := range []string{"connection", "existing"} {
		if id == "" {
			var v struct {
				ID string `json:"id"`
			}
			json.Unmarshal(raw[name], &v)
			id = v.ID
		}
	}
	if id == "" {
		return errors.New("GitHub connection response is missing its ID")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := dial(ctx)
	if err != nil {
		return err
	}
	defer c.CloseNow()
	// Authenticate before restoring the owner's NIP-34 announcement.
	for {
		_, b, err := c.Read(ctx)
		if err != nil {
			return errors.New("relay authentication challenge missing")
		}
		var m []json.RawMessage
		json.Unmarshal(b, &m)
		if len(m) != 2 {
			continue
		}
		var label, challenge string
		json.Unmarshal(m[0], &label)
		json.Unmarshal(m[1], &challenge)
		if label != "AUTH" {
			continue
		}
		event := nostr.Event{Kind: 22242, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"relay", relayPublicURL()}, {"challenge", challenge}}}
		event.Sign(key)
		if err := send(ctx, c, []any{"AUTH", event}); err != nil {
			return err
		}
		break
	}
	// Restore the repository's public NIP-29 channel before its announcement.
	channelTag := config.Announcement.Tags.Find("buzz-channel")
	if len(channelTag) != 2 || channelTag[1] == "" {
		return errors.New("captured repository channel is missing")
	}
	channel := channelTag[1]
	groups, err := query(ctx, c, map[string]any{"kinds": []int{39000}, "#d": []string{channel}})
	if err != nil {
		return err
	}
	if len(groups) == 0 {
		create := nostr.Event{Kind: 9007, CreatedAt: config.Announcement.CreatedAt - 1, Content: `{"name":"GitHub test project","visibility":"open"}`, Tags: nostr.Tags{{"h", channel}, {"name", "GitHub test project"}, {"visibility", "open"}, {"supported_kinds", "9", "7", "1", "5", "1111", "1618", "1619", "1621", "1630", "1631", "1632", "1633", "30617", "30621"}}}
		if err := create.Sign(key); err != nil {
			return err
		}
		if err := publish(ctx, c, create); err != nil {
			return err
		}
	}
	if err := publish(ctx, c, config.Announcement); err != nil {
		return err
	}
	config.Integration["connection_id"] = id
	config.Integration["expected_announcement_id"] = config.Announcement.ID.Hex()
	_, status, err = api(dir, "PUT", "/api/forge/repositories/"+config.Owner+"/"+config.Repo+"/integration", config.Integration)
	if err != nil {
		return err
	}
	if status != 200 {
		return fmt.Errorf("GitHub attachment returned HTTP %d", status)
	}
	fmt.Println("GitHub connection and repository attachment ready.")
	return nil
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

func validateKeys(dir string) error {
	for _, prefix := range []string{"relay", "operator", "github-bridge"} {
		key, err := readKey(dir, prefix+"-secret")
		if err != nil {
			return err
		}
		public, err := os.ReadFile(filepath.Join(dir, prefix+"-pubkey"))
		if err != nil {
			return fmt.Errorf("missing %s public key", prefix)
		}
		if strings.TrimSpace(string(public)) != key.Public().Hex() {
			return fmt.Errorf("%s keypair does not match", prefix)
		}
	}
	var fixtures struct {
		Keys map[string]string `json:"keys"`
	}
	b, err := os.ReadFile(filepath.Join(dir, "fixture-secrets.json"))
	if err != nil {
		return errors.New("missing fixture identities")
	}
	if json.Unmarshal(b, &fixtures) != nil {
		return errors.New("invalid fixture identities")
	}
	key, err := readKey(dir, "operator-secret")
	if err != nil {
		return err
	}
	if fixtures.Keys["operator"] != key.Hex() {
		return errors.New("fixture operator does not match the copied operator")
	}
	var public map[string]string
	b, err = os.ReadFile(filepath.Join(dir, "fixture-public.json"))
	if err != nil {
		return errors.New("missing fixture public identities")
	}
	if json.Unmarshal(b, &public) != nil {
		return errors.New("invalid fixture public identities")
	}
	for _, name := range []string{"operator", "communityOne", "communityTwo", "publisher", "curator"} {
		secret, err := nostr.SecretKeyFromHex(fixtures.Keys[name])
		if err != nil || public[name] != secret.Public().Hex() {
			return errors.New("fixture identity keypair does not match")
		}
	}
	reader, err := os.ReadFile(filepath.Join(dir, "materializer-reader.key"))
	if err != nil || strings.TrimSpace(string(reader)) != fixtures.Keys["communityOne"] {
		return errors.New("materializer reader does not match its fixture identity")
	}
	b, err = os.ReadFile(filepath.Join(dir, "github-bootstrap.json"))
	if err == nil {
		var saved struct {
			Owner        string      `json:"owner"`
			Repo         string      `json:"repo"`
			Announcement nostr.Event `json:"announcement"`
		}
		if json.Unmarshal(b, &saved) != nil || saved.Owner != key.Public().Hex() || saved.Announcement.PubKey != key.Public() || saved.Announcement.Kind != 30617 || saved.Announcement.Tags.GetD() != saved.Repo || !saved.Announcement.CheckID() || !saved.Announcement.VerifySignature() {
			return errors.New("captured GitHub repository owner signature is invalid")
		}
	} else if !os.IsNotExist(err) {
		return errors.New("cannot read captured GitHub binding")
	}
	return nil
}
