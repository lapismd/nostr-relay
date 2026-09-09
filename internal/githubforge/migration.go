package githubforge

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"zooid/internal/forge"
)

func (backend *Backend) SnapshotRefs(ctx context.Context, repository forge.Repository) (map[string]string, error) {
	refs := make(map[string]string)
	for _, prefix := range []string{"heads", "tags"} {
		for page := 1; page <= 100; page++ {
			path := repoPath(repository) + "/git/matching-refs/" + prefix + "?per_page=100&page=" + strconv.Itoa(page)
			response, err := backend.doRepository(ctx, repository, http.MethodGet, path, nil, gitReadPermissions)
			if err != nil {
				return nil, err
			}
			var batch []gitHubRef
			if err := decodeAndClose(response, &batch, 8<<20); err != nil {
				return nil, err
			}
			for _, ref := range batch {
				if !strings.HasPrefix(ref.Ref, "refs/"+prefix+"/") || !fullObjectID(ref.Object.SHA) {
					return nil, &forge.Error{Status: http.StatusBadGateway, Code: "invalid_github_ref"}
				}
				refs[ref.Ref] = strings.ToLower(ref.Object.SHA)
			}
			if len(batch) < 100 {
				break
			}
		}
	}
	return refs, nil
}

func (backend *Backend) CloneSource(ctx context.Context, repository forge.Repository) (string, string, string, error) {
	connection, repositoryID, err := backend.connectionFor(ctx, repository)
	if err != nil {
		return "", "", "", err
	}
	token, err := backend.tokens.installation(ctx, connection, repositoryID, gitReadPermissions)
	if err != nil {
		return "", "", "", err
	}
	descriptor, err := backend.CloneDescriptor(ctx, repository)
	if err != nil {
		return "", "", "", err
	}
	return descriptor.URL, "x-access-token", token, nil
}

func (backend *Backend) GitReplicationTarget(ctx context.Context, repository forge.Repository) (forge.GitReplicationTarget, error) {
	connection, repositoryID, err := backend.connectionFor(ctx, repository)
	if err != nil {
		return forge.GitReplicationTarget{}, err
	}
	token, err := backend.tokens.installation(ctx, connection, repositoryID, gitWritePermissions)
	if err != nil {
		return forge.GitReplicationTarget{}, err
	}
	descriptor, err := backend.CloneDescriptor(ctx, repository)
	if err != nil {
		return forge.GitReplicationTarget{}, err
	}
	return forge.GitReplicationTarget{CloneURL: descriptor.URL, Username: "x-access-token", Password: token}, nil
}

func (backend *Backend) Project(ctx context.Context, projection forge.MirrorProjection) (forge.CollaborationResult, error) {
	return backend.Execute(ctx, forge.CollaborationCommand{
		OperationID: projection.OperationID, Kind: projection.Kind,
		Actor: projection.Event.PubKey, Repository: projection.Repository,
		RootEventID: projection.RootEventID, ExpectedHead: projection.ExpectedHead,
		CorrelationMarker: projection.CorrelationMarker, Payload: projection.Payload,
	})
}

var _ forge.GitAuthorityBackend = (*Backend)(nil)
var _ forge.AuthenticatedCloneSource = (*Backend)(nil)
var _ forge.GitReplicationTargetBackend = (*Backend)(nil)
var _ forge.MirrorBackend = (*Backend)(nil)
