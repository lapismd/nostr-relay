package zooid

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"time"

	"fiatjaf.com/nostr"
	"zooid/internal/forge"
	"zooid/internal/projecttask"
)

type gitReplicationJob struct {
	OperationID string
	Coordinate  projecttask.RepositoryCoordinate
	Generation  int64
	Sequence    string
	Reason      string
	Attempts    int
}

func (policy *GitForgePolicy) gitReplicationHealth(ctx context.Context, coordinate projecttask.RepositoryCoordinate) (map[string]any, bool, error) {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	var target, state, failure string
	var success, reconcile sql.NullTime
	err := GetDb().QueryRowContext(queryCtx, policy.Events.Schema.Render(`
		SELECT target_backend, state, last_success_at, last_reconcile_at, last_error
		FROM {{.Name}}__git_replication_state WHERE owner = $1 AND repo = $2`),
		coordinate.Owner.Hex(), coordinate.ID).Scan(&target, &state, &success, &reconcile, &failure)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	result := map[string]any{"target_backend": target, "state": state,
		"last_success_at": nullableTime(success), "last_reconcile_at": nullableTime(reconcile)}
	if failure != "" {
		result["failure"] = failure
	}
	return result, true, nil
}

func (policy *GitForgePolicy) enqueueGitReplication(ctx context.Context, coordinate projecttask.RepositoryCoordinate, generation int64, sequence, reason string) error {
	operationID, err := newOperationID("grp")
	if err != nil {
		return err
	}
	if reason == "" {
		reason = "walgit-ref"
	}
	dedup := coordinate.String() + ":" + strconv.FormatInt(generation, 10) + ":" + sequence + ":" + reason
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	tx, err := GetDb().BeginTx(queryCtx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(queryCtx, policy.Events.Schema.Render(`
		INSERT INTO {{.Name}}__git_replication_jobs
		  (operation_id, owner, repo, generation, source_sequence, reason, deduplication_key)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (deduplication_key) DO NOTHING`), operationID,
		coordinate.Owner.Hex(), coordinate.ID, generation, sequence, reason, dedup); err != nil {
		return err
	}
	if _, err := tx.ExecContext(queryCtx, policy.Events.Schema.Render(`
		INSERT INTO {{.Name}}__git_replication_state
		  (owner, repo, target_backend, generation, state, updated_at)
		SELECT owner, repo, git_replication_target_backend, generation, 'pending', NOW()
		FROM {{.Name}}__repository_integrations
		WHERE owner = $1 AND repo = $2 AND generation = $3
		  AND git_authority_backend = 'walgit' AND git_replication_target_backend <> ''
		ON CONFLICT (owner, repo) DO UPDATE SET target_backend = EXCLUDED.target_backend,
		  generation = EXCLUDED.generation, state = 'pending', updated_at = NOW()`),
		coordinate.Owner.Hex(), coordinate.ID, generation); err != nil {
		return err
	}
	return tx.Commit()
}

func (policy *GitForgePolicy) claimGitReplication(ctx context.Context) (gitReplicationJob, bool, error) {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	tx, err := GetDb().BeginTx(queryCtx, &sql.TxOptions{})
	if err != nil {
		return gitReplicationJob{}, false, err
	}
	defer tx.Rollback()
	var job gitReplicationJob
	var ownerHex string
	err = tx.QueryRowContext(queryCtx, policy.Events.Schema.Render(`
		SELECT operation_id, owner, repo, generation, source_sequence, reason, attempt_count
		FROM {{.Name}}__git_replication_jobs
		WHERE (processing_state IN ('pending', 'retry') AND process_after <= NOW())
		   OR (processing_state = 'running' AND lease_until < NOW())
		ORDER BY process_after, created_at, operation_id
		FOR UPDATE SKIP LOCKED LIMIT 1`)).Scan(&job.OperationID, &ownerHex,
		&job.Coordinate.ID, &job.Generation, &job.Sequence, &job.Reason, &job.Attempts)
	if err == sql.ErrNoRows {
		return gitReplicationJob{}, false, nil
	}
	if err != nil {
		return gitReplicationJob{}, false, err
	}
	job.Coordinate.Owner, err = nostr.PubKeyFromHex(ownerHex)
	if err != nil {
		return gitReplicationJob{}, false, err
	}
	if _, err := tx.ExecContext(queryCtx, policy.Events.Schema.Render(`
		UPDATE {{.Name}}__git_replication_jobs SET processing_state = 'running',
		  lease_until = NOW() + INTERVAL '5 minutes', updated_at = NOW()
		WHERE operation_id = $1`), job.OperationID); err != nil {
		return gitReplicationJob{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return gitReplicationJob{}, false, err
	}
	return job, true, nil
}

func (policy *GitForgePolicy) ProcessGitReplicationOnce(ctx context.Context) error {
	job, found, err := policy.claimGitReplication(ctx)
	if err != nil || !found {
		return err
	}
	resolved, found, err := policy.ResolveIntegration(ctx, job.Coordinate)
	if err != nil || !found {
		if err == nil {
			err = &forge.Error{Status: http.StatusNotFound, Code: "repository_not_found"}
		}
		return policy.failGitReplication(ctx, job, err)
	}
	if resolved.Repository.Integration.Generation != job.Generation ||
		resolved.Repository.Integration.GitAuthorityBackend != forge.BackendWalGit ||
		resolved.Repository.Integration.GitReplicationTargetBackend == "" {
		return policy.completeGitReplication(ctx, job)
	}
	source, ok := resolved.Authority.(forge.GitReplicationSource)
	if !ok || resolved.ReplicationTarget == nil {
		return policy.failGitReplication(ctx, job, &forge.Error{Status: http.StatusConflict, Code: "git_replication_unavailable"})
	}
	target, err := resolved.ReplicationTarget.GitReplicationTarget(ctx, resolved.Repository)
	if err == nil {
		err = source.ReplicateRepository(ctx, resolved.Repository, target, true, true)
	}
	target.Password = ""
	if err != nil {
		return policy.failGitReplication(ctx, job, err)
	}
	return policy.completeGitReplication(ctx, job)
}

func (policy *GitForgePolicy) completeGitReplication(ctx context.Context, job gitReplicationJob) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	tx, err := GetDb().BeginTx(queryCtx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(queryCtx, policy.Events.Schema.Render(`
		UPDATE {{.Name}}__git_replication_jobs SET processing_state = 'complete',
		  lease_until = NULL, last_error = '', completed_at = NOW(), updated_at = NOW()
		WHERE operation_id = $1`), job.OperationID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(queryCtx, policy.Events.Schema.Render(`
		UPDATE {{.Name}}__git_replication_state SET state = 'current', last_error = '',
		  last_success_at = NOW(), updated_at = NOW()
		WHERE owner = $1 AND repo = $2 AND generation = $3`),
		job.Coordinate.Owner.Hex(), job.Coordinate.ID, job.Generation); err != nil {
		return err
	}
	return tx.Commit()
}

func (policy *GitForgePolicy) failGitReplication(ctx context.Context, job gitReplicationJob, processErr error) error {
	attempts := job.Attempts + 1
	state := "failed"
	next := time.Now()
	var forgeError *forge.Error
	if (!errors.As(processErr, &forgeError) || forgeError.Retryable) && attempts < 12 {
		state = "retry"
		next = time.Now().Add(time.Duration(min(1<<min(attempts, 8), 300)) * time.Second)
		if forgeError != nil && forgeError.RetryAt.After(next) {
			next = forgeError.RetryAt
		}
	}
	failure := sanitizeGitError(processErr)
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	tx, err := GetDb().BeginTx(queryCtx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(queryCtx, policy.Events.Schema.Render(`
		UPDATE {{.Name}}__git_replication_jobs SET processing_state = $2,
		  attempt_count = $3, process_after = $4, lease_until = NULL,
		  last_error = $5, updated_at = NOW() WHERE operation_id = $1`),
		job.OperationID, state, attempts, next, failure); err != nil {
		return err
	}
	if _, err := tx.ExecContext(queryCtx, policy.Events.Schema.Render(`
		UPDATE {{.Name}}__git_replication_state SET state = $3, last_error = $4, updated_at = NOW()
		WHERE owner = $1 AND repo = $2`), job.Coordinate.Owner.Hex(), job.Coordinate.ID,
		map[bool]string{true: "failed", false: "stale"}[state == "failed"], failure); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return processErr
}

func (policy *GitForgePolicy) ReconcileGitReplicationOnce(ctx context.Context) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	var ownerHex, repo string
	var generation int64
	err := GetDb().QueryRowContext(queryCtx, policy.Events.Schema.Render(`
		SELECT integration.owner, integration.repo, integration.generation
		FROM {{.Name}}__repository_integrations integration
		LEFT JOIN {{.Name}}__git_replication_state state
		  ON state.owner = integration.owner AND state.repo = integration.repo
		WHERE integration.git_authority_backend = 'walgit'
		  AND integration.git_replication_target_backend <> ''
		ORDER BY state.last_reconcile_at NULLS FIRST, state.last_reconcile_at,
		         integration.owner, integration.repo LIMIT 1`)).Scan(&ownerHex, &repo, &generation)
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
	bucket := strconv.FormatInt(time.Now().UTC().Unix()/300, 10)
	if err := policy.enqueueGitReplication(ctx, coordinate, generation, "", "reconcile-"+bucket); err != nil {
		return err
	}
	queryCtx, cancel = context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	_, err = GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
		UPDATE {{.Name}}__git_replication_state SET last_reconcile_at = NOW(), updated_at = NOW()
		WHERE owner = $1 AND repo = $2`), ownerHex, repo)
	return err
}
