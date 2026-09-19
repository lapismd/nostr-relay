package agentbunker

import (
	"fiatjaf.com/nostr"
	"time"
	"zooid/internal/keywrap"
)

const (
	StatusPending = "pending"
	StatusActive  = "active"
	StatusRevoked = "revoked"
	ClientPending = "pending"
	ClientActive  = "active"
	ClientRevoked = "revoked"
)

type Config struct {
	PublicURL             string
	GitPublicURL          string
	AllowedBrowserOrigins []string
	InvitationLifetime    time.Duration
	MaxActivePerOwner     int
	MaxPendingPerOwner    int
	CreationsPerHour      int
	RequestsPerMinute     int
	RelayPubkey           nostr.PubKey
}

type Agent struct {
	Pubkey, OwnerPubkey, RelayPubkey string
	Status                           string
	ProfileContent                   string
	ProfileCreatedAt                 int64
	Key                              keywrap.Envelope
	CreatedAt, ExpiresAt             time.Time
	ActivatedAt                      *time.Time
	OwnerEvent, ProfileEvent         *nostr.Event
}

type Client struct {
	AgentPubkey, ClientPubkey, Status, InvitationHash string
	Invitation                                        keywrap.Envelope
	CreatedAt                                         time.Time
	PairedAt, RevokedAt                               *time.Time
}

type CachedRequest struct {
	ContentHash   string
	ResponseEvent nostr.Event
}

type AgentView struct {
	Pubkey          string     `json:"pubkey"`
	OwnerPubkey     string     `json:"owner_pubkey"`
	Status          string     `json:"status"`
	ConnectionState string     `json:"connection_state"`
	CreatedAt       time.Time  `json:"created_at"`
	ExpiresAt       time.Time  `json:"expires_at"`
	ActivatedAt     *time.Time `json:"activated_at,omitempty"`
}

type Audit struct {
	OwnerPubkey, AgentPubkey, ClientPubkey, Operation, Result string
	CreatedAt                                                 time.Time
}
