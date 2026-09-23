package zooid

import (
	"context"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
	"zooid/internal/projecttask"
)

func TestProjectTaskPolicyRequiresMaintainerToCreateRepository(t *testing.T) {
	policy, groups := createProjectTaskTestPolicy(t, "repo-channel", false)
	creator := nostr.Generate()
	member := nostr.Generate()
	groups.creatorCache.Store("repo-channel", creator.Public())
	addProjectTaskTestMember(groups, "repo-channel", member.Public())

	announcement := signedEvent(member, 10, nostr.KindRepositoryAnnouncement, nostr.Tags{
		{"d", "notes"}, {projecttask.TagBuzzChannel, "repo-channel"},
	})
	handled, reason := policy.CheckWrite(context.Background(), announcement)
	require.True(t, handled)
	require.Contains(t, reason, "maintainer")

	groups.SetMemberRoles("repo-channel", member.Public(), []string{"maintainer"})
	handled, reason = policy.CheckWrite(context.Background(), announcement)
	require.True(t, handled)
	require.Empty(t, reason)

	creatorAnnouncement := signedEvent(creator, 11, nostr.KindRepositoryAnnouncement, nostr.Tags{
		{"d", "notes-two"}, {projecttask.TagBuzzChannel, "repo-channel"},
	})
	handled, reason = policy.CheckWrite(context.Background(), creatorAnnouncement)
	require.True(t, handled)
	require.Empty(t, reason)
}

func TestProjectTaskPolicyRejectsChannelLessRepository(t *testing.T) {
	policy, _ := createProjectTaskTestPolicy(t, "repo-channel", false)
	owner := nostr.Generate()
	announcement := signedEvent(owner, 10, nostr.KindRepositoryAnnouncement, nostr.Tags{{"d", "notes"}})
	handled, reason := policy.CheckWrite(context.Background(), announcement)
	require.True(t, handled)
	require.Contains(t, reason, "require a channel")
}

func TestProjectTaskPolicyRejectsReviewWithoutCanMaintain(t *testing.T) {
	policy, groups := createProjectTaskTestPolicy(t, "repo-channel", false)
	owner := nostr.Generate()
	author := nostr.Generate()
	reviewer := nostr.Generate()
	groups.creatorCache.Store("repo-channel", owner.Public())
	addProjectTaskTestMember(groups, "repo-channel", author.Public())
	addProjectTaskTestMember(groups, "repo-channel", reviewer.Public())
	coordinate := projecttask.RepositoryCoordinate{Owner: owner.Public(), ID: "notes"}
	repository := signedEvent(owner, 10, nostr.KindRepositoryAnnouncement, nostr.Tags{
		{"d", "notes"}, {projecttask.TagBuzzChannel, "repo-channel"},
	})
	require.NoError(t, policy.Events.SaveEvent(repository))
	root := signedEvent(author, 20, projecttask.KindPullRequest, nostr.Tags{
		{"a", coordinate.String()}, {"p", owner.Public().Hex()}, {"subject", "Review"},
		{"c", strings.Repeat("a", 40)}, {"clone", "https://example.com/notes.git"},
	})
	require.NoError(t, policy.Events.SaveEvent(root))

	decision := signedEvent(reviewer, 30, nostr.KindTextNote, nostr.Tags{
		{"e", root.ID.Hex(), "", "root"}, {"a", coordinate.String()}, {"t", "approval"},
		{"c", strings.Repeat("a", 40)},
	})
	handled, reason := policy.CheckWrite(context.Background(), decision)
	require.True(t, handled)
	require.Contains(t, reason, "maintainer")

	authorDecision := signedEvent(author, 31, nostr.KindTextNote, nostr.Tags{
		{"e", root.ID.Hex(), "", "root"}, {"a", coordinate.String()}, {"t", "approval"},
		{"c", strings.Repeat("a", 40)},
	})
	groups.SetMemberRoles("repo-channel", author.Public(), []string{"maintainer"})
	handled, reason = policy.CheckWrite(context.Background(), authorDecision)
	require.True(t, handled)
	require.Contains(t, reason, "authors cannot")

	groups.SetMemberRoles("repo-channel", reviewer.Public(), []string{"maintainer"})
	handled, reason = policy.CheckWrite(context.Background(), decision)
	require.True(t, handled)
	require.Empty(t, reason)

	ownerRoot := signedEvent(owner, 21, projecttask.KindPullRequest, nostr.Tags{
		{"a", coordinate.String()}, {"subject", "Owned"},
		{"c", strings.Repeat("b", 40)}, {"clone", "https://example.com/notes.git"},
	})
	require.NoError(t, policy.Events.SaveEvent(ownerRoot))
	ownerDecision := signedEvent(owner, 32, nostr.KindTextNote, nostr.Tags{
		{"e", ownerRoot.ID.Hex(), "", "root"}, {"a", coordinate.String()}, {"t", "approval"},
		{"c", strings.Repeat("b", 40)},
	})
	handled, reason = policy.CheckWrite(context.Background(), ownerDecision)
	require.True(t, handled)
	require.Empty(t, reason)

	groups.Config.Roles = map[string]Role{
		"admin": {Pubkeys: []string{author.Public().Hex()}, CanManage: true},
	}
	handled, reason = policy.CheckWrite(context.Background(), authorDecision)
	require.True(t, handled)
	require.Empty(t, reason)
}

