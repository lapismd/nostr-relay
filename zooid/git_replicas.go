package zooid

import (
	"context"
	"database/sql"
	"fiatjaf.com/nostr"
	"net/http"
	"strconv"
	"strings"
	"zooid/internal/forge"
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
