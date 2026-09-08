package zooid

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"fiatjaf.com/nostr"
	"zooid/internal/forge"
	"zooid/internal/projecttask"
)

func (policy *GitForgePolicy) rememberForgeProjection(ctx context.Context, event nostr.Event) error {
	if tag := event.Tags.Find("external-provider"); tag != nil && len(tag) > 1 && tag[1] == forge.BackendGitHub {
		return nil
	}
	var root nostr.Event
	var coordinate projecttask.RepositoryCoordinate
	command, objectType := "", ""
	payload := make(map[string]any)
	switch event.Kind {
	case nostr.KindIssue:
		var err error
		coordinate, err = repositoryCoordinateFromEvent(event)
		if err != nil {
			return nil
		}
		root = event
		command, objectType = "create_issue", "issue"
		payload["title"] = workItemTitle(event)
		payload["body"] = event.Content
	case projecttask.KindPullRequest:
		var err error
		coordinate, err = repositoryCoordinateFromEvent(event)
		if err != nil {
			return nil
		}
		root = event
		command, objectType = "create_pull_request", "pull_request"
		payload["title"] = workItemTitle(event)
		payload["body"] = event.Content
		payload["head_sha"] = forgeFirstTag(event, "c")
		payload["head"] = firstNonempty(forgeFirstTag(event, "branch-name"), forgeFirstTag(event, "c"))
		payload["base"] = firstNonempty(forgeFirstTag(event, "target-branch"), "main")
		payload["draft"] = forgeHasTagValue(event, "t", "draft")
	case nostr.KindTextNote, nostr.KindComment:
		rootID := forgeRootEventID(event)
		if rootID == "" {
			return nil
		}
		var found bool
		var err error
		root, found, err = policy.eventByHexID(ctx, rootID)
		if err != nil || !found || root.Kind != nostr.KindIssue && root.Kind != projecttask.KindPullRequest {
			return err
		}
		coordinate, err = repositoryCoordinateFromEvent(root)
		if err != nil {
			return nil
		}
		command, objectType = "comment", "comment"
		payload["body"] = event.Content
	case nostr.KindStatusOpen, nostr.KindStatusApplied, nostr.KindStatusClosed, nostr.KindStatusDraft:
		rootID := forgeRootEventID(event)
		if rootID == "" {
			return nil
		}
		var found bool
		var err error
		root, found, err = policy.eventByHexID(ctx, rootID)
		if err != nil || !found || root.Kind != nostr.KindIssue && root.Kind != projecttask.KindPullRequest {
			return err
		}
		coordinate, err = repositoryCoordinateFromEvent(root)
		if err != nil {
			return nil
		}
		command, objectType = "state", workItemObjectType(root)
		payload["state"] = outboundForgeState(event.Kind)
	default:
		return nil
	}
	resolved, found, err := policy.ResolveIntegration(ctx, coordinate)
	if err != nil || !found {
		return err
	}
	if resolved.Repository.Integration.CollaborationBackend != forge.BackendGitHub || resolved.Collaboration == nil {
		return nil
	}
	encoded, _ := json.Marshal(payload)
	idempotency := "nostr:" + event.ID.Hex() + ":github:" + resolved.Repository.Integration.ConnectionID
	digest := sha256.Sum256([]byte(idempotency))
	operationID := "evt_" + hex.EncodeToString(digest[:16])
	_, err = policy.enqueueForgeOperation(ctx, forgeOperation{
		ID: operationID, Provider: forge.BackendGitHub,
		ConnectionID: resolved.Repository.Integration.ConnectionID,
		Coordinate:   coordinate, Command: command, ObjectType: objectType,
		Actor: event.PubKey, RootEventID: root.ID.Hex(), SourceEventID: event.ID.Hex(),
		Payload: encoded, IdempotencyKey: idempotency,
	})
	return err
}

func (policy *GitForgePolicy) rebuildForgeOutbox(ctx context.Context) error {
	for _, kinds := range [][]nostr.Kind{
		{nostr.KindIssue, projecttask.KindPullRequest},
		{nostr.KindTextNote, nostr.KindComment, nostr.KindStatusOpen, nostr.KindStatusApplied, nostr.KindStatusClosed, nostr.KindStatusDraft},
	} {
		for event := range policy.Events.QueryEventsBefore(ctx, nostr.Filter{Kinds: kinds}, "", 0) {
			if policy.ProjectTasks.IsTombstoned(ctx, event) {
				continue
			}
			if err := policy.rememberForgeProjection(ctx, event); err != nil {
				return err
			}
		}
	}
	return nil
}

func (policy *GitForgePolicy) rebuildRepositoryForgeOutbox(ctx context.Context, coordinate projecttask.RepositoryCoordinate) error {
	for _, kind := range []nostr.Kind{nostr.KindIssue, projecttask.KindPullRequest} {
		filter := nostr.Filter{Kinds: []nostr.Kind{kind}, Tags: nostr.TagMap{"a": {coordinate.String()}}, Limit: 2000}
		for event := range policy.Events.QueryEventsBefore(ctx, filter, "", 2000) {
			if err := policy.rememberForgeProjection(ctx, event); err != nil {
				return err
			}
		}
	}
	return nil
}

func workItemTitle(event nostr.Event) string {
	title := forgeFirstTag(event, "subject")
	if title == "" {
		title = strings.TrimSpace(strings.SplitN(event.Content, "\n", 2)[0])
	}
	if len(title) > 256 {
		title = title[:256]
	}
	if title == "" {
		title = "Nostr work item"
	}
	return title
}

func forgeRootEventID(event nostr.Event) string {
	for _, name := range []string{"E", "e"} {
		for _, tag := range event.Tags {
			if len(tag) < 2 || tag[0] != name {
				continue
			}
			if name == "E" || len(tag) < 4 || tag[3] == "root" {
				if _, err := nostr.IDFromHex(tag[1]); err == nil {
					return tag[1]
				}
			}
		}
	}
	return ""
}

func forgeFirstTag(event nostr.Event, name string) string {
	tag := event.Tags.Find(name)
	if tag != nil && len(tag) > 1 {
		return tag[1]
	}
	return ""
}

func forgeHasTagValue(event nostr.Event, name, value string) bool {
	for _, tag := range event.Tags {
		if len(tag) > 1 && tag[0] == name && strings.EqualFold(tag[1], value) {
			return true
		}
	}
	return false
}

func workItemObjectType(root nostr.Event) string {
	if root.Kind == projecttask.KindPullRequest {
		return "pull_request"
	}
	return "issue"
}

func outboundForgeState(kind nostr.Kind) string {
	switch kind {
	case nostr.KindStatusOpen:
		return "open"
	case nostr.KindStatusDraft:
		return "draft"
	default:
		return "closed"
	}
}
