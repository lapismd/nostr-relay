package zooid

import (
	"context"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
	"zooid/internal/projecttask"
)

func TestProjectTaskPolicyPersistsDerivedScopeWithoutMutatingEvent(t *testing.T) {
	policy, group := createProjectTaskTestPolicy(t, "private-project", true)
	owner := nostr.Generate()
	member := nostr.Generate()
	coordinate := projecttask.RepositoryCoordinate{Owner: owner.Public(), ID: "notes"}
	group.creatorCache.Store("private-project", owner.Public())

	repository := signedEvent(owner, 10, nostr.KindRepositoryAnnouncement, nostr.Tags{
		{"d", "notes"}, {projecttask.TagBuzzChannel, "private-project"},
	})
	_, err := policy.Events.ReplaceEvent(repository)
	require.NoError(t, err)

	issue := signedEvent(owner, 20, nostr.KindIssue, nostr.Tags{{"a", coordinate.String()}})
	require.NoError(t, policy.Events.SaveEvent(issue))

	handled, reason := policy.CheckWrite(context.Background(), issue)
	require.True(t, handled)
	require.Empty(t, reason)
	require.Nil(t, issue.Tags.Find("h"), "policy must not alter signed tags")
	require.NoError(t, policy.Remember(context.Background(), issue))

	channel, found, err := policy.loadScope(context.Background(), issue.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "private-project", channel)
	require.False(t, policy.CanRead(context.Background(), member.Public(), issue))

	set := group.getOrCreateMemberSet("private-project")
	set.mu.Lock()
	set.members[member.Public()] = struct{}{}
	set.mu.Unlock()
	group.membershipFullyLoaded.Store("private-project", struct{}{})
	require.True(t, policy.CanRead(context.Background(), member.Public(), issue))
}

func TestProjectTaskPolicyTombstoneScopeSurvivesTargetDeletion(t *testing.T) {
	policy, _ := createProjectTaskTestPolicy(t, "project", false)
	owner := nostr.Generate()
	repository := signedEvent(owner, 10, nostr.KindRepositoryAnnouncement, nostr.Tags{
		{"d", "notes"}, {projecttask.TagBuzzChannel, "project"},
	})
	require.NoError(t, policy.Events.SaveEvent(repository))

	address := "30617:" + owner.Public().Hex() + ":notes"
	tombstone := signedEvent(owner, 20, nostr.KindDeletion, nostr.Tags{{"a", address}})
	require.NoError(t, policy.Events.SaveEvent(tombstone))
	require.NoError(t, policy.Remember(context.Background(), tombstone))
	require.True(t, policy.IsTombstoned(context.Background(), repository))

	require.NoError(t, policy.Events.DeleteEvent(repository.ID))
	scope, err := policy.resolveStored(context.Background(), tombstone)
	require.NoError(t, err)
	require.Equal(t, "project", scope.Channel)

	revival := signedEvent(owner, 30, nostr.KindRepositoryAnnouncement, nostr.Tags{
		{"d", "notes"}, {projecttask.TagBuzzChannel, "project"},
	})
	require.False(t, policy.IsTombstoned(context.Background(), revival))
}

func TestQueryStoredAppliesDerivedPrivateGroupACLAndTombstoneProjection(t *testing.T) {
	policy, group := createProjectTaskTestPolicy(t, "private-project", true)
	owner := nostr.Generate()
	member := nostr.Generate()
	outsider := nostr.Generate()
	coordinate := projecttask.RepositoryCoordinate{Owner: owner.Public(), ID: "notes"}
	repository := signedEvent(owner, 10, nostr.KindRepositoryAnnouncement, nostr.Tags{
		{"d", "notes"}, {projecttask.TagBuzzChannel, "private-project"},
	})
	require.NoError(t, policy.Events.SaveEvent(repository))
	issue := signedEvent(owner, 20, nostr.KindIssue, nostr.Tags{{"a", coordinate.String()}})
	require.NoError(t, policy.Events.SaveEvent(issue))
	require.NoError(t, policy.Remember(context.Background(), issue))

	set := group.getOrCreateMemberSet("private-project")
	set.mu.Lock()
	set.members[member.Public()] = struct{}{}
	set.mu.Unlock()
	group.membershipFullyLoaded.Store("private-project", struct{}{})

	instance := &Instance{
		Ctx:          context.Background(),
		Config:       policy.Events.Config,
		Events:       policy.Events,
		Groups:       group,
		ProjectTasks: policy,
	}
	filter := nostr.Filter{Kinds: []nostr.Kind{nostr.KindIssue}}

	memberEvents := collectEvents(instance.QueryStored(withAuthenticatedPubkey(context.Background(), member.Public()), filter))
	require.Len(t, memberEvents, 1)
	outsiderEvents := collectEvents(instance.QueryStored(withAuthenticatedPubkey(context.Background(), outsider.Public()), filter))
	require.Empty(t, outsiderEvents)

	tombstone := signedEvent(owner, 30, nostr.KindDeletion, nostr.Tags{{"a", "30617:" + owner.Public().Hex() + ":notes"}})
	require.NoError(t, policy.Events.SaveEvent(tombstone))
	repositories := collectEvents(instance.QueryStored(withAuthenticatedPubkey(context.Background(), member.Public()), nostr.Filter{
		Kinds: []nostr.Kind{nostr.KindRepositoryAnnouncement},
	}))
	require.Empty(t, repositories)
}

func createProjectTaskTestPolicy(t *testing.T, channel string, private bool) (*ProjectTaskPolicy, *GroupStore) {
	t.Helper()
	store := createTestEventStore()
	store.Config.Policy.Open = true
	store.Config.Groups.Enabled = true
	if err := store.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	management := &ManagementStore{Config: store.Config, Events: store}
	groups := &GroupStore{
		Config:       store.Config,
		Events:       store,
		Management:   management,
		cachesWarmed: true,
	}
	tags := nostr.Tags{{"d", channel}}
	if private {
		tags = append(tags, nostr.Tag{"private"}, nostr.Tag{"closed"}, nostr.Tag{"hidden"})
	}
	groups.metadataCache.Store(channel, &groupMetaCache{
		event:   nostr.Event{Kind: nostr.KindSimpleGroupMetadata, Tags: tags},
		found:   true,
		private: private,
		hidden:  private,
		closed:  private,
	})
	return NewProjectTaskPolicy(store, groups), groups
}

func signedEvent(secret nostr.SecretKey, createdAt nostr.Timestamp, kind nostr.Kind, tags nostr.Tags) nostr.Event {
	event := nostr.Event{Kind: kind, CreatedAt: createdAt, Tags: tags}
	event.Sign(secret)
	return event
}

func collectEvents(sequence func(func(nostr.Event) bool)) []nostr.Event {
	events := make([]nostr.Event, 0)
	sequence(func(event nostr.Event) bool {
		events = append(events, event)
		return true
	})
	return events
}
