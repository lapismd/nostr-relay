package zooid

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"
	"zooid/internal/forge"
	"zooid/internal/gitforge"
)

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
