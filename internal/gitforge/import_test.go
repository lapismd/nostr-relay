package gitforge

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseRefOutputAcceptsHeadsTagsAndIgnoresPeeledTags(t *testing.T) {
	sha1 := strings.Repeat("a", 40)
	sha256 := strings.Repeat("b", 64)
	refs, err := parseRefOutput([]byte(
		sha1 + "\trefs/heads/main\n" +
			sha256 + " refs/tags/v1\n" +
			strings.Repeat("c", 40) + "\trefs/tags/v1^{}\n",
	))
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		"refs/heads/main": sha1,
		"refs/tags/v1":    sha256,
	}, refs)
}

func TestParseRefOutputRejectsMalformedOrUnexpectedRefs(t *testing.T) {
	for _, raw := range []string{
		"not-a-sha refs/heads/main\n",
		strings.Repeat("a", 40) + " refs/pull/1/head\n",
		strings.Repeat("a", 40) + " refs/heads/main extra\n",
	} {
		_, err := parseRefOutput([]byte(raw))
		require.Error(t, err, raw)
	}
}

func TestEqualRefsIsCaseInsensitiveAndExact(t *testing.T) {
	sha := strings.Repeat("a", 40)
	require.True(t, equalRefs(
		map[string]string{"refs/heads/main": strings.ToUpper(sha)},
		map[string]string{"refs/heads/main": sha},
	))
	require.False(t, equalRefs(
		map[string]string{"refs/heads/main": sha},
		map[string]string{"refs/heads/other": sha},
	))
}

func TestWalGitBackendRejectsHeaderInjectionInRelayToken(t *testing.T) {
	_, err := NewWalGitBackend("http://walgit:8080", "secret\r\nX-Leak: yes")
	require.ErrorContains(t, err, "invalid WalGit relay token")
}
