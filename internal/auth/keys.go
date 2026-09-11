package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

type KeyWrapper interface {
	Version() string
	Wrap(dataKey, associatedData []byte) ([]byte, error)
	Unwrap(wrappedKey, associatedData []byte) ([]byte, error)
}

type AESKeyWrapper struct {
	version string
	aead    cipher.AEAD
	rand    io.Reader
}

func NewAESKeyWrapper(version string, masterKey []byte) (*AESKeyWrapper, error) {
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
	return &AESKeyWrapper{version: version, aead: aead, rand: rand.Reader}, nil
}

func (wrapper *AESKeyWrapper) Version() string { return wrapper.version }

func (wrapper *AESKeyWrapper) Wrap(dataKey, associatedData []byte) ([]byte, error) {
	return seal(wrapper.aead, wrapper.rand, dataKey, associatedData)
}

func (wrapper *AESKeyWrapper) Unwrap(wrappedKey, associatedData []byte) ([]byte, error) {
	return open(wrapper.aead, wrappedKey, associatedData)
}

type ManagedKey struct {
	AccountID          string
	Pubkey             string
	EncryptedSecretKey string
	EncryptedDataKey   string
	EncryptionVersion  string
}

func EncryptManagedKey(accountID, pubkey string, secretKey []byte, wrapper KeyWrapper) (ManagedKey, error) {
	if accountID == "" || pubkey == "" || len(secretKey) != 32 || wrapper == nil {
		return ManagedKey{}, errors.New("invalid managed key input")
	}
	dataKey := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, dataKey); err != nil {
		return ManagedKey{}, fmt.Errorf("generate data key: %w", err)
	}
	defer clear(dataKey)
	block, err := aes.NewCipher(dataKey)
	if err != nil {
		return ManagedKey{}, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return ManagedKey{}, err
	}
	aad := managedKeyAAD(accountID, pubkey, wrapper.Version())
	encryptedSecret, err := seal(aead, rand.Reader, secretKey, aad)
	if err != nil {
		return ManagedKey{}, err
	}
	wrappedDataKey, err := wrapper.Wrap(dataKey, aad)
	if err != nil {
		return ManagedKey{}, err
	}
	return ManagedKey{
		AccountID: accountID, Pubkey: pubkey,
		EncryptedSecretKey: base64.RawURLEncoding.EncodeToString(encryptedSecret),
		EncryptedDataKey:   base64.RawURLEncoding.EncodeToString(wrappedDataKey),
		EncryptionVersion:  wrapper.Version(),
	}, nil
}

func (key ManagedKey) Decrypt(wrapper KeyWrapper) ([]byte, error) {
	if wrapper == nil || key.EncryptionVersion != wrapper.Version() {
		return nil, errors.New("managed key wrapper version unavailable")
	}
	aad := managedKeyAAD(key.AccountID, key.Pubkey, key.EncryptionVersion)
	wrapped, err := base64.RawURLEncoding.DecodeString(key.EncryptedDataKey)
	if err != nil {
		return nil, errors.New("invalid wrapped data key")
	}
	dataKey, err := wrapper.Unwrap(wrapped, aad)
	if err != nil {
		return nil, errors.New("unwrap managed data key")
	}
	defer clear(dataKey)
	block, err := aes.NewCipher(dataKey)
	if err != nil {
		return nil, errors.New("invalid managed data key")
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errors.New("invalid managed data key")
	}
	ciphertext, err := base64.RawURLEncoding.DecodeString(key.EncryptedSecretKey)
	if err != nil {
		return nil, errors.New("invalid encrypted managed key")
	}
	plaintext, err := open(aead, ciphertext, aad)
	if err != nil || len(plaintext) != 32 {
		clear(plaintext)
		return nil, errors.New("decrypt managed key")
	}
	return plaintext, nil
}

func RewrapManagedKey(key ManagedKey, oldWrapper, newWrapper KeyWrapper) (ManagedKey, error) {
	secret, err := key.Decrypt(oldWrapper)
	if err != nil {
		return ManagedKey{}, err
	}
	defer clear(secret)
	return EncryptManagedKey(key.AccountID, key.Pubkey, secret, newWrapper)
}

func managedKeyAAD(accountID, pubkey, version string) []byte {
	return []byte("lapis-auth-managed-key-v1\x00" + accountID + "\x00" + pubkey + "\x00" + version)
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
	nonce, payload := ciphertext[:aead.NonceSize()], ciphertext[aead.NonceSize():]
	return aead.Open(nil, nonce, payload, aad)
}
