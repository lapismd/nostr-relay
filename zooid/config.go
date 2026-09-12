package zooid

import (
	"fmt"
	"log"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"github.com/BurntSushi/toml"
	"zooid/internal/githubforge"
)

type Role struct {
	Pubkeys   []string `toml:"pubkeys"`
	CanInvite bool     `toml:"can_invite"`
	CanManage bool     `toml:"can_manage"`
}

type GitConfig struct {
	Enabled           bool   `toml:"enabled"`
	PublicURL         string `toml:"public_url"`
	BackendURL        string `toml:"backend_url"`
	ReconcileInterval string `toml:"reconcile_interval"`
	MaxDiffBytes      int64  `toml:"max_diff_bytes"`
	MaxDiffFiles      int    `toml:"max_diff_files"`
}

type GitHubConfig struct {
	Enabled           bool   `toml:"enabled"`
	CredentialDir     string `toml:"credential_dir"`
	WorkerInterval    string `toml:"worker_interval"`
	ReconcileInterval string `toml:"reconcile_interval"`
	MaxWebhookBytes   int64  `toml:"max_webhook_bytes"`
}

type OAuthProviderConfig struct {
	ClientID        string `toml:"client_id"`
	ClientSecretRef string `toml:"client_secret_ref"`
}

type AuthTestingConfig struct {
	GitHubAuthorizationURL string `toml:"github_authorization_url"`
	GitHubTokenURL         string `toml:"github_token_url"`
	GitHubUserURL          string `toml:"github_user_url"`
	GoogleIssuerURL        string `toml:"google_issuer_url"`
}

type AuthConfig struct {
	Enabled               bool                `toml:"enabled"`
	PublicURL             string              `toml:"public_url"`
	AllowedBrowserOrigins []string            `toml:"allowed_browser_origins"`
	WebAuthnRPID          string              `toml:"webauthn_rp_id"`
	WebAuthnOrigins       []string            `toml:"webauthn_origins"`
	CredentialDir         string              `toml:"credential_dir"`
	MasterKeyRef          string              `toml:"master_key_ref"`
	MasterKeyVersion      string              `toml:"master_key_version"`
	SessionLifetime       string              `toml:"session_lifetime"`
	SessionIdle           string              `toml:"session_idle"`
	ChallengeTTL          string              `toml:"challenge_ttl"`
	GitHub                OAuthProviderConfig `toml:"github"`
	Google                OAuthProviderConfig `toml:"google"`
	Testing               AuthTestingConfig   `toml:"testing"`
}

type AgentConfig struct {
	Enabled                bool              `toml:"enabled"`
	AllowOwnerAuth         bool              `toml:"allow_owner_auth"`
	EnforceCredentialKinds bool              `toml:"enforce_credential_kinds"`
	Bunker                 AgentBunkerConfig `toml:"bunker"`
}

type AgentBunkerConfig struct {
	Enabled               bool     `toml:"enabled"`
	PublicURL             string   `toml:"public_url"`
	AllowedBrowserOrigins []string `toml:"allowed_browser_origins"`
	CredentialDir         string   `toml:"credential_dir"`
	MasterKeyRef          string   `toml:"master_key_ref"`
	MasterKeyVersion      string   `toml:"master_key_version"`
	InvitationLifetime    string   `toml:"invitation_lifetime"`
	MaxActivePerOwner     int      `toml:"max_active_per_owner"`
	MaxPendingPerOwner    int      `toml:"max_pending_per_owner"`
	CreationsPerHour      int      `toml:"creations_per_hour"`
	RequestsPerMinute     int      `toml:"requests_per_minute"`
}

