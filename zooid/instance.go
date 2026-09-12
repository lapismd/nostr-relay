package zooid

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"log"
	"net/http"
	"os"
	"reflect"
	"slices"
	"strings"
	"time"
	"unsafe"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore"
	"github.com/fasthttp/websocket"
	"github.com/gosimple/slug"
	"zooid/buzzagents/protocol"
	authn "zooid/internal/auth"
	"zooid/internal/forge"
	"zooid/internal/gitforge"
	"zooid/internal/githubforge"
	"zooid/internal/khatru"
)

type Instance struct {
	// Ctx is the service-level root context, propagated from main via
	// signal.NotifyContext. Stored here so per-call DB timeouts in
	// EventStore/etc. can derive from it instead of context.Background().
	// Cancellation flows through on SIGTERM, aborting in-flight work.
	Ctx          context.Context
	Relay        *khatru.Relay
	Config       *Config
	Events       *EventStore
	Blossom      *BlossomStore
	Management   *ManagementStore
	Groups       *GroupStore
	Registry     *RegistryPolicy
	ProjectTasks *ProjectTaskPolicy
	GitForge     *GitForgePolicy
	Agents       *AgentPolicy
	Auth         *authn.Service
}

func MakeInstance(ctx context.Context, filename string) (*Instance, error) {
	config, err := LoadConfig(filename)
	if err != nil {
		return nil, err
	}

	relay := khatru.NewRelay()

	// Enable WebSocket per-message compression (permessage-deflate).
	// The upgrader field is unexported, so we use reflect/unsafe to set it.
	rv := reflect.ValueOf(relay).Elem()
	field := rv.FieldByName("upgrader")
	upgrader := (*websocket.Upgrader)(unsafe.Pointer(field.UnsafeAddr()))
	upgrader.EnableCompression = true

	events := &EventStore{
		Relay:  relay,
		Config: config,
		Schema: &Schema{
			Name: slug.Make(config.Schema),
		},
		rootCtx: ctx,
	}

	blossom := &BlossomStore{
		Config: config,
		Events: events,
	}

	management := &ManagementStore{
		Config: config,
		Events: events,
	}

	debounceMs := envInt("GROUP_REWRITE_DEBOUNCE_MS", 200)
	if debounceMs < 0 {
		debounceMs = 0
	}
	groups := &GroupStore{
		Config:        config,
		Events:        events,
		Management:    management,
		DebounceDelay: time.Duration(debounceMs) * time.Millisecond,
	}

	instance := &Instance{
		Ctx:        ctx,
		Relay:      relay,
		Config:     config,
		Events:     events,
		Blossom:    blossom,
		Management: management,
		Groups:     groups,
	}
	instance.Registry = NewRegistryPolicy(config, groups, events)
	instance.ProjectTasks = NewProjectTaskPolicy(events, groups)
	instance.Agents = NewAgentPolicy(config, management, groups)
	instance.Agents.Events = events

	// NIP 11 info

	self := config.GetSelf()
	owner := config.GetOwner()

	instance.Relay.Negentropy = true
	instance.Relay.Info.Name = config.Info.Name
	instance.Relay.Info.Icon = config.Info.Icon
	instance.Relay.Info.Self = &self
	instance.Relay.Info.PubKey = &owner
	instance.Relay.Info.Description = config.Info.Description
	instance.Relay.Info.Software = "https://github.com/coracle-social/zooid"
	instance.Relay.Info.Version = "v0.1.0"
	instance.Relay.Info.SupportedNIPs = append(instance.Relay.Info.SupportedNIPs, 43)

	// Handlers

	instance.Relay.OnConnect = instance.OnConnect
	instance.Relay.OnDisconnect = instance.Agents.OnDisconnect
	instance.Relay.OnAuth = instance.Agents.OnAuth
	instance.Relay.PreventBroadcast = instance.PreventBroadcast
	instance.Relay.StoreEvent = instance.StoreEvent
	instance.Relay.ReplaceEvent = instance.ReplaceEvent
	instance.Relay.DeleteEvent = instance.DeleteEvent
	instance.Relay.OnRequest = instance.OnRequest
	instance.Relay.OnCountMany = instance.OnCountMany
	instance.Relay.QueryStored = instance.QueryStored
	instance.Relay.CountMany = instance.CountStored
	instance.Relay.OnEvent = instance.OnEvent
	instance.Relay.OnEventSaved = instance.OnEventSaved
	instance.Relay.OnEventDeleted = instance.OnEventDeleted
	instance.Relay.OnEphemeralEvent = instance.OnEphemeralEvent

	// Todo: when there's a new version of khatru
	// instance.Relay.StartExpirationManager()

	// HTTP request handling

	router := instance.Relay.Router()

	router.HandleFunc("/{$}", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "templates/index.html")
	})

	router.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))

	// Initialize the database

	if err := instance.Events.Init(); err != nil {
		log.Fatal("Failed to initialize event store: ", err)
	}

	if config.Auth.Enabled {
		credentials := githubforge.RuntimeCredentials{SecretDir: config.Auth.CredentialDir}
		masterValue, err := credentials.Load(config.Auth.MasterKeyRef)
		if err != nil {
			instance.Events.Close()
			return nil, fmt.Errorf("load auth master key: %w", err)
		}
		masterKey, err := authn.ParseMasterKey(masterValue)
		clear(masterValue)
		if err != nil {
			instance.Events.Close()
			return nil, err
		}
		wrapper, err := authn.NewAESKeyWrapper(config.Auth.MasterKeyVersion, masterKey)
		clear(masterKey)
		if err != nil {
			instance.Events.Close()
			return nil, err
		}
		store, err := authn.NewSQLStore(GetDb(), events.Schema.Name)
		if err != nil {
			instance.Events.Close()
			return nil, err
		}
		providers := make([]authn.OAuthProvider, 0, 2)
		if config.Auth.GitHub.ClientID != "" {
			secret, err := credentials.Load(config.Auth.GitHub.ClientSecretRef)
			if err != nil {
				instance.Events.Close()
				return nil, fmt.Errorf("load GitHub OAuth secret: %w", err)
			}
			var provider *authn.GitHubProvider
			var providerErr error
			if config.Auth.Testing.GitHubAuthorizationURL != "" {
				provider, providerErr = authn.NewGitHubProviderWithEndpoints(config.Auth.GitHub.ClientID, strings.TrimSpace(string(secret)), strings.TrimRight(config.Auth.PublicURL, "/")+"/api/auth/callback/github", authn.GitHubProviderEndpoints{
					AuthorizationURL: config.Auth.Testing.GitHubAuthorizationURL,
					TokenURL:         config.Auth.Testing.GitHubTokenURL,
					UserURL:          config.Auth.Testing.GitHubUserURL,
				}, nil)
			} else {
				provider, providerErr = authn.NewGitHubProvider(config.Auth.GitHub.ClientID, strings.TrimSpace(string(secret)), strings.TrimRight(config.Auth.PublicURL, "/")+"/api/auth/callback/github", nil)
			}
			clear(secret)
			if providerErr != nil {
				instance.Events.Close()
				return nil, providerErr
			}
			providers = append(providers, provider)
		}
		if config.Auth.Google.ClientID != "" {
			secret, err := credentials.Load(config.Auth.Google.ClientSecretRef)
			if err != nil {
				instance.Events.Close()
				return nil, fmt.Errorf("load Google OAuth secret: %w", err)
			}
			var provider *authn.GoogleProvider
			var providerErr error
			if config.Auth.Testing.GoogleIssuerURL != "" {
				provider, providerErr = authn.NewGoogleProviderWithIssuer(ctx, config.Auth.Google.ClientID, strings.TrimSpace(string(secret)), strings.TrimRight(config.Auth.PublicURL, "/")+"/api/auth/callback/google", config.Auth.Testing.GoogleIssuerURL, nil)
			} else {
				provider, providerErr = authn.NewGoogleProvider(ctx, config.Auth.Google.ClientID, strings.TrimSpace(string(secret)), strings.TrimRight(config.Auth.PublicURL, "/")+"/api/auth/callback/google")
			}
			clear(secret)
			if providerErr != nil {
				instance.Events.Close()
				return nil, providerErr
			}
			providers = append(providers, provider)
		}
		instance.Auth, err = authn.NewService(authn.ServiceConfig{
			PublicURL: config.Auth.PublicURL, SessionLifetime: config.AuthSessionLifetime(),
			SessionIdle: config.AuthSessionIdle(), ChallengeTTL: config.AuthChallengeTTL(),
			GitHubClientID: config.Auth.GitHub.ClientID, AllowedBrowserOrigins: config.Auth.AllowedBrowserOrigins,
		}, store, wrapper, providers...)
		if err != nil {
			instance.Events.Close()
			return nil, err
		}
	}

	// Warm caches

	instance.Management.WarmCaches()
	instance.Groups.WarmCaches()
	for event := range instance.Events.QueryEvents(nostr.Filter{Kinds: []nostr.Kind{nostr.KindProfileMetadata}}, 0) {
		instance.Agents.RememberStored(event)
	}

	if config.Git.Enabled {
		forgeRegistry := forge.NewRegistry()
		instance.GitForge = NewGitForgePolicyWithRegistry(ctx, config, events, groups, instance.ProjectTasks, forgeRegistry)
		if instance.Auth != nil {
			instance.GitForge.UserCredentialMode = instance.Auth.GitHubCredentialMode
		}
		if strings.TrimSpace(config.Git.BackendURL) != "" {
			serviceToken := strings.TrimSpace(os.Getenv("WALGIT_RELAY_TOKEN"))
			webhookSecret := strings.TrimSpace(os.Getenv("WALGIT_WEBHOOK_SECRET"))
			if serviceToken == "" {
				instance.Events.Close()
				return nil, fmt.Errorf("WALGIT_RELAY_TOKEN is required when the WalGit backend is enabled")
			}
			if webhookSecret == "" {
				instance.Events.Close()
				return nil, fmt.Errorf("WALGIT_WEBHOOK_SECRET is required when the WalGit backend is enabled")
			}
			backend, err := gitforge.NewWalGitBackend(config.Git.BackendURL, serviceToken)
			if err != nil {
				instance.Events.Close()
				return nil, err
			}
			if err := forgeRegistry.RegisterGit(gitforge.NewWalGitAdapter(backend)); err != nil {
				instance.Events.Close()
				return nil, err
			}
			instance.GitForge.WebhookSecret = webhookSecret
		}
		if config.GitHub.Enabled {
			credentials := githubforge.RuntimeCredentials{SecretDir: config.GitHub.CredentialDir}
			backend, err := githubforge.New(instance.GitForge, credentials, nil)
			if err != nil {
				instance.Events.Close()
				return nil, err
			}
			if instance.Auth != nil {
				backend.SetUserTokenResolver(instance.Auth)
			}
			if err := forgeRegistry.RegisterGit(backend); err != nil {
				instance.Events.Close()
				return nil, err
			}
			if err := forgeRegistry.RegisterCollaboration(backend); err != nil {
				instance.Events.Close()
				return nil, err
			}
			if err := forgeRegistry.RegisterMirror(backend); err != nil {
				instance.Events.Close()
				return nil, err
			}
			if err := forgeRegistry.RegisterGitReplicationTarget(backend); err != nil {
				instance.Events.Close()
				return nil, err
			}
			if err := forgeRegistry.RegisterCI(backend); err != nil {
				instance.Events.Close()
				return nil, err
			}
			instance.GitForge.GitHub = backend
			instance.GitForge.Credentials = credentials
			instance.GitForge.EventSource = backend
		}
		instance.GitForge.PersistEvent = func(persistCtx context.Context, event nostr.Event) error {
			if err := instance.Events.SaveEvent(event); err != nil && !errors.Is(err, eventstore.ErrDupEvent) {
				return err
			}
			if err := instance.ProjectTasks.Remember(persistCtx, event); err != nil {
				return err
			}
			if err := instance.GitForge.Remember(persistCtx, event); err != nil {
				return err
			}
			instance.Relay.BroadcastEvent(event)
			return nil
		}
		if err := instance.GitForge.Start(); err != nil {
			instance.GitForge.Close()
			instance.Events.Close()
			return nil, fmt.Errorf("start Git repository reconciliation: %w", err)
		}
	}

	// Enable extra functionality

	if config.Blossom.Enabled {
		instance.Blossom.Enable(instance)
	}

	if config.Management.Enabled {
		instance.Management.Enable(instance)
	}

	if config.Groups.Enabled {
		instance.Groups.Enable(instance)
		// Publish relay-level admin list (d tag = "_" for relay admins)
		// This allows clients to query for relay admins via GROUP_ADMINS with #d: ["_"]
		admins := instance.Groups.GetAdmins("_")
		log.Printf("Publishing relay admin list with %d admins: %v", len(admins), admins)
		if err := instance.Groups.UpdateAdminsList("_"); err != nil {
			log.Printf("Failed to publish relay admin list: %v", err)
		} else {
			log.Printf("Successfully published relay admin list")
		}
	}

	// Lapis compatibility boundary for Buzz desktop and mobile clients. Keep
	// this after Blossom so /media/{sha256_ext} can reuse its existing handler.
	enableBuzzCompatibility(instance)

	// Update managed membership/admin lists

	instance.Management.AllowPubkey(config.GetSelf())
	instance.Management.AllowPubkey(config.GetOwner())

	for _, role := range config.Roles {
		for _, hex := range role.Pubkeys {
			if pubkey, err := nostr.PubKeyFromHex(hex); err == nil {
				instance.Management.AllowPubkey(pubkey)
			}
		}
	}

	return instance, nil
}

