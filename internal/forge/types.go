// Package forge defines provider-neutral contracts for repository, provider
// collaboration, mirroring, and CI integrations. Provider implementations stay
// behind these interfaces; Nostr authority and NIP-29 policy remain in Zooid.
package forge

import (
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
