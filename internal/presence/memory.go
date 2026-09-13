package presence

import (
	"context"
	"sync"
	"time"
)

type Memory struct {
	mu       sync.Mutex
	Now      func() time.Time
	accounts map[string]Account
	leases   map[string]map[string]Lease
	watchers map[chan string]struct{}
}

func NewMemory() *Memory {
	return &Memory{Now: time.Now, accounts: map[string]Account{}, leases: map[string]map[string]Lease{}, watchers: map[chan string]struct{}{}}
}

func (m *Memory) mutate(key string, update *Update, connection string) (Snapshot, error) {
	now := m.Now().Unix()
	a, exists := m.accounts[key]
	if !exists {
		a = NewAccount(key)
	}
	leases := m.leases[key]
	if leases == nil {
		leases = map[string]Lease{}
	}
	if update != nil {
		if err := apply(&a, leases, *update, now); err != nil {
			return Snapshot{}, err
		}
	}
	if connection != "" {
		delete(leases, connection)
	}
	previous := m.accounts[key]
	reconcile(&a, leases, now)
	if a.Status != previous.Status || a.Mode != previous.Mode || a.ExpiresAt != previous.ExpiresAt {
		a.Revision++
		a.UpdatedAt = now
		for watcher := range m.watchers {
			select {
			case watcher <- key:
			default:
			}
		}
	}
	m.accounts[key], m.leases[key] = a, leases
	return a.Snapshot(now), nil
}

func (m *Memory) Apply(ctx context.Context, u Update) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.mutate(u.PubKey, &u, "")
}

func (m *Memory) Snapshot(ctx context.Context, keys []string) ([]Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]Snapshot, 0, len(keys))
	for _, key := range keys {
		value, err := m.mutate(key, nil, "")
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, nil
}

func (m *Memory) RemoveConnection(ctx context.Context, connection string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key := range m.accounts {
		if _, err := m.mutate(key, nil, connection); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func (m *Memory) Expire(ctx context.Context) error { return m.RemoveConnection(ctx, "") }

func (m *Memory) Watch(ctx context.Context, notify func(string)) error {
	ch := make(chan string, 256)
	m.mu.Lock()
	m.watchers[ch] = struct{}{}
	m.mu.Unlock()
	defer func() { m.mu.Lock(); delete(m.watchers, ch); m.mu.Unlock() }()
	notify("")
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case key := <-ch:
			notify(key)
		}
	}
}
