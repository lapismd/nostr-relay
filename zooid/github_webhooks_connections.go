package zooid

import (
	"context"
	"database/sql"
	"fiatjaf.com/nostr"
	"net/http"
	"strconv"
	"strings"
	"zooid/internal/forge"
	"zooid/internal/githubforge"
)

func (policy *GitForgePolicy) disableGitHubConnection(ctx context.Context, id, reason string) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	tx, err := GetDb().BeginTx(queryCtx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(queryCtx, policy.Events.Schema.Render(`
		UPDATE {{.Name}}__forge_connections SET status = 'disabled', last_error = $2, updated_at = NOW()
		WHERE id = $1 AND provider = 'github'`), id, sanitizeGitErrorString(reason)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(queryCtx, policy.Events.Schema.Render(`
		UPDATE {{.Name}}__repository_integrations
		SET health_state = 'disabled', health_detail = $2, updated_at = NOW()
		WHERE connection_id = $1 OR ci_connection_id = $1`), id, sanitizeGitErrorString(reason)); err != nil {
		return err
	}
	return tx.Commit()
}

func (policy *GitForgePolicy) disableGitHubRepository(ctx context.Context, connectionID string, repositoryID int64, reason string) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	_, err := GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
		UPDATE {{.Name}}__repository_integrations
		SET health_state = 'disabled', health_detail = $3, updated_at = NOW()
		WHERE (connection_id = $1 AND external_repository_id = $2)
		   OR (ci_connection_id = $1 AND ci_external_repository_id = $2)`),
		connectionID, strconv.FormatInt(repositoryID, 10), sanitizeGitErrorString(reason))
	return err
}

func (policy *GitForgePolicy) enableGitHubRepository(ctx context.Context, connectionID string, repository githubWebhookRepository) error {
	attached, found, err := policy.repositoryForGitHub(ctx, connectionID, strconv.FormatInt(repository.ID, 10))
	if err != nil {
		return err
	}
	if !found {
		attached, found, err = policy.repositoryForGitHubCI(ctx, connectionID, strconv.FormatInt(repository.ID, 10))
		if err != nil || !found {
			return err
		}
		queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
		defer cancel()
		_, err = GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
			UPDATE {{.Name}}__repository_integrations
			SET ci_external_repository = $3, health_state = 'ready', health_detail = '', updated_at = NOW()
			WHERE owner = $1 AND repo = $2 AND ci_connection_id = $4 AND ci_external_repository_id = $5`),
			attached.Coordinate.Owner.Hex(), attached.Coordinate.ID, repository.FullName,
			connectionID, strconv.FormatInt(repository.ID, 10))
		return err
	}
	state, detail := "degraded", "signed clone/web URL is stale or does not match the attached GitHub repository"
	metadata := githubRepositoryMetadata(repository)
	if githubAnnouncementMatches(attached.Announcement, metadata) {
		state, detail = "ready", ""
	}
	if err := policy.updateIntegrationHealth(ctx, attached.Coordinate, state, detail, repository.FullName); err != nil {
		return err
	}
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	_, err = GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
		UPDATE {{.Name}}__repository_integrations SET ci_external_repository = $3, updated_at = NOW()
		WHERE owner = $1 AND repo = $2 AND ci_connection_id = $4 AND ci_external_repository_id = $5`),
		attached.Coordinate.Owner.Hex(), attached.Coordinate.ID, repository.FullName,
		connectionID, strconv.FormatInt(repository.ID, 10))
	return err
}

func (policy *GitForgePolicy) renameGitHubRepository(ctx context.Context, connectionID string, repository githubWebhookRepository) error {
	return policy.enableGitHubRepository(ctx, connectionID, repository)
}

func githubRepositoryMetadata(repository githubWebhookRepository) githubforge.RepositoryMetadata {
	return githubforge.RepositoryMetadata{ID: repository.ID, FullName: repository.FullName, CloneURL: repository.CloneURL, HTMLURL: repository.HTMLURL}
}

func (policy *GitForgePolicy) recordGitHubPush(ctx context.Context, delivery githubWebhookDelivery, payload githubWebhookPayload) error {
	repository, found, err := policy.repositoryForGitHub(ctx, delivery.ConnectionID, delivery.ExternalRepositoryID)
	if err != nil || !found {
		return err
	}
	if payload.Ref == "" || (!strings.HasPrefix(payload.Ref, "refs/heads/") && !strings.HasPrefix(payload.Ref, "refs/tags/")) {
		return &forge.Error{Status: http.StatusBadRequest, Code: "invalid_github_push"}
	}
	if repository.Integration.Replica != nil && repository.Integration.Replica.Enabled {
		managed := repository.Integration.Replica.SyncBranches && strings.HasPrefix(payload.Ref, "refs/heads/") ||
			repository.Integration.Replica.SyncTags && strings.HasPrefix(payload.Ref, "refs/tags/")
		if !managed {
			return nil
		}
	}
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	deleted := payload.Deleted || strings.Trim(payload.After, "0") == ""
	if deleted {
		_, err = GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
			DELETE FROM {{.Name}}__forge_provider_refs
			WHERE provider = 'github' AND connection_id = $1 AND owner = $2 AND repo = $3 AND ref_name = $4`),
			delivery.ConnectionID, repository.Coordinate.Owner.Hex(), repository.Coordinate.ID, payload.Ref)
	} else if !fullForgeObjectID(payload.After) {
		return &forge.Error{Status: http.StatusBadRequest, Code: "invalid_github_push"}
	} else {
		_, err = GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
		INSERT INTO {{.Name}}__forge_provider_refs
		  (provider, connection_id, owner, repo, ref_name, commit_oid, observed_at)
		VALUES ('github', $1, $2, $3, $4, $5, NOW())
		ON CONFLICT (provider, connection_id, owner, repo, ref_name) DO UPDATE SET
		  commit_oid = EXCLUDED.commit_oid, observed_at = NOW()`),
			delivery.ConnectionID, repository.Coordinate.Owner.Hex(), repository.Coordinate.ID,
			payload.Ref, strings.ToLower(payload.After))
	}
	if err != nil {
		return err
	}
	if repository.Integration.Replica == nil || !repository.Integration.Replica.Enabled {
		return nil
	}
	authorityOID := strings.ToLower(payload.After)
	if deleted {
		authorityOID = ""
	}
	_, err = GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
		INSERT INTO {{.Name}}__git_replica_refs
		  (owner, repo, ref_name, authority_oid, replica_oid, source_delivery_id, last_seen_at)
		VALUES ($1, $2, $3, $4, '', $5, NOW())
		ON CONFLICT (owner, repo, ref_name) DO UPDATE SET
		  authority_oid = EXCLUDED.authority_oid,
		  source_delivery_id = EXCLUDED.source_delivery_id, last_seen_at = NOW()`),
		repository.Coordinate.Owner.Hex(), repository.Coordinate.ID, payload.Ref, authorityOID, delivery.DeliveryID)
	if err != nil {
		return err
	}
	if err := policy.setGitReplicaState(ctx, repository.Coordinate, "stale", "", false); err != nil {
		return err
	}
	// On-demand replicas record authoritative drift but defer the transfer
	// until a mutable read asks for it. Continuous replicas take the same
	// durable path immediately.
	if repository.Integration.Replica.Mode != forge.ReplicaModeContinuous {
		return nil
	}
	return policy.enqueueGitReplicaSync(ctx, repository.Coordinate, repository.Integration.Generation, forge.ReplicaSyncRequest{
		DeliveryID: delivery.DeliveryID, RefName: payload.Ref, TargetOID: authorityOID, Reason: "github-push",
	})
}

