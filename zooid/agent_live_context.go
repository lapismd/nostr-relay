package zooid

import (
	"encoding/json"
	"fiatjaf.com/nostr"
	"zooid/internal/projecttask"
)

const repositoryLiveContextKind nostr.Kind = 23908

func (policy *AgentPolicy) liveContextGroup(event nostr.Event) (string, bool) {
	group, err := exactRequiredTag(event.Tags, "h")
	if err != nil || policy.Events == nil || len(event.Content) > 48*1024 {
		return "", false
	}
	address, err := exactRequiredTag(event.Tags, "a")
	if err != nil {
		return "", false
	}
	coordinate, err := projecttask.ParseRepositoryCoordinate(address)
	if err != nil {
		return "", false
	}
	scope, scopeErr := exactRequiredTag(event.Tags, "r")
	span, spanErr := exactRequiredTag(event.Tags, "d")
	var body struct {
		Schema   string            `json:"schema"`
		Version  int               `json:"version"`
		Scope    string            `json:"scope_id"`
		Span     string            `json:"span_id"`
		Sequence *uint64           `json:"sequence"`
		State    string            `json:"state"`
		Records  []json.RawMessage `json:"records"`
	}
	if scopeErr != nil || spanErr != nil || json.Unmarshal([]byte(event.Content), &body) != nil ||
		body.Schema != "jj-sync/agent-context-live" || body.Version != 2 || body.Scope != scope || body.Span != span ||
		body.Sequence == nil || *body.Sequence > 9007199254740991 || body.Records == nil || len(body.Records) > 1 {
		return "", false
	}
	switch body.State {
	case "running", "completed", "failed", "cancelled":
	default:
		return "", false
	}
	filter := nostr.Filter{Kinds: []nostr.Kind{nostr.KindRepositoryAnnouncement}, Authors: []nostr.PubKey{coordinate.Owner}, Tags: nostr.TagMap{"d": []string{coordinate.ID}}}
	for repository := range policy.Events.QueryEvents(filter, 1) {
		channel, channelErr := exactOptionalTag(repository.Tags, "buzz-channel")
		h, hErr := exactOptionalTag(repository.Tags, "h")
		if channelErr != nil || hErr != nil || (h != "" && channel != "" && h != channel) {
			return "", false
		}
		if channel == "" {
			channel = h
		}
		return group, channel == group
	}
	return "", false
}
