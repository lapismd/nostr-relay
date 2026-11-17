package forge

import (
	"context"
	"time"
	"zooid/internal/projecttask"
)

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
