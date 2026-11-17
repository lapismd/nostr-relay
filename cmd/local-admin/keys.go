package main

import (
	"context"
	"encoding/json"
	"errors"
	"fiatjaf.com/nostr"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

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
