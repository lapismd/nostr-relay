package zooid

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore"
	"zooid/internal/forge"
	"zooid/internal/githubforge"
	"zooid/internal/projecttask"
)

const (
	forgeOperationPending    = "pending"
	forgeOperationRunning    = "running"
	forgeOperationProjecting = "projecting"
	forgeOperationRetry      = "retry"
	forgeOperationComplete   = "complete"
	forgeOperationFailed     = "failed"
)

type forgeOperation struct {
	ID                string
	Provider          string
	ConnectionID      string
	Coordinate        projecttask.RepositoryCoordinate
	Command           string
	ObjectType        string
	Actor             nostr.PubKey
	CredentialMode    string
	RootEventID       string
	SourceEventID     string
	ExpectedHead      string
	Payload           json.RawMessage
	IdempotencyKey    string
	CorrelationMarker string
	State             string
	Attempts          int
	ProviderObjectID  string
	ProviderResult    *forge.CollaborationResult
	Failure           string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type forgeObjectMapping struct {
	Provider          string
	ConnectionID      string
	Coordinate        projecttask.RepositoryCoordinate
	ObjectType        string
	NostrEventID      string
	NostrRootID       string
	ExternalID        string
	ExternalNumber    int64
	Origin            string
	Authority         string
	ExternalURL       string
	CorrelationMarker string
}

func forgeCorrelationMarker(idempotencyKey string) string {
	digest := sha256.Sum256([]byte("lapis-forge-v1\x00" + idempotencyKey))
	return hex.EncodeToString(digest[:16])
}

func (policy *GitForgePolicy) enqueueForgeOperation(ctx context.Context, operation forgeOperation) (forgeOperation, error) {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	if operation.ID == "" || operation.IdempotencyKey == "" || operation.Provider == "" || operation.Command == "" {
		return forgeOperation{}, &forge.Error{Status: http.StatusBadRequest, Code: "invalid_forge_operation"}
	}
	if operation.CorrelationMarker == "" {
		operation.CorrelationMarker = forgeCorrelationMarker(operation.IdempotencyKey)
	}
	if operation.CredentialMode == "" {
		operation.CredentialMode = forge.CredentialSystem
		if operation.Provider == forge.BackendGitHub && operation.Actor != nostr.ZeroPK && policy.UserCredentialMode != nil && userAttributedForgeCommand(operation.Command) {
			mode, modeErr := policy.UserCredentialMode(queryCtx, operation.Actor)
			if modeErr != nil {
				return forgeOperation{}, modeErr
			}
			if mode == forge.CredentialUser {
				operation.CredentialMode = mode
			}
		}
	}
	_, err := GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
		INSERT INTO {{.Name}}__forge_outbox
		  (operation_id, provider, connection_id, owner, repo, command,
		   object_type, actor, credential_mode, nostr_root_id, source_event_id, expected_head, payload,
		   idempotency_key, correlation_marker, processing_state)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, 'pending')
		ON CONFLICT (idempotency_key) DO NOTHING`),
		operation.ID, operation.Provider, operation.ConnectionID,
		operation.Coordinate.Owner.Hex(), operation.Coordinate.ID, operation.Command,
		operation.ObjectType, operation.Actor.Hex(), operation.CredentialMode, operation.RootEventID, operation.SourceEventID,
		operation.ExpectedHead, operation.Payload, operation.IdempotencyKey,
		operation.CorrelationMarker)
	if err != nil {
		return forgeOperation{}, err
	}
	return policy.loadForgeOperationByIdempotency(queryCtx, operation.IdempotencyKey)
}

func userAttributedForgeCommand(command string) bool {
	switch command {
	case "create_issue", "create_pull_request", "comment", "review", "review_comment", "state", "merge", "reaction_add", "reaction_delete":
		return true
	default:
		return false
	}
}

func (policy *GitForgePolicy) loadForgeOperation(ctx context.Context, id string) (forgeOperation, bool, error) {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	operation, err := policy.scanForgeOperation(GetDb().QueryRowContext(queryCtx, policy.Events.Schema.Render(`
		SELECT operation_id, provider, connection_id, owner, repo, command,
		       object_type, actor, credential_mode, nostr_root_id, source_event_id, expected_head, payload,
		       idempotency_key, correlation_marker, processing_state,
		       attempt_count, provider_object_id, provider_result,
		       sanitized_failure, created_at, updated_at
		FROM {{.Name}}__forge_outbox WHERE operation_id = $1`), id))
	if err == sql.ErrNoRows {
		return forgeOperation{}, false, nil
	}
	return operation, err == nil, err
}

func (policy *GitForgePolicy) loadForgeOperationByIdempotency(ctx context.Context, key string) (forgeOperation, error) {
	return policy.scanForgeOperation(GetDb().QueryRowContext(ctx, policy.Events.Schema.Render(`
		SELECT operation_id, provider, connection_id, owner, repo, command,
		       object_type, actor, credential_mode, nostr_root_id, source_event_id, expected_head, payload,
		       idempotency_key, correlation_marker, processing_state,
		       attempt_count, provider_object_id, provider_result,
		       sanitized_failure, created_at, updated_at
		FROM {{.Name}}__forge_outbox WHERE idempotency_key = $1`), key))
}

type rowScanner interface {
	Scan(...any) error
}

func (policy *GitForgePolicy) scanForgeOperation(row rowScanner) (forgeOperation, error) {
	var operation forgeOperation
	var ownerHex, actorHex string
	var resultJSON []byte
	err := row.Scan(
		&operation.ID, &operation.Provider, &operation.ConnectionID, &ownerHex,
		&operation.Coordinate.ID, &operation.Command, &operation.ObjectType,
		&actorHex, &operation.CredentialMode, &operation.RootEventID, &operation.SourceEventID, &operation.ExpectedHead, &operation.Payload,
		&operation.IdempotencyKey, &operation.CorrelationMarker, &operation.State,
		&operation.Attempts, &operation.ProviderObjectID, &resultJSON,
		&operation.Failure, &operation.CreatedAt, &operation.UpdatedAt,
	)
	if err != nil {
		return forgeOperation{}, err
	}
	operation.Coordinate.Owner, err = nostr.PubKeyFromHex(ownerHex)
	if err != nil {
		return forgeOperation{}, fmt.Errorf("invalid forge operation owner")
	}
	operation.Actor, err = nostr.PubKeyFromHex(actorHex)
	if err != nil {
		return forgeOperation{}, fmt.Errorf("invalid forge operation actor")
	}
	if len(resultJSON) != 0 {
		var result forge.CollaborationResult
		if err := json.Unmarshal(resultJSON, &result); err != nil {
			return forgeOperation{}, fmt.Errorf("invalid stored forge result")
		}
		operation.ProviderResult = &result
	}
	return operation, nil
}

func (policy *GitForgePolicy) claimForgeOperation(ctx context.Context) (forgeOperation, bool, error) {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	tx, err := GetDb().BeginTx(queryCtx, &sql.TxOptions{})
	if err != nil {
		return forgeOperation{}, false, err
	}
	defer tx.Rollback()
	operation, err := policy.scanForgeOperation(tx.QueryRowContext(queryCtx, policy.Events.Schema.Render(`
		SELECT operation_id, provider, connection_id, owner, repo, command,
		       object_type, actor, credential_mode, nostr_root_id, source_event_id, expected_head, payload,
		       idempotency_key, correlation_marker, processing_state,
		       attempt_count, provider_object_id, provider_result,
		       sanitized_failure, created_at, updated_at
		FROM {{.Name}}__forge_outbox
		WHERE (
		  processing_state IN ('pending', 'retry') AND next_attempt_at <= NOW()
		) OR (
		  processing_state IN ('running', 'projecting') AND lease_until < NOW()
		)
		ORDER BY next_attempt_at, created_at, operation_id
		FOR UPDATE SKIP LOCKED LIMIT 1`)))
	if err == sql.ErrNoRows {
		return forgeOperation{}, false, nil
	}
	if err != nil {
		return forgeOperation{}, false, err
	}
	state := forgeOperationRunning
	if operation.ProviderResult != nil {
		state = forgeOperationProjecting
	}
	_, err = tx.ExecContext(queryCtx, policy.Events.Schema.Render(`
		UPDATE {{.Name}}__forge_outbox
		SET processing_state = $2, lease_until = NOW() + INTERVAL '60 seconds', updated_at = NOW()
		WHERE operation_id = $1`), operation.ID, state)
	if err != nil {
		return forgeOperation{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return forgeOperation{}, false, err
	}
	operation.State = state
	return operation, true, nil
}

func (policy *GitForgePolicy) storeForgeProviderResult(ctx context.Context, operation forgeOperation, result forge.CollaborationResult) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	encoded, _ := json.Marshal(result)
	_, err := GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
		UPDATE {{.Name}}__forge_outbox
		SET provider_result = $2, provider_object_id = $3,
		    processing_state = 'projecting', lease_until = NOW() + INTERVAL '60 seconds',
		    sanitized_failure = '', updated_at = NOW()
		WHERE operation_id = $1`), operation.ID, encoded, result.ExternalID)
	return err
}