func (instance *Instance) Cleanup() {
	if instance.GitForge != nil {
		instance.GitForge.Close()
	}
	instance.Events.Close()
}

// Utility methods

func (instance *Instance) StripSignature(ctx context.Context, event nostr.Event) nostr.Event {
	pubkey, _ := authenticatedPubkey(ctx)

	if instance.Config.Policy.StripSignatures && !instance.Config.CanManage(pubkey) &&
		(instance.Agents == nil || !instance.Agents.PreserveSignature(event)) {
		var zeroSig [64]byte
		event.Sig = zeroSig
	}

	return event
}

func (instance *Instance) AllowRecipientEvent(event nostr.Event) bool {
	// For zap receipts and gift wraps, authorize the recipient instead of the author.
	// For everything else, make sure the authenticated user is the same as the event author
	recipientAuthKinds := []nostr.Kind{
		nostr.KindZap,
		nostr.KindGiftWrap,
	}

	if slices.Contains(recipientAuthKinds, event.Kind) {
		recipientTag := event.Tags.Find("p")

		if len(recipientTag) >= 2 {
			pubkey, err := nostr.PubKeyFromHex(recipientTag[1])

			if err == nil && (instance.Management.IsMember(pubkey) ||
				(instance.Agents != nil && instance.Agents.IsKnownActiveAgent(pubkey))) {
				return true
			}
		}
	}

	return false
}

