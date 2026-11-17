package auth

import (
	"context"
	"database/sql"
	"errors"
	"fiatjaf.com/nostr"
	"time"
)

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
