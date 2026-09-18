package zooid

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"

	"fiatjaf.com/nostr"
	"zooid/internal/forge"
	"zooid/internal/gitforge"
	"zooid/internal/projecttask"
)

type forgeMigration struct {
	OperationID             string
	Coordinate              projecttask.RepositoryCoordinate
	SourceAnnouncementID    string
	SourceConnectionID      string
	SourceRepositoryID      string
	SourceRefs              map[string]string
	KeepGitHubCollaboration bool
	RetainGitHubMirror      bool
	State                   string
	Failure                 string
	TargetAnnouncementID    string
}

func (policy *GitForgePolicy) enqueueForgeMigration(ctx context.Context, actor nostr.PubKey, repository forge.Repository, idempotencyKey string, keepCollaboration, retainMirror bool) (forgeOperation, error) {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	tx, err := GetDb().BeginTx(queryCtx, &sql.TxOptions{})
	if err != nil {
		return forgeOperation{}, err
	}
	defer tx.Rollback()
	operationID, err := newOperationID("mig")
	if err != nil {
		return forgeOperation{}, err
	}
	payload, _ := json.Marshal(map[string]any{"keep_github_collaboration": keepCollaboration, "retain_github_mirror": retainMirror})
	marker := forgeCorrelationMarker(idempotencyKey)
	_, err = tx.ExecContext(queryCtx, policy.Events.Schema.Render(`
		INSERT INTO {{.Name}}__forge_outbox
		  (operation_id, provider, connection_id, owner, repo, command,
		   object_type, actor, payload, idempotency_key, correlation_marker, processing_state)
		VALUES ($1, 'github', $2, $3, $4, 'migrate_to_walgit', 'migration', $5, $6, $7, $8, 'pending')
		ON CONFLICT (idempotency_key) DO NOTHING`), operationID,
		repository.Integration.ConnectionID, repository.Coordinate.Owner.Hex(), repository.Coordinate.ID,
		actor.Hex(), payload, idempotencyKey, marker)
	if err != nil {
		return forgeOperation{}, err
	}
	var actualOperationID string
	err = tx.QueryRowContext(queryCtx, policy.Events.Schema.Render(`
		SELECT operation_id FROM {{.Name}}__forge_outbox WHERE idempotency_key = $1`), idempotencyKey).Scan(&actualOperationID)
	if err != nil {
		return forgeOperation{}, err
	}
	_, err = tx.ExecContext(queryCtx, policy.Events.Schema.Render(`
		INSERT INTO {{.Name}}__forge_migrations
		  (operation_id, owner, repo, source_announcement_id, source_connection_id,
		   source_repository_id, keep_github_collaboration, retain_github_mirror, state)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'pending')
		ON CONFLICT (operation_id) DO NOTHING`), actualOperationID,
		repository.Coordinate.Owner.Hex(), repository.Coordinate.ID,
		repository.Announcement.ID.Hex(), repository.Integration.ConnectionID,
		repository.Integration.ExternalRepositoryID, keepCollaboration, retainMirror)
	if err != nil {
		return forgeOperation{}, err
	}
	_, err = tx.ExecContext(queryCtx, policy.Events.Schema.Render(`
		UPDATE {{.Name}}__repository_integrations
		SET health_state = 'migrating', health_detail = 'GitHub write freeze acknowledged; importing into WalGit', updated_at = NOW()
		WHERE owner = $1 AND repo = $2 AND announcement_id = $3 AND git_backend = 'github'`),
		repository.Coordinate.Owner.Hex(), repository.Coordinate.ID, repository.Announcement.ID.Hex())
	if err != nil {
		return forgeOperation{}, err
	}
	if err := tx.Commit(); err != nil {
		return forgeOperation{}, err
	}
	operation, found, err := policy.loadForgeOperation(ctx, actualOperationID)
	if err != nil || !found {
		return forgeOperation{}, err
	}
	return operation, nil
}