func (instance *Instance) IsInternalEvent(event nostr.Event) bool {
	if event.Kind == nostr.KindApplicationSpecificData {
		tag := event.Tags.Find("d")

		if tag != nil && strings.HasPrefix(tag[1], "zooid/") {
			return true
		}
	}

	return false
}

func (instance *Instance) IsReadOnlyEvent(event nostr.Event) bool {
	readOnlyEventKinds := []nostr.Kind{
		RELAY_ADD_MEMBER,
		RELAY_REMOVE_MEMBER,
		RELAY_MEMBERS,
	}

	return slices.Contains(readOnlyEventKinds, event.Kind)
}

func (instance *Instance) IsWriteOnlyEvent(event nostr.Event) bool {
	writeOnlyEventKinds := []nostr.Kind{
		RELAY_JOIN,
		RELAY_LEAVE,
	}

	return slices.Contains(writeOnlyEventKinds, event.Kind)
}

// isLargeListEvent returns true for events that are too large to broadcast
// to all subscribers (e.g. group members list with thousands of entries).
// These events are still stored and served on explicit REQ.
func isLargeListEvent(event nostr.Event) bool {
	return event.Kind == nostr.KindSimpleGroupMembers
}

func (instance *Instance) GenerateInviteEvent(pubkey nostr.PubKey) nostr.Event {
	filter := nostr.Filter{
		Kinds: []nostr.Kind{RELAY_INVITE},
		Tags: nostr.TagMap{
			"#p": []string{pubkey.Hex()},
		},
	}

	for event := range instance.Events.QueryEvents(filter, 1) {
		return event
	}

	event := nostr.Event{
		Kind:      RELAY_INVITE,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			[]string{"claim", RandomString(8)},
			[]string{"p", pubkey.Hex()},
		},
	}

	if err := instance.Events.SignAndStoreEvent(&event, false); err != nil {
		log.Printf("Failed to sign invite event: %v", err)
	}

	return event
}

