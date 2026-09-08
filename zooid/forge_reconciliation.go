package zooid

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"time"

	"fiatjaf.com/nostr"
	"zooid/internal/forge"
	"zooid/internal/projecttask"
)

func (policy *GitForgePolicy) ReconcileGitHubOnce(ctx context.Context) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	var ownerHex, repo, connectionID string
	var updatedSince time.Time
	err := GetDb().QueryRowContext(queryCtx, policy.Events.Schema.Render(`
		SELECT integration.owner, integration.repo, integration.connection_id,
		       COALESCE(cursor.overlap_from, integration.updated_at - INTERVAL '5 minutes')
		FROM {{.Name}}__repository_integrations AS integration
		LEFT JOIN {{.Name}}__forge_reconciliation_cursors AS cursor
		  ON cursor.provider = 'github'
		 AND cursor.connection_id = integration.connection_id
		 AND cursor.owner = integration.owner AND cursor.repo = integration.repo
		 AND cursor.object_family = 'collaboration'
		WHERE integration.collaboration_backend = 'github'
		  AND integration.health_state NOT IN ('inactive', 'disabled', 'migrating')
		ORDER BY cursor.updated_at NULLS FIRST, integration.updated_at, integration.owner, integration.repo
		LIMIT 1`)).Scan(&ownerHex, &repo, &connectionID, &updatedSince)
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
	resolved, found, err := policy.ResolveIntegration(ctx, projecttask.RepositoryCoordinate{Owner: owner, ID: repo})
	if err != nil || !found {
		return err
	}
	reconciler, ok := resolved.Collaboration.(forge.CollaborationReconciler)
	if !ok {
		return nil
	}
	result, err := reconciler.Reconcile(ctx, resolved.Repository, forge.ProviderImportRequest{
		UpdatedSince: updatedSince, MaximumRoots: 1000, MaximumComments: 500,
	})
	if err != nil {
		return err
	}
	for _, event := range result.Events {
		digest := sha256.Sum256(event.Payload)
		if _, err := policy.storeGitHubWebhook(ctx, event, hex.EncodeToString(digest[:])); err != nil {
			return err
		}
	}
	now := time.Now().UTC()
	overlap := now.Add(-5 * time.Minute)
	queryCtx, cancel = context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	_, err = GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
		INSERT INTO {{.Name}}__forge_reconciliation_cursors
		  (provider, connection_id, owner, repo, object_family, cursor, overlap_from, updated_at)
		VALUES ('github', $1, $2, $3, 'collaboration', $4, $5, NOW())
		ON CONFLICT (provider, connection_id, owner, repo, object_family) DO UPDATE SET
		  cursor = EXCLUDED.cursor, overlap_from = EXCLUDED.overlap_from, updated_at = NOW()`),
		connectionID, ownerHex, repo, now.Format(time.RFC3339Nano), overlap)
	return err
}