func (policy *GitForgePolicy) processForgeMigration(ctx context.Context, resolved forge.ResolvedRepository, operation forgeOperation) error {
	migration, found, err := policy.loadForgeMigration(ctx, operation.ID)
	if err != nil || !found {
		return &forge.Error{Status: http.StatusConflict, Code: "migration_state_missing"}
	}
	if migration.State == "awaiting-owner-announcement" || migration.State == "complete" {
		return policy.finishForgeOperation(ctx, operation)
	}
	snapshotter := resolved.Authority
	if snapshotter == nil {
		return &forge.Error{Status: http.StatusConflict, Code: "source_snapshot_unsupported"}
	}
	cloneSource, ok := resolved.Authority.(forge.AuthenticatedCloneSource)
	if !ok {
		return &forge.Error{Status: http.StatusConflict, Code: "source_clone_unsupported"}
	}
	targetBackend := policy.Registry.ContentBackend(forge.BackendWalGit)
	importer, ok := targetBackend.(forge.RepositoryImporter)
	if !ok {
		return &forge.Error{Status: http.StatusConflict, Code: "walgit_import_unavailable"}
	}
	if err := policy.updateForgeMigration(ctx, operation.ID, "importing", nil, ""); err != nil {
		return err
	}
	before, err := snapshotter.SnapshotRefs(ctx, resolved.Repository)
	if err != nil {
		return err
	}
	target := resolved.Repository
	target.Classification = string(gitforge.RepositoryHosted)
	target.CloneURL = strings.TrimRight(policy.Config.Git.PublicURL, "/") + "/git/" + target.Coordinate.Owner.Hex() + "/" + target.Coordinate.ID
	target.Integration.GitBackend = forge.BackendWalGit
	target.Integration.GitAuthorityBackend = forge.BackendWalGit
	target.Integration.ContentBackend = forge.BackendWalGit
	reusedReplica := false
	if replica, exists, replicaErr := policy.loadGitReplica(ctx, resolved.Repository.Coordinate); replicaErr != nil {
		return replicaErr
	} else if exists && replica.State == "current" {
		replicaRefs, refsErr := policy.gitReplicaRefs(ctx, resolved.Repository.Coordinate)
		if refsErr != nil {
			return refsErr
		}
		reusedReplica = forgeRefMapsEqual(before, replicaRefs)
	}
	if !reusedReplica {
		cloneURL, username, password, cloneErr := cloneSource.CloneSource(ctx, resolved.Repository)
		if cloneErr != nil {
			return cloneErr
		}
		_, err = importer.ImportRepository(ctx, target, forge.ImportRequest{
			SourceCloneURL: cloneURL, SourceUsername: username, SourcePassword: password,
			ExpectedRefs: before, SyncBranches: true, SyncTags: true,
		})
		password = ""
		if err != nil {
			return err
		}
	}
	if err := policy.verifyMigrationReplicaObjects(ctx, resolved, target, before); err != nil {
		return err
	}
	after, err := snapshotter.SnapshotRefs(ctx, resolved.Repository)
	if err != nil {
		return err
	}
	if !forgeRefMapsEqual(before, after) {
		return &forge.Error{Status: http.StatusConflict, Code: "source_refs_changed"}
	}
	if err := policy.updateForgeMigration(ctx, operation.ID, "awaiting-owner-announcement", before, ""); err != nil {
		return err
	}
	if err := policy.updateIntegrationHealth(ctx, operation.Coordinate, "migrating", "WalGit import verified; repository owner must publish the canonical relay clone URL", ""); err != nil {
		return err
	}
	result := forge.CollaborationResult{State: "awaiting_owner_announcement"}
	if err := policy.storeForgeProviderResult(ctx, operation, result); err != nil {
		return err
	}
	return policy.finishForgeOperation(ctx, operation)
}

