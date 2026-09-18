package projecttask

import (
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
)

func TestReducePullRequestMatchesBuzzTipStatusAndReviewSemantics(t *testing.T) {
	owner := pubkey(t, "1")
	author := pubkey(t, "2")
	requested := pubkey(t, "3")
	member := pubkey(t, "4")
	coordinate := RepositoryCoordinate{Owner: owner, ID: "notes"}
	initialCommit := strings.Repeat("a", 40)
	currentCommit := strings.Repeat("b", 40)
	ignoredCommit := strings.Repeat("c", 40)
	root := validPullRequestEvent(t, id(t, "a"), author, owner, "notes", initialCommit)
	root.CreatedAt = 5
	root.Tags = append(root.Tags,
		nostr.Tag{"p", author.Hex()},
		nostr.Tag{"t", "DRAFT"},
		nostr.Tag{"branch-name", "review/buzz"},
		nostr.Tag{"target-branch", "main"},
		nostr.Tag{"base-commit", strings.Repeat("e", 40)},
	)
	repository := nostr.Event{
		Kind: nostr.KindRepositoryAnnouncement, PubKey: owner,
		Tags: nostr.Tags{{"d", "notes"}, {TagBuzzChannel, "repo-channel"}},
	}

	events := []nostr.Event{
		prUpdate(t, id(t, "2"), 10, owner, root, coordinate, strings.Repeat("d", 40)),
		prUpdate(t, id(t, "1"), 10, author, root, coordinate, currentCommit),
		prUpdate(t, id(t, "0"), 30, member, root, coordinate, ignoredCommit),
		prStatus(id(t, "2"), 20, owner, root, coordinate, nostr.KindStatusClosed),
		prStatus(id(t, "1"), 20, author, root, coordinate, nostr.KindStatusOpen),
		prStatus(id(t, "0"), 40, member, root, coordinate, nostr.KindStatusApplied),
		prReviewRequest(id(t, "3"), 21, author, root, coordinate, requested),
		prReviewRequest(id(t, "4"), 22, member, root, coordinate, member),
		prReviewDecision(id(t, "5"), 23, author, root, coordinate, initialCommit, "approval"),
		prReviewDecision(id(t, "6"), 24, requested, root, coordinate, initialCommit, "approval"),
		prReviewDecision(id(t, "7"), 25, requested, root, coordinate, currentCommit, "changes-requested"),
		prReviewDecision(id(t, "8"), 25, requested, root, coordinate, currentCommit, "approval"),
		prReviewDecision(id(t, "9"), 26, owner, root, coordinate, currentCommit, "changes-requested"),
		prInlineComment(id(t, "b"), 27, requested, root, coordinate, initialCommit, "src/old.go", "old", "3"),
		prInlineComment(id(t, "c"), 28, requested, root, coordinate, currentCommit, "src/current.go", "new", "4"),
		prInlineComment(id(t, "d"), 29, requested, root, coordinate, currentCommit, "../invalid", "new", "0"),
		prNIP22Comment(id(t, "e"), 30, member, root, id(t, "f")),
	}

	state, err := ReducePullRequest(root, repository, events, Maintainers(owner, requested))
	require.NoError(t, err)
	require.Equal(t, coordinate, state.Repository)
	require.Equal(t, "Review title", state.Title)
	require.Equal(t, initialCommit, state.InitialCommit)
	require.Equal(t, currentCommit, state.CurrentCommit, "smallest update event ID wins a same-second tie")
	require.Equal(t, PullRequestOpen, state.Lifecycle, "smallest status event ID wins a same-second tie")
	require.Equal(t, "review/buzz", state.BranchName)
	require.Equal(t, "main", state.TargetBranch)
	require.Equal(t, strings.Repeat("f", 40), state.BaseCommit)
	require.Len(t, state.Updates, 2, "authorized group members may be stored but cannot move the trusted tip")
	require.Equal(t, []nostr.PubKey{owner, requested}, state.Reviewers)
	require.Len(t, state.Approvals, 1)
	require.Equal(t, id(t, "8"), state.Approvals[0].Event.ID, "largest decision ID wins a same-second reviewer tie")
	require.Len(t, state.ChangeRequests, 1)
	require.Equal(t, owner, state.ChangeRequests[0].Reviewer)
	require.Equal(t, nostr.Timestamp(30), state.UpdatedAt)

	comments := commentsByID(state.Comments)
	require.False(t, comments[id(t, "5")].IsTrustedReviewDecision, "the PR author cannot review their own PR")
	require.Equal(t, ReviewDecisionHistorical, comments[id(t, "6")].ReviewDecisionStatus)
	require.Equal(t, ReviewDecisionCurrent, comments[id(t, "8")].ReviewDecisionStatus)
	require.Equal(t, InlineCommentOutdated, comments[id(t, "b")].InlineCommentStatus)
	require.Equal(t, InlineCommentCurrent, comments[id(t, "c")].InlineCommentStatus)
	require.True(t, comments[id(t, "d")].IsInlineComment)
	require.Nil(t, comments[id(t, "d")].Anchor, "invalid anchors remain visible without becoming usable")
	require.Equal(t, CommentNIP22, comments[id(t, "e")].Encoding)
}