// Handlers

func (instance *Instance) OnConnect(ctx context.Context) {
	khatru.RequestAuth(ctx)
}

func (instance *Instance) PreventBroadcast(ws *khatru.WebSocket, filter nostr.Filter, event nostr.Event) bool {
	if instance.IsWriteOnlyEvent(event) || isLargeListEvent(event) {
		return true
	}

	pubkey, authenticated := khatru.GetAuthed(ws.Context)
	if !authenticated {
		if isMembershipNotification(event.Kind) {
			return true
		}
		return !instance.Config.Policy.AnonymousRead || !instance.CanAnonymousRead(event)
	}
	if instance.Agents != nil && !instance.Agents.CanRead(pubkey, event) {
		return true
	}
	if instance.Groups.IsGroupEvent(event) && !isMembershipNotification(event.Kind) && !protocol.IsGlobalOnly(event.Kind) && !instance.Groups.CanRead(pubkey, event) {
		return true
	}
	return instance.ProjectTasks != nil && !instance.ProjectTasks.CanRead(ws.Context, pubkey, event)
}

func (instance *Instance) StoreEvent(ctx context.Context, event nostr.Event) error {
	return instance.Events.StoreEvent(event)
}

func (instance *Instance) ReplaceEvent(ctx context.Context, event nostr.Event) error {
	_, err := instance.Events.ReplaceEvent(event)
	return err
}

