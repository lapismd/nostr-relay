package zooid

import (
	"context"
	"database/sql"
	"fiatjaf.com/nostr"
	"fmt"
	"net/http"
	"zooid/internal/forge"
)

func (policy *GitForgePolicy) saveForgeMapping(ctx context.Context, mapping forgeObjectMapping) error {
	if mapping.Provider == "" || mapping.ConnectionID == "" || mapping.ExternalID == "" || mapping.ObjectType == "" {
		return &forge.Error{Status: http.StatusBadRequest, Code: "invalid_forge_mapping"}
	}
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	_, err := GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
		INSERT INTO {{.Name}}__forge_object_mappings
		  (provider, connection_id, owner, repo, object_type, nostr_event_id,
		   nostr_root_id, external_id, external_number, origin, authority,
		   external_url, correlation_marker, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NULLIF($9, 0), $10, $11, $12, $13, NOW())
		ON CONFLICT (provider, connection_id, external_id, object_type) DO UPDATE SET
		  nostr_event_id = CASE WHEN EXCLUDED.nostr_event_id = '' THEN {{.Name}}__forge_object_mappings.nostr_event_id ELSE EXCLUDED.nostr_event_id END,
		  nostr_root_id = CASE WHEN EXCLUDED.nostr_root_id = '' THEN {{.Name}}__forge_object_mappings.nostr_root_id ELSE EXCLUDED.nostr_root_id END,
		  external_number = COALESCE(EXCLUDED.external_number, {{.Name}}__forge_object_mappings.external_number),
		  origin = EXCLUDED.origin, authority = EXCLUDED.authority,
		  external_url = CASE WHEN EXCLUDED.external_url = '' THEN {{.Name}}__forge_object_mappings.external_url ELSE EXCLUDED.external_url END,
		  correlation_marker = CASE WHEN EXCLUDED.correlation_marker = '' THEN {{.Name}}__forge_object_mappings.correlation_marker ELSE EXCLUDED.correlation_marker END,
		  updated_at = NOW()`),
		mapping.Provider, mapping.ConnectionID, mapping.Coordinate.Owner.Hex(), mapping.Coordinate.ID,
		mapping.ObjectType, mapping.NostrEventID, mapping.NostrRootID, mapping.ExternalID,
		mapping.ExternalNumber, mapping.Origin, mapping.Authority, mapping.ExternalURL,
		mapping.CorrelationMarker)
	return err
}

func (policy *GitForgePolicy) loadMappingByNostrRoot(ctx context.Context, provider, connectionID, rootID string) (forgeObjectMapping, bool, error) {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	return policy.scanForgeMapping(GetDb().QueryRowContext(queryCtx, policy.Events.Schema.Render(`
		SELECT provider, connection_id, owner, repo, object_type, nostr_event_id,
		       nostr_root_id, external_id, COALESCE(external_number, 0), origin,
		       authority, external_url, correlation_marker
		FROM {{.Name}}__forge_object_mappings
		WHERE provider = $1 AND connection_id = $2 AND nostr_root_id = $3
		  AND object_type IN ('issue', 'pull_request')
		ORDER BY updated_at DESC LIMIT 1`), provider, connectionID, rootID))
}

func (policy *GitForgePolicy) loadMappingByExternal(ctx context.Context, provider, connectionID, externalID, objectType string) (forgeObjectMapping, bool, error) {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	return policy.scanForgeMapping(GetDb().QueryRowContext(queryCtx, policy.Events.Schema.Render(`
		SELECT provider, connection_id, owner, repo, object_type, nostr_event_id,
		       nostr_root_id, external_id, COALESCE(external_number, 0), origin,
		       authority, external_url, correlation_marker
		FROM {{.Name}}__forge_object_mappings
		WHERE provider = $1 AND connection_id = $2 AND external_id = $3 AND object_type = $4`),
		provider, connectionID, externalID, objectType))
}

func (policy *GitForgePolicy) loadMappingByNostrEvent(ctx context.Context, provider, connectionID, eventID, objectType string) (forgeObjectMapping, bool, error) {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	return policy.scanForgeMapping(GetDb().QueryRowContext(queryCtx, policy.Events.Schema.Render(`
		SELECT provider, connection_id, owner, repo, object_type, nostr_event_id,
		       nostr_root_id, external_id, COALESCE(external_number, 0), origin,
		       authority, external_url, correlation_marker
		FROM {{.Name}}__forge_object_mappings
		WHERE provider = $1 AND connection_id = $2 AND nostr_event_id = $3 AND object_type = $4`),
		provider, connectionID, eventID, objectType))
}

func (policy *GitForgePolicy) loadCommentMappingByNostrEvent(ctx context.Context, provider, connectionID, eventID string) (forgeObjectMapping, bool, error) {
	for _, objectType := range []string{"comment", "activity"} {
		mapping, found, err := policy.loadMappingByNostrEvent(ctx, provider, connectionID, eventID, objectType)
		if err != nil || found {
			return mapping, found, err
		}
	}
	return forgeObjectMapping{}, false, nil
}

func (policy *GitForgePolicy) loadProviderReactionMappings(ctx context.Context, connectionID, targetEventID string) ([]forgeObjectMapping, error) {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	rows, err := GetDb().QueryContext(queryCtx, policy.Events.Schema.Render(`
		SELECT provider, connection_id, owner, repo, object_type, nostr_event_id,
		       nostr_root_id, external_id, COALESCE(external_number, 0), origin,
		       authority, external_url, correlation_marker
		FROM {{.Name}}__forge_object_mappings
		WHERE provider = 'github' AND connection_id = $1 AND nostr_root_id = $2
		  AND object_type = 'reaction' AND origin = 'provider' AND authority <> 'deleted'
		ORDER BY external_id`), connectionID, targetEventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]forgeObjectMapping, 0)
	for rows.Next() {
		mapping, found, err := policy.scanForgeMapping(rows)
		if err != nil {
			return nil, err
		}
		if found {
			result = append(result, mapping)
		}
	}
	return result, rows.Err()
}

func (policy *GitForgePolicy) markProviderReactionDeleted(ctx context.Context, mapping forgeObjectMapping) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	_, err := GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
		UPDATE {{.Name}}__forge_object_mappings
		SET authority = 'deleted', updated_at = NOW()
		WHERE provider = $1 AND connection_id = $2 AND external_id = $3
		  AND object_type = 'reaction' AND origin = 'provider'`),
		mapping.Provider, mapping.ConnectionID, mapping.ExternalID)
	return err
}

func (policy *GitForgePolicy) scanForgeMapping(row rowScanner) (forgeObjectMapping, bool, error) {
	var mapping forgeObjectMapping
	var ownerHex string
	err := row.Scan(
		&mapping.Provider, &mapping.ConnectionID, &ownerHex, &mapping.Coordinate.ID,
		&mapping.ObjectType, &mapping.NostrEventID, &mapping.NostrRootID,
		&mapping.ExternalID, &mapping.ExternalNumber, &mapping.Origin, &mapping.Authority,
		&mapping.ExternalURL, &mapping.CorrelationMarker,
	)
	if err == sql.ErrNoRows {
		return forgeObjectMapping{}, false, nil
	}
	if err != nil {
		return forgeObjectMapping{}, false, err
	}
	mapping.Coordinate.Owner, err = nostr.PubKeyFromHex(ownerHex)
	if err != nil {
		return forgeObjectMapping{}, false, fmt.Errorf("invalid forge mapping owner")
	}
	return mapping, true, nil
}
