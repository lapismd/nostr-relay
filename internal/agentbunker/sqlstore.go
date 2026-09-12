package agentbunker

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"time"

	"fiatjaf.com/nostr"
)

type SQLStore struct {
	db     *sql.DB
	prefix string
}

func NewSQLStore(db *sql.DB, schema string) (*SQLStore, error) {
	if db == nil || schema == "" {
		return nil, errors.New("managed agent database and schema are required")
	}
	return &SQLStore{db: db, prefix: schema + "__"}, nil
}
func (s *SQLStore) table(name string) string { return s.prefix + name }
func (s *SQLStore) CreateAgent(ctx context.Context, agent Agent, client Client) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO `+s.table("managed_agents")+` (pubkey,owner_pubkey,relay_pubkey,status,profile_content,profile_created_at,encrypted_secret_key,encrypted_data_key,encryption_version,created_at,expires_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, agent.Pubkey, agent.OwnerPubkey, agent.RelayPubkey, agent.Status, agent.ProfileContent, agent.ProfileCreatedAt, agent.Key.Ciphertext, agent.Key.WrappedKey, agent.Key.Version, agent.CreatedAt, agent.ExpiresAt)
	if err != nil {
		return mapConflict(err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO `+s.table("managed_agent_clients")+` (agent_pubkey,client_pubkey,status,invitation_hash,invitation_ciphertext,invitation_data_key,invitation_key_version,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, client.AgentPubkey, client.ClientPubkey, client.Status, client.InvitationHash, client.Invitation.Ciphertext, client.Invitation.WrappedKey, client.Invitation.Version, client.CreatedAt)
	if err != nil {
		return mapConflict(err)
	}
	return tx.Commit()
}
func (s *SQLStore) Agent(ctx context.Context, pubkey string) (Agent, error) {
	var a Agent
	var activated sql.NullTime
	var ownerRaw, profileRaw []byte
	err := s.db.QueryRowContext(ctx, `SELECT pubkey,owner_pubkey,relay_pubkey,status,profile_content,profile_created_at,encrypted_secret_key,encrypted_data_key,encryption_version,created_at,expires_at,activated_at,owner_event,profile_event FROM `+s.table("managed_agents")+` WHERE pubkey=$1`, pubkey).Scan(&a.Pubkey, &a.OwnerPubkey, &a.RelayPubkey, &a.Status, &a.ProfileContent, &a.ProfileCreatedAt, &a.Key.Ciphertext, &a.Key.WrappedKey, &a.Key.Version, &a.CreatedAt, &a.ExpiresAt, &activated, &ownerRaw, &profileRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	if err != nil {
		return a, err
	}
	if activated.Valid {
		a.ActivatedAt = &activated.Time
	}
	if len(ownerRaw) > 0 {
		var e nostr.Event
		if json.Unmarshal(ownerRaw, &e) == nil {
			a.OwnerEvent = &e
		}
	}
	if len(profileRaw) > 0 {
		var e nostr.Event
		if json.Unmarshal(profileRaw, &e) == nil {
			a.ProfileEvent = &e
		}
	}
	return a, nil
}
func (s *SQLStore) Client(ctx context.Context, agent, client string) (Client, error) {
	var c Client
	var paired, revoked sql.NullTime
	err := s.db.QueryRowContext(ctx, `SELECT agent_pubkey,client_pubkey,status,invitation_hash,invitation_ciphertext,invitation_data_key,invitation_key_version,created_at,paired_at,revoked_at FROM `+s.table("managed_agent_clients")+` WHERE agent_pubkey=$1 AND client_pubkey=$2`, agent, client).Scan(&c.AgentPubkey, &c.ClientPubkey, &c.Status, &c.InvitationHash, &c.Invitation.Ciphertext, &c.Invitation.WrappedKey, &c.Invitation.Version, &c.CreatedAt, &paired, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	if paired.Valid {
		c.PairedAt = &paired.Time
	}
	if revoked.Valid {
		c.RevokedAt = &revoked.Time
	}
	return c, err
}
func (s *SQLStore) ListAgents(ctx context.Context, owner string) ([]AgentView, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT a.pubkey,a.owner_pubkey,a.status,CASE WHEN BOOL_OR(c.status='pending') AND BOOL_OR(c.status='active') THEN 'replacement_pending' WHEN BOOL_OR(c.status='pending') THEN 'pending' WHEN BOOL_OR(c.status='active') THEN 'active' ELSE 'none' END,a.created_at,a.expires_at,a.activated_at FROM `+s.table("managed_agents")+` a LEFT JOIN `+s.table("managed_agent_clients")+` c ON c.agent_pubkey=a.pubkey AND c.status IN ('active','pending') WHERE a.owner_pubkey=$1 GROUP BY a.pubkey,a.owner_pubkey,a.status,a.created_at,a.expires_at,a.activated_at ORDER BY a.created_at`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AgentView{}
	for rows.Next() {
		var v AgentView
		var activated sql.NullTime
		if err := rows.Scan(&v.Pubkey, &v.OwnerPubkey, &v.Status, &v.ConnectionState, &v.CreatedAt, &v.ExpiresAt, &activated); err != nil {
			return nil, err
		}
		if activated.Valid {
			v.ActivatedAt = &activated.Time
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *SQLStore) OwnerCounts(ctx context.Context, owner string, since time.Time) (int, int, int, error) {
	var a, p, r int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FILTER (WHERE status='active'),COUNT(*) FILTER (WHERE status='pending'),COUNT(*) FILTER (WHERE created_at >= $2) FROM `+s.table("managed_agents")+` WHERE owner_pubkey=$1`, owner, since).Scan(&a, &p, &r)
	return a, p, r, err
}
func (s *SQLStore) Activate(ctx context.Context, pubkey string, ownerEvent, profileEvent []byte, now time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE `+s.table("managed_agents")+` SET status='active',activated_at=$2,owner_event=$3,profile_event=$4 WHERE pubkey=$1 AND status='pending'`, pubkey, now, ownerEvent, profileEvent)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrConflict
	}
	return nil
}
func (s *SQLStore) PairClient(ctx context.Context, agent, client string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE `+s.table("managed_agent_clients")+` SET status='revoked',revoked_at=$2 WHERE agent_pubkey=$1 AND status='active' AND client_pubkey<>$3`, agent, now, client); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE `+s.table("managed_agent_clients")+` SET status='active',paired_at=COALESCE(paired_at,$3),invitation_hash='',invitation_ciphertext='',invitation_data_key='',invitation_key_version='' WHERE agent_pubkey=$1 AND client_pubkey=$2 AND status IN ('pending','active')`, agent, client, now)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrConflict
	}
	return tx.Commit()
}
func (s *SQLStore) RevokeCurrent(ctx context.Context, agent string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE `+s.table("managed_agent_clients")+` SET status='revoked',revoked_at=$2 WHERE agent_pubkey=$1 AND status='active'`, agent, now)
	return err
}
func (s *SQLStore) DeleteAgent(ctx context.Context, agent string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM `+s.table("managed_agents")+` WHERE pubkey=$1`, agent)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrNotFound
	}
	return nil
}
func (s *SQLStore) SaveReplacement(ctx context.Context, c Client) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO `+s.table("managed_agent_clients")+` (agent_pubkey,client_pubkey,status,invitation_hash,invitation_ciphertext,invitation_data_key,invitation_key_version,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (agent_pubkey,client_pubkey) DO UPDATE SET status=EXCLUDED.status,invitation_hash=EXCLUDED.invitation_hash,invitation_ciphertext=EXCLUDED.invitation_ciphertext,invitation_data_key=EXCLUDED.invitation_data_key,invitation_key_version=EXCLUDED.invitation_key_version,created_at=EXCLUDED.created_at,paired_at=NULL,revoked_at=NULL`, c.AgentPubkey, c.ClientPubkey, c.Status, c.InvitationHash, c.Invitation.Ciphertext, c.Invitation.WrappedKey, c.Invitation.Version, c.CreatedAt)
	return err
}
func (s *SQLStore) CachedRequest(ctx context.Context, a, c, id string) (CachedRequest, error) {
	var x CachedRequest
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT content_hash,response_event FROM `+s.table("managed_agent_requests")+` WHERE agent_pubkey=$1 AND client_pubkey=$2 AND request_id=$3`, a, c, id).Scan(&x.ContentHash, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return x, ErrNotFound
	}
	if err == nil {
		err = json.Unmarshal(raw, &x.ResponseEvent)
	}
	return x, err
}
func (s *SQLStore) SaveRequest(ctx context.Context, a, c, id string, x CachedRequest, now time.Time) error {
	raw, _ := json.Marshal(x.ResponseEvent)
	_, err := s.db.ExecContext(ctx, `INSERT INTO `+s.table("managed_agent_requests")+` (agent_pubkey,client_pubkey,request_id,content_hash,response_event,created_at) VALUES ($1,$2,$3,$4,$5,$6)`, a, c, id, x.ContentHash, raw, now)
	return mapConflict(err)
}
func (s *SQLStore) UseAuthEvent(ctx context.Context, id string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO `+s.table("managed_agent_auth_events")+` (event_id,used_at) VALUES ($1,$2)`, id, now)
	return mapConflict(err)
}
func (s *SQLStore) HTTPResult(ctx context.Context, o, k string) (string, []byte, error) {
	var h string
	var b []byte
	err := s.db.QueryRowContext(ctx, `SELECT request_hash,response_json FROM `+s.table("managed_agent_http_idempotency")+` WHERE owner_pubkey=$1 AND idempotency_key=$2`, o, k).Scan(&h, &b)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, ErrNotFound
	}
	return h, b, err
}
func (s *SQLStore) SaveHTTPResult(ctx context.Context, o, k, h string, b []byte, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO `+s.table("managed_agent_http_idempotency")+` (owner_pubkey,idempotency_key,request_hash,response_json,created_at) VALUES ($1,$2,$3,$4,$5)`, o, k, h, b, now)
	return mapConflict(err)
}
func (s *SQLStore) Audit(ctx context.Context, a Audit) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO `+s.table("managed_agent_audit")+` (owner_pubkey,agent_pubkey,client_pubkey,operation,result,created_at) VALUES ($1,$2,$3,$4,$5,$6)`, a.OwnerPubkey, a.AgentPubkey, a.ClientPubkey, a.Operation, a.Result, a.CreatedAt)
	return err
}
func mapConflict(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return ErrConflict
	}
	return err
}
