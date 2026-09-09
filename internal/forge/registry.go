package forge

import (
	"context"
	"fmt"
	"sync"
)

// Registry owns process-local provider implementations. Durable attachment is
// represented by Integration and resolved independently by Zooid.
type Registry struct {
	mu                 sync.RWMutex
	authority          map[string]GitAuthorityBackend
	content            map[string]RepositoryContentBackend
	collaboration      map[string]CollaborationBackend
	mirror             map[string]MirrorBackend
	ci                 map[string]CIBackend
	replicationTargets map[string]GitReplicationTargetBackend
}

func NewRegistry() *Registry {
	return &Registry{
		authority: make(map[string]GitAuthorityBackend), content: make(map[string]RepositoryContentBackend),
		collaboration: make(map[string]CollaborationBackend),
		mirror:        make(map[string]MirrorBackend), ci: make(map[string]CIBackend),
		replicationTargets: make(map[string]GitReplicationTargetBackend),
	}
}

func (registry *Registry) RegisterGitReplicationTarget(backend GitReplicationTargetBackend) error {
	if backend == nil || backend.Name() == "" {
		return fmt.Errorf("forge Git replication target requires a name")
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, exists := registry.replicationTargets[backend.Name()]; exists {
		return fmt.Errorf("forge Git replication target %q is already registered", backend.Name())
	}
	registry.replicationTargets[backend.Name()] = backend
	return nil
}

func (registry *Registry) GitReplicationTarget(name string) GitReplicationTargetBackend {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	return registry.replicationTargets[name]
}

// RegisterGit is a compatibility registration helper. Backends implement the
// small authority and/or content interfaces; registration never requires one
// provider-sized interface.
func (registry *Registry) RegisterGit(backend any) error {
	authority, hasAuthority := backend.(GitAuthorityBackend)
	content, hasContent := backend.(RepositoryContentBackend)
	if !hasAuthority && !hasContent {
		return fmt.Errorf("forge backend must implement Git authority or repository content")
	}
	name := ""
	if hasAuthority {
		name = authority.Name()
	} else {
		name = content.Name()
	}
	if name == "" || (hasAuthority && authority.Name() != name) || (hasContent && content.Name() != name) {
		return fmt.Errorf("forge Git backend requires one stable name")
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, exists := registry.authority[name]; exists && hasAuthority {
		return fmt.Errorf("forge Git authority backend %q is already registered", name)
	}
	if _, exists := registry.content[name]; exists && hasContent {
		return fmt.Errorf("forge repository content backend %q is already registered", name)
	}
	if hasAuthority {
		registry.authority[name] = authority
	}
	if hasContent {
		registry.content[name] = content
	}
	return nil
}

func (registry *Registry) RegisterCollaboration(backend CollaborationBackend) error {
	if backend == nil || backend.Name() == "" {
		return fmt.Errorf("forge collaboration backend requires a name")
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, exists := registry.collaboration[backend.Name()]; exists {
		return fmt.Errorf("forge collaboration backend %q is already registered", backend.Name())
	}
	registry.collaboration[backend.Name()] = backend
	return nil
}

func (registry *Registry) RegisterMirror(backend MirrorBackend) error {
	if backend == nil || backend.Name() == "" {
		return fmt.Errorf("forge mirror backend requires a name")
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, exists := registry.mirror[backend.Name()]; exists {
		return fmt.Errorf("forge mirror backend %q is already registered", backend.Name())
	}
	registry.mirror[backend.Name()] = backend
	return nil
}

func (registry *Registry) RegisterCI(backend CIBackend) error {
	if backend == nil || backend.Name() == "" {
		return fmt.Errorf("forge CI backend requires a name")
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, exists := registry.ci[backend.Name()]; exists {
		return fmt.Errorf("forge CI backend %q is already registered", backend.Name())
	}
	registry.ci[backend.Name()] = backend
	return nil
}

func (registry *Registry) AuthorityBackend(name string) GitAuthorityBackend {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	return registry.authority[name]
}

func (registry *Registry) ContentBackend(name string) RepositoryContentBackend {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	return registry.content[name]
}

func (registry *Registry) Resolve(ctx context.Context, repository Repository) (ResolvedRepository, error) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	result := ResolvedRepository{Repository: repository}
	integration := repository.Integration
	authorityName := integration.GitAuthorityBackend
	contentName := integration.ContentBackend
	if authorityName == "" {
		authorityName = integration.GitBackend
	}
	if contentName == "" {
		contentName = integration.GitBackend
	}

	result.Capabilities.GitBackend = integration.GitBackend
	result.Capabilities.GitAuthorityBackend = authorityName
	result.Capabilities.ContentBackend = contentName
	if authorityName != "" && authorityName != BackendNone && authorityName != BackendUnmanaged {
		result.Authority = registry.authority[authorityName]
		if result.Authority == nil {
			return result, &Error{Status: 503, Code: "git_authority_backend_unavailable", Retryable: true}
		}
		capabilities, err := result.Authority.AuthorityCapabilities(ctx, repository)
		if err != nil {
			return result, err
		}
		result.Capabilities.Clone = capabilities.Clone
		result.Capabilities.SmartHTTP = capabilities.SmartHTTP
		result.Capabilities.RefEvents = capabilities.RefEvents
		result.Capabilities.Provision = capabilities.Provision
		result.Capabilities.Import = capabilities.Import
		// Collaboration, mirroring, and CI are selected independently. A Git
		// adapter implementing those interfaces does not enable them by itself.
		result.Capabilities.Issues = false
		result.Capabilities.PullRequests = false
		result.Capabilities.Reviews = false
		result.Capabilities.Merge = false
		result.Capabilities.Mirror = false
		result.Capabilities.CI = false
	} else if authorityName == BackendUnmanaged && repository.CloneURL != "" {
		result.Capabilities.Clone = CloneDescriptor{
			Available: true, URL: repository.CloneURL, Auth: CloneAuthProvider,
		}
	}

	if contentName != "" && contentName != BackendNone && contentName != BackendUnmanaged {
		result.Content = registry.content[contentName]
		if result.Content == nil {
			return result, &Error{Status: 503, Code: "content_backend_unavailable", Retryable: true}
		}
		capabilities, err := result.Content.ContentCapabilities(ctx, repository)
		if err != nil {
			return result, err
		}
		result.Capabilities.Browse = capabilities.Browse
		result.Capabilities.Compare = capabilities.Compare
		result.Capabilities.Limits = capabilities.Limits
		result.Capabilities.CapabilityLimits = capabilities.Limits
	}
	if integration.Replica != nil && integration.Replica.Enabled {
		result.Capabilities.LocalReplica = true
		result.Capabilities.ReplicaState = integration.HealthState
	}

	result.Capabilities.CollaborationBackend = integration.CollaborationBackend
	if integration.CollaborationBackend != "" && integration.CollaborationBackend != BackendRelay {
		result.Collaboration = registry.collaboration[integration.CollaborationBackend]
		if result.Collaboration == nil {
			return result, &Error{Status: 503, Code: "collaboration_backend_unavailable", Retryable: true}
		}
		caps, err := result.Collaboration.Capabilities(ctx, repository)
		if err != nil {
			return result, err
		}
		result.Capabilities.Issues = caps.Issues
		result.Capabilities.PullRequests = caps.PullRequests
		result.Capabilities.Reviews = caps.Reviews
		result.Capabilities.Merge = caps.Merge
	}

	result.Capabilities.MirrorBackend = integration.MirrorBackend
	if integration.MirrorBackend != "" && integration.MirrorBackend != BackendNone {
		result.Mirror = registry.mirror[integration.MirrorBackend]
		if result.Mirror == nil {
			return result, &Error{Status: 503, Code: "mirror_backend_unavailable", Retryable: true}
		}
		result.Capabilities.Mirror = true
	}

	result.Capabilities.GitReplicationTargetBackend = integration.GitReplicationTargetBackend
	if integration.GitReplicationTargetBackend != "" && integration.GitReplicationTargetBackend != BackendNone {
		result.ReplicationTarget = registry.replicationTargets[integration.GitReplicationTargetBackend]
		if result.ReplicationTarget == nil {
			return result, &Error{Status: 503, Code: "git_replication_target_unavailable", Retryable: true}
		}
		if _, ok := result.Authority.(GitReplicationSource); !ok {
			return result, &Error{Status: 503, Code: "git_replication_source_unavailable", Retryable: true}
		}
		result.Capabilities.GitReplicationTarget = true
	}

	result.Capabilities.CIBackend = integration.CIBackend
	if integration.CIBackend != "" && integration.CIBackend != BackendNone {
		result.CI = registry.ci[integration.CIBackend]
		if result.CI == nil {
			return result, &Error{Status: 503, Code: "ci_backend_unavailable", Retryable: true}
		}
		result.Capabilities.CI = true
	}
	return result, nil
}