func TestProjectTaskPolicyRejectsOwnerAuthorReviewWithoutCanMaintain(t *testing.T) {
	policy, groups := createProjectTaskTestPolicy(t, "repo-channel", false)
	creator := nostr.Generate()
	owner := nostr.Generate()
	groups.creatorCache.Store("repo-channel", creator.Public())
	addProjectTaskTestMember(groups, "repo-channel", owner.Public())
	coordinate := projecttask.RepositoryCoordinate{Owner: owner.Public(), ID: "notes"}
	repository := signedEvent(owner, 10, nostr.KindRepositoryAnnouncement, nostr.Tags{
		{"d", "notes"}, {projecttask.TagBuzzChannel, "repo-channel"},
	})
	require.NoError(t, policy.Events.SaveEvent(repository))
	root := signedEvent(owner, 20, projecttask.KindPullRequest, nostr.Tags{
		{"a", coordinate.String()}, {"subject", "Review"},
		{"c", strings.Repeat("a", 40)}, {"clone", "https://example.com/notes.git"},
	})
	require.NoError(t, policy.Events.SaveEvent(root))
	decision := signedEvent(owner, 30, nostr.KindTextNote, nostr.Tags{
		{"e", root.ID.Hex(), "", "root"}, {"a", coordinate.String()}, {"t", "approval"},
		{"c", strings.Repeat("a", 40)},
	})
	handled, reason := policy.CheckWrite(context.Background(), decision)
	require.True(t, handled)
	require.Contains(t, reason, "maintainer")
}

func TestProjectTaskPolicyRejectsTaskStatusWithoutCanMaintain(t *testing.T) {
	policy, groups := createProjectTaskTestPolicy(t, "repo-channel", false)
	owner := nostr.Generate()
	author := nostr.Generate()
	member := nostr.Generate()
	groups.creatorCache.Store("repo-channel", owner.Public())
	addProjectTaskTestMember(groups, "repo-channel", author.Public())
	addProjectTaskTestMember(groups, "repo-channel", member.Public())
	coordinate := projecttask.RepositoryCoordinate{Owner: owner.Public(), ID: "notes"}
	repository := signedEvent(owner, 10, nostr.KindRepositoryAnnouncement, nostr.Tags{
		{"d", "notes"}, {projecttask.TagBuzzChannel, "repo-channel"},
	})
	require.NoError(t, policy.Events.SaveEvent(repository))
	issue := signedEvent(author, 20, nostr.KindIssue, nostr.Tags{
		{"a", coordinate.String()}, {"subject", "Ship"},
	})
	require.NoError(t, policy.Events.SaveEvent(issue))

	closed := signedEvent(member, 30, nostr.KindStatusClosed, nostr.Tags{
		{"e", issue.ID.Hex(), "", "root"}, {"a", coordinate.String()},
	})
	handled, reason := policy.CheckWrite(context.Background(), closed)
	require.True(t, handled)
	require.Contains(t, reason, "task status")

	authorClosed := signedEvent(author, 31, nostr.KindStatusClosed, nostr.Tags{
		{"e", issue.ID.Hex(), "", "root"}, {"a", coordinate.String()},
	})
	handled, reason = policy.CheckWrite(context.Background(), authorClosed)
	require.True(t, handled)
	require.Contains(t, reason, "task status")

	groups.SetMemberRoles("repo-channel", member.Public(), []string{"maintainer"})
	handled, reason = policy.CheckWrite(context.Background(), closed)
	require.True(t, handled)
	require.Empty(t, reason)
}

func TestProjectTaskPolicyAllowsAuthorToClosePullRequest(t *testing.T) {
	policy, groups := createProjectTaskTestPolicy(t, "repo-channel", false)
	owner := nostr.Generate()
	author := nostr.Generate()
	member := nostr.Generate()
	groups.creatorCache.Store("repo-channel", owner.Public())
	addProjectTaskTestMember(groups, "repo-channel", author.Public())
	addProjectTaskTestMember(groups, "repo-channel", member.Public())
	coordinate := projecttask.RepositoryCoordinate{Owner: owner.Public(), ID: "notes"}
	repository := signedEvent(owner, 10, nostr.KindRepositoryAnnouncement, nostr.Tags{
		{"d", "notes"}, {projecttask.TagBuzzChannel, "repo-channel"},
	})
	require.NoError(t, policy.Events.SaveEvent(repository))
	root := signedEvent(author, 20, projecttask.KindPullRequest, nostr.Tags{
		{"a", coordinate.String()}, {"p", owner.Public().Hex()}, {"subject", "Review"},
		{"c", strings.Repeat("a", 40)}, {"clone", "https://example.com/notes.git"},
	})
	require.NoError(t, policy.Events.SaveEvent(root))

	closed := signedEvent(author, 30, nostr.KindStatusClosed, nostr.Tags{
		{"e", root.ID.Hex(), "", "root"}, {"a", coordinate.String()},
	})
	handled, reason := policy.CheckWrite(context.Background(), closed)
	require.True(t, handled)
	require.Empty(t, reason)

	merged := signedEvent(author, 31, nostr.KindStatusApplied, nostr.Tags{
		{"e", root.ID.Hex(), "", "root"}, {"a", coordinate.String()},
	})
	handled, reason = policy.CheckWrite(context.Background(), merged)
	require.True(t, handled)
	require.Contains(t, reason, "merging")

	memberClosed := signedEvent(member, 32, nostr.KindStatusClosed, nostr.Tags{
		{"e", root.ID.Hex(), "", "root"}, {"a", coordinate.String()},
	})
	handled, reason = policy.CheckWrite(context.Background(), memberClosed)
	require.True(t, handled)
	require.Contains(t, reason, "pull request status")
}

func TestCanMaintainFailsClosedForUnknownGroups(t *testing.T) {
	groups, _ := createTestGroupStore()
	groups.WarmCaches()
	pk := nostr.Generate().Public()
	require.False(t, groups.CanMaintain("", pk))
	require.False(t, groups.CanMaintain("missing", pk))
}
