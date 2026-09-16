package specdocs

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestGitHubCIWorkflow(t *testing.T) {
	workflow := readWorkflow(t, "ci.yml")

	requireContains(t, workflow, "pull_request:")
	requireContains(t, workflow, "push:")
	requireContains(t, workflow, "workflow_dispatch:")
	requireContains(t, workflow, "permissions:\n  contents: read")
	requireContains(t, workflow, "GOWORK=off go test ./internal/specdocs")
	requireContains(t, workflow, "GOWORK=off go test ./...")
	requireContains(t, workflow, "go test -tags=integration ./zooid/")
	requireContains(t, workflow, "-tags=github_integration")
	requireContains(t, workflow, "-run '^TestGitHubForgeDockerLane$'")
	requireContains(t, workflow, "-tags=walgit_integration")
	requireContains(t, workflow, "-run '^TestWalGitObjectStoreRecovery$'")
	requireContains(t, workflow, "target: relay")
	requireContains(t, workflow, "mdbook@0.5.4")
	assertRuntimeVulnerabilityGate(t, workflow)
	assertFalseContains(t, workflow, "pull_request_target:")
	assertActionsUseImmutableCommits(t, workflow)
}

func TestGitHubPublishImageWorkflow(t *testing.T) {
	workflow := readWorkflow(t, "publish-image.yml")

	requireContains(t, workflow, "branches: [main]")
	requireContains(t, workflow, `tags: ["v*.*.*"]`)
	requireContains(t, workflow, "workflow_dispatch:")
	requireContains(t, workflow, "needs: verify")
	requireContains(t, workflow, "packages: write")
	requireContains(t, workflow, "attestations: write")
	requireContains(t, workflow, "artifact-metadata: write")
	requireContains(t, workflow, "id-token: write")
	requireContains(t, workflow, "ghcr.io/${{ github.repository_owner }}/lapis-relay")
	requireContains(t, workflow, `echo "IMAGE_NAME=${IMAGE_NAME,,}" >> "$GITHUB_ENV"`)
	requireContains(t, workflow, "platforms: linux/amd64,linux/arm64")
	requireContains(t, workflow, "target: relay")
	requireContains(t, workflow, "provenance: mode=max")
	requireContains(t, workflow, "sbom: generator=docker/buildkit-syft-scanner:stable-1@sha256:ae4f3b554449e7e25548e7d8ccc029d17357348e30c6e3df01b92bc93654d6a9")
	requireContains(t, workflow, "push: true")
	requireContains(t, workflow, "push-to-registry: true")
	requireContains(t, workflow, "go test -tags=integration ./zooid/")
	requireContains(t, workflow, "-tags=github_integration")
	requireContains(t, workflow, "-tags=walgit_integration")
	requireContains(t, workflow, "tonistiigi/binfmt:qemu-v10.2.3@sha256:400a4873b838d1b89194d982c45e5fb3cda4593fbfd7e08a02e76b03b21166f0")
	requireContains(t, workflow, "moby/buildkit:v0.33.0@sha256:6c2fa84a6b61ccd72899dde4239f8d5717f05f9a8ca6f3cad185fb1a95a94de3")
	requireContains(t, workflow, "docker logout ghcr.io")
	requireContains(t, workflow, "docker buildx imagetools inspect")
	requireContains(t, workflow, "org.opencontainers.image.source=https://github.com/${{ github.repository }}")
	assertRuntimeVulnerabilityGate(t, workflow)
	assertFalseContains(t, workflow, "pull_request:")
	assertFalseContains(t, workflow, "pull_request_target:")
	assertActionsUseImmutableCommits(t, workflow)
}

func readWorkflow(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join("..", "..", ".github", "workflows", name)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func requireContains(t *testing.T, workflow, needle string) {
	t.Helper()
	if !strings.Contains(workflow, needle) {
		t.Errorf("workflow is missing %q", needle)
	}
}

func assertFalseContains(t *testing.T, workflow, needle string) {
	t.Helper()
	if strings.Contains(workflow, needle) {
		t.Errorf("workflow must not contain %q", needle)
	}
}

func assertRuntimeVulnerabilityGate(t *testing.T, workflow string) {
	t.Helper()
	requireContains(t, workflow, "aquasec/trivy:0.74.0@sha256:62b1e65e8869bc4b4c6aa4fa2b21595256c7c2f6018a9d9ad61caf87187c1969")
	requireContains(t, workflow, "--severity HIGH,CRITICAL")
	requireContains(t, workflow, "--ignore-unfixed")
	requireContains(t, workflow, "--exit-code 1")
}

func assertActionsUseImmutableCommits(t *testing.T, workflow string) {
	t.Helper()
	uses := regexp.MustCompile(`(?m)^\s+-?\s*uses:\s+([^\s#]+)(?:\s+#.*)?$`)
	matches := uses.FindAllStringSubmatch(workflow, -1)
	if len(matches) == 0 {
		t.Fatal("workflow must use at least one action")
	}

	pinned := regexp.MustCompile(`^[\w.-]+/[\w.-]+(?:/[\w./-]+)?@[0-9a-f]{40}$`)
	for _, match := range matches {
		action := match[1]
		if !pinned.MatchString(action) {
			t.Errorf("action must be pinned to a full commit: %s", action)
		}
	}
}
