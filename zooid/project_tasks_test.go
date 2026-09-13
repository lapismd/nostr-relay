package zooid

import (
	"context"
	"strings"
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

	channels, found, err := policy.loadScopes(context.Background(), issue.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, []string{"private-project"}, channels)
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
	require.Equal(t, []string{"project"}, scope.Channels)

	revival := signedEvent(owner, 30, nostr.KindRepositoryAnnouncement, nostr.Tags{
		{"d", "notes"}, {projecttask.TagBuzzChannel, "project"},
	})
	require.False(t, policy.IsTombstoned(context.Background(), revival))
}

func TestProjectTaskPolicyRequiresEveryPullRequestScope(t *testing.T) {
	policy, groups := createProjectTaskTestPolicy(t, "repo-private", true)
	addProjectTaskTestGroup(groups, "origin-private", true)
	owner := nostr.Generate()
	author := nostr.Generate()
	both := nostr.Generate()
	repoOnly := nostr.Generate()
	originOnly := nostr.Generate()
	neither := nostr.Generate()
	coordinate := projecttask.RepositoryCoordinate{Owner: owner.Public(), ID: "notes"}

	repository := signedEvent(owner, 10, nostr.KindRepositoryAnnouncement, nostr.Tags{
		{"d", "notes"}, {projecttask.TagBuzzChannel, "repo-private"},
	})
	require.NoError(t, policy.Events.SaveEvent(repository))
	root := signedEvent(author, 20, projecttask.KindPullRequest, nostr.Tags{
		{"a", coordinate.String()}, {"p", owner.Public().Hex()}, {"subject", "Review"},
		{"c", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, {"clone", "https://example.com/notes.git"},
		{"h", "origin-private"},
	})
	require.NoError(t, policy.Events.SaveEvent(root))
	for _, member := range []nostr.PubKey{author.Public(), both.Public(), repoOnly.Public()} {
		addProjectTaskTestMember(groups, "repo-private", member)
	}
	for _, member := range []nostr.PubKey{author.Public(), both.Public(), originOnly.Public()} {
		addProjectTaskTestMember(groups, "origin-private", member)
	}

	handled, reason := policy.CheckWrite(context.Background(), root)
	require.True(t, handled)
	require.Empty(t, reason)
	require.Equal(t, "origin-private", root.Tags.Find("h")[1], "the signed h tag must remain unchanged")
	require.NoError(t, policy.Remember(context.Background(), root))
	channels, found, err := policy.loadScopes(context.Background(), root.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, []string{"origin-private", "repo-private"}, channels)

	require.True(t, policy.CanRead(context.Background(), both.Public(), root))
	require.False(t, policy.CanRead(context.Background(), repoOnly.Public(), root))
	require.False(t, policy.CanRead(context.Background(), originOnly.Public(), root))
	require.False(t, policy.CanRead(context.Background(), neither.Public(), root))
	require.False(t, policy.CanAnonymousRead(context.Background(), root))

	for name, signer := range map[string]nostr.SecretKey{
		"both": both, "repo only": repoOnly, "origin only": originOnly, "neither": neither,
	} {
		t.Run(name+" write", func(t *testing.T) {
			comment := signedEvent(signer, 30, nostr.KindTextNote, nostr.Tags{
				{"e", root.ID.Hex(), "", "root"}, {"a", coordinate.String()},
			})
			_, reason := policy.CheckWrite(context.Background(), comment)
			if name == "both" {
				require.Empty(t, reason)
			} else {
				require.NotEmpty(t, reason)
			}
		})
	}
}

func TestProjectTaskPolicyAnonymousPullRequestReadRequiresEveryScopePublic(t *testing.T) {
	policy, groups := createProjectTaskTestPolicy(t, "repo-public", false)
	addProjectTaskTestGroup(groups, "origin-public", false)
	owner := nostr.Generate()
	author := nostr.Generate()
	coordinate := projecttask.RepositoryCoordinate{Owner: owner.Public(), ID: "notes"}
	repository := signedEvent(owner, 10, nostr.KindRepositoryAnnouncement, nostr.Tags{{"d", "notes"}, {projecttask.TagBuzzChannel, "repo-public"}})
	require.NoError(t, policy.Events.SaveEvent(repository))
	root := signedEvent(author, 20, projecttask.KindPullRequest, nostr.Tags{
		{"a", coordinate.String()}, {"p", owner.Public().Hex()}, {"subject", "Review"},
		{"c", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, {"clone", "https://example.com/notes.git"},
		{"h", "origin-public"},
	})
	require.NoError(t, policy.Events.SaveEvent(root))
	require.True(t, policy.CanAnonymousRead(context.Background(), root))

	addProjectTaskTestGroup(groups, "origin-public", true)
	require.False(t, policy.CanAnonymousRead(context.Background(), root))
}

func TestProjectTaskPolicyPullRequestTombstoneIsSignerOnlyAndVisible(t *testing.T) {
	policy, groups := createProjectTaskTestPolicy(t, "repo", false)
	owner := nostr.Generate()
	author := nostr.Generate()
	other := nostr.Generate()
	coordinate := projecttask.RepositoryCoordinate{Owner: owner.Public(), ID: "notes"}
	repository := signedEvent(owner, 10, nostr.KindRepositoryAnnouncement, nostr.Tags{{"d", "notes"}, {projecttask.TagBuzzChannel, "repo"}})
	require.NoError(t, policy.Events.SaveEvent(repository))
	root := signedEvent(author, 20, projecttask.KindPullRequest, nostr.Tags{
		{"a", coordinate.String()}, {"p", owner.Public().Hex()}, {"subject", "Review"},
		{"c", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, {"clone", "https://example.com/notes.git"},
	})
	require.NoError(t, policy.Events.SaveEvent(root))
	groups.creatorCache.Store("repo", owner.Public())

	unauthorized := signedEvent(other, 30, nostr.KindDeletion, nostr.Tags{{"e", root.ID.Hex()}})
	handled, reason := policy.CheckWrite(context.Background(), unauthorized)
	require.True(t, handled)
	require.Contains(t, reason, "only an event signer")

	tombstone := signedEvent(author, 30, nostr.KindDeletion, nostr.Tags{{"e", root.ID.Hex()}})
	_, reason = policy.CheckWrite(context.Background(), tombstone)
	require.Empty(t, reason)
	require.NoError(t, policy.Events.SaveEvent(tombstone))
	require.NoError(t, policy.Remember(context.Background(), tombstone))
	require.True(t, policy.IsTombstoned(context.Background(), root))
	require.False(t, policy.IsTombstoned(context.Background(), repository), "deleting the PR root must not cascade")
	require.NoError(t, policy.Events.DeleteEvent(root.ID))
	restarted := NewProjectTaskPolicy(policy.Events, groups)
	scope, err := restarted.resolveStored(context.Background(), tombstone)
	require.NoError(t, err)
	require.Equal(t, []string{"repo"}, scope.Channels)
}

func TestProjectTaskPolicyDoesNotClaimUnrelatedEventDeletions(t *testing.T) {
	policy, _ := createProjectTaskTestPolicy(t, "repo", false)
	author := nostr.Generate()
	note := signedEvent(author, 10, nostr.KindTextNote, nil)
	require.NoError(t, policy.Events.SaveEvent(note))

	for _, target := range []string{note.ID.Hex(), strings.Repeat("f", 64)} {
		deletion := signedEvent(author, 20, nostr.KindDeletion, nostr.Tags{{"e", target}})
		handled, reason := policy.CheckWrite(context.Background(), deletion)
		require.False(t, handled)
		require.Empty(t, reason)
	}
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
	addProjectTaskTestGroup(groups, channel, private)
	return NewProjectTaskPolicy(store, groups), groups
}

func addProjectTaskTestGroup(groups *GroupStore, channel string, private bool) {
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
}

func addProjectTaskTestMember(groups *GroupStore, channel string, member nostr.PubKey) {
	set := groups.getOrCreateMemberSet(channel)
	set.mu.Lock()
	set.members[member] = struct{}{}
	set.mu.Unlock()
	groups.membershipFullyLoaded.Store(channel, struct{}{})
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

func TestGroupReactionDeletionRemainsReadableAfterTargetRemoval(t *testing.T) {
	policy, groups := createProjectTaskTestPolicy(t, "agent-room", true)
	agent, member, outsider := nostr.Generate(), nostr.Generate(), nostr.Generate()
	addProjectTaskTestMember(groups, "agent-room", agent.Public())
	addProjectTaskTestMember(groups, "agent-room", member.Public())
	reaction := signedEvent(agent, 10, nostr.KindReaction, nostr.Tags{{"h", "agent-room"}, {"e", nostr.Generate().Public().Hex()}})
	require.NoError(t, policy.Events.SaveEvent(reaction))
	deletion := signedEvent(agent, 20, nostr.KindDeletion, nostr.Tags{{"h", "agent-room"}, {"e", reaction.ID.Hex()}, {"k", "7"}})
	require.NoError(t, policy.Events.SaveEvent(deletion))
	require.NoError(t, policy.Remember(context.Background(), deletion))
	require.NoError(t, policy.Events.DeleteEvent(reaction.ID))
	restarted := NewProjectTaskPolicy(policy.Events, groups)
	require.True(t, restarted.CanRead(context.Background(), member.Public(), deletion))
	require.False(t, restarted.CanRead(context.Background(), outsider.Public(), deletion))
}
