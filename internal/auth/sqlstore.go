package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

var sqlPrefixPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

type SQLStore struct {
	DB     *sql.DB
	Prefix string
}

func NewSQLStore(db *sql.DB, prefix string) (*SQLStore, error) {
	if db == nil || !sqlPrefixPattern.MatchString(prefix) {
		return nil, errors.New("invalid auth SQL store configuration")
	}
	return &SQLStore{DB: db, Prefix: prefix}, nil
}

func (store *SQLStore) table(suffix string) string { return store.Prefix + "__auth_" + suffix }

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

type sqlExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
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

func (store *SQLStore) SaveGitHubCredential(ctx context.Context, credential GitHubCredential) error {
	_, err := store.DB.ExecContext(ctx, `INSERT INTO `+store.table("github_credentials")+`
		(account_id,github_user_id,github_login,encrypted_access_token,encrypted_refresh_token,
		 access_token_expires_at,refresh_token_expires_at,app_client_id,status,updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (account_id) DO UPDATE SET github_user_id=EXCLUDED.github_user_id,
		github_login=EXCLUDED.github_login,encrypted_access_token=EXCLUDED.encrypted_access_token,
		encrypted_refresh_token=EXCLUDED.encrypted_refresh_token,access_token_expires_at=EXCLUDED.access_token_expires_at,
		refresh_token_expires_at=EXCLUDED.refresh_token_expires_at,app_client_id=EXCLUDED.app_client_id,
		status=EXCLUDED.status,updated_at=EXCLUDED.updated_at`, credential.AccountID, credential.GitHubUserID,
		credential.Login, credential.EncryptedAccessToken, credential.EncryptedRefreshToken,
		nullableTime(credential.AccessTokenExpiresAt), nullableTime(credential.RefreshTokenExpiresAt), credential.AppClientID,
		credential.Status, credential.UpdatedAt)
	return classifyConflict(err)
}

func (store *SQLStore) UpdateGitHubCredentialStatus(ctx context.Context, accountID, status string, now time.Time) error {
	_, err := store.DB.ExecContext(ctx, `UPDATE `+store.table("github_credentials")+`
		SET status=$2,updated_at=$3 WHERE account_id=$1`, accountID, status, now)
	return err
}

func (store *SQLStore) GitHubCredentialByPubkey(ctx context.Context, pubkey nostr.PubKey) (GitHubCredential, error) {
	return store.scanGitHubCredential(store.DB.QueryRowContext(ctx, `SELECT c.account_id,c.github_user_id,c.github_login,
		c.encrypted_access_token,c.encrypted_refresh_token,c.access_token_expires_at,c.refresh_token_expires_at,
		c.app_client_id,c.status,c.updated_at FROM `+store.table("github_credentials")+` c
		JOIN `+store.table("accounts")+` a ON a.account_id=c.account_id WHERE a.nostr_pubkey=$1`, pubkey.Hex()))
}

func (store *SQLStore) GitHubCredentialByAccount(ctx context.Context, accountID string) (GitHubCredential, error) {
	return store.scanGitHubCredential(store.DB.QueryRowContext(ctx, `SELECT account_id,github_user_id,github_login,
		encrypted_access_token,encrypted_refresh_token,access_token_expires_at,refresh_token_expires_at,
		app_client_id,status,updated_at FROM `+store.table("github_credentials")+` WHERE account_id=$1`, accountID))
}

func (store *SQLStore) scanGitHubCredential(row interface{ Scan(...any) error }) (GitHubCredential, error) {
	var credential GitHubCredential
	var accessExpiry, refreshExpiry sql.NullTime
	err := row.Scan(&credential.AccountID, &credential.GitHubUserID, &credential.Login,
		&credential.EncryptedAccessToken, &credential.EncryptedRefreshToken, &accessExpiry, &refreshExpiry,
		&credential.AppClientID, &credential.Status, &credential.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return credential, ErrNotFound
	}
	if accessExpiry.Valid {
		credential.AccessTokenExpiresAt = accessExpiry.Time
	}
	if refreshExpiry.Valid {
		credential.RefreshTokenExpiresAt = refreshExpiry.Time
	}
	return credential, err
}

