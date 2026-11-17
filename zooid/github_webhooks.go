package zooid

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"
	"zooid/internal/forge"
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
	Sender              githubWebhookUser         `json:"sender"`
	RequestedReviewer   githubWebhookUser         `json:"requested_reviewer"`
	Reactions           []githubWebhookReaction   `json:"reactions"`
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
		SHA string `json:"sha"`
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

type githubWebhookReaction struct {
	ID        int64             `json:"id"`
	Content   string            `json:"content"`
	CreatedAt time.Time         `json:"created_at"`
	User      githubWebhookUser `json:"user"`
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
