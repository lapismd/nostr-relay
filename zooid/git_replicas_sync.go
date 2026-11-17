package zooid

import (
	"context"
	"database/sql"
	"errors"
	"fiatjaf.com/nostr"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"zooid/internal/forge"
	"zooid/internal/gitforge"
	"zooid/internal/projecttask"
)

func (policy *GitForgePolicy) Provision(ctx context.Context, repository forge.Repository) error {
	return policy.Sync(ctx, repository, forge.ReplicaSyncRequest{Reason: "provision"})
}

func (policy *GitForgePolicy) Sync(ctx context.Context, repository forge.Repository, request forge.ReplicaSyncRequest) error {
	resolved, err := policy.Registry.Resolve(ctx, repository)
	if err != nil {
		return err
	}
	config := repository.Integration.Replica
	if config == nil || !config.Enabled || repository.Integration.GitAuthorityBackend != forge.BackendGitHub || repository.Integration.ContentBackend != forge.BackendWalGit {
		return &forge.Error{Status: http.StatusConflict, Code: "replica_configuration_changed"}
	}
	cloneSource, ok := resolved.Authority.(forge.AuthenticatedCloneSource)
	if !ok {
		return &forge.Error{Status: http.StatusConflict, Code: "source_clone_unsupported"}
	}
	importer, ok := policy.Registry.ContentBackend(config.ReplicaBackend).(forge.RepositoryImporter)
	if !ok {
		return &forge.Error{Status: http.StatusConflict, Code: "walgit_replica_unavailable"}
	}
	if err := policy.setGitReplicaState(ctx, repository.Coordinate, "syncing", "", true); err != nil {
		return err
	}
	before, err := resolved.Authority.SnapshotRefs(ctx, repository)
	if err != nil {
		return err
	}
	before = filterReplicaRefs(before, config.SyncBranches, config.SyncTags)
	defaultBranch, err := resolved.Authority.DefaultBranch(ctx, repository, replicaPrincipal(repository.Integration.ConnectionID))
	if err != nil {
		return err
	}
	cloneURL, username, password, err := cloneSource.CloneSource(ctx, repository)
	if err != nil {
		return err
	}
	target := repository
	target.Classification = string(gitforge.RepositoryHosted)
	target.CloneURL = strings.TrimRight(policy.Config.Git.PublicURL, "/") + "/git/" + target.Coordinate.Owner.Hex() + "/" + target.Coordinate.ID
	target.Integration.GitBackend = forge.BackendWalGit
	target.Integration.GitAuthorityBackend = forge.BackendWalGit
	target.Integration.ContentBackend = forge.BackendWalGit
	imported, importErr := importer.ImportRepository(ctx, target, forge.ImportRequest{
		SourceCloneURL: cloneURL, ExpectedRefs: before, SourceUsername: username,
		SourcePassword: password, DestinationPrincipal: replicaPrincipal(repository.Integration.ConnectionID),
		SyncBranches: config.SyncBranches, SyncTags: config.SyncTags,
	})
	password = ""
	if importErr != nil {
		return importErr
	}
	after, err := resolved.Authority.SnapshotRefs(ctx, repository)
	if err != nil {
		return err
	}
	after = filterReplicaRefs(after, config.SyncBranches, config.SyncTags)
	if !forgeRefMapsEqual(before, after) {
		return &forge.Error{Status: http.StatusConflict, Code: "source_refs_changed", Retryable: true}
	}
	if !forgeRefMapsEqual(after, imported.Refs) {
		return &forge.Error{Status: http.StatusUnprocessableEntity, Code: "replica_ref_verification_failed", Retryable: true}
	}
	return policy.finishGitReplicaSync(ctx, repository, after, defaultBranch, request.DeliveryID)
}

func (policy *GitForgePolicy) Reconcile(ctx context.Context, repository forge.Repository) error {
	return policy.Sync(ctx, repository, forge.ReplicaSyncRequest{Reason: "reconcile"})
}

func filterReplicaRefs(refs map[string]string, branches, tags bool) map[string]string {
	if !branches && !tags {
		branches, tags = true, true
	}
	filtered := make(map[string]string)
	for name, oid := range refs {
		if (branches && strings.HasPrefix(name, "refs/heads/")) || (tags && strings.HasPrefix(name, "refs/tags/")) {
			filtered[name] = strings.ToLower(oid)
		}
	}
	return filtered
}