func (instance *Instance) DeleteEvent(ctx context.Context, id nostr.ID) error {
	return instance.Events.DeleteEvent(id)
}

// Requests

func (instance *Instance) OnRequest(ctx context.Context, filter nostr.Filter) (reject bool, msg string) {
	pubkey, ok := authenticatedPubkey(ctx)

	if !ok {
		if instance.Config.Policy.AnonymousRead {
			return false, ""
		}
		return true, "auth-required: authentication is required for access"
	}

	// If open policy, allow all authenticated users; otherwise require membership
	if !instance.Config.Policy.Open && (instance.Agents == nil || !instance.Agents.IsRelayMember(ctx, pubkey)) {
		return true, "restricted: you are not a member of this relay"
	}

	return false, ""
}

func (instance *Instance) QueryStored(ctx context.Context, filter nostr.Filter) iter.Seq[nostr.Event] {
	return instance.queryStored(ctx, filter, "")
}

func (instance *Instance) QueryStoredBefore(ctx context.Context, filter nostr.Filter, beforeID string) iter.Seq[nostr.Event] {
	return instance.queryStored(ctx, filter, beforeID)
}

func (instance *Instance) queryStored(ctx context.Context, filter nostr.Filter, beforeID string) iter.Seq[nostr.Event] {
	return func(yield func(nostr.Event) bool) {
		if khatru.IsInternalCall(ctx) {
			for event := range instance.Events.QueryEventsBefore(ctx, filter, beforeID, 0) {
				if !yield(event) {
					return
				}
			}
		} else {
			pubkey, authenticated := authenticatedPubkey(ctx)
			generated := make([]nostr.Event, 0)
			visible := func(event nostr.Event) bool {
				if event.Kind == RELAY_INVITE || instance.IsInternalEvent(event) || instance.IsWriteOnlyEvent(event) {
					return false
				}
				if instance.ProjectTasks != nil && instance.ProjectTasks.IsTombstoned(ctx, event) {
					return false
				}
				if !authenticated {
					return !isMembershipNotification(event.Kind) && instance.Config.Policy.AnonymousRead && instance.CanAnonymousRead(event)
				}
				if instance.Agents != nil && !instance.Agents.CanRead(pubkey, event) {
					return false
				}
				if instance.Groups.IsGroupEvent(event) && !isMembershipNotification(event.Kind) && !protocol.IsGlobalOnly(event.Kind) && !instance.Groups.CanRead(pubkey, event) {
					return false
				}
				return instance.ProjectTasks == nil || instance.ProjectTasks.CanRead(ctx, pubkey, event)
			}

			if slices.Contains(filter.Kinds, RELAY_INVITE) && instance.Config.CanInvite(pubkey) {
				generated = append(generated, instance.GenerateInviteEvent(pubkey))
			}
			for _, event := range generated {
				if !filter.Matches(event) {
					continue
				}

				if !yield(instance.StripSignature(ctx, event)) {
					return
				}
			}

			personaMayMatch := len(filter.Kinds) == 0 || slices.Contains(filter.Kinds, protocol.KindPersona)
			if !personaMayMatch {
				for event := range instance.Events.QueryEventsBefore(ctx, filter, beforeID, 1000) {
					if visible(event) && !yield(instance.StripSignature(ctx, event)) {
						return
					}
				}
				return
			}

			// Persona visibility depends on the authenticated reader and the current
			// owner-to-instance link. Scan storage pages until the requested number
			// of authorized rows has been produced so newer private personas cannot
			// consume the client's limit and hide an older visible head.
			if filter.LimitZero {
				return
			}
			requested := filter.Limit
			if requested <= 0 || requested > 1000 {
				requested = 1000
			}
			scanFilter := filter
			scanFilter.Limit = 1000
			cursor := beforeID
			delivered := 0
			for delivered < requested {
				scanned := 0
				var last nostr.Event
				for event := range instance.Events.QueryEventsBefore(ctx, scanFilter, cursor, 1000) {
					scanned++
					last = event
					if !visible(event) {
						continue
					}
					delivered++
					if !yield(instance.StripSignature(ctx, event)) || delivered >= requested {
						return
					}
				}
				if scanned < scanFilter.Limit || last.ID == (nostr.ID{}) {
					return
				}
				scanFilter.Until = last.CreatedAt
				cursor = last.ID.Hex()
			}
		}
	}
}

