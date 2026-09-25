package zooid

import (
	"context"
	"database/sql"
	"encoding/base64"
	"fiatjaf.com/nostr"
	"net/http"
	"strconv"
	"strings"
	"zooid/internal/forge"
	"zooid/internal/projecttask"
)

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
