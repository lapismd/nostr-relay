package auth

import (
	"context"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
)

type managedKeyLoader struct{ key ManagedKey }

func (loader managedKeyLoader) LoadManagedKey(context.Context, string) (ManagedKey, error) {
	return loader.key, nil
}

func TestManagedKeyEnvelopeEncryptionBindsAccountAndSupportsRewrap(t *testing.T) {
	secret := nostr.Generate()
	master1 := make([]byte, 32)
	master2 := make([]byte, 32)
	_, _ = rand.Read(master1)
	_, _ = rand.Read(master2)
	wrapper1, err := NewAESKeyWrapper("v1", master1)
	require.NoError(t, err)
	wrapper2, err := NewAESKeyWrapper("v2", master2)
	require.NoError(t, err)

	key, err := EncryptManagedKey("account-a", secret.Public().Hex(), secret[:], wrapper1)
	require.NoError(t, err)
	require.NotContains(t, key.EncryptedSecretKey, secret.Hex())

	plaintext, err := key.Decrypt(wrapper1)
	require.NoError(t, err)
	require.Equal(t, secret[:], plaintext)
	clear(plaintext)

	tampered := key
	tampered.AccountID = "account-b"
	_, err = tampered.Decrypt(wrapper1)
	require.Error(t, err)

	rewrapped, err := RewrapManagedKey(key, wrapper1, wrapper2)
	require.NoError(t, err)
	_, err = rewrapped.Decrypt(wrapper1)
	require.Error(t, err)
	plaintext, err = rewrapped.Decrypt(wrapper2)
	require.NoError(t, err)
	require.Equal(t, secret[:], plaintext)
}

func TestManagedSignerEnforcesContextPolicyAndPubkey(t *testing.T) {
	secret := nostr.Generate()
	master := make([]byte, 32)
	_, _ = rand.Read(master)
	wrapper, err := NewAESKeyWrapper("v1", master)
	require.NoError(t, err)
	key, err := EncryptManagedKey("account-a", secret.Public().Hex(), secret[:], wrapper)
	require.NoError(t, err)

	signer := ManagedSigner{
		AccountID: "account-a", Pubkey: secret.Public(), Keys: managedKeyLoader{key}, Wrapper: wrapper,
		Policy: SigningPolicyFunc(func(_ context.Context, event nostr.Event, _ SignContext) error {
			if event.Kind != nostr.KindTextNote {
				return errors.New("kind denied")
			}
			return nil
		}),
	}
	event := nostr.Event{Kind: nostr.KindTextNote, Content: "hello", CreatedAt: nostr.Timestamp(time.Now().Unix())}
	signed, err := signer.SignEvent(context.Background(), event, SignContext{
		AccountID: "account-a", Purpose: SignPurposeNote, RequestedKind: nostr.KindTextNote,
	})
	require.NoError(t, err)
	require.Equal(t, secret.Public(), signed.PubKey)
	require.True(t, signed.CheckID())
	require.True(t, signed.VerifySignature())

	_, err = signer.SignEvent(context.Background(), event, SignContext{
		AccountID: "account-b", Purpose: SignPurposeNote, RequestedKind: nostr.KindTextNote,
	})
	require.ErrorIs(t, err, ErrSigningDenied)
}
