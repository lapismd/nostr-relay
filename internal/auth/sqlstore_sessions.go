package auth

import (
	"context"
	"database/sql"
	"errors"
	"fiatjaf.com/nostr"
	"time"
)

func (store *SQLStore) LoadManagedKey(ctx context.Context, accountID string) (ManagedKey, error) {
	var key ManagedKey
	err := store.DB.QueryRowContext(ctx, `SELECT account_id,public_key,encrypted_private_key,encrypted_data_key,encryption_version
		FROM `+store.table("managed_keys")+` WHERE account_id=$1`, accountID).Scan(&key.AccountID, &key.Pubkey,
		&key.EncryptedSecretKey, &key.EncryptedDataKey, &key.EncryptionVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return key, ErrNotFound
	}
	return key, err
}

func (store *SQLStore) SignerBindingByAccount(ctx context.Context, accountID string) (SignerBinding, error) {
	var binding SignerBinding
	var pubkey string
	err := store.DB.QueryRowContext(ctx, `SELECT account_id,signer_type,public_key,status,metadata,created_at,updated_at
		FROM `+store.table("signer_bindings")+` WHERE account_id=$1`, accountID).Scan(
		&binding.AccountID, &binding.Type, &pubkey, &binding.Status, &binding.Metadata, &binding.CreatedAt, &binding.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return binding, ErrNotFound
	}
	if err != nil {
		return binding, err
	}
	binding.Pubkey, err = nostr.PubKeyFromHex(pubkey)
	return binding, err
}

func (store *SQLStore) SetNIP46Binding(ctx context.Context, accountID string, pubkey nostr.PubKey, metadata []byte, now time.Time) error {
	result, err := store.DB.ExecContext(ctx, `UPDATE `+store.table("signer_bindings")+`
		SET signer_type='nip46',status='active',metadata=$3,updated_at=$4
		WHERE account_id=$1 AND public_key=$2 AND signer_type IN ('external','nip46')`, accountID, pubkey.Hex(), metadata, now)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return ErrSigningDenied
	}
	return nil
}

func (store *SQLStore) SetExternalBinding(ctx context.Context, accountID string, pubkey nostr.PubKey, now time.Time) error {
	result, err := store.DB.ExecContext(ctx, `UPDATE `+store.table("signer_bindings")+`
		SET signer_type='external',status='active',metadata='{}'::jsonb,updated_at=$3
		WHERE account_id=$1 AND public_key=$2 AND signer_type='nip46'`, accountID, pubkey.Hex(), now)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return ErrSigningDenied
	}
	return nil
}

func (store *SQLStore) CreateSession(ctx context.Context, session Session) error {
	_, err := store.DB.ExecContext(ctx, `INSERT INTO `+store.table("sessions")+`
		(session_id,account_id,token_hash,auth_provider,expires_at,idle_expires_at,created_at,last_seen_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, session.ID, session.AccountID, session.TokenHash[:],
		session.AuthProvider, session.ExpiresAt, session.IdleExpires, session.CreatedAt, session.LastSeenAt)
	return err
}

func (store *SQLStore) SessionByTokenHash(ctx context.Context, hash [32]byte, now time.Time) (Session, Account, error) {
	var session Session
	var account Account
	var rawHash []byte
	var pubkey string
	err := store.DB.QueryRowContext(ctx, `SELECT s.session_id,s.account_id,s.token_hash,s.auth_provider,s.expires_at,s.idle_expires_at,
		s.created_at,s.last_seen_at,s.revoked_at,a.nostr_pubkey,a.created_at,a.updated_at
		FROM `+store.table("sessions")+` s JOIN `+store.table("accounts")+` a ON a.account_id=s.account_id
		WHERE s.token_hash=$1 AND s.revoked_at IS NULL AND s.expires_at>$2 AND s.idle_expires_at>$2`, hash[:], now).Scan(
		&session.ID, &session.AccountID, &rawHash, &session.AuthProvider, &session.ExpiresAt, &session.IdleExpires,
		&session.CreatedAt, &session.LastSeenAt, &session.RevokedAt, &pubkey, &account.CreatedAt, &account.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return session, account, ErrNotFound
	}
	if err != nil {
		return session, account, err
	}
	copy(session.TokenHash[:], rawHash)
	account.ID = session.AccountID
	account.Pubkey, err = nostr.PubKeyFromHex(pubkey)
	return session, account, err
}

func (store *SQLStore) TouchSession(ctx context.Context, sessionID string, seenAt, idleExpires time.Time) error {
	_, err := store.DB.ExecContext(ctx, `UPDATE `+store.table("sessions")+` SET last_seen_at=$2,idle_expires_at=$3
		WHERE session_id=$1 AND revoked_at IS NULL`, sessionID, seenAt, idleExpires)
	return err
}

func (store *SQLStore) RevokeSession(ctx context.Context, sessionID string, now time.Time) error {
	_, err := store.DB.ExecContext(ctx, `UPDATE `+store.table("sessions")+` SET revoked_at=$2 WHERE session_id=$1 AND revoked_at IS NULL`, sessionID, now)
	return err
}

func (store *SQLStore) RevokeAccountSessions(ctx context.Context, accountID, exceptSession string, now time.Time) error {
	_, err := store.DB.ExecContext(ctx, `UPDATE `+store.table("sessions")+` SET revoked_at=$3
		WHERE account_id=$1 AND session_id<>$2 AND revoked_at IS NULL`, accountID, exceptSession, now)
	return err
}

func (store *SQLStore) SaveNostrChallenge(ctx context.Context, challenge NostrChallenge) error {
	_, err := store.DB.ExecContext(ctx, `INSERT INTO `+store.table("nostr_challenges")+` (challenge_hash,nostr_pubkey,expires_at)
		VALUES ($1,$2,$3)`, challenge.ChallengeHash[:], challenge.Pubkey.Hex(), challenge.ExpiresAt)
	return err
}

func (store *SQLStore) ConsumeNostrChallenge(ctx context.Context, hash [32]byte, pubkey nostr.PubKey, now time.Time) error {
	var value int
	err := store.DB.QueryRowContext(ctx, `DELETE FROM `+store.table("nostr_challenges")+`
		WHERE challenge_hash=$1 AND nostr_pubkey=$2 AND expires_at>$3 RETURNING 1`, hash[:], pubkey.Hex(), now).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
