package gitforge

import (
	"context"
	"net/http"

	"zooid/internal/projecttask"
)

// Backend is the deliberately small process boundary around the Git engine.
// Git semantics stay in the sidecar; the relay supplies identity and policy.
type Backend interface {
	EnsureRepository(context.Context, projecttask.RepositoryCoordinate) error
	ProxyGitHTTP(http.ResponseWriter, *http.Request, Repository)
	ProxyRepositoryAPI(http.ResponseWriter, *http.Request, Repository, string)
	Compare(context.Context, Repository, CompareRequest) (CompareResult, error)
}

type CompareRequest struct {
	Base          string
	Head          string
	UseMergeBase  bool
	MaxFiles      int
	MaxPatchBytes int64
}

type CompareFile struct {
	Path      string `json:"path"`
	OldPath   string `json:"old_path,omitempty"`
	Status    string `json:"status"`
	Additions int64  `json:"additions"`
	Deletions int64  `json:"deletions"`
	Binary    bool   `json:"binary,omitempty"`
}

type CompareResult struct {
	Base      string        `json:"base"`
	Head      string        `json:"head"`
	MergeBase string        `json:"merge_base,omitempty"`
	Files     []CompareFile `json:"files"`
	Patch     string        `json:"patch"`
}
