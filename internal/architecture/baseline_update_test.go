package architecture

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// TestWriteBaseline regenerates baseline.json from the current tree. It is the
// writer behind scripts/update-architecture-baseline.sh and is skipped during a
// normal test run.
//
// Regeneration only ever lowers a recorded budget or drops a resolved entry. A
// file that is over budget without a recorded entry is reported as a failure
// unless ARCH_BASELINE_ALLOW_NEW=1, which is reserved for bootstrapping the gate
// and for a deliberate, reviewed decision to record new debt. This is what stops
// a change from dodging the 300-line new-file budget by recording an allowance.
//
// Environment:
//   - ARCH_BASELINE_UPDATE=1    run the writer instead of skipping
//   - ARCH_BASELINE_OUT=path    write here instead of the committed baseline
//   - ARCH_BASELINE_ALLOW_NEW=1 permit budgets absent from the committed baseline
func TestWriteBaseline(t *testing.T) {
	if os.Getenv("ARCH_BASELINE_UPDATE") == "" {
		t.Skip("set ARCH_BASELINE_UPDATE=1 to regenerate baseline.json")
	}

	root := repositoryRoot(t)
	committedPath := filepath.Join(root, "internal", "architecture", "baseline.json")

	upstream, err := loadUpstreamPaths(filepath.Join(root, "internal", "architecture", "upstream-paths.txt"))
	if err != nil {
		t.Fatal(err)
	}
	scope, err := loadScope(root, upstream)
	if err != nil {
		t.Fatal(err)
	}

	committed := &Baseline{Production: map[string]int{}, Test: map[string]int{}}
	if _, err := os.Stat(committedPath); err == nil {
		loaded, err := loadBaseline(committedPath)
		if err != nil {
			t.Fatal(err)
		}
		committed = loaded
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}

	allowNew := os.Getenv("ARCH_BASELINE_ALLOW_NEW") == "1"
	out := &Baseline{Production: map[string]int{}, Test: map[string]int{}}
	var newOffenders, raised []string

	for _, rel := range scope.InScope {
		counted, err := countedFile(root, rel)
		if err != nil {
			t.Fatal(err)
		}

		entries, committedEntries := out.Production, committed.Production
		if isTestFile(rel) {
			entries, committedEntries = out.Test, committed.Test
		}
		// Record at the 300-line floor for both kinds so the 301-500 band is
		// visible as ratcheted debt even where a test file is still inside its
		// own 500-line budget.
		if counted <= BaselineRecordFloor {
			continue
		}

		previous, recorded := committedEntries[rel]
		if !recorded {
			newOffenders = append(newOffenders, rel)
			if allowNew {
				entries[rel] = counted
			}
			continue
		}
		if counted < previous {
			entries[rel] = counted
		} else {
			// Keep the recorded budget so a grown file still fails the gate
			// test instead of being laundered into a larger allowance.
			entries[rel] = previous
			if counted > previous {
				raised = append(raised, rel)
			}
		}
	}

	if len(newOffenders) > 0 && !allowNew {
		sort.Strings(newOffenders)
		for _, rel := range newOffenders {
			t.Errorf("%s is newly over budget; split it instead of recording an allowance (or pass ARCH_BASELINE_ALLOW_NEW=1 to record it deliberately)", rel)
		}
		t.FailNow()
	}

	outPath := committedPath
	if configured := os.Getenv("ARCH_BASELINE_OUT"); configured != "" {
		outPath = configured
	}
	if err := writeBaseline(outPath, out); err != nil {
		t.Fatal(err)
	}

	sort.Strings(raised)
	for _, rel := range raised {
		t.Logf("%s grew past its recorded budget; the budget was held so the gate still fails", rel)
	}
	t.Logf("wrote %s: %d production and %d test budgets", outPath, len(out.Production), len(out.Test))
}