func (policy *GitForgePolicy) finishForgeOperation(ctx context.Context, operation forgeOperation) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	_, err := GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
		UPDATE {{.Name}}__forge_outbox
		SET processing_state = 'complete', lease_until = NULL,
		    sanitized_failure = '', completed_at = NOW(), updated_at = NOW()
		WHERE operation_id = $1`), operation.ID)
	return err
}

func (policy *GitForgePolicy) failForgeOperation(ctx context.Context, operation forgeOperation, operationErr error) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	attempts := operation.Attempts + 1
	state := forgeOperationFailed
	nextAttempt := time.Now()
	var backendError *forge.Error
	if errors.As(operationErr, &backendError) && backendError.Retryable && attempts < 12 {
		state = forgeOperationRetry
		nextAttempt = time.Now().Add(time.Duration(math.Min(math.Pow(2, float64(min(attempts, 8))), 300)) * time.Second)
		if backendError.RetryAt.After(nextAttempt) {
			nextAttempt = backendError.RetryAt
		}
	}
	failure := "forge_operation_failed"
	if errors.As(operationErr, &backendError) && backendError.Code != "" {
		failure = backendError.Code
	}
	_, err := GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
		UPDATE {{.Name}}__forge_outbox
		SET processing_state = $2, attempt_count = $3, next_attempt_at = $4,
		    lease_until = NULL, sanitized_failure = $5, updated_at = NOW()
		WHERE operation_id = $1`), operation.ID, state, attempts, nextAttempt, sanitizeGitErrorString(failure))
	return err
}

