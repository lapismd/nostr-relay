package zooid

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore"
	"fmt"
	"net/http"
	"strings"
	"time"
	"zooid/internal/forge"
	"zooid/internal/githubforge"
	"zooid/internal/projecttask"
)

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
