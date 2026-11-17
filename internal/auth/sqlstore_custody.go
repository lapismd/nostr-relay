package auth

import (
	"context"
	"database/sql"
	"errors"
	"fiatjaf.com/nostr"
	"time"
)

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
