package architecture

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Budgets in counted lines. A Lapis-authored production file that is not
// baselined must stay at or under NewProductionBudget; a test file at or under
// NewTestBudget. A file that is not baselined and exceeds HardCeiling fails even
// when it is a test file. A baselined file may only shrink: it fails when it
// grows past its recorded budget, and it fails when the budget is stale.
const (
	NewProductionBudget = 300
	NewTestBudget       = 500
	HardCeiling         = 500

	// BaselineRecordFloor is the counted size at or below which a file carries no
	// recorded budget. It equals the production budget because that is the size
	// every in-scope file is driven toward, including test files. A test file
	// between 301 and 500 is inside its NewTestBudget, so the gate would not
	// otherwise notice it; recording it anyway turns that band into ratcheted
	// debt that can only shrink.
	BaselineRecordFloor = 300

	// BaselineSlack lets a recorded budget be lowered when a file shrinks far
	// enough that current size plus this slack is still under the budget.
	BaselineSlack = 20
)

// UpstreamPin is the Unicity commit recorded in UPSTREAM.md. Paths present in
// that commit are inherited rather than Lapis-authored and stay out of scope.
const UpstreamPin = "6c89860dbb1fd2d5cb628cf3978c78cb2dbb7999"

// Scope names the tree this package governs, relative to the repository root.
type Scope struct {
	// InScope holds every .go file subject to the budgets.
	InScope []string
	// Upstream holds .go files inherited from UpstreamPin.
	Upstream []string
	// Vendored holds .go files under the vendored Khatru tree.
	Vendored []string
}

// excludedDir reports whether a path component is never walked. Generated
// output, fixtures, third-party archives, and version-control metadata are all
// out of scope.
func excludedDir(name string) bool {
	switch name {
	case ".git", ".jj", "node_modules", "vendor", "third_party", "spec", "testdata":
		return true
	}
	return false
}

// loadScope walks the repository root and classifies every .go file. The
// upstream manifest and the Khatru vendor tree are read from disk so the
// classification cannot silently drift from scripts/update-khatru.sh.
func loadScope(root string, upstream []string) (*Scope, error) {
	upstreamSet := make(map[string]bool, len(upstream))
	for _, path := range upstream {
		upstreamSet[path] = true
	}

	scope := &Scope{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if excludedDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".go") {
			return nil
		}

		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)

		switch {
		case strings.HasPrefix(rel, "internal/khatru/"):
			scope.Vendored = append(scope.Vendored, rel)
		case upstreamSet[rel]:
			scope.Upstream = append(scope.Upstream, rel)
		default:
			scope.InScope = append(scope.InScope, rel)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	for _, list := range [][]string{scope.InScope, scope.Upstream, scope.Vendored} {
		sort.Strings(list)
	}
	return scope, nil
}

// isTestFile reports whether a path names a Go test file.
func isTestFile(path string) bool {
	return strings.HasSuffix(filepath.Base(path), "_test.go")
}

// loadUpstreamPaths reads the generated manifest of .go files inherited from
// UpstreamPin. Blank lines and "#" comments are ignored.
func loadUpstreamPaths(path string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var paths []string
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !strings.HasSuffix(trimmed, ".go") {
			return nil, fmt.Errorf("%s: %q is not a .go path", filepath.Base(path), trimmed)
		}
		paths = append(paths, trimmed)
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("%s contains no upstream paths", filepath.Base(path))
	}
	sort.Strings(paths)
	return paths, nil
}

// countedFile reads a repository-relative path and reports its counted size.
func countedFile(root, rel string) (int, error) {
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return 0, err
	}
	counted, err := CountedLines(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", rel, err)
	}
	return counted, nil
}
