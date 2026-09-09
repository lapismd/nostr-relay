package zooid

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"zooid/internal/forge"
	"zooid/internal/githubforge"
	"zooid/internal/projecttask"
)

type githubAdministration interface {
	ValidateConnection(context.Context, githubforge.Connection) (githubforge.Connection, error)
	ListRepositories(context.Context, githubforge.Connection) ([]githubforge.RepositoryMetadata, error)
	RepositoryByID(context.Context, string, int64) (githubforge.RepositoryMetadata, error)
}

func (policy *GitForgePolicy) LoadGitHubConnection(ctx context.Context, id string) (githubforge.Connection, bool, error) {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	var connection githubforge.Connection
	var installationID string
	err := GetDb().QueryRowContext(queryCtx, policy.Events.Schema.Render(`
		SELECT id, host, app_id, installation_id, credential_ref,
		       webhook_secret_ref, bridge_credential_ref, bridge_pubkey,
		       account_id, account_login, status
		FROM {{.Name}}__forge_connections
		WHERE id = $1 AND provider = 'github'`), id).Scan(
		&connection.ID, &connection.Host, &connection.AppID, &installationID,
		&connection.CredentialRef, &connection.WebhookSecretRef,
		&connection.BridgeCredentialRef, &connection.BridgePubkey,
		&connection.AccountID, &connection.AccountLogin, &connection.Status)
	if err == sql.ErrNoRows {
		return githubforge.Connection{}, false, nil
	}
	if err != nil {
		return githubforge.Connection{}, false, err
	}
	connection.InstallationID, err = strconv.ParseInt(installationID, 10, 64)
	if err != nil || connection.InstallationID <= 0 {
		return githubforge.Connection{}, false, fmt.Errorf("invalid stored GitHub installation")
	}
	return connection, true, nil
}

func (policy *GitForgePolicy) saveGitHubConnection(ctx context.Context, connection githubforge.Connection) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	_, err := GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
		INSERT INTO {{.Name}}__forge_connections
		  (id, provider, host, app_id, credential_ref, installation_id,
		   account_id, account_login, webhook_secret_ref,
		   bridge_credential_ref, bridge_pubkey, status, last_error, updated_at)
		VALUES ($1, 'github', 'github.com', $2, $3, $4, $5, $6, $7, $8, $9, $10, '', NOW())
		ON CONFLICT (id) DO UPDATE SET
		  app_id = EXCLUDED.app_id, credential_ref = EXCLUDED.credential_ref,
		  installation_id = EXCLUDED.installation_id,
		  account_id = EXCLUDED.account_id, account_login = EXCLUDED.account_login,
		  webhook_secret_ref = EXCLUDED.webhook_secret_ref,
		  bridge_credential_ref = EXCLUDED.bridge_credential_ref,
		  bridge_pubkey = EXCLUDED.bridge_pubkey, status = EXCLUDED.status,
		  last_error = '', updated_at = NOW()
		WHERE {{.Name}}__forge_connections.provider = 'github'`),
		connection.ID, connection.AppID, connection.CredentialRef,
		strconv.FormatInt(connection.InstallationID, 10), connection.AccountID,
		connection.AccountLogin, connection.WebhookSecretRef,
		connection.BridgeCredentialRef, connection.BridgePubkey, connection.Status)
	return err
}

func (policy *GitForgePolicy) setExplicitIntegration(ctx context.Context, integration forge.Integration, expectedAnnouncementID string) error {
	if integration.GitAuthorityBackend == "" {
		integration.GitAuthorityBackend = integration.GitBackend
	}
	if integration.ContentBackend == "" {
		integration.ContentBackend = integration.GitBackend
	}
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	result, err := GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
		INSERT INTO {{.Name}}__repository_integrations
		  (owner, repo, announcement_id, configuration_source, git_backend,
		   git_authority_backend, content_backend, git_replication_target_backend,
		   collaboration_backend, mirror_backend, ci_backend, connection_id,
		   external_repository_id, external_repository, generation,
		   health_state, health_detail, ci_connection_id, ci_external_repository_id,
		   ci_external_repository, ci_workflow, replica_enabled, replica_direction,
		   replica_sync_branches, replica_sync_tags, replica_mode,
		   replica_read_policy, replica_wait_milliseconds, updated_at)
		VALUES ($1, $2, $3, 'explicit', $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, 1, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26, NOW())
		ON CONFLICT (owner, repo) DO UPDATE SET
		  announcement_id = EXCLUDED.announcement_id,
		  configuration_source = 'explicit', git_backend = EXCLUDED.git_backend,
		  git_authority_backend = EXCLUDED.git_authority_backend,
		  content_backend = EXCLUDED.content_backend,
		  git_replication_target_backend = EXCLUDED.git_replication_target_backend,
		  collaboration_backend = EXCLUDED.collaboration_backend,
		  mirror_backend = EXCLUDED.mirror_backend, ci_backend = EXCLUDED.ci_backend,
		  connection_id = EXCLUDED.connection_id,
		  external_repository_id = EXCLUDED.external_repository_id,
		  external_repository = EXCLUDED.external_repository,
		  ci_connection_id = EXCLUDED.ci_connection_id,
		  ci_external_repository_id = EXCLUDED.ci_external_repository_id,
		  ci_external_repository = EXCLUDED.ci_external_repository,
		  ci_workflow = EXCLUDED.ci_workflow,
		  replica_enabled = EXCLUDED.replica_enabled,
		  replica_direction = EXCLUDED.replica_direction,
		  replica_sync_branches = EXCLUDED.replica_sync_branches,
		  replica_sync_tags = EXCLUDED.replica_sync_tags,
		  replica_mode = EXCLUDED.replica_mode,
		  replica_read_policy = EXCLUDED.replica_read_policy,
		  replica_wait_milliseconds = EXCLUDED.replica_wait_milliseconds,
		  generation = {{.Name}}__repository_integrations.generation + 1,
		  health_state = EXCLUDED.health_state, health_detail = EXCLUDED.health_detail,
		  updated_at = NOW()
		WHERE {{.Name}}__repository_integrations.announcement_id = $27`),
		integration.Coordinate.Owner.Hex(), integration.Coordinate.ID,
		integration.AnnouncementID, integration.GitBackend, integration.GitAuthorityBackend,
		integration.ContentBackend, integration.GitReplicationTargetBackend,
		integration.CollaborationBackend, integration.MirrorBackend,
		integration.CIBackend, integration.ConnectionID, integration.ExternalRepositoryID,
		integration.ExternalRepository, integration.HealthState, integration.HealthDetail,
		integration.CIConnectionID, integration.CIExternalRepositoryID,
		integration.CIExternalRepository, integration.CIWorkflow,
		replicaEnabled(integration), replicaDirection(integration), replicaSyncBranches(integration),
		replicaSyncTags(integration), replicaMode(integration), replicaReadPolicy(integration),
		replicaWaitMilliseconds(integration), expectedAnnouncementID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return &forge.Error{Status: 409, Code: "announcement_changed"}
	}
	return nil
}