type Config struct {
	Host   string `toml:"host"`
	Schema string `toml:"schema"`
	Secret string `toml:"secret"`
	Info   struct {
		Name        string `toml:"name"`
		Icon        string `toml:"icon"`
		Pubkey      string `toml:"pubkey"`
		Description string `toml:"description"`
	} `toml:"info"`

	Policy struct {
		KeytrStorage    bool `toml:"keytr_storage"`  // Public encrypted Keytr records on open relays; opt-in
		Open            bool `toml:"open"`           // Allow all authenticated users (no membership required)
		AnonymousRead   bool `toml:"anonymous_read"` // Allow read-only access to public community data without NIP-42
		PublicJoin      bool `toml:"public_join"`
		StripSignatures bool `toml:"strip_signatures"`
	} `toml:"policy"`

	Groups struct {
		Enabled                 bool `toml:"enabled"`
		AutoJoin                bool `toml:"auto_join"`
		AdminCreateOnly         bool `toml:"admin_create_only"`          // Only admins can create groups
		PrivateAdminOnly        bool `toml:"private_admin_only"`         // Only admins can create private groups
		PrivateRelayAdminAccess bool `toml:"private_relay_admin_access"` // Relay admins can see and moderate private groups
		Retention               struct {
			Default string            `toml:"default"` // Default retention duration (e.g. "7d", "24h"); empty = unlimited
			Groups  map[string]string `toml:"groups"`  // Per-group retention overrides keyed by group ID
		} `toml:"retention"`
	} `toml:"groups"`

	Management struct {
		Enabled bool     `toml:"enabled"`
		Methods []string `toml:"methods"`
	} `toml:"management"`

	Blossom struct {
		Enabled   bool   `toml:"enabled"`
		PublicURL string `toml:"public_url"`
	} `toml:"blossom"`

	Registry struct {
		Enabled        bool   `toml:"enabled"`
		ReleaseGroupID string `toml:"release_group_id"`
		CuratorRole    string `toml:"curator_role"`
	} `toml:"registry"`

	Agents AgentConfig `toml:"agents"`

	Git    GitConfig    `toml:"git"`
	GitHub GitHubConfig `toml:"github"`
	Auth   AuthConfig   `toml:"auth"`

	Roles map[string]Role `toml:"roles"`

	// Private/parsed values
	path   string
	secret nostr.SecretKey
}

func LoadConfig(filename string) (*Config, error) {
	path := filepath.Join(Env("CONFIG"), filename)

	config, err := decodeConfigFile(path)
	if err != nil {
		return nil, fmt.Errorf("Failed to parse config file %s: %w", path, err)
	}

	if config.Host == "" {
		return nil, fmt.Errorf("host is required")
	}

	if config.Schema == "" {
		return nil, fmt.Errorf("schema is required")
	}

	// Validate retention config early so operators get immediate feedback
	if err := config.validateRetention(); err != nil {
		return nil, fmt.Errorf("invalid retention config in %s: %w", path, err)
	}
	if config.Registry.Enabled {
		if strings.TrimSpace(config.Registry.ReleaseGroupID) == "" {
			return nil, fmt.Errorf("registry.release_group_id is required when registry policy is enabled")
		}
		if strings.TrimSpace(config.Registry.CuratorRole) == "" {
			return nil, fmt.Errorf("registry.curator_role is required when registry policy is enabled")
		}
	}
	if err := config.validateGit(); err != nil {
		return nil, fmt.Errorf("invalid git config in %s: %w", path, err)
	}
	if err := config.validateGitHub(); err != nil {
		return nil, fmt.Errorf("invalid github config in %s: %w", path, err)
	}
	if err := config.validateAuth(); err != nil {
		return nil, fmt.Errorf("invalid auth config in %s: %w", path, err)
	}
	if err := config.validateAgentBunker(); err != nil {
		return nil, fmt.Errorf("invalid agents.bunker config in %s: %w", path, err)
	}

	secret, err := nostr.SecretKeyFromHex(config.Secret)
	if err != nil {
		return nil, err
	}

	// Save the path for later
	config.path = path

	// Make the secret... secret
	config.Secret = ""
	config.secret = secret

	return &config, nil
}

func decodeConfigFile(path string) (Config, error) {
	var config Config
	config.Policy.AnonymousRead = true
	config.Agents.Enabled = true
	config.Agents.AllowOwnerAuth = true
	config.Agents.EnforceCredentialKinds = true
	config.Agents.Bunker.CredentialDir = "/run/secrets"
	config.Agents.Bunker.MasterKeyVersion = "v1"
	config.Agents.Bunker.InvitationLifetime = "24h"
	config.Agents.Bunker.MaxActivePerOwner = 10
	config.Agents.Bunker.MaxPendingPerOwner = 3
	config.Agents.Bunker.CreationsPerHour = 5
	config.Agents.Bunker.RequestsPerMinute = 120
	config.Git.ReconcileInterval = "5s"
	config.Git.MaxDiffBytes = 8 << 20
	config.Git.MaxDiffFiles = 2000
	config.GitHub.CredentialDir = "/run/secrets"
	config.GitHub.WorkerInterval = "2s"
	config.GitHub.ReconcileInterval = "5m"
	config.GitHub.MaxWebhookBytes = 4 << 20
	config.Auth.CredentialDir = "/run/secrets"
	config.Auth.MasterKeyVersion = "v1"
	config.Auth.SessionLifetime = "168h"
	config.Auth.SessionIdle = "24h"
	config.Auth.ChallengeTTL = "10m"
	_, err := toml.DecodeFile(path, &config)
	return config, err
}

