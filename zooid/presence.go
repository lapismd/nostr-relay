package zooid

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip11"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	buzzpresence "zooid/buzzagents/presence"
	"zooid/buzzagents/protocol"
	"zooid/internal/khatru"
	"zooid/internal/presence"
)

var presenceFailures = promauto.NewCounterVec(prometheus.CounterOpts{Name: "lapis_presence_failures_total", Help: "Presence backend failures by operation."}, []string{"operation"})
var presenceRecoveries = promauto.NewCounter(prometheus.CounterOpts{Name: "lapis_presence_notification_connections_total", Help: "Established presence notification listeners."})

type presenceConnection struct {
	mu     sync.Mutex
	id     string
	closed bool
}
type PresencePolicy struct {
	instance    *Instance
	Backend     presence.Backend
	connections sync.Map
}

func NewPresencePolicy(instance *Instance, backend presence.Backend) *PresencePolicy {
	return &PresencePolicy{instance: instance, Backend: backend}
}

func (p *PresencePolicy) Connect(ctx context.Context) {
	if connection := khatru.GetConnection(ctx); connection != nil {
		p.connections.LoadOrStore(connection, &presenceConnection{id: uuid.NewString()})
	}
}

func (p *PresencePolicy) Disconnect(ctx context.Context) {
	connection := khatru.GetConnection(ctx)
	value, ok := p.connections.Load(connection)
	if !ok {
		return
	}
	state := value.(*presenceConnection)
	state.mu.Lock()
	defer state.mu.Unlock()
	state.closed = true
	defer p.connections.Delete(connection)
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := p.Backend.RemoveConnection(cleanup, state.id); err != nil {
		presenceFailures.WithLabelValues("disconnect").Inc()
	}
}

func (p *PresencePolicy) Update(ctx context.Context, event nostr.Event) error {
	if p.instance.Management.PubkeyIsBanned(event.PubKey) {
		return errors.New("restricted: this author has been banned from this relay")
	}
	value, ok := p.connections.Load(khatru.GetConnection(ctx))
	if !ok {
		return errors.New("auth-required: presence requires a live connection")
	}
	state := value.(*presenceConnection)
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed {
		return errors.New("error: presence connection closed")
	}
	mode, err := exactOptionalTag(event.Tags, "presence-mode")
	if err != nil {
		return errors.New("invalid: malformed presence mode")
	}
	sequenceText, err := exactOptionalTag(event.Tags, "presence-sequence")
	if err != nil {
		return errors.New("invalid: malformed presence sequence")
	}
	sequence := int64(0)
	if sequenceText != "" {
		sequence, err = strconv.ParseInt(sequenceText, 10, 64)
		if err != nil {
			return errors.New("invalid: malformed presence sequence")
		}
	}
	update := presence.Update{PubKey: event.PubKey.Hex(), Connection: state.id, Status: buzzpresence.ParseStatus(event.Content), Mode: mode, EventID: event.ID.Hex(), CreatedAt: int64(event.CreatedAt), Sequence: sequence}
	if err = presence.Validate(update, time.Now().Unix()); err != nil {
		return err
	}
	snapshot, err := p.Backend.Apply(ctx, update)
	if err != nil {
		presenceFailures.WithLabelValues("write").Inc()
		if strings.HasPrefix(err.Error(), "invalid:") || strings.HasPrefix(err.Error(), "rate-limited:") {
			return err
		}
		return errors.New("error: presence storage unavailable")
	}
	p.broadcast(snapshot)
	return nil
}

func (p *PresencePolicy) event(snapshot presence.Snapshot) (nostr.Event, error) {
	event := nostr.Event{Kind: protocol.KindPresence, CreatedAt: nostr.Timestamp(snapshot.UpdatedAt), Content: snapshot.Status, Tags: nostr.Tags{
		{"p", snapshot.PubKey}, {"presence-mode", snapshot.Mode}, {"presence-revision", strconv.FormatInt(snapshot.Revision, 10)}, {"expiration", strconv.FormatInt(snapshot.ExpiresAt, 10)},
	}}
	err := p.instance.Config.Sign(&event)
	return event, err
}

