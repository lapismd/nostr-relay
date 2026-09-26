package zooid

import (
	"context"
	"errors"
	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip29"
	"fmt"
	"slices"
	"zooid/buzzagents/protocol"
)

func (instance *Instance) validateCountVisibility(ctx context.Context, filter nostr.Filter) error {
	if containsAnyKind(filter.Kinds, protocol.KindRuntimeSnapshot, protocol.KindRuntimeRequest) {
		return errUnsafeCountFilter
	}
	for key, values := range filter.Tags {
		if len(key) != 1 || len(values) == 0 {
			return errUnsafeCountFilter
		}
	}

	if containsAnyKind(filter.Kinds,
		protocol.KindMemberAddedNotification,
		protocol.KindMemberRemovedNotification,
		protocol.KindAgentEngram,
		protocol.KindPrivateManagedAgent,
		protocol.KindAgentTurnMetric,
	) {
		if filter.Search != "" || len(filter.Kinds) == 0 {
			return errUnsafeCountFilter
		}
		for _, kind := range filter.Kinds {
			switch kind {
			case protocol.KindMemberAddedNotification, protocol.KindMemberRemovedNotification,
				protocol.KindAgentEngram, protocol.KindPrivateManagedAgent, protocol.KindAgentTurnMetric:
			default:
				return errUnsafeCountFilter
			}
		}
		pubkey, authenticated := authenticatedPubkey(ctx)
		if !authenticated {
			return errors.New("authentication is required for private counts")
		}
		pValues := filter.Tags["p"]
		if len(pValues) == 1 && pValues[0] == pubkey.Hex() {
			return nil
		}
		if len(filter.Authors) == 1 && filter.Authors[0] == pubkey {
			for _, kind := range filter.Kinds {
				if kind != protocol.KindAgentEngram && kind != protocol.KindPrivateManagedAgent {
					return errUnsafeCountFilter
				}
			}
			return nil
		}
		return errUnsafeCountFilter
	}

	if containsAnyKind(filter.Kinds, protocol.KindPersona) {
		pubkey, authenticated := authenticatedPubkey(ctx)
		if !authenticated {
			return errors.New("authentication is required for persona counts")
		}
		if instance.Agents == nil || !instance.Agents.CanReadPersonaFilter(pubkey, filter) {
			return errUnsafeCountFilter
		}
		return nil
	}

	groups := filter.Tags["h"]
	if len(groups) > 0 {
		if len(filter.Kinds) == 0 {
			return errUnsafeCountFilter
		}
		for _, kind := range filter.Kinds {
			if slices.Contains(nip29.MetadataEventKinds, kind) {
				return errUnsafeCountFilter
			}
		}
		pubkey, authenticated := authenticatedPubkey(ctx)
		for _, group := range groups {
			event := nostr.Event{Kind: nostr.KindSimpleGroupChatMessage, Tags: nostr.Tags{{"h", group}}}
			if !authenticated {
				if !instance.CanAnonymousRead(event) {
					return fmt.Errorf("group is not visible to this client")
				}
				continue
			}
			if !instance.Groups.CanRead(pubkey, event) {
				return fmt.Errorf("group is not visible to this client")
			}
		}
		return nil
	}

	// Without an explicit group scope, a filter can match both non-group and
	// private group events. Refuse it until that visibility union is represented
	// directly in SQL; never derive a count by downloading matching events.
	return errUnsafeCountFilter
}