func TestReducePullRequestDefaultsDraftAndFallsBackToInitialDecisionCommit(t *testing.T) {
	owner := pubkey(t, "5")
	author := pubkey(t, "6")
	coordinate := RepositoryCoordinate{Owner: owner, ID: "notes"}
	commit := strings.Repeat("e", 64)
	root := validPullRequestEvent(t, id(t, "1"), author, owner, "notes", commit)
	root.Tags = append(root.Tags, nostr.Tag{"t", "DrAfT"})
	repository := nostr.Event{Kind: nostr.KindRepositoryAnnouncement, PubKey: owner, Tags: nostr.Tags{{"d", "notes"}}}
	decision := prReviewDecision(id(t, "2"), 10, owner, root, coordinate, "", "approval")

	state, err := ReducePullRequest(root, repository, []nostr.Event{decision}, Maintainers(owner))
	require.NoError(t, err)
	require.Equal(t, PullRequestDraft, state.Lifecycle)
	require.Len(t, state.Approvals, 1)
	require.Equal(t, commit, state.Approvals[0].Commit)
}

func TestReducePullRequestIgnoresRequestedReviewerWithoutCanMaintain(t *testing.T) {
	owner := pubkey(t, "9")
	author := pubkey(t, "a")
	requested := pubkey(t, "b")
	coordinate := RepositoryCoordinate{Owner: owner, ID: "notes"}
	commit := strings.Repeat("a", 40)
	root := validPullRequestEvent(t, id(t, "1"), author, owner, "notes", commit)
	root.Tags = append(root.Tags, nostr.Tag{"p", requested.Hex()})
	repository := nostr.Event{Kind: nostr.KindRepositoryAnnouncement, PubKey: owner, Tags: nostr.Tags{{"d", "notes"}}}
	decision := prReviewDecision(id(t, "2"), 10, requested, root, coordinate, commit, "approval")
	merged := prStatus(id(t, "3"), 11, owner, root, coordinate, nostr.KindStatusApplied)

	state, err := ReducePullRequest(root, repository, []nostr.Event{decision, merged})
	require.NoError(t, err)
	require.Empty(t, state.Approvals)
	require.Equal(t, PullRequestOpen, state.Lifecycle)

	state, err = ReducePullRequest(root, repository, []nostr.Event{decision, merged}, Maintainers(requested))
	require.NoError(t, err)
	require.Len(t, state.Approvals, 1)
	require.Equal(t, PullRequestOpen, state.Lifecycle, "owner merge is ignored unless the owner CanMaintain")

	root.Tags = append(root.Tags, nostr.Tag{"external-provider", "github"})
	providerMerged := prStatus(id(t, "4"), 12, author, root, coordinate, nostr.KindStatusApplied)
	state, err = ReducePullRequest(root, repository, []nostr.Event{providerMerged})
	require.NoError(t, err)
	require.Equal(t, PullRequestMerged, state.Lifecycle, "provider-authored roots keep bridge-signed merges")
}

func TestReducePullRequestKeepsAmbiguousAndNestedNIP22Comments(t *testing.T) {
	owner := pubkey(t, "7")
	author := pubkey(t, "8")
	root := validPullRequestEvent(t, id(t, "3"), author, owner, "notes", strings.Repeat("f", 40))
	repository := nostr.Event{Kind: nostr.KindRepositoryAnnouncement, PubKey: owner, Tags: nostr.Tags{{"d", "notes"}}}
	parent := prNIP22Comment(id(t, "4"), 10, owner, root, root.ID)
	parent.Tags = append(parent.Tags, nostr.Tag{"t", "approval"}, nostr.Tag{"t", "changes-requested"})
	child := prNIP22Comment(id(t, "5"), 11, owner, root, parent.ID)

	state, err := ReducePullRequest(root, repository, []nostr.Event{child, parent})
	require.NoError(t, err)
	require.Len(t, state.Comments, 2)
	require.Empty(t, state.Comments[0].ReviewDecision, "ambiguous review labels do not form a decision")
	require.Equal(t, parent.ID.Hex(), state.Comments[1].Event.Tags.Find("e")[1])
	require.Empty(t, state.Approvals)
	require.Empty(t, state.ChangeRequests)
}

