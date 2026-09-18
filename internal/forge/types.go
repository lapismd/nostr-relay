// Package forge defines provider-neutral contracts for repository, provider
// collaboration, mirroring, and CI integrations. Provider implementations stay
// behind these interfaces; Nostr authority and NIP-29 policy remain in Zooid.
package forge

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"fiatjaf.com/nostr"
	"zooid/internal/projecttask"
)

const (
	BackendNone      = "none"
	BackendUnmanaged = "unmanaged"
	BackendRelay     = "relay"
	BackendWalGit    = "walgit"
	BackendGitHub    = "github"

	ConfigurationLegacyDefault = "legacy-default"
	ConfigurationExplicit      = "explicit"

	CredentialSystem = "system"
	CredentialUser   = "user"
)

type CloneAuth string

const (
	CloneAuthNone     CloneAuth = "none"
	CloneAuthNostr    CloneAuth = "nostr"
	CloneAuthProvider CloneAuth = "provider"
)

// Integration is the operational composition for one immutable Nostr
// repository coordinate. AnnouncementID is an optimistic-concurrency guard;
// changing this row never mutates the signed announcement.
type Integration struct {
	Coordinate                  projecttask.RepositoryCoordinate `json:"-"`
	AnnouncementID              string                           `json:"announcement_id"`
	ConfigurationSource         string                           `json:"configuration_source"`
	GitAuthorityBackend         string                           `json:"git_authority_backend"`
	ContentBackend              string                           `json:"content_backend"`
	GitReplicationTargetBackend string                           `json:"git_replication_target_backend,omitempty"`
	// GitBackend is the legacy shorthand retained for old rows and clients.
	// New code resolves authority and content independently.
	GitBackend             string         `json:"git_backend"`
	CollaborationBackend   string         `json:"collaboration_backend"`
	MirrorBackend          string         `json:"mirror_backend,omitempty"`
	CIBackend              string         `json:"ci_backend,omitempty"`
	ConnectionID           string         `json:"connection_id,omitempty"`
	ExternalRepositoryID   string         `json:"external_repository_id,omitempty"`
	ExternalRepository     string         `json:"external_repository,omitempty"`
	CIConnectionID         string         `json:"ci_connection_id,omitempty"`
	CIExternalRepositoryID string         `json:"ci_external_repository_id,omitempty"`
	CIExternalRepository   string         `json:"ci_external_repository,omitempty"`
	CIWorkflow             string         `json:"ci_workflow,omitempty"`
	Generation             int64          `json:"generation"`
	HealthState            string         `json:"health_state"`
	HealthDetail           string         `json:"health_detail,omitempty"`
	Replica                *ReplicaConfig `json:"replica,omitempty"`
}

type ReplicaMode string

const (
	ReplicaModeOff        ReplicaMode = "off"
	ReplicaModeOnDemand   ReplicaMode = "on-demand"
	ReplicaModeContinuous ReplicaMode = "continuous"
)

type ReplicaReadPolicy string

const (
	ReplicaReadWait     ReplicaReadPolicy = "wait"
	ReplicaReadFallback ReplicaReadPolicy = "fallback"
	ReplicaReadStale    ReplicaReadPolicy = "stale"
)

type ReplicaConfig struct {
	Enabled          bool              `json:"enabled"`
	AuthorityBackend string            `json:"authority_backend"`
	ReplicaBackend   string            `json:"replica_backend"`
	Direction        string            `json:"direction"`
	SyncBranches     bool              `json:"sync_branches"`
	SyncTags         bool              `json:"sync_tags"`
	Mode             ReplicaMode       `json:"mode"`
	ReadPolicy       ReplicaReadPolicy `json:"read_policy"`
	WaitMilliseconds int               `json:"wait_milliseconds,omitempty"`
}

type Repository struct {
	Coordinate     projecttask.RepositoryCoordinate
	Announcement   nostr.Event
	Channel        string
	Classification string
	CloneURL       string
	Integration    Integration
}

type CloneDescriptor struct {
	Available bool      `json:"available"`
	URL       string    `json:"url,omitempty"`
	Auth      CloneAuth `json:"auth"`
	Provider  string    `json:"provider,omitempty"`
}

type SmartHTTPCapabilities struct {
	Read  bool `json:"read"`
	Write bool `json:"write"`
}

