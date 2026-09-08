package zooid

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"fiatjaf.com/nostr"
	"zooid/internal/projecttask"
)

// ProjectTaskPolicy is Zooid's single adapter between the relay/store and the
// transport-independent projecttask domain. It intentionally does not alter
// internal/khatru.
type ProjectTaskPolicy struct {
	Events   *EventStore
	Groups   *GroupStore
	Resolver projecttask.ScopeResolver
}

func NewProjectTaskPolicy(events *EventStore, groups *GroupStore) *ProjectTaskPolicy {
	policy := &ProjectTaskPolicy{Events: events, Groups: groups}
	policy.Resolver = projecttask.ScopeResolver{Reader: policy}
	return policy
}

func (policy *ProjectTaskPolicy) EventByID(ctx context.Context, id nostr.ID) (nostr.Event, bool, error) {
	for event := range policy.Events.QueryEventsBefore(ctx, nostr.Filter{IDs: []nostr.ID{id}}, "", 1) {
		return event, true, nil
	}
	return nostr.Event{}, false, nil
}

func (policy *ProjectTaskPolicy) EventByAddress(
	ctx context.Context,
	kind nostr.Kind,
	owner nostr.PubKey,
	id string,
) (nostr.Event, bool, error) {
	filter := nostr.Filter{
		Kinds:   []nostr.Kind{kind},
		Authors: []nostr.PubKey{owner},
		Tags:    nostr.TagMap{"d": {id}},
		Limit:   1,
	}
	for event := range policy.Events.QueryEventsBefore(ctx, filter, "", 1) {
		return event, true, nil
	}
	return nostr.Event{}, false, nil
}

// CheckWrite validates the event shape, resolves its derived channel, and
// applies ordinary NIP-29 write policy to an internal copy.
func (policy *ProjectTaskPolicy) CheckWrite(ctx context.Context, event nostr.Event) (bool, string) {
	recognized, err := projecttask.ValidateEvent(event)
	if err != nil {
		return true, "invalid: " + err.Error()
	}
	if !recognized {
		return false, ""
	}
	scope, err := policy.Resolver.Resolve(ctx, event)
	if err != nil {
		return true, "invalid: " + err.Error()
	}
	if scope.Channel == "" {
		return true, ""
	}
	derived := withDerivedGroup(event, scope.Channel)
	if reason := policy.Groups.CheckWrite(derived); reason != "" {
		return true, reason
	}
	return true, ""
}

func (policy *ProjectTaskPolicy) CanRead(ctx context.Context, pubkey nostr.PubKey, event nostr.Event) bool {
	scope, err := policy.resolveStored(ctx, event)
	if err != nil {
		return false
	}
	if !scope.Relevant || scope.Channel == "" {
		return true
	}
	return policy.Groups.CanRead(pubkey, withDerivedGroup(event, scope.Channel))
}

func (policy *ProjectTaskPolicy) CanAnonymousRead(ctx context.Context, event nostr.Event) bool {
	scope, err := policy.resolveStored(ctx, event)
	if err != nil || !scope.Relevant || scope.Channel == "" {
		return false
	}
	metadata, found := policy.Groups.GetMetadata(scope.Channel)
	return found && !HasTag(metadata.Tags, "private") && !HasTag(metadata.Tags, "hidden")
}

// Remember persists a derived scope before the upstream WebSocket deletion
// path removes an addressed target. This keeps tombstones readable after a
// restart and also amortizes ancestry lookups for normal reads.
func (policy *ProjectTaskPolicy) Remember(ctx context.Context, event nostr.Event) error {
	scope, err := policy.Resolver.Resolve(ctx, event)
	if err != nil || !scope.Relevant || scope.Channel == "" {
		return err
	}
	return policy.storeScope(ctx, event.ID, scope.Channel)
}

func (policy *ProjectTaskPolicy) resolveStored(ctx context.Context, event nostr.Event) (projecttask.ResolvedScope, error) {
	if event.ID != nostr.ZeroID {
		channel, found, err := policy.loadScope(ctx, event.ID)
		if err != nil {
			return projecttask.ResolvedScope{}, err
		}
		if found {
			return projecttask.ResolvedScope{Relevant: true, Channel: channel}, nil
		}
	}
	scope, err := policy.Resolver.Resolve(ctx, event)
	if err == nil && scope.Relevant && scope.Channel != "" && event.ID != nostr.ZeroID {
		_ = policy.storeScope(ctx, event.ID, scope.Channel)
	}
	return scope, err
}

func (policy *ProjectTaskPolicy) loadScope(ctx context.Context, id nostr.ID) (string, bool, error) {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	var channel string
	err := GetDb().QueryRowContext(queryCtx,
		"SELECT channel FROM "+policy.Events.Schema.Prefix("project_task_scopes")+" WHERE event_id = $1",
		id.Hex()).Scan(&channel)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return channel, true, nil
}

func (policy *ProjectTaskPolicy) storeScope(ctx context.Context, id nostr.ID, channel string) error {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	_, err := sb.Insert(policy.Events.Schema.Prefix("project_task_scopes")).
		Columns("event_id", "channel").
		Values(id.Hex(), channel).
		Suffix("ON CONFLICT(event_id) DO UPDATE SET channel = EXCLUDED.channel").
		RunWith(GetDb()).
		ExecContext(queryCtx)
	return err
}

// IsTombstoned implements visible NIP-09 address deletion for both WebSocket
// and HTTP publication. The upstream WebSocket path may additionally remove
// the stored row; this projection covers the generic HTTP path.
func (policy *ProjectTaskPolicy) IsTombstoned(ctx context.Context, event nostr.Event) bool {
	if event.Kind != projecttask.KindProject && event.Kind != nostr.KindRepositoryAnnouncement {
		return false
	}
	id := event.Tags.GetD()
	if id == "" {
		return false
	}
	address := fmt.Sprintf("%d:%s:%s", event.Kind, event.PubKey.Hex(), id)
	filter := nostr.Filter{
		Kinds: []nostr.Kind{nostr.KindDeletion},
		Tags:  nostr.TagMap{"a": {address}},
		Since: event.CreatedAt,
	}
	for tombstone := range policy.Events.QueryEventsBefore(ctx, filter, "", 0) {
		if tombstone.PubKey == event.PubKey && tombstone.CreatedAt >= event.CreatedAt {
			return true
		}
	}
	return false
}

func withDerivedGroup(event nostr.Event, channel string) nostr.Event {
	copy := event
	copy.Tags = event.Tags.CloneDeep()
	if copy.Tags.FindWithValue("h", channel) == nil {
		copy.Tags = append(copy.Tags, nostr.Tag{"h", channel})
	}
	return copy
}
