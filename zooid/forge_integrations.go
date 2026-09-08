package zooid

import (
	"context"
	"database/sql"

	"zooid/internal/forge"
	"zooid/internal/gitforge"
	"zooid/internal/projecttask"
)

func legacyIntegration(repository gitforge.Repository) forge.Integration {
	gitBackend := forge.BackendNone
	health := "ready"
	switch repository.Class {
	case gitforge.RepositoryHosted:
		gitBackend = forge.BackendWalGit
		health = "provisioning"
	case gitforge.RepositoryExternal:
		gitBackend = forge.BackendUnmanaged
	case gitforge.RepositoryAnchor:
		gitBackend = forge.BackendNone
	}
	return forge.Integration{
		Coordinate: repository.Coordinate, AnnouncementID: repository.Announcement.ID.Hex(),
		ConfigurationSource: forge.ConfigurationLegacyDefault,
		GitBackend:          gitBackend, CollaborationBackend: forge.BackendRelay,
		Generation: 1, HealthState: health,
	}
}

// upsertLegacyIntegration follows repository classification only while the row
// remains a legacy default. Explicit admin configuration is authoritative and
// is never silently changed by a later clone locator.
func (policy *GitForgePolicy) upsertLegacyIntegration(ctx context.Context, repository gitforge.Repository) error {
	integration := legacyIntegration(repository)
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	_, err := GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
		INSERT INTO {{.Name}}__repository_integrations
		  (owner, repo, announcement_id, configuration_source, git_backend,
		   collaboration_backend, generation, health_state, health_detail, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, 1, $7, '', NOW())
		ON CONFLICT (owner, repo) DO UPDATE SET
		  announcement_id = EXCLUDED.announcement_id,
		  git_backend = EXCLUDED.git_backend,
		  collaboration_backend = EXCLUDED.collaboration_backend,
		  generation = {{.Name}}__repository_integrations.generation + 1,
		  health_state = EXCLUDED.health_state,
		  health_detail = '',
		  updated_at = NOW()
		WHERE {{.Name}}__repository_integrations.configuration_source = 'legacy-default'`),
		repository.Coordinate.Owner.Hex(), repository.Coordinate.ID, integration.AnnouncementID,
		integration.ConfigurationSource, integration.GitBackend, integration.CollaborationBackend,
		integration.HealthState)
	return err
}

func (policy *GitForgePolicy) markIntegrationInactive(ctx context.Context, coordinate projecttask.RepositoryCoordinate) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	_, err := GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
		INSERT INTO {{.Name}}__repository_integrations
		  (owner, repo, configuration_source, git_backend, collaboration_backend, health_state)
		VALUES ($1, $2, 'legacy-default', 'none', 'relay', 'inactive')
		ON CONFLICT (owner, repo) DO UPDATE SET
		  generation = {{.Name}}__repository_integrations.generation + 1,
		  health_state = 'inactive', health_detail = '', updated_at = NOW()
		WHERE {{.Name}}__repository_integrations.configuration_source = 'legacy-default'`),
		coordinate.Owner.Hex(), coordinate.ID)
	return err
}

func (policy *GitForgePolicy) repositoryIntegration(ctx context.Context, coordinate projecttask.RepositoryCoordinate) (forge.Integration, bool, error) {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	var integration forge.Integration
	var mirrorBackend, ciBackend, connectionID, externalID, externalRepository string
	err := GetDb().QueryRowContext(queryCtx, policy.Events.Schema.Render(`
		SELECT announcement_id, configuration_source, git_backend,
		       collaboration_backend, mirror_backend, ci_backend, connection_id,
		       external_repository_id, external_repository, generation,
		       health_state, health_detail
		FROM {{.Name}}__repository_integrations
		WHERE owner = $1 AND repo = $2`), coordinate.Owner.Hex(), coordinate.ID).Scan(
		&integration.AnnouncementID, &integration.ConfigurationSource, &integration.GitBackend,
		&integration.CollaborationBackend, &mirrorBackend, &ciBackend, &connectionID,
		&externalID, &externalRepository, &integration.Generation,
		&integration.HealthState, &integration.HealthDetail)
	if err == sql.ErrNoRows {
		return forge.Integration{}, false, nil
	}
	if err != nil {
		return forge.Integration{}, false, err
	}
	integration.Coordinate = coordinate
	integration.MirrorBackend = mirrorBackend
	integration.CIBackend = ciBackend
	integration.ConnectionID = connectionID
	integration.ExternalRepositoryID = externalID
	integration.ExternalRepository = externalRepository
	return integration, true, nil
}

func (policy *GitForgePolicy) updateLegacyIntegrationHealth(ctx context.Context, coordinate projecttask.RepositoryCoordinate, state, detail string) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	_, err := GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
		UPDATE {{.Name}}__repository_integrations
		SET health_state = $3, health_detail = $4, updated_at = NOW()
		WHERE owner = $1 AND repo = $2
		  AND configuration_source = 'legacy-default' AND git_backend = 'walgit'`),
		coordinate.Owner.Hex(), coordinate.ID, state, detail)
	return err
}

func forgeRepository(repository gitforge.Repository, integration forge.Integration) forge.Repository {
	return forge.Repository{
		Coordinate: repository.Coordinate, Announcement: repository.Announcement,
		Channel: repository.Channel, Classification: string(repository.Class),
		CloneURL: repository.CloneURL, Integration: integration,
	}
}

// ResolveIntegration composes the current signed announcement with its
// operational attachment. It does not rewrite either source.
func (policy *GitForgePolicy) ResolveIntegration(ctx context.Context, coordinate projecttask.RepositoryCoordinate) (forge.ResolvedRepository, bool, error) {
	repository, found, err := policy.resolveRepository(ctx, coordinate)
	if err != nil || !found {
		return forge.ResolvedRepository{}, false, err
	}
	integration, found, err := policy.repositoryIntegration(ctx, coordinate)
	if err != nil {
		return forge.ResolvedRepository{}, false, err
	}
	if !found {
		if err := policy.upsertLegacyIntegration(ctx, repository); err != nil {
			return forge.ResolvedRepository{}, false, err
		}
		integration = legacyIntegration(repository)
	}
	resolved, err := policy.Registry.Resolve(ctx, forgeRepository(repository, integration))
	if err != nil {
		return forge.ResolvedRepository{}, false, err
	}
	return resolved, true, nil
}

var _ forge.RepositoryIntegrationResolver = (*GitForgePolicy)(nil)
