package githubforge

import (
	"context"
	"fiatjaf.com/nostr"
	"fmt"
	"net/http"
	"net/url"
	"time"
	"zooid/internal/forge"
)

const (
	maxGitHubResponse = 32 << 20
	maxBrowserBlob    = 2 << 20
)

var gitReadPermissions = map[string]string{"metadata": "read", "contents": "read"}

var gitWritePermissions = map[string]string{"metadata": "read", "contents": "write"}

type Backend struct {
	connections ConnectionLoader
	credentials CredentialStore
	userTokens  UserTokenResolver
	client      *http.Client
	base        *url.URL
	tokens      *tokenManager
	reads       *githubReadCache
}

// UserTokenResolver returns an OAuth token for the Nostr author selected when
// a durable forge operation was enqueued.
type UserTokenResolver interface {
	ResolveGitHubUserToken(context.Context, nostr.PubKey) (string, error)
	MarkGitHubUserReconnectRequired(context.Context, nostr.PubKey) error
}

func (backend *Backend) SetUserTokenResolver(resolver UserTokenResolver) {
	backend.userTokens = resolver
}

func New(connections ConnectionLoader, credentials CredentialStore, client *http.Client) (*Backend, error) {
	base, _ := url.Parse(APIBaseURL)
	return newBackend(connections, credentials, client, base, false)
}

func newBackend(connections ConnectionLoader, credentials CredentialStore, client *http.Client, base *url.URL, allowTestHost bool) (*Backend, error) {
	if connections == nil || credentials == nil {
		return nil, fmt.Errorf("GitHub backend requires connection and credential stores")
	}
	if base == nil || base.Scheme == "" || base.Host == "" || (!allowTestHost && base.String() != APIBaseURL) {
		return nil, fmt.Errorf("GitHub API base must be %s", APIBaseURL)
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &Backend{
		connections: connections, credentials: credentials, client: client, base: base,
		tokens: newTokenManager(credentials, client, base), reads: newGitHubReadCache(),
	}, nil
}

func (*Backend) Name() string { return forge.BackendGitHub }

type RepositoryMetadata struct {
	ID            int64  `json:"id"`
	FullName      string `json:"full_name"`
	Name          string `json:"name"`
	DefaultBranch string `json:"default_branch"`
	CloneURL      string `json:"clone_url"`
	HTMLURL       string `json:"html_url"`
	Private       bool   `json:"private"`
	Archived      bool   `json:"archived"`
	Disabled      bool   `json:"disabled"`
	Owner         struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	} `json:"owner"`
}

func (backend *Backend) AuthorityCapabilities(ctx context.Context, repository forge.Repository) (forge.GitAuthorityCapabilities, error) {
	descriptor, err := backend.CloneDescriptor(ctx, repository)
	if err != nil {
		return forge.GitAuthorityCapabilities{}, err
	}
	return forge.GitAuthorityCapabilities{Clone: descriptor, RefEvents: true}, nil
}

func (*Backend) ContentCapabilities(context.Context, forge.Repository) (forge.ContentCapabilities, error) {
	return forge.ContentCapabilities{Browse: true, Compare: true, Limits: forge.ContentLimits{
		MaximumBlobBytes: 100 << 20, MaximumChangedFiles: 300, MaximumTreeEntries: 1000,
		MayTruncateCompare: true, MayTruncateTree: true,
	}}, nil
}

func (backend *Backend) Capabilities(ctx context.Context, repository forge.Repository) (forge.Capabilities, error) {
	authority, err := backend.AuthorityCapabilities(ctx, repository)
	if err != nil {
		return forge.Capabilities{}, err
	}
	content, _ := backend.ContentCapabilities(ctx, repository)
	return forge.Capabilities{
		GitBackend: forge.BackendGitHub, GitAuthorityBackend: forge.BackendGitHub,
		ContentBackend: forge.BackendGitHub, Clone: authority.Clone,
		Browse: content.Browse, Compare: content.Compare, RefEvents: authority.RefEvents,
		Issues: true, PullRequests: true, Reviews: true, Merge: true,
		Limits: content.Limits, CapabilityLimits: content.Limits,
	}, nil
}

func (backend *Backend) CloneDescriptor(_ context.Context, repository forge.Repository) (forge.CloneDescriptor, error) {
	fullName, err := repositoryName(repository)
	if err != nil {
		return forge.CloneDescriptor{}, err
	}
	return forge.CloneDescriptor{
		Available: true, URL: "https://github.com/" + fullName + ".git",
		Auth: forge.CloneAuthProvider, Provider: forge.BackendGitHub,
	}, nil
}

func (backend *Backend) DefaultBranch(ctx context.Context, repository forge.Repository, _ string) (string, error) {
	metadata, err := backend.Repository(ctx, repository)
	if err != nil {
		return "", err
	}
	if metadata.DefaultBranch == "" {
		return "", &forge.Error{Status: http.StatusBadGateway, Code: "invalid_github_repository"}
	}
	return metadata.DefaultBranch, nil
}
