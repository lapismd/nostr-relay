package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
)

func TestInitializationPreservesExistingIdentitiesAndRejectsMismatch(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, initKeys(dir))
	files, err := os.ReadDir(dir)
	require.NoError(t, err)
	stamp := time.Unix(1000, 0)
	for _, file := range files {
		require.NoError(t, os.Chtimes(filepath.Join(dir, file.Name()), stamp, stamp))
	}
	require.NoError(t, initKeys(dir))
	for _, file := range files {
		info, err := os.Stat(filepath.Join(dir, file.Name()))
		require.NoError(t, err)
		require.Equal(t, stamp, info.ModTime(), file.Name())
	}
	reader := filepath.Join(dir, "materializer-reader.key")
	replacement := []byte(nostr.Generate().Hex() + "\n")
	require.NoError(t, os.WriteFile(reader, replacement, 0600))
	require.ErrorContains(t, initKeys(dir), "materializer reader does not match")
	retained, err := os.ReadFile(reader)
	require.NoError(t, err)
	require.Equal(t, replacement, retained)
}

func TestBackupVerifiesAllRecordsBeforeReplacingRecovery(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "backups"), 0700))
	events := []nostr.Event{}
	for range 1001 {
		e := nostr.Event{Kind: 31777, CreatedAt: nostr.Now(), Content: "encrypted"}
		require.NoError(t, e.Sign(nostr.Generate()))
		events = append(events, e)
	}
	data, err := json.Marshal(events)
	require.NoError(t, err)
	pending := filepath.Join(dir, "backups/keytr.pending.json")
	require.NoError(t, writePrivate(pending, data))
	require.NoError(t, keytrRecords(dir, "backup"))
	backup := filepath.Join(dir, "backups/keytr.json")
	got, err := os.ReadFile(backup)
	require.NoError(t, err)
	require.Equal(t, data, got)
	info, err := os.Stat(backup)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	events[len(events)-1].Content = "tampered"
	bad, err := json.Marshal(events)
	require.NoError(t, err)
	require.NoError(t, writePrivate(pending, bad))
	require.Error(t, keytrRecords(dir, "backup"))
	got, err = os.ReadFile(backup)
	require.NoError(t, err)
	require.Equal(t, data, got)
}
func TestPrivateWriteRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "link")
	require.NoError(t, os.WriteFile(target, []byte("original"), 0600))
	require.NoError(t, os.Symlink(target, link))
	require.Error(t, writePrivate(link, []byte("replacement")))
	got, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "original", string(got))
}