func webhookCorrelationMarker(payload githubWebhookPayload) string {
	text := firstNonempty(payload.Issue.Body, payload.PullRequest.Body, payload.Comment.Body, payload.Review.Body)
	prefix := "<!-- lapis:"
	start := strings.Index(text, prefix)
	if start < 0 {
		return ""
	}
	start += len(prefix)
	end := strings.Index(text[start:], " -->")
	if end < 0 {
		return ""
	}
	marker := text[start : start+end]
	if len(marker) != 32 {
		return ""
	}
	for _, character := range marker {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return ""
		}
	}
	return marker
}

func (policy *GitForgePolicy) acknowledgeCorrelatedWebhook(ctx context.Context, repository forge.Repository, delivery githubWebhookDelivery, payload githubWebhookPayload, marker string) (bool, error) {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	operation, err := policy.scanForgeOperation(GetDb().QueryRowContext(queryCtx, policy.Events.Schema.Render(`
		SELECT operation_id, provider, connection_id, owner, repo, command,
		       object_type, actor, credential_mode, nostr_root_id, source_event_id, expected_head, payload,
		       idempotency_key, correlation_marker, processing_state,
		       attempt_count, provider_object_id, provider_result,
		       sanitized_failure, created_at, updated_at
		FROM {{.Name}}__forge_outbox
		WHERE provider = 'github' AND connection_id = $1 AND owner = $2 AND repo = $3
		  AND correlation_marker = $4`), delivery.ConnectionID, repository.Coordinate.Owner.Hex(), repository.Coordinate.ID, marker))
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	result := webhookCollaborationResult(delivery, payload)
	if result.ExternalID == "" {
		return true, nil
	}
	if operation.ProviderResult == nil {
		if err := policy.storeForgeProviderResult(ctx, operation, result); err != nil {
			return true, err
		}
	}
	return true, nil
}

func (policy *GitForgePolicy) bridgeSecretForConnection(ctx context.Context, connectionID string) (nostr.SecretKey, error) {
	connection, found, err := policy.LoadGitHubConnection(ctx, connectionID)
	if err != nil || !found {
		if err == nil {
			err = &forge.Error{Status: http.StatusServiceUnavailable, Code: "github_connection_unavailable", Retryable: true}
		}
		return nostr.SecretKey{}, err
	}
	return policy.bridgeSecret(connection)
}
