package forge

import (
	"context"
	"net/http"
)

type GitAuthorityBackend interface {
	Name() string
	AuthorityCapabilities(context.Context, Repository) (GitAuthorityCapabilities, error)
	CloneDescriptor(context.Context, Repository) (CloneDescriptor, error)
	DefaultBranch(context.Context, Repository, string) (string, error)
	Refs(context.Context, Repository, RefsRequest) (RefPage, error)
	SnapshotRefs(context.Context, Repository) (map[string]string, error)
}

type RepositoryContentBackend interface {
	Name() string
	ContentCapabilities(context.Context, Repository) (ContentCapabilities, error)
	Resolve(context.Context, Repository, RevisionRequest) (Revision, error)
	Tree(context.Context, Repository, RevisionRequest) (Tree, error)
	Blob(context.Context, Repository, RevisionRequest) (Blob, error)
	Commits(context.Context, Repository, CommitsRequest) (CommitPage, error)
	Commit(context.Context, Repository, RevisionRequest) (CommitDetail, error)
	Compare(context.Context, Repository, CompareRequest) (Diff, error)
}

// PullRequestDiffBackend exposes a provider's immutable review file set as a
// compatibility fallback for legacy roots without a persisted Git baseline.
// New reviews use RepositoryContentBackend with their stored object IDs.
type PullRequestDiffBackend interface {
	PullRequestDiff(context.Context, Repository, PullRequestDiffRequest) (Diff, error)
}

type RepositoryProvisioner interface {
	EnsureRepository(context.Context, Repository) error
}

type RepositoryImporter interface {
	ImportRepository(context.Context, Repository, ImportRequest) (ImportResult, error)
}

type ImportRequest struct {
	SourceCloneURL       string
	ExpectedRefs         map[string]string
	SourceUsername       string `json:"-"`
	SourcePassword       string `json:"-"`
	DestinationPrincipal string `json:"-"`
	SyncBranches         bool
	SyncTags             bool
}

type ImportResult struct {
	Refs map[string]string `json:"refs"`
}

type ImportObjectRequest struct {
	SourceCloneURL       string
	ObjectOID            string
	SourceUsername       string `json:"-"`
	SourcePassword       string `json:"-"`
	DestinationPrincipal string `json:"-"`
}

type RepositoryObjectImporter interface {
	ImportObject(context.Context, Repository, ImportObjectRequest) error
}

// GitReplicationTargetBackend supplies short-lived credentials for an
// explicitly configured outbound Git replica. It is intentionally distinct
// from MirrorBackend, which projects collaboration objects.
type GitReplicationTargetBackend interface {
	Name() string
	GitReplicationTarget(context.Context, Repository) (GitReplicationTarget, error)
}

type GitReplicationTarget struct {
	CloneURL string `json:"-"`
	Username string `json:"-"`
	Password string `json:"-"`
}

// GitReplicationSource transfers the source repository's complete managed
// heads/tags to a target. Implementations must make this one directional.
type GitReplicationSource interface {
	ReplicateRepository(context.Context, Repository, GitReplicationTarget, bool, bool) error
}

type ReplicaSyncRequest struct {
	DeliveryID string
	RefName    string
	TargetOID  string
	Reason     string
}

type GitReplicaCoordinator interface {
	Provision(context.Context, Repository) error
	Sync(context.Context, Repository, ReplicaSyncRequest) error
	Reconcile(context.Context, Repository) error
}

type AuthenticatedCloneSource interface {
	CloneSource(context.Context, Repository) (url, username, password string, err error)
}

type SmartHTTPBackend interface {
	ServeGitHTTP(http.ResponseWriter, *http.Request, Repository, string, string)
}
