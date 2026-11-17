package githubforge

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"zooid/internal/forge"
)

func decodeGitHub(response *http.Response, target any, limit int64) error {
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return githubStatusError(response.StatusCode, response.Header)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, limit))
	if err := decoder.Decode(target); err != nil {
		return &forge.Error{Status: http.StatusBadGateway, Code: "invalid_github_response"}
	}
	return nil
}

func decodeAndClose(response *http.Response, target any, limit int64) error {
	defer response.Body.Close()
	return decodeGitHub(response, target, limit)
}

type gitHubCommit struct {
	SHA     string `json:"sha"`
	Parents []struct {
		SHA string `json:"sha"`
	} `json:"parents"`
	Commit struct {
		Message string `json:"message"`
		Author  struct {
			Name  string `json:"name"`
			Email string `json:"email"`
			Date  string `json:"date"`
		} `json:"author"`
		Committer struct {
			Name string `json:"name"`
			Date string `json:"date"`
		} `json:"committer"`
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	} `json:"commit"`
	Files []gitHubFile `json:"files"`
}

type gitHubFile struct {
	Filename         string `json:"filename"`
	PreviousFilename string `json:"previous_filename"`
	Status           string `json:"status"`
	Additions        int64  `json:"additions"`
	Deletions        int64  `json:"deletions"`
	Changes          int64  `json:"changes"`
	Patch            string `json:"patch"`
}

type gitHubCompare struct {
	BaseCommit      gitHubCommit `json:"base_commit"`
	HeadCommit      gitHubCommit `json:"head_commit"`
	MergeBaseCommit gitHubCommit `json:"merge_base_commit"`
	Files           []gitHubFile `json:"files"`
}

type gitHubTree struct {
	Truncated bool `json:"truncated"`
	Tree      []struct {
		Path string `json:"path"`
		Mode string `json:"mode"`
		Type string `json:"type"`
		Size int64  `json:"size"`
		SHA  string `json:"sha"`
	} `json:"tree"`
}

type gitHubContent struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Type     string `json:"type"`
	Size     int64  `json:"size"`
	SHA      string `json:"sha"`
	Encoding string `json:"encoding"`
	Content  string `json:"content"`
}

func normalizeCommit(commit gitHubCommit) forge.Commit {
	subject, body, _ := strings.Cut(commit.Commit.Message, "\n")
	parents := make([]string, 0, len(commit.Parents))
	for _, parent := range commit.Parents {
		parents = append(parents, parent.SHA)
	}
	return forge.Commit{
		SHA: commit.SHA, Parents: parents, Author: commit.Commit.Author.Name,
		AuthorEmail: commit.Commit.Author.Email, AuthorDate: commit.Commit.Author.Date,
		Committer: commit.Commit.Committer.Name, CommitDate: commit.Commit.Committer.Date,
		Subject: subject, Body: strings.TrimSpace(body), Trailers: []any{},
	}
}

func encodeContinuation(kind string, value int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(kind + ":" + strconv.Itoa(value)))
}

func decodeContinuation(token, kind string) (int, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) > 64 {
		return 0, &forge.Error{Status: http.StatusBadRequest, Code: "invalid_continuation"}
	}
	prefix := kind + ":"
	if !strings.HasPrefix(string(raw), prefix) {
		return 0, &forge.Error{Status: http.StatusBadRequest, Code: "invalid_continuation"}
	}
	value, err := strconv.Atoi(strings.TrimPrefix(string(raw), prefix))
	if err != nil || value < 0 || value > 10000 {
		return 0, &forge.Error{Status: http.StatusBadRequest, Code: "invalid_continuation"}
	}
	return value, nil
}

func normalizeGitHubTreeType(kind string) string {
	if kind == "tree" {
		return "tree"
	}
	return "blob"
}

func normalizeContentType(kind string) string {
	if kind == "dir" {
		return "tree"
	}
	return "blob"
}

func contentMode(kind string) string {
	if kind == "dir" {
		return "040000"
	}
	if kind == "symlink" {
		return "120000"
	}
	if kind == "submodule" {
		return "160000"
	}
	return "100644"
}

func normalizeFileStatus(status string) string {
	switch status {
	case "added", "removed", "modified", "renamed", "copied", "changed", "unchanged":
		if status == "removed" {
			return "deleted"
		}
		if status == "changed" {
			return "type_changed"
		}
		return status
	default:
		return "unknown"
	}
}

func appendPatch(builder *strings.Builder, file gitHubFile) {
	if file.Patch == "" {
		return
	}
	oldPath := file.PreviousFilename
	if oldPath == "" {
		oldPath = file.Filename
	}
	builder.WriteString("diff --git a/")
	builder.WriteString(oldPath)
	builder.WriteString(" b/")
	builder.WriteString(file.Filename)
	builder.WriteByte('\n')
	builder.WriteString(file.Patch)
	if !strings.HasSuffix(file.Patch, "\n") {
		builder.WriteByte('\n')
	}
}

func decodeContent(content, encoding string) ([]byte, error) {
	if encoding == "" && content == "" {
		return []byte{}, nil
	}
	if encoding != "base64" {
		return nil, fmt.Errorf("unsupported content encoding")
	}
	return base64.StdEncoding.DecodeString(strings.ReplaceAll(content, "\n", ""))
}

var (
	_ forge.GitAuthorityBackend      = (*Backend)(nil)
	_ forge.RepositoryContentBackend = (*Backend)(nil)
)
