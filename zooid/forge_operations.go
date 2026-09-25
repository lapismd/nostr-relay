package zooid

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fiatjaf.com/nostr"
	"fmt"
	"math"
	"net/http"
	"time"
	"zooid/internal/forge"
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
