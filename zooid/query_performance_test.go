//go:build integration && query_performance

package zooid

import (
	"context"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Scale parameters for the performance dataset.
const (
	perfNumEvents  = 500_000
	perfNumMembers = 10_000
	perfNumGroups  = 10
	perfBatchSize  = 1_000
)

var perfGroups = []string{
	"general", "random", "dev", "design", "support",
	"offtopic", "announcements", "feedback", "testing", "ops",
}

// perfStore is initialised once by TestIntegration_QueryPerformance and
// shared across its sub-tests.
var (
	perfStore     *EventStore
	perfPubkeys   []string // hex pubkeys of the 10K members
	perfSetupOnce sync.Once
	perfSetupErr  error
)

// seedPerfData bulk-inserts 500K events and 10K member pubkeys into a fresh
// schema. It uses raw batch INSERTs (not EventStore.SaveEvent) so the seeding
// finishes in seconds rather than hours.
func seedPerfData(t *testing.T) *EventStore {
	t.Helper()
	perfSetupOnce.Do(func() {
		store := createTestEventStore()
		if err := store.Init(); err != nil {
			perfSetupErr = fmt.Errorf("Init: %w", err)
			return
		}

		db := GetDb()
		copyConn, err := pgx.Connect(context.Background(), Env("DATABASE_URL"))
		if err != nil {
			perfSetupErr = fmt.Errorf("connect COPY client: %w", err)
			return
		}
		defer copyConn.Close(context.Background())
		eventsTable := store.Schema.Prefix("events")
		tagsTable := store.Schema.Prefix("event_tags")
		for _, table := range []string{tagsTable, eventsTable} {
			if _, err := copyConn.Exec(context.Background(), "ALTER TABLE "+table+" SET UNLOGGED"); err != nil {
				perfSetupErr = fmt.Errorf("make fixture table unlogged: %w", err)
				return
			}
		}
		if _, err := copyConn.Exec(context.Background(), "ALTER TABLE "+eventsTable+" DISABLE TRIGGER USER"); err != nil {
			perfSetupErr = fmt.Errorf("disable fixture triggers: %w", err)
			return
		}
		defer func() {
			_, _ = copyConn.Exec(context.Background(), "ALTER TABLE "+eventsTable+" ENABLE TRIGGER USER")
		}()

		// Pre-generate 10K valid nostr pubkeys. Using nostr.Generate()
		// ensures every pubkey passes PubKeyFromHex validation when
		// QueryEvents parses rows back.
		pubkeys := make([]string, perfNumMembers)
		for i := range pubkeys {
			pubkeys[i] = nostr.Generate().Public().Hex()
		}
		t.Logf("Generated %d member pubkeys", perfNumMembers)
		perfPubkeys = pubkeys

		t.Logf("Seeding %d events across %d groups with %d members...",
			perfNumEvents, perfNumGroups, perfNumMembers)
		start := time.Now()

		baseTS := int64(1_700_000_000)

		// Pre-sign one event per member to get a valid signature we can reuse.
		// The DB doesn't verify signatures, but QueryEvents parses sig hex.
		memberSigs := make([]string, perfNumMembers)
		for i := 0; i < perfNumMembers; i++ {
			sk := nostr.Generate()
			// Override pubkeys with correctly-derived ones so PubKeyFromHex
			// accepts them when QueryEvents parses result rows.
			pubkeys[i] = sk.Public().Hex()
			evt := nostr.Event{Kind: 9, CreatedAt: nostr.Timestamp(baseTS), Content: "s"}
			evt.Sign(sk)
			memberSigs[i] = hex.EncodeToString(evt.Sig[:])
		}
		t.Logf("Pre-signed %d member keys", perfNumMembers)

		for batchStart := 0; batchStart < perfNumEvents; batchStart += perfBatchSize {
			batchEnd := batchStart + perfBatchSize
			if batchEnd > perfNumEvents {
				batchEnd = perfNumEvents
			}

			tx, err := copyConn.Begin(context.Background())
			if err != nil {
				perfSetupErr = fmt.Errorf("begin tx: %w", err)
				return
			}

			eventRows := make([][]any, 0, batchEnd-batchStart)
			tagRows := make([][]any, 0, batchEnd-batchStart)
			for i := batchStart; i < batchEnd; i++ {
				memberIdx := i % perfNumMembers
				pubkey := pubkeys[memberIdx]
				sig := memberSigs[memberIdx]
				group := perfGroups[i%perfNumGroups]
				ts := baseTS + int64(i)

				// Event IDs: any 32-byte hex works for IDFromHex.
				// Avoid all-zeros (index 0) just in case.
				id := fmt.Sprintf("%064x", i+1)

				eventRows = append(eventRows, []any{
					id, ts, 9, pubkey, fmt.Sprintf("msg %d in %s", i, group),
					fmt.Sprintf(`[["h","%s"]]`, group), sig,
				})
				tagRows = append(tagRows, []any{id, "h", group, 9})
			}

			if _, err := tx.CopyFrom(
				context.Background(),
				pgx.Identifier{strings.ToLower(eventsTable)},
				[]string{"id", "created_at", "kind", "pubkey", "content", "tags", "sig"},
				pgx.CopyFromRows(eventRows),
			); err != nil {
				tx.Rollback(context.Background())
				perfSetupErr = fmt.Errorf("insert events: %w", err)
				return
			}
			if _, err := tx.CopyFrom(
				context.Background(),
				pgx.Identifier{strings.ToLower(tagsTable)},
				[]string{"event_id", "key", "value", "kind"},
				pgx.CopyFromRows(tagRows),
			); err != nil {
				tx.Rollback(context.Background())
				perfSetupErr = fmt.Errorf("insert tags: %w", err)
				return
			}
			if err := tx.Commit(context.Background()); err != nil {
				perfSetupErr = fmt.Errorf("commit: %w", err)
				return
			}

			if (batchStart+perfBatchSize)%100_000 == 0 {
				t.Logf("  %d / %d events inserted...", batchStart+perfBatchSize, perfNumEvents)
			}
		}

		// Add a realistic long Registry discussion so the same fixture covers
		// exact NIP-45 counts and newest-page latency for kinds 11/1111.
		tx, err := copyConn.Begin(context.Background())
		if err != nil {
			perfSetupErr = fmt.Errorf("begin discussion tx: %w", err)
			return
		}
		discussionEvents := make([][]any, 0, 260)
		discussionTags := make([][]any, 0, 260)
		for i := 0; i < 260; i++ {
			kind := 11
			if i > 0 {
				kind = 1111
			}
			id := fmt.Sprintf("%064x", perfNumEvents+i+1)
			discussionEvents = append(discussionEvents, []any{
				id, baseTS + int64(perfNumEvents+i), kind, pubkeys[i],
				fmt.Sprintf("registry reply %d", i), `[["h","registry-discussion"]]`, memberSigs[i],
			})
			discussionTags = append(discussionTags, []any{id, "h", "registry-discussion", kind})
		}
		if _, err := tx.CopyFrom(context.Background(), pgx.Identifier{strings.ToLower(eventsTable)}, []string{"id", "created_at", "kind", "pubkey", "content", "tags", "sig"}, pgx.CopyFromRows(discussionEvents)); err != nil {
			tx.Rollback(context.Background())
			perfSetupErr = fmt.Errorf("insert discussion events: %w", err)
			return
		}
		if _, err := tx.CopyFrom(context.Background(), pgx.Identifier{strings.ToLower(tagsTable)}, []string{"event_id", "key", "value", "kind"}, pgx.CopyFromRows(discussionTags)); err != nil {
			tx.Rollback(context.Background())
			perfSetupErr = fmt.Errorf("insert discussion tags: %w", err)
			return
		}
		if err := tx.Commit(context.Background()); err != nil {
			perfSetupErr = fmt.Errorf("commit discussion events: %w", err)
			return
		}
		if _, err := copyConn.Exec(context.Background(), "ALTER TABLE "+eventsTable+" ENABLE TRIGGER USER"); err != nil {
			perfSetupErr = fmt.Errorf("enable fixture triggers: %w", err)
			return
		}
		t.Logf("Seeded %d events in %v", perfNumEvents, time.Since(start))

		// Let the planner know about the new data.
		db.Exec(fmt.Sprintf("ANALYZE %s", eventsTable))
		db.Exec(fmt.Sprintf("ANALYZE %s", tagsTable))

		perfStore = store
	})

	if perfSetupErr != nil {
		t.Fatalf("Perf data seeding failed: %v", perfSetupErr)
	}
	return perfStore
}

// ---------- helpers ----------

// explainAnalyze runs EXPLAIN ANALYZE for the query built by buildSelectQuery
// and returns the full plan text.
func explainAnalyze(t *testing.T, store *EventStore, filter nostr.Filter) string {
	t.Helper()
	qb, err := store.buildSelectQuery(filter)
	if err != nil {
		t.Fatalf("buildSelectQuery: %v", err)
	}
	sql, args, err := qb.ToSql()
	if err != nil {
		t.Fatalf("ToSql: %v", err)
	}

	rows, err := GetDb().Query("EXPLAIN ANALYZE "+sql, args...)
	if err != nil {
		t.Fatalf("EXPLAIN ANALYZE failed: %v", err)
	}
	defer rows.Close()

	var plan strings.Builder
	for rows.Next() {
		var line string
		rows.Scan(&line)
		plan.WriteString(line + "\n")
	}
	return plan.String()
}

// assertNoSeqScanOnEvents fails if the plan contains a sequential scan on the
// events table.
func assertNoSeqScanOnEvents(t *testing.T, plan, eventsTable string) {
	t.Helper()
	lower := strings.ToLower(plan)
	marker := "seq scan on " + strings.ToLower(eventsTable)
	if strings.Contains(lower, marker) {
		t.Errorf("Plan contains sequential scan on events table:\n%s", plan)
	}
}

// timeQuery runs a filter through QueryEvents, collects results up to a cap,
// returns the count and wall-clock duration.
func timeQuery(store *EventStore, filter nostr.Filter, cap int) (int, time.Duration) {
	start := time.Now()
	n := 0
	for range store.QueryEvents(filter, cap) {
		n++
	}
	return n, time.Since(start)
}

// ---------- test ----------

func TestIntegration_QueryPerformance(t *testing.T) {
	// Start a metrics HTTP server so an external scraper (e.g. Grafana Alloy)
	// can collect query duration histograms during the test run.
	// Set METRICS_PORT to enable (e.g. METRICS_PORT=9090).
	if port := os.Getenv("METRICS_PORT"); port != "" {
		srv := &http.Server{
			Addr:    ":" + port,
			Handler: promhttp.Handler(),
		}
		srvErr := make(chan error, 1)
		go func() {
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				srvErr <- err
			}
		}()
		defer srv.Close()
		// Give the server a moment to fail on bind errors
		time.Sleep(50 * time.Millisecond)
		select {
		case err := <-srvErr:
			t.Fatalf("Metrics server failed to start: %v", err)
		default:
		}
		t.Logf("Metrics server listening on :%s", port)
	}

	store := seedPerfData(t)
	eventsTable := store.Schema.Prefix("events")

	// Keep the concurrent tag-first sort in memory. Docker's small writable
	// layer otherwise fills with one temporary sort file per simulated client,
	// which tests container disk capacity rather than query latency.
	db := GetDb()
	if _, err := db.Exec("ALTER SYSTEM SET work_mem = '32MB'"); err != nil {
		t.Fatalf("configure performance work_mem: %v", err)
	}
	if _, err := db.Exec("SELECT pg_reload_conf()"); err != nil {
		t.Fatalf("reload performance work_mem: %v", err)
	}
	db.SetMaxIdleConns(0)
	db.SetMaxIdleConns(5)

	// Each group has perfNumEvents/perfNumGroups = 50K events.
	eventsPerGroup := perfNumEvents / perfNumGroups

	// ── 1. Tag filter only (the root-cause query pattern) ────────────
	t.Run("TagFilter", func(t *testing.T) {
		filter := nostr.Filter{
			Tags:  nostr.TagMap{"h": []string{"general"}},
			Limit: 1000,
		}

		plan := explainAnalyze(t, store, filter)
		t.Logf("Plan:\n%s", plan)
		assertNoSeqScanOnEvents(t, plan, eventsTable)

		n, dur := timeQuery(store, filter, 1000)
		t.Logf("Returned %d rows in %v", n, dur)
		if n != 1000 {
			t.Errorf("Expected 1000 results, got %d", n)
		}
		if dur > 2*time.Second {
			t.Errorf("Query too slow: %v (want < 2s)", dur)
		}
	})

	// ── 2. Kind + tag filter (exact production query) ────────────────
	t.Run("KindTagFilter", func(t *testing.T) {
		filter := nostr.Filter{
			Kinds: []nostr.Kind{9},
			Tags:  nostr.TagMap{"h": []string{"general"}},
			Limit: 1000,
		}

		plan := explainAnalyze(t, store, filter)
		t.Logf("Plan:\n%s", plan)
		assertNoSeqScanOnEvents(t, plan, eventsTable)

		n, dur := timeQuery(store, filter, 1000)
		t.Logf("Returned %d rows in %v", n, dur)
		if n != 1000 {
			t.Errorf("Expected 1000 results, got %d", n)
		}
		if dur > 2*time.Second {
			t.Errorf("Query too slow: %v (want < 2s)", dur)
		}
	})

	// ── 3. Kind + tag + Since (time-bounded) ─────────────────────────
	t.Run("KindTagSinceFilter", func(t *testing.T) {
		// Pick a Since that gives us roughly half the events in a group.
		midpoint := nostr.Timestamp(1_700_000_000 + int64(perfNumEvents/2))
		filter := nostr.Filter{
			Kinds: []nostr.Kind{9},
			Tags:  nostr.TagMap{"h": []string{"dev"}},
			Since: midpoint,
			Limit: 1000,
		}

		plan := explainAnalyze(t, store, filter)
		t.Logf("Plan:\n%s", plan)
		assertNoSeqScanOnEvents(t, plan, eventsTable)

		n, dur := timeQuery(store, filter, 1000)
		t.Logf("Returned %d rows in %v", n, dur)
		if n == 0 {
			t.Error("Expected results, got 0")
		}
		if dur > 2*time.Second {
			t.Errorf("Query too slow: %v (want < 2s)", dur)
		}
	})

	// ── 4. Multiple tag values (h IN ('general', 'random')) ──────────
	t.Run("MultipleTagValues", func(t *testing.T) {
		filter := nostr.Filter{
			Kinds: []nostr.Kind{9},
			Tags:  nostr.TagMap{"h": []string{"general", "random"}},
			Limit: 1000,
		}

		plan := explainAnalyze(t, store, filter)
		t.Logf("Plan:\n%s", plan)
		assertNoSeqScanOnEvents(t, plan, eventsTable)

		n, dur := timeQuery(store, filter, 1000)
		t.Logf("Returned %d rows in %v", n, dur)
		if n != 1000 {
			t.Errorf("Expected 1000 results, got %d", n)
		}
		if dur > 2*time.Second {
			t.Errorf("Query too slow: %v (want < 2s)", dur)
		}
	})

	// ── 5. Author + kind (no tag filter — tests non-tag path) ───────
	t.Run("AuthorKindFilter", func(t *testing.T) {
		// Query by author using raw SQL to avoid nostr pubkey validation.
		// Each pubkey appears in perfNumEvents/perfNumMembers = 50 events.
		expected := perfNumEvents / perfNumMembers
		pubkey := perfPubkeys[0]

		start := time.Now()
		var n int
		rows, err := GetDb().Query(
			fmt.Sprintf(
				"SELECT id FROM %s WHERE kind = $1 AND pubkey = $2 ORDER BY created_at DESC LIMIT $3",
				store.Schema.Prefix("events"),
			), 9, pubkey, 100,
		)
		if err != nil {
			t.Fatalf("Query failed: %v", err)
		}
		for rows.Next() {
			var id string
			rows.Scan(&id)
			n++
		}
		rows.Close()
		dur := time.Since(start)

		t.Logf("Returned %d rows in %v", n, dur)
		if n != expected && n != 100 {
			t.Errorf("Expected %d or 100 (capped) results, got %d", expected, n)
		}
		if dur > 2*time.Second {
			t.Errorf("Query too slow: %v (want < 2s)", dur)
		}
	})

	// ── 6. COUNT with tag filter ─────────────────────────────────────
	t.Run("CountTagFilter", func(t *testing.T) {
		filter := nostr.Filter{
			Kinds: []nostr.Kind{9},
			Tags:  nostr.TagMap{"h": []string{"general"}},
		}

		start := time.Now()
		count, err := store.CountEvents(filter)
		dur := time.Since(start)
		if err != nil {
			t.Fatalf("CountEvents: %v", err)
		}
		t.Logf("COUNT = %d in %v", count, dur)

		if int(count) != eventsPerGroup {
			t.Errorf("Expected count %d, got %d", eventsPerGroup, count)
		}
		if dur > 5*time.Second {
			t.Errorf("Count too slow: %v (want < 5s)", dur)
		}
	})

	t.Run("BoundedNewestRegistryPageAndExactCount", func(t *testing.T) {
		filter := nostr.Filter{
			Kinds: []nostr.Kind{11, 1111},
			Tags:  nostr.TagMap{"h": {"registry-discussion"}},
			Limit: 20,
		}
		start := time.Now()
		var posts int
		for range store.QueryEvents(filter, 20) {
			posts++
		}
		if posts != 20 {
			t.Fatalf("newest Registry page returned %d posts", posts)
		}
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Fatalf("newest Registry page took %v", elapsed)
		}
		count, err := store.CountEvents(filter)
		if err != nil || count != 260 {
			t.Fatalf("Registry count = %d, %v", count, err)
		}
	})

	t.Run("BroadSafeCountUsesPlannerEstimate", func(t *testing.T) {
		config := *store.Config
		config.Policy.Open = true
		config.Policy.AnonymousRead = true
		groups := &GroupStore{Config: &config, Events: store, cachesWarmed: true}
		for _, group := range []string{"general", "random"} {
			groups.metadataCache.Store(group, &groupMetaCache{
				found: true,
				event: nostr.Event{Kind: nostr.KindSimpleGroupMetadata, Tags: nostr.Tags{{"d", group}}},
			})
		}
		instance := &Instance{Config: &config, Events: store, Groups: groups}
		result, err := instance.CountStored(context.Background(), []nostr.Filter{{
			Kinds: []nostr.Kind{9},
			Tags:  nostr.TagMap{"h": {"general", "random"}},
		}})
		if err != nil {
			t.Fatal(err)
		}
		if !result.Approximate || result.Count <= exactCountEstimateThreshold {
			t.Fatalf("broad count = %#v, want planner estimate above %d", result, exactCountEstimateThreshold)
		}
	})

	// ── 7. Correctness: result ordering ──────────────────────────────
	t.Run("ResultsOrderedDescending", func(t *testing.T) {
		filter := nostr.Filter{
			Kinds: []nostr.Kind{9},
			Tags:  nostr.TagMap{"h": []string{"feedback"}},
			Limit: 500,
		}

		var prev nostr.Timestamp
		first := true
		for evt := range store.QueryEvents(filter, 500) {
			if first {
				prev = evt.CreatedAt
				first = true
			} else {
				if evt.CreatedAt > prev {
					t.Fatalf("Results not ordered DESC: %d came after %d",
						evt.CreatedAt, prev)
				}
				prev = evt.CreatedAt
			}
		}
	})

	// ── 8. Concurrent queries (simulate 20 connected clients) ────────
	t.Run("ConcurrentClients", func(t *testing.T) {
		const numClients = 20
		var wg sync.WaitGroup
		errors := make(chan error, numClients)

		for i := 0; i < numClients; i++ {
			wg.Add(1)
			go func(clientID int) {
				defer wg.Done()
				group := perfGroups[clientID%perfNumGroups]
				filter := nostr.Filter{
					Kinds: []nostr.Kind{9},
					Tags:  nostr.TagMap{"h": []string{group}},
					Limit: 1000,
				}

				start := time.Now()
				n := 0
				for range store.QueryEvents(filter, 1000) {
					n++
				}
				dur := time.Since(start)

				if n != 1000 {
					errors <- fmt.Errorf("client %d: expected 1000 results, got %d", clientID, n)
					return
				}
				if dur > 5*time.Second {
					errors <- fmt.Errorf("client %d: query took %v (want < 5s)", clientID, dur)
					return
				}
			}(i)
		}

		wg.Wait()
		close(errors)

		for err := range errors {
			t.Error(err)
		}
	})

	// ── 9. No-tag query still works (no JOIN path) ───────────────────
	t.Run("NoTagFilter", func(t *testing.T) {
		filter := nostr.Filter{
			Kinds: []nostr.Kind{9},
			Limit: 100,
		}

		plan := explainAnalyze(t, store, filter)
		t.Logf("Plan:\n%s", plan)

		n, dur := timeQuery(store, filter, 100)
		t.Logf("Returned %d rows in %v", n, dur)
		if n != 100 {
			t.Errorf("Expected 100 results, got %d", n)
		}
		if dur > 2*time.Second {
			t.Errorf("Query too slow: %v (want < 2s)", dur)
		}
	})

	// ── 10. Hold metrics server open for scraping ─────────────────────
	if wait := os.Getenv("METRICS_WAIT"); wait != "" {
		dur, err := time.ParseDuration(wait)
		if err != nil {
			dur = 2 * time.Minute
		}
		t.Logf("Holding metrics server open for %v (METRICS_WAIT=%s)", dur, wait)
		time.Sleep(dur)
	}
}
