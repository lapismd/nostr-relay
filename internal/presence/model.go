// Package presence owns short-lived connection leases and account preferences.
// Backends persist projections only, never the signed ephemeral event envelope.
package presence

import (
	"context"
	"errors"
	"sort"
)

const TTL int64 = 180
const ReplayWindow int64 = 300
const MaxSubjects = 200

type Update struct {
	PubKey, Connection, Status, Mode, EventID string
	CreatedAt, Sequence                       int64
}

type Lease struct {
	Status                                      string
	CreatedAt, Sequence, ExpiresAt, RetainUntil int64
}

type Account struct {
	PubKey                         string
	Mode                           string
	Status                         string
	Revision, UpdatedAt, ExpiresAt int64
	ModeCreatedAt, ModeSequence    int64
	Seen                           map[string]int64
}

type Snapshot struct {
	PubKey, Mode, Status           string
	Revision, UpdatedAt, ExpiresAt int64
}

// Watch calls notify with an empty key after connecting, allowing the caller to
// reconcile its watched subjects before trusting notification delivery again.
type Backend interface {
	Apply(context.Context, Update) (Snapshot, error)
	Snapshot(context.Context, []string) ([]Snapshot, error)
	RemoveConnection(context.Context, string) error
	Expire(context.Context) error
	Watch(context.Context, func(string)) error
}

func NewAccount(pubkey string) Account {
	return Account{PubKey: pubkey, Mode: "auto", Status: "offline", Seen: map[string]int64{}}
}

func (a Account) Snapshot(now int64) Snapshot {
	expires := a.ExpiresAt
	if expires == 0 {
		expires = now + TTL
	}
	return Snapshot{a.PubKey, a.Mode, a.Status, a.Revision, now, expires}
}

func Validate(u Update, now int64) error {
	if u.PubKey == "" || u.Connection == "" || u.EventID == "" || len(u.Status) > 128 || u.Status == "" {
		return errors.New("invalid: malformed presence update")
	}
	if u.CreatedAt < now-ReplayWindow || u.CreatedAt > now+30 || u.Sequence < 0 {
		return errors.New("invalid: presence timestamp is not fresh")
	}
	if u.Mode != "" && u.Mode != "auto" && u.Mode != "away" && u.Mode != "offline" {
		return errors.New("invalid: unsupported presence mode")
	}
	return nil
}

func apply(a *Account, leases map[string]Lease, u Update, now int64) error {
	if err := Validate(u, now); err != nil {
		return err
	}
	for id, at := range a.Seen {
		if at < now-ReplayWindow {
			delete(a.Seen, id)
		}
	}
	if _, seen := a.Seen[u.EventID]; seen {
		return nil
	}
	old, exists := leases[u.Connection]
	if exists && (u.CreatedAt < old.CreatedAt || (u.CreatedAt == old.CreatedAt && u.Sequence > 0 && u.Sequence <= old.Sequence)) {
		return nil
	}
	if len(a.Seen) >= 600 {
		return errors.New("rate-limited: presence update limit exceeded")
	}
	if !exists && len(leases) >= 64 {
		return errors.New("rate-limited: too many presence sessions")
	}
	a.Seen[u.EventID] = now
	expires := now + TTL
	if u.Status == "offline" {
		expires = 0
	}
	leases[u.Connection] = Lease{u.Status, u.CreatedAt, u.Sequence, expires, now + ReplayWindow}
	if u.Mode != "" && (u.CreatedAt > a.ModeCreatedAt || (u.CreatedAt == a.ModeCreatedAt && u.Sequence >= a.ModeSequence)) {
		a.Mode, a.ModeCreatedAt, a.ModeSequence = u.Mode, u.CreatedAt, u.Sequence
	}
	return nil
}

func reconcile(a *Account, leases map[string]Lease, now int64) {
	for id, at := range a.Seen {
		if at < now-ReplayWindow {
			delete(a.Seen, id)
		}
	}
	status, expires := "offline", int64(0)
	keys := make([]string, 0, len(leases))
	for key := range leases {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	latest := int64(0)
	for _, key := range keys {
		lease := leases[key]
		if lease.RetainUntil <= now {
			delete(leases, key)
			continue
		}
		if lease.ExpiresAt <= now {
			continue
		}
		// Revisit at the earliest lease expiry, including online -> away changes.
		if expires == 0 || lease.ExpiresAt < expires {
			expires = lease.ExpiresAt
		}
		if lease.Status == "online" {
			status = "online"
		} else if status != "online" {
			if lease.Status == "away" {
				status = "away"
			} else if status != "away" && lease.CreatedAt >= latest {
				status = lease.Status
				latest = lease.CreatedAt
			}
		}
	}
	if a.Mode == "offline" {
		status = "offline"
	} else if a.Mode == "away" && expires > now {
		status = "away"
	}
	a.Status, a.ExpiresAt = status, expires
}

func nextReconcile(a Account, leases map[string]Lease) int64 {
	next := a.ExpiresAt
	for _, at := range a.Seen {
		deadline := at + ReplayWindow + 1
		if next == 0 || deadline < next {
			next = deadline
		}
	}
	for _, lease := range leases {
		if next == 0 || lease.RetainUntil < next {
			next = lease.RetainUntil
		}
	}
	return next
}