func (policy *GitForgePolicy) verifyMigrationReplicaObjects(ctx context.Context, source forge.ResolvedRepository, target forge.Repository, refs map[string]string) error {
	content := policy.Registry.ContentBackend(forge.BackendWalGit)
	if content == nil {
		return &forge.Error{Status: http.StatusConflict, Code: "walgit_content_unavailable"}
	}
	oids := make(map[string]struct{}, len(refs))
	for _, oid := range refs {
		oids[strings.ToLower(oid)] = struct{}{}
	}
	filter := nostr.Filter{Kinds: []nostr.Kind{projecttask.KindPullRequest}, Tags: nostr.TagMap{"a": {source.Repository.Coordinate.String()}}, Limit: 2000}
	for root := range policy.Events.QueryEventsBefore(ctx, filter, "", 2000) {
		if policy.ProjectTasks.IsTombstoned(ctx, root) {
			continue
		}
		state, err := projecttask.ReducePullRequest(root, source.Repository.Announcement, policy.pullRequestRelatedEvents(ctx, root.ID), policy.ProjectTasks.repositoryAuthority(source.Repository.Announcement))
		if err == nil {
			for _, oid := range []string{state.CurrentCommit, state.BaseCommit, state.MergeBase} {
				if fullForgeObjectID(oid) {
					oids[strings.ToLower(oid)] = struct{}{}
				}
			}
		}
	}
	for oid := range oids {
		_, err := content.Resolve(ctx, target, forge.RevisionRequest{Revision: oid, Principal: replicaPrincipal(source.Repository.Integration.ConnectionID)})
		if err == nil {
			continue
		}
		if !isGitObjectMissing(err) {
			return err
		}
		objectRepository := source.Repository
		objectRepository.Integration.ContentBackend = forge.BackendWalGit
		if err := policy.ensureReplicaObject(ctx, objectRepository, oid); err != nil {
			return err
		}
		if _, err := content.Resolve(ctx, target, forge.RevisionRequest{Revision: oid, Principal: replicaPrincipal(source.Repository.Integration.ConnectionID)}); err != nil {
			return &forge.Error{Status: http.StatusUnprocessableEntity, Code: "migration_object_verification_failed", Retryable: true}
		}
	}
	return nil
}

func (policy *GitForgePolicy) loadForgeMigration(ctx context.Context, operationID string) (forgeMigration, bool, error) {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	var migration forgeMigration
	var ownerHex string
	var refsJSON []byte
	err := GetDb().QueryRowContext(queryCtx, policy.Events.Schema.Render(`
		SELECT operation_id, owner, repo, source_announcement_id, source_connection_id,
		       source_repository_id, source_refs, keep_github_collaboration,
		       retain_github_mirror, state, sanitized_failure, target_announcement_id
		FROM {{.Name}}__forge_migrations WHERE operation_id = $1`), operationID).Scan(
		&migration.OperationID, &ownerHex, &migration.Coordinate.ID,
		&migration.SourceAnnouncementID, &migration.SourceConnectionID,
		&migration.SourceRepositoryID, &refsJSON, &migration.KeepGitHubCollaboration,
		&migration.RetainGitHubMirror, &migration.State, &migration.Failure,
		&migration.TargetAnnouncementID,
	)
	if err == sql.ErrNoRows {
		return forgeMigration{}, false, nil
	}
	if err != nil {
		return forgeMigration{}, false, err
	}
	migration.Coordinate.Owner, err = nostr.PubKeyFromHex(ownerHex)
	if err != nil {
		return forgeMigration{}, false, err
	}
	if len(refsJSON) != 0 {
		if err := json.Unmarshal(refsJSON, &migration.SourceRefs); err != nil {
			return forgeMigration{}, false, err
		}
	}
	return migration, true, nil
}