type ContentLimits struct {
	MaximumBlobBytes    int64 `json:"maximum_blob_bytes,omitempty"`
	MaximumChangedFiles int   `json:"maximum_changed_files,omitempty"`
	MaximumTreeEntries  int   `json:"maximum_tree_entries,omitempty"`
	MaximumPatchBytes   int64 `json:"maximum_patch_bytes,omitempty"`
	MayTruncateCompare  bool  `json:"may_truncate_compare,omitempty"`
	MayTruncateTree     bool  `json:"may_truncate_tree,omitempty"`
}

// Limits remains an internal source-compatibility alias. ContentLimits is the
// provider-neutral contract used by new code and exposed as capability_limits.
type Limits = ContentLimits

type GitAuthorityCapabilities struct {
	Clone     CloneDescriptor       `json:"clone"`
	SmartHTTP SmartHTTPCapabilities `json:"smart_http"`
	RefEvents bool                  `json:"ref_events"`
	Provision bool                  `json:"provision"`
	Import    bool                  `json:"import"`
}

type ContentCapabilities struct {
	Browse  bool          `json:"browse"`
	Compare bool          `json:"compare"`
	Limits  ContentLimits `json:"capability_limits"`
}

// Capabilities is intentionally explicit. A false value is meaningful and
// prevents callers from inferring functionality from a provider name.
type Capabilities struct {
	GitAuthorityBackend         string                `json:"git_authority_backend"`
	ContentBackend              string                `json:"content_backend"`
	GitBackend                  string                `json:"git_backend"`
	CollaborationBackend        string                `json:"collaboration_backend"`
	MirrorBackend               string                `json:"mirror_backend,omitempty"`
	GitReplicationTargetBackend string                `json:"git_replication_target_backend,omitempty"`
	CIBackend                   string                `json:"ci_backend,omitempty"`
	Clone                       CloneDescriptor       `json:"clone"`
	SmartHTTP                   SmartHTTPCapabilities `json:"smart_http"`
	Browse                      bool                  `json:"browse"`
	Compare                     bool                  `json:"compare"`
	RefEvents                   bool                  `json:"ref_events"`
	Provision                   bool                  `json:"provision"`
	Import                      bool                  `json:"import"`
	Issues                      bool                  `json:"issues"`
	PullRequests                bool                  `json:"pull_requests"`
	Reviews                     bool                  `json:"reviews"`
	Merge                       bool                  `json:"merge"`
	Protection                  *BranchProtection     `json:"protection,omitempty"`
	Mirror                      bool                  `json:"mirror"`
	GitReplicationTarget        bool                  `json:"git_replication_target,omitempty"`
	CI                          bool                  `json:"ci"`
	LocalReplica                bool                  `json:"local_replica,omitempty"`
	ReplicaState                string                `json:"replica_state,omitempty"`
	Limits                      ContentLimits         `json:"limits"`
	CapabilityLimits            ContentLimits         `json:"capability_limits"`
}

// ResultMetadata is additive metadata shared by every repository-content
// result. Continuation is opaque to callers. A missing continuation on a
// truncated result means the provider cannot retrieve the omitted portion.
type ResultMetadata struct {
	Truncated        bool           `json:"truncated,omitempty"`
	Continuation     string         `json:"continuation,omitempty"`
	Incomplete       []string       `json:"incomplete,omitempty"`
	LimitExceeded    *LimitExceeded `json:"limit_exceeded,omitempty"`
	CapabilityLimits *ContentLimits `json:"capability_limits,omitempty"`

	CacheControl string `json:"-"`
	ETag         string `json:"-"`
	LastModified string `json:"-"`
}

type LimitExceeded struct {
	Resource string `json:"resource"`
	Limit    int64  `json:"limit"`
	Actual   int64  `json:"actual,omitempty"`
}

type RefsRequest struct {
	Kind         string
	Continuation string
	Principal    string
}

type RevisionRequest struct {
	Revision  string
	Path      string
	Principal string
}

type CommitsRequest struct {
	Ref          string
	Path         string
	Skip         int
	Limit        int
	Continuation string
	Principal    string
}

type CompareRequest struct {
	Base          string
	Head          string
	UseMergeBase  bool
	MaxFiles      int
	MaxPatchBytes int64
	Principal     string
}

