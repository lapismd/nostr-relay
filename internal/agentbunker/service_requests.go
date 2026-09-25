package agentbunker

import (
	"fiatjaf.com/nostr"
	"time"
)

type createRequest struct {
	ClientPubkey     string `json:"client_pubkey"`
	ProfileContent   string `json:"profile_content"`
	ProfileCreatedAt int64  `json:"profile_created_at"`
	IdempotencyKey   string `json:"idempotency_key"`
}

type createResponse struct {
	Agent           AgentView     `json:"agent"`
	BunkerURL       string        `json:"bunker_url"`
	ProfileTemplate eventTemplate `json:"profile_template"`
}

type eventTemplate struct {
	Kind      int        `json:"kind"`
	CreatedAt int64      `json:"created_at"`
	Tags      [][]string `json:"tags"`
	Content   string     `json:"content"`
}

type activationRequest struct {
	OwnerEvent   nostr.Event `json:"owner_event"`
	ProfileEvent nostr.Event `json:"profile_event"`
}

type replacementRequest struct {
	ClientPubkey   string `json:"client_pubkey"`
	IdempotencyKey string `json:"idempotency_key"`
}

type replacementResponse struct {
	BunkerURL string    `json:"bunker_url"`
	ExpiresAt time.Time `json:"expires_at"`
}

type deletionRequest struct {
	UnlinkEvent nostr.Event `json:"unlink_event"`
}
