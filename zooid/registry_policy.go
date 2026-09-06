package zooid

import (
	"encoding/json"
	"fmt"
	"math/big"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"fiatjaf.com/nostr"
)

const (
	registryReleaseTopic = "lapis-plugin-release"
	registryHeadTopic    = "lapis-plugin-registry-head"
)

type registryHeadRelease struct {
	ReleaseEventID  string `json:"releaseEventId"`
	ManifestSHA256  string `json:"manifestSha256"`
	Version         string `json:"version"`
	ReleaseSequence string `json:"releaseSequence,omitempty"`
	State           string `json:"state"`
	DecisionEventID string `json:"decisionEventId"`
}

type registryHeadContent struct {
	Schema               string                `json:"schema"`
	PluginID             string                `json:"pluginId"`
	AuthorityEpoch       string                `json:"authorityEpoch"`
	HeadRevision         string                `json:"headRevision"`
	PreviousHeadEventID  string                `json:"previousHeadEventId,omitempty"`
	ActiveReleaseEventID string                `json:"activeReleaseEventId,omitempty"`
	Releases             []registryHeadRelease `json:"releases"`
	IssuedAt             string                `json:"issuedAt"`
	ValidUntil           string                `json:"validUntil"`
}

type registryHeadReservation struct {
	revision *big.Int
	id       nostr.ID
}

// RegistryPolicy enforces Lapis release management at the relay boundary. It
// stores no Registry projection: signed events remain the source of truth.
type RegistryPolicy struct {
	Config *Config
	Groups *GroupStore
	Events *EventStore

	mu                 sync.Mutex
	reserved           map[string]registryHeadReservation
	findEventOverride  func(nostr.ID) (nostr.Event, bool)
	latestHeadOverride func(string) (*big.Int, nostr.ID, bool)
}

func NewRegistryPolicy(config *Config, groups *GroupStore, events *EventStore) *RegistryPolicy {
	return &RegistryPolicy{
		Config:   config,
		Groups:   groups,
		Events:   events,
		reserved: make(map[string]registryHeadReservation),
	}
}

// CheckWrite returns handled=true for Registry management events. Handled
// events bypass the generic write-restricted-group rule after this stricter
// role/ownership policy succeeds.
func (p *RegistryPolicy) CheckWrite(event nostr.Event) (handled bool, reason string) {
	if p == nil || !p.Config.Registry.Enabled || exactTag(event.Tags, "h") != p.Config.Registry.ReleaseGroupID {
		return false, ""
	}
	if event.Kind == nostr.KindReaction && isRegistryDecisionEnvelope(event) {
		return p.checkDecision(event)
	}
	if event.Kind == nostr.KindSimpleGroupChatMessage && hasExactTopic(event.Tags, registryHeadTopic) {
		return p.checkHead(event)
	}
	return false, ""
}

func isRegistryDecisionEnvelope(event nostr.Event) bool {
	if len(event.Tags) != 2 || slices.ContainsFunc(event.Tags, func(tag nostr.Tag) bool { return len(tag) != 2 }) || exactTag(event.Tags, "e") == "" {
		return false
	}
	switch strings.TrimSpace(event.Content) {
	case "✅", "❌", "🚫", "withdrawn":
		return true
	default:
		return false
	}
}

func (p *RegistryPolicy) checkDecision(event nostr.Event) (bool, string) {
	if len(event.Tags) != 2 || slices.ContainsFunc(event.Tags, func(tag nostr.Tag) bool { return len(tag) != 2 }) || exactTag(event.Tags, "e") == "" {
		return true, "invalid: registry decisions must target exactly one release"
	}
	target, err := nostr.IDFromHex(exactTag(event.Tags, "e"))
	if err != nil {
		return true, "invalid: registry decision target is invalid"
	}
	release, found := p.findEvent(target)
	if !found || !isRegistryRelease(release, p.Config.Registry.ReleaseGroupID) {
		return true, "invalid: registry decision target is not a registry release"
	}
	switch strings.TrimSpace(event.Content) {
	case "✅", "❌", "🚫":
		if !p.Groups.HasRole(p.Config.Registry.ReleaseGroupID, event.PubKey, p.Config.Registry.CuratorRole) {
			return true, "restricted: registry curator role is required"
		}
		return true, ""
	case "withdrawn":
		if release.PubKey != event.PubKey {
			return true, "restricted: publishers may withdraw only their own releases"
		}
		return true, ""
	default:
		return false, ""
	}
}

