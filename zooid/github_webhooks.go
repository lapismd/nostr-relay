package zooid

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"zooid/internal/forge"
	"zooid/internal/githubforge"
	"zooid/internal/projecttask"
)

type githubWebhookDelivery struct {
	Provider             string
	DeliveryID           string
	ConnectionID         string
	InstallationID       int64
	EventType            string
	Action               string
	ExternalRepositoryID string
	ExternalObjectID     string
	Payload              json.RawMessage
	PayloadHash          string
	State                string
	Attempts             int
	ReceivedAt           time.Time
}

func (policy *GitForgePolicy) storeGitHubWebhook(ctx context.Context, event forge.ProviderEvent, payloadHash string) (bool, error) {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	result, err := GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
		INSERT INTO {{.Name}}__forge_webhook_deliveries
		  (provider, delivery_id, connection_id, installation_id, event_type,
		   action, external_repository_id, external_object_id, payload, payload_sha256,
		   processing_state)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 'pending')
		ON CONFLICT (provider, delivery_id) DO NOTHING`), event.Provider, event.DeliveryID,
		event.ConnectionID, event.InstallationID, event.EventType, event.Action,
		event.ExternalRepositoryID, event.ExternalObjectID, []byte(event.Payload), payloadHash)
	if err != nil {
		return false, err
	}
	rows, _ := result.RowsAffected()
	if rows == 1 {
		return true, nil
	}
	var existingHash string
	err = GetDb().QueryRowContext(queryCtx, policy.Events.Schema.Render(`
		SELECT payload_sha256 FROM {{.Name}}__forge_webhook_deliveries
		WHERE provider = $1 AND delivery_id = $2`), event.Provider, event.DeliveryID).Scan(&existingHash)
	if err != nil {
		return false, err
	}
	if existingHash != payloadHash {
		return false, &forge.Error{Status: http.StatusConflict, Code: "github_delivery_conflict"}
	}
	return false, nil
}

func (policy *GitForgePolicy) claimGitHubWebhook(ctx context.Context) (githubWebhookDelivery, bool, error) {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	tx, err := GetDb().BeginTx(queryCtx, &sql.TxOptions{})
	if err != nil {
		return githubWebhookDelivery{}, false, err
	}
	defer tx.Rollback()
	var delivery githubWebhookDelivery
	err = tx.QueryRowContext(queryCtx, policy.Events.Schema.Render(`
		SELECT provider, delivery_id, connection_id, installation_id, event_type,
		       action, external_repository_id, external_object_id, payload,
		       payload_sha256, processing_state, attempt_count, received_at
		FROM {{.Name}}__forge_webhook_deliveries
		WHERE (processing_state IN ('pending', 'retry') AND process_after <= NOW())
		   OR (processing_state = 'running' AND lease_until < NOW())
		ORDER BY process_after, received_at, delivery_id
		FOR UPDATE SKIP LOCKED LIMIT 1`)).Scan(
		&delivery.Provider, &delivery.DeliveryID, &delivery.ConnectionID,
		&delivery.InstallationID, &delivery.EventType, &delivery.Action,
		&delivery.ExternalRepositoryID, &delivery.ExternalObjectID, &delivery.Payload,
		&delivery.PayloadHash, &delivery.State, &delivery.Attempts, &delivery.ReceivedAt,
	)
	if err == sql.ErrNoRows {
		return githubWebhookDelivery{}, false, nil
	}
	if err != nil {
		return githubWebhookDelivery{}, false, err
	}
	_, err = tx.ExecContext(queryCtx, policy.Events.Schema.Render(`
		UPDATE {{.Name}}__forge_webhook_deliveries
		SET processing_state = 'running', lease_until = NOW() + INTERVAL '60 seconds'
		WHERE provider = $1 AND delivery_id = $2`), delivery.Provider, delivery.DeliveryID)
	if err != nil {
		return githubWebhookDelivery{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return githubWebhookDelivery{}, false, err
	}
	return delivery, true, nil
}

func (policy *GitForgePolicy) finishGitHubWebhook(ctx context.Context, delivery githubWebhookDelivery) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	_, err := GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
		UPDATE {{.Name}}__forge_webhook_deliveries
		SET processing_state = 'complete', lease_until = NULL, last_error = '', processed_at = NOW()
		WHERE provider = $1 AND delivery_id = $2`), delivery.Provider, delivery.DeliveryID)
	return err
}

func (policy *GitForgePolicy) failGitHubWebhook(ctx context.Context, delivery githubWebhookDelivery, processErr error) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	attempts := delivery.Attempts + 1
	state := "failed"
	next := time.Now()
	var backendError *forge.Error
	if (!errors.As(processErr, &backendError) || backendError.Retryable) && attempts < 12 {
		state = "retry"
		next = time.Now().Add(time.Duration(min(1<<min(attempts, 8), 300)) * time.Second)
		if backendError != nil && backendError.RetryAt.After(next) {
			next = backendError.RetryAt
		}
	}
	failure := "github_webhook_processing_failed"
	if backendError != nil && backendError.Code != "" {
		failure = backendError.Code
	}
	_, err := GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
		UPDATE {{.Name}}__forge_webhook_deliveries
		SET processing_state = $3, attempt_count = $4, process_after = $5,
		    lease_until = NULL, last_error = $6
		WHERE provider = $1 AND delivery_id = $2`), delivery.Provider, delivery.DeliveryID,
		state, attempts, next, sanitizeGitErrorString(failure))
	return err
}

func (policy *GitForgePolicy) ProcessGitHubWebhookOnce(ctx context.Context) error {
	delivery, found, err := policy.claimGitHubWebhook(ctx)
	if err != nil || !found {
		return err
	}
	if err := policy.processGitHubWebhook(ctx, delivery); err != nil {
		_ = policy.failGitHubWebhook(ctx, delivery, err)
		return err
	}
	return policy.finishGitHubWebhook(ctx, delivery)
}

type githubWebhookPayload struct {
	Action       string `json:"action"`
	Ref          string `json:"ref"`
	Before       string `json:"before"`
	After        string `json:"after"`
	Created      bool   `json:"created"`
	Deleted      bool   `json:"deleted"`
	Forced       bool   `json:"forced"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
	Repository          githubWebhookRepository   `json:"repository"`
	RepositoriesRemoved []githubWebhookRepository `json:"repositories_removed"`
	RepositoriesAdded   []githubWebhookRepository `json:"repositories_added"`
	Issue               githubWebhookIssue        `json:"issue"`
	PullRequest         githubWebhookPullRequest  `json:"pull_request"`
	Comment             githubWebhookComment      `json:"comment"`
	Review              githubWebhookReview       `json:"review"`
	WorkflowRun         githubWebhookWorkflowRun  `json:"workflow_run"`
}