// CanAnonymousRead limits unauthenticated clients to public community data.
// It intentionally does not use a zero pubkey with membership checks: a zero
// key can match an absent creator cache entry and incorrectly grant access.
func (instance *Instance) CanAnonymousRead(event nostr.Event) bool {
	if !instance.Config.Policy.Open || !instance.Config.Policy.AnonymousRead {
		return false
	}
	if event.Kind == nostr.KindProfileMetadata || event.Kind == nostr.KindEmojiList || event.Kind == nostr.KindEmojiSets {
		return true
	}
	if instance.ProjectTasks != nil && instance.ProjectTasks.CanAnonymousRead(instance.Ctx, event) {
		return true
	}
	if !instance.Groups.IsGroupEvent(event) {
		return false
	}

	h := GetGroupIDFromEvent(event)
	if h == "" || h == "_" {
		return false
	}
	metadata, found := instance.Groups.GetMetadata(h)
	return found && !HasTag(metadata.Tags, "private") && !HasTag(metadata.Tags, "hidden")
}

// Event publishing

func (instance *Instance) OnEvent(ctx context.Context, event nostr.Event) (reject bool, msg string) {
	if instance.AllowRecipientEvent(event) {
		return false, ""
	}

	pubkey, isAuthenticated := authenticatedPubkey(ctx)

	if !isAuthenticated {
		return true, "auth-required: authentication is required for access"
	} else if pubkey != event.PubKey {
		// NIP-17 gift wraps deliberately use a fresh ephemeral author key. On an
		// open relay an authenticated sender may submit a well-addressed wrap;
		// closed relays retain the existing recipient membership gate above.
		if event.Kind == nostr.KindGiftWrap && instance.Config.Policy.Open {
			if recipient := event.Tags.Find("p"); len(recipient) >= 2 {
				if _, err := nostr.PubKeyFromHex(recipient[1]); err == nil {
					return false, ""
				}
			}
		}
		return true, "restricted: you cannot publish events on behalf of others"
	}

	if event.Kind == RELAY_JOIN {
		return instance.Management.ValidateJoinRequest(event)
	}

	if instance.Agents != nil {
		if reason := instance.Agents.CheckWrite(ctx, event); reason != "" {
			return true, reason
		}
	}

	// If open policy, allow all authenticated users; otherwise require membership
	if !instance.Config.Policy.Open && (instance.Agents == nil || !instance.Agents.IsRelayMember(ctx, pubkey)) {
		return true, "restricted: you are not a member of this relay"
	}

	if instance.IsInternalEvent(event) {
		return true, "invalid: this event's kind is not accepted"
	}

	if instance.IsReadOnlyEvent(event) {
		return true, "invalid: this event's kind is not accepted"
	}

	if instance.Groups.IsGroupEvent(event) && !protocol.IsGlobalOnly(event.Kind) {
		handled, registryReason := instance.Registry.CheckWrite(event)
		if registryReason != "" {
			return true, registryReason
		}
		if !handled {
			if err := instance.Groups.CheckWrite(event); err != "" {
				return true, err
			}
		}
	}

	if instance.ProjectTasks != nil {
		if _, reason := instance.ProjectTasks.CheckWrite(ctx, event); reason != "" {
			return true, reason
		}
	}
	if instance.GitForge != nil {
		if _, reason := instance.GitForge.CheckWrite(event); reason != "" {
			return true, reason
		}
	}

	if instance.Management.EventIsBanned(event.ID) {
		return true, "restricted: this event has been banned from this relay"
	}

	return false, ""
}