type PullRequestDiffRequest struct {
	Number        int64
	MaxFiles      int
	MaxPatchBytes int64
	Principal     string
}

type Revision struct {
	Ref  string `json:"ref"`
	SHA  string `json:"sha"`
	Path string `json:"path"`
	Kind string `json:"kind"`
	ResultMetadata
}

type Ref struct {
	Name string `json:"name"`
	SHA  string `json:"sha"`
}

type RefPage struct {
	Head *Ref  `json:"head,omitempty"`
	Refs []Ref `json:"refs,omitempty"`
	More bool  `json:"more,omitempty"`
	ResultMetadata
}

type TreeEntry struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Mode string `json:"mode"`
	Size int64  `json:"size"`
	SHA  string `json:"sha"`
}

type Tree struct {
	Ref     string      `json:"ref"`
	SHA     string      `json:"sha"`
	Path    string      `json:"path"`
	Entries []TreeEntry `json:"entries"`
	ResultMetadata
}

type Blob struct {
	Ref      string `json:"ref"`
	SHA      string `json:"sha"`
	Path     string `json:"path"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Contents string `json:"contents,omitempty"`
	Binary   bool   `json:"binary,omitempty"`
	TooLarge bool   `json:"too_large,omitempty"`
	ResultMetadata
}

type Commit struct {
	SHA         string   `json:"sha"`
	Parents     []string `json:"parents"`
	Author      string   `json:"author"`
	AuthorEmail string   `json:"author_email"`
	AuthorDate  string   `json:"author_date"`
	Committer   string   `json:"committer"`
	CommitDate  string   `json:"commit_date"`
	Subject     string   `json:"subject"`
	Body        string   `json:"body"`
	Trailers    []any    `json:"trailers"`
}

type CommitPage struct {
	Ref     string   `json:"ref"`
	SHA     string   `json:"sha"`
	Commits []Commit `json:"commits"`
	More    bool     `json:"more"`
	ResultMetadata
}

type CommitStat struct {
	Path      string `json:"path"`
	Additions int64  `json:"additions"`
	Deletions int64  `json:"deletions"`
}

type CommitDetail struct {
	Commit Commit       `json:"commit"`
	Stats  []CommitStat `json:"stats"`
	Patch  string       `json:"patch"`
	ResultMetadata
}

type DiffFile struct {
	Path      string `json:"path"`
	OldPath   string `json:"old_path,omitempty"`
	Status    string `json:"status"`
	Additions int64  `json:"additions"`
	Deletions int64  `json:"deletions"`
	Binary    bool   `json:"binary,omitempty"`
}

type Diff struct {
	Base      string     `json:"base"`
	Head      string     `json:"head"`
	MergeBase string     `json:"merge_base,omitempty"`
	Files     []DiffFile `json:"files"`
	Patch     string     `json:"patch"`
	ResultMetadata
}

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

type CollaborationCommand struct {
	OperationID       string
	Kind              string
	Actor             nostr.PubKey
	CredentialMode    string
	Repository        Repository
	RootEventID       string
	ExpectedHead      string
	CorrelationMarker string
	Payload           json.RawMessage
}

type CollaborationResult struct {
	ExternalID       string   `json:"external_id"`
	ExternalNumber   int64    `json:"external_number,omitempty"`
	ExternalURL      string   `json:"external_url,omitempty"`
	ExternalAuthor   string   `json:"external_author,omitempty"`
	ExternalAuthorID string   `json:"external_author_id,omitempty"`
	State            string   `json:"state,omitempty"`
	HeadSHA          string   `json:"head_sha,omitempty"`
	HeadRef          string   `json:"head_ref,omitempty"`
	BaseSHA          string   `json:"base_sha,omitempty"`
	BaseRef          string   `json:"base_ref,omitempty"`
	MergedCommit     string   `json:"merged_commit,omitempty"`
	ImportedRoots    int      `json:"imported_roots,omitempty"`
	ImportedComments int      `json:"imported_comments,omitempty"`
	Truncated        bool     `json:"truncated,omitempty"`
	Incomplete       []string `json:"incomplete,omitempty"`
}

// BranchProtection is the relay-owned default-branch contract advertised to
// Community. GitHub-authoritative repositories keep clone/push protection on
// GitHub and only report that fact here.
type BranchProtection struct {
	DefaultBranch        string `json:"default_branch,omitempty"`
	ProtectedRef         string `json:"protected_ref,omitempty"`
	PullRequestsRequired bool   `json:"pull_requests_required"`
	MergeActors          string `json:"merge_actors,omitempty"`
	GitHubAuthoritative  bool   `json:"github_authoritative,omitempty"`
}

type ProtectedRefMergeRequest struct {
	Principal    string
	TargetRef    string
	ExpectedHead string
	TargetSHA    string
	Method       string
	Title        string
	Message      string
}

type ProtectedRefMerger interface {
	MergeProtectedRef(context.Context, Repository, ProtectedRefMergeRequest) (CollaborationResult, error)
}

type CollaborationBackend interface {
	Name() string
	Capabilities(context.Context, Repository) (Capabilities, error)
	Execute(context.Context, CollaborationCommand) (CollaborationResult, error)
}

type MirrorProjection struct {
	OperationID       string
	Kind              string
	Repository        Repository
	Event             nostr.Event
	CredentialMode    string
	RootEventID       string
	ExternalNumber    int64
	ObjectType        string
	ExpectedHead      string
	CorrelationMarker string
	Payload           json.RawMessage
}

type MirrorBackend interface {
	Name() string
	Project(context.Context, MirrorProjection) (CollaborationResult, error)
}

type ProviderEvent struct {
	DeliveryID           string
	Provider             string
	ConnectionID         string
	InstallationID       int64
	EventType            string
	Action               string
	ExternalRepositoryID string
	ExternalObjectID     string
	Payload              json.RawMessage
}

type EventSource interface {
	Provider() string
	VerifyAndDecode(context.Context, http.Header, []byte) ([]ProviderEvent, error)
}

type ProviderImportRequest struct {
	UpdatedSince    time.Time
	OpenOnly        bool
	MaximumRoots    int
	MaximumComments int
}

type ProviderImportResult struct {
	Events     []ProviderEvent `json:"-"`
	Roots      int             `json:"roots"`
	Comments   int             `json:"comments"`
	Truncated  bool            `json:"truncated,omitempty"`
	Incomplete []string        `json:"incomplete,omitempty"`
}

type CollaborationImporter interface {
	InitialImport(context.Context, Repository, ProviderImportRequest) (ProviderImportResult, error)
}

type CollaborationReconciler interface {
	Reconcile(context.Context, Repository, ProviderImportRequest) (ProviderImportResult, error)
}

type CIRequest struct {
	OperationID string
	Repository  Repository
	Commit      string
	Workflow    string
	Inputs      map[string]string
	MirrorRef   string
}

type CIRun struct {
	ProviderRunID string    `json:"provider_run_id"`
	Commit        string    `json:"commit"`
	Status        string    `json:"status"`
	URL           string    `json:"url,omitempty"`
	MirrorRef     string    `json:"mirror_ref,omitempty"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type CIBackend interface {
	Name() string
	Start(context.Context, CIRequest) (CIRun, error)
	Status(context.Context, Repository, string) (CIRun, error)
	Cancel(context.Context, Repository, string) error
}

type CIDiscoveryBackend interface {
	Discover(context.Context, CIRequest) (CIRun, error)
}

type CICleanupBackend interface {
	Cleanup(context.Context, Repository, string) error
}

type CIMirrorTarget struct {
	CloneURL string
	Username string
	Password string
}

type CIMirrorTargetBackend interface {
	MirrorTarget(context.Context, Repository) (CIMirrorTarget, error)
}

type CommitMirrorSource interface {
	MirrorCommit(context.Context, Repository, string, CIMirrorTarget, string) error
}

type RepositoryIntegrationResolver interface {
	ResolveIntegration(context.Context, projecttask.RepositoryCoordinate) (ResolvedRepository, bool, error)
}

type ResolvedRepository struct {
	Repository        Repository
	Capabilities      Capabilities
	Authority         GitAuthorityBackend
	Content           RepositoryContentBackend
	Collaboration     CollaborationBackend
	Mirror            MirrorBackend
	CI                CIBackend
	ReplicationTarget GitReplicationTargetBackend
}
