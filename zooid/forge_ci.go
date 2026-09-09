package zooid

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"zooid/internal/forge"
	"zooid/internal/projecttask"
)

type forgeCIRun struct {
	OperationID       string
	Provider          string
	ConnectionID      string
	Coordinate        projecttask.RepositoryCoordinate
	PullRequestEvent  string
	Commit            string
	Workflow          string
	Inputs            map[string]string
	ProviderRunID     string
	ProviderURL       string
	MirrorRef         string
	Status            string
	CleanupState      string
	Failure           string
	ActivityEventID   string
	ProviderUpdatedAt time.Time
	UpdatedAt         time.Time
}

func (policy *GitForgePolicy) enqueueForgeCI(w http.ResponseWriter, r *http.Request, actor nostr.PubKey, idempotencyKey string, resolved forge.ResolvedRepository, body []byte) {
	if status, code := policy.authorizeRepository(actor, resolved.Repository, true); status != 0 {
		writeGitError(w, status, code)
		return
	}
	if resolved.CI == nil || resolved.Repository.Integration.CIBackend != forge.BackendGitHub || !validGitHubWorkflow(resolved.Repository.Integration.CIWorkflow) {
		writeGitError(w, http.StatusConflict, "ci_not_configured")
		return
	}
	var request struct {
		Commit             string            `json:"commit"`
		Inputs             map[string]string `json:"inputs"`
		PullRequestEventID string            `json:"pull_request_event_id"`
	}
	if !decodeStrictJSON(body, &request) || !fullForgeObjectID(request.Commit) || !validCIInputs(request.Inputs) {
		writeGitError(w, http.StatusBadRequest, "invalid_ci_request")
		return
	}
	request.Commit = strings.ToLower(request.Commit)
	resolvedCommit, err := resolved.Content.Resolve(r.Context(), resolved.Repository, forge.RevisionRequest{Revision: request.Commit, Principal: actor.Hex()})
	if err != nil || !strings.EqualFold(resolvedCommit.SHA, request.Commit) {
		writeGitError(w, http.StatusConflict, "ci_commit_not_found")
		return
	}
	if request.PullRequestEventID != "" {
		root, found, err := policy.eventByHexID(r.Context(), request.PullRequestEventID)
		if err != nil || !found || root.Kind != projecttask.KindPullRequest {
			writeGitError(w, http.StatusBadRequest, "invalid_ci_pull_request")
			return
		}
		coordinate, err := repositoryCoordinateFromEvent(root)
		if err != nil || coordinate != resolved.Repository.Coordinate {
			writeGitError(w, http.StatusBadRequest, "invalid_ci_pull_request")
			return
		}
		if status, code := policy.authorizeWorkItem(r.Context(), actor, root, resolved.Repository, true); status != 0 {
			writeGitError(w, status, code)
			return
		}
	}
	payload, _ := json.Marshal(map[string]any{
		"workflow": resolved.Repository.Integration.CIWorkflow,
		"inputs":   request.Inputs, "pull_request_event_id": request.PullRequestEventID,
	})
	operationID, err := newOperationID("ci")
	if err != nil {
		writeGitError(w, http.StatusInternalServerError, "operation_id_failed")
		return
	}
	operation, err := policy.enqueueForgeOperation(r.Context(), forgeOperation{
		ID: operationID, Provider: forge.BackendGitHub,
		ConnectionID: resolved.Repository.Integration.CIConnectionID,
		Coordinate:   resolved.Repository.Coordinate, Command: "ci_start", ObjectType: "ci",
		Actor: actor, RootEventID: request.PullRequestEventID, ExpectedHead: request.Commit,
		Payload: payload, IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		writeGitBackendFailure(w, err)
		return
	}
	writeForgeJSON(w, http.StatusAccepted, publicForgeOperation(operation))
}

func validCIInputs(inputs map[string]string) bool {
	if len(inputs) > 20 {
		return false
	}
	for key, value := range inputs {
		if key == "" || len(key) > 100 || len(value) > 1024 || strings.ContainsAny(key+value, "\x00\r\n") {
			return false
		}
	}
	return true
}

func (policy *GitForgePolicy) processForgeCIStart(ctx context.Context, resolved forge.ResolvedRepository, operation forgeOperation) error {
	if resolved.CI == nil || resolved.Repository.Integration.CIBackend != forge.BackendGitHub {
		return &forge.Error{Status: http.StatusConflict, Code: "ci_backend_changed"}
	}
	var payload struct {
		Workflow           string            `json:"workflow"`
		Inputs             map[string]string `json:"inputs"`
		PullRequestEventID string            `json:"pull_request_event_id"`
	}
	if err := json.Unmarshal(operation.Payload, &payload); err != nil || !validGitHubWorkflow(payload.Workflow) || !validCIInputs(payload.Inputs) {
		return &forge.Error{Status: http.StatusBadRequest, Code: "invalid_ci_request"}
	}
	mirrorRef := "refs/heads/lapis-ci/" + operation.ID
	if err := policy.saveForgeCIRun(ctx, forgeCIRun{
		OperationID: operation.ID, Provider: forge.BackendGitHub,
		ConnectionID: resolved.Repository.Integration.CIConnectionID,
		Coordinate:   operation.Coordinate, PullRequestEvent: payload.PullRequestEventID,
		Commit: operation.ExpectedHead, Workflow: payload.Workflow, Inputs: payload.Inputs,
		MirrorRef: mirrorRef, Status: "preparing", CleanupState: "pending",
	}); err != nil {
		return err
	}
	if resolved.Repository.Integration.GitAuthorityBackend == forge.BackendWalGit || resolved.Repository.Integration.GitBackend == forge.BackendWalGit {
		source, ok := resolved.Content.(forge.CommitMirrorSource)
		if !ok {
			return &forge.Error{Status: http.StatusConflict, Code: "ci_mirror_unavailable"}
		}
		targetBackend, ok := resolved.CI.(forge.CIMirrorTargetBackend)
		if !ok {
			return &forge.Error{Status: http.StatusConflict, Code: "ci_mirror_target_unavailable"}
		}
		target, err := targetBackend.MirrorTarget(ctx, resolved.Repository)
		if err != nil {
			return err
		}
		if err := source.MirrorCommit(ctx, resolved.Repository, operation.ExpectedHead, target, mirrorRef); err != nil {
			target.Password = ""
			return err
		}
		target.Password = ""
		if err := policy.saveForgeMirrorRef(ctx, operation, resolved.Repository, mirrorRef); err != nil {
			return err
		}
	}
	run, err := resolved.CI.Start(ctx, forge.CIRequest{
		OperationID: operation.ID, Repository: resolved.Repository, Commit: operation.ExpectedHead,
		Workflow: payload.Workflow, Inputs: payload.Inputs, MirrorRef: mirrorRef,
	})
	if err != nil {
		return err
	}
	if !strings.EqualFold(run.Commit, operation.ExpectedHead) {
		return &forge.Error{Status: http.StatusBadGateway, Code: "github_ci_head_mismatch"}
	}
	if run.MirrorRef == "" {
		run.MirrorRef = mirrorRef
	}
	if err := policy.saveForgeCIRun(ctx, forgeCIRun{
		OperationID: operation.ID, Provider: forge.BackendGitHub,
		ConnectionID: resolved.Repository.Integration.CIConnectionID,
		Coordinate:   operation.Coordinate, PullRequestEvent: payload.PullRequestEventID,
		Commit: operation.ExpectedHead, Workflow: payload.Workflow, Inputs: payload.Inputs,
		ProviderRunID: run.ProviderRunID, ProviderURL: run.URL, MirrorRef: run.MirrorRef,
		Status: run.Status, CleanupState: "pending", ProviderUpdatedAt: run.UpdatedAt,
	}); err != nil {
		return err
	}
	result := forge.CollaborationResult{ExternalID: run.ProviderRunID, ExternalURL: run.URL, State: run.Status, HeadSHA: run.Commit}
	if err := policy.storeForgeProviderResult(ctx, operation, result); err != nil {
		return err
	}
	return policy.finishForgeOperation(ctx, operation)
}

func (policy *GitForgePolicy) saveForgeCIRun(ctx context.Context, run forgeCIRun) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	inputs, _ := json.Marshal(run.Inputs)
	_, err := GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
		INSERT INTO {{.Name}}__forge_ci_runs
		  (operation_id, provider, connection_id, owner, repo, pull_request_event_id,
		   commit_oid, workflow, inputs, provider_run_id, provider_url, mirror_ref,
		   status, cleanup_state, provider_updated_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,NULLIF($15, '0001-01-01'::timestamptz),NOW())
		ON CONFLICT (operation_id) DO UPDATE SET
		  provider_run_id = CASE WHEN EXCLUDED.provider_run_id = '' THEN {{.Name}}__forge_ci_runs.provider_run_id ELSE EXCLUDED.provider_run_id END,
		  provider_url = CASE WHEN EXCLUDED.provider_url = '' THEN {{.Name}}__forge_ci_runs.provider_url ELSE EXCLUDED.provider_url END,
		  mirror_ref = EXCLUDED.mirror_ref, status = EXCLUDED.status,
		  provider_updated_at = COALESCE(EXCLUDED.provider_updated_at, {{.Name}}__forge_ci_runs.provider_updated_at),
		  cleanup_state = EXCLUDED.cleanup_state, sanitized_failure = '', updated_at = NOW()`),
		run.OperationID, run.Provider, run.ConnectionID, run.Coordinate.Owner.Hex(), run.Coordinate.ID,
		run.PullRequestEvent, run.Commit, run.Workflow, inputs, run.ProviderRunID,
		run.ProviderURL, run.MirrorRef, run.Status, run.CleanupState, run.ProviderUpdatedAt)
	return err
}

func (policy *GitForgePolicy) saveForgeMirrorRef(ctx context.Context, operation forgeOperation, repository forge.Repository, mirrorRef string) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	_, err := GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
		INSERT INTO {{.Name}}__forge_mirror_refs
		  (owner, repo, provider, connection_id, external_repository_id,
		   source_oid, mirror_ref, provider_oid, status, cleanup_state, updated_at)
		VALUES ($1,$2,'github',$3,$4,$5,$6,$5,'ready','pending',NOW())
		ON CONFLICT (owner, repo, provider, connection_id, mirror_ref) DO UPDATE SET
		  source_oid = EXCLUDED.source_oid, provider_oid = EXCLUDED.provider_oid,
		  status = 'ready', cleanup_state = 'pending', updated_at = NOW()`),
		operation.Coordinate.Owner.Hex(), operation.Coordinate.ID,
		repository.Integration.CIConnectionID, repository.Integration.CIExternalRepositoryID,
		operation.ExpectedHead, mirrorRef)
	return err
}