func (p *RegistryPolicy) checkHead(event nostr.Event) (bool, string) {
	if !p.Groups.HasRole(p.Config.Registry.ReleaseGroupID, event.PubKey, p.Config.Registry.CuratorRole) {
		return true, "restricted: registry curator role is required"
	}
	head, err := parseRegistryHead(event)
	if err != nil {
		return true, "invalid: " + err.Error()
	}
	if !hasExactTopic(event.Tags, "plugin:"+head.PluginID) || len(event.Tags) != 3 {
		return true, "invalid: registry head plugin topic does not match its content"
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	previousRevision, previousID, found := p.latestHead(head.PluginID)
	if reserved, ok := p.reserved[head.PluginID]; ok && (!found || reserved.revision.Cmp(previousRevision) > 0) {
		previousRevision = new(big.Int).Set(reserved.revision)
		previousID = reserved.id
		found = true
	}
	want := big.NewInt(1)
	if found {
		want = new(big.Int).Add(previousRevision, big.NewInt(1))
	}
	revision, ok := canonicalDecimal(head.HeadRevision, false)
	if !ok || revision.Cmp(want) != 0 {
		return true, fmt.Sprintf("restricted: registry head revision must be %s", want.String())
	}
	if (!found && head.PreviousHeadEventID != "") || (found && head.PreviousHeadEventID != previousID.Hex()) {
		return true, "restricted: registry head compare-and-swap failed"
	}
	p.reserved[head.PluginID] = registryHeadReservation{revision: revision, id: event.ID}
	return true, ""
}

func (p *RegistryPolicy) findEvent(id nostr.ID) (nostr.Event, bool) {
	if p.findEventOverride != nil {
		return p.findEventOverride(id)
	}
	if p.Events == nil {
		return nostr.Event{}, false
	}
	for event := range p.Events.QueryEvents(nostr.Filter{IDs: []nostr.ID{id}}, 1) {
		return event, true
	}
	return nostr.Event{}, false
}

func (p *RegistryPolicy) latestHead(pluginID string) (*big.Int, nostr.ID, bool) {
	if p.latestHeadOverride != nil {
		return p.latestHeadOverride(pluginID)
	}
	if p.Events == nil {
		return nil, nostr.ID{}, false
	}
	filter := nostr.Filter{
		Kinds: []nostr.Kind{nostr.KindSimpleGroupChatMessage},
		Tags:  nostr.TagMap{"h": {p.Config.Registry.ReleaseGroupID}, "t": {registryHeadTopic}},
		Limit: 100,
	}
	var best *big.Int
	var bestID nostr.ID
	for event := range p.Events.QueryEvents(filter, 100) {
		head, err := parseRegistryHead(event)
		if err != nil || head.PluginID != pluginID {
			continue
		}
		revision, ok := canonicalDecimal(head.HeadRevision, false)
		if !ok || (best != nil && revision.Cmp(best) <= 0) {
			continue
		}
		best = revision
		bestID = event.ID
	}
	return best, bestID, best != nil
}

func parseRegistryHead(event nostr.Event) (registryHeadContent, error) {
	var head registryHeadContent
	decoder := json.NewDecoder(strings.NewReader(event.Content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&head); err != nil {
		return head, fmt.Errorf("registry head JSON is invalid")
	}
	if decoder.More() || head.Schema != "lapis.registry.plugin-head/1" || head.PluginID == "" || head.AuthorityEpoch == "" {
		return head, fmt.Errorf("registry head envelope is invalid")
	}
	issuedAt, err := time.Parse(time.RFC3339Nano, head.IssuedAt)
	if err != nil || issuedAt.Unix() != int64(event.CreatedAt) {
		return head, fmt.Errorf("issuedAt must match the event timestamp")
	}
	validUntil, err := time.Parse(time.RFC3339Nano, head.ValidUntil)
	if err != nil || !validUntil.After(issuedAt) || validUntil.Sub(issuedAt) > 30*24*time.Hour {
		return head, fmt.Errorf("validUntil must be within 30 days of issuedAt")
	}
	if _, ok := canonicalDecimal(head.HeadRevision, false); !ok {
		return head, fmt.Errorf("headRevision must be a positive canonical decimal")
	}
	seenEvents := make(map[string]struct{})
	seenVersions := make(map[string]struct{})
	seenSequences := make(map[string]struct{})
	for _, release := range head.Releases {
		if !hexString(release.ReleaseEventID, 64) || !hexString(release.ManifestSHA256, 64) || !hexString(release.DecisionEventID, 64) || release.Version == "" || !slices.Contains([]string{"approved", "rejected", "revoked", "withdrawn"}, release.State) {
			return head, fmt.Errorf("registry head contains invalid release evidence")
		}
		if release.ReleaseSequence != "" {
			if _, ok := canonicalDecimal(release.ReleaseSequence, true); !ok {
				return head, fmt.Errorf("releaseSequence must be canonical")
			}
			if _, exists := seenSequences[release.ReleaseSequence]; exists {
				return head, fmt.Errorf("releaseSequence must be unique")
			}
			seenSequences[release.ReleaseSequence] = struct{}{}
		}
		if _, exists := seenEvents[release.ReleaseEventID]; exists {
			return head, fmt.Errorf("release event IDs must be unique")
		}
		if _, exists := seenVersions[release.Version]; exists {
			return head, fmt.Errorf("release versions must be unique")
		}
		seenEvents[release.ReleaseEventID] = struct{}{}
		seenVersions[release.Version] = struct{}{}
	}
	if !sort.SliceIsSorted(head.Releases, func(i, j int) bool {
		left, right := head.Releases[i], head.Releases[j]
		if left.ReleaseSequence == "" && right.ReleaseSequence != "" {
			return true
		}
		if left.ReleaseSequence != "" && right.ReleaseSequence == "" {
			return false
		}
		if left.ReleaseSequence != right.ReleaseSequence {
			leftNumber, _ := canonicalDecimal(left.ReleaseSequence, true)
			rightNumber, _ := canonicalDecimal(right.ReleaseSequence, true)
			return leftNumber.Cmp(rightNumber) < 0
		}
		return left.ReleaseEventID < right.ReleaseEventID
	}) {
		return head, fmt.Errorf("registry head releases are not canonically sorted")
	}
	if head.ActiveReleaseEventID != "" {
		active := false
		for _, release := range head.Releases {
			if release.ReleaseEventID == head.ActiveReleaseEventID && release.State == "approved" {
				active = true
			}
		}
		if !active {
			return head, fmt.Errorf("active release must be approved")
		}
	}
	return head, nil
}

func canonicalDecimal(value string, allowZero bool) (*big.Int, bool) {
	if value == "" || (len(value) > 1 && value[0] == '0') || (!allowZero && value == "0") {
		return nil, false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return nil, false
		}
	}
	number, ok := new(big.Int).SetString(value, 10)
	return number, ok
}

func exactTag(tags nostr.Tags, name string) string {
	value := ""
	count := 0
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == name {
			value, count = tag[1], count+1
		}
	}
	if count != 1 {
		return ""
	}
	return value
}

func hasExactTopic(tags nostr.Tags, topic string) bool {
	count := 0
	for _, tag := range tags {
		if len(tag) == 2 && tag[0] == "t" && tag[1] == topic {
			count++
		}
	}
	return count == 1
}

func isRegistryRelease(event nostr.Event, groupID string) bool {
	return event.Kind == nostr.KindSimpleGroupChatMessage && exactTag(event.Tags, "h") == groupID && hasExactTopic(event.Tags, registryReleaseTopic)
}

func hexString(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}
