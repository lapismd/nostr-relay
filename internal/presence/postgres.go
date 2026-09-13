package presence

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var activeLeases = promauto.NewGaugeVec(prometheus.GaugeOpts{Name: "lapis_presence_active_leases", Help: "Current unexpired leases in the shared backend."}, []string{"scope"})
var cleanupLag = promauto.NewGaugeVec(prometheus.GaugeOpts{Name: "lapis_presence_cleanup_lag_seconds", Help: "Age of the oldest outstanding presence reconciliation."}, []string{"scope"})

type Postgres struct {
	db                             *sql.DB
	dsn, accounts, leases, channel string
}

func NewPostgres(db *sql.DB, dsn, schema string) (*Postgres, error) {
	schema = strings.ToLower(schema)
	if !regexp.MustCompile(`^[a-zA-Z0-9_-]+$`).MatchString(schema) {
		return nil, errors.New("invalid presence schema")
	}
	sum := sha256.Sum256([]byte(schema))
	return &Postgres{db, dsn, `"` + schema + `__presence_accounts"`, `"` + schema + `__presence_leases"`, "lapis_presence_" + hex.EncodeToString(sum[:16])}, nil
}

func (p *Postgres) mutate(ctx context.Context, key string, update *Update, connection string) (Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, err
	}
	defer tx.Rollback()
	a := NewAccount(key)
	initial, _ := json.Marshal(a)
	if _, err = tx.ExecContext(ctx, "INSERT INTO "+p.accounts+" (pubkey,state) VALUES ($1,$2) ON CONFLICT DO NOTHING", key, initial); err != nil {
		return Snapshot{}, err
	}
	var raw []byte
	if err = tx.QueryRowContext(ctx, "SELECT state FROM "+p.accounts+" WHERE pubkey=$1 FOR UPDATE", key).Scan(&raw); err != nil {
		return Snapshot{}, err
	}
	if err = json.Unmarshal(raw, &a); err != nil {
		return Snapshot{}, err
	}
	var now int64
	if err = tx.QueryRowContext(ctx, "SELECT floor(extract(epoch FROM clock_timestamp()))::bigint").Scan(&now); err != nil {
		return Snapshot{}, err
	}
	leases := map[string]Lease{}
	rows, err := tx.QueryContext(ctx, "SELECT connection_id::text,lease FROM "+p.leases+" WHERE pubkey=$1", key)
	if err != nil {
		return Snapshot{}, err
	}
	for rows.Next() {
		var id string
		var value []byte
		var lease Lease
		if err = rows.Scan(&id, &value); err == nil {
			err = json.Unmarshal(value, &lease)
		}
		if err != nil {
			rows.Close()
			return Snapshot{}, err
		}
		leases[id] = lease
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return Snapshot{}, err
	}
	previousStatus, previousMode, previousExpiry := a.Status, a.Mode, a.ExpiresAt
	if update != nil {
		if err = apply(&a, leases, *update, now); err != nil {
			return Snapshot{}, err
		}
	}
	if connection != "" {
		delete(leases, connection)
	}
	reconcile(&a, leases, now)
	changed := a.Status != previousStatus || a.Mode != previousMode || a.ExpiresAt != previousExpiry
	if changed {
		a.Revision++
		a.UpdatedAt = now
	}
	encoded, err := json.Marshal(a)
	if err != nil {
		return Snapshot{}, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE "+p.accounts+" SET state=$2,expires_at=$3 WHERE pubkey=$1", key, encoded, nextReconcile(a, leases)); err != nil {
		return Snapshot{}, err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM "+p.leases+" WHERE pubkey=$1 AND (retain_until <= $2 OR connection_id::text=$3)", key, now, connection); err != nil {
		return Snapshot{}, err
	}
	if update != nil {
		if lease, ok := leases[update.Connection]; ok {
			data, marshalErr := json.Marshal(lease)
			if marshalErr != nil {
				return Snapshot{}, marshalErr
			}
			_, err = tx.ExecContext(ctx, "INSERT INTO "+p.leases+" (pubkey,connection_id,lease,retain_until) VALUES ($1,$2,$3,$4) ON CONFLICT(pubkey,connection_id) DO UPDATE SET lease=excluded.lease,retain_until=excluded.retain_until", key, update.Connection, data, lease.RetainUntil)
			if err != nil {
				return Snapshot{}, err
			}
		}
	}
	if changed {
		if _, err = tx.ExecContext(ctx, "SELECT pg_notify($1,$2)", p.channel, key); err != nil {
			return Snapshot{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return Snapshot{}, err
	}
	return a.Snapshot(now), nil
}

func (p *Postgres) Apply(ctx context.Context, update Update) (Snapshot, error) {
	return p.mutate(ctx, update.PubKey, &update, "")
}

func (p *Postgres) Snapshot(ctx context.Context, keys []string) ([]Snapshot, error) {
	if len(keys) > MaxSubjects {
		return nil, errors.New("invalid: too many presence subjects")
	}
	result := make([]Snapshot, 0, len(keys))
	for _, key := range keys {
		value, err := p.mutate(ctx, key, nil, "")
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, nil
}

func (p *Postgres) keys(ctx context.Context, query string, args ...any) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rows, err := p.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func (p *Postgres) RemoveConnection(ctx context.Context, connection string) error {
	keys, err := p.keys(ctx, "SELECT DISTINCT pubkey FROM "+p.leases+" WHERE connection_id=$1", connection)
	if err != nil {
		return err
	}
	for _, key := range keys {
		if _, err = p.mutate(ctx, key, nil, connection); err != nil {
			return err
		}
	}
	return nil
}

func (p *Postgres) Expire(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	keys, err := p.keys(ctx, "SELECT pubkey FROM "+p.accounts+" WHERE expires_at > 0 AND expires_at <= extract(epoch FROM clock_timestamp()) ORDER BY expires_at LIMIT 500")
	if err != nil {
		return err
	}
	for _, key := range keys {
		if _, err = p.mutate(ctx, key, nil, ""); err != nil {
			return err
		}
	}
	_, err = p.db.ExecContext(ctx, "DELETE FROM "+p.leases+" WHERE (pubkey,connection_id) IN (SELECT pubkey,connection_id FROM "+p.leases+" WHERE retain_until <= extract(epoch FROM clock_timestamp()) LIMIT 500)")
	if err != nil {
		return err
	}
	var count, lag float64
	err = p.db.QueryRowContext(ctx, "SELECT (SELECT count(*) FROM "+p.leases+" WHERE (lease->>'ExpiresAt')::bigint > extract(epoch FROM clock_timestamp())), (SELECT coalesce(max(extract(epoch FROM clock_timestamp())-expires_at),0) FROM "+p.accounts+" WHERE expires_at > 0 AND expires_at < extract(epoch FROM clock_timestamp()))").Scan(&count, &lag)
	if err == nil {
		activeLeases.WithLabelValues(p.channel).Set(count)
		cleanupLag.WithLabelValues(p.channel).Set(lag)
	}
	return err
}

func (p *Postgres) Watch(ctx context.Context, notify func(string)) error {
	conn, err := pgx.Connect(ctx, p.dsn)
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = conn.Close(closeCtx)
	}()
	if _, err = conn.Exec(ctx, "LISTEN "+p.channel); err != nil {
		return err
	}
	notify("")
	for {
		message, err := conn.WaitForNotification(ctx)
		if err != nil {
			return fmt.Errorf("presence notifications: %w", err)
		}
		notify(message.Payload)
	}
}