func (instance *Instance) OnEventSaved(ctx context.Context, event nostr.Event) {
	if instance.Agents != nil {
		instance.Agents.RememberStored(event)
	}
	if instance.ProjectTasks != nil {
		if err := instance.ProjectTasks.Remember(instance.Ctx, event); err != nil {
			log.Printf("Failed to persist project/task scope for event %s: %v", event.ID.Hex(), err)
		}
	}
	if instance.GitForge != nil {
		if err := instance.GitForge.Remember(instance.Ctx, event); err != nil {
			log.Printf("Failed to persist Git repository intent for event %s: %v", event.ID.Hex(), err)
		}
	}

	h := GetGroupIDFromEvent(event)

	if event.Kind == nostr.KindSimpleGroupJoinRequest && instance.Config.Groups.AutoJoin {
		if err := instance.Groups.AddMember(h, event.PubKey); err != nil {
			log.Printf("Failed to add member %s to group %q: %v", event.PubKey, h, err)
		} else if err := instance.emitMembershipNotification(true, h, event.PubKey, event.PubKey); err != nil {
			log.Printf("Failed to notify member %s of group %q addition: %v", event.PubKey, h, err)
		}
		if err := instance.Groups.ScheduleMembersListUpdate(h); err != nil {
			log.Printf("Failed to update members list for group %q: %v", h, err)
		}
		if err := instance.Groups.ScheduleMemberCountRefresh(h); err != nil {
			log.Printf("Failed to refresh member count for group %q: %v", h, err)
		}
	}

	if event.Kind == nostr.KindSimpleGroupLeaveRequest {
		if err := instance.Groups.RemoveMember(h, event.PubKey); err != nil {
			log.Printf("Failed to remove member %s from group %q: %v", event.PubKey, h, err)
		} else if err := instance.emitMembershipNotification(false, h, event.PubKey, event.PubKey); err != nil {
			log.Printf("Failed to notify member %s of group %q removal: %v", event.PubKey, h, err)
		}
		if err := instance.Groups.ScheduleMembersListUpdate(h); err != nil {
			log.Printf("Failed to update members list for group %q: %v", h, err)
		}
		if err := instance.Groups.ScheduleMemberCountRefresh(h); err != nil {
			log.Printf("Failed to refresh member count for group %q: %v", h, err)
		}
	}

	if event.Kind == nostr.KindSimpleGroupPutUser {
		// Update membership and role caches for externally-received PutUser events
		for tag := range event.Tags.FindAll("p") {
			if pubkey, err := nostr.PubKeyFromHex(tag[1]); err == nil {
				ms := instance.Groups.getOrCreateMemberSet(h)
				ms.mu.Lock()
				ms.members[pubkey] = struct{}{}
				ms.mu.Unlock()

				// Extract roles from p-tag positions 2+ and update role cache
				roles := make([]string, 0, len(tag)-2)
				for i := 2; i < len(tag); i++ {
					roles = append(roles, tag[i])
				}
				instance.Groups.SetMemberRoles(h, pubkey, roles)
				if err := instance.emitMembershipNotification(true, h, pubkey, event.PubKey); err != nil {
					log.Printf("Failed to notify member %s of group %q addition: %v", pubkey, h, err)
				}
			}
		}
		if err := instance.Groups.ScheduleMembersListUpdate(h); err != nil {
			log.Printf("Failed to update members list for group %q: %v", h, err)
		}
		if err := instance.Groups.ScheduleMemberCountRefresh(h); err != nil {
			log.Printf("Failed to refresh member count for group %q: %v", h, err)
		}
	}

	if event.Kind == nostr.KindSimpleGroupRemoveUser {
		// Update membership and role caches for externally-received RemoveUser events
		for tag := range event.Tags.FindAll("p") {
			if pubkey, err := nostr.PubKeyFromHex(tag[1]); err == nil {
				if v, ok := instance.Groups.membershipCache.Load(h); ok {
					ms := v.(*memberSet)
					ms.mu.Lock()
					delete(ms.members, pubkey)
					ms.mu.Unlock()
				}
				instance.Groups.ClearMemberRoles(h, pubkey)
				if err := instance.emitMembershipNotification(false, h, pubkey, event.PubKey); err != nil {
					log.Printf("Failed to notify member %s of group %q removal: %v", pubkey, h, err)
				}
			}
		}
		if err := instance.Groups.ScheduleMembersListUpdate(h); err != nil {
			log.Printf("Failed to update members list for group %q: %v", h, err)
		}
		if err := instance.Groups.ScheduleMemberCountRefresh(h); err != nil {
			log.Printf("Failed to refresh member count for group %q: %v", h, err)
		}
	}

	if event.Kind == nostr.KindSimpleGroupCreateGroup {
		instance.Groups.creatorCache.Store(h, event.PubKey)
		// Brand-new group: there are no pre-existing members beyond
		// the creator we're about to add. Mark membership as fully
		// loaded BEFORE AddMember/ScheduleMembersListUpdate run, so
		// IsMember treats the cache as authoritative for this group
		// from creation onward and the eventual UpdateMembersList
		// publishes a correct snapshot the next restart's
		// WarmCaches will accept. Issue #25.
		instance.Groups.membershipFullyLoaded.Store(h, struct{}{})
		if err := instance.Groups.AddMember(h, event.PubKey); err != nil {
			log.Printf("Failed to add creator %s to group %q: %v", event.PubKey, h, err)
		} else if err := instance.emitMembershipNotification(true, h, event.PubKey, event.PubKey); err != nil {
			log.Printf("Failed to notify creator %s of group %q addition: %v", event.PubKey, h, err)
		}
		if err := instance.Groups.UpdateMetadata(event); err != nil {
			log.Printf("Failed to create metadata for group %q: %v", h, err)
		}
		if err := instance.Groups.ScheduleMembersListUpdate(h); err != nil {
			log.Printf("Failed to update members list for group %q: %v", h, err)
		}
		if err := instance.Groups.UpdateAdminsList(h); err != nil {
			log.Printf("Failed to update admins list for group %q: %v", h, err)
		}
	}

	if event.Kind == nostr.KindSimpleGroupEditMetadata {
		if err := instance.Groups.UpdateMetadata(event); err != nil {
			log.Printf("Failed to update metadata for group %q: %v", h, err)
		}
		if err := instance.Groups.UpdateAdminsList(h); err != nil {
			log.Printf("Failed to update admins list for group %q: %v", h, err)
		}
	}

	if event.Kind == nostr.KindSimpleGroupDeleteGroup {
		instance.Groups.DeleteGroup(h)
	}
}