func (policy *GitForgePolicy) updateForgeMigration(ctx context.Context, operationID, state string, refs map[string]string, failure string) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	var refsJSON any
	if refs != nil {
		encoded, _ := json.Marshal(refs)
		refsJSON = encoded
	}
	_, err := GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
		UPDATE {{.Name}}__forge_migrations
		SET state = $2, source_refs = COALESCE($3, source_refs),
		    sanitized_failure = $4, updated_at = NOW()
		WHERE operation_id = $1`), operationID, state, refsJSON, sanitizeGitErrorString(failure))
	return err
}

func (policy *GitForgePolicy) recordForgeMigrationFailure(ctx context.Context, operationID string, migrationErr error) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	failure := "migration_failed"
	if backendError, ok := migrationErr.(*forge.Error); ok && backendError.Code != "" {
		failure = backendError.Code
	}
	_, err := GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
		UPDATE {{.Name}}__forge_migrations
		SET sanitized_failure = $2, updated_at = NOW() WHERE operation_id = $1`),
		operationID, sanitizeGitErrorString(failure))
	return err
}

func (policy *GitForgePolicy) handleForgeMigrationFailure(ctx context.Context, operation forgeOperation, migrationErr error) error {
	if err := policy.recordForgeMigrationFailure(ctx, operation.ID, migrationErr); err != nil {
		return err
	}
	if err := policy.failForgeOperation(ctx, operation, migrationErr); err != nil {
		return err
	}
	updated, found, err := policy.loadForgeOperation(ctx, operation.ID)
	if err != nil || !found || updated.State != forgeOperationFailed {
		return err
	}
	failure := updated.Failure
	if failure == "" {
		failure = "migration_failed"
	}
	if err := policy.updateForgeMigration(ctx, operation.ID, "failed", nil, failure); err != nil {
		return err
	}
	return policy.updateIntegrationHealth(ctx, operation.Coordinate, "degraded", "GitHub to WalGit migration failed: "+failure, "")
}

func forgeRefMapsEqual(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for ref, oid := range left {
		if !strings.EqualFold(oid, right[ref]) {
			return false
		}
	}
	return true
}

