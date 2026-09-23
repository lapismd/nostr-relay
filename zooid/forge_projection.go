package zooid

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"

	"fiatjaf.com/nostr"
	"zooid/internal/forge"
	"zooid/internal/gitforge"
	"zooid/internal/githubforge"
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
		if root.Kind == projecttask.KindPullRequest {
			switch {
			case forgeHasTagValue(event, "t", "inline-comment"):
				line, lineErr := strconv.ParseInt(forgeFirstTag(event, "line"), 10, 64)
				path := forgeFirstTag(event, "file")
				commit := strings.ToLower(forgeFirstTag(event, "c"))
				side := strings.ToLower(forgeFirstTag(event, "side"))
				if path == "" || lineErr != nil || line <= 0 || !fullForgeObjectID(commit) {
					return nil
				}
				switch side {
				case "old", "left":
					side = "LEFT"
				case "new", "right":
					side = "RIGHT"
				default:
					return nil
				}
				command, objectType = "review_comment", "comment"
				payload["commit_id"] = commit
				payload["path"] = path
				payload["line"] = line
				payload["side"] = side
			case forgeHasTagValue(event, "t", "approval"):
				command, objectType = "review", "review"
				payload["event"] = "APPROVE"
			case forgeHasTagValue(event, "t", "changes-requested"):
				command, objectType = "review", "review"
				payload["event"] = "REQUEST_CHANGES"
			case forgeHasTagValue(event, "t", "review-comment"):
				command, objectType = "review", "review"
				payload["event"] = "COMMENT"
			}
			if command == "review" {
				if commit := strings.ToLower(forgeFirstTag(event, "c")); fullForgeObjectID(commit) {
					payload["commit_id"] = commit
				}
			}
		}
	case nostr.KindReaction:
		content, supported := githubforge.ReactionContent(event.Content)
		if !supported {
			return nil
		}
		targetID := forgeFirstTag(event, "e")
		var found bool
		var err error
		root, found, err = policy.eventByHexID(ctx, targetID)
		if err != nil || !found || root.Kind != nostr.KindTextNote && root.Kind != nostr.KindComment || root.Tags.Find("external-decision") != nil {
			return err
		}
		coordinate, err = repositoryCoordinateFromEvent(event)
		if err != nil || repositoryTagValueForForge(root) != coordinate.String() {
			return nil
		}
		command, objectType = "reaction_add", "reaction"
		payload["content"] = content
		payload["target_event_id"] = root.ID.Hex()
	case nostr.KindDeletion:
		reactionID := forgeFirstTag(event, "e")
		if reactionID == "" {
			return nil
		}
		reaction, found, err := policy.eventByHexID(ctx, reactionID)
		if err != nil || !found || reaction.Kind != nostr.KindReaction || reaction.PubKey != event.PubKey {
			return err
		}
		targetID := forgeFirstTag(reaction, "e")
		root, found, err = policy.eventByHexID(ctx, targetID)
		if err != nil || !found || root.Kind != nostr.KindTextNote && root.Kind != nostr.KindComment || root.Tags.Find("external-decision") != nil {
			return err
		}
		coordinate, err = repositoryCoordinateFromEvent(reaction)
		if err != nil || repositoryTagValueForForge(root) != coordinate.String() {
			return nil
		}
		command, objectType = "reaction_delete", "reaction"
		payload["target_event_id"] = root.ID.Hex()
		payload["reaction_event_id"] = reaction.ID.Hex()
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
	if err := policy.rememberLogReviewRef(ctx, event, resolved); err != nil {
		return err
	}
	githubCollaboration := resolved.Repository.Integration.CollaborationBackend == forge.BackendGitHub && resolved.Collaboration != nil
	githubMirror := resolved.Repository.Integration.MirrorBackend == forge.BackendGitHub && resolved.Mirror != nil
	if !githubCollaboration && !githubMirror {
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

func (policy *GitForgePolicy) rememberLogReviewRef(ctx context.Context, event nostr.Event, resolved forge.ResolvedRepository) error {
	if event.Kind != projecttask.KindPullRequest || resolved.Repository.Integration.GitAuthorityBackend != forge.BackendWalGit {
		return nil
	}
	reviewRef, ok := gitforge.LogReviewRef(forgeFirstTag(event, "branch-name"), event.PubKey)
	if !ok {
		return nil
	}
	head := strings.ToLower(forgeFirstTag(event, "c"))
	if !fullForgeObjectID(head) {
		return nil
	}
	coordinate, err := repositoryCoordinateFromEvent(event)
	if err != nil {
		return nil
	}
	payload, _ := json.Marshal(map[string]string{"review_ref": reviewRef})
	idempotency := "review-ref:" + event.ID.Hex()
	digest := sha256.Sum256([]byte(idempotency))
	_, err = policy.enqueueForgeOperation(ctx, forgeOperation{
		ID: "rev_" + hex.EncodeToString(digest[:16]), Provider: forge.BackendRelay,
		Coordinate: coordinate, Command: "publish_review_ref", ObjectType: "pull_request",
		Actor: event.PubKey, RootEventID: event.ID.Hex(), SourceEventID: event.ID.Hex(),
		ExpectedHead: head, Payload: payload, IdempotencyKey: idempotency,
	})
	return err
}

func (policy *GitForgePolicy) rebuildForgeOutbox(ctx context.Context) error {
	for _, kinds := range [][]nostr.Kind{
		{nostr.KindIssue, projecttask.KindPullRequest},
		{nostr.KindTextNote, nostr.KindComment, nostr.KindStatusOpen, nostr.KindStatusApplied, nostr.KindStatusClosed, nostr.KindStatusDraft, nostr.KindReaction, nostr.KindDeletion},
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
	rootIDs := make([]string, 0)
	for _, kind := range []nostr.Kind{nostr.KindIssue, projecttask.KindPullRequest} {
		filter := nostr.Filter{Kinds: []nostr.Kind{kind}, Tags: nostr.TagMap{"a": {coordinate.String()}}, Limit: 2000}
		for event := range policy.Events.QueryEventsBefore(ctx, filter, "", 2000) {
			if policy.ProjectTasks.IsTombstoned(ctx, event) {
				continue
			}
			if err := policy.rememberForgeProjection(ctx, event); err != nil {
				return err
			}
			rootIDs = append(rootIDs, event.ID.Hex())
		}
	}
	relatedKinds := []nostr.Kind{
		nostr.KindTextNote, nostr.KindComment, nostr.KindStatusOpen,
		nostr.KindStatusApplied, nostr.KindStatusClosed, nostr.KindStatusDraft,
	}
	seen := make(map[nostr.ID]struct{})
	for _, rootID := range rootIDs {
		for _, tagName := range []string{"e", "E"} {
			filter := nostr.Filter{Kinds: relatedKinds, Tags: nostr.TagMap{tagName: {rootID}}, Limit: 2000}
			for event := range policy.Events.QueryEventsBefore(ctx, filter, "", 2000) {
				if _, duplicate := seen[event.ID]; duplicate || policy.ProjectTasks.IsTombstoned(ctx, event) {
					continue
				}
				seen[event.ID] = struct{}{}
				if err := policy.rememberForgeProjection(ctx, event); err != nil {
					return err
				}
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

func repositoryTagValueForForge(event nostr.Event) string {
	tag := event.Tags.Find("a")
	if tag == nil || len(tag) < 2 {
		return ""
	}
	return tag[1]
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
