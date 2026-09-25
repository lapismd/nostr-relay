package zooid

import (
	"context"
	"encoding/json"
	"fiatjaf.com/nostr"
	"net/http"
	"strconv"
	"strings"
	"time"
	"zooid/internal/forge"
	"zooid/internal/projecttask"
)

func webhookCollaborationResult(delivery githubWebhookDelivery, payload githubWebhookPayload) forge.CollaborationResult {
	result := forge.CollaborationResult{}
	switch delivery.EventType {
	case "issues":
		result = forge.CollaborationResult{ExternalID: strconv.FormatInt(payload.Issue.ID, 10), ExternalNumber: payload.Issue.Number, ExternalURL: payload.Issue.HTMLURL, State: payload.Issue.State, ExternalAuthor: payload.Issue.User.Login, ExternalAuthorID: strconv.FormatInt(payload.Issue.User.ID, 10)}
	case "pull_request":
		result = forge.CollaborationResult{ExternalID: strconv.FormatInt(payload.PullRequest.ID, 10), ExternalNumber: payload.PullRequest.Number, ExternalURL: payload.PullRequest.HTMLURL, State: payload.PullRequest.State, HeadSHA: payload.PullRequest.Head.SHA, HeadRef: payload.PullRequest.Head.Ref, BaseSHA: payload.PullRequest.Base.SHA, BaseRef: payload.PullRequest.Base.Ref, ExternalAuthor: payload.PullRequest.User.Login, ExternalAuthorID: strconv.FormatInt(payload.PullRequest.User.ID, 10)}
	case "issue_comment", "pull_request_review_comment":
		result = forge.CollaborationResult{ExternalID: strconv.FormatInt(payload.Comment.ID, 10), ExternalURL: payload.Comment.HTMLURL, ExternalAuthor: payload.Comment.User.Login, ExternalAuthorID: strconv.FormatInt(payload.Comment.User.ID, 10)}
	case "pull_request_review":
		result = forge.CollaborationResult{ExternalID: strconv.FormatInt(payload.Review.ID, 10), ExternalURL: payload.Review.HTMLURL, State: strings.ToLower(payload.Review.State), ExternalAuthor: payload.Review.User.Login, ExternalAuthorID: strconv.FormatInt(payload.Review.User.ID, 10)}
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
	result := forge.CollaborationResult{ExternalID: externalID, ExternalNumber: pull.Number, ExternalURL: pull.HTMLURL, State: pull.State, HeadSHA: pull.Head.SHA, HeadRef: pull.Head.Ref, BaseSHA: pull.Base.SHA, BaseRef: pull.Base.Ref, ExternalAuthor: pull.User.Login, ExternalAuthorID: strconv.FormatInt(pull.User.ID, 10)}
	event, err := policy.newProviderPullRequestEvent(ctx, repository, delivery.ConnectionID, delivery.ReceivedAt, pull.Title, pull.Body, pull.Draft, result)
	if err != nil {
		return nostr.Event{}, forgeObjectMapping{}, err
	}
	mapping = forgeObjectMapping{Provider: forge.BackendGitHub, ConnectionID: delivery.ConnectionID, Coordinate: repository.Coordinate, ObjectType: "pull_request", NostrEventID: event.ID.Hex(), NostrRootID: event.ID.Hex(), ExternalID: externalID, ExternalNumber: pull.Number, Origin: "provider", Authority: "provider", ExternalURL: pull.HTMLURL}
	return event, mapping, policy.saveForgeMapping(ctx, mapping)
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
	state, err := projecttask.ReducePullRequest(root, repository.Announcement, policy.pullRequestRelatedEvents(ctx, root.ID), policy.ProjectTasks.repositoryAuthority(repository.Announcement))
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
	if fullForgeObjectID(pull.Base.SHA) {
		event.Tags = append(event.Tags, nostr.Tag{"base-commit", strings.ToLower(pull.Base.SHA)})
	}
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

func (policy *GitForgePolicy) publishProviderReviewRequest(ctx context.Context, repository forge.Repository, root nostr.Event, delivery githubWebhookDelivery, payload githubWebhookPayload) error {
	reviewer := strings.TrimSpace(payload.RequestedReviewer.Login)
	if reviewer == "" {
		return &forge.Error{Status: http.StatusBadRequest, Code: "invalid_github_review_request"}
	}
	requester := strings.TrimSpace(payload.Sender.Login)
	content := "Requested @" + reviewer + " to review"
	if requester != "" {
		content = "@" + requester + " requested a review from @" + reviewer
	}
	tags := nostr.Tags{{"t", "review-request"}, {"external-reviewer", reviewer}}
	if payload.RequestedReviewer.ID > 0 {
		tags = append(tags, nostr.Tag{"external-reviewer-id", strconv.FormatInt(payload.RequestedReviewer.ID, 10)})
	}
	result := forge.CollaborationResult{ExternalAuthor: requester}
	if payload.Sender.ID > 0 {
		result.ExternalAuthorID = strconv.FormatInt(payload.Sender.ID, 10)
	}
	return policy.publishBridgeNote(ctx, repository, root, delivery.ConnectionID, delivery.ReceivedAt, content, tags, result)
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