type githubWebhookRepository struct {
	ID       int64  `json:"id"`
	FullName string `json:"full_name"`
	CloneURL string `json:"clone_url"`
	HTMLURL  string `json:"html_url"`
}

type githubWebhookUser struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
}

type githubWebhookIssue struct {
	ID          int64             `json:"id"`
	Number      int64             `json:"number"`
	Title       string            `json:"title"`
	Body        string            `json:"body"`
	State       string            `json:"state"`
	StateReason string            `json:"state_reason"`
	HTMLURL     string            `json:"html_url"`
	User        githubWebhookUser `json:"user"`
}

type githubWebhookPullRequest struct {
	ID             int64             `json:"id"`
	Number         int64             `json:"number"`
	Title          string            `json:"title"`
	Body           string            `json:"body"`
	State          string            `json:"state"`
	Draft          bool              `json:"draft"`
	Merged         bool              `json:"merged"`
	HTMLURL        string            `json:"html_url"`
	MergeCommitSHA string            `json:"merge_commit_sha"`
	User           githubWebhookUser `json:"user"`
	Head           struct {
		SHA string `json:"sha"`
		Ref string `json:"ref"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

type githubWebhookComment struct {
	ID       int64             `json:"id"`
	Body     string            `json:"body"`
	HTMLURL  string            `json:"html_url"`
	User     githubWebhookUser `json:"user"`
	CommitID string            `json:"commit_id"`
	Path     string            `json:"path"`
	Line     int64             `json:"line"`
	Side     string            `json:"side"`
}

type githubWebhookReview struct {
	ID       int64             `json:"id"`
	Body     string            `json:"body"`
	State    string            `json:"state"`
	HTMLURL  string            `json:"html_url"`
	CommitID string            `json:"commit_id"`
	User     githubWebhookUser `json:"user"`
}

type githubWebhookWorkflowRun struct {
	ID         int64     `json:"id"`
	HeadSHA    string    `json:"head_sha"`
	HeadBranch string    `json:"head_branch"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion"`
	HTMLURL    string    `json:"html_url"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func (policy *GitForgePolicy) processGitHubWebhook(ctx context.Context, delivery githubWebhookDelivery) error {
	var payload githubWebhookPayload
	if err := json.Unmarshal(delivery.Payload, &payload); err != nil {
		return &forge.Error{Status: http.StatusBadRequest, Code: "invalid_github_webhook"}
	}
	switch delivery.EventType {
	case "installation":
		if delivery.Action == "deleted" || delivery.Action == "suspend" {
			return policy.disableGitHubConnection(ctx, delivery.ConnectionID, "GitHub installation "+delivery.Action)
		}
		if delivery.Action == "unsuspend" || delivery.Action == "created" {
			return policy.updateGitHubConnectionStatus(ctx, delivery.ConnectionID, "active", "")
		}
		return nil
	case "installation_repositories":
		for _, repository := range payload.RepositoriesRemoved {
			if err := policy.disableGitHubRepository(ctx, delivery.ConnectionID, repository.ID, "GitHub App repository access removed"); err != nil {
				return err
			}
		}
		for _, repository := range payload.RepositoriesAdded {
			if err := policy.enableGitHubRepository(ctx, delivery.ConnectionID, repository); err != nil {
				return err
			}
		}
		return nil
	case "repository":
		if delivery.Action == "renamed" || delivery.Action == "edited" || delivery.Action == "transferred" {
			return policy.renameGitHubRepository(ctx, delivery.ConnectionID, payload.Repository)
		}
		if delivery.Action == "deleted" || delivery.Action == "archived" {
			return policy.disableGitHubRepository(ctx, delivery.ConnectionID, payload.Repository.ID, "GitHub repository "+delivery.Action)
		}
		return nil
	case "push":
		return policy.recordGitHubPush(ctx, delivery, payload)
	case "workflow_run":
		repository, found, err := policy.repositoryForGitHubCI(ctx, delivery.ConnectionID, delivery.ExternalRepositoryID)
		if err != nil || !found {
			return err
		}
		return policy.processGitHubWorkflowRun(ctx, repository, payload.WorkflowRun)
	}
	repository, found, err := policy.repositoryForGitHub(ctx, delivery.ConnectionID, delivery.ExternalRepositoryID)
	if err != nil || !found {
		if err == nil {
			return &forge.Error{Status: http.StatusNotFound, Code: "github_repository_not_attached"}
		}
		return err
	}
	if marker := webhookCorrelationMarker(delivery.Payload); marker != "" {
		if acknowledged, err := policy.acknowledgeCorrelatedWebhook(ctx, repository, delivery, payload, marker); err != nil || acknowledged {
			return err
		}
	}
	if repository.Integration.CollaborationBackend != forge.BackendGitHub {
		if repository.Integration.MirrorBackend != forge.BackendGitHub {
			return nil
		}
		return policy.processGitHubMirrorObservation(ctx, repository, delivery, payload)
	}
	switch delivery.EventType {
	case "issues":
		root, mapping, err := policy.ensureProviderIssue(ctx, repository, delivery, payload.Issue)
		if err != nil {
			return err
		}
		if delivery.Action == "closed" || delivery.Action == "reopened" {
			return policy.publishProviderState(ctx, repository, root, mapping, delivery, issueState(payload.Issue))
		}
	case "pull_request":
		root, mapping, err := policy.ensureProviderPullRequest(ctx, repository, delivery, payload.PullRequest)
		if err != nil {
			return err
		}
		if delivery.Action == "synchronize" {
			return policy.publishProviderPullRequestUpdate(ctx, repository, root, mapping, delivery, payload.PullRequest)
		}
		state := pullRequestWebhookState(delivery.Action, payload.PullRequest)
		if state != "" {
			return policy.publishProviderState(ctx, repository, root, mapping, delivery, state)
		}
	case "issue_comment":
		if delivery.Action == "created" {
			return policy.publishExternalComment(ctx, repository, delivery, strconv.FormatInt(payload.Issue.ID, 10), payload.Comment, false)
		}
	case "pull_request_review":
		if delivery.Action == "submitted" || delivery.Action == "edited" {
			return policy.publishExternalReview(ctx, repository, delivery, strconv.FormatInt(payload.PullRequest.ID, 10), payload.Review, githubWebhookComment{})
		}
	case "pull_request_review_comment":
		if delivery.Action == "created" {
			return policy.publishExternalReview(ctx, repository, delivery, strconv.FormatInt(payload.PullRequest.ID, 10), githubWebhookReview{}, payload.Comment)
		}
	}
	return nil
}

func (policy *GitForgePolicy) processGitHubMirrorObservation(ctx context.Context, repository forge.Repository, delivery githubWebhookDelivery, payload githubWebhookPayload) error {
	switch delivery.EventType {
	case "issues":
		return policy.publishMappedStateObservation(ctx, repository, delivery, "issue", strconv.FormatInt(payload.Issue.ID, 10), "GitHub state changed to "+issueState(payload.Issue), payload.Issue.User)
	case "pull_request":
		state := pullRequestWebhookState(delivery.Action, payload.PullRequest)
		if delivery.Action == "synchronize" {
			state = "head changed to " + payload.PullRequest.Head.SHA
		}
		if state == "" {
			return nil
		}
		return policy.publishMappedStateObservation(ctx, repository, delivery, "pull_request", strconv.FormatInt(payload.PullRequest.ID, 10), "GitHub "+state, payload.PullRequest.User)
	case "issue_comment":
		if delivery.Action == "created" {
			return policy.publishExternalComment(ctx, repository, delivery, strconv.FormatInt(payload.Issue.ID, 10), payload.Comment, false)
		}
	case "pull_request_review":
		if delivery.Action == "submitted" || delivery.Action == "edited" {
			return policy.publishExternalReview(ctx, repository, delivery, strconv.FormatInt(payload.PullRequest.ID, 10), payload.Review, githubWebhookComment{})
		}
	case "pull_request_review_comment":
		if delivery.Action == "created" {
			return policy.publishExternalReview(ctx, repository, delivery, strconv.FormatInt(payload.PullRequest.ID, 10), githubWebhookReview{}, payload.Comment)
		}
	}
	return nil
}

func (policy *GitForgePolicy) publishMappedStateObservation(ctx context.Context, repository forge.Repository, delivery githubWebhookDelivery, objectType, externalID, content string, user githubWebhookUser) error {
	mapping, found, err := policy.loadMappingByExternal(ctx, forge.BackendGitHub, delivery.ConnectionID, externalID, objectType)
	if err != nil || !found {
		return err
	}
	root, found, err := policy.eventByHexID(ctx, mapping.NostrRootID)
	if err != nil || !found {
		return missingMappedEvent(err, found)
	}
	return policy.publishExternalStateObservation(ctx, repository, root, delivery, content, user)
}

func (policy *GitForgePolicy) repositoryForGitHub(ctx context.Context, connectionID, repositoryID string) (forge.Repository, bool, error) {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	var ownerHex, repo string
	err := GetDb().QueryRowContext(queryCtx, policy.Events.Schema.Render(`
		SELECT owner, repo FROM {{.Name}}__repository_integrations
		WHERE connection_id = $1 AND external_repository_id = $2
		  AND health_state <> 'inactive'
		ORDER BY generation DESC LIMIT 1`), connectionID, repositoryID).Scan(&ownerHex, &repo)
	if err == sql.ErrNoRows {
		return forge.Repository{}, false, nil
	}
	if err != nil {
		return forge.Repository{}, false, err
	}
	owner, err := nostr.PubKeyFromHex(ownerHex)
	if err != nil {
		return forge.Repository{}, false, err
	}
	resolved, found, err := policy.ResolveIntegration(ctx, projecttask.RepositoryCoordinate{Owner: owner, ID: repo})
	return resolved.Repository, found, err
}

func (policy *GitForgePolicy) repositoryForGitHubCI(ctx context.Context, connectionID, repositoryID string) (forge.Repository, bool, error) {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	var ownerHex, repo string
	err := GetDb().QueryRowContext(queryCtx, policy.Events.Schema.Render(`
		SELECT owner, repo FROM {{.Name}}__repository_integrations
		WHERE ci_connection_id = $1 AND ci_external_repository_id = $2
		  AND ci_backend = 'github' AND health_state <> 'inactive'
		ORDER BY generation DESC LIMIT 1`), connectionID, repositoryID).Scan(&ownerHex, &repo)
	if err == sql.ErrNoRows {
		return forge.Repository{}, false, nil
	}
	if err != nil {
		return forge.Repository{}, false, err
	}
	owner, err := nostr.PubKeyFromHex(ownerHex)
	if err != nil {
		return forge.Repository{}, false, err
	}
	resolved, found, err := policy.ResolveIntegration(ctx, projecttask.RepositoryCoordinate{Owner: owner, ID: repo})
	return resolved.Repository, found, err
}

func (policy *GitForgePolicy) disableGitHubConnection(ctx context.Context, id, reason string) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	tx, err := GetDb().BeginTx(queryCtx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(queryCtx, policy.Events.Schema.Render(`
		UPDATE {{.Name}}__forge_connections SET status = 'disabled', last_error = $2, updated_at = NOW()
		WHERE id = $1 AND provider = 'github'`), id, sanitizeGitErrorString(reason)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(queryCtx, policy.Events.Schema.Render(`
		UPDATE {{.Name}}__repository_integrations
		SET health_state = 'disabled', health_detail = $2, updated_at = NOW()
		WHERE connection_id = $1 OR ci_connection_id = $1`), id, sanitizeGitErrorString(reason)); err != nil {
		return err
	}
	return tx.Commit()
}

func (policy *GitForgePolicy) disableGitHubRepository(ctx context.Context, connectionID string, repositoryID int64, reason string) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	_, err := GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
		UPDATE {{.Name}}__repository_integrations
		SET health_state = 'disabled', health_detail = $3, updated_at = NOW()
		WHERE (connection_id = $1 AND external_repository_id = $2)
		   OR (ci_connection_id = $1 AND ci_external_repository_id = $2)`),
		connectionID, strconv.FormatInt(repositoryID, 10), sanitizeGitErrorString(reason))
	return err
}

