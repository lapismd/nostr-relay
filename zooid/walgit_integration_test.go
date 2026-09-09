//go:build walgit_integration || github_integration

package zooid

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/docker/go-connections/nat"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	walgitIntegrationImage = "lapis-walgit:local"
	walgitServiceToken     = "integration-test-relay-token"
	rustfsAccessKey        = "integration-test-access"
	rustfsSecretKey        = "integration-test-secret-key"
)

// TestWalGitObjectStoreRecovery proves the deployment property that matters to
// Lapis: a new WalGit process with an empty local cache can serve Git and the
// browser read API from the durable RustFS object store.
func TestWalGitObjectStoreRecovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	buildWalGitIntegrationImage(t)

	net, err := network.New(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = net.Remove(context.Background()) })

	rustfs := startRustFS(t, ctx, net)
	t.Cleanup(func() { _ = rustfs.Terminate(context.Background()) })
	createWalGitBucket(t, ctx, net)

	first := startWalGit(t, ctx, net)
	firstActive := true
	t.Cleanup(func() {
		if firstActive {
			_ = first.Terminate(context.Background())
		}
	})
	firstURL := containerHTTPURL(t, ctx, first, "8080")

	owner := strings.Repeat("a", 64)
	repository := "recovery"
	repositoryURL := fmt.Sprintf("%s/%s/%s.git", firstURL, owner, repository)
	requestStatus(t, http.MethodPut, fmt.Sprintf("%s/%s/%s/api", firstURL, owner, repository), http.StatusCreated)

	worktree := t.TempDir()
	git(t, worktree, "init", "-q", "-b", "main")
	git(t, worktree, "config", "user.email", "integration@example.invalid")
	git(t, worktree, "config", "user.name", "WalGit Integration")
	git(t, worktree, "config", "commit.gpgsign", "false")
	require.NoError(t, os.WriteFile(filepath.Join(worktree, "README.md"), []byte("# durable v1\n"), 0o600))
	git(t, worktree, "add", "README.md")
	git(t, worktree, "commit", "-q", "-m", "initial")
	base := strings.TrimSpace(git(t, worktree, "rev-parse", "HEAD"))
	require.NoError(t, os.WriteFile(filepath.Join(worktree, "README.md"), []byte("# durable v2\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(worktree, "web.txt"), []byte("browser content\n"), 0o600))
	git(t, worktree, "add", ".")
	git(t, worktree, "commit", "-q", "-m", "web content")
	head := strings.TrimSpace(git(t, worktree, "rev-parse", "HEAD"))
	gitWithAuth(t, worktree, "push", "--mirror", repositoryURL)

	assertWalGitReadSurface(t, firstURL, owner, repository, base, head)
	require.NoError(t, first.Terminate(ctx))
	firstActive = false

	second := startWalGit(t, ctx, net)
	t.Cleanup(func() { _ = second.Terminate(context.Background()) })
	secondURL := containerHTTPURL(t, ctx, second, "8080")
	assertWalGitReadSurface(t, secondURL, owner, repository, base, head)

	clone := filepath.Join(t.TempDir(), "clone")
	gitWithAuth(t, "", "clone", "-q", fmt.Sprintf("%s/%s/%s.git", secondURL, owner, repository), clone)
	require.Equal(t, "# durable v2", strings.TrimSpace(git(t, clone, "show", "HEAD:README.md")))
}

func buildWalGitIntegrationImage(t *testing.T) {
	t.Helper()
	if err := exec.Command("docker", "image", "inspect", walgitIntegrationImage).Run(); err == nil {
		return
	}
	cmd := exec.Command("docker", "build", "-f", "third_party/walgit/Containerfile", "-t", walgitIntegrationImage, ".")
	cmd.Dir = ".."
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "build WalGit image:\n%s", output)
}

func startRustFS(t *testing.T, ctx context.Context, net *testcontainers.DockerNetwork) testcontainers.Container {
	t.Helper()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: "rustfs/rustfs@sha256:b7014e0ce2bc703c1316b3ef760e29dfae61fe4a50d1a66fa89638e0f8ea211f",
			Env: map[string]string{
				"RUSTFS_ACCESS_KEY":      rustfsAccessKey,
				"RUSTFS_SECRET_KEY":      rustfsSecretKey,
				"RUSTFS_ADDRESS":         "0.0.0.0:9000",
				"RUSTFS_CONSOLE_ADDRESS": "0.0.0.0:9001",
				"RUSTFS_VOLUMES":         "/data",
			},
			ExposedPorts: []string{"9000/tcp"},
			Networks:     []string{net.Name},
			NetworkAliases: map[string][]string{
				net.Name: {"rustfs"},
			},
			WaitingFor: wait.ForHTTP("/minio/health/live").WithPort(nat.Port("9000/tcp")).WithStartupTimeout(90 * time.Second),
		},
		Started: true,
	})
	require.NoError(t, err)
	return container
}

