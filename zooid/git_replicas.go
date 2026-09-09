package zooid

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"zooid/internal/forge"
	"zooid/internal/gitforge"
	"zooid/internal/projecttask"
)

const replicaDirectionAuthorityToReplica = "authority-to-replica"

type gitReplica struct {
	Coordinate           projecttask.RepositoryCoordinate
	AuthorityBackend     string
	ReplicaBackend       string
	ConnectionID         string
	ExternalRepositoryID string
	DefaultBranch        string
	State                string
	Generation           int64
	LastError            string
	LastSyncAt           sql.NullTime
	LastSuccessAt        sql.NullTime
	LastAuthorityCheckAt sql.NullTime
	LastReconcileAt      sql.NullTime
}

type gitReplicaJob struct {
	OperationID string
	Coordinate  projecttask.RepositoryCoordinate
	Generation  int64
	DeliveryID  string
	RefName     string
	TargetOID   string
	Reason      string
	Attempts    int
}

func replicaPrincipal(connectionID string) string { return "github-replica:" + connectionID }

func (policy *GitForgePolicy) configureGitReplica(ctx context.Context, integration forge.Integration) error {
	stored, found, err := policy.repositoryIntegration(ctx, integration.Coordinate)
	if err != nil || !found {
		return err
	}
	if stored.Replica == nil || !stored.Replica.Enabled {
		queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
		defer cancel()
		_, err := GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
			UPDATE {{.Name}}__git_replicas SET state = 'disabled', updated_at = NOW()
			WHERE owner = $1 AND repo = $2`), stored.Coordinate.Owner.Hex(), stored.Coordinate.ID)
		return err
	}
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	_, err = GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
		INSERT INTO {{.Name}}__git_replicas
		  (owner, repo, authority_backend, replica_backend, connection_id,
		   external_repository_id, state, generation, last_error, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, 'provisioning', $7, '', NOW())
		ON CONFLICT (owner, repo) DO UPDATE SET
		  authority_backend = EXCLUDED.authority_backend,
		  replica_backend = EXCLUDED.replica_backend,
		  connection_id = EXCLUDED.connection_id,
		  external_repository_id = EXCLUDED.external_repository_id,
		  state = 'provisioning', generation = EXCLUDED.generation,
		  last_error = '', updated_at = NOW()`),
		stored.Coordinate.Owner.Hex(), stored.Coordinate.ID, stored.GitAuthorityBackend,
		stored.ContentBackend, stored.ConnectionID, stored.ExternalRepositoryID, stored.Generation)
	if err != nil {
		return err
	}
	return policy.enqueueGitReplicaSync(ctx, stored.Coordinate, stored.Generation, forge.ReplicaSyncRequest{Reason: "provision"})
}