func (policy *GitForgePolicy) activateForgeMigration(ctx context.Context, repository forge.Repository, expectedAnnouncementID string) (forge.Integration, error) {
	if repository.Announcement.ID.Hex() != expectedAnnouncementID ||
		repository.Classification != string(gitforge.RepositoryHosted) ||
		!hasPublishedClone(repository.Announcement, repository.CloneURL) {
		return forge.Integration{}, &forge.Error{Status: http.StatusConflict, Code: "canonical_walgit_announcement_required"}
	}
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	tx, err := GetDb().BeginTx(queryCtx, &sql.TxOptions{})
	if err != nil {
		return forge.Integration{}, err
	}
	defer tx.Rollback()
	var migration forgeMigration
	var ownerHex string
	err = tx.QueryRowContext(queryCtx, policy.Events.Schema.Render(`
		SELECT operation_id, owner, repo, source_announcement_id, source_connection_id,
		       source_repository_id, keep_github_collaboration, retain_github_mirror, state
		FROM {{.Name}}__forge_migrations
		WHERE owner = $1 AND repo = $2 AND state = 'awaiting-owner-announcement'
		ORDER BY updated_at DESC LIMIT 1 FOR UPDATE`), repository.Coordinate.Owner.Hex(), repository.Coordinate.ID).Scan(
		&migration.OperationID, &ownerHex, &migration.Coordinate.ID,
		&migration.SourceAnnouncementID, &migration.SourceConnectionID,
		&migration.SourceRepositoryID, &migration.KeepGitHubCollaboration,
		&migration.RetainGitHubMirror, &migration.State,
	)
	if err == sql.ErrNoRows {
		return forge.Integration{}, &forge.Error{Status: http.StatusConflict, Code: "migration_not_ready"}
	}
	if err != nil {
		return forge.Integration{}, err
	}
	integration := repository.Integration
	integration.AnnouncementID = expectedAnnouncementID
	integration.ConfigurationSource = forge.ConfigurationExplicit
	integration.GitBackend = forge.BackendWalGit
	integration.GitAuthorityBackend = forge.BackendWalGit
	integration.ContentBackend = forge.BackendWalGit
	integration.GitReplicationTargetBackend = ""
	integration.Replica = nil
	integration.CollaborationBackend = forge.BackendRelay
	integration.MirrorBackend = ""
	integration.CIBackend = ""
	integration.CIConnectionID, integration.CIExternalRepositoryID = "", ""
	integration.CIExternalRepository, integration.CIWorkflow = "", ""
	integration.HealthState, integration.HealthDetail = "ready", ""
	if migration.KeepGitHubCollaboration {
		integration.CollaborationBackend = forge.BackendGitHub
	}
	if migration.RetainGitHubMirror {
		integration.GitReplicationTargetBackend = forge.BackendGitHub
	}
	if !migration.KeepGitHubCollaboration && !migration.RetainGitHubMirror {
		integration.ConnectionID, integration.ExternalRepositoryID, integration.ExternalRepository = "", "", ""
	}
	_, err = tx.ExecContext(queryCtx, policy.Events.Schema.Render(`
		UPDATE {{.Name}}__repository_integrations
			SET announcement_id = $3, configuration_source = 'explicit', git_backend = 'walgit',
			    git_authority_backend = 'walgit', content_backend = 'walgit',
			    git_replication_target_backend = $6,
			    replica_enabled = FALSE, replica_direction = '', replica_mode = 'off',
			    collaboration_backend = $4, mirror_backend = $5, connection_id = $7,
			    external_repository_id = $8, external_repository = $9,
			    ci_backend = '', ci_connection_id = '', ci_external_repository_id = '',
			    ci_external_repository = '', ci_workflow = '',
		    generation = generation + 1, health_state = 'ready', health_detail = '', updated_at = NOW()
		WHERE owner = $1 AND repo = $2`), repository.Coordinate.Owner.Hex(), repository.Coordinate.ID,
		expectedAnnouncementID, integration.CollaborationBackend, integration.MirrorBackend,
		integration.GitReplicationTargetBackend, integration.ConnectionID, integration.ExternalRepositoryID, integration.ExternalRepository)
	if err != nil {
		return forge.Integration{}, err
	}
	_, err = tx.ExecContext(queryCtx, policy.Events.Schema.Render(`
		UPDATE {{.Name}}__forge_migrations
		SET state = 'complete', target_announcement_id = $2, completed_at = NOW(), updated_at = NOW()
		WHERE operation_id = $1`), migration.OperationID, expectedAnnouncementID)
	if err != nil {
		return forge.Integration{}, err
	}
	_, err = tx.ExecContext(queryCtx, policy.Events.Schema.Render(`
		UPDATE {{.Name}}__git_repository_intents SET reconcile_state = 'ready', last_error = '', updated_at = NOW()
		WHERE owner = $1 AND repo = $2 AND announcement_id = $3`),
		repository.Coordinate.Owner.Hex(), repository.Coordinate.ID, expectedAnnouncementID)
	if err != nil {
		return forge.Integration{}, err
	}
	if err := tx.Commit(); err != nil {
		return forge.Integration{}, err
	}
	queryCtx, cancel = context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	_, _ = GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
		UPDATE {{.Name}}__git_replicas SET state = 'disabled', updated_at = NOW()
		WHERE owner = $1 AND repo = $2`), repository.Coordinate.Owner.Hex(), repository.Coordinate.ID)
	stored, found, loadErr := policy.repositoryIntegration(ctx, repository.Coordinate)
	if loadErr != nil {
		return forge.Integration{}, loadErr
	}
	if found {
		integration = stored
	}
	if integration.GitReplicationTargetBackend != "" {
		if err := policy.enqueueGitReplication(ctx, repository.Coordinate, integration.Generation, "", "migration-activation"); err != nil {
			return forge.Integration{}, err
		}
	}
	return integration, nil
}

func hasPublishedClone(event nostr.Event, expected string) bool {
	expected = strings.TrimRight(strings.TrimSpace(expected), "/")
	for _, tag := range event.Tags {
		if len(tag) < 2 || tag[0] != "clone" {
			continue
		}
		for _, value := range tag[1:] {
			if strings.TrimRight(strings.TrimSpace(value), "/") == expected {
				return true
			}
		}
	}
	return false
}