func replicaEnabled(integration forge.Integration) bool {
	return integration.Replica != nil && integration.Replica.Enabled
}
func replicaDirection(integration forge.Integration) string {
	if integration.Replica == nil {
		return ""
	}
	return integration.Replica.Direction
}
func replicaSyncBranches(integration forge.Integration) bool {
	return integration.Replica != nil && integration.Replica.SyncBranches
}
func replicaSyncTags(integration forge.Integration) bool {
	return integration.Replica != nil && integration.Replica.SyncTags
}
func replicaMode(integration forge.Integration) string {
	if integration.Replica == nil {
		return string(forge.ReplicaModeOff)
	}
	return string(integration.Replica.Mode)
}
func replicaReadPolicy(integration forge.Integration) string {
	if integration.Replica == nil {
		return string(forge.ReplicaReadFallback)
	}
	return string(integration.Replica.ReadPolicy)
}
func replicaWaitMilliseconds(integration forge.Integration) int {
	if integration.Replica == nil || integration.Replica.WaitMilliseconds <= 0 {
		return 1500
	}
	return integration.Replica.WaitMilliseconds
}

func (policy *GitForgePolicy) updateIntegrationHealth(ctx context.Context, coordinate projecttask.RepositoryCoordinate, state, detail, externalRepository string) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	_, err := GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
		UPDATE {{.Name}}__repository_integrations
		SET health_state = $3, health_detail = $4,
		    external_repository = CASE WHEN $5 = '' THEN external_repository ELSE $5 END,
		    updated_at = NOW()
		WHERE owner = $1 AND repo = $2`), coordinate.Owner.Hex(), coordinate.ID, state, sanitizeGitErrorString(detail), externalRepository)
	return err
}

func sanitizeGitErrorString(message string) string {
	message = strings.Map(func(character rune) rune {
		if character < 0x20 || character == 0x7f {
			return ' '
		}
		return character
	}, message)
	if len(message) > 512 {
		message = message[:512]
	}
	return message
}

func newOperationID(prefix string) (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(value), nil
}

var _ githubforge.ConnectionLoader = (*GitForgePolicy)(nil)

func (policy *GitForgePolicy) GitHubWebhookConnections(ctx context.Context) ([]githubforge.Connection, error) {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	rows, err := GetDb().QueryContext(queryCtx, policy.Events.Schema.Render(`
		SELECT id, host, app_id, installation_id, credential_ref,
		       webhook_secret_ref, bridge_credential_ref, bridge_pubkey,
		       account_id, account_login, status
		FROM {{.Name}}__forge_connections
		WHERE provider = 'github' AND status = 'active'
		ORDER BY id`))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	connections := make([]githubforge.Connection, 0)
	for rows.Next() {
		var connection githubforge.Connection
		var installationID string
		if err := rows.Scan(
			&connection.ID, &connection.Host, &connection.AppID, &installationID,
			&connection.CredentialRef, &connection.WebhookSecretRef,
			&connection.BridgeCredentialRef, &connection.BridgePubkey,
			&connection.AccountID, &connection.AccountLogin, &connection.Status,
		); err != nil {
			return nil, err
		}
		connection.InstallationID, err = strconv.ParseInt(installationID, 10, 64)
		if err != nil || connection.InstallationID <= 0 {
			return nil, fmt.Errorf("invalid stored GitHub installation")
		}
		connections = append(connections, connection)
	}
	return connections, rows.Err()
}

func (policy *GitForgePolicy) updateGitHubConnectionStatus(ctx context.Context, id, status, failure string) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	_, err := GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
		UPDATE {{.Name}}__forge_connections
		SET status = $2, last_error = $3, updated_at = NOW()
		WHERE id = $1 AND provider = 'github'`), id, status, sanitizeGitErrorString(failure))
	return err
}

var _ githubforge.WebhookConnectionLoader = (*GitForgePolicy)(nil)
