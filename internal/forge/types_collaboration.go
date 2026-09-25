package forge

import (
	"context"
	"encoding/json"
	"fiatjaf.com/nostr"
	"net/http"
	"time"
)

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
type BranchProtectionRule struct {
	ID                string                   `json:"id"`
	Name              string                   `json:"name"`
	Enforcement       string                   `json:"enforcement"`
	Targets           []BranchProtectionTarget `json:"targets"`
	Bypass            []BranchProtectionBypass `json:"bypass"`
	RestrictCreations bool                     `json:"restrict_creations"`
	RestrictUpdates   bool                     `json:"restrict_updates"`
	RestrictDeletions bool                     `json:"restrict_deletions"`
	RequiredApprovals int                      `json:"required_approvals"`
}

type BranchProtectionTarget struct {
	Type    string `json:"type"`
	Pattern string `json:"pattern,omitempty"`
}

type BranchProtectionBypass struct {
	Kind   string `json:"kind"`
	Pubkey string `json:"pubkey,omitempty"`
}

type BranchProtection struct {
	DefaultBranch        string                 `json:"default_branch,omitempty"`
	ProtectedRef         string                 `json:"protected_ref,omitempty"`
	ProtectedPatterns    []string               `json:"protected_patterns,omitempty"`
	DocumentPatterns     []string               `json:"document_patterns,omitempty"`
	RequiredApprovals    int                    `json:"required_approvals,omitempty"`
	AdminBypass          bool                   `json:"admin_bypass,omitempty"`
	PullRequestsRequired bool                   `json:"pull_requests_required"`
	MergeActors          string                 `json:"merge_actors,omitempty"`
	GitHubAuthoritative  bool                   `json:"github_authoritative,omitempty"`
	Rules                []BranchProtectionRule `json:"rules,omitempty"`
}

type ProtectedRefMergeRequest struct {
	Principal    string
	TargetRef    string
	ExpectedHead string
	TargetSHA    string
	Method       string
	Title        string
	Message      string
	ReviewRef    string
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
