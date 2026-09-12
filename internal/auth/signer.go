package auth

import (
	"context"
	"errors"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip44"
)

type ManagedKeyLoader interface {
	LoadManagedKey(context.Context, string) (ManagedKey, error)
}

func (signer *ManagedSigner) Encrypt(ctx context.Context, plaintext string, recipient nostr.PubKey) (string, error) {
	secret, err := signer.secretKey(ctx)
	if err != nil {
		return "", err
	}
	defer clear(secret[:])
	conversationKey, err := nip44.GenerateConversationKey(recipient, secret)
	if err != nil {
		return "", ErrSigningDenied
	}
	return nip44.Encrypt(plaintext, conversationKey)
}

func (signer *ManagedSigner) Decrypt(ctx context.Context, ciphertext string, sender nostr.PubKey) (string, error) {
	secret, err := signer.secretKey(ctx)
	if err != nil {
		return "", err
	}
	defer clear(secret[:])
	conversationKey, err := nip44.GenerateConversationKey(sender, secret)
	if err != nil {
		return "", ErrSigningDenied
	}
	plaintext, err := nip44.Decrypt(ciphertext, conversationKey)
	if err != nil {
		return "", ErrSigningDenied
	}
	return plaintext, nil
}

func (signer *ManagedSigner) secretKey(ctx context.Context) (nostr.SecretKey, error) {
	if signer == nil || signer.Keys == nil || signer.Wrapper == nil || signer.AccountID == "" || signer.Pubkey == nostr.ZeroPK {
		return nostr.SecretKey{}, ErrSignerUnavailable
	}
	key, err := signer.Keys.LoadManagedKey(ctx, signer.AccountID)
	if err != nil || key.Pubkey != signer.Pubkey.Hex() {
		return nostr.SecretKey{}, ErrSignerUnavailable
	}
	secretBytes, err := key.Decrypt(signer.Wrapper)
	if err != nil {
		return nostr.SecretKey{}, ErrSignerUnavailable
	}
	defer clear(secretBytes)
	var secret nostr.SecretKey
	copy(secret[:], secretBytes)
	if secret.Public() != signer.Pubkey {
		clear(secret[:])
		return nostr.SecretKey{}, ErrSignerUnavailable
	}
	return secret, nil
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
	secret, err := signer.secretKey(ctx)
	if err != nil {
		return nostr.Event{}, err
	}
	defer clear(secret[:])
	event.PubKey = signer.Pubkey
	if err := event.Sign(secret); err != nil {
		return nostr.Event{}, err
	}
	return event, nil
}
