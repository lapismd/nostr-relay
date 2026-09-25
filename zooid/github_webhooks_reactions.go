package zooid

import (
	"context"
	"fiatjaf.com/nostr"
	"strconv"
	"zooid/internal/forge"
	"zooid/internal/githubforge"
)

func (policy *GitForgePolicy) syncExternalCommentReactions(ctx context.Context, repository forge.Repository, delivery githubWebhookDelivery, payload githubWebhookPayload) error {
	commentID := strconv.FormatInt(payload.Comment.ID, 10)
	if payload.Comment.ID <= 0 {
		commentID = delivery.ExternalObjectID
	}
	targetMapping, found, err := policy.loadMappingByExternal(ctx, forge.BackendGitHub, delivery.ConnectionID, commentID, "activity")
	if err != nil || !found {
		return err
	}
	target, found, err := policy.eventByHexID(ctx, targetMapping.NostrEventID)
	if err != nil || !found {
		return missingMappedEvent(err, found)
	}
	secret, err := policy.bridgeSecretForConnection(ctx, delivery.ConnectionID)
	if err != nil {
		return err
	}
	existing, err := policy.loadProviderReactionMappings(ctx, delivery.ConnectionID, target.ID.Hex())
	if err != nil {
		return err
	}
	byExternalID := make(map[string]forgeObjectMapping, len(existing))
	for _, mapping := range existing {
		byExternalID[mapping.ExternalID] = mapping
	}
	current := make(map[string]struct{}, len(payload.Reactions))
	for _, reaction := range payload.Reactions {
		externalID := strconv.FormatInt(reaction.ID, 10)
		emoji, supported := githubforge.ReactionEmoji(reaction.Content)
		if reaction.ID <= 0 || !supported {
			continue
		}
		current[externalID] = struct{}{}
		if _, mapped := byExternalID[externalID]; mapped {
			continue
		}
		// A relay-authored reaction is mapped as soon as GitHub accepts it. The
		// next reconciliation snapshot contains that same GitHub reaction, so
		// retain the authored event instead of importing a bridge-authored copy.
		_, mapped, err := policy.loadMappingByExternal(ctx, forge.BackendGitHub, delivery.ConnectionID, externalID, "reaction")
		if err != nil {
			return err
		}
		if mapped {
			continue
		}
		at := reaction.CreatedAt
		if at.IsZero() {
			at = delivery.ReceivedAt
		}
		result := forge.CollaborationResult{
			ExternalID: externalID, ExternalAuthor: reaction.User.Login,
			ExternalAuthorID: strconv.FormatInt(reaction.User.ID, 10),
		}
		event := nostr.Event{CreatedAt: nostr.Timestamp(at.Unix()), Kind: nostr.KindReaction, Content: emoji, Tags: nostr.Tags{
			{"e", target.ID.Hex()}, {"p", target.PubKey.Hex()}, {"k", strconv.FormatUint(uint64(target.Kind), 10)},
			{"a", repository.Coordinate.String()},
		}}
		event.Tags = append(event.Tags, externalProvenanceTags(result, nil)...)
		if err := event.Sign(secret); err != nil {
			return err
		}
		if err := policy.persistBridgeEvent(ctx, event); err != nil {
			return err
		}
		if err := policy.saveForgeMapping(ctx, forgeObjectMapping{
			Provider: forge.BackendGitHub, ConnectionID: delivery.ConnectionID,
			Coordinate: repository.Coordinate, ObjectType: "reaction",
			NostrEventID: event.ID.Hex(), NostrRootID: target.ID.Hex(), ExternalID: externalID,
			Origin: "provider", Authority: "observation",
		}); err != nil {
			return err
		}
	}
	for _, mapping := range existing {
		if _, present := current[mapping.ExternalID]; present {
			continue
		}
		reaction, found, err := policy.eventByHexID(ctx, mapping.NostrEventID)
		if err != nil || !found {
			return missingMappedEvent(err, found)
		}
		if reaction.PubKey != secret.Public() {
			continue
		}
		if !policy.ProjectTasks.IsTombstoned(ctx, reaction) {
			deletion := nostr.Event{CreatedAt: nostr.Timestamp(delivery.ReceivedAt.Unix()), Kind: nostr.KindDeletion, Tags: nostr.Tags{{"e", reaction.ID.Hex()}, {"external-provider", forge.BackendGitHub}}}
			if err := deletion.Sign(secret); err != nil {
				return err
			}
			if err := policy.persistBridgeEvent(ctx, deletion); err != nil {
				return err
			}
		}
		if err := policy.markProviderReactionDeleted(ctx, mapping); err != nil {
			return err
		}
	}
	return nil
}
