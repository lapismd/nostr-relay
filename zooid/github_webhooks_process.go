package zooid

import (
	"context"
	"database/sql"
	"encoding/json"
	"fiatjaf.com/nostr"
	"net/http"
	"strconv"
	"strings"
	"time"
	"zooid/internal/forge"
	"zooid/internal/projecttask"
)

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
	if marker := webhookCorrelationMarker(payload); marker != "" {
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
		if delivery.Action == "review_requested" {
			return policy.publishProviderReviewRequest(ctx, repository, root, delivery, payload)
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
	case "issue_comment_reactions", "pull_request_review_comment_reactions":
		if delivery.Action == "synchronize" {
			return policy.syncExternalCommentReactions(ctx, repository, delivery, payload)
		}
	}
	return nil
}

func (policy *GitForgePolicy) processGitHubMirrorObservation(ctx context.Context, repository forge.Repository, delivery githubWebhookDelivery, payload githubWebhookPayload) error {
	switch delivery.EventType {
	case "issues":
		return policy.publishMappedStateObservation(ctx, repository, delivery, "issue", strconv.FormatInt(payload.Issue.ID, 10), "GitHub state changed to "+issueState(payload.Issue), payload.Issue.User)
	case "pull_request":
		if delivery.Action == "review_requested" {
			mapping, found, err := policy.loadMappingByExternal(ctx, forge.BackendGitHub, delivery.ConnectionID, strconv.FormatInt(payload.PullRequest.ID, 10), "pull_request")
			if err != nil || !found {
				return err
			}
			root, found, err := policy.eventByHexID(ctx, mapping.NostrRootID)
			if err != nil || !found {
				return missingMappedEvent(err, found)
			}
			return policy.publishProviderReviewRequest(ctx, repository, root, delivery, payload)
		}
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
	case "issue_comment_reactions", "pull_request_review_comment_reactions":
		if delivery.Action == "synchronize" {
			return policy.syncExternalCommentReactions(ctx, repository, delivery, payload)
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
	if fullForgeObjectID(result.BaseSHA) {
		event.Tags = append(event.Tags, nostr.Tag{"base-commit", strings.ToLower(result.BaseSHA)})
	}
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