func (policy *GitForgePolicy) loadActiveForgeCIRun(ctx context.Context) (forgeCIRun, bool, error) {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	var run forgeCIRun
	var ownerHex string
	var inputs []byte
	err := GetDb().QueryRowContext(queryCtx, policy.Events.Schema.Render(`
		SELECT operation_id, provider, connection_id, owner, repo, pull_request_event_id,
		       commit_oid, workflow, inputs, provider_run_id, provider_url, mirror_ref,
		       status, cleanup_state, sanitized_failure, activity_event_id,
		       COALESCE(provider_updated_at, updated_at), updated_at
		FROM {{.Name}}__forge_ci_runs
		WHERE status NOT IN ('success','failure','cancelled','timed_out','action_required','neutral','skipped','stale','error')
		   OR cleanup_state <> 'complete' OR activity_event_id = ''
		ORDER BY updated_at, operation_id LIMIT 1`)).Scan(
		&run.OperationID, &run.Provider, &run.ConnectionID, &ownerHex, &run.Coordinate.ID,
		&run.PullRequestEvent, &run.Commit, &run.Workflow, &inputs, &run.ProviderRunID,
		&run.ProviderURL, &run.MirrorRef, &run.Status, &run.CleanupState, &run.Failure,
		&run.ActivityEventID, &run.ProviderUpdatedAt, &run.UpdatedAt)
	if err == sql.ErrNoRows {
		return forgeCIRun{}, false, nil
	}
	if err != nil {
		return forgeCIRun{}, false, err
	}
	var parseErr error
	run.Coordinate.Owner, parseErr = nostr.PubKeyFromHex(ownerHex)
	if parseErr != nil {
		return forgeCIRun{}, false, parseErr
	}
	if err := json.Unmarshal(inputs, &run.Inputs); err != nil {
		return forgeCIRun{}, false, err
	}
	return run, true, nil
}

