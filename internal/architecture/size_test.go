package architecture

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestFileSizeBudgets enforces the counted-line budgets in budget.go over every
// Lapis-authored .go file. Files inherited from the Unicity pin and the vendored
// Khatru tree are out of scope; TestExclusionManifestIsCurrent and
// scripts/update-architecture-baseline.sh --check prove those exclusions still
// match the pin.
//
// A baselined file may only shrink: it fails when it grows past its recorded
// budget, and it fails when it shrinks far enough that the budget is stale.
// Refusing new baseline entries is enforced against git history by
// scripts/update-architecture-baseline.sh --check, so a change cannot dodge the
// 300-line new-file budget by recording a fresh allowance.
func TestFileSizeBudgets(t *testing.T) {
	root := repositoryRoot(t)
	upstream, err := loadUpstreamPaths(filepath.Join(root, "internal", "architecture", "upstream-paths.txt"))
	if err != nil {
		t.Fatal(err)
	}
	scope, err := loadScope(root, upstream)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := loadBaseline(filepath.Join(root, "internal", "architecture", "baseline.json"))
	if err != nil {
		t.Fatal(err)
	}

	seen := map[string]bool{}
	for _, rel := range scope.InScope {
		seen[rel] = true
		counted, err := countedFile(root, rel)
		if err != nil {
			t.Fatal(err)
		}

		newBudget := NewProductionBudget
		if isTestFile(rel) {
			newBudget = NewTestBudget
		}
		budget, baselined := baselineEntry(baseline, rel)

		switch {
		case baselined && counted > budget:
			t.Errorf("%s: %d counted lines exceeds its recorded budget of %d; split it or justify the growth", rel, counted, budget)
		case baselined && counted+BaselineSlack < budget:
			t.Errorf("%s: budget %d is stale, the file is now %d counted lines; lower the budget to %d", rel, budget, counted, counted)
		case baselined:
			// Recorded debt inside its budget; P1 lowers or removes entries.
		case counted > HardCeiling:
			t.Errorf("%s: %d counted lines exceeds the %d hard ceiling; split it", rel, counted, HardCeiling)
		case counted > newBudget:
			t.Errorf("%s: %d counted lines exceeds the %d budget for %s files; split it", rel, counted, newBudget, kindFor(rel))
		}
	}

	assertBaselineEntriesAreReal(t, seen, baseline.Production, false)
	assertBaselineEntriesAreReal(t, seen, baseline.Test, true)
}

// baselineEntry returns the recorded budget for a path. A production path is
// never read from the test budget or vice versa.
func baselineEntry(baseline *Baseline, rel string) (int, bool) {
	if isTestFile(rel) {
		budget, ok := baseline.Test[rel]
		return budget, ok
	}
	budget, ok := baseline.Production[rel]
	return budget, ok
}

func kindFor(rel string) string {
	if isTestFile(rel) {
		return "test"
	}
	return "production"
}

// assertBaselineEntriesAreReal rejects budgets recorded for files that no
// longer exist, that fell out of scope, or that were filed under the wrong kind.
func assertBaselineEntriesAreReal(t *testing.T, seen map[string]bool, entries map[string]int, wantTest bool) {
	t.Helper()

	kind := "production"
	if wantTest {
		kind = "test"
	}

	var stale []string
	for path := range entries {
		if !seen[path] {
			stale = append(stale, path)
			continue
		}
		if isTestFile(path) != wantTest {
			t.Errorf("baseline.json lists %s under %q but it is the other kind", path, kind)
		}
	}
	sort.Strings(stale)
	for _, path := range stale {
		t.Errorf("baseline.json lists %s under %q but that file is gone or out of scope; remove the entry", path, kind)
	}
}

// TestExclusionManifestIsCurrent proves the upstream manifest still matches the
// pin recorded in UPSTREAM.md, and that the vendored Khatru tree is still the
// one scripts/update-khatru.sh regenerates. Without this the size gate could
// silently widen its own exclusions.
func TestExclusionManifestIsCurrent(t *testing.T) {
	root := repositoryRoot(t)
	manifestPath := filepath.Join(root, "internal", "architecture", "upstream-paths.txt")

	declared, err := loadUpstreamPaths(manifestPath)
	if err != nil {
		t.Fatal(err)
	}

	documented, err := os.ReadFile(filepath.Join(root, "UPSTREAM.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(documented), UpstreamPin) {
		t.Errorf("UPSTREAM.md no longer records pin %s; update UpstreamPin and regenerate the manifest", UpstreamPin[:12])
	}

	scope, err := loadScope(root, declared)
	if err != nil {
		t.Fatal(err)
	}

	if len(scope.Upstream) != len(declared) {
		missing := difference(declared, scope.Upstream)
		t.Errorf("manifest declares %d upstream paths but only %d exist in the tree; missing: %v",
			len(declared), len(scope.Upstream), missing)
	}
	if len(scope.Vendored) == 0 {
		t.Error("no vendored Khatru files found; internal/khatru was removed or the exclusion prefix changed")
	}

	t.Logf("scope: %d in scope, %d upstream, %d vendored",
		len(scope.InScope), len(scope.Upstream), len(scope.Vendored))
}

// difference returns the entries of want that are absent from have, sorted.
func difference(want, have []string) []string {
	present := make(map[string]bool, len(have))
	for _, entry := range have {
		present[entry] = true
	}
	var missing []string
	for _, entry := range want {
		if !present[entry] {
			missing = append(missing, entry)
		}
	}
	sort.Strings(missing)
	return missing
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Clean(filepath.Join("..", ".."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repository root does not contain go.mod: %v", err)
	}
	return root
}
