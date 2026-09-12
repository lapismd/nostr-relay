package auth

import "zooid/internal/keywrap"

func ParseMasterKey(value []byte) ([]byte, error) { return keywrap.ParseMasterKey(value) }

type KeyWrapper = keywrap.Wrapper
type AESKeyWrapper = keywrap.AESWrapper

func NewAESKeyWrapper(version string, masterKey []byte) (*AESKeyWrapper, error) {
	return keywrap.NewAESWrapper(version, masterKey)
}

type ManagedKey struct {
	AccountID          string
	Pubkey             string
	EncryptedSecretKey string
	EncryptedDataKey   string
	EncryptionVersion  string
}

func EncryptManagedKey(accountID, pubkey string, secretKey []byte, wrapper KeyWrapper) (ManagedKey, error) {
	aad := managedKeyAAD(accountID, pubkey, wrapper.Version())
	envelope, err := keywrap.Encrypt(secretKey, aad, wrapper)
	if err != nil {
		return ManagedKey{}, err
	}
	return ManagedKey{AccountID: accountID, Pubkey: pubkey, EncryptedSecretKey: envelope.Ciphertext, EncryptedDataKey: envelope.WrappedKey, EncryptionVersion: envelope.Version}, nil
}

func (key ManagedKey) Decrypt(wrapper KeyWrapper) ([]byte, error) {
	aad := managedKeyAAD(key.AccountID, key.Pubkey, key.EncryptionVersion)
	return (keywrap.Envelope{Ciphertext: key.EncryptedSecretKey, WrappedKey: key.EncryptedDataKey, Version: key.EncryptionVersion}).Decrypt(aad, wrapper)
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