func (config *Config) validateAgentBunker() error {
	value := config.Agents.Bunker
	if !value.Enabled {
		return nil
	}
	parsed, err := url.Parse(strings.TrimSpace(value.PublicURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLocalDevelopmentHost(parsed.Hostname()))) {
		return fmt.Errorf("public_url must be HTTPS, except for loopback development")
	}
	if !filepath.IsAbs(value.CredentialDir) {
		return fmt.Errorf("credential_dir must be absolute")
	}
	if err := githubforge.ValidateCredentialReference(value.MasterKeyRef); err != nil {
		return fmt.Errorf("invalid master_key_ref")
	}
	if strings.TrimSpace(value.MasterKeyVersion) == "" {
		return fmt.Errorf("master_key_version is required")
	}
	if duration, err := time.ParseDuration(value.InvitationLifetime); err != nil || duration <= 0 {
		return fmt.Errorf("invitation_lifetime must be positive")
	}
	if value.MaxActivePerOwner <= 0 || value.MaxPendingPerOwner <= 0 || value.CreationsPerHour <= 0 || value.RequestsPerMinute <= 0 {
		return fmt.Errorf("quotas and rate limits must be positive")
	}
	seen := map[string]struct{}{}
	for _, raw := range value.AllowedBrowserOrigins {
		origin, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || origin.Scheme == "" || origin.Host == "" || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" || (origin.Path != "" && origin.Path != "/") {
			return fmt.Errorf("allowed_browser_origins entries must be absolute origins")
		}
		if origin.Scheme != "https" && !(origin.Scheme == "http" && isLocalDevelopmentHost(origin.Hostname())) {
			return fmt.Errorf("allowed browser origins must use HTTPS, except for localhost development")
		}
		normalized := strings.ToLower(origin.Scheme + "://" + origin.Host)
		if _, ok := seen[normalized]; ok {
			return fmt.Errorf("allowed browser origins must be unique")
		}
		seen[normalized] = struct{}{}
	}
	return nil
}

func (config *Config) AgentBunkerInvitationLifetime() time.Duration {
	duration, _ := time.ParseDuration(config.Agents.Bunker.InvitationLifetime)
	return duration
}

