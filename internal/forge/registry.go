package forge

import (
	"context"
	"fmt"
	"sync"
)

// Registry owns process-local provider implementations. Durable attachment is
// represented by Integration and resolved independently by Zooid.
type Registry struct {
	mu            sync.RWMutex
	git           map[string]GitBackend
	collaboration map[string]CollaborationBackend
	mirror        map[string]MirrorBackend
	ci            map[string]CIBackend
}

func NewRegistry() *Registry {
	return &Registry{
		git: make(map[string]GitBackend), collaboration: make(map[string]CollaborationBackend),
		mirror: make(map[string]MirrorBackend), ci: make(map[string]CIBackend),
	}
}

func (registry *Registry) RegisterGit(backend GitBackend) error {
	if backend == nil || backend.Name() == "" {
		return fmt.Errorf("forge Git backend requires a name")
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, exists := registry.git[backend.Name()]; exists {
		return fmt.Errorf("forge Git backend %q is already registered", backend.Name())
	}
	registry.git[backend.Name()] = backend
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

func (registry *Registry) Resolve(ctx context.Context, repository Repository) (ResolvedRepository, error) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	result := ResolvedRepository{Repository: repository}
	integration := repository.Integration

	if integration.GitBackend != "" && integration.GitBackend != BackendNone && integration.GitBackend != BackendUnmanaged {
		result.Git = registry.git[integration.GitBackend]
		if result.Git == nil {
			return result, &Error{Status: 503, Code: "git_backend_unavailable", Retryable: true}
		}
		capabilities, err := result.Git.Capabilities(ctx, repository)
		if err != nil {
			return result, err
		}
		result.Capabilities = capabilities
		// Collaboration, mirroring, and CI are selected independently. A Git
		// adapter implementing those interfaces does not enable them by itself.
		result.Capabilities.Issues = false
		result.Capabilities.PullRequests = false
		result.Capabilities.Reviews = false
		result.Capabilities.Merge = false
		result.Capabilities.Mirror = false
		result.Capabilities.CI = false
	} else {
		result.Capabilities.GitBackend = integration.GitBackend
		if integration.GitBackend == BackendUnmanaged && repository.CloneURL != "" {
			result.Capabilities.Clone = CloneDescriptor{
				Available: true, URL: repository.CloneURL, Auth: CloneAuthProvider,
			}
		}
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
