package agentbunker

import (
	"context"
	"errors"
	"fiatjaf.com/nostr"
	"net/url"
	"strings"
	"sync"
	"time"
	"zooid/internal/keywrap"
)

const maxBody = 128 << 10

type Service struct {
	Config       Config
	Store        Store
	Wrapper      keywrap.Wrapper
	Now          func() time.Time
	AdmitOwner   func(context.Context, nostr.PubKey) bool
	IsMember     func(string, nostr.PubKey) bool
	PersistEvent func(nostr.Event) error
	CurrentEvent func(kind nostr.Kind, author nostr.PubKey, d string) *nostr.Event
	EventByID    func(nostr.ID) *nostr.Event
	mu           sync.Mutex
	rates        map[string]rateWindow
}

type rateWindow struct {
	started time.Time
	count   int
}

func NewService(config Config, store Store, wrapper keywrap.Wrapper) (*Service, error) {
	if store == nil || wrapper == nil || config.RelayPubkey == nostr.ZeroPK {
		return nil, errors.New("managed agent store, wrapper, and relay pubkey are required")
	}
	if config.InvitationLifetime <= 0 {
		config.InvitationLifetime = 24 * time.Hour
	}
	if config.MaxActivePerOwner <= 0 {
		config.MaxActivePerOwner = 10
	}
	if config.MaxPendingPerOwner <= 0 {
		config.MaxPendingPerOwner = 3
	}
	if config.CreationsPerHour <= 0 {
		config.CreationsPerHour = 5
	}
	if config.RequestsPerMinute <= 0 {
		config.RequestsPerMinute = 120
	}
	return &Service{Config: config, Store: store, Wrapper: wrapper, Now: time.Now, rates: map[string]rateWindow{}}, nil
}

func (s *Service) NIP11() map[string]any {
	u, _ := url.Parse(strings.TrimRight(s.Config.PublicURL, "/") + "/nip46")
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	return map[string]any{"version": 1, "provisioning_url": strings.TrimRight(s.Config.PublicURL, "/") + "/api/agents/managed", "bunker_url": u.String(), "relay_pubkey": s.Config.RelayPubkey.Hex(), "authorization_type": "lapis-managed-v1"}
}

func (s *Service) inboxRelayURL() string {
	u, err := url.Parse(s.Config.PublicURL)
	if err != nil {
		return ""
	}
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else if u.Scheme == "http" {
		u.Scheme = "ws"
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

func (s *Service) allowRequest(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.Now()
	window := s.rates[key]
	if window.started.IsZero() || now.Sub(window.started) >= time.Minute {
		window = rateWindow{started: now}
	}
	if window.count >= s.Config.RequestsPerMinute {
		return false
	}
	window.count++
	s.rates[key] = window
	return true
}

func countTags(tags nostr.Tags, name string) int {
	count := 0
	for _, tag := range tags {
		if len(tag) > 0 && tag[0] == name {
			count++
		}
	}
	return count
}
