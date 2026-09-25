package agentbunker

import (
	"context"
	"encoding/json"
	"fiatjaf.com/nostr"
	"slices"
)

func validJSONObject(value string) bool {
	var object map[string]any
	return json.Unmarshal([]byte(value), &object) == nil
}

func (s *Service) validActivation(a Agent, input activationRequest) bool {
	return validSigned(input.OwnerEvent) && input.OwnerEvent.Kind == 30177 && input.OwnerEvent.PubKey.Hex() == a.OwnerPubkey && exactTag(input.OwnerEvent.Tags, "d") == a.Pubkey && validSigned(input.ProfileEvent) && input.ProfileEvent.PubKey.Hex() == a.Pubkey && templateMatches(input.ProfileEvent, s.profileTemplate(a))
}

func validUnlink(event nostr.Event, a Agent) bool {
	return validSigned(event) && event.Kind == nostr.KindDeletion && event.PubKey.Hex() == a.OwnerPubkey && slices.ContainsFunc(event.Tags, func(t nostr.Tag) bool {
		return len(t) == 2 && t[0] == "a" && t[1] == "30177:"+a.OwnerPubkey+":"+a.Pubkey
	})
}

func validSigned(e nostr.Event) bool { return e.CheckID() && e.VerifySignature() }

func exactTag(tags nostr.Tags, name string) string {
	value := ""
	count := 0
	for _, tag := range tags {
		if len(tag) > 0 && tag[0] == name {
			if len(tag) != 2 {
				return ""
			}
			value = tag[1]
			count++
		}
	}
	if count != 1 {
		return ""
	}
	return value
}

func templateMatches(e nostr.Event, t eventTemplate) bool {
	return int(e.Kind) == t.Kind && int64(e.CreatedAt) == t.CreatedAt && e.Content == t.Content && equalTags(e.Tags, t.Tags)
}

func equalTags(a nostr.Tags, b [][]string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !slices.Equal([]string(a[i]), b[i]) {
			return false
		}
	}
	return true
}

func (s *Service) audit(ctx context.Context, a Audit) { _ = s.Store.Audit(ctx, a) }

func (s *Service) VerifyActiveOwnership(ctx context.Context, agent, owner, relay nostr.PubKey) bool {
	a, err := s.Store.Agent(ctx, agent.Hex())
	if err != nil || a.Status != StatusActive || a.OwnerPubkey != owner.Hex() || a.RelayPubkey != relay.Hex() {
		return false
	}
	ownerEvent, profileEvent := a.OwnerEvent, a.ProfileEvent
	if s.CurrentEvent != nil {
		ownerEvent = s.CurrentEvent(30177, owner, agent.Hex())
		profileEvent = s.CurrentEvent(nostr.KindProfileMetadata, agent, "")
	}
	return ownerEvent != nil && profileEvent != nil && s.validActivation(a, activationRequest{OwnerEvent: *ownerEvent, ProfileEvent: *profileEvent})
}
