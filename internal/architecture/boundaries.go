package architecture

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Layer ranks encode the ownership boundaries in spec/src/architecture.md. A
// lower rank sits closer to the process entry point; a higher rank is more
// foundational. An import edge is permitted when it points strictly downward.
//
// A horizontal edge between two packages of equal rank is inverted relative to
// the documented ownership and must be recorded in allowlist.json with a reason.
// The allowlist may only shrink, which TestBoundaryAllowlistIsExact enforces.
const (
	rankBinaries = -1 // cmd/* executables
	rankZooid    = 0  // composition root: relay policy, persistence, HTTP routes
	rankAdapter  = 1  // relay-facing adapters
	rankProvider = 2  // provider and credential adapters
	rankLeaf     = 3  // storage-independent domain leaves
	rankProtocol = 4  // wire-format rules with no Lapis dependencies
	rankVendor   = 5  // vendored upstream transport
	rankTooling  = 6  // repository checks; isolated from product code
)

// packageRanks maps a package directory to its layer rank. A directory absent
// from this map fails the boundary test, so a new package must be placed
// deliberately instead of inheriting a default.
var packageRanks = map[string]int{
	"cmd/forge-worker": rankBinaries,
	"cmd/local-admin":  rankBinaries,
	"cmd/migrate":      rankBinaries,
	"cmd/relay":        rankBinaries,

	"zooid": rankZooid,

	"internal/agentbunker": rankAdapter,
	"internal/blossom":     rankAdapter,
	"internal/buzzcompat":  rankAdapter,

	"internal/auth":             rankProvider,
	"internal/gitforge":         rankProvider,
	"internal/githubforge":      rankProvider,
	"modules/buzzagents/auth":   rankProvider,
	"modules/buzzagents/policy": rankProvider,

	"internal/forge":              rankLeaf,
	"internal/keywrap":            rankLeaf,
	"internal/presence":           rankLeaf,
	"internal/projecttask":        rankLeaf,
	"modules/buzzagents/presence": rankLeaf,

	"modules/buzzagents/protocol": rankProtocol,

	"internal/khatru":         rankVendor,
	"internal/khatru/blossom": rankVendor,

	"internal/architecture": rankTooling,
	"internal/specdocs":     rankTooling,
}

// toolingPackages are repository checks. They must not import product code and
// must not be imported by it, so their isolation is asserted separately from the
// rank ordering.
var toolingPackages = []string{"internal/architecture", "internal/specdocs"}

// AllowlistEntry records one permitted horizontal edge.
type AllowlistEntry struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Reason string `json:"reason"`
}

type allowlistDocument struct {
	Edges []AllowlistEntry `json:"edges"`
}

// Allowlist holds the permitted horizontal edges keyed by "from -> to".
type Allowlist map[string]AllowlistEntry

func loadAllowlist(path string) (Allowlist, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var document allowlistDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("parse %s: %w", filepath.Base(path), err)
	}

	out := make(Allowlist, len(document.Edges))
	for _, entry := range document.Edges {
		switch {
		case entry.From == "" || entry.To == "":
			return nil, fmt.Errorf("%s: an edge has an empty from or to", filepath.Base(path))
		case strings.TrimSpace(entry.Reason) == "":
			return nil, fmt.Errorf("%s: edge %s needs a reason", filepath.Base(path), edgeKey(entry.From, entry.To))
		}
		key := edgeKey(entry.From, entry.To)
		if _, duplicate := out[key]; duplicate {
			return nil, fmt.Errorf("%s: edge %s is listed twice", filepath.Base(path), key)
		}
		out[key] = entry
	}
	return out, nil
}

func edgeKey(from, to string) string {
	return from + " -> " + to
}

// rankFor returns the layer rank of a package directory.
func rankFor(dir string) (int, error) {
	rank, ok := packageRanks[dir]
	if !ok {
		return 0, fmt.Errorf("%s has no layer rank; add it to packageRanks in internal/architecture/boundaries.go", dir)
	}
	return rank, nil
}

// isTooling reports whether a package directory is a repository check.
func isTooling(dir string) bool {
	for _, tool := range toolingPackages {
		if dir == tool {
			return true
		}
	}
	return false
}

// isVendored reports whether a package directory belongs to the vendored Khatru
// tree. Edges inside that tree come from upstream and are governed by
// scripts/update-khatru.sh rather than by this matrix; only their isolation from
// Lapis product code is asserted here.
func isVendored(dir string) bool {
	return strings.HasPrefix(dir, "internal/khatru")
}