func createWalGitBucket(t *testing.T, ctx context.Context, net *testcontainers.DockerNetwork) {
	t.Helper()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:      "rustfs/rc@sha256:fad686174251b22de1e7b11438b3389932c93d2043c4eec981e1168c59edf92f",
			Entrypoint: []string{"/bin/sh", "-ec"},
			Cmd: []string{
				"rc alias set --quiet --region us-east-1 --bucket-lookup path local http://rustfs:9000 \"$S3_ACCESS_KEY_ID\" \"$S3_SECRET_ACCESS_KEY\" && rc bucket create --quiet --ignore-existing local/lapis-walgit",
			},
			Env: map[string]string{
				"S3_ACCESS_KEY_ID":     rustfsAccessKey,
				"S3_SECRET_ACCESS_KEY": rustfsSecretKey,
			},
			Networks:   []string{net.Name},
			WaitingFor: wait.ForExit().WithExitTimeout(60 * time.Second),
		},
		Started: true,
	})
	require.NoError(t, err)
	defer container.Terminate(context.Background())
	state, err := container.State(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, state.ExitCode, "bucket initializer exit code")
}

func startWalGit(t *testing.T, ctx context.Context, net *testcontainers.DockerNetwork) testcontainers.Container {
	t.Helper()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        walgitIntegrationImage,
			ExposedPorts: []string{"8080/tcp"},
			Env: map[string]string{
				"WALGIT_RELAY_TOKEN":             walgitServiceToken,
				"WALGIT__EVENTS__WEBHOOK_SECRET": "integration-test-webhook-secret",
				"S3_ACCESS_KEY_ID":               rustfsAccessKey,
				"S3_SECRET_ACCESS_KEY":           rustfsSecretKey,
			},
			Networks: []string{net.Name},
			WaitingFor: wait.ForHTTP("/readyz").WithPort(nat.Port("8080/tcp")).
				WithStartupTimeout(90 * time.Second),
		},
		Started: true,
	})
	require.NoError(t, err)
	return container
}

func containerHTTPURL(t *testing.T, ctx context.Context, container testcontainers.Container, port string) string {
	t.Helper()
	host, err := container.Host(ctx)
	require.NoError(t, err)
	mapped, err := container.MappedPort(ctx, nat.Port(port+"/tcp"))
	require.NoError(t, err)
	return "http://" + host + ":" + mapped.Port()
}

func requestStatus(t *testing.T, method, target string, wanted int) []byte {
	t.Helper()
	request, err := http.NewRequest(method, target, nil)
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer "+walgitServiceToken)
	request.Header.Set("X-Walgit-Principal", "integration-test")
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	require.NoError(t, err)
	require.Equal(t, wanted, response.StatusCode, "%s %s: %s", method, target, body)
	return body
}

func assertWalGitReadSurface(t *testing.T, baseURL, owner, repository, base, head string) {
	t.Helper()
	prefix := fmt.Sprintf("%s/%s/%s/api", baseURL, owner, repository)
	for _, path := range []string{
		"/refs",
		"/resolve/main",
		"/tree/" + head,
		"/blob/" + head + "/README.md",
		"/commits?ref=" + head,
		"/commit/" + head,
		"/compare?base=" + base + "&head=" + head + "&mode=direct&max_files=20&max_patch_bytes=1048576",
	} {
		body := requestStatus(t, http.MethodGet, prefix+path, http.StatusOK)
		var result any
		require.NoError(t, json.Unmarshal(body, &result), "%s returned non-JSON: %s", path, body)
	}
	blob := requestStatus(t, http.MethodGet, prefix+"/blob/"+head+"/README.md", http.StatusOK)
	require.Contains(t, string(blob), "durable v2")
	diff := requestStatus(t, http.MethodGet, prefix+"/compare?base="+base+"&head="+head+"&mode=direct&max_files=20&max_patch_bytes=1048576", http.StatusOK)
	require.Contains(t, string(diff), "web.txt")
}

func gitWithAuth(t *testing.T, directory string, args ...string) string {
	t.Helper()
	return git(t, directory, append([]string{"-c", "http.extraHeader=Authorization: Bearer " + walgitServiceToken}, args...)...)
}

func git(t *testing.T, directory string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if directory != "" {
		cmd.Dir = directory
	}
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %s:\n%s", strings.Join(args, " "), output)
	return string(output)
}
