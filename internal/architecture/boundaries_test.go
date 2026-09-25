package architecture

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestPackageBoundaries enforces the ownership boundaries documented in
// spec/src/architecture.md as a rank ordering: an import must point from a
// package closer to the process entry point toward a more foundational one. A
// horizontal edge between packages of equal rank inverts that ownership and must
// be recorded in allowlist.json with a reason.
func TestPackageBoundaries(t *testing.T) {
	graph := loadImportGraph(t).Internal
	allowlist := loadAllowlistOrFail(t)

	exercised := map[string]bool{}
	for _, from := range sortedKeys(graph) {
		fromRank, err := rankFor(from)
		if err != nil {
			t.Error(err)
			continue
		}

		for _, to := range graph[from] {
			toRank, err := rankFor(to)
			if err != nil {
				t.Error(err)
				continue
			}

			switch {
			case isTooling(from) || isTooling(to):
				t.Errorf("%s imports %s; repository check packages must stay isolated from product code", from, to)
			case toRank > fromRank:
				// Downward edge: permitted by the layer ordering.
			case isVendored(from) && isVendored(to):
				// Inside the vendored Khatru tree. scripts/update-khatru.sh
				// regenerates these files, so this matrix does not govern them.
			default:
				key := edgeKey(from, to)
				entry, allowed := allowlist[key]
				if !allowed {
					t.Errorf("%s imports %s horizontally or upward; either move the shared code to a leaf or record the edge in internal/architecture/allowlist.json with a reason", from, to)
					continue
				}
				exercised[key] = true
				t.Logf("allowlisted edge %s: %s", key, entry.Reason)
			}
		}
	}

	assertAllowlistIsExact(t, allowlist, exercised)
}

// assertAllowlistIsExact makes the allowlist shrink-only. An entry that no longer
// corresponds to a real edge, or that has become a legitimate downward edge, is
// dead weight that would let a future inversion slip through unnoticed.
func assertAllowlistIsExact(t *testing.T, allowlist Allowlist, exercised map[string]bool) {
	t.Helper()

	for _, key := range sortedKeys(allowlist) {
		entry := allowlist[key]
		if !exercised[key] {
			t.Errorf("allowlist.json records %s but no package imports it that way; remove the entry, or land the move that retires it", key)
			continue
		}
		fromRank, fromErr := rankFor(entry.From)
		toRank, toErr := rankFor(entry.To)
		if fromErr != nil || toErr != nil {
			if fromErr != nil {
				t.Error(fromErr)
			} else {
				t.Error(toErr)
			}
			continue
		}
		if toRank > fromRank {
			t.Errorf("allowlist.json records %s, which is already a permitted downward edge; remove the entry", key)
		}
	}
}

// TestNoImportCycles proves the internal package graph is acyclic across both
// modules. A cycle would defeat the rank ordering and make the layering
// unenforceable.
func TestNoImportCycles(t *testing.T) {
	if cycle := findCycle(loadImportGraph(t).Internal); cycle != nil {
		t.Errorf("import cycle: %s", strings.Join(cycle, " -> "))
	}
}

// TestEveryPackageHasARank makes a new package land deliberately. Without this an
// unranked directory would simply produce no edges and escape the matrix.
func TestEveryPackageHasARank(t *testing.T) {
	dirs := loadPackageDirs(t)

	for _, dir := range sortedKeys(dirs) {
		if _, err := rankFor(dir); err != nil {
			t.Error(err)
		}
	}

	for dir := range packageRanks {
		if _, ok := dirs[dir]; !ok {
			t.Errorf("packageRanks lists %s, which has no .go files; remove the entry", dir)
		}
	}
	t.Logf("%d packages ranked", len(dirs))
}

// providerSDKs are third-party credential and identity SDKs that must not escape
// internal/auth. spec/src/architecture.md promises provider types do not leave
// their adapter, and UPSTREAM.md repeats it for the GitHub boundary.
var providerSDKs = []string{
	"golang.org/x/oauth2",
	"github.com/coreos/go-oidc",
	"github.com/go-webauthn",
}

// TestProviderSDKContainment keeps credential-provider SDKs inside the package
// that adapts them, so the rest of the tree depends on provider-neutral types.
//
// This covers third-party SDKs only. Containment of internal/githubforge response
// types is a type-level promise an import graph cannot express, because the zooid
// composition root legitimately wires that backend.
func TestProviderSDKContainment(t *testing.T) {
	external := loadImportGraph(t).External

	const owner = "internal/auth"
	for _, dir := range sortedKeys(external) {
		if dir == owner || strings.HasPrefix(dir, owner+"/") {
			continue
		}
		for _, importPath := range external[dir] {
			for _, sdk := range providerSDKs {
				if matchesPrefix(importPath, sdk) {
					t.Errorf("%s imports %s, which must stay inside %s; depend on provider-neutral types instead", dir, importPath, owner)
				}
			}
		}
	}
}

// matchesPrefix reports whether an import path is the prefix itself or a package
// beneath it.
func matchesPrefix(importPath, prefix string) bool {
	return importPath == prefix || strings.HasPrefix(importPath, prefix+"/")
}

func loadImportGraph(t *testing.T) *ImportGraph {
	t.Helper()
	root := repositoryRoot(t)
	modules, err := findModules(root)
	if err != nil {
		t.Fatal(err)
	}
	dirs, err := packageDirs(root, modules)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := collectImportGraph(root, dirs, modules)
	if err != nil {
		t.Fatal(err)
	}
	return graph
}

func loadPackageDirs(t *testing.T) map[string]string {
	t.Helper()
	root := repositoryRoot(t)
	modules, err := findModules(root)
	if err != nil {
		t.Fatal(err)
	}
	dirs, err := packageDirs(root, modules)
	if err != nil {
		t.Fatal(err)
	}
	return dirs
}

func loadAllowlistOrFail(t *testing.T) Allowlist {
	t.Helper()
	path := filepath.Join(repositoryRoot(t), "internal", "architecture", "allowlist.json")
	allowlist, err := loadAllowlist(path)
	if err != nil {
		t.Fatal(err)
	}
	return allowlist
}