func (policy *GitForgePolicy) loadForgeCIRun(ctx context.Context, operationID string) (forgeCIRun, bool, error) {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	var run forgeCIRun
	var ownerHex string
	var inputs []byte
	err := GetDb().QueryRowContext(queryCtx, policy.Events.Schema.Render(`
		SELECT operation_id, provider, connection_id, owner, repo, pull_request_event_id,
		       commit_oid, workflow, inputs, provider_run_id, provider_url, mirror_ref,
		       status, cleanup_state, sanitized_failure, activity_event_id,
		       COALESCE(provider_updated_at, updated_at), updated_at
		FROM {{.Name}}__forge_ci_runs WHERE operation_id = $1`), operationID).Scan(
		&run.OperationID, &run.Provider, &run.ConnectionID, &ownerHex, &run.Coordinate.ID,
		&run.PullRequestEvent, &run.Commit, &run.Workflow, &inputs, &run.ProviderRunID,
		&run.ProviderURL, &run.MirrorRef, &run.Status, &run.CleanupState, &run.Failure,
		&run.ActivityEventID, &run.ProviderUpdatedAt, &run.UpdatedAt)
	if err == sql.ErrNoRows {
		return forgeCIRun{}, false, nil
	}
	if err != nil {
		return forgeCIRun{}, false, err
	}
	run.Coordinate.Owner, err = nostr.PubKeyFromHex(ownerHex)
	if err != nil {
		return forgeCIRun{}, false, err
	}
	if err := json.Unmarshal(inputs, &run.Inputs); err != nil {
		return forgeCIRun{}, false, err
	}
	return run, true, nil
}