func (config *Config) validateAuth() error {
	if !config.Auth.Enabled {
		return nil
	}
	parsed, err := url.Parse(strings.TrimSpace(config.Auth.PublicURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLocalDevelopmentHost(parsed.Hostname()))) {
		return fmt.Errorf("auth.public_url must be HTTPS, except for loopback development")
	}
	if !filepath.IsAbs(config.Auth.CredentialDir) {
		return fmt.Errorf("auth.credential_dir must be absolute")
	}
	if strings.TrimSpace(config.Auth.MasterKeyRef) == "" || strings.TrimSpace(config.Auth.MasterKeyVersion) == "" {
		return fmt.Errorf("auth master key reference and version are required")
	}
	for name, raw := range map[string]string{
		"session_lifetime": config.Auth.SessionLifetime,
		"session_idle":     config.Auth.SessionIdle,
		"challenge_ttl":    config.Auth.ChallengeTTL,
	} {
		if duration, err := time.ParseDuration(raw); err != nil || duration <= 0 {
			return fmt.Errorf("auth.%s must be a positive duration", name)
		}
	}
	for name, provider := range map[string]OAuthProviderConfig{"github": config.Auth.GitHub, "google": config.Auth.Google} {
		if (provider.ClientID == "") != (provider.ClientSecretRef == "") {
			return fmt.Errorf("auth.%s client ID and secret reference must be configured together", name)
		}
	}
	seenOrigins := make(map[string]struct{}, len(config.Auth.AllowedBrowserOrigins))
	for _, value := range config.Auth.AllowedBrowserOrigins {
		origin, err := url.Parse(strings.TrimSpace(value))
		if err != nil || origin.Scheme == "" || origin.Host == "" || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" || (origin.Path != "" && origin.Path != "/") {
			return fmt.Errorf("auth.allowed_browser_origins entries must be absolute origins")
		}
		localhost := isLocalDevelopmentHost(origin.Hostname())
		if origin.Scheme != "https" && !(origin.Scheme == "http" && localhost) {
			return fmt.Errorf("auth.allowed_browser_origins entries must use HTTPS, except for localhost development")
		}
		normalized := origin.Scheme + "://" + origin.Host
		if _, duplicate := seenOrigins[normalized]; duplicate {
			return fmt.Errorf("auth.allowed_browser_origins entries must be unique")
		}
		seenOrigins[normalized] = struct{}{}
	}
	webAuthnRPID := strings.ToLower(strings.TrimSpace(config.Auth.WebAuthnRPID))
	if webAuthnRPID == "" {
		webAuthnRPID = strings.ToLower(parsed.Hostname())
	}
	if strings.ContainsAny(webAuthnRPID, "/:@") || strings.HasPrefix(webAuthnRPID, ".") || strings.HasSuffix(webAuthnRPID, ".") {
		return fmt.Errorf("auth.webauthn_rp_id must be a hostname")
	}
	webAuthnOrigins := config.Auth.WebAuthnOrigins
	if len(webAuthnOrigins) == 0 {
		webAuthnOrigins = []string{parsed.Scheme + "://" + parsed.Host}
	}
	seenWebAuthnOrigins := make(map[string]struct{}, len(webAuthnOrigins))
	for _, value := range webAuthnOrigins {
		origin, err := url.Parse(strings.TrimSpace(value))
		if err != nil || origin.Scheme == "" || origin.Host == "" || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" || (origin.Path != "" && origin.Path != "/") {
			return fmt.Errorf("auth.webauthn_origins entries must be absolute origins")
		}
		localhost := isLocalDevelopmentHost(origin.Hostname())
		if origin.Scheme != "https" && !(origin.Scheme == "http" && localhost) {
			return fmt.Errorf("auth.webauthn_origins entries must use HTTPS, except for localhost development")
		}
		normalized := strings.ToLower(origin.Scheme + "://" + origin.Host)
		publicOrigin := strings.ToLower(parsed.Scheme + "://" + parsed.Host)
		if normalized != publicOrigin {
			if _, allowed := seenOrigins[normalized]; !allowed {
				return fmt.Errorf("auth.webauthn_origins entries must also be allowed browser origins")
			}
		}
		hostname := strings.ToLower(origin.Hostname())
		if hostname != webAuthnRPID && !strings.HasSuffix(hostname, "."+webAuthnRPID) {
			return fmt.Errorf("auth.webauthn origin %q is not compatible with RP ID %q", normalized, webAuthnRPID)
		}
		if _, duplicate := seenWebAuthnOrigins[normalized]; duplicate {
			return fmt.Errorf("auth.webauthn_origins entries must be unique")
		}
		seenWebAuthnOrigins[normalized] = struct{}{}
	}
	if err := config.validateAuthTesting(parsed); err != nil {
		return err
	}
	return nil
}

func (config *Config) validateAuthTesting(publicURL *url.URL) error {
	testing := config.Auth.Testing
	githubValues := []string{testing.GitHubAuthorizationURL, testing.GitHubTokenURL, testing.GitHubUserURL}
	githubCount := 0
	for _, value := range githubValues {
		if strings.TrimSpace(value) != "" {
			githubCount++
		}
	}
	if githubCount != 0 && githubCount != len(githubValues) {
		return fmt.Errorf("auth.testing GitHub endpoints must be configured together")
	}
	if githubCount > 0 && config.Auth.GitHub.ClientID == "" {
		return fmt.Errorf("auth.testing GitHub endpoints require the GitHub provider")
	}
	if testing.GoogleIssuerURL != "" && config.Auth.Google.ClientID == "" {
		return fmt.Errorf("auth.testing Google issuer requires the Google provider")
	}
	values := append(githubValues, testing.GoogleIssuerURL)
	hasOverrides := false
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		hasOverrides = true
		endpoint, err := url.Parse(value)
		if err != nil || endpoint.Scheme == "" || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" ||
			(endpoint.Scheme != "http" && endpoint.Scheme != "https") || !isLocalDevelopmentHost(endpoint.Hostname()) {
			return fmt.Errorf("auth.testing OAuth endpoints must use localhost HTTP(S) URLs")
		}
	}
	if hasOverrides && !isLocalDevelopmentHost(publicURL.Hostname()) {
		return fmt.Errorf("auth.testing OAuth endpoints require a localhost auth.public_url")
	}
	return nil
}

func isLocalDevelopmentHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	return host == "localhost" || host == "127.0.0.1" || host == "::1" || strings.HasSuffix(host, ".localhost")
}

