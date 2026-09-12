package keywrap

import (
	"crypto/rand"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestEnvelopeBindsAssociatedDataAndVersion(t *testing.T) {
	master := make([]byte, 32)
	_, _ = rand.Read(master)
	wrapper, err := NewAESWrapper("v1", master)
	require.NoError(t, err)
	secret := []byte("credential material")
	envelope, err := Encrypt(secret, []byte("agent-a|owner-a|relay-a|v1"), wrapper)
	require.NoError(t, err)
	require.NotContains(t, envelope.Ciphertext, string(secret))
	plain, err := envelope.Decrypt([]byte("agent-a|owner-a|relay-a|v1"), wrapper)
	require.NoError(t, err)
	require.Equal(t, secret, plain)
	clear(plain)
	_, err = envelope.Decrypt([]byte("agent-b|owner-a|relay-a|v1"), wrapper)
	require.Error(t, err)
}