func (policy *GitForgePolicy) cancelForgeCI(ctx context.Context, operation forgeOperation) error {
	run, found, err := policy.loadForgeCIRun(ctx, operation.ID)
	if err != nil {
		return err
	}
	if !found {
		result := forge.CollaborationResult{State: "cancelled"}
		if err := policy.storeForgeProviderResult(ctx, operation, result); err != nil {
			return err
		}
		return policy.finishForgeOperation(ctx, operation)
	}
	if terminalCIStatus(run.Status) || run.ProviderRunID == "" {
		return &forge.Error{Status: http.StatusConflict, Code: "ci_run_not_cancellable"}
	}
	resolved, found, err := policy.ResolveIntegration(ctx, run.Coordinate)
	if err != nil || !found || resolved.CI == nil {
		if err == nil {
			err = &forge.Error{Status: http.StatusConflict, Code: "ci_backend_changed"}
		}
		return err
	}
	if err := resolved.CI.Cancel(ctx, resolved.Repository, run.ProviderRunID); err != nil {
		return err
	}
	run.Status = "cancel_requested"
	return policy.updateForgeCIRun(ctx, run)
}

func (policy *GitForgePolicy) ProcessForgeCIOnce(ctx context.Context) error {
	record, found, err := policy.loadActiveForgeCIRun(ctx)
	if err != nil || !found {
		return err
	}
	resolved, found, err := policy.ResolveIntegration(ctx, record.Coordinate)
	if err != nil || !found || resolved.CI == nil {
		if err == nil {
			err = &forge.Error{Status: http.StatusConflict, Code: "ci_backend_changed"}
		}
		return policy.failForgeCIRun(ctx, record, err)
	}
	if terminalCIStatus(record.Status) {
		return policy.finishForgeCIRun(ctx, resolved, record, record.ProviderUpdatedAt)
	}
	var run forge.CIRun
	if record.ProviderRunID == "" {
		discovery, ok := resolved.CI.(forge.CIDiscoveryBackend)
		if !ok {
			return policy.failForgeCIRun(ctx, record, &forge.Error{Status: http.StatusConflict, Code: "ci_discovery_unavailable"})
		}
		run, err = discovery.Discover(ctx, forge.CIRequest{
			OperationID: record.OperationID, Repository: resolved.Repository, Commit: record.Commit,
			Workflow: record.Workflow, Inputs: record.Inputs, MirrorRef: record.MirrorRef,
		})
	} else {
		run, err = resolved.CI.Status(ctx, resolved.Repository, record.ProviderRunID)
	}
	if err != nil {
		return policy.failForgeCIRun(ctx, record, err)
	}
	if !strings.EqualFold(run.Commit, record.Commit) {
		return policy.failForgeCIRun(ctx, record, &forge.Error{Status: http.StatusBadGateway, Code: "github_ci_head_mismatch"})
	}
	if run.ProviderRunID == "" {
		run.ProviderRunID = record.ProviderRunID
	}
	if run.URL == "" {
		run.URL = record.ProviderURL
	}
	if run.MirrorRef == "" {
		run.MirrorRef = record.MirrorRef
	}
	record.ProviderRunID, record.ProviderURL, record.Status = run.ProviderRunID, run.URL, run.Status
	record.ProviderUpdatedAt = run.UpdatedAt
	if err := policy.updateForgeCIRun(ctx, record); err != nil {
		return err
	}
	if !terminalCIStatus(record.Status) {
		return nil
	}
	return policy.finishForgeCIRun(ctx, resolved, record, run.UpdatedAt)
}