func (policy *GitForgePolicy) enableGitHubRepository(ctx context.Context, connectionID string, repository githubWebhookRepository) error {
	attached, found, err := policy.repositoryForGitHub(ctx, connectionID, strconv.FormatInt(repository.ID, 10))
	if err != nil {
		return err
	}
	if !found {
		attached, found, err = policy.repositoryForGitHubCI(ctx, connectionID, strconv.FormatInt(repository.ID, 10))
		if err != nil || !found {
			return err
		}
		queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
		defer cancel()
		_, err = GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
			UPDATE {{.Name}}__repository_integrations
			SET ci_external_repository = $3, health_state = 'ready', health_detail = '', updated_at = NOW()
			WHERE owner = $1 AND repo = $2 AND ci_connection_id = $4 AND ci_external_repository_id = $5`),
			attached.Coordinate.Owner.Hex(), attached.Coordinate.ID, repository.FullName,
			connectionID, strconv.FormatInt(repository.ID, 10))
		return err
	}
	state, detail := "degraded", "signed clone/web URL is stale or does not match the attached GitHub repository"
	metadata := githubRepositoryMetadata(repository)
	if githubAnnouncementMatches(attached.Announcement, metadata) {
		state, detail = "ready", ""
	}
	if err := policy.updateIntegrationHealth(ctx, attached.Coordinate, state, detail, repository.FullName); err != nil {
		return err
	}
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	_, err = GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
		UPDATE {{.Name}}__repository_integrations SET ci_external_repository = $3, updated_at = NOW()
		WHERE owner = $1 AND repo = $2 AND ci_connection_id = $4 AND ci_external_repository_id = $5`),
		attached.Coordinate.Owner.Hex(), attached.Coordinate.ID, repository.FullName,
		connectionID, strconv.FormatInt(repository.ID, 10))
	return err
}

