package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgconn"
	"regexp"
	"time"
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

type sqlExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
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
