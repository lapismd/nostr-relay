package projecttask

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseOwnersDocumentLongestPathWins(t *testing.T) {
	owner := "11" + zeroHex(62)
	docs := "22" + zeroHex(62)
	document := ParseOwnersDocument("# comment\n* " + owner + "\ndocs/ " + docs + "\n")
	require.Equal(t, []string{owner}, document.OwnersForPath("README.md"))
	require.Equal(t, []string{docs}, document.OwnersForPath("docs/guide.md"))
	require.Equal(t, []OwnersRule{{Path: "docs/", Pubkeys: []string{docs}}}, document.RequiredGroups([]string{"docs/guide.md", "docs/api.md"}))
}

func TestParseOwnersDocumentFailsClosed(t *testing.T) {
	require.Empty(t, ParseOwnersDocument("* not-a-key").Rules)
	require.Empty(t, ParseOwnersDocument("docs/\n").Rules)
}

func zeroHex(count int) string {
	out := make([]byte, count)
	for i := range out {
		out[i] = '0'
	}
	return string(out)
}
