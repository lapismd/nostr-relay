package zooid

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"fiatjaf.com/nostr"
)

func TestLoadConfig_AnonymousReadDefaultsOnAndCanBeDisabled(t *testing.T) {
	configDir := t.TempDir()

	for _, test := range []struct {
		name     string
		policy   string
		expected bool
	}{
		{name: "default", policy: "", expected: true},
		{name: "disabled", policy: "anonymous_read = false\n", expected: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			filename := test.name + ".toml"
			path := filepath.Join(configDir, filename)
			contents := "[policy]\n" + test.policy
			if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
				t.Fatal(err)
			}

			config, err := decodeConfigFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if config.Policy.AnonymousRead != test.expected {
				t.Fatalf("AnonymousRead = %v, want %v", config.Policy.AnonymousRead, test.expected)
			}
		})
	}
}

func TestConfig_IsOwner(t *testing.T) {
	ownerPubkey := nostr.MustPubKeyFromHex("1234567890123456789012345678901234567890123456789012345678901234")
	otherPubkey := nostr.MustPubKeyFromHex("abcdef1234567890123456789012345678901234567890123456789012345678")

	config := &Config{
		Info: struct {
			Name        string `toml:"name"`
			Icon        string `toml:"icon"`
			Pubkey      string `toml:"pubkey"`
			Description string `toml:"description"`
		}{
			Pubkey: ownerPubkey.Hex(),
		},
	}

	if !config.IsOwner(ownerPubkey) {
		t.Error("IsOwner() should return true for owner pubkey")
	}

	if config.IsOwner(otherPubkey) {
		t.Error("IsOwner() should return false for non-owner pubkey")
	}
}

func TestConfig_IsSelf(t *testing.T) {
	secret := nostr.Generate()
	selfPubkey := secret.Public()
	otherPubkey := nostr.MustPubKeyFromHex("abcdef1234567890123456789012345678901234567890123456789012345678")

	config := &Config{
		secret: secret,
	}

	if !config.IsSelf(selfPubkey) {
		t.Error("IsSelf() should return true for self pubkey")
	}

	if config.IsSelf(otherPubkey) {
		t.Error("IsSelf() should return false for non-self pubkey")
	}
}

func TestConfig_GetAllRoles(t *testing.T) {
	pubkey1 := nostr.MustPubKeyFromHex("1234567890123456789012345678901234567890123456789012345678901234")
	pubkey2 := nostr.MustPubKeyFromHex("abcdef1234567890123456789012345678901234567890123456789012345678")

	config := &Config{
		Roles: map[string]Role{
			"member": {
				Pubkeys:   []string{},
				CanInvite: true,
			},
			"admin": {
				Pubkeys:   []string{pubkey1.Hex()},
				CanManage: true,
			},
			"moderator": {
				Pubkeys:   []string{pubkey2.Hex()},
				CanInvite: true,
			},
		},
	}

	roles := config.GetAllRoles(pubkey1)
	if len(roles) != 2 {
		t.Errorf("GetAllRoles() returned %d roles, want 2", len(roles))
	}

	roles = config.GetAllRoles(pubkey2)
	if len(roles) != 2 {
		t.Errorf("GetAllRoles() returned %d roles, want 2", len(roles))
	}
}

func TestConfig_CanManage(t *testing.T) {
	ownerPubkey := nostr.MustPubKeyFromHex("9999999999999999999999999999999999999999999999999999999999999999")
	adminPubkey := nostr.MustPubKeyFromHex("1234567890123456789012345678901234567890123456789012345678901234")
	userPubkey := nostr.MustPubKeyFromHex("abcdef1234567890123456789012345678901234567890123456789012345678")

	config := &Config{
		secret: nostr.Generate(),
		Info: struct {
			Name        string `toml:"name"`
			Icon        string `toml:"icon"`
			Pubkey      string `toml:"pubkey"`
			Description string `toml:"description"`
		}{
			Pubkey: ownerPubkey.Hex(),
		},
		Roles: map[string]Role{
			"admin": {
				Pubkeys:   []string{adminPubkey.Hex()},
				CanManage: true,
			},
			"user": {
				Pubkeys:   []string{userPubkey.Hex()},
				CanManage: false,
			},
		},
	}

	if !config.CanManage(adminPubkey) {
		t.Error("CanManage() should return true for admin")
	}

	if config.CanManage(userPubkey) {
		t.Error("CanManage() should return false for regular user")
	}
}