func (instance *Instance) OnEventDeleted(_ context.Context, event nostr.Event) {
	if instance.Agents != nil {
		instance.Agents.ForgetStored(event)
	}
}

func (instance *Instance) OnEphemeralEvent(ctx context.Context, event nostr.Event) {
	if instance.Agents != nil {
		instance.Agents.OnEphemeral(event)
	}
	if event.Kind == RELAY_JOIN {
		instance.Management.AddMember(event.PubKey)
	}

	if event.Kind == RELAY_LEAVE {
		instance.Management.RemoveMember(event.PubKey)
	}
}

func isMembershipNotification(kind nostr.Kind) bool {
	return kind == protocol.KindMemberAddedNotification || kind == protocol.KindMemberRemovedNotification
}

func (instance *Instance) emitMembershipNotification(added bool, channel string, target, actor nostr.PubKey) error {
	kind := protocol.KindMemberRemovedNotification
	if added {
		kind = protocol.KindMemberAddedNotification
	}
	event := nostr.Event{
		Kind: kind, CreatedAt: nostr.Now(),
		Tags:    nostr.Tags{{"p", target.Hex()}, {"h", channel}},
		Content: membershipNotificationContent(added, channel, actor),
	}
	return instance.Events.SignAndStoreEvent(&event, true)
}
