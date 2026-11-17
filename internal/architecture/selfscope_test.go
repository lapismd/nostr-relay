package architecture

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestCheckerDoesNotExemptItself guards two ways this gate could quietly stop
// governing its own package: adding internal/architecture to the exclusions, and
// recording a baseline allowance for one of its own files. TestFileSizeBudgets
// already enforces the budgets over every in-scope path, so this test only
// asserts the scope and baseline membership.
func TestCheckerDoesNotExemptItself(t *testing.T) {
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

	own := make(map[string]bool)
	for _, rel := range scope.InScope {
		if strings.HasPrefix(rel, "internal/architecture/") {
			own[rel] = true
		}
	}
	if len(own) == 0 {
		t.Fatal("no internal/architecture files are in scope; the gate would be exempting itself")
	}

	for _, list := range [][]string{scope.Upstream, scope.Vendored} {
		for _, path := range list {
			if strings.HasPrefix(path, "internal/architecture/") {
				t.Errorf("%s is excluded from the gate; this package must stay in scope", path)
			}
		}
	}

	for path := range own {
		if _, baselined := baselineEntry(baseline, path); baselined {
			t.Errorf("%s has a recorded budget allowance; the checker must meet the %d-line budget itself", path, NewProductionBudget)
		}
	}
}