func (policy *GitForgePolicy) retryForgeOperation(ctx context.Context, id string) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	result, err := GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
		UPDATE {{.Name}}__forge_outbox
		SET processing_state = CASE WHEN provider_result IS NULL THEN 'pending' ELSE 'projecting' END,
		    next_attempt_at = NOW(), lease_until = NULL, sanitized_failure = '', updated_at = NOW()
		WHERE operation_id = $1 AND processing_state = 'failed'`), id)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows != 1 {
		return &forge.Error{Status: http.StatusConflict, Code: "operation_not_retryable"}
	}
	return nil
}

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

func (policy *GitForgePolicy) ProcessForgeOutboxOnce(ctx context.Context) error {
	operation, found, err := policy.claimForgeOperation(ctx)
	if err != nil || !found {
		return err
	}
	resolved, found, err := policy.ResolveIntegration(ctx, operation.Coordinate)
	if err != nil || !found {
		if err == nil {
			err = &forge.Error{Status: http.StatusNotFound, Code: "repository_not_found"}
		}
		_ = policy.failForgeOperation(ctx, operation, err)
		return err
	}
	if operation.Command == "migrate_to_walgit" {
		if err := policy.processForgeMigration(ctx, resolved, operation); err != nil {
			_ = policy.handleForgeMigrationFailure(ctx, operation, err)
			return err
		}
		return nil
	}
	if operation.Command == "ci_start" {
		if err := policy.processForgeCIStart(ctx, resolved, operation); err != nil {
			_ = policy.failForgeOperation(ctx, operation, err)
			return err
		}
		return nil
	}
	if operation.Provider == forge.BackendRelay && operation.Command == "merge" {
		return policy.processRelayMerge(ctx, resolved, operation)
	}
	if operation.Provider == forge.BackendRelay && operation.Command == "publish_review_ref" {
		return policy.processPublishReviewRef(ctx, resolved, operation)
	}
	result := operation.ProviderResult
	if operation.Command == "initial_import" && result != nil {
		return policy.finishForgeOperation(ctx, operation)
	}
	if operation.Command == "initial_import" && result == nil {
		importer, ok := resolved.Collaboration.(forge.CollaborationImporter)
		if !ok {
			err = &forge.Error{Status: http.StatusConflict, Code: "provider_import_unsupported"}
			_ = policy.failForgeOperation(ctx, operation, err)
			return err
		}
		imported, importErr := importer.InitialImport(ctx, resolved.Repository, forge.ProviderImportRequest{
			UpdatedSince: operation.CreatedAt.Add(-90 * 24 * time.Hour), OpenOnly: true,
			MaximumRoots: 1000, MaximumComments: 500,
		})
		if importErr != nil {
			_ = policy.failForgeOperation(ctx, operation, importErr)
			return importErr
		}
		for _, event := range imported.Events {
			digest := sha256.Sum256(event.Payload)
			if _, err := policy.storeGitHubWebhook(ctx, event, hex.EncodeToString(digest[:])); err != nil {
				_ = policy.failForgeOperation(ctx, operation, err)
				return err
			}
		}
		providerResult := forge.CollaborationResult{
			State: "queued", ImportedRoots: imported.Roots, ImportedComments: imported.Comments,
			Truncated: imported.Truncated, Incomplete: imported.Incomplete,
		}
		if err := policy.storeForgeProviderResult(ctx, operation, providerResult); err != nil {
			return err
		}
		operation.ProviderResult = &providerResult
		return policy.finishForgeOperation(ctx, operation)
	}
	if result == nil {
		payload, prepareErr := policy.prepareForgeCommand(ctx, operation)
		if prepareErr != nil {
			_ = policy.failForgeOperation(ctx, operation, prepareErr)
			return prepareErr
		}
		var providerResult forge.CollaborationResult
		var executeErr error
		if resolved.Collaboration != nil && resolved.Collaboration.Name() == operation.Provider {
			providerResult, executeErr = resolved.Collaboration.Execute(ctx, forge.CollaborationCommand{
				OperationID: operation.ID, Kind: operation.Command, Actor: operation.Actor,
				CredentialMode: operation.CredentialMode,
				Repository:     resolved.Repository, RootEventID: operation.RootEventID,
				ExpectedHead: operation.ExpectedHead, CorrelationMarker: operation.CorrelationMarker,
				Payload: payload,
			})
		} else if resolved.Mirror != nil && resolved.Mirror.Name() == operation.Provider && operation.SourceEventID != "" {
			source, found, sourceErr := policy.eventByHexID(ctx, operation.SourceEventID)
			if sourceErr != nil || !found {
				executeErr = &forge.Error{Status: http.StatusConflict, Code: "source_event_not_found"}
			} else {
				providerResult, executeErr = resolved.Mirror.Project(ctx, forge.MirrorProjection{
					OperationID: operation.ID, Kind: operation.Command, Repository: resolved.Repository,
					CredentialMode: operation.CredentialMode,
					Event:          source, RootEventID: operation.RootEventID, ExpectedHead: operation.ExpectedHead,
					CorrelationMarker: operation.CorrelationMarker, Payload: payload,
				})
			}
		} else {
			executeErr = &forge.Error{Status: http.StatusConflict, Code: "provider_backend_changed"}
		}
		if executeErr != nil {
			_ = policy.failForgeOperation(ctx, operation, executeErr)
			return executeErr
		}
		if err := policy.storeForgeProviderResult(ctx, operation, providerResult); err != nil {
			return err
		}
		result = &providerResult
		operation.ProviderResult = result
	}
	if err := policy.projectForgeOperation(ctx, resolved.Repository, operation, *result); err != nil {
		_ = policy.failForgeOperation(ctx, operation, err)
		return err
	}
	return policy.finishForgeOperation(ctx, operation)
}

func (policy *GitForgePolicy) projectForgeOperation(ctx context.Context, repository forge.Repository, operation forgeOperation, result forge.CollaborationResult) error {
	if operation.SourceEventID != "" {
		source, found, err := policy.eventByHexID(ctx, operation.SourceEventID)
		if err != nil || !found {
			return &forge.Error{Status: http.StatusConflict, Code: "source_event_not_found"}
		}
		switch operation.Command {
		case "create_issue", "create_pull_request":
			return policy.saveForgeMapping(ctx, forgeObjectMapping{
				Provider: operation.Provider, ConnectionID: operation.ConnectionID,
				Coordinate: operation.Coordinate, ObjectType: operation.ObjectType,
				NostrEventID: source.ID.Hex(), NostrRootID: source.ID.Hex(),
				ExternalID: result.ExternalID, ExternalNumber: result.ExternalNumber,
				Origin: "relay", Authority: "relay", ExternalURL: result.ExternalURL,
				CorrelationMarker: operation.CorrelationMarker,
			})
		case "comment", "review", "review_comment":
			objectType := "comment"
			if operation.Command == "review" {
				objectType = "review"
			}
			return policy.saveForgeMapping(ctx, forgeObjectMapping{
				Provider: operation.Provider, ConnectionID: operation.ConnectionID,
				Coordinate: operation.Coordinate, ObjectType: objectType,
				NostrEventID: source.ID.Hex(), NostrRootID: operation.RootEventID,
				ExternalID: result.ExternalID, ExternalNumber: result.ExternalNumber,
				Origin: "relay", Authority: "relay", ExternalURL: result.ExternalURL,
				CorrelationMarker: operation.CorrelationMarker,
			})
		case "reaction_add":
			return policy.saveForgeMapping(ctx, forgeObjectMapping{
				Provider: operation.Provider, ConnectionID: operation.ConnectionID,
				Coordinate: operation.Coordinate, ObjectType: "reaction",
				NostrEventID: source.ID.Hex(), NostrRootID: operation.RootEventID,
				ExternalID: result.ExternalID, Origin: "relay", Authority: "relay",
			})
		case "reaction_delete":
			return nil
		case "state":
			return nil
		}
	}
	connection, found, err := policy.LoadGitHubConnection(ctx, operation.ConnectionID)
	if err != nil || !found {
		if err == nil {
			err = &forge.Error{Status: http.StatusServiceUnavailable, Code: "github_connection_unavailable", Retryable: true}
		}
		return err
	}
	secret, err := policy.bridgeSecret(connection)
	if err != nil {
		return err
	}
	var payload map[string]any
	if err := json.Unmarshal(operation.Payload, &payload); err != nil {
		return &forge.Error{Status: http.StatusBadRequest, Code: "invalid_forge_command"}
	}
	createdAt := nostr.Timestamp(operation.CreatedAt.Unix())
	provenance := externalProvenanceTags(result, payload)
	var event nostr.Event
	mapping := forgeObjectMapping{
		Provider: operation.Provider, ConnectionID: operation.ConnectionID,
		Coordinate: operation.Coordinate, ObjectType: operation.ObjectType,
		ExternalID: result.ExternalID, ExternalNumber: result.ExternalNumber,
		Origin: "provider", Authority: "provider", ExternalURL: result.ExternalURL,
		CorrelationMarker: operation.CorrelationMarker,
	}
	switch operation.Command {
	case "create_issue":
		event = nostr.Event{CreatedAt: createdAt, Kind: nostr.KindIssue, Content: stringValue(payload["body"]), Tags: nostr.Tags{
			{"a", operation.Coordinate.String()}, {"p", operation.Coordinate.Owner.Hex()},
			{"subject", stringValue(payload["title"])},
		}}
		event.Tags = append(event.Tags, provenance...)
	case "create_pull_request":
		headSHA := strings.ToLower(result.HeadSHA)
		if !fullForgeObjectID(headSHA) {
			headSHA = strings.ToLower(stringValue(payload["head_sha"]))
		}
		if !fullForgeObjectID(headSHA) {
			return &forge.Error{Status: http.StatusBadGateway, Code: "github_pr_head_missing", Retryable: true}
		}
		clone, err := policy.Registry.Resolve(ctx, repository)
		if err != nil || !clone.Capabilities.Clone.Available {
			return &forge.Error{Status: http.StatusConflict, Code: "clone_unavailable"}
		}
		event = nostr.Event{CreatedAt: createdAt, Kind: projecttask.KindPullRequest, Content: stringValue(payload["body"]), Tags: nostr.Tags{
			{"a", operation.Coordinate.String()}, {"p", operation.Coordinate.Owner.Hex()},
			{"subject", stringValue(payload["title"])}, {"c", headSHA},
			{"clone", clone.Capabilities.Clone.URL},
		}}
		if baseSHA := strings.ToLower(result.BaseSHA); fullForgeObjectID(baseSHA) {
			event.Tags = append(event.Tags, nostr.Tag{"base-commit", baseSHA})
		}
		if ref := firstNonempty(result.HeadRef, stringValue(payload["head"])); ref != "" {
			event.Tags = append(event.Tags, nostr.Tag{"branch-name", ref})
		}
		if ref := firstNonempty(result.BaseRef, stringValue(payload["base"])); ref != "" {
			event.Tags = append(event.Tags, nostr.Tag{"target-branch", ref})
		}
		if boolValue(payload["draft"]) {
			event.Tags = append(event.Tags, nostr.Tag{"t", "draft"})
		}
		event.Tags = append(event.Tags, provenance...)
	case "comment":
		root, found, err := policy.eventByHexID(ctx, operation.RootEventID)
		if err != nil || !found {
			return &forge.Error{Status: http.StatusConflict, Code: "work_item_not_found"}
		}
		event = nostr.Event{CreatedAt: createdAt, Kind: nostr.KindTextNote, Content: stringValue(payload["body"]), Tags: nostr.Tags{
			{"e", root.ID.Hex(), "", "root"}, {"a", operation.Coordinate.String()}, {"p", root.PubKey.Hex()},
		}}
		event.Tags = append(event.Tags, provenance...)
		mapping.NostrRootID = root.ID.Hex()
	case "state", "merge":
		root, found, err := policy.eventByHexID(ctx, operation.RootEventID)
		if err != nil || !found {
			return &forge.Error{Status: http.StatusConflict, Code: "work_item_not_found"}
		}
		rootMapping, found, err := policy.loadMappingByNostrRoot(ctx, operation.Provider, operation.ConnectionID, root.ID.Hex())
		if err != nil || !found {
			return &forge.Error{Status: http.StatusConflict, Code: "forge_mapping_missing"}
		}
		state := stringValue(payload["state"])
		if operation.Command == "merge" {
			state = "merged"
		}
		provenanceResult := result
		provenanceResult.ExternalID = rootMapping.ExternalID
		provenanceResult.ExternalNumber = rootMapping.ExternalNumber
		provenanceResult.ExternalURL = rootMapping.ExternalURL
		if rootMapping.Authority == "provider" && root.PubKey == secret.Public() {
			kind := forgeStatusKind(state)
			if kind == 0 {
				return &forge.Error{Status: http.StatusBadRequest, Code: "invalid_forge_state"}
			}
			event = nostr.Event{CreatedAt: createdAt, Kind: kind, Tags: nostr.Tags{
				{"e", root.ID.Hex(), "", "root"}, {"a", operation.Coordinate.String()},
			}}
			if operation.Command == "merge" && fullForgeObjectID(result.MergedCommit) {
				event.Tags = append(event.Tags, nostr.Tag{"merge-commit", strings.ToLower(result.MergedCommit)}, nostr.Tag{"r", strings.ToLower(result.MergedCommit)})
			}
		} else {
			event = nostr.Event{CreatedAt: createdAt, Kind: nostr.KindTextNote,
				Content: "GitHub state changed to " + state,
				Tags:    nostr.Tags{{"e", root.ID.Hex(), "", "root"}, {"a", operation.Coordinate.String()}, {"t", "external-state"}},
			}
		}
		event.Tags = append(event.Tags, externalProvenanceTags(provenanceResult, payload)...)
		mapping = forgeObjectMapping{}
	default:
		return &forge.Error{Status: http.StatusBadRequest, Code: "unsupported_forge_command"}
	}
	if err := event.Sign(secret); err != nil {
		return &forge.Error{Status: http.StatusServiceUnavailable, Code: "bridge_signing_failed", Retryable: true}
	}
	if err := policy.persistBridgeEvent(ctx, event); err != nil {
		return err
	}
	if mapping.Provider != "" {
		mapping.NostrEventID = event.ID.Hex()
		if mapping.NostrRootID == "" {
			mapping.NostrRootID = event.ID.Hex()
		}
		return policy.saveForgeMapping(ctx, mapping)
	}
	return nil
}

func (policy *GitForgePolicy) prepareForgeCommand(ctx context.Context, operation forgeOperation) (json.RawMessage, error) {
	if operation.RootEventID == "" || operation.Command == "create_issue" || operation.Command == "create_pull_request" {
		return operation.Payload, nil
	}
	if operation.Command == "reaction_add" || operation.Command == "reaction_delete" {
		var payload map[string]any
		if err := json.Unmarshal(operation.Payload, &payload); err != nil {
			return nil, &forge.Error{Status: http.StatusBadRequest, Code: "invalid_forge_command"}
		}
		targetID := stringValue(payload["target_event_id"])
		mapping, found, err := policy.loadCommentMappingByNostrEvent(ctx, operation.Provider, operation.ConnectionID, targetID)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, &forge.Error{Status: http.StatusConflict, Code: "forge_mapping_pending", Retryable: true}
		}
		target, found, err := policy.eventByHexID(ctx, targetID)
		if err != nil || !found {
			return nil, &forge.Error{Status: http.StatusConflict, Code: "source_event_not_found"}
		}
		if target.Tags.Find("external-decision") != nil {
			return nil, &forge.Error{Status: http.StatusBadRequest, Code: "unsupported_github_comment_type"}
		}
		commentType := "issue_comment"
		if (forgeHasTagValue(target, "t", "external-review") && target.Tags.Find("path") != nil) ||
			(forgeHasTagValue(target, "t", "inline-comment") && target.Tags.Find("file") != nil) {
			commentType = "pull_request_review_comment"
		}
		payload["external_comment_id"] = mapping.ExternalID
		payload["comment_type"] = commentType
		delete(payload, "target_event_id")
		if operation.Command == "reaction_delete" {
			reactionID := stringValue(payload["reaction_event_id"])
			reactionMapping, found, err := policy.loadMappingByNostrEvent(ctx, operation.Provider, operation.ConnectionID, reactionID, "reaction")
			if err != nil {
				return nil, err
			}
			if !found {
				return nil, &forge.Error{Status: http.StatusConflict, Code: "forge_mapping_pending", Retryable: true}
			}
			payload["external_reaction_id"] = reactionMapping.ExternalID
			delete(payload, "reaction_event_id")
		}
		return json.Marshal(payload)
	}
	mapping, found, err := policy.loadMappingByNostrRoot(ctx, operation.Provider, operation.ConnectionID, operation.RootEventID)
	if err != nil {
		return nil, err
	}
	if !found || mapping.ExternalNumber <= 0 {
		return nil, &forge.Error{Status: http.StatusConflict, Code: "forge_mapping_pending", Retryable: true}
	}
	var payload map[string]any
	if err := json.Unmarshal(operation.Payload, &payload); err != nil {
		return nil, &forge.Error{Status: http.StatusBadRequest, Code: "invalid_forge_command"}
	}
	payload["external_number"] = mapping.ExternalNumber
	payload["object_type"] = mapping.ObjectType
	return json.Marshal(payload)
}

func (policy *GitForgePolicy) bridgeSecret(connection githubforge.Connection) (nostr.SecretKey, error) {
	if policy.Credentials == nil {
		return nostr.SecretKey{}, &forge.Error{Status: http.StatusServiceUnavailable, Code: "bridge_credential_unavailable", Retryable: true}
	}
	raw, err := policy.Credentials.Load(connection.BridgeCredentialRef)
	if err != nil {
		return nostr.SecretKey{}, &forge.Error{Status: http.StatusServiceUnavailable, Code: "bridge_credential_unavailable", Retryable: true}
	}
	secret, err := nostr.SecretKeyFromHex(strings.TrimSpace(string(raw)))
	if err != nil || secret.Public().Hex() != connection.BridgePubkey {
		return nostr.SecretKey{}, &forge.Error{Status: http.StatusServiceUnavailable, Code: "bridge_credential_mismatch", Retryable: true}
	}
	return secret, nil
}

func (policy *GitForgePolicy) persistBridgeEvent(ctx context.Context, event nostr.Event) error {
	if !event.CheckID() || !event.VerifySignature() {
		return &forge.Error{Status: http.StatusInternalServerError, Code: "invalid_bridge_event"}
	}
	recognized, err := projecttask.ValidateEvent(event)
	if err == nil && !recognized && event.Kind == nostr.KindDeletion {
		scope, scopeErr := policy.ProjectTasks.Resolver.Resolve(ctx, event)
		err = scopeErr
		recognized = scope.Relevant
	}
	if err != nil || !recognized {
		return &forge.Error{Status: http.StatusInternalServerError, Code: "invalid_bridge_event"}
	}
	if policy.PersistEvent != nil {
		return policy.PersistEvent(ctx, event)
	}
	if err := policy.Events.SaveEvent(event); err != nil && err != eventstore.ErrDupEvent {
		return err
	}
	if err := policy.ProjectTasks.Remember(ctx, event); err != nil {
		return err
	}
	return nil
}

func externalProvenanceTags(result forge.CollaborationResult, payload map[string]any) nostr.Tags {
	tags := nostr.Tags{{"external-provider", forge.BackendGitHub}}
	if result.ExternalID != "" {
		tags = append(tags, nostr.Tag{"external-id", result.ExternalID})
	}
	if result.ExternalNumber > 0 {
		tags = append(tags, nostr.Tag{"external-number", fmt.Sprintf("%d", result.ExternalNumber)})
	}
	if result.ExternalURL != "" {
		tags = append(tags, nostr.Tag{"external-url", result.ExternalURL})
	}
	for _, entry := range []struct{ tag, value, key string }{
		{"external-author", result.ExternalAuthor, "external_author"},
		{"external-author-id", result.ExternalAuthorID, "external_author_id"},
	} {
		if value := firstNonempty(entry.value, stringValue(payload[entry.key])); value != "" {
			tags = append(tags, nostr.Tag{entry.tag, value})
		}
	}
	return tags
}

func (policy *GitForgePolicy) eventByHexID(ctx context.Context, raw string) (nostr.Event, bool, error) {
	id, err := nostr.IDFromHex(raw)
	if err != nil {
		return nostr.Event{}, false, err
	}
	return policy.ProjectTasks.EventByID(ctx, id)
}

func forgeStatusKind(state string) nostr.Kind {
	switch strings.ToLower(state) {
	case "open", "reopened", "ready":
		return nostr.KindStatusOpen
	case "merged", "completed":
		return nostr.KindStatusApplied
	case "closed", "not_planned":
		return nostr.KindStatusClosed
	case "draft":
		return nostr.KindStatusDraft
	default:
		return 0
	}
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func boolValue(value any) bool {
	result, _ := value.(bool)
	return result
}

func firstNonempty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func fullForgeObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') || (character >= 'A' && character <= 'F')) {
			return false
		}
	}
	return true
}

func validForgeTitle(value string) bool {
	return value != "" && utf8.ValidString(value) && len(value) <= 256
}
