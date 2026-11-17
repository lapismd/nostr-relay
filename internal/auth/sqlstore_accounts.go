package auth

import (
	"context"
	"database/sql"
	"errors"
	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"time"
)

func (store *SQLStore) BeginOAuthFlow(ctx context.Context, flow OAuthFlow) error {
	_, err := store.DB.ExecContext(ctx, `INSERT INTO `+store.table("oauth_flows")+`
		(state_hash, provider, verifier, nonce, intent, account_id, return_to, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, flow.StateHash[:], flow.Provider, flow.Verifier,
		flow.Nonce, flow.Intent, flow.AccountID, flow.ReturnTo, flow.ExpiresAt)
	return err
}

func (store *SQLStore) ConsumeOAuthFlow(ctx context.Context, hash [32]byte, now time.Time) (OAuthFlow, error) {
	var flow OAuthFlow
	var rawHash []byte
	err := store.DB.QueryRowContext(ctx, `DELETE FROM `+store.table("oauth_flows")+`
		WHERE state_hash=$1 AND expires_at>$2
		RETURNING state_hash,provider,verifier,nonce,intent,account_id,return_to,expires_at`, hash[:], now).Scan(
		&rawHash, &flow.Provider, &flow.Verifier, &flow.Nonce, &flow.Intent, &flow.AccountID, &flow.ReturnTo, &flow.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return flow, ErrNotFound
	}
	copy(flow.StateHash[:], rawHash)
	return flow, err
}

func (store *SQLStore) FindAccountByIdentity(ctx context.Context, provider, providerID string) (Account, error) {
	return store.scanAccount(store.DB.QueryRowContext(ctx, `SELECT a.account_id,a.nostr_pubkey,a.created_at,a.updated_at
		FROM `+store.table("accounts")+` a JOIN `+store.table("external_identities")+` i ON i.account_id=a.account_id
		WHERE i.provider=$1 AND i.provider_user_id=$2`, provider, providerID))
}

func (store *SQLStore) AccountByID(ctx context.Context, accountID string) (Account, error) {
	return store.scanAccount(store.DB.QueryRowContext(ctx, `SELECT account_id,nostr_pubkey,created_at,updated_at
		FROM `+store.table("accounts")+` WHERE account_id=$1`, accountID))
}

func (store *SQLStore) AccountByPubkey(ctx context.Context, pubkey nostr.PubKey) (Account, error) {
	return store.scanAccount(store.DB.QueryRowContext(ctx, `SELECT account_id,nostr_pubkey,created_at,updated_at
		FROM `+store.table("accounts")+` WHERE nostr_pubkey=$1`, pubkey.Hex()))
}

func (store *SQLStore) scanAccount(row interface{ Scan(...any) error }) (Account, error) {
	var account Account
	var pubkey string
	if err := row.Scan(&account.ID, &pubkey, &account.CreatedAt, &account.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return account, ErrNotFound
		}
		return account, err
	}
	parsed, err := nostr.PubKeyFromHex(pubkey)
	if err != nil {
		return account, errors.New("stored account pubkey is invalid")
	}
	account.Pubkey = parsed
	return account, nil
}

func (store *SQLStore) CreateManagedAccount(ctx context.Context, identity ExternalIdentity, key ManagedKey) (Account, error) {
	pubkey, err := nostr.PubKeyFromHex(key.Pubkey)
	if err != nil || identity.Provider == "" || identity.ProviderID == "" {
		return Account{}, errors.New("invalid managed account")
	}
	accountID := uuid.NewString()
	if identity.AccountID != "" {
		accountID = identity.AccountID
	}
	if key.AccountID != accountID {
		return Account{}, errors.New("managed key account mismatch")
	}
	tx, err := store.DB.BeginTx(ctx, nil)
	if err != nil {
		return Account{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO `+store.table("accounts")+` (account_id,nostr_pubkey) VALUES ($1,$2)`, accountID, pubkey.Hex()); err != nil {
		return Account{}, classifyConflict(err)
	}
	if err = store.insertIdentity(ctx, tx, accountID, identity); err != nil {
		return Account{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO `+store.table("signer_bindings")+`
		(account_id,signer_type,public_key,status) VALUES ($1,'managed',$2,'active')`, accountID, pubkey.Hex()); err != nil {
		return Account{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO `+store.table("managed_keys")+`
		(account_id,public_key,encrypted_private_key,encrypted_data_key,encryption_version)
		VALUES ($1,$2,$3,$4,$5)`, accountID, pubkey.Hex(), key.EncryptedSecretKey, key.EncryptedDataKey, key.EncryptionVersion); err != nil {
		return Account{}, err
	}
	if err = tx.Commit(); err != nil {
		return Account{}, err
	}
	return store.AccountByID(ctx, accountID)
}

func (store *SQLStore) CreateExternalAccount(ctx context.Context, pubkey nostr.PubKey) (Account, error) {
	accountID := uuid.NewString()
	tx, err := store.DB.BeginTx(ctx, nil)
	if err != nil {
		return Account{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO `+store.table("accounts")+` (account_id,nostr_pubkey) VALUES ($1,$2)`, accountID, pubkey.Hex()); err != nil {
		if existing, lookupErr := store.AccountByPubkey(ctx, pubkey); lookupErr == nil {
			return existing, nil
		}
		return Account{}, classifyConflict(err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO `+store.table("signer_bindings")+`
		(account_id,signer_type,public_key,status) VALUES ($1,'external',$2,'active')`, accountID, pubkey.Hex()); err != nil {
		return Account{}, err
	}
	if err = tx.Commit(); err != nil {
		return Account{}, err
	}
	return store.AccountByID(ctx, accountID)
}

func (store *SQLStore) insertIdentity(ctx context.Context, executor sqlExecutor, accountID string, identity ExternalIdentity) error {
	_, err := executor.ExecContext(ctx, `INSERT INTO `+store.table("external_identities")+`
		(account_id,provider,provider_user_id,provider_login,display_name,email)
		VALUES ($1,$2,$3,$4,$5,$6)`, accountID, identity.Provider, identity.ProviderID,
		identity.Login, identity.DisplayName, identity.Email)
	return classifyConflict(err)
}

func (store *SQLStore) LinkIdentity(ctx context.Context, identity ExternalIdentity) error {
	if identity.AccountID == "" {
		return errors.New("account is required")
	}
	return store.insertIdentity(ctx, store.DB, identity.AccountID, identity)
}

func (store *SQLStore) ExternalIdentities(ctx context.Context, accountID string) ([]ExternalIdentity, error) {
	rows, err := store.DB.QueryContext(ctx, `SELECT account_id,provider,provider_user_id,provider_login,display_name,email,created_at,updated_at
		FROM `+store.table("external_identities")+` WHERE account_id=$1 ORDER BY provider`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var identities []ExternalIdentity
	for rows.Next() {
		var identity ExternalIdentity
		if err := rows.Scan(&identity.AccountID, &identity.Provider, &identity.ProviderID, &identity.Login,
			&identity.DisplayName, &identity.Email, &identity.CreatedAt, &identity.UpdatedAt); err != nil {
			return nil, err
		}
		identities = append(identities, identity)
	}
	return identities, rows.Err()
}

func (store *SQLStore) RemoveIdentity(ctx context.Context, accountID, provider string) error {
	result, err := store.DB.ExecContext(ctx, `DELETE FROM `+store.table("external_identities")+` WHERE account_id=$1 AND provider=$2
		AND (SELECT COUNT(*) FROM `+store.table("external_identities")+` WHERE account_id=$1)>1`, accountID, provider)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return errors.New("cannot remove the last login method")
	}
	return nil
}
