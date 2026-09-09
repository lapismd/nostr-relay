package forge

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProviderNeutralResultsExposeAdditiveCompletenessMetadata(t *testing.T) {
	limits := ContentLimits{MaximumChangedFiles: 300, MaximumTreeEntries: 1000}
	result := Diff{
		Base: "base", Head: "head", Files: []DiffFile{}, Patch: "",
		ResultMetadata: ResultMetadata{
			Truncated: true, Incomplete: []string{"changed-files", "patch"},
			LimitExceeded:    &LimitExceeded{Resource: "changed_files", Limit: 300, Actual: 300},
			CapabilityLimits: &limits,
		},
	}
	raw, err := json.Marshal(result)
	require.NoError(t, err)
	require.JSONEq(t, `{
		"base":"base","head":"head","files":[],"patch":"",
		"truncated":true,"incomplete":["changed-files","patch"],
		"limit_exceeded":{"resource":"changed_files","limit":300,"actual":300},
		"capability_limits":{"maximum_changed_files":300,"maximum_tree_entries":1000}
	}`, string(raw))
}

func TestTruncationWithoutContinuationMeansNotPageable(t *testing.T) {
	result := Tree{ResultMetadata: ResultMetadata{Truncated: true, Incomplete: []string{"entries"}}}
	raw, err := json.Marshal(result)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "continuation")
}
