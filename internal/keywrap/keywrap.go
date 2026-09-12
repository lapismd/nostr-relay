// Package keywrap provides transport-neutral envelope encryption for relay
// credentials. Callers own the associated-data schema and persistence model.
package keywrap

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

func ParseMasterKey(value []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 64 {
		if decoded, err := hex.DecodeString(string(trimmed)); err == nil && len(decoded) == 32 {
			return decoded, nil
		}
	}
	for _, encoding := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.StdEncoding} {
		if decoded, err := encoding.DecodeString(string(trimmed)); err == nil && len(decoded) == 32 {
			return decoded, nil
		}
	}
	return nil, errors.New("master key must decode to exactly 32 bytes")
}

type Wrapper interface {
	Version() string
	Wrap(dataKey, associatedData []byte) ([]byte, error)
	Unwrap(wrappedKey, associatedData []byte) ([]byte, error)
}

type AESWrapper struct {
	version string
	aead    cipher.AEAD
	rand    io.Reader
}

func NewAESWrapper(version string, masterKey []byte) (*AESWrapper, error) {
	if version == "" {
		return nil, errors.New("key wrapper version is required")
	}
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return nil, fmt.Errorf("create key wrapper: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create key wrapper AEAD: %w", err)
	}
	return &AESWrapper{version: version, aead: aead, rand: rand.Reader}, nil
}

func (wrapper *AESWrapper) Version() string { return wrapper.version }
func (wrapper *AESWrapper) Wrap(dataKey, aad []byte) ([]byte, error) {
	return seal(wrapper.aead, wrapper.rand, dataKey, aad)
}
func (wrapper *AESWrapper) Unwrap(wrappedKey, aad []byte) ([]byte, error) {
	return open(wrapper.aead, wrappedKey, aad)
}

type Envelope struct {
	Ciphertext string
	WrappedKey string
	Version    string
}

func Encrypt(plaintext, aad []byte, wrapper Wrapper) (Envelope, error) {
	if len(plaintext) == 0 || len(aad) == 0 || wrapper == nil {
		return Envelope{}, errors.New("invalid envelope input")
	}
	dataKey := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, dataKey); err != nil {
		return Envelope{}, fmt.Errorf("generate data key: %w", err)
	}
	defer clear(dataKey)
	block, err := aes.NewCipher(dataKey)
	if err != nil {
		return Envelope{}, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return Envelope{}, err
	}
	ciphertext, err := seal(aead, rand.Reader, plaintext, aad)
	if err != nil {
		return Envelope{}, err
	}
	wrapped, err := wrapper.Wrap(dataKey, aad)
	if err != nil {
		return Envelope{}, err
	}
	return Envelope{
		Ciphertext: base64.RawURLEncoding.EncodeToString(ciphertext),
		WrappedKey: base64.RawURLEncoding.EncodeToString(wrapped),
		Version:    wrapper.Version(),
	}, nil
}

func (envelope Envelope) Decrypt(aad []byte, wrapper Wrapper) ([]byte, error) {
	if wrapper == nil || envelope.Version != wrapper.Version() || len(aad) == 0 {
		return nil, errors.New("envelope wrapper version unavailable")
	}
	wrapped, err := base64.RawURLEncoding.DecodeString(envelope.WrappedKey)
	if err != nil {
		return nil, errors.New("invalid wrapped data key")
	}
	dataKey, err := wrapper.Unwrap(wrapped, aad)
	if err != nil {
		return nil, errors.New("unwrap data key")
	}
	defer clear(dataKey)
	block, err := aes.NewCipher(dataKey)
	if err != nil {
		return nil, errors.New("invalid data key")
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errors.New("invalid data key")
	}
	ciphertext, err := base64.RawURLEncoding.DecodeString(envelope.Ciphertext)
	if err != nil {
		return nil, errors.New("invalid ciphertext")
	}
	plaintext, err := open(aead, ciphertext, aad)
	if err != nil {
		clear(plaintext)
		return nil, errors.New("decrypt envelope")
	}
	return plaintext, nil
}

func seal(aead cipher.AEAD, random io.Reader, plaintext, aad []byte) ([]byte, error) {
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(random, nonce); err != nil {
		return nil, fmt.Errorf("generate encryption nonce: %w", err)
	}
	return aead.Seal(nonce, nonce, plaintext, aad), nil
}

func open(aead cipher.AEAD, ciphertext, aad []byte) ([]byte, error) {
	if len(ciphertext) < aead.NonceSize() {
		return nil, errors.New("ciphertext is too short")
	}
	return aead.Open(nil, ciphertext[:aead.NonceSize()], ciphertext[aead.NonceSize():], aad)
}
