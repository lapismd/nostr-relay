package zooid

import (
	"fiatjaf.com/nostr"
	"time"
	"zooid/buzzagents/protocol"
)

func (agentPolicy *AgentPolicy) runtimeWriteReason(event nostr.Event) string {
	route, err := protocol.ValidateRuntimeEnvelope(event)
	if err != nil {
		return "invalid: " + err.Error()
	}
	if !agentPolicy.isOwner(route.Agent, route.Owner) {
		return "auth-required: verified agent ownership is required"
	}
	if !agentPolicy.allowRate(event.PubKey, "runtime", 60, time.Minute) {
		return "rate-limited: runtime event rate exceeded"
	}
	return ""
}
func (agentPolicy *AgentPolicy) canReadRuntime(reader nostr.PubKey, event nostr.Event) bool {
	route, err := protocol.ValidateRuntimeEnvelope(event)
	return err == nil && agentPolicy.isOwner(route.Agent, route.Owner) && (reader == route.Agent || reader == route.Owner)
}
func (agentPolicy *AgentPolicy) validateOwnerScoped(event nostr.Event, requireAgentTag bool) string {
	agent := event.PubKey
	if requireAgentTag {
		agentRaw, err := exactRequiredTag(event.Tags, "agent")
		if err != nil {
			return "invalid: " + err.Error()
		}
		parsed, err := nostr.PubKeyFromHex(agentRaw)
		if err != nil || parsed != event.PubKey {
			return "invalid: agent tag must equal the event author"
		}
	}
	ownerRaw, err := exactRequiredTag(event.Tags, "p")
	if err != nil {
		return "invalid: " + err.Error()
	}
	owner, err := nostr.PubKeyFromHex(ownerRaw)
	if err != nil || !agentPolicy.isOwner(agent, owner) {
		return "auth-required: verified agent ownership is required"
	}
	if err := protocol.ValidateNIP44V2(event.Content); err != nil {
		return "invalid: content " + err.Error()
	}
	return ""
}
