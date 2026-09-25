package main

import (
	"context"
	"encoding/json"
	"errors"
	"fiatjaf.com/nostr"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

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
