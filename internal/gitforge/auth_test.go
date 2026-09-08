package gitforge

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
)

func signedHTTPAuth(t *testing.T, secret nostr.SecretKey, at time.Time, method, target string, extra ...nostr.Tag) string {
	t.Helper()
	event := nostr.Event{
		Kind: nostr.KindHTTPAuth, CreatedAt: nostr.Timestamp(at.Unix()),
		Tags: nostr.Tags{{"u", target}, {"method", method}},
	}
	event.Tags = append(event.Tags, extra...)
	require.NoError(t, event.Sign(secret))
	raw, err := event.MarshalJSON()
	require.NoError(t, err)
	return "Nostr " + base64.StdEncoding.EncodeToString(raw)
}

func TestVerifyGitAuthMatchesBuzzCredentialReuse(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	secret := nostr.MustSecretKeyFromHex(strings.Repeat("1", 64))
	root := "https://relay.example/git/" + secret.Public().Hex() + "/notes"
	authorization := signedHTTPAuth(t, secret, now, http.MethodGet, root)

	for _, endpoint := range []struct{ method, target string }{
		{http.MethodGet, root + "/info/refs?service=git-upload-pack"},
		{http.MethodPost, root + "/git-upload-pack"},
		{http.MethodPost, root + "/git-receive-pack"},
	} {
		request, err := http.NewRequest(endpoint.method, endpoint.target, nil)
		require.NoError(t, err)
		request.Header.Set("Authorization", authorization)
		event, err := VerifyGitAuth(request, root, now)
		require.NoError(t, err)
		require.Equal(t, secret.Public(), event.PubKey)
	}
}

func TestVerifyReadAuthBindsExactMethodAndURL(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	secret := nostr.MustSecretKeyFromHex(strings.Repeat("2", 64))
	target := "https://relay.example/api/git/" + secret.Public().Hex() + "/notes/tree/main"
	request, err := http.NewRequest(http.MethodGet, target, nil)
	require.NoError(t, err)
	request.Header.Set("Authorization", signedHTTPAuth(t, secret, now, http.MethodGet, target))
	_, err = VerifyReadAuth(request, target, now)
	require.NoError(t, err)

	request.Method = http.MethodPost
	_, err = VerifyReadAuth(request, target, now)
	require.ErrorContains(t, err, "method")
}

func TestGitAuthRejectsExpiredInvalidAndWrongRepository(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	secret := nostr.MustSecretKeyFromHex(strings.Repeat("3", 64))
	root := "https://relay.example/git/" + secret.Public().Hex() + "/notes"
	request, err := http.NewRequest(http.MethodGet, root+"/info/refs", nil)
	require.NoError(t, err)
	request.Header.Set("Authorization", signedHTTPAuth(t, secret, now.Add(-2*AuthWindow), http.MethodGet, root))
	_, err = VerifyGitAuth(request, root, now)
	require.ErrorContains(t, err, "time window")

	request.Header.Set("Authorization", signedHTTPAuth(t, secret, now, http.MethodGet, root+"-other"))
	_, err = VerifyGitAuth(request, root, now)
	require.ErrorContains(t, err, "URL")

	request.Header.Set("Authorization", "Nostr not-base64")
	_, err = VerifyGitAuth(request, root, now)
	require.Error(t, err)

	request.Header["Authorization"] = []string{
		signedHTTPAuth(t, secret, now, http.MethodGet, root),
		signedHTTPAuth(t, secret, now, http.MethodGet, root),
	}
	_, err = VerifyGitAuth(request, root, now)
	require.Error(t, err, "ambiguous authorization headers must fail closed")

	request.Header.Set("Authorization", "Nostr "+strings.Repeat("a", maxAuthorizationHeaderBytes))
	_, err = VerifyGitAuth(request, root, now)
	require.Error(t, err, "oversized credentials must be rejected before decoding")
}
