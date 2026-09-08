// Package forge defines provider-neutral contracts for repository, provider
// collaboration, mirroring, and CI integrations. Provider implementations stay
// behind these interfaces; Nostr authority and NIP-29 policy remain in Zooid.
package forge

import (
	"context"
	"encoding/json"
	"io"
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
	Coordinate           projecttask.RepositoryCoordinate `json:"-"`
	AnnouncementID       string                           `json:"announcement_id"`
	ConfigurationSource  string                           `json:"configuration_source"`
	GitBackend           string                           `json:"git_backend"`
	CollaborationBackend string                           `json:"collaboration_backend"`
	MirrorBackend        string                           `json:"mirror_backend,omitempty"`
	CIBackend            string                           `json:"ci_backend,omitempty"`
	ConnectionID         string                           `json:"connection_id,omitempty"`
	ExternalRepositoryID string                           `json:"external_repository_id,omitempty"`
	ExternalRepository   string                           `json:"external_repository,omitempty"`
	Generation           int64                            `json:"generation"`
	HealthState          string                           `json:"health_state"`
	HealthDetail         string                           `json:"health_detail,omitempty"`
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

type Limits struct {
	MaximumBlobBytes    int64 `json:"maximum_blob_bytes,omitempty"`
	MaximumChangedFiles int   `json:"maximum_changed_files,omitempty"`
	MaximumTreeEntries  int   `json:"maximum_tree_entries,omitempty"`
	MaximumPatchBytes   int64 `json:"maximum_patch_bytes,omitempty"`
	MayTruncateCompare  bool  `json:"may_truncate_compare,omitempty"`
	MayTruncateTree     bool  `json:"may_truncate_tree,omitempty"`
}

// Capabilities is intentionally explicit. A false value is meaningful and
// prevents callers from inferring functionality from a provider name.
type Capabilities struct {
	GitBackend           string                `json:"git_backend"`
	CollaborationBackend string                `json:"collaboration_backend"`
	MirrorBackend        string                `json:"mirror_backend,omitempty"`
	CIBackend            string                `json:"ci_backend,omitempty"`
	Clone                CloneDescriptor       `json:"clone"`
	SmartHTTP            SmartHTTPCapabilities `json:"smart_http"`
	Browse               bool                  `json:"browse"`
	Compare              bool                  `json:"compare"`
	RefEvents            bool                  `json:"ref_events"`
	Provision            bool                  `json:"provision"`
	Import               bool                  `json:"import"`
	Issues               bool                  `json:"issues"`
	PullRequests         bool                  `json:"pull_requests"`
	Reviews              bool                  `json:"reviews"`
	Merge                bool                  `json:"merge"`
	Mirror               bool                  `json:"mirror"`
	CI                   bool                  `json:"ci"`
	Limits               Limits                `json:"limits"`
}

type Response struct {
	Status int
	Header http.Header
	Body   io.ReadCloser
}

type RefsRequest struct {
	Kind      string
	Principal string
}

type RevisionRequest struct {
	Revision  string
	Path      string
	Principal string
}

type CommitsRequest struct {
	Ref       string
	Path      string
	Skip      int
	Limit     int
	Principal string
}

type CompareRequest struct {
	Base          string
	Head          string
	UseMergeBase  bool
	MaxFiles      int
	MaxPatchBytes int64
	Principal     string
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
	Base       string        `json:"base"`
	Head       string        `json:"head"`
	MergeBase  string        `json:"merge_base,omitempty"`
	Files      []CompareFile `json:"files"`
	Patch      string        `json:"patch"`
	Truncated  bool          `json:"truncated,omitempty"`
	Incomplete []string      `json:"incomplete,omitempty"`
}

type GitBackend interface {
	Name() string
	Capabilities(context.Context, Repository) (Capabilities, error)
	CloneDescriptor(context.Context, Repository) (CloneDescriptor, error)
	DefaultBranch(context.Context, Repository, string) (string, error)
	Refs(context.Context, Repository, RefsRequest) (Response, error)
	Resolve(context.Context, Repository, RevisionRequest) (string, error)
	Tree(context.Context, Repository, RevisionRequest) (Response, error)
	Blob(context.Context, Repository, RevisionRequest) (Response, error)
	Commits(context.Context, Repository, CommitsRequest) (Response, error)
	Commit(context.Context, Repository, RevisionRequest) (Response, error)
	Compare(context.Context, Repository, CompareRequest) (CompareResult, error)
}

type RepositoryProvisioner interface {
	EnsureRepository(context.Context, Repository) error
}

type RepositoryImporter interface {
	ImportRepository(context.Context, Repository, ImportRequest) (ImportResult, error)
}

type ImportRequest struct {
	SourceCloneURL string
	ExpectedRefs   map[string]string
}

type ImportResult struct {
	Refs map[string]string `json:"refs"`
}

type SmartHTTPBackend interface {
	ServeGitHTTP(http.ResponseWriter, *http.Request, Repository, string, string)
}

// RepositoryAPIBackend preserves the normalized v1 streaming contract. It is
// optional because a backend may expose Git objects without offering the
// relay's browser route shape.
type RepositoryAPIBackend interface {
	ServeRepositoryAPI(http.ResponseWriter, *http.Request, Repository, string, string)
}

type CollaborationCommand struct {
	OperationID       string
	Kind              string
	Actor             nostr.PubKey
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
	BaseRef          string   `json:"base_ref,omitempty"`
	MergedCommit     string   `json:"merged_commit,omitempty"`
	ImportedRoots    int      `json:"imported_roots,omitempty"`
	ImportedComments int      `json:"imported_comments,omitempty"`
	Truncated        bool     `json:"truncated,omitempty"`
	Incomplete       []string `json:"incomplete,omitempty"`
}

type CollaborationBackend interface {
	Name() string
	Capabilities(context.Context, Repository) (Capabilities, error)
	Execute(context.Context, CollaborationCommand) (CollaborationResult, error)
}

type MirrorProjection struct {
	OperationID string
	Kind        string
	Repository  Repository
	Event       nostr.Event
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
}

type CIRun struct {
	ProviderRunID string    `json:"provider_run_id"`
	Commit        string    `json:"commit"`
	Status        string    `json:"status"`
	URL           string    `json:"url,omitempty"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type CIBackend interface {
	Name() string
	Start(context.Context, CIRequest) (CIRun, error)
	Status(context.Context, Repository, string) (CIRun, error)
	Cancel(context.Context, Repository, string) error
}

type RepositoryIntegrationResolver interface {
	ResolveIntegration(context.Context, projecttask.RepositoryCoordinate) (ResolvedRepository, bool, error)
}

type ResolvedRepository struct {
	Repository    Repository
	Capabilities  Capabilities
	Git           GitBackend
	Collaboration CollaborationBackend
	Mirror        MirrorBackend
	CI            CIBackend
}
