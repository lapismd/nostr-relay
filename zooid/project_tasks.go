package zooid

import (
	"context"
	"fmt"
	"slices"

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

// CheckWrite validates the event shape, resolves its derived channel
// intersection, and applies ordinary NIP-29 write policy to one internal copy
// per channel.
func (policy *ProjectTaskPolicy) CheckWrite(ctx context.Context, event nostr.Event) (bool, string) {
	recognized, err := projecttask.ValidateEvent(event)
	if err != nil {
		return true, "invalid: " + err.Error()
	}
	var scope projecttask.ResolvedScope
	if !recognized && event.Kind == nostr.KindDeletion && event.Tags.Find("e") != nil {
		scope, err = policy.Resolver.Resolve(ctx, event)
		if err != nil {
			if scope.Relevant {
				return true, "invalid: " + err.Error()
			}
			return false, ""
		}
		if !scope.Relevant {
			return false, ""
		}
		recognized = true
	}
	if !recognized {
		return false, ""
	}
	if scope.Relevant == false {
		scope, err = policy.Resolver.Resolve(ctx, event)
	}
	if err != nil {
		return true, "invalid: " + err.Error()
	}
	for _, channel := range scope.Channels {
		if reason := policy.Groups.CheckWrite(withOnlyDerivedGroup(event, channel)); reason != "" {
			return true, reason
		}
	}
	return true, ""
}

func (policy *ProjectTaskPolicy) CanRead(ctx context.Context, pubkey nostr.PubKey, event nostr.Event) bool {
	scope, err := policy.resolveStored(ctx, event)
	if err != nil {
		return false
	}
	if !scope.Relevant {
		return true
	}
	for _, channel := range scope.Channels {
		if !policy.Groups.CanRead(pubkey, withOnlyDerivedGroup(event, channel)) {
			return false
		}
	}
	return true
}

func (policy *ProjectTaskPolicy) CanAnonymousRead(ctx context.Context, event nostr.Event) bool {
	scope, err := policy.resolveStored(ctx, event)
	if err != nil || !scope.Relevant || len(scope.Channels) == 0 {
		return false
	}
	for _, channel := range scope.Channels {
		metadata, found := policy.Groups.GetMetadata(channel)
		if !found || HasTag(metadata.Tags, "private") || HasTag(metadata.Tags, "hidden") {
			return false
		}
	}
	return true
}

// Remember persists derived scopes before the upstream WebSocket deletion
// path removes a target. This keeps tombstones readable after a restart and
// also amortizes ancestry lookups for normal reads.
func (policy *ProjectTaskPolicy) Remember(ctx context.Context, event nostr.Event) error {
	scope, err := policy.Resolver.Resolve(ctx, event)
	if err != nil || !scope.Relevant || len(scope.Channels) == 0 {
		return err
	}
	return policy.storeScopes(ctx, event.ID, scope.Channels)
}

func (policy *ProjectTaskPolicy) resolveStored(ctx context.Context, event nostr.Event) (projecttask.ResolvedScope, error) {
	if event.ID != nostr.ZeroID {
		channels, found, err := policy.loadScopes(ctx, event.ID)
		if err != nil {
			return projecttask.ResolvedScope{}, err
		}
		if found {
			return projecttask.ResolvedScope{Relevant: true, Channels: channels}, nil
		}
	}
	scope, err := policy.Resolver.Resolve(ctx, event)
	if err == nil && scope.Relevant && len(scope.Channels) > 0 && event.ID != nostr.ZeroID {
		_ = policy.storeScopes(ctx, event.ID, scope.Channels)
	}
	return scope, err
}

func (policy *ProjectTaskPolicy) loadScopes(ctx context.Context, id nostr.ID) ([]string, bool, error) {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	rows, err := GetDb().QueryContext(queryCtx,
		"SELECT channel FROM "+policy.Events.Schema.Prefix("project_task_scopes")+" WHERE event_id = $1 ORDER BY channel",
		id.Hex())
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	channels := make([]string, 0, 2)
	for rows.Next() {
		var channel string
		if err := rows.Scan(&channel); err != nil {
			return nil, false, err
		}
		channels = append(channels, channel)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return channels, len(channels) > 0, nil
}

func (policy *ProjectTaskPolicy) storeScopes(ctx context.Context, id nostr.ID, channels []string) error {
	channels = append([]string(nil), channels...)
	slices.Sort(channels)
	channels = slices.Compact(channels)
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	for _, channel := range channels {
		if channel == "" {
			continue
		}
		_, err := sb.Insert(policy.Events.Schema.Prefix("project_task_scopes")).
			Columns("event_id", "channel").
			Values(id.Hex(), channel).
			Suffix("ON CONFLICT(event_id, channel) DO NOTHING").
			RunWith(GetDb()).
			ExecContext(queryCtx)
		if err != nil {
			return err
		}
	}
	return nil
}

// IsTombstoned implements visible NIP-09 deletion for project/repository
// addresses and PR-graph event IDs through both WebSocket and HTTP.
func (policy *ProjectTaskPolicy) IsTombstoned(ctx context.Context, event nostr.Event) bool {
	filters := make([]nostr.Filter, 0, 2)
	if event.Kind == projecttask.KindProject || event.Kind == nostr.KindRepositoryAnnouncement {
		id := event.Tags.GetD()
		if id != "" {
			address := fmt.Sprintf("%d:%s:%s", event.Kind, event.PubKey.Hex(), id)
			filters = append(filters, nostr.Filter{
				Kinds: []nostr.Kind{nostr.KindDeletion},
				Tags:  nostr.TagMap{"a": {address}},
				Since: event.CreatedAt,
			})
		}
	}
	scope, err := policy.resolveStored(ctx, event)
	if err == nil && scope.Relevant && event.ID != nostr.ZeroID {
		filters = append(filters, nostr.Filter{
			Kinds: []nostr.Kind{nostr.KindDeletion},
			Tags:  nostr.TagMap{"e": {event.ID.Hex()}},
			Since: event.CreatedAt,
		})
	}
	for _, filter := range filters {
		for tombstone := range policy.Events.QueryEventsBefore(ctx, filter, "", 0) {
			if tombstone.PubKey == event.PubKey && tombstone.CreatedAt >= event.CreatedAt {
				return true
			}
		}
	}
	return false
}

func withOnlyDerivedGroup(event nostr.Event, channel string) nostr.Event {
	copy := event
	copy.Tags = make(nostr.Tags, 0, len(event.Tags)+1)
	for _, tag := range event.Tags {
		if len(tag) == 0 || tag[0] != "h" {
			copy.Tags = append(copy.Tags, append(nostr.Tag(nil), tag...))
		}
	}
	copy.Tags = append(copy.Tags, nostr.Tag{"h", channel})
	return copy
}