func prUpdate(t *testing.T, eventID nostr.ID, createdAt nostr.Timestamp, signer nostr.PubKey, root nostr.Event, coordinate RepositoryCoordinate, commit string) nostr.Event {
	t.Helper()
	return nostr.Event{
		ID: eventID, Kind: KindPullRequestUpdate, CreatedAt: createdAt, PubKey: signer,
		Tags: nostr.Tags{
			{"a", coordinate.String()}, {"p", coordinate.Owner.Hex()},
			{"E", root.ID.Hex()}, {"P", root.PubKey.Hex()}, {"c", commit},
			{"clone", "https://updates.example/notes.git"},
			{"base-commit", strings.Repeat("f", 40)}, {"merge-base", strings.Repeat("f", 40)},
		},
	}
}

func prStatus(eventID nostr.ID, createdAt nostr.Timestamp, signer nostr.PubKey, root nostr.Event, coordinate RepositoryCoordinate, kind nostr.Kind) nostr.Event {
	return nostr.Event{
		ID: eventID, Kind: kind, CreatedAt: createdAt, PubKey: signer,
		Tags: nostr.Tags{{"e", root.ID.Hex(), "", "root"}, {"a", coordinate.String()}},
	}
}

func prReviewRequest(eventID nostr.ID, createdAt nostr.Timestamp, signer nostr.PubKey, root nostr.Event, coordinate RepositoryCoordinate, reviewers ...nostr.PubKey) nostr.Event {
	tags := nostr.Tags{{"e", root.ID.Hex(), "", "root"}, {"a", coordinate.String()}, {"t", "review-request"}}
	for _, reviewer := range reviewers {
		tags = append(tags, nostr.Tag{"p", reviewer.Hex()})
	}
	return nostr.Event{ID: eventID, Kind: nostr.KindTextNote, CreatedAt: createdAt, PubKey: signer, Tags: tags}
}

func prReviewDecision(eventID nostr.ID, createdAt nostr.Timestamp, signer nostr.PubKey, root nostr.Event, coordinate RepositoryCoordinate, commit, label string) nostr.Event {
	tags := nostr.Tags{{"e", root.ID.Hex(), "", "root"}, {"a", coordinate.String()}, {"t", label}}
	if commit != "" {
		tags = append(tags, nostr.Tag{"c", commit})
	}
	return nostr.Event{ID: eventID, Kind: nostr.KindTextNote, CreatedAt: createdAt, PubKey: signer, Tags: tags}
}

func prInlineComment(eventID nostr.ID, createdAt nostr.Timestamp, signer nostr.PubKey, root nostr.Event, coordinate RepositoryCoordinate, commit, path, side, line string) nostr.Event {
	event := prReviewDecision(eventID, createdAt, signer, root, coordinate, commit, "inline-comment")
	event.Tags = append(event.Tags, nostr.Tag{"file", path}, nostr.Tag{"side", side}, nostr.Tag{"line", line})
	return event
}

func prNIP22Comment(eventID nostr.ID, createdAt nostr.Timestamp, signer nostr.PubKey, root nostr.Event, parentID nostr.ID) nostr.Event {
	parentKind := "1111"
	if parentID == root.ID {
		parentKind = "1618"
	}
	return nostr.Event{
		ID: eventID, Kind: nostr.KindComment, CreatedAt: createdAt, PubKey: signer,
		Tags: nostr.Tags{
			{"E", root.ID.Hex()}, {"K", "1618"}, {"P", root.PubKey.Hex()},
			{"e", parentID.Hex()}, {"k", parentKind}, {"p", signer.Hex()},
		},
	}
}

func commentsByID(comments []PullRequestComment) map[nostr.ID]PullRequestComment {
	result := make(map[nostr.ID]PullRequestComment, len(comments))
	for _, comment := range comments {
		result[comment.Event.ID] = comment
	}
	return result
}
