package auth

import (
	"context"
	"encoding/json"
	"errors"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip46"
)

type NIP46Remote interface {
	GetPublicKey(context.Context) (nostr.PubKey, error)
	SignEvent(context.Context, *nostr.Event) error
	NIP44Encrypt(context.Context, nostr.PubKey, string) (string, error)
	NIP44Decrypt(context.Context, nostr.PubKey, string) (string, error)
	SwitchRelays(context.Context) ([]string, error)
}

type NIP46Connector interface {
	Connect(context.Context, nostr.SecretKey, string) (NIP46Remote, error)
}

func (signer *NIP46Signer) Encrypt(ctx context.Context, plaintext string, recipient nostr.PubKey) (string, error) {
	if signer == nil || signer.Remote == nil || recipient == nostr.ZeroPK {
		return "", ErrSignerUnavailable
	}
	value, err := signer.Remote.NIP44Encrypt(ctx, recipient, plaintext)
	if err != nil {
		return "", ErrSignerUnavailable
	}
	return value, nil
}

func (signer *NIP46Signer) Decrypt(ctx context.Context, ciphertext string, sender nostr.PubKey) (string, error) {
	if signer == nil || signer.Remote == nil || sender == nostr.ZeroPK {
		return "", ErrSignerUnavailable
	}
	value, err := signer.Remote.NIP44Decrypt(ctx, sender, ciphertext)
	if err != nil {
		return "", ErrSignerUnavailable
	}
	return value, nil
}

type RemoteNIP46Connector struct{}

func (RemoteNIP46Connector) Connect(ctx context.Context, clientSecret nostr.SecretKey, bunkerURL string) (NIP46Remote, error) {
	client, err := nip46.ConnectBunker(ctx, clientSecret, bunkerURL, nil, func(string) {})
	if err != nil {
		return nil, err
	}
	return client, nil
}

type NIP46Signer struct {
	AccountID string
	Pubkey    nostr.PubKey
	Remote    NIP46Remote
	Policy    SigningPolicy
}

func (signer *NIP46Signer) PublicKey(ctx context.Context) (nostr.PubKey, error) {
	if signer == nil || signer.Remote == nil || signer.Pubkey == nostr.ZeroPK {
		return nostr.ZeroPK, ErrSignerUnavailable
	}
	pubkey, err := signer.Remote.GetPublicKey(ctx)
	if err != nil || pubkey != signer.Pubkey {
		return nostr.ZeroPK, ErrSignerUnavailable
	}
	return pubkey, nil
}

func (signer *NIP46Signer) SignEvent(ctx context.Context, event nostr.Event, signCtx SignContext) (nostr.Event, error) {
	if signer == nil || signer.Remote == nil || signer.Policy == nil || signCtx.AccountID != signer.AccountID || signCtx.RequestedKind != event.Kind {
		return nostr.Event{}, ErrSigningDenied
	}
	if err := signer.Policy.Authorize(ctx, event, signCtx); err != nil {
		return nostr.Event{}, errors.Join(ErrSigningDenied, err)
	}
	event.PubKey = signer.Pubkey
	original := event
	expectedID := original.GetID()
	if err := signer.Remote.SignEvent(ctx, &event); err != nil {
		return nostr.Event{}, ErrSignerUnavailable
	}
	if event.PubKey != signer.Pubkey || event.ID != expectedID || !event.CheckID() || !event.VerifySignature() {
		return nostr.Event{}, ErrSignerUnavailable
	}
	return event, nil
}

func (signer *NIP46Signer) SwitchRelays(ctx context.Context) ([]string, error) {
	if signer == nil || signer.Remote == nil {
		return nil, ErrSignerUnavailable
	}
	return signer.Remote.SwitchRelays(ctx)
}

func (signer *NIP46Signer) Logout(ctx context.Context) error {
	if signer == nil || signer.Remote == nil {
		return ErrSignerUnavailable
	}
	return nil
}

type nip46BindingMetadata struct {
	BunkerURL             string `json:"bunker_url"`
	EncryptedClientSecret string `json:"encrypted_client_secret"`
}

func encodeNIP46Binding(accountID, bunkerURL string, clientSecret nostr.SecretKey, codec CredentialCodec) ([]byte, error) {
	encrypted, err := codec.Encrypt(accountID, "nip46-client-secret", clientSecret.Hex())
	if err != nil {
		return nil, err
	}
	return json.Marshal(nip46BindingMetadata{BunkerURL: bunkerURL, EncryptedClientSecret: encrypted})
}

func decodeNIP46Binding(accountID string, metadata []byte, codec CredentialCodec) (string, nostr.SecretKey, error) {
	var binding nip46BindingMetadata
	if json.Unmarshal(metadata, &binding) != nil || binding.BunkerURL == "" || binding.EncryptedClientSecret == "" {
		return "", nostr.SecretKey{}, ErrSignerUnavailable
	}
	raw, err := codec.Decrypt(accountID, "nip46-client-secret", binding.EncryptedClientSecret)
	if err != nil {
		return "", nostr.SecretKey{}, ErrSignerUnavailable
	}
	secret, err := nostr.SecretKeyFromHex(raw)
	if err != nil {
		return "", nostr.SecretKey{}, ErrSignerUnavailable
	}
	return binding.BunkerURL, secret, nil
}