func (config *Config) AuthSessionLifetime() time.Duration {
	duration, _ := time.ParseDuration(config.Auth.SessionLifetime)
	return duration
}

func (config *Config) AuthSessionIdle() time.Duration {
	duration, _ := time.ParseDuration(config.Auth.SessionIdle)
	return duration
}

func (config *Config) AuthChallengeTTL() time.Duration {
	duration, _ := time.ParseDuration(config.Auth.ChallengeTTL)
	return duration
}

func (config *Config) validateGit() error {
	if !config.Git.Enabled {
		return nil
	}
	values := map[string]string{"public_url": config.Git.PublicURL}
	if strings.TrimSpace(config.Git.BackendURL) != "" {
		values["backend_url"] = config.Git.BackendURL
	} else if !config.GitHub.Enabled {
		return fmt.Errorf("git.backend_url is required unless github is enabled")
	}
	for name, raw := range values {
		parsed, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || parsed.Scheme == "" || parsed.Host == "" ||
			(parsed.Scheme != "http" && parsed.Scheme != "https") ||
			parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return fmt.Errorf("git.%s must be an absolute http(s) URL without credentials, query, or fragment", name)
		}
	}
	interval, err := time.ParseDuration(config.Git.ReconcileInterval)
	if err != nil || interval <= 0 {
		return fmt.Errorf("git.reconcile_interval must be a positive duration")
	}
	if config.Git.MaxDiffBytes <= 0 {
		return fmt.Errorf("git.max_diff_bytes must be positive")
	}
	if config.Git.MaxDiffFiles <= 0 {
		return fmt.Errorf("git.max_diff_files must be positive")
	}
	return nil
}

func (config *Config) GitReconcileInterval() time.Duration {
	interval, err := time.ParseDuration(config.Git.ReconcileInterval)
	if err != nil || interval <= 0 {
		return 5 * time.Second
	}
	return interval
}

func (config *Config) validateGitHub() error {
	if !config.GitHub.Enabled {
		return nil
	}
	if !config.Git.Enabled {
		return fmt.Errorf("github.enabled requires git.enabled")
	}
	if !filepath.IsAbs(config.GitHub.CredentialDir) {
		return fmt.Errorf("github.credential_dir must be absolute")
	}
	for name, raw := range map[string]string{
		"worker_interval":    config.GitHub.WorkerInterval,
		"reconcile_interval": config.GitHub.ReconcileInterval,
	} {
		duration, err := time.ParseDuration(raw)
		if err != nil || duration <= 0 {
			return fmt.Errorf("github.%s must be a positive duration", name)
		}
	}
	if config.GitHub.MaxWebhookBytes <= 0 || config.GitHub.MaxWebhookBytes > 64<<20 {
		return fmt.Errorf("github.max_webhook_bytes must be 1..=67108864")
	}
	return nil
}

func (config *Config) GitHubWorkerInterval() time.Duration {
	duration, err := time.ParseDuration(config.GitHub.WorkerInterval)
	if err != nil || duration <= 0 {
		return 2 * time.Second
	}
	return duration
}

func (config *Config) GitHubReconcileInterval() time.Duration {
	duration, err := time.ParseDuration(config.GitHub.ReconcileInterval)
	if err != nil || duration <= 0 {
		return 5 * time.Minute
	}
	return duration
}

func (config *Config) Save() error {
	// Restore the secret key to the public field for saving
	config.Secret = config.secret.Hex()

	file, err := os.Create(config.path)
	if err != nil {
		return fmt.Errorf("Failed to open config file %s: %w", config.path, err)
	}
	defer file.Close()

	encoder := toml.NewEncoder(file)
	if err := encoder.Encode(config); err != nil {
		return fmt.Errorf("Failed to encode config file %s: %w", config.path, err)
	}

	// Clear the secret again
	config.Secret = ""

	return nil
}

func (config *Config) SetName(name string) error {
	config.Info.Name = name

	return config.Save()
}

func (config *Config) SetDescription(description string) error {
	config.Info.Description = description

	return config.Save()
}

func (config *Config) SetIcon(icon string) error {
	config.Info.Icon = icon

	return config.Save()
}

func (config *Config) Sign(event *nostr.Event) error {
	return event.Sign(config.secret)
}

func (config *Config) GetSelf() nostr.PubKey {
	return config.secret.Public()
}

func (config *Config) IsSelf(pubkey nostr.PubKey) bool {
	return pubkey == config.GetSelf()
}

func (config *Config) GetOwner() nostr.PubKey {
	return nostr.MustPubKeyFromHex(config.Info.Pubkey)
}

