package zooid

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	agentauth "zooid/buzzagents/auth"
	"zooid/buzzagents/policy"
	"zooid/buzzagents/presence"
	"zooid/buzzagents/protocol"
	"zooid/internal/khatru"
)

const agentAuthFreshness = 120 * time.Second

type authenticatedPrincipal struct {
	PubKey     nostr.PubKey
	Owner      nostr.PubKey
	Virtual    bool
	Credential agentauth.Credential
}

type authenticatedPrincipalContextKey struct{}

type rateWindow struct {
	started time.Time
	count   int
}

// AgentPolicy is the Lapis adapter around the isolated Buzz protocol module.
// It owns only relay session/projection state; ACP and model execution stay in
// external harnesses.
type AgentPolicy struct {
	Config       *Config
	Management   *ManagementStore
	Groups       *GroupStore
	Events       *EventStore
	Presence     *presence.Store
	now          func() time.Time
	linkResolver func(agent, owner nostr.PubKey, personaID string) bool

	mu       sync.RWMutex
	sessions map[string]map[nostr.PubKey]authenticatedPrincipal
	owners   map[nostr.PubKey]nostr.PubKey
	rates    map[string]rateWindow
}

func NewAgentPolicy(config *Config, management *ManagementStore, groups *GroupStore) *AgentPolicy {
	return &AgentPolicy{
		Config: config, Management: management, Groups: groups,
		Presence: presence.NewStore(), now: time.Now,
		sessions: make(map[string]map[nostr.PubKey]authenticatedPrincipal),
		owners:   make(map[nostr.PubKey]nostr.PubKey), rates: make(map[string]rateWindow),
	}
}

func withAuthenticatedPrincipal(ctx context.Context, principal authenticatedPrincipal) context.Context {
	return context.WithValue(ctx, authenticatedPrincipalContextKey{}, principal)
}

func principalFromContext(ctx context.Context) (authenticatedPrincipal, bool) {
	principal, ok := ctx.Value(authenticatedPrincipalContextKey{}).(authenticatedPrincipal)
	return principal, ok
}

// OnAuth extends the verified NIP-42 flow with NIP-AA admission. Direct relay
// members follow the existing path. Virtual membership is connection-local.
func (agentPolicy *AgentPolicy) OnAuth(ctx context.Context, event nostr.Event) (bool, string) {
	if !agentPolicy.Config.Agents.Enabled || agentPolicy.Config.Policy.Open {
		return false, ""
	}
	if agentPolicy.Management.IsMember(event.PubKey) {
		return false, ""
	}
	if !agentPolicy.Config.Agents.AllowOwnerAuth {
		return true, "restricted: you are not a member of this relay"
	}
	if agentPolicy.Management.PubkeyIsBanned(event.PubKey) {
		return true, "restricted: agent is banned from this relay"
	}
	if !agentPolicy.authTimestampFresh(uint64(event.CreatedAt)) {
		return true, "invalid: AUTH event is outside the agent freshness window"
	}
	tag, err := agentauth.Extract(event.Tags)
	if errors.Is(err, agentauth.ErrMissingCredential) {
		// Preserve the relay's existing authenticate-then-request-membership flow.
		// Without a credential this identity is authenticated but remains a
		// non-member until the ordinary policy gate admits it.
		return false, ""
	}
	if err != nil {
		return true, "restricted: " + err.Error()
	}
	credential, err := agentauth.VerifyForAuthEvent(tag, event.PubKey, uint64(event.CreatedAt))
	if err != nil {
		return true, "restricted: invalid owner credential"
	}
	if !agentPolicy.ownerIsActive(credential.Owner) {
		return true, "restricted: owner is not an active relay member"
	}
	if !agentPolicy.rememberOwner(credential.Agent, credential.Owner) {
		return true, "restricted: agent is already bound to another owner"
	}
	connection := khatru.GetConnection(ctx)
	if connection == nil {
		return true, "restricted: agent authentication requires a WebSocket connection"
	}
	agentPolicy.mu.Lock()
	byPubkey := agentPolicy.sessions[connection.GetID()]
	if byPubkey == nil {
		byPubkey = make(map[nostr.PubKey]authenticatedPrincipal)
		agentPolicy.sessions[connection.GetID()] = byPubkey
	}
	byPubkey[event.PubKey] = authenticatedPrincipal{PubKey: event.PubKey, Owner: credential.Owner, Virtual: true, Credential: credential}
	agentPolicy.mu.Unlock()
	return false, ""
}

