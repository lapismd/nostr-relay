package githubforge

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type mapCredentials map[string][]byte

func (credentials mapCredentials) Load(reference string) ([]byte, error) {
	return credentials[reference], nil
}

func testPrivateKey(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}

func TestInstallationTokenIsRepositoryAndPermissionNarrowedAndOpaque(t *testing.T) {
	key := testPrivateKey(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		require.Equal(t, "/app/installations/99/access_tokens", r.URL.Path)
		require.Equal(t, APIVersion, r.Header.Get("X-GitHub-Api-Version"))
		require.True(t, strings.HasPrefix(r.Header.Get("Authorization"), "Bearer eyJ"))
		var body struct {
			RepositoryIDs []int64           `json:"repository_ids"`
			Permissions   map[string]string `json:"permissions"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, []int64{42}, body.RepositoryIDs)
		require.Equal(t, map[string]string{"contents": "read", "metadata": "read"}, body.Permissions)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token": "ghs_99_stateless_and_not_forty_characters", "expires_at": time.Now().Add(time.Hour).UTC(),
		})
	}))
	defer server.Close()
	base, err := url.Parse(server.URL)
	require.NoError(t, err)
	manager := newTokenManager(mapCredentials{"env:APP_KEY": key}, server.Client(), base)
	connection := Connection{ID: "connection", AppID: "123", InstallationID: 99, CredentialRef: "env:APP_KEY"}

	token, err := manager.installation(context.Background(), connection, 42, map[string]string{"metadata": "read", "contents": "read"})
	require.NoError(t, err)
	require.Equal(t, "ghs_99_stateless_and_not_forty_characters", token)
	second, err := manager.installation(context.Background(), connection, 42, map[string]string{"contents": "read", "metadata": "read"})
	require.NoError(t, err)
	require.Equal(t, token, second)
	require.EqualValues(t, 1, calls.Load(), "unexpired tokens are cached independent of map iteration order")
}

func TestAppJWTUsesBoundedClaims(t *testing.T) {
	key := testPrivateKey(t)
	manager := newTokenManager(mapCredentials{"file:key": key}, http.DefaultClient, mustURL(t, APIBaseURL))
	now := time.Unix(1_800_000_000, 0).UTC()
	manager.now = func() time.Time { return now }
	token, err := manager.appJWT(Connection{AppID: "123", CredentialRef: "file:key"})
	require.NoError(t, err)
	parts := strings.Split(token, ".")
	require.Len(t, parts, 3)
	claimsRaw, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	var claims map[string]any
	require.NoError(t, json.Unmarshal(claimsRaw, &claims))
	require.Equal(t, "123", claims["iss"])
	require.EqualValues(t, now.Add(-time.Minute).Unix(), claims["iat"])
	require.EqualValues(t, now.Add(9*time.Minute).Unix(), claims["exp"])
}

func TestRuntimeCredentialsRejectTraversalAndSupportsNamedSources(t *testing.T) {
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(directory+"/github-key", []byte("key"), 0o600))
	store := RuntimeCredentials{SecretDir: directory, LookupEnv: func(name string) (string, bool) {
		return map[string]string{"GITHUB_APP_KEY": "env-key"}[name], name == "GITHUB_APP_KEY"
	}}
	value, err := store.Load("env:GITHUB_APP_KEY")
	require.NoError(t, err)
	require.Equal(t, "env-key", string(value))
	value, err = store.Load("file:github-key")
	require.NoError(t, err)
	require.Equal(t, "key", string(value))
	for _, invalid := range []string{"file:../key", "file:/tmp/key", "env:A-B", "literal:secret"} {
		_, err := store.Load(invalid)
		require.Error(t, err, invalid)
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	require.NoError(t, err)
	return parsed
}