func (config *Config) IsOwner(pubkey nostr.PubKey) bool {
	return pubkey == config.GetOwner()
}

func (config *Config) GetAssignedRoles(pubkey nostr.PubKey) []Role {
	roles := make([]Role, 0)
	for _, role := range config.Roles {
		if slices.Contains(role.Pubkeys, pubkey.Hex()) {
			roles = append(roles, role)
		}
	}

	return roles
}

func (config *Config) GetAllRoles(pubkey nostr.PubKey) []Role {
	roles := make([]Role, 0)
	for name, role := range config.Roles {
		if name == "member" {
			roles = append(roles, role)
		} else if slices.Contains(role.Pubkeys, pubkey.Hex()) {
			roles = append(roles, role)
		}
	}

	return roles
}

func (config *Config) CanInvite(pubkey nostr.PubKey) bool {
	if config.IsOwner(pubkey) || config.IsSelf(pubkey) {
		return true
	}

	for _, role := range config.GetAllRoles(pubkey) {
		if role.CanInvite {
			return true
		}
	}

	return false
}

func (config *Config) CanManage(pubkey nostr.PubKey) bool {
	if config.IsOwner(pubkey) || config.IsSelf(pubkey) {
		return true
	}

	for _, role := range config.GetAllRoles(pubkey) {
		if role.CanManage {
			return true
		}
	}

	return false
}

// ParseRetentionDuration parses a retention duration string like "30s", "5m", "24h", "7d".
// Returns 0 for empty strings (meaning unlimited). Supports s(econds), m(inutes), h(ours), d(ays).
func ParseRetentionDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}

	if len(s) < 2 {
		return 0, fmt.Errorf("invalid retention duration: %q", s)
	}

	unit := s[len(s)-1]
	valueStr := s[:len(s)-1]

	value, err := strconv.ParseInt(valueStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid retention duration: %q", s)
	}

	if value <= 0 {
		return 0, fmt.Errorf("retention duration must be positive: %q", s)
	}

	var multiplier time.Duration
	switch unit {
	case 's':
		multiplier = time.Second
	case 'm':
		multiplier = time.Minute
	case 'h':
		multiplier = time.Hour
	case 'd':
		multiplier = 24 * time.Hour
	default:
		return 0, fmt.Errorf("invalid retention duration unit %q in %q (use s, m, h, or d)", string(unit), s)
	}

	// Guard against int64 overflow: max time.Duration is ~292 years in nanoseconds.
	maxValue := math.MaxInt64 / int64(multiplier)
	if value > maxValue {
		return 0, fmt.Errorf("retention duration too large: %q", s)
	}

	return time.Duration(value) * multiplier, nil
}

// validateRetention checks all retention duration strings at config load time.
func (config *Config) validateRetention() error {
	if config.Groups.Retention.Default != "" {
		if _, err := ParseRetentionDuration(config.Groups.Retention.Default); err != nil {
			return fmt.Errorf("default: %w", err)
		}
	}
	for groupID, s := range config.Groups.Retention.Groups {
		if _, err := ParseRetentionDuration(s); err != nil {
			return fmt.Errorf("group %q: %w", groupID, err)
		}
	}
	return nil
}

// HasRetention returns true if any effective retention policy is configured
// (a non-empty default or at least one per-group override that parses to a positive duration).
func (config *Config) HasRetention() bool {
	if d, err := ParseRetentionDuration(config.Groups.Retention.Default); err == nil && d > 0 {
		return true
	}
	for _, s := range config.Groups.Retention.Groups {
		if d, err := ParseRetentionDuration(s); err == nil && d > 0 {
			return true
		}
	}
	return false
}

// GetRetention returns the retention duration for a group. Per-group overrides
// take precedence over the default. Returns 0 (unlimited) if no retention is configured.
// Since values are validated at config load time, parse errors here are unexpected
// and logged as warnings.
func (config *Config) GetRetention(groupID string) time.Duration {
	if config.Groups.Retention.Groups != nil {
		if s, ok := config.Groups.Retention.Groups[groupID]; ok {
			d, err := ParseRetentionDuration(s)
			if err != nil {
				log.Printf("retention: unexpected invalid duration for group %q: %v", groupID, err)
			} else if d > 0 {
				return d
			}
		}
	}

	d, err := ParseRetentionDuration(config.Groups.Retention.Default)
	if err != nil {
		log.Printf("retention: unexpected invalid default duration: %v", err)
		return 0
	}
	return d
}