func TestConfig_CanInvite(t *testing.T) {
	ownerPubkey := nostr.MustPubKeyFromHex("9999999999999999999999999999999999999999999999999999999999999999")
	inviterPubkey := nostr.MustPubKeyFromHex("1234567890123456789012345678901234567890123456789012345678901234")
	userPubkey := nostr.MustPubKeyFromHex("abcdef1234567890123456789012345678901234567890123456789012345678")

	config := &Config{
		secret: nostr.Generate(),
		Info: struct {
			Name        string `toml:"name"`
			Icon        string `toml:"icon"`
			Pubkey      string `toml:"pubkey"`
			Description string `toml:"description"`
		}{
			Pubkey: ownerPubkey.Hex(),
		},
		Roles: map[string]Role{
			"inviter": {
				Pubkeys:   []string{inviterPubkey.Hex()},
				CanInvite: true,
			},
			"user": {
				Pubkeys:   []string{userPubkey.Hex()},
				CanInvite: false,
			},
		},
	}

	if !config.CanInvite(inviterPubkey) {
		t.Error("CanInvite() should return true for inviter")
	}

	if config.CanInvite(userPubkey) {
		t.Error("CanInvite() should return false for regular user")
	}
}

func TestConfig_MemberRole(t *testing.T) {
	ownerPubkey := nostr.MustPubKeyFromHex("9999999999999999999999999999999999999999999999999999999999999999")
	anyPubkey := nostr.MustPubKeyFromHex("1234567890123456789012345678901234567890123456789012345678901234")

	config := &Config{
		secret: nostr.Generate(),
		Info: struct {
			Name        string `toml:"name"`
			Icon        string `toml:"icon"`
			Pubkey      string `toml:"pubkey"`
			Description string `toml:"description"`
		}{
			Pubkey: ownerPubkey.Hex(),
		},
		Roles: map[string]Role{
			"member": {
				Pubkeys:   []string{},
				CanInvite: true,
			},
		},
	}

	roles := config.GetAllRoles(anyPubkey)
	if len(roles) != 1 {
		t.Errorf("GetAllRoles() should return member role for any pubkey, got %d roles", len(roles))
	}

	if !config.CanInvite(anyPubkey) {
		t.Error("Any pubkey should have member role permissions")
	}
}

func TestGitConfigDefaultsAndValidation(t *testing.T) {
	configDir := t.TempDir()
	path := filepath.Join(configDir, "git.toml")
	contents := "[git]\nenabled = true\npublic_url = \"https://relay.example/prefix\"\nbackend_url = \"http://walgit:8080\"\n"
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	config, err := decodeConfigFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.validateGit(); err != nil {
		t.Fatal(err)
	}
	if config.GitReconcileInterval().String() != "5s" || config.Git.MaxDiffBytes != 8<<20 || config.Git.MaxDiffFiles != 2000 {
		t.Fatalf("unexpected Git defaults: %+v", config.Git)
	}

	config.Git.BackendURL = "walgit:8080"
	if err := config.validateGit(); err == nil {
		t.Fatal("expected invalid backend URL")
	}
	config.Git.BackendURL = "http://walgit:8080"
	config.Git.ReconcileInterval = "0s"
	if err := config.validateGit(); err == nil {
		t.Fatal("expected invalid reconcile interval")
	}
}

func TestGitHubConfigSupportsGitHubOnlyAndValidatesSecretDirectory(t *testing.T) {
	configDir := t.TempDir()
	path := filepath.Join(configDir, "github.toml")
	contents := "[git]\nenabled = true\npublic_url = \"https://relay.example\"\n\n[github]\nenabled = true\n"
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	config, err := decodeConfigFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.validateGit(); err != nil {
		t.Fatalf("GitHub-only forge must not require WalGit: %v", err)
	}
	if err := config.validateGitHub(); err != nil {
		t.Fatal(err)
	}
	if config.GitHub.CredentialDir != "/run/secrets" || config.GitHubWorkerInterval().String() != "2s" || config.GitHubReconcileInterval().String() != "5m0s" {
		t.Fatalf("unexpected GitHub defaults: %+v", config.GitHub)
	}
	config.GitHub.CredentialDir = "relative"
	if err := config.validateGitHub(); err == nil {
		t.Fatal("expected relative credential directory rejection")
	}
}

func TestAuthConfigDefaultsAndRejectsInsecurePublicDeployment(t *testing.T) {
	configDir := t.TempDir()
	path := filepath.Join(configDir, "auth.toml")
	contents := `[auth]
enabled = true
public_url = "https://relay.example"
master_key_ref = "file:lapis-auth-master-key"

[auth.github]
client_id = "client-id"
client_secret_ref = "file:github-oauth-secret"
`
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	config, err := decodeConfigFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.validateAuth(); err != nil {
		t.Fatal(err)
	}
	if config.AuthSessionLifetime() != 7*24*time.Hour || config.AuthSessionIdle() != 24*time.Hour || config.AuthChallengeTTL() != 10*time.Minute {
		t.Fatalf("unexpected auth defaults: %+v", config.Auth)
	}
	config.Auth.PublicURL = "http://relay.example"
	if err := config.validateAuth(); err == nil {
		t.Fatal("expected public HTTP auth URL rejection")
	}
	config.Auth.PublicURL = "http://localhost:8281"
	if err := config.validateAuth(); err != nil {
		t.Fatalf("loopback development URL should be allowed: %v", err)
	}
	config.Auth.Google.ClientID = "google-client"
	if err := config.validateAuth(); err == nil {
		t.Fatal("expected incomplete Google configuration rejection")
	}
}