func (policy *GitForgePolicy) enqueueGitReplicaSync(ctx context.Context, coordinate projecttask.RepositoryCoordinate, generation int64, request forge.ReplicaSyncRequest) error {
	operationID, err := newOperationID("rep")
	if err != nil {
		return err
	}
	reason := request.Reason
	if reason == "" {
		reason = "sync"
	}
	dedup := coordinate.String() + ":" + strconv.FormatInt(generation, 10) + ":" + reason + ":" + request.RefName + ":" + request.TargetOID
	if request.DeliveryID != "" {
		dedup = "github-delivery:" + request.DeliveryID
	}
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	_, err = GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
		INSERT INTO {{.Name}}__git_replica_jobs
		  (operation_id, owner, repo, generation, source_delivery_id,
		   requested_ref, target_oid, reason, deduplication_key)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (deduplication_key) DO NOTHING`), operationID,
		coordinate.Owner.Hex(), coordinate.ID, generation, request.DeliveryID,
		request.RefName, strings.ToLower(request.TargetOID), reason, dedup)
	return err
}

func (policy *GitForgePolicy) claimGitReplicaJob(ctx context.Context) (gitReplicaJob, bool, error) {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	tx, err := GetDb().BeginTx(queryCtx, &sql.TxOptions{})
	if err != nil {
		return gitReplicaJob{}, false, err
	}
	defer tx.Rollback()
	var job gitReplicaJob
	var ownerHex string
	err = tx.QueryRowContext(queryCtx, policy.Events.Schema.Render(`
		SELECT operation_id, owner, repo, generation, source_delivery_id,
		       requested_ref, target_oid, reason, attempt_count
		FROM {{.Name}}__git_replica_jobs
		WHERE (processing_state IN ('pending', 'retry') AND process_after <= NOW())
		   OR (processing_state = 'running' AND lease_until < NOW())
		ORDER BY process_after, created_at, operation_id
		FOR UPDATE SKIP LOCKED LIMIT 1`)).Scan(&job.OperationID, &ownerHex,
		&job.Coordinate.ID, &job.Generation, &job.DeliveryID, &job.RefName,
		&job.TargetOID, &job.Reason, &job.Attempts)
	if err == sql.ErrNoRows {
		return gitReplicaJob{}, false, nil
	}
	if err != nil {
		return gitReplicaJob{}, false, err
	}
	job.Coordinate.Owner, err = nostr.PubKeyFromHex(ownerHex)
	if err != nil {
		return gitReplicaJob{}, false, err
	}
	_, err = tx.ExecContext(queryCtx, policy.Events.Schema.Render(`
		UPDATE {{.Name}}__git_replica_jobs
		SET processing_state = 'running', lease_until = NOW() + INTERVAL '5 minutes', updated_at = NOW()
		WHERE operation_id = $1`), job.OperationID)
	if err != nil {
		return gitReplicaJob{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return gitReplicaJob{}, false, err
	}
	return job, true, nil
}

func (policy *GitForgePolicy) ProcessGitReplicaOnce(ctx context.Context) error {
	job, found, err := policy.claimGitReplicaJob(ctx)
	if err != nil || !found {
		return err
	}
	resolved, found, err := policy.ResolveIntegration(ctx, job.Coordinate)
	if err != nil || !found {
		if err == nil {
			err = &forge.Error{Status: http.StatusNotFound, Code: "repository_not_found"}
		}
		return policy.failGitReplicaJob(ctx, job, err)
	}
	if resolved.Repository.Integration.Generation != job.Generation || resolved.Repository.Integration.Replica == nil || !resolved.Repository.Integration.Replica.Enabled {
		return policy.completeGitReplicaJob(ctx, job)
	}
	err = policy.Sync(ctx, resolved.Repository, forge.ReplicaSyncRequest{DeliveryID: job.DeliveryID, RefName: job.RefName, TargetOID: job.TargetOID, Reason: job.Reason})
	if err != nil {
		return policy.failGitReplicaJob(ctx, job, err)
	}
	return policy.completeGitReplicaJob(ctx, job)
}

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

func (policy *GitForgePolicy) loadGitReplica(ctx context.Context, coordinate projecttask.RepositoryCoordinate) (gitReplica, bool, error) {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	var replica gitReplica
	var ownerHex string
	err := GetDb().QueryRowContext(queryCtx, policy.Events.Schema.Render(`
		SELECT owner, repo, authority_backend, replica_backend, connection_id,
		       external_repository_id, default_branch, state, generation, last_error,
		       last_sync_at, last_success_at, last_authority_check_at, last_reconcile_at
		FROM {{.Name}}__git_replicas WHERE owner = $1 AND repo = $2`),
		coordinate.Owner.Hex(), coordinate.ID).Scan(&ownerHex, &replica.Coordinate.ID,
		&replica.AuthorityBackend, &replica.ReplicaBackend, &replica.ConnectionID,
		&replica.ExternalRepositoryID, &replica.DefaultBranch, &replica.State,
		&replica.Generation, &replica.LastError, &replica.LastSyncAt,
		&replica.LastSuccessAt, &replica.LastAuthorityCheckAt, &replica.LastReconcileAt)
	if err == sql.ErrNoRows {
		return gitReplica{}, false, nil
	}
	if err != nil {
		return gitReplica{}, false, err
	}
	replica.Coordinate.Owner, err = nostr.PubKeyFromHex(ownerHex)
	return replica, err == nil, err
}

func (policy *GitForgePolicy) gitReplicaBehindRefs(ctx context.Context, coordinate projecttask.RepositoryCoordinate) (int, error) {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	var count int
	err := GetDb().QueryRowContext(queryCtx, policy.Events.Schema.Render(`
		SELECT COUNT(*) FROM {{.Name}}__git_replica_refs
		WHERE owner = $1 AND repo = $2 AND authority_oid <> replica_oid`),
		coordinate.Owner.Hex(), coordinate.ID).Scan(&count)
	return count, err
}

func (policy *GitForgePolicy) gitReplicaRefs(ctx context.Context, coordinate projecttask.RepositoryCoordinate) (map[string]string, error) {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	rows, err := GetDb().QueryContext(queryCtx, policy.Events.Schema.Render(`
		SELECT ref_name, replica_oid FROM {{.Name}}__git_replica_refs
		WHERE owner = $1 AND repo = $2 AND replica_oid <> '' ORDER BY ref_name`),
		coordinate.Owner.Hex(), coordinate.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	refs := make(map[string]string)
	for rows.Next() {
		var name, oid string
		if err := rows.Scan(&name, &oid); err != nil {
			return nil, err
		}
		refs[name] = oid
	}
	return refs, rows.Err()
}

func (policy *GitForgePolicy) replicaRefPage(ctx context.Context, repository forge.Repository, kind, continuation string) (forge.RefPage, bool, error) {
	replica, found, err := policy.loadGitReplica(ctx, repository.Coordinate)
	if err != nil || !found || (replica.State != "current" && repository.Integration.Replica.ReadPolicy != forge.ReplicaReadStale) {
		return forge.RefPage{}, false, err
	}
	if kind == "" {
		name := "refs/heads/" + replica.DefaultBranch
		queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
		defer cancel()
		var oid string
		err := GetDb().QueryRowContext(queryCtx, policy.Events.Schema.Render(`SELECT replica_oid FROM {{.Name}}__git_replica_refs WHERE owner = $1 AND repo = $2 AND ref_name = $3 AND replica_oid <> ''`), repository.Coordinate.Owner.Hex(), repository.Coordinate.ID, name).Scan(&oid)
		if err == sql.ErrNoRows {
			return forge.RefPage{}, false, nil
		}
		if err != nil {
			return forge.RefPage{}, false, err
		}
		result := forge.RefPage{Head: &forge.Ref{Name: name, SHA: oid}}
		if replica.State != "current" {
			result.Incomplete = []string{"stale-replica"}
		}
		return result, true, nil
	}
	offset, err := decodeReplicaContinuation(kind, continuation)
	if err != nil {
		return forge.RefPage{}, false, err
	}
	prefix := "refs/heads/%"
	if kind == "tags" {
		prefix = "refs/tags/%"
	}
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	rows, err := GetDb().QueryContext(queryCtx, policy.Events.Schema.Render(`
		SELECT ref_name, replica_oid FROM {{.Name}}__git_replica_refs
		WHERE owner = $1 AND repo = $2 AND ref_name LIKE $3 AND replica_oid <> ''
		ORDER BY ref_name OFFSET $4 LIMIT 101`), repository.Coordinate.Owner.Hex(), repository.Coordinate.ID, prefix, offset)
	if err != nil {
		return forge.RefPage{}, false, err
	}
	defer rows.Close()
	refs := make([]forge.Ref, 0, 100)
	for rows.Next() {
		var ref forge.Ref
		if err := rows.Scan(&ref.Name, &ref.SHA); err != nil {
			return forge.RefPage{}, false, err
		}
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		return forge.RefPage{}, false, err
	}
	result := forge.RefPage{Refs: refs}
	if len(result.Refs) > 100 {
		result.Refs = result.Refs[:100]
		result.More = true
		result.Continuation = encodeReplicaContinuation(kind, offset+100)
	}
	if replica.State != "current" {
		result.Incomplete = append(result.Incomplete, "stale-replica")
	}
	return result, true, nil
}

func (policy *GitForgePolicy) contentBackendsForRead(ctx context.Context, resolved forge.ResolvedRepository, address string) (forge.RepositoryContentBackend, forge.RepositoryContentBackend, bool, error) {
	config := resolved.Repository.Integration.Replica
	if config == nil || !config.Enabled {
		return resolved.Content, nil, false, nil
	}
	replica, found, err := policy.loadGitReplica(ctx, resolved.Repository.Coordinate)
	if err != nil {
		return nil, nil, false, err
	}
	provider := policy.Registry.ContentBackend(resolved.Repository.Integration.GitAuthorityBackend)
	if immutableAddress(address) {
		return resolved.Content, providerFallback(config, provider), false, nil
	}
	if found && replica.State == "current" {
		return resolved.Content, providerFallback(config, provider), false, nil
	}
	reason := "on-demand-" + strconv.FormatInt(time.Now().UTC().Unix()/5, 10)
	_ = policy.enqueueGitReplicaSync(ctx, resolved.Repository.Coordinate, resolved.Repository.Integration.Generation, forge.ReplicaSyncRequest{Reason: reason})
	if config.WaitMilliseconds > 0 && (config.ReadPolicy == forge.ReplicaReadWait || config.ReadPolicy == forge.ReplicaReadFallback) {
		deadline := time.Now().Add(time.Duration(config.WaitMilliseconds) * time.Millisecond)
		for time.Now().Before(deadline) {
			select {
			case <-ctx.Done():
				return nil, nil, false, ctx.Err()
			case <-time.After(50 * time.Millisecond):
			}
			current, ok, loadErr := policy.loadGitReplica(ctx, resolved.Repository.Coordinate)
			if loadErr != nil {
				return nil, nil, false, loadErr
			}
			if ok && current.State == "current" {
				return resolved.Content, providerFallback(config, provider), false, nil
			}
		}
	}
	switch config.ReadPolicy {
	case forge.ReplicaReadFallback:
		if provider == nil {
			return nil, nil, false, &forge.Error{Status: http.StatusServiceUnavailable, Code: "provider_unavailable_or_replica_stale", Retryable: true}
		}
		return provider, nil, false, nil
	case forge.ReplicaReadStale:
		return resolved.Content, nil, true, nil
	default:
		return nil, nil, false, &forge.Error{Status: http.StatusServiceUnavailable, Code: "replica_stale", Retryable: true}
	}
}

func providerFallback(config *forge.ReplicaConfig, provider forge.RepositoryContentBackend) forge.RepositoryContentBackend {
	if config != nil && config.ReadPolicy == forge.ReplicaReadFallback {
		return provider
	}
	return nil
}

func immutableAddress(address string) bool {
	component := strings.SplitN(strings.Trim(address, "/"), "/", 2)[0]
	return fullForgeObjectID(component)
}

func markStaleResult(result any) any {
	metadata := repositoryResultMetadata(result)
	if metadata == nil {
		return result
	}
	copyMetadata := *metadata
	copyMetadata.Incomplete = append(copyMetadata.Incomplete, "stale-replica")
	switch value := result.(type) {
	case forge.Revision:
		value.ResultMetadata = copyMetadata
		return value
	case forge.Tree:
		value.ResultMetadata = copyMetadata
		return value
	case forge.Blob:
		value.ResultMetadata = copyMetadata
		return value
	case forge.CommitPage:
		value.ResultMetadata = copyMetadata
		return value
	case forge.CommitDetail:
		value.ResultMetadata = copyMetadata
		return value
	case forge.Diff:
		value.ResultMetadata = copyMetadata
		return value
	default:
		return result
	}
}

func (policy *GitForgePolicy) ensureReplicaObject(ctx context.Context, repository forge.Repository, oid string) error {
	if !fullForgeObjectID(oid) {
		return &forge.Error{Status: http.StatusBadRequest, Code: "invalid_git_object"}
	}
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	_, err := GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
		INSERT INTO {{.Name}}__git_replica_objects (owner, repo, object_oid, reason, state)
		VALUES ($1, $2, $3, 'content-read', 'fetching')
		ON CONFLICT (owner, repo, object_oid) DO UPDATE SET state = 'fetching', last_error = '', requested_at = NOW()`), repository.Coordinate.Owner.Hex(), repository.Coordinate.ID, strings.ToLower(oid))
	cancel()
	if err != nil {
		return err
	}
	resolved, err := policy.Registry.Resolve(ctx, repository)
	if err != nil {
		return err
	}
	source, ok := resolved.Authority.(forge.AuthenticatedCloneSource)
	if !ok {
		return &forge.Error{Status: http.StatusConflict, Code: "source_clone_unsupported"}
	}
	importer, ok := policy.Registry.ContentBackend(repository.Integration.ContentBackend).(forge.RepositoryObjectImporter)
	if !ok {
		return &forge.Error{Status: http.StatusConflict, Code: "repository_object_import_unavailable"}
	}
	cloneURL, username, password, err := source.CloneSource(ctx, repository)
	if err != nil {
		return err
	}
	target := repository
	target.Classification = string(gitforge.RepositoryHosted)
	target.Integration.GitBackend = forge.BackendWalGit
	target.Integration.GitAuthorityBackend = forge.BackendWalGit
	target.Integration.ContentBackend = forge.BackendWalGit
	err = importer.ImportObject(ctx, target, forge.ImportObjectRequest{SourceCloneURL: cloneURL, ObjectOID: strings.ToLower(oid), SourceUsername: username, SourcePassword: password, DestinationPrincipal: replicaPrincipal(repository.Integration.ConnectionID)})
	password = ""
	state, failure := "current", ""
	if err != nil {
		state, failure = "failed", sanitizeGitError(err)
	}
	queryCtx, cancel = context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	_, storeErr := GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`UPDATE {{.Name}}__git_replica_objects SET state = $4, last_error = $5, verified_at = CASE WHEN $4 = 'current' THEN NOW() ELSE NULL END WHERE owner = $1 AND repo = $2 AND object_oid = $3`), repository.Coordinate.Owner.Hex(), repository.Coordinate.ID, strings.ToLower(oid), state, failure)
	if err != nil {
		return err
	}
	return storeErr
}

func encodeReplicaContinuation(kind string, offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(kind + ":" + strconv.Itoa(offset)))
}
func decodeReplicaContinuation(kind, token string) (int, error) {
	if token == "" {
		return 0, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) > 64 || !strings.HasPrefix(string(raw), kind+":") {
		return 0, &forge.Error{Status: http.StatusBadRequest, Code: "invalid_continuation"}
	}
	value, err := strconv.Atoi(strings.TrimPrefix(string(raw), kind+":"))
	if err != nil || value < 0 {
		return 0, &forge.Error{Status: http.StatusBadRequest, Code: "invalid_continuation"}
	}
	return value, nil
}

var _ forge.GitReplicaCoordinator = (*GitForgePolicy)(nil)
