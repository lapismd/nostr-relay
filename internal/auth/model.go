// Package auth owns application accounts, authentication identities, signing
// bindings, and custody transitions. Nostr pubkeys remain the canonical
// identity used by relay policy.
package auth

import (
	"context"
	"errors"
	"time"

	"fiatjaf.com/nostr"
)

var (
	ErrNotFound          = errors.New("auth record not found")
	ErrIdentityConflict  = errors.New("external identity is already linked")
	ErrSignerUnavailable = errors.New("signer is unavailable")
	ErrSigningDenied     = errors.New("signing request denied")
)

type SignerType string

const (
	SignerManaged  SignerType = "managed"
	SignerNIP46    SignerType = "nip46"
	SignerExternal SignerType = "external"
)

type SignerStatus string

const (
	SignerActive           SignerStatus = "active"
	SignerMigrationPending SignerStatus = "migration_pending"
	SignerDisabled         SignerStatus = "disabled"
)

type Account struct {
	ID        string
	Pubkey    nostr.PubKey
	CreatedAt time.Time
	UpdatedAt time.Time
}

type ExternalIdentity struct {
	AccountID   string
	Provider    string
	ProviderID  string
	Login       string
	DisplayName string
	Email       string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type SignerBinding struct {
	AccountID string
	Type      SignerType
	Pubkey    nostr.PubKey
	Status    SignerStatus
	Metadata  []byte
	CreatedAt time.Time
	UpdatedAt time.Time
}

type Session struct {
	ID           string
	AccountID    string
	TokenHash    [32]byte
	AuthProvider string
	ExpiresAt    time.Time
	IdleExpires  time.Time
	CreatedAt    time.Time
	LastSeenAt   time.Time
	RevokedAt    *time.Time
}

type SignPurpose string

const (
	SignPurposeNote       SignPurpose = "note"
	SignPurposeForum      SignPurpose = "forum"
	SignPurposeProfile    SignPurpose = "profile"
	SignPurposeRepository SignPurpose = "repository"
	SignPurposeRelayAuth  SignPurpose = "relay_auth"
	SignPurposeHTTPAuth   SignPurpose = "http_auth"
)

type SignContext struct {
	AccountID     string
	Purpose       SignPurpose
	RequestedKind nostr.Kind
	Source        string
	CorrelationID string
}

type Signer interface {
	PublicKey(context.Context) (nostr.PubKey, error)
	SignEvent(context.Context, nostr.Event, SignContext) (nostr.Event, error)
}

type SigningPolicy interface {
	Authorize(context.Context, nostr.Event, SignContext) error
}

type SigningPolicyFunc func(context.Context, nostr.Event, SignContext) error

func (fn SigningPolicyFunc) Authorize(ctx context.Context, event nostr.Event, signCtx SignContext) error {
	return fn(ctx, event, signCtx)
}