func (policy *GitForgePolicy) updateForgeCIRun(ctx context.Context, run forgeCIRun) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	_, err := GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
		UPDATE {{.Name}}__forge_ci_runs SET provider_run_id = $2, provider_url = $3,
		  status = $4, sanitized_failure = $5,
		  provider_updated_at = COALESCE(NULLIF($6, '0001-01-01'::timestamptz), provider_updated_at),
		  updated_at = NOW() WHERE operation_id = $1`),
		run.OperationID, run.ProviderRunID, run.ProviderURL, run.Status,
		sanitizeGitErrorString(run.Failure), run.ProviderUpdatedAt)
	return err
}

func (policy *GitForgePolicy) failForgeCIRun(ctx context.Context, run forgeCIRun, runErr error) error {
	run.Failure = "ci_status_failed"
	if backendError, ok := runErr.(*forge.Error); ok && backendError.Code != "" {
		run.Failure = backendError.Code
		if !backendError.Retryable {
			run.Status = "error"
		}
	} else {
		run.Status = "error"
	}
	if err := policy.updateForgeCIRun(ctx, run); err != nil {
		return err
	}
	return runErr
}

func terminalCIStatus(status string) bool {
	switch strings.ToLower(status) {
	case "success", "failure", "cancelled", "timed_out", "action_required", "neutral", "skipped", "stale", "error":
		return true
	default:
		return false
	}
}

func (policy *GitForgePolicy) finishForgeCIRun(ctx context.Context, resolved forge.ResolvedRepository, run forgeCIRun, completedAt time.Time) error {
	cleanup, ok := resolved.CI.(forge.CICleanupBackend)
	if !ok {
		return &forge.Error{Status: http.StatusConflict, Code: "ci_cleanup_unavailable"}
	}
	if err := cleanup.Cleanup(ctx, resolved.Repository, run.MirrorRef); err != nil {
		return err
	}
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	_, err := GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
		UPDATE {{.Name}}__forge_ci_runs SET cleanup_state = 'complete', completed_at = NOW(), updated_at = NOW()
		WHERE operation_id = $1`), run.OperationID)
	cancel()
	if err != nil {
		return err
	}
	queryCtx, cancel = context.WithTimeout(ctx, dbOpTimeout)
	_, err = GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
		UPDATE {{.Name}}__forge_mirror_refs SET cleanup_state = 'complete', status = 'complete', updated_at = NOW()
		WHERE owner = $1 AND repo = $2 AND provider = 'github' AND connection_id = $3 AND mirror_ref = $4`),
		run.Coordinate.Owner.Hex(), run.Coordinate.ID, run.ConnectionID, run.MirrorRef)
	cancel()
	if err != nil {
		return err
	}
	if run.Status == "error" {
		queryCtx, cancel = context.WithTimeout(ctx, dbOpTimeout)
		defer cancel()
		_, err = GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
			UPDATE {{.Name}}__forge_ci_runs SET activity_event_id = 'none' WHERE operation_id = $1`), run.OperationID)
		return err
	}
	if completedAt.IsZero() {
		completedAt = run.UpdatedAt
	}
	if completedAt.IsZero() {
		completedAt = time.Now().UTC()
	}
	event, err := policy.publishForgeCIActivity(ctx, resolved.Repository, run, completedAt)
	if err != nil {
		return err
	}
	queryCtx, cancel = context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	_, err = GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
		UPDATE {{.Name}}__forge_ci_runs SET activity_event_id = $2 WHERE operation_id = $1`), run.OperationID, event.ID.Hex())
	return err
}

func (policy *GitForgePolicy) publishForgeCIActivity(ctx context.Context, repository forge.Repository, run forgeCIRun, at time.Time) (nostr.Event, error) {
	secret, err := policy.bridgeSecretForConnection(ctx, repository.Integration.CIConnectionID)
	if err != nil {
		return nostr.Event{}, err
	}
	tags := nostr.Tags{
		{"t", "ci"}, {"a", repository.Coordinate.String()}, {"c", strings.ToLower(run.Commit)},
		{"status", strings.ToLower(run.Status)}, {"external-provider", forge.BackendGitHub},
	}
	if run.PullRequestEvent != "" {
		tags = append(tags, nostr.Tag{"e", run.PullRequestEvent, "", "root"})
	}
	if run.ProviderRunID != "" {
		tags = append(tags, nostr.Tag{"external-run-id", run.ProviderRunID})
	}
	if run.ProviderURL != "" {
		tags = append(tags, nostr.Tag{"external-url", run.ProviderURL})
	}
	event := nostr.Event{Kind: nostr.KindTextNote, CreatedAt: nostr.Timestamp(at.Unix()), Content: "GitHub Actions " + strings.ToLower(run.Status), Tags: tags}
	if err := event.Sign(secret); err != nil {
		return nostr.Event{}, err
	}
	return event, policy.persistBridgeEvent(ctx, event)
}
