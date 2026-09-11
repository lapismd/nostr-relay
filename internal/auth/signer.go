package auth

import (
	"context"
	"errors"

	"fiatjaf.com/nostr"
)

type ManagedKeyLoader interface {
	LoadManagedKey(context.Context, string) (ManagedKey, error)
}

type ManagedSigner struct {
	AccountID string
	Pubkey    nostr.PubKey
	Keys      ManagedKeyLoader
	Wrapper   KeyWrapper
	Policy    SigningPolicy
}

func (signer *ManagedSigner) PublicKey(context.Context) (nostr.PubKey, error) {
	if signer == nil || signer.AccountID == "" || signer.Pubkey == nostr.ZeroPK {
		return nostr.ZeroPK, ErrSignerUnavailable
	}
	return signer.Pubkey, nil
}

func (signer *ManagedSigner) SignEvent(ctx context.Context, event nostr.Event, signCtx SignContext) (nostr.Event, error) {
	if signer == nil || signer.Keys == nil || signer.Wrapper == nil || signer.Policy == nil ||
		signCtx.AccountID != signer.AccountID || signCtx.RequestedKind != event.Kind {
		return nostr.Event{}, ErrSigningDenied
	}
	if err := signer.Policy.Authorize(ctx, event, signCtx); err != nil {
		return nostr.Event{}, errors.Join(ErrSigningDenied, err)
	}
	key, err := signer.Keys.LoadManagedKey(ctx, signer.AccountID)
	if err != nil || key.Pubkey != signer.Pubkey.Hex() {
		return nostr.Event{}, ErrSignerUnavailable
	}
	secretBytes, err := key.Decrypt(signer.Wrapper)
	if err != nil {
		return nostr.Event{}, ErrSignerUnavailable
	}
	defer clear(secretBytes)
	var secret nostr.SecretKey
	copy(secret[:], secretBytes)
	if secret.Public() != signer.Pubkey {
		return nostr.Event{}, ErrSignerUnavailable
	}
	event.PubKey = signer.Pubkey
	if err := event.Sign(secret); err != nil {
		return nostr.Event{}, err
	}
	return event, nil
}