func (agentPolicy *AgentPolicy) OnDisconnect(ctx context.Context) {
	if connection := khatru.GetConnection(ctx); connection != nil {
		agentPolicy.mu.Lock()
		delete(agentPolicy.sessions, connection.GetID())
		agentPolicy.mu.Unlock()
	}
}

// AuthenticateHTTP applies the same direct/owner-derived relay membership gate
// to Buzz's NIP-98 bridge. signedAt comes from the verified NIP-98 event.
func (agentPolicy *AgentPolicy) AuthenticateHTTP(ctx context.Context, pubkey nostr.PubKey, signedAt uint64, authTag string) (context.Context, error) {
	principal := authenticatedPrincipal{PubKey: pubkey}
	if agentPolicy.Config.Policy.Open || agentPolicy.Management.IsMember(pubkey) {
		return withAuthenticatedPrincipal(ctx, principal), nil
	}
	if !agentPolicy.Config.Agents.Enabled || !agentPolicy.Config.Agents.AllowOwnerAuth {
		return nil, errors.New("relay membership is required")
	}
	if agentPolicy.Management.PubkeyIsBanned(pubkey) || !agentPolicy.authTimestampFresh(signedAt) {
		return nil, errors.New("agent authentication is not permitted")
	}
	if authTag == "" {
		return nil, errors.New("owner credential is required")
	}
	credential, err := agentauth.ParseJSON(authTag, pubkey)
	if err != nil || !credential.AllowsCreatedAt(signedAt) {
		return nil, errors.New("owner credential is invalid")
	}
	if !agentPolicy.ownerIsActive(credential.Owner) || !agentPolicy.rememberOwner(credential.Agent, credential.Owner) {
		return nil, errors.New("owner credential is not active")
	}
	principal = authenticatedPrincipal{PubKey: pubkey, Owner: credential.Owner, Virtual: true, Credential: credential}
	return withAuthenticatedPrincipal(ctx, principal), nil
}

func (agentPolicy *AgentPolicy) IsRelayMember(ctx context.Context, pubkey nostr.PubKey) bool {
	if agentPolicy.Config.Policy.Open || agentPolicy.Management.IsMember(pubkey) {
		return true
	}
	principal, ok := agentPolicy.principal(ctx, pubkey)
	return ok && principal.Virtual && agentPolicy.ownerIsActive(principal.Owner)
}

