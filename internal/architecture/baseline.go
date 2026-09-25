package architecture

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Baseline records the counted size of an in-scope file that was already over
// budget when the gate landed. Entries may only be lowered or removed; a file
// that shrinks below its recorded budget by more than BaselineSlack must have
// the budget lowered in the same change.
//
// On disk the budgets are ordered key/value pairs rather than a JSON object so
// regeneration produces a stable, reviewable diff.
type Baseline struct {
	Production map[string]int
	Test       map[string]int
}

// baselineDocument is the on-disk shape of baseline.json.
type baselineDocument struct {
	Production []budgetEntry `json:"production"`
	Test       []budgetEntry `json:"test"`
}

type budgetEntry struct {
	Path   string `json:"path"`
	Budget int    `json:"budget"`
}

func loadBaseline(path string) (*Baseline, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var document baselineDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("parse %s: %w", filepath.Base(path), err)
	}

	for _, group := range []struct {
		kind  string
		items []budgetEntry
	}{{"production", document.Production}, {"test", document.Test}} {
		seen := make(map[string]bool, len(group.items))
		for _, item := range group.items {
			switch {
			case item.Path == "":
				return nil, fmt.Errorf("%s: %s budget has an empty path", filepath.Base(path), group.kind)
			case item.Budget <= 0:
				return nil, fmt.Errorf("%s: %s budget %d is not positive", filepath.Base(path), item.Path, item.Budget)
			case seen[item.Path]:
				return nil, fmt.Errorf("%s: %s is listed twice under %s", filepath.Base(path), item.Path, group.kind)
			}
			seen[item.Path] = true
		}
	}

	return &Baseline{
		Production: budgetsFromEntries(document.Production),
		Test:       budgetsFromEntries(document.Test),
	}, nil
}

func budgetsFromEntries(entries []budgetEntry) map[string]int {
	out := make(map[string]int, len(entries))
	for _, entry := range entries {
		out[entry.Path] = entry.Budget
	}
	return out
}

// writeBaseline serialises a baseline with sorted keys.
func writeBaseline(path string, baseline *Baseline) error {
	document := baselineDocument{
		Production: sortedBudgets(baseline.Production),
		Test:       sortedBudgets(baseline.Test),
	}
	raw, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}

func sortedBudgets(entries map[string]int) []budgetEntry {
	out := make([]budgetEntry, 0, len(entries))
	for path, budget := range entries {
		out = append(out, budgetEntry{Path: path, Budget: budget})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}