func (p *PresencePolicy) broadcast(snapshot presence.Snapshot) {
	event, err := p.event(snapshot)
	if err != nil {
		presenceFailures.WithLabelValues("sign").Inc()
		return
	}
	p.instance.Relay.BroadcastEvent(event)
}

func (p *PresencePolicy) Query(ctx context.Context, filters []nostr.Filter) ([]nostr.Event, error) {
	reader, ok := authenticatedPubkey(ctx)
	if !ok || p.instance.Management.PubkeyIsBanned(reader) {
		return nil, errors.New("auth-required: presence requires authentication")
	}
	seen := map[string]bool{}
	keys := []string{}
	for _, filter := range filters {
		for _, author := range filter.Authors {
			key := author.Hex()
			if !seen[key] {
				seen[key] = true
				keys = append(keys, key)
			}
		}
	}
	if len(keys) > presence.MaxSubjects {
		return nil, errors.New("invalid: too many presence subjects")
	}
	snapshots, err := p.Backend.Snapshot(ctx, keys)
	if err != nil {
		presenceFailures.WithLabelValues("snapshot").Inc()
		return nil, errors.New("error: presence storage unavailable")
	}
	events := make([]nostr.Event, 0, len(snapshots))
	for _, snapshot := range snapshots {
		event, err := p.event(snapshot)
		if err != nil {
			return nil, errors.New("error: presence signing unavailable")
		}
		events = append(events, event)
	}
	return events, nil
}

func (p *PresencePolicy) watched() []string {
	seen := map[string]bool{}
	keys := []string{}
	for _, filter := range p.instance.Relay.GetListeningFilters() {
		for _, kind := range filter.Kinds {
			if kind == protocol.KindPresence {
				for _, key := range filter.Tags["p"] {
					if nostr.IsValid32ByteHex(key) && !seen[key] {
						seen[key] = true
						keys = append(keys, key)
					}
				}
			}
		}
	}
	return keys
}

func (p *PresencePolicy) refresh(ctx context.Context, key string) {
	keys := []string{key}
	if key == "" {
		keys = p.watched()
	}
	for len(keys) > 0 {
		n := min(len(keys), presence.MaxSubjects)
		snapshots, err := p.Backend.Snapshot(ctx, keys[:n])
		if err != nil {
			presenceFailures.WithLabelValues("reconcile").Inc()
			return
		}
		for _, snapshot := range snapshots {
			p.broadcast(snapshot)
		}
		keys = keys[n:]
	}
}

func (p *PresencePolicy) Start(ctx context.Context) {
	previous := p.instance.Relay.OverwriteRelayInformation
	p.instance.Relay.OverwriteRelayInformation = func(ctx context.Context, request *http.Request, info nip11.RelayInformationDocument) nip11.RelayInformationDocument {
		if previous != nil {
			info = previous(ctx, request, info)
		}
		if info.Malformed == nil {
			info.Malformed = map[string]any{}
		}
		info.Malformed["lapis_presence"] = map[string]any{"version": 1, "heartbeat_seconds": 60, "ttl_seconds": presence.TTL, "multi_session": true, "account_preference": true}
		return info
	}
	go func() {
		for ctx.Err() == nil {
			_ = p.Backend.Watch(ctx, func(key string) {
				if key == "" {
					presenceRecoveries.Inc()
				}
				p.refresh(ctx, key)
			})
			if ctx.Err() != nil {
				return
			}
			presenceFailures.WithLabelValues("notifications").Inc()
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}()
	go func() {
		expiry := time.NewTicker(5 * time.Second)
		refresh := time.NewTicker(time.Minute)
		defer expiry.Stop()
		defer refresh.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-expiry.C:
				if err := p.Backend.Expire(ctx); err != nil {
					presenceFailures.WithLabelValues("expiry").Inc()
				}
			case <-refresh.C:
				p.refresh(ctx, "")
			}
		}
	}()
}