func (store *SQLStore) DisconnectGitHubCredential(ctx context.Context, accountID string, now time.Time) error {
	_, err := store.DB.ExecContext(ctx, `UPDATE `+store.table("github_credentials")+`
		SET encrypted_access_token='',encrypted_refresh_token='',status='disconnected',updated_at=$2 WHERE account_id=$1`, accountID, now)
	return err
}

func (store *SQLStore) PasskeysByAccount(ctx context.Context, accountID string) ([]PasskeyRecord, error) {
	rows, err := store.DB.QueryContext(ctx, `SELECT account_id,credential_id_hash,encrypted_credential,label,created_at,last_used_at
		FROM `+store.table("passkeys")+` WHERE account_id=$1 ORDER BY created_at`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := make([]PasskeyRecord, 0)
	for rows.Next() {
		var record PasskeyRecord
		var rawHash []byte
		var lastUsed sql.NullTime
		if err := rows.Scan(&record.AccountID, &rawHash, &record.EncryptedCredential, &record.Label, &record.CreatedAt, &lastUsed); err != nil {
			return nil, err
		}
		if len(rawHash) != len(record.CredentialIDHash) {
			return nil, errors.New("invalid passkey credential hash")
		}
		copy(record.CredentialIDHash[:], rawHash)
		if lastUsed.Valid {
			record.LastUsedAt = lastUsed.Time
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (store *SQLStore) SavePasskey(ctx context.Context, record PasskeyRecord) error {
	_, err := store.DB.ExecContext(ctx, `INSERT INTO `+store.table("passkeys")+`
		(account_id,credential_id_hash,encrypted_credential,label,created_at,last_used_at)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (account_id,credential_id_hash) DO UPDATE SET
		encrypted_credential=EXCLUDED.encrypted_credential,label=EXCLUDED.label,last_used_at=EXCLUDED.last_used_at`,
		record.AccountID, record.CredentialIDHash[:], record.EncryptedCredential, record.Label,
		record.CreatedAt, nullableTime(record.LastUsedAt))
	return classifyConflict(err)
}

func (store *SQLStore) BeginWebAuthnFlow(ctx context.Context, flow WebAuthnFlow) error {
	_, err := store.DB.ExecContext(ctx, `INSERT INTO `+store.table("webauthn_flows")+`
		(flow_hash,account_id,flow_kind,encrypted_session,device_public_key,expires_at)
		VALUES ($1,$2,$3,$4,$5,$6)`, flow.FlowHash[:], flow.AccountID, flow.Kind,
		flow.EncryptedSession, nullableBytes(flow.DevicePublicKey), flow.ExpiresAt)
	return err
}

func (store *SQLStore) ConsumeWebAuthnFlow(ctx context.Context, hash [32]byte, kind, accountID string, now time.Time) (WebAuthnFlow, error) {
	var flow WebAuthnFlow
	var rawHash []byte
	err := store.DB.QueryRowContext(ctx, `DELETE FROM `+store.table("webauthn_flows")+`
		WHERE flow_hash=$1 AND flow_kind=$2 AND account_id=$3 AND expires_at>$4
		RETURNING flow_hash,account_id,flow_kind,encrypted_session,device_public_key,expires_at`,
		hash[:], kind, accountID, now).Scan(&rawHash, &flow.AccountID, &flow.Kind,
		&flow.EncryptedSession, &flow.DevicePublicKey, &flow.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return flow, ErrNotFound
	}
	copy(flow.FlowHash[:], rawHash)
	return flow, err
}

func (store *SQLStore) CreateCustodyTransfer(ctx context.Context, transfer CustodyTransfer) error {
	tx, err := store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE `+store.table("signer_bindings")+`
		SET status='migration_pending',updated_at=$2
		WHERE account_id=$1 AND signer_type='managed' AND status IN ('active','migration_pending')`, transfer.AccountID, transfer.CreatedAt)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return ErrSigningDenied
	}
	if _, err = tx.ExecContext(ctx, `UPDATE `+store.table("custody_transfers")+`
		SET transfer_state='cancelled' WHERE account_id=$1 AND transfer_state='key_received_by_device'`, transfer.AccountID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO `+store.table("custody_transfers")+`
		(transfer_id,account_id,challenge_hash,transfer_state,expires_at,created_at)
		VALUES ($1,$2,$3,$4,$5,$6)`, transfer.ID, transfer.AccountID, transfer.ChallengeHash[:],
		transfer.State, transfer.ExpiresAt, transfer.CreatedAt); err != nil {
		return err
	}
	return tx.Commit()
}

func (store *SQLStore) CustodyTransferByID(ctx context.Context, transferID, accountID string, now time.Time) (CustodyTransfer, error) {
	var transfer CustodyTransfer
	var rawHash []byte
	err := store.DB.QueryRowContext(ctx, `SELECT transfer_id,account_id,challenge_hash,transfer_state,expires_at,created_at,completed_at
		FROM `+store.table("custody_transfers")+`
		WHERE transfer_id=$1 AND account_id=$2 AND expires_at>$3`, transferID, accountID, now).Scan(
		&transfer.ID, &transfer.AccountID, &rawHash, &transfer.State, &transfer.ExpiresAt, &transfer.CreatedAt, &transfer.CompletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return transfer, ErrNotFound
	}
	copy(transfer.ChallengeHash[:], rawHash)
	return transfer, err
}

func (store *SQLStore) CompleteCustodyTransfer(ctx context.Context, transferID, accountID string, pubkey nostr.PubKey, metadata []byte, now time.Time) error {
	tx, err := store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state string
	if err = tx.QueryRowContext(ctx, `SELECT transfer_state FROM `+store.table("custody_transfers")+`
		WHERE transfer_id=$1 AND account_id=$2 AND expires_at>$3 FOR UPDATE`, transferID, accountID, now).Scan(&state); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if state != "key_received_by_device" {
		return ErrSigningDenied
	}
	result, err := tx.ExecContext(ctx, `UPDATE `+store.table("signer_bindings")+`
		SET signer_type='nip46',status='active',metadata=$3,updated_at=$4
		WHERE account_id=$1 AND public_key=$2 AND signer_type='managed' AND status='migration_pending'`,
		accountID, pubkey.Hex(), metadata, now)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return ErrSigningDenied
	}
	result, err = tx.ExecContext(ctx, `DELETE FROM `+store.table("managed_keys")+` WHERE account_id=$1 AND public_key=$2`, accountID, pubkey.Hex())
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return ErrSigningDenied
	}
	if _, err = tx.ExecContext(ctx, `UPDATE `+store.table("custody_transfers")+`
		SET transfer_state='complete',completed_at=$3 WHERE transfer_id=$1 AND account_id=$2`, transferID, accountID, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (store *SQLStore) SaveAuditEvent(ctx context.Context, event AuditEvent) error {
	pubkey := ""
	if event.Pubkey != nostr.ZeroPK {
		pubkey = event.Pubkey.Hex()
	}
	_, err := store.DB.ExecContext(ctx, `INSERT INTO `+store.table("audit")+`
		(audit_id,account_id,nostr_pubkey,auth_provider,signer_type,operation,github_actor_id,
		 github_credential_type,source_event_id,correlation_id,result,created_at)
		VALUES ($1,NULLIF($2,''),$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, event.ID, event.AccountID,
		pubkey, event.AuthProvider, event.SignerType, event.Operation, event.GitHubActorID,
		event.GitHubCredentialType, event.SourceEventID, event.CorrelationID, event.Result, event.CreatedAt)
	return err
}

func classifyConflict(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrIdentityConflict
	}
	return err
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}

func nullableBytes(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}

func (store *SQLStore) String() string { return fmt.Sprintf("auth SQL store %s", store.Prefix) }
