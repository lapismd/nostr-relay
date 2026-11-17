package zooid

import (
	"context"
	"encoding/json"
	"fiatjaf.com/nostr"
	"net/http"
	"strings"
	"unicode/utf8"
	"zooid/internal/forge"
	"zooid/internal/projecttask"
)

func (policy *GitForgePolicy) projectForgeOperation(ctx context.Context, repository forge.Repository, operation forgeOperation, result forge.CollaborationResult) error {
	if operation.SourceEventID != "" {
		source, found, err := policy.eventByHexID(ctx, operation.SourceEventID)
		if err != nil || !found {
			return &forge.Error{Status: http.StatusConflict, Code: "source_event_not_found"}
		}
		switch operation.Command {
		case "create_issue", "create_pull_request":
			return policy.saveForgeMapping(ctx, forgeObjectMapping{
				Provider: operation.Provider, ConnectionID: operation.ConnectionID,
				Coordinate: operation.Coordinate, ObjectType: operation.ObjectType,
				NostrEventID: source.ID.Hex(), NostrRootID: source.ID.Hex(),
				ExternalID: result.ExternalID, ExternalNumber: result.ExternalNumber,
				Origin: "relay", Authority: "relay", ExternalURL: result.ExternalURL,
				CorrelationMarker: operation.CorrelationMarker,
			})
		case "comment", "review", "review_comment":
			objectType := "comment"
			if operation.Command == "review" {
				objectType = "review"
			}
			return policy.saveForgeMapping(ctx, forgeObjectMapping{
				Provider: operation.Provider, ConnectionID: operation.ConnectionID,
				Coordinate: operation.Coordinate, ObjectType: objectType,
				NostrEventID: source.ID.Hex(), NostrRootID: operation.RootEventID,
				ExternalID: result.ExternalID, ExternalNumber: result.ExternalNumber,
				Origin: "relay", Authority: "relay", ExternalURL: result.ExternalURL,
				CorrelationMarker: operation.CorrelationMarker,
			})
		case "reaction_add":
			return policy.saveForgeMapping(ctx, forgeObjectMapping{
				Provider: operation.Provider, ConnectionID: operation.ConnectionID,
				Coordinate: operation.Coordinate, ObjectType: "reaction",
				NostrEventID: source.ID.Hex(), NostrRootID: operation.RootEventID,
				ExternalID: result.ExternalID, Origin: "relay", Authority: "relay",
			})
		case "reaction_delete":
			return nil
		case "state":
			return nil
		}
	}
	connection, found, err := policy.LoadGitHubConnection(ctx, operation.ConnectionID)
	if err != nil || !found {
		if err == nil {
			err = &forge.Error{Status: http.StatusServiceUnavailable, Code: "github_connection_unavailable", Retryable: true}
		}
		return err
	}
	secret, err := policy.bridgeSecret(connection)
	if err != nil {
		return err
	}
	var payload map[string]any
	if err := json.Unmarshal(operation.Payload, &payload); err != nil {
		return &forge.Error{Status: http.StatusBadRequest, Code: "invalid_forge_command"}
	}
	createdAt := nostr.Timestamp(operation.CreatedAt.Unix())
	provenance := externalProvenanceTags(result, payload)
	var event nostr.Event
	mapping := forgeObjectMapping{
		Provider: operation.Provider, ConnectionID: operation.ConnectionID,
		Coordinate: operation.Coordinate, ObjectType: operation.ObjectType,
		ExternalID: result.ExternalID, ExternalNumber: result.ExternalNumber,
		Origin: "provider", Authority: "provider", ExternalURL: result.ExternalURL,
		CorrelationMarker: operation.CorrelationMarker,
	}
	switch operation.Command {
	case "create_issue":
		event = nostr.Event{CreatedAt: createdAt, Kind: nostr.KindIssue, Content: stringValue(payload["body"]), Tags: nostr.Tags{
			{"a", operation.Coordinate.String()}, {"p", operation.Coordinate.Owner.Hex()},
			{"subject", stringValue(payload["title"])},
		}}
		event.Tags = append(event.Tags, provenance...)
	case "create_pull_request":
		headSHA := strings.ToLower(result.HeadSHA)
		if !fullForgeObjectID(headSHA) {
			headSHA = strings.ToLower(stringValue(payload["head_sha"]))
		}
		if !fullForgeObjectID(headSHA) {
			return &forge.Error{Status: http.StatusBadGateway, Code: "github_pr_head_missing", Retryable: true}
		}
		clone, err := policy.Registry.Resolve(ctx, repository)
		if err != nil || !clone.Capabilities.Clone.Available {
			return &forge.Error{Status: http.StatusConflict, Code: "clone_unavailable"}
		}
		event = nostr.Event{CreatedAt: createdAt, Kind: projecttask.KindPullRequest, Content: stringValue(payload["body"]), Tags: nostr.Tags{
			{"a", operation.Coordinate.String()}, {"p", operation.Coordinate.Owner.Hex()},
			{"subject", stringValue(payload["title"])}, {"c", headSHA},
			{"clone", clone.Capabilities.Clone.URL},
		}}
		if baseSHA := strings.ToLower(result.BaseSHA); fullForgeObjectID(baseSHA) {
			event.Tags = append(event.Tags, nostr.Tag{"base-commit", baseSHA})
		}
		if ref := firstNonempty(result.HeadRef, stringValue(payload["head"])); ref != "" {
			event.Tags = append(event.Tags, nostr.Tag{"branch-name", ref})
		}
		if ref := firstNonempty(result.BaseRef, stringValue(payload["base"])); ref != "" {
			event.Tags = append(event.Tags, nostr.Tag{"target-branch", ref})
		}
		if boolValue(payload["draft"]) {
			event.Tags = append(event.Tags, nostr.Tag{"t", "draft"})
		}
		event.Tags = append(event.Tags, provenance...)
	case "comment":
		root, found, err := policy.eventByHexID(ctx, operation.RootEventID)
		if err != nil || !found {
			return &forge.Error{Status: http.StatusConflict, Code: "work_item_not_found"}
		}
		event = nostr.Event{CreatedAt: createdAt, Kind: nostr.KindTextNote, Content: stringValue(payload["body"]), Tags: nostr.Tags{
			{"e", root.ID.Hex(), "", "root"}, {"a", operation.Coordinate.String()}, {"p", root.PubKey.Hex()},
		}}
		event.Tags = append(event.Tags, provenance...)
		mapping.NostrRootID = root.ID.Hex()
	case "state", "merge":
		root, found, err := policy.eventByHexID(ctx, operation.RootEventID)
		if err != nil || !found {
			return &forge.Error{Status: http.StatusConflict, Code: "work_item_not_found"}
		}
		rootMapping, found, err := policy.loadMappingByNostrRoot(ctx, operation.Provider, operation.ConnectionID, root.ID.Hex())
		if err != nil || !found {
			return &forge.Error{Status: http.StatusConflict, Code: "forge_mapping_missing"}
		}
		state := stringValue(payload["state"])
		if operation.Command == "merge" {
			state = "merged"
		}
		provenanceResult := result
		provenanceResult.ExternalID = rootMapping.ExternalID
		provenanceResult.ExternalNumber = rootMapping.ExternalNumber
		provenanceResult.ExternalURL = rootMapping.ExternalURL
		if rootMapping.Authority == "provider" && root.PubKey == secret.Public() {
			kind := forgeStatusKind(state)
			if kind == 0 {
				return &forge.Error{Status: http.StatusBadRequest, Code: "invalid_forge_state"}
			}
			event = nostr.Event{CreatedAt: createdAt, Kind: kind, Tags: nostr.Tags{
				{"e", root.ID.Hex(), "", "root"}, {"a", operation.Coordinate.String()},
			}}
			if operation.Command == "merge" && fullForgeObjectID(result.MergedCommit) {
				event.Tags = append(event.Tags, nostr.Tag{"merge-commit", strings.ToLower(result.MergedCommit)}, nostr.Tag{"r", strings.ToLower(result.MergedCommit)})
			}
		} else {
			event = nostr.Event{CreatedAt: createdAt, Kind: nostr.KindTextNote,
				Content: "GitHub state changed to " + state,
				Tags:    nostr.Tags{{"e", root.ID.Hex(), "", "root"}, {"a", operation.Coordinate.String()}, {"t", "external-state"}},
			}
		}
		event.Tags = append(event.Tags, externalProvenanceTags(provenanceResult, payload)...)
		mapping = forgeObjectMapping{}
	default:
		return &forge.Error{Status: http.StatusBadRequest, Code: "unsupported_forge_command"}
	}
	if err := event.Sign(secret); err != nil {
		return &forge.Error{Status: http.StatusServiceUnavailable, Code: "bridge_signing_failed", Retryable: true}
	}
	if err := policy.persistBridgeEvent(ctx, event); err != nil {
		return err
	}
	if mapping.Provider != "" {
		mapping.NostrEventID = event.ID.Hex()
		if mapping.NostrRootID == "" {
			mapping.NostrRootID = event.ID.Hex()
		}
		return policy.saveForgeMapping(ctx, mapping)
	}
	return nil
}

func forgeStatusKind(state string) nostr.Kind {
	switch strings.ToLower(state) {
	case "open", "reopened", "ready":
		return nostr.KindStatusOpen
	case "merged", "completed":
		return nostr.KindStatusApplied
	case "closed", "not_planned":
		return nostr.KindStatusClosed
	case "draft":
		return nostr.KindStatusDraft
	default:
		return 0
	}
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func boolValue(value any) bool {
	result, _ := value.(bool)
	return result
}

func firstNonempty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func fullForgeObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') || (character >= 'A' && character <= 'F')) {
			return false
		}
	}
	return true
}

func validForgeTitle(value string) bool {
	return value != "" && utf8.ValidString(value) && len(value) <= 256
}