func (policy *GitForgePolicy) renameGitHubRepository(ctx context.Context, connectionID string, repository githubWebhookRepository) error {
	return policy.enableGitHubRepository(ctx, connectionID, repository)
}

func githubRepositoryMetadata(repository githubWebhookRepository) githubforge.RepositoryMetadata {
	return githubforge.RepositoryMetadata{ID: repository.ID, FullName: repository.FullName, CloneURL: repository.CloneURL, HTMLURL: repository.HTMLURL}
}

func (policy *GitForgePolicy) recordGitHubPush(ctx context.Context, delivery githubWebhookDelivery, payload githubWebhookPayload) error {
	repository, found, err := policy.repositoryForGitHub(ctx, delivery.ConnectionID, delivery.ExternalRepositoryID)
	if err != nil || !found {
		return err
	}
	if payload.Ref == "" || (!strings.HasPrefix(payload.Ref, "refs/heads/") && !strings.HasPrefix(payload.Ref, "refs/tags/")) {
		return &forge.Error{Status: http.StatusBadRequest, Code: "invalid_github_push"}
	}
	if repository.Integration.Replica != nil && repository.Integration.Replica.Enabled {
		managed := repository.Integration.Replica.SyncBranches && strings.HasPrefix(payload.Ref, "refs/heads/") ||
			repository.Integration.Replica.SyncTags && strings.HasPrefix(payload.Ref, "refs/tags/")
		if !managed {
			return nil
		}
	}
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	deleted := payload.Deleted || strings.Trim(payload.After, "0") == ""
	if deleted {
		_, err = GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
			DELETE FROM {{.Name}}__forge_provider_refs
			WHERE provider = 'github' AND connection_id = $1 AND owner = $2 AND repo = $3 AND ref_name = $4`),
			delivery.ConnectionID, repository.Coordinate.Owner.Hex(), repository.Coordinate.ID, payload.Ref)
	} else if !fullForgeObjectID(payload.After) {
		return &forge.Error{Status: http.StatusBadRequest, Code: "invalid_github_push"}
	} else {
		_, err = GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
		INSERT INTO {{.Name}}__forge_provider_refs
		  (provider, connection_id, owner, repo, ref_name, commit_oid, observed_at)
		VALUES ('github', $1, $2, $3, $4, $5, NOW())
		ON CONFLICT (provider, connection_id, owner, repo, ref_name) DO UPDATE SET
		  commit_oid = EXCLUDED.commit_oid, observed_at = NOW()`),
			delivery.ConnectionID, repository.Coordinate.Owner.Hex(), repository.Coordinate.ID,
			payload.Ref, strings.ToLower(payload.After))
	}
	if err != nil {
		return err
	}
	if repository.Integration.Replica == nil || !repository.Integration.Replica.Enabled {
		return nil
	}
	authorityOID := strings.ToLower(payload.After)
	if deleted {
		authorityOID = ""
	}
	_, err = GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
		INSERT INTO {{.Name}}__git_replica_refs
		  (owner, repo, ref_name, authority_oid, replica_oid, source_delivery_id, last_seen_at)
		VALUES ($1, $2, $3, $4, '', $5, NOW())
		ON CONFLICT (owner, repo, ref_name) DO UPDATE SET
		  authority_oid = EXCLUDED.authority_oid,
		  source_delivery_id = EXCLUDED.source_delivery_id, last_seen_at = NOW()`),
		repository.Coordinate.Owner.Hex(), repository.Coordinate.ID, payload.Ref, authorityOID, delivery.DeliveryID)
	if err != nil {
		return err
	}
	if err := policy.setGitReplicaState(ctx, repository.Coordinate, "stale", "", false); err != nil {
		return err
	}
	// On-demand replicas record authoritative drift but defer the transfer
	// until a mutable read asks for it. Continuous replicas take the same
	// durable path immediately.
	if repository.Integration.Replica.Mode != forge.ReplicaModeContinuous {
		return nil
	}
	return policy.enqueueGitReplicaSync(ctx, repository.Coordinate, repository.Integration.Generation, forge.ReplicaSyncRequest{
		DeliveryID: delivery.DeliveryID, RefName: payload.Ref, TargetOID: authorityOID, Reason: "github-push",
	})
}

func webhookCorrelationMarker(payload []byte) string {
	text := string(payload)
	prefix := "<!-- lapis:"
	start := strings.Index(text, prefix)
	if start < 0 {
		return ""
	}
	start += len(prefix)
	end := strings.Index(text[start:], " -->")
	if end < 0 {
		return ""
	}
	marker := text[start : start+end]
	if len(marker) != 32 {
		return ""
	}
	for _, character := range marker {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return ""
		}
	}
	return marker
}

func (policy *GitForgePolicy) acknowledgeCorrelatedWebhook(ctx context.Context, repository forge.Repository, delivery githubWebhookDelivery, payload githubWebhookPayload, marker string) (bool, error) {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	operation, err := policy.scanForgeOperation(GetDb().QueryRowContext(queryCtx, policy.Events.Schema.Render(`
		SELECT operation_id, provider, connection_id, owner, repo, command,
		       object_type, actor, nostr_root_id, source_event_id, expected_head, payload,
		       idempotency_key, correlation_marker, processing_state,
		       attempt_count, provider_object_id, provider_result,
		       sanitized_failure, created_at, updated_at
		FROM {{.Name}}__forge_outbox
		WHERE provider = 'github' AND connection_id = $1 AND owner = $2 AND repo = $3
		  AND correlation_marker = $4`), delivery.ConnectionID, repository.Coordinate.Owner.Hex(), repository.Coordinate.ID, marker))
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	result := webhookCollaborationResult(delivery, payload)
	if result.ExternalID == "" {
		return true, nil
	}
	if operation.ProviderResult == nil {
		if err := policy.storeForgeProviderResult(ctx, operation, result); err != nil {
			return true, err
		}
	}
	return true, nil
}

func webhookCollaborationResult(delivery githubWebhookDelivery, payload githubWebhookPayload) forge.CollaborationResult {
	result := forge.CollaborationResult{}
	switch delivery.EventType {
	case "issues":
		result = forge.CollaborationResult{ExternalID: strconv.FormatInt(payload.Issue.ID, 10), ExternalNumber: payload.Issue.Number, ExternalURL: payload.Issue.HTMLURL, State: payload.Issue.State, ExternalAuthor: payload.Issue.User.Login, ExternalAuthorID: strconv.FormatInt(payload.Issue.User.ID, 10)}
	case "pull_request":
		result = forge.CollaborationResult{ExternalID: strconv.FormatInt(payload.PullRequest.ID, 10), ExternalNumber: payload.PullRequest.Number, ExternalURL: payload.PullRequest.HTMLURL, State: payload.PullRequest.State, HeadSHA: payload.PullRequest.Head.SHA, HeadRef: payload.PullRequest.Head.Ref, BaseRef: payload.PullRequest.Base.Ref, ExternalAuthor: payload.PullRequest.User.Login, ExternalAuthorID: strconv.FormatInt(payload.PullRequest.User.ID, 10)}
	case "issue_comment", "pull_request_review_comment":
		result = forge.CollaborationResult{ExternalID: strconv.FormatInt(payload.Comment.ID, 10), ExternalURL: payload.Comment.HTMLURL, ExternalAuthor: payload.Comment.User.Login, ExternalAuthorID: strconv.FormatInt(payload.Comment.User.ID, 10)}
	}
	return result
}

func (policy *GitForgePolicy) ensureProviderIssue(ctx context.Context, repository forge.Repository, delivery githubWebhookDelivery, issue githubWebhookIssue) (nostr.Event, forgeObjectMapping, error) {
	externalID := strconv.FormatInt(issue.ID, 10)
	mapping, found, err := policy.loadMappingByExternal(ctx, forge.BackendGitHub, delivery.ConnectionID, externalID, "issue")
	if err != nil {
		return nostr.Event{}, forgeObjectMapping{}, err
	}
	if found {
		root, exists, err := policy.eventByHexID(ctx, mapping.NostrRootID)
		return root, mapping, missingMappedEvent(err, exists)
	}
	if !validForgeTitle(issue.Title) {
		return nostr.Event{}, forgeObjectMapping{}, &forge.Error{Status: http.StatusBadRequest, Code: "invalid_github_issue"}
	}
	result := forge.CollaborationResult{ExternalID: externalID, ExternalNumber: issue.Number, ExternalURL: issue.HTMLURL, State: issue.State, ExternalAuthor: issue.User.Login, ExternalAuthorID: strconv.FormatInt(issue.User.ID, 10)}
	event, err := policy.newProviderIssueEvent(ctx, repository, delivery.ConnectionID, delivery.ReceivedAt, issue.Title, issue.Body, result)
	if err != nil {
		return nostr.Event{}, forgeObjectMapping{}, err
	}
	mapping = forgeObjectMapping{Provider: forge.BackendGitHub, ConnectionID: delivery.ConnectionID, Coordinate: repository.Coordinate, ObjectType: "issue", NostrEventID: event.ID.Hex(), NostrRootID: event.ID.Hex(), ExternalID: externalID, ExternalNumber: issue.Number, Origin: "provider", Authority: "provider", ExternalURL: issue.HTMLURL}
	return event, mapping, policy.saveForgeMapping(ctx, mapping)
}

func (policy *GitForgePolicy) ensureProviderPullRequest(ctx context.Context, repository forge.Repository, delivery githubWebhookDelivery, pull githubWebhookPullRequest) (nostr.Event, forgeObjectMapping, error) {
	externalID := strconv.FormatInt(pull.ID, 10)
	mapping, found, err := policy.loadMappingByExternal(ctx, forge.BackendGitHub, delivery.ConnectionID, externalID, "pull_request")
	if err != nil {
		return nostr.Event{}, forgeObjectMapping{}, err
	}
	if found {
		root, exists, err := policy.eventByHexID(ctx, mapping.NostrRootID)
		return root, mapping, missingMappedEvent(err, exists)
	}
	if !validForgeTitle(pull.Title) || !fullForgeObjectID(pull.Head.SHA) {
		return nostr.Event{}, forgeObjectMapping{}, &forge.Error{Status: http.StatusBadRequest, Code: "invalid_github_pull_request"}
	}
	result := forge.CollaborationResult{ExternalID: externalID, ExternalNumber: pull.Number, ExternalURL: pull.HTMLURL, State: pull.State, HeadSHA: pull.Head.SHA, HeadRef: pull.Head.Ref, BaseRef: pull.Base.Ref, ExternalAuthor: pull.User.Login, ExternalAuthorID: strconv.FormatInt(pull.User.ID, 10)}
	event, err := policy.newProviderPullRequestEvent(ctx, repository, delivery.ConnectionID, delivery.ReceivedAt, pull.Title, pull.Body, pull.Draft, result)
	if err != nil {
		return nostr.Event{}, forgeObjectMapping{}, err
	}
	mapping = forgeObjectMapping{Provider: forge.BackendGitHub, ConnectionID: delivery.ConnectionID, Coordinate: repository.Coordinate, ObjectType: "pull_request", NostrEventID: event.ID.Hex(), NostrRootID: event.ID.Hex(), ExternalID: externalID, ExternalNumber: pull.Number, Origin: "provider", Authority: "provider", ExternalURL: pull.HTMLURL}
	return event, mapping, policy.saveForgeMapping(ctx, mapping)
}

func missingMappedEvent(err error, found bool) error {
	if err != nil {
		return err
	}
	if !found {
		return &forge.Error{Status: http.StatusConflict, Code: "mapped_nostr_event_missing", Retryable: true}
	}
	return nil
}

func (policy *GitForgePolicy) newProviderIssueEvent(ctx context.Context, repository forge.Repository, connectionID string, at time.Time, title, body string, result forge.CollaborationResult) (nostr.Event, error) {
	secret, err := policy.bridgeSecretForConnection(ctx, connectionID)
	if err != nil {
		return nostr.Event{}, err
	}
	event := nostr.Event{CreatedAt: nostr.Timestamp(at.Unix()), Kind: nostr.KindIssue, Content: body, Tags: nostr.Tags{{"a", repository.Coordinate.String()}, {"p", repository.Coordinate.Owner.Hex()}, {"subject", title}}}
	event.Tags = append(event.Tags, externalProvenanceTags(result, nil)...)
	if err := event.Sign(secret); err != nil {
		return nostr.Event{}, err
	}
	return event, policy.persistBridgeEvent(ctx, event)
}

func (policy *GitForgePolicy) newProviderPullRequestEvent(ctx context.Context, repository forge.Repository, connectionID string, at time.Time, title, body string, draft bool, result forge.CollaborationResult) (nostr.Event, error) {
	secret, err := policy.bridgeSecretForConnection(ctx, connectionID)
	if err != nil {
		return nostr.Event{}, err
	}
	resolved, err := policy.Registry.Resolve(ctx, repository)
	if err != nil || !resolved.Capabilities.Clone.Available {
		return nostr.Event{}, &forge.Error{Status: http.StatusConflict, Code: "clone_unavailable"}
	}
	event := nostr.Event{CreatedAt: nostr.Timestamp(at.Unix()), Kind: projecttask.KindPullRequest, Content: body, Tags: nostr.Tags{{"a", repository.Coordinate.String()}, {"p", repository.Coordinate.Owner.Hex()}, {"subject", title}, {"c", strings.ToLower(result.HeadSHA)}, {"clone", resolved.Capabilities.Clone.URL}}}
	if result.HeadRef != "" {
		event.Tags = append(event.Tags, nostr.Tag{"branch-name", result.HeadRef})
	}
	if result.BaseRef != "" {
		event.Tags = append(event.Tags, nostr.Tag{"target-branch", result.BaseRef})
	}
	if draft {
		event.Tags = append(event.Tags, nostr.Tag{"t", "draft"})
	}
	event.Tags = append(event.Tags, externalProvenanceTags(result, nil)...)
	if err := event.Sign(secret); err != nil {
		return nostr.Event{}, err
	}
	return event, policy.persistBridgeEvent(ctx, event)
}

func (policy *GitForgePolicy) bridgeSecretForConnection(ctx context.Context, connectionID string) (nostr.SecretKey, error) {
	connection, found, err := policy.LoadGitHubConnection(ctx, connectionID)
	if err != nil || !found {
		if err == nil {
			err = &forge.Error{Status: http.StatusServiceUnavailable, Code: "github_connection_unavailable", Retryable: true}
		}
		return nostr.SecretKey{}, err
	}
	return policy.bridgeSecret(connection)
}

func issueState(issue githubWebhookIssue) string {
	if issue.State == "closed" && issue.StateReason == "not_planned" {
		return "not_planned"
	}
	return issue.State
}

func pullRequestWebhookState(action string, pull githubWebhookPullRequest) string {
	switch action {
	case "closed":
		if pull.Merged {
			return "merged"
		}
		return "closed"
	case "reopened", "ready_for_review":
		return "open"
	case "converted_to_draft":
		return "draft"
	default:
		return ""
	}
}

func (policy *GitForgePolicy) publishProviderPullRequestUpdate(ctx context.Context, repository forge.Repository, root nostr.Event, mapping forgeObjectMapping, delivery githubWebhookDelivery, pull githubWebhookPullRequest) error {
	if mapping.Authority != "provider" || !fullForgeObjectID(pull.Head.SHA) {
		return policy.publishExternalStateObservation(ctx, repository, root, delivery, "GitHub head changed to "+pull.Head.SHA, pull.User)
	}
	state, err := projecttask.ReducePullRequest(root, repository.Announcement, policy.pullRequestRelatedEvents(ctx, root.ID))
	if err == nil && strings.EqualFold(state.CurrentCommit, pull.Head.SHA) {
		return nil
	}
	secret, err := policy.bridgeSecretForConnection(ctx, delivery.ConnectionID)
	if err != nil {
		return err
	}
	resolved, err := policy.Registry.Resolve(ctx, repository)
	if err != nil || !resolved.Capabilities.Clone.Available {
		return &forge.Error{Status: http.StatusConflict, Code: "clone_unavailable"}
	}
	result := forge.CollaborationResult{ExternalID: mapping.ExternalID, ExternalNumber: mapping.ExternalNumber, ExternalURL: mapping.ExternalURL, ExternalAuthor: pull.User.Login, ExternalAuthorID: strconv.FormatInt(pull.User.ID, 10)}
	event := nostr.Event{CreatedAt: nostr.Timestamp(delivery.ReceivedAt.Unix()), Kind: projecttask.KindPullRequestUpdate, Tags: nostr.Tags{{"E", root.ID.Hex()}, {"P", root.PubKey.Hex()}, {"a", repository.Coordinate.String()}, {"c", strings.ToLower(pull.Head.SHA)}, {"clone", resolved.Capabilities.Clone.URL}}}
	event.Tags = append(event.Tags, externalProvenanceTags(result, nil)...)
	if err := event.Sign(secret); err != nil {
		return err
	}
	return policy.persistBridgeEvent(ctx, event)
}

func (policy *GitForgePolicy) publishProviderState(ctx context.Context, repository forge.Repository, root nostr.Event, mapping forgeObjectMapping, delivery githubWebhookDelivery, state string) error {
	if mapping.Authority != "provider" {
		return policy.publishExternalStateObservation(ctx, repository, root, delivery, "GitHub state changed to "+state, githubWebhookUser{})
	}
	secret, err := policy.bridgeSecretForConnection(ctx, delivery.ConnectionID)
	if err != nil {
		return err
	}
	if root.PubKey != secret.Public() {
		return &forge.Error{Status: http.StatusConflict, Code: "provider_authority_mismatch"}
	}
	kind := forgeStatusKind(state)
	if kind == 0 {
		return nil
	}
	result := forge.CollaborationResult{ExternalID: mapping.ExternalID, ExternalNumber: mapping.ExternalNumber, ExternalURL: mapping.ExternalURL}
	event := nostr.Event{CreatedAt: nostr.Timestamp(delivery.ReceivedAt.Unix()), Kind: kind, Tags: nostr.Tags{{"e", root.ID.Hex(), "", "root"}, {"a", repository.Coordinate.String()}}}
	if kind == nostr.KindStatusApplied {
		var payload githubWebhookPayload
		_ = json.Unmarshal(delivery.Payload, &payload)
		if fullForgeObjectID(payload.PullRequest.MergeCommitSHA) {
			commit := strings.ToLower(payload.PullRequest.MergeCommitSHA)
			event.Tags = append(event.Tags, nostr.Tag{"merge-commit", commit}, nostr.Tag{"r", commit})
		}
	}
	event.Tags = append(event.Tags, externalProvenanceTags(result, nil)...)
	if err := event.Sign(secret); err != nil {
		return err
	}
	return policy.persistBridgeEvent(ctx, event)
}

func (policy *GitForgePolicy) publishExternalStateObservation(ctx context.Context, repository forge.Repository, root nostr.Event, delivery githubWebhookDelivery, content string, user githubWebhookUser) error {
	result := forge.CollaborationResult{ExternalID: delivery.ExternalObjectID, ExternalAuthor: user.Login, ExternalAuthorID: strconv.FormatInt(user.ID, 10)}
	return policy.publishBridgeNote(ctx, repository, root, delivery.ConnectionID, delivery.ReceivedAt, content, nostr.Tags{{"t", "external-state"}}, result)
}

func (policy *GitForgePolicy) publishExternalComment(ctx context.Context, repository forge.Repository, delivery githubWebhookDelivery, rootExternalID string, comment githubWebhookComment, review bool) error {
	objectType := "issue"
	rootMapping, found, err := policy.loadMappingByExternal(ctx, forge.BackendGitHub, delivery.ConnectionID, rootExternalID, objectType)
	if !found && err == nil {
		objectType = "pull_request"
		rootMapping, found, err = policy.loadMappingByExternal(ctx, forge.BackendGitHub, delivery.ConnectionID, rootExternalID, objectType)
	}
	if err != nil || !found {
		return err
	}
	root, found, err := policy.eventByHexID(ctx, rootMapping.NostrRootID)
	if err != nil || !found {
		return missingMappedEvent(err, found)
	}
	tags := nostr.Tags{}
	if review {
		tags = append(tags, nostr.Tag{"t", "external-review"})
	}
	if fullForgeObjectID(comment.CommitID) {
		tags = append(tags, nostr.Tag{"c", strings.ToLower(comment.CommitID)})
	}
	if comment.Path != "" {
		tags = append(tags, nostr.Tag{"path", comment.Path})
	}
	if comment.Line > 0 {
		tags = append(tags, nostr.Tag{"line", strconv.FormatInt(comment.Line, 10)})
	}
	if comment.Side != "" {
		tags = append(tags, nostr.Tag{"side", strings.ToLower(comment.Side)})
	}
	result := forge.CollaborationResult{ExternalID: strconv.FormatInt(comment.ID, 10), ExternalURL: comment.HTMLURL, ExternalAuthor: comment.User.Login, ExternalAuthorID: strconv.FormatInt(comment.User.ID, 10)}
	if err := policy.publishBridgeNote(ctx, repository, root, delivery.ConnectionID, delivery.ReceivedAt, comment.Body, tags, result); err != nil {
		return err
	}
	return nil
}

func (policy *GitForgePolicy) publishExternalReview(ctx context.Context, repository forge.Repository, delivery githubWebhookDelivery, rootExternalID string, review githubWebhookReview, comment githubWebhookComment) error {
	if comment.ID != 0 {
		return policy.publishExternalComment(ctx, repository, delivery, rootExternalID, comment, true)
	}
	mapping, found, err := policy.loadMappingByExternal(ctx, forge.BackendGitHub, delivery.ConnectionID, rootExternalID, "pull_request")
	if err != nil || !found {
		return err
	}
	root, found, err := policy.eventByHexID(ctx, mapping.NostrRootID)
	if err != nil || !found {
		return missingMappedEvent(err, found)
	}
	tags := nostr.Tags{{"t", "external-review"}, {"external-decision", strings.ToLower(review.State)}}
	if fullForgeObjectID(review.CommitID) {
		tags = append(tags, nostr.Tag{"c", strings.ToLower(review.CommitID)})
	}
	result := forge.CollaborationResult{ExternalID: strconv.FormatInt(review.ID, 10), ExternalURL: review.HTMLURL, ExternalAuthor: review.User.Login, ExternalAuthorID: strconv.FormatInt(review.User.ID, 10)}
	return policy.publishBridgeNote(ctx, repository, root, delivery.ConnectionID, delivery.ReceivedAt, review.Body, tags, result)
}

func (policy *GitForgePolicy) publishBridgeNote(ctx context.Context, repository forge.Repository, root nostr.Event, connectionID string, at time.Time, content string, extra nostr.Tags, result forge.CollaborationResult) error {
	secret, err := policy.bridgeSecretForConnection(ctx, connectionID)
	if err != nil {
		return err
	}
	event := nostr.Event{CreatedAt: nostr.Timestamp(at.Unix()), Kind: nostr.KindTextNote, Content: content, Tags: nostr.Tags{{"e", root.ID.Hex(), "", "root"}, {"a", repository.Coordinate.String()}, {"p", root.PubKey.Hex()}}}
	event.Tags = append(event.Tags, extra...)
	event.Tags = append(event.Tags, externalProvenanceTags(result, nil)...)
	if err := event.Sign(secret); err != nil {
		return err
	}
	if err := policy.persistBridgeEvent(ctx, event); err != nil {
		return err
	}
	if result.ExternalID != "" {
		return policy.saveForgeMapping(ctx, forgeObjectMapping{Provider: forge.BackendGitHub, ConnectionID: connectionID, Coordinate: repository.Coordinate, ObjectType: "activity", NostrEventID: event.ID.Hex(), NostrRootID: root.ID.Hex(), ExternalID: result.ExternalID, Origin: "provider", Authority: "observation", ExternalURL: result.ExternalURL})
	}
	return nil
}

func (policy *GitForgePolicy) processGitHubWorkflowRun(ctx context.Context, repository forge.Repository, webhook githubWebhookWorkflowRun) error {
	if webhook.ID <= 0 || !fullForgeObjectID(webhook.HeadSHA) || !strings.HasPrefix(webhook.HeadBranch, "lapis-ci/") {
		return nil
	}
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	var operationID string
	err := GetDb().QueryRowContext(queryCtx, policy.Events.Schema.Render(`
		SELECT operation_id FROM {{.Name}}__forge_ci_runs
		WHERE provider = 'github' AND connection_id = $1 AND owner = $2 AND repo = $3
		  AND (provider_run_id = $4 OR (provider_run_id = '' AND mirror_ref = $5))
		ORDER BY updated_at DESC LIMIT 1`), repository.Integration.CIConnectionID,
		repository.Coordinate.Owner.Hex(), repository.Coordinate.ID,
		strconv.FormatInt(webhook.ID, 10), "refs/heads/"+webhook.HeadBranch).Scan(&operationID)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	run, found, err := policy.loadForgeCIRun(ctx, operationID)
	if err != nil || !found {
		return err
	}
	if !strings.EqualFold(run.Commit, webhook.HeadSHA) {
		return policy.failForgeCIRun(ctx, run, &forge.Error{Status: http.StatusBadGateway, Code: "github_ci_head_mismatch"})
	}
	status := strings.ToLower(webhook.Status)
	if status == "completed" && webhook.Conclusion != "" {
		status = strings.ToLower(webhook.Conclusion)
	}
	run.ProviderRunID = strconv.FormatInt(webhook.ID, 10)
	run.ProviderURL = webhook.HTMLURL
	run.Status = status
	run.ProviderUpdatedAt = webhook.UpdatedAt
	if err := policy.updateForgeCIRun(ctx, run); err != nil {
		return err
	}
	if !terminalCIStatus(run.Status) {
		return nil
	}
	resolved, found, err := policy.ResolveIntegration(ctx, run.Coordinate)
	if err != nil || !found || resolved.CI == nil {
		return err
	}
	return policy.finishForgeCIRun(ctx, resolved, run, webhook.UpdatedAt)
}