func (policy *GitForgePolicy) finishGitReplicaSync(ctx context.Context, repository forge.Repository, refs map[string]string, defaultBranch, deliveryID string) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	tx, err := GetDb().BeginTx(queryCtx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(queryCtx, policy.Events.Schema.Render(`DELETE FROM {{.Name}}__git_replica_refs WHERE owner = $1 AND repo = $2`), repository.Coordinate.Owner.Hex(), repository.Coordinate.ID); err != nil {
		return err
	}
	names := make([]string, 0, len(refs))
	for name := range refs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		oid := refs[name]
		if _, err := tx.ExecContext(queryCtx, policy.Events.Schema.Render(`
			INSERT INTO {{.Name}}__git_replica_refs
			  (owner, repo, ref_name, authority_oid, replica_oid, source_delivery_id, last_seen_at, verified_at)
			VALUES ($1, $2, $3, $4, $4, $5, NOW(), NOW())`), repository.Coordinate.Owner.Hex(), repository.Coordinate.ID, name, oid, deliveryID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(queryCtx, policy.Events.Schema.Render(`
		UPDATE {{.Name}}__git_replicas
		SET state = 'current', default_branch = $3, last_sync_at = NOW(),
		    last_success_at = NOW(), last_authority_check_at = NOW(),
		    last_error = '', updated_at = NOW()
		WHERE owner = $1 AND repo = $2 AND generation = $4`), repository.Coordinate.Owner.Hex(), repository.Coordinate.ID, defaultBranch, repository.Integration.Generation); err != nil {
		return err
	}
	if _, err := tx.ExecContext(queryCtx, policy.Events.Schema.Render(`
		UPDATE {{.Name}}__repository_integrations
		SET health_state = 'ready', health_detail = '', updated_at = NOW()
		WHERE owner = $1 AND repo = $2 AND generation = $3 AND replica_enabled = TRUE`), repository.Coordinate.Owner.Hex(), repository.Coordinate.ID, repository.Integration.Generation); err != nil {
		return err
	}
	return tx.Commit()
}

func (policy *GitForgePolicy) setGitReplicaState(ctx context.Context, coordinate projecttask.RepositoryCoordinate, state, failure string, checked bool) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	_, err := GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
		UPDATE {{.Name}}__git_replicas SET state = $3, last_error = $4,
		    last_authority_check_at = CASE WHEN $5 THEN NOW() ELSE last_authority_check_at END,
		    updated_at = NOW()
		WHERE owner = $1 AND repo = $2`), coordinate.Owner.Hex(), coordinate.ID, state, sanitizeGitErrorString(failure), checked)
	return err
}

func (policy *GitForgePolicy) completeGitReplicaJob(ctx context.Context, job gitReplicaJob) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	_, err := GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
		UPDATE {{.Name}}__git_replica_jobs SET processing_state = 'complete', lease_until = NULL,
		    last_error = '', completed_at = NOW(), updated_at = NOW() WHERE operation_id = $1`), job.OperationID)
	return err
}

func (policy *GitForgePolicy) failGitReplicaJob(ctx context.Context, job gitReplicaJob, processErr error) error {
	attempts := job.Attempts + 1
	state := "failed"
	next := time.Now()
	var backendError *forge.Error
	if attempts < 12 {
		state = "retry"
		next = time.Now().Add(time.Duration(min(1<<min(attempts, 8), 300)) * time.Second)
	}
	if errors.As(processErr, &backendError) && backendError.RetryAt.After(next) {
		next = backendError.RetryAt
	}
	failure := sanitizeGitError(processErr)
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	_, err := GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
		UPDATE {{.Name}}__git_replica_jobs SET processing_state = $2, attempt_count = $3,
		    process_after = $4, lease_until = NULL, last_error = $5, updated_at = NOW()
		WHERE operation_id = $1`), job.OperationID, state, attempts, next, failure)
	if err != nil {
		return err
	}
	_ = policy.setGitReplicaState(ctx, job.Coordinate, map[bool]string{true: "failed", false: "stale"}[state == "failed"], failure, false)
	return processErr
}

func (policy *GitForgePolicy) ReconcileGitReplicasOnce(ctx context.Context) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	var ownerHex, repo string
	var generation int64
	err := GetDb().QueryRowContext(queryCtx, policy.Events.Schema.Render(`
		SELECT replica.owner, replica.repo, replica.generation
		FROM {{.Name}}__git_replicas replica
		JOIN {{.Name}}__repository_integrations integration
		  ON integration.owner = replica.owner AND integration.repo = replica.repo
		WHERE replica.state <> 'disabled' AND integration.replica_enabled = TRUE
		  AND integration.replica_mode = 'continuous'
		ORDER BY replica.last_reconcile_at NULLS FIRST, replica.last_reconcile_at,
		         replica.owner, replica.repo LIMIT 1`)).Scan(&ownerHex, &repo, &generation)
	cancel()
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	owner, err := nostr.PubKeyFromHex(ownerHex)
	if err != nil {
		return err
	}
	coordinate := projecttask.RepositoryCoordinate{Owner: owner, ID: repo}
	bucket := time.Now().UTC().Unix() / 300
	if err := policy.enqueueGitReplicaSync(ctx, coordinate, generation, forge.ReplicaSyncRequest{Reason: "reconcile-" + strconv.FormatInt(bucket, 10)}); err != nil {
		return err
	}
	queryCtx, cancel = context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	_, err = GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`UPDATE {{.Name}}__git_replicas SET last_reconcile_at = NOW(), updated_at = NOW() WHERE owner = $1 AND repo = $2`), ownerHex, repo)
	return err
}

var _ forge.GitReplicaCoordinator = (*GitForgePolicy)(nil)