func (agentPolicy *AgentPolicy) CheckWrite(ctx context.Context, event nostr.Event) string {
	principal, virtual := agentPolicy.principal(ctx, event.PubKey)
	if virtual && agentPolicy.Config.Agents.EnforceCredentialKinds && !principal.Credential.AllowsKind(event.Kind) {
		return "restricted: owner credential does not allow this event kind"
	}

	if event.Kind == protocol.KindPrivateManagedAgent {
		return "invalid: kind 30179 is reserved until CAS and privacy enforcement are complete"
	}
	if err := protocol.ValidateDurableEnvelope(event); err != nil {
		return "invalid: " + err.Error()
	}

	if event.Kind == nostr.KindProfileMetadata {
		return ""
	}

	switch event.Kind {
	case protocol.KindPresence:
		if h, err := exactOptionalTag(event.Tags, "h"); err != nil {
			return "invalid: " + err.Error()
		} else if h != "" && !agentPolicy.Groups.IsMember(h, event.PubKey) {
			return "restricted: channel membership is required"
		}
	case protocol.KindTyping:
		h, err := exactRequiredTag(event.Tags, "h")
		if err != nil {
			return "invalid: " + err.Error()
		}
		if !agentPolicy.Groups.IsMember(h, event.PubKey) {
			return "restricted: channel membership is required"
		}
	case protocol.KindAgentObserverFrame:
		route, err := protocol.ValidateObserver(event, agentPolicy.now())
		if errors.Is(err, protocol.ErrUnknownObserverFrame) {
			return ""
		}
		if err != nil {
			return "invalid: " + err.Error()
		}
		if !agentPolicy.isOwner(route.Agent, route.Owner) {
			return "auth-required: verified agent ownership is required"
		}
		if route.GroupID != "" && (!agentPolicy.Groups.IsMember(route.GroupID, route.Agent) || !agentPolicy.Groups.IsMember(route.GroupID, route.Owner)) {
			return "restricted: agent and owner channel membership is required"
		}
		if route.Direction == protocol.ObserverTelemetry && !agentPolicy.allowRate(route.Agent, "observer", 100, time.Second) {
			return "rate-limited: observer frame rate exceeded"
		}
	case protocol.KindAgentTurnMetric:
		if reason := agentPolicy.validateOwnerScoped(event, true); reason != "" {
			return reason
		}
		if !agentPolicy.allowRate(event.PubKey, "metric", 60, time.Minute) {
			return "rate-limited: turn metric rate exceeded"
		}
	case protocol.KindAgentEngram:
		if reason := agentPolicy.validateOwnerScoped(event, false); reason != "" {
			return reason
		}
		if _, err := exactRequiredTag(event.Tags, "d"); err != nil {
			return "invalid: " + err.Error()
		}
	}
	return ""
}

func (agentPolicy *AgentPolicy) OnEphemeral(event nostr.Event) {
	if event.Kind == protocol.KindPresence {
		agentPolicy.Presence.Update(event, presence.ParseStatus(event.Content), agentPolicy.now())
	}
}

func (agentPolicy *AgentPolicy) PresenceEvents(filters []nostr.Filter) ([]nostr.Event, error) {
	authors := make([]nostr.PubKey, 0)
	seen := make(map[nostr.PubKey]struct{})
	for _, filter := range filters {
		for _, author := range filter.Authors {
			if _, duplicate := seen[author]; duplicate {
				continue
			}
			seen[author] = struct{}{}
			authors = append(authors, author)
		}
	}
	if len(authors) == 0 {
		return nil, nil
	}

	now := agentPolicy.now()
	entries := agentPolicy.Presence.Snapshot(authors, now)
	events := make([]nostr.Event, 0, len(entries))
	for _, entry := range entries {
		event := nostr.Event{
			Kind:      protocol.KindPresence,
			CreatedAt: nostr.Timestamp(now.Unix()),
			Content:   entry.Status,
			Tags:      nostr.Tags{{"p", entry.PubKey.Hex()}},
		}
		if err := agentPolicy.Config.Sign(&event); err != nil {
			return nil, fmt.Errorf("sign presence snapshot: %w", err)
		}
		events = append(events, event)
	}
	return events, nil
}

func (agentPolicy *AgentPolicy) CanRead(reader nostr.PubKey, event nostr.Event) bool {
	if event.Kind == protocol.KindPersona {
		if policy.CanReadPrivateEvent(reader, event) {
			return true
		}
		return agentPolicy.canReadLinkedPersona(reader, event)
	}
	if !policy.CanReadPrivateEvent(reader, event) {
		return false
	}
	if event.Kind == protocol.KindAgentObserverFrame {
		route, err := protocol.ValidateObserver(event, agentPolicy.now())
		return err == nil && agentPolicy.isOwner(route.Agent, route.Owner) && (reader == route.Agent || reader == route.Owner)
	}
	return true
}

