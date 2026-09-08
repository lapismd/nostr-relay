package zooid

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
)

func TestRunMigrations_AppliesAndTracks(t *testing.T) {
	store := createTestEventStore()
	store.Init()

	kv := GetKeyValueStore(context.Background())

	key := fmt.Sprintf("migration:%s:001_covering_indexes.sql", store.Schema.Name)
	val, err := kv.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("Migration not tracked in kv: %v", err)
	}
	if val != "applied" {
		t.Errorf("Migration kv value = %q, want %q", val, "applied")
	}
}

func TestRunMigrations_Idempotent(t *testing.T) {
	store := createTestEventStore()
	store.Init()

	if err := RunMigrations(context.Background(), store.Schema); err != nil {
		t.Fatalf("Second RunMigrations() failed: %v", err)
	}
}

func TestProjectTaskScopeMigrationPreservesRowsAndAllowsIntersections(t *testing.T) {
	store := createTestEventStore()
	require.NoError(t, store.Init())
	table := store.Schema.Prefix("project_task_scopes")
	constraint := table + "_pkey"
	_, err := GetDb().Exec("ALTER TABLE " + table + " DROP CONSTRAINT " + constraint)
	require.NoError(t, err)
	_, err = GetDb().Exec("ALTER TABLE " + table + " ADD PRIMARY KEY (event_id)")
	require.NoError(t, err)

	event := createTestEvent(nostr.KindTextNote, "legacy scope")
	require.NoError(t, store.SaveEvent(event))
	_, err = GetDb().Exec("INSERT INTO "+table+" (event_id, channel) VALUES ($1, $2)", event.ID.Hex(), "repo-channel")
	require.NoError(t, err)

	key := fmt.Sprintf("migration:%s:005_project_task_scope_intersection.sql", store.Schema.Name)
	_, err = GetDb().Exec("DELETE FROM kv WHERE key = $1", key)
	require.NoError(t, err)
	require.NoError(t, RunMigrations(context.Background(), store.Schema))

	_, err = GetDb().Exec("INSERT INTO "+table+" (event_id, channel) VALUES ($1, $2)", event.ID.Hex(), "origin-channel")
	require.NoError(t, err)
	rows, err := GetDb().Query("SELECT channel FROM "+table+" WHERE event_id = $1 ORDER BY channel", event.ID.Hex())
	require.NoError(t, err)
	defer rows.Close()
	channels := make([]string, 0, 2)
	for rows.Next() {
		var channel string
		require.NoError(t, rows.Scan(&channel))
		channels = append(channels, channel)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []string{"origin-channel", "repo-channel"}, channels)
}

func TestInit_CoveringIndexesExistAndValid(t *testing.T) {
	store := createTestEventStore()
	store.Init()

	indexes := []string{
		strings.ToLower(store.Schema.Prefix("idx_event_tags_key_value_event_id")),
		strings.ToLower(store.Schema.Prefix("idx_events_kind_created_at")),
	}

	for _, idx := range indexes {
		var valid bool
		err := GetDb().QueryRow(`
			SELECT pg_index.indisvalid
			FROM pg_class
			JOIN pg_index ON pg_index.indexrelid = pg_class.oid
			WHERE pg_class.relname = $1
		`, idx).Scan(&valid)
		if err != nil {
			t.Errorf("Index %s not found: %v", idx, err)
			continue
		}
		if !valid {
			t.Errorf("Index %s exists but is INVALID", idx)
		}
	}
}