// CanReadPersonaFilter reports whether an exact persona COUNT filter is safe
// for the authenticated reader. Broader filters stay rejected because their
// mixed private/shared visibility cannot be represented by the count SQL.
func (agentPolicy *AgentPolicy) CanReadPersonaFilter(reader nostr.PubKey, filter nostr.Filter) bool {
	if filter.Search != "" || len(filter.Kinds) != 1 || filter.Kinds[0] != protocol.KindPersona || len(filter.Authors) != 1 {
		return false
	}
	owner := filter.Authors[0]
	if owner == reader {
		return true
	}
	if shared := filter.Tags["shared"]; len(shared) == 1 && shared[0] == "true" {
		return true
	}
	personaIDs := filter.Tags["d"]
	return len(personaIDs) == 1 && agentPolicy.hasCurrentPersonaLink(reader, owner, personaIDs[0])
}

func (agentPolicy *AgentPolicy) canReadLinkedPersona(agent nostr.PubKey, persona nostr.Event) bool {
	personaID, err := exactRequiredTag(persona.Tags, "d")
	return err == nil && agentPolicy.hasCurrentPersonaLink(agent, persona.PubKey, personaID)
}

func (agentPolicy *AgentPolicy) hasCurrentPersonaLink(agent, owner nostr.PubKey, personaID string) bool {
	if protocol.ValidatePersonaSlug(personaID) != nil {
		return false
	}
	if agentPolicy.Events != nil {
		currentOwner, ok := agentPolicy.currentProfileOwner(agent)
		if !ok || currentOwner != owner {
			return false
		}
	} else if !agentPolicy.isOwner(agent, owner) {
		return false
	}
	if agentPolicy.linkResolver != nil {
		return agentPolicy.linkResolver(agent, owner, personaID)
	}
	if agentPolicy.Events == nil {
		return false
	}
	filter := nostr.Filter{
		Kinds:   []nostr.Kind{protocol.KindManagedAgent},
		Authors: []nostr.PubKey{owner},
		Tags:    nostr.TagMap{"d": []string{agent.Hex()}},
	}
	for event := range agentPolicy.Events.QueryEvents(filter, 1) {
		instance, err := protocol.ParseManagedAgent(event.Content)
		return err == nil && instance.PersonaID == personaID
	}
	return false
}

// currentProfileOwner keeps private persona access tied to the current public
// ownership proof. A connection-scoped NIP-AA credential can admit the agent
// to the relay, but it does not replace the agent-signed kind:0 authority.
func (agentPolicy *AgentPolicy) currentProfileOwner(agent nostr.PubKey) (nostr.PubKey, bool) {
	if agentPolicy.Events == nil {
		return nostr.PubKey{}, false
	}
	filter := nostr.Filter{
		Kinds:   []nostr.Kind{nostr.KindProfileMetadata},
		Authors: []nostr.PubKey{agent},
	}
	for event := range agentPolicy.Events.QueryEvents(filter, 1) {
		credential, err := agentauth.VerifyEvent(event)
		if err != nil || !agentPolicy.ownerIsActive(credential.Owner) {
			return nostr.PubKey{}, false
		}
		return credential.Owner, true
	}
	return nostr.PubKey{}, false
}

func (agentPolicy *AgentPolicy) IsKnownActiveAgent(agent nostr.PubKey) bool {
	agentPolicy.mu.RLock()
	owner, ok := agentPolicy.owners[agent]
	agentPolicy.mu.RUnlock()
	return ok && agentPolicy.ownerIsActive(owner)
}

func (agentPolicy *AgentPolicy) PreserveSignature(event nostr.Event) bool {
	switch event.Kind {
	case protocol.KindAgentProfile, protocol.KindAgentEngram, protocol.KindPersona,
		protocol.KindAgentTeam, protocol.KindManagedAgent, protocol.KindSharedTeamCatalog,
		protocol.KindMemberAddedNotification, protocol.KindMemberRemovedNotification,
		protocol.KindAgentTurnMetric:
		return true
	default:
		return false
	}
}

func (agentPolicy *AgentPolicy) RememberStored(event nostr.Event) {
	if event.Kind != nostr.KindProfileMetadata {
		return
	}
	credential, err := agentauth.VerifyEvent(event)
	agentPolicy.mu.Lock()
	defer agentPolicy.mu.Unlock()
	if err != nil {
		delete(agentPolicy.owners, event.PubKey)
		return
	}
	agentPolicy.owners[event.PubKey] = credential.Owner
}

// ForgetStored removes ownership derived from a deleted public profile. Persona
// links are read from the authoritative current 30177 head and need no cache.
func (agentPolicy *AgentPolicy) ForgetStored(event nostr.Event) {
	if event.Kind != nostr.KindProfileMetadata {
		return
	}
	agentPolicy.mu.Lock()
	delete(agentPolicy.owners, event.PubKey)
	agentPolicy.mu.Unlock()
}

func (agentPolicy *AgentPolicy) principal(ctx context.Context, pubkey nostr.PubKey) (authenticatedPrincipal, bool) {
	if principal, ok := principalFromContext(ctx); ok && principal.PubKey == pubkey {
		return principal, principal.Virtual
	}
	connection := khatru.GetConnection(ctx)
	if connection == nil {
		return authenticatedPrincipal{}, false
	}
	agentPolicy.mu.RLock()
	principal, ok := agentPolicy.sessions[connection.GetID()][pubkey]
	agentPolicy.mu.RUnlock()
	return principal, ok
}

func (agentPolicy *AgentPolicy) rememberOwner(agent, owner nostr.PubKey) bool {
	agentPolicy.mu.Lock()
	defer agentPolicy.mu.Unlock()
	if current, ok := agentPolicy.owners[agent]; ok {
		return current == owner
	}
	agentPolicy.owners[agent] = owner
	return true
}

func (agentPolicy *AgentPolicy) isOwner(agent, owner nostr.PubKey) bool {
	agentPolicy.mu.RLock()
	current, ok := agentPolicy.owners[agent]
	agentPolicy.mu.RUnlock()
	return ok && current == owner && agentPolicy.ownerIsActive(owner)
}

func (agentPolicy *AgentPolicy) ownerIsActive(owner nostr.PubKey) bool {
	return agentPolicy.Management.IsMember(owner) && !agentPolicy.Management.PubkeyIsBanned(owner)
}

func (agentPolicy *AgentPolicy) authTimestampFresh(createdAt uint64) bool {
	created := time.Unix(int64(createdAt), 0)
	now := agentPolicy.now()
	return !created.Before(now.Add(-agentAuthFreshness)) && !created.After(now.Add(agentAuthFreshness))
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

func (agentPolicy *AgentPolicy) allowRate(pubkey nostr.PubKey, class string, maximum int, period time.Duration) bool {
	key := class + ":" + pubkey.Hex()
	now := agentPolicy.now()
	agentPolicy.mu.Lock()
	defer agentPolicy.mu.Unlock()
	window := agentPolicy.rates[key]
	if window.started.IsZero() || now.Sub(window.started) >= period {
		window = rateWindow{started: now}
	}
	if window.count >= maximum {
		return false
	}
	window.count++
	agentPolicy.rates[key] = window
	return true
}

func exactRequiredTag(tags nostr.Tags, name string) (string, error) {
	value, err := exactOptionalTag(tags, name)
	if err != nil {
		return "", err
	}
	if value == "" {
		return "", fmt.Errorf("event is missing %s tag", name)
	}
	return value, nil
}

func exactOptionalTag(tags nostr.Tags, name string) (string, error) {
	var value string
	count := 0
	for _, tag := range tags {
		if len(tag) == 0 || tag[0] != name {
			continue
		}
		if len(tag) != 2 || tag[1] == "" {
			return "", fmt.Errorf("%s tag must contain exactly one value", name)
		}
		count++
		value = tag[1]
	}
	if count > 1 {
		return "", fmt.Errorf("event has multiple %s tags", name)
	}
	return value, nil
}

// membershipNotificationContent matches the unmodified Buzz Desktop schema.
func membershipNotificationContent(added bool, channel string, actor nostr.PubKey) string {
	eventType := "member_removed"
	if added {
		eventType = "member_added"
	}
	content, _ := json.Marshal(map[string]string{"type": eventType, "channel_id": channel, "actor": actor.Hex()})
	return string(content)
}
