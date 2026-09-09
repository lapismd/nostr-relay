package projecttask

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
)

type memoryEventReader struct {
	byID      map[nostr.ID]nostr.Event
	byAddress map[string]nostr.Event
}

func (reader memoryEventReader) EventByID(_ context.Context, id nostr.ID) (nostr.Event, bool, error) {
	event, found := reader.byID[id]
	return event, found, nil
}

func (reader memoryEventReader) EventByAddress(_ context.Context, kind nostr.Kind, owner nostr.PubKey, id string) (nostr.Event, bool, error) {
	event, found := reader.byAddress[fmt.Sprintf("%d:%s:%s", kind, owner.Hex(), id)]
	return event, found, nil
}

func TestScopeResolverDerivesTaskAncestryWithoutAddingHTag(t *testing.T) {
	owner := pubkey(t, "1")
	repoID := id(t, "a")
	issueID := id(t, "b")
	coordinate := RepositoryCoordinate{Owner: owner, ID: "notes"}
	repository := nostr.Event{
		ID: repoID, Kind: nostr.KindRepositoryAnnouncement, PubKey: owner,
		Tags: nostr.Tags{{"d", "notes"}, {TagBuzzChannel, "private-notes"}},
	}
	issue := nostr.Event{
		ID: issueID, Kind: nostr.KindIssue, PubKey: owner,
		Tags: nostr.Tags{{"a", coordinate.String()}},
	}
	reader := memoryEventReader{
		byID: map[nostr.ID]nostr.Event{repoID: repository, issueID: issue},
		byAddress: map[string]nostr.Event{
			coordinate.String(): repository,
		},
	}
	resolver := ScopeResolver{Reader: reader}
	comment := nostr.Event{
		Kind: nostr.KindComment,
		Tags: nostr.Tags{
			{"E", issueID.Hex()}, {"K", "1621"}, {"P", owner.Hex()},
			{"e", issueID.Hex()}, {"k", "1621"}, {"p", owner.Hex()},
		},
	}

	scope, err := resolver.Resolve(context.Background(), comment)
	require.NoError(t, err)
	require.Equal(t, ResolvedScope{Relevant: true, Channels: []string{"private-notes"}}, scope)
	require.Nil(t, comment.Tags.Find("h"), "resolver must not mutate the signed event")
}

func TestScopeResolverIntersectsRepositoryAndPullRequestOriginForEveryDescendant(t *testing.T) {
	owner := pubkey(t, "5")
	author := pubkey(t, "6")
	reviewer := pubkey(t, "7")
	coordinate := RepositoryCoordinate{Owner: owner, ID: "notes"}
	repository := nostr.Event{
		ID: id(t, "1"), Kind: nostr.KindRepositoryAnnouncement, PubKey: owner,
		Tags: nostr.Tags{{"d", "notes"}, {TagBuzzChannel, "repo-channel"}},
	}
	root := validPullRequestEvent(t, id(t, "2"), author, owner, "notes", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	root.Tags = append(root.Tags, nostr.Tag{"h", "origin-channel"})
	kind1 := nostr.Event{
		ID: id(t, "3"), Kind: nostr.KindTextNote, PubKey: reviewer,
		Tags: nostr.Tags{{"e", root.ID.Hex(), "", "root"}, {"a", coordinate.String()}},
	}
	nip22 := nostr.Event{
		ID: id(t, "4"), Kind: nostr.KindComment, PubKey: reviewer,
		Tags: nostr.Tags{
			{"E", root.ID.Hex()}, {"K", "1618"}, {"P", author.Hex()},
			{"e", root.ID.Hex()}, {"k", "1618"}, {"p", author.Hex()},
		},
	}
	reader := memoryEventReader{
		byID: map[nostr.ID]nostr.Event{
			repository.ID: repository, root.ID: root, kind1.ID: kind1, nip22.ID: nip22,
		},
		byAddress: map[string]nostr.Event{coordinate.String(): repository},
	}
	resolver := ScopeResolver{Reader: reader}
	update := nostr.Event{
		Kind: KindPullRequestUpdate, PubKey: author,
		Tags: nostr.Tags{{"a", coordinate.String()}, {"E", root.ID.Hex()}, {"P", author.Hex()}},
	}
	status := nostr.Event{
		Kind: nostr.KindStatusOpen, PubKey: owner,
		Tags: nostr.Tags{{"e", root.ID.Hex(), "", "root"}, {"a", coordinate.String()}},
	}
	tombstone := nostr.Event{
		Kind: nostr.KindDeletion, PubKey: reviewer,
		Tags: nostr.Tags{{"e", kind1.ID.Hex()}},
	}

	for name, event := range map[string]nostr.Event{
		"root": root, "update": update, "status": status,
		"kind1 comment": kind1, "NIP-22 comment": nip22, "tombstone": tombstone,
	} {
		t.Run(name, func(t *testing.T) {
			scope, err := resolver.Resolve(context.Background(), event)
			require.NoError(t, err)
			require.Equal(t, ResolvedScope{Relevant: true, Channels: []string{"origin-channel", "repo-channel"}}, scope)
		})
	}
	require.Nil(t, root.Tags.Find("derived-h"), "resolution never rewrites the signed event")
}

func TestScopeResolverDerivesPullRequestReactionAndDeletionScope(t *testing.T) {
	owner := pubkey(t, "c")
	author := pubkey(t, "d")
	reviewer := pubkey(t, "e")
	coordinate := RepositoryCoordinate{Owner: owner, ID: "notes"}
	repository := nostr.Event{
		ID: id(t, "6"), Kind: nostr.KindRepositoryAnnouncement, PubKey: owner,
		Tags: nostr.Tags{{"d", "notes"}, {TagBuzzChannel, "repo-channel"}},
	}
	root := validPullRequestEvent(t, id(t, "7"), author, owner, "notes", strings.Repeat("a", 40))
	comment := nostr.Event{
		ID: id(t, "8"), Kind: nostr.KindTextNote, PubKey: reviewer,
		Tags: nostr.Tags{{"e", root.ID.Hex(), "", "root"}, {"a", coordinate.String()}},
	}
	reaction := nostr.Event{
		ID: id(t, "9"), Kind: nostr.KindReaction, PubKey: reviewer, Content: "👍",
		Tags: nostr.Tags{{"e", comment.ID.Hex()}, {"p", reviewer.Hex()}, {"k", "1"}, {"a", coordinate.String()}},
	}
	reader := memoryEventReader{
		byID: map[nostr.ID]nostr.Event{
			repository.ID: repository, root.ID: root, comment.ID: comment, reaction.ID: reaction,
		},
		byAddress: map[string]nostr.Event{coordinate.String(): repository},
	}
	resolver := ScopeResolver{Reader: reader}

	for name, event := range map[string]nostr.Event{
		"reaction": reaction,
		"deletion": {Kind: nostr.KindDeletion, PubKey: reviewer, Tags: nostr.Tags{{"e", reaction.ID.Hex()}}},
	} {
		t.Run(name, func(t *testing.T) {
			scope, err := resolver.Resolve(context.Background(), event)
			require.NoError(t, err)
			require.Equal(t, ResolvedScope{Relevant: true, Channels: []string{"repo-channel"}}, scope)
		})
	}
}

func TestScopeResolverRejectsPullRequestRootAndRepositoryMismatches(t *testing.T) {
	owner := pubkey(t, "8")
	author := pubkey(t, "9")
	coordinate := RepositoryCoordinate{Owner: owner, ID: "notes"}
	repository := nostr.Event{Kind: nostr.KindRepositoryAnnouncement, PubKey: owner, Tags: nostr.Tags{{"d", "notes"}, {TagBuzzChannel, "repo"}}}
	root := validPullRequestEvent(t, id(t, "5"), author, owner, "notes", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	reader := memoryEventReader{
		byID:      map[nostr.ID]nostr.Event{root.ID: root},
		byAddress: map[string]nostr.Event{coordinate.String(): repository},
	}
	resolver := ScopeResolver{Reader: reader}

	wrongAuthor := nostr.Event{Kind: KindPullRequestUpdate, Tags: nostr.Tags{
		{"a", coordinate.String()}, {"E", root.ID.Hex()}, {"P", owner.Hex()},
	}}
	_, err := resolver.Resolve(context.Background(), wrongAuthor)
	require.ErrorContains(t, err, "root author")

	otherCoordinate := RepositoryCoordinate{Owner: owner, ID: "other"}
	wrongRepository := wrongAuthor
	wrongRepository.Tags = nostr.Tags{{"a", otherCoordinate.String()}, {"E", root.ID.Hex()}, {"P", author.Hex()}}
	_, err = resolver.Resolve(context.Background(), wrongRepository)
	require.ErrorContains(t, err, "does not match")

	conflictingDescendant := nostr.Event{Kind: nostr.KindStatusOpen, Tags: nostr.Tags{
		{"e", root.ID.Hex(), "", "root"}, {"a", coordinate.String()}, {"h", "other-origin"},
	}}
	_, err = resolver.Resolve(context.Background(), conflictingDescendant)
	require.ErrorContains(t, err, "conflicts")
}

func TestScopeResolverValidatesNestedNIP22Ancestry(t *testing.T) {
	owner := pubkey(t, "a")
	author := pubkey(t, "b")
	coordinate := RepositoryCoordinate{Owner: owner, ID: "notes"}
	repository := nostr.Event{Kind: nostr.KindRepositoryAnnouncement, PubKey: owner, Tags: nostr.Tags{{"d", "notes"}, {TagBuzzChannel, "repo"}}}
	root := validPullRequestEvent(t, id(t, "6"), author, owner, "notes", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	parent := nostr.Event{ID: id(t, "7"), Kind: nostr.KindComment, PubKey: owner, Tags: nostr.Tags{
		{"E", root.ID.Hex()}, {"K", "1618"}, {"P", author.Hex()},
		{"e", root.ID.Hex()}, {"k", "1618"}, {"p", author.Hex()},
	}}
	child := nostr.Event{ID: id(t, "8"), Kind: nostr.KindComment, PubKey: owner, Tags: nostr.Tags{
		{"E", root.ID.Hex()}, {"K", "1618"}, {"P", author.Hex()},
		{"e", parent.ID.Hex()}, {"k", "1111"}, {"p", owner.Hex()},
	}}
	reader := memoryEventReader{
		byID:      map[nostr.ID]nostr.Event{root.ID: root, parent.ID: parent},
		byAddress: map[string]nostr.Event{coordinate.String(): repository},
	}
	resolver := ScopeResolver{Reader: reader}
	scope, err := resolver.Resolve(context.Background(), child)
	require.NoError(t, err)
	require.Equal(t, []string{"repo"}, scope.Channels)

	conflict := child
	conflict.Tags = replaceNamedTag(child.Tags, "P", nostr.Tag{"P", owner.Hex()})
	_, err = resolver.Resolve(context.Background(), conflict)
	require.ErrorContains(t, err, "root author")
}

func TestScopeResolverRejectsConflictingBindings(t *testing.T) {
	owner := pubkey(t, "2")
	repository := nostr.Event{
		Kind: nostr.KindRepositoryAnnouncement, PubKey: owner,
		Tags: nostr.Tags{{"d", "notes"}, {TagBuzzChannel, "repo-channel"}},
	}
	coordinate := RepositoryCoordinate{Owner: owner, ID: "notes"}
	resolver := ScopeResolver{Reader: memoryEventReader{
		byAddress: map[string]nostr.Event{coordinate.String(): repository},
	}}
	issue := nostr.Event{
		Kind: nostr.KindIssue,
		Tags: nostr.Tags{{"a", coordinate.String()}, {"h", "hostile-channel"}},
	}

	_, err := resolver.Resolve(context.Background(), issue)
	require.ErrorIs(t, err, ErrScopeUnresolved)
	require.ErrorContains(t, err, "conflicting")
}

func TestScopeResolverFailsClosedForMissingRepository(t *testing.T) {
	coordinate := RepositoryCoordinate{Owner: pubkey(t, "3"), ID: "missing"}
	resolver := ScopeResolver{Reader: memoryEventReader{}}
	issue := nostr.Event{Kind: nostr.KindIssue, Tags: nostr.Tags{{"a", coordinate.String()}}}

	_, err := resolver.Resolve(context.Background(), issue)
	require.ErrorIs(t, err, ErrScopeUnresolved)
	require.ErrorContains(t, err, "was not found")
}

func TestScopeResolverAppliesRepositoryAndOptionalPROriginToCIActivity(t *testing.T) {
	owner := pubkey(t, "d")
	author := pubkey(t, "e")
	coordinate := RepositoryCoordinate{Owner: owner, ID: "notes"}
	repository := nostr.Event{Kind: nostr.KindRepositoryAnnouncement, PubKey: owner, Tags: nostr.Tags{{"d", "notes"}, {TagBuzzChannel, "repo"}}}
	root := validPullRequestEvent(t, id(t, "e"), author, owner, "notes", strings.Repeat("a", 40))
	root.Tags = append(root.Tags, nostr.Tag{"h", "origin"})
	resolver := ScopeResolver{Reader: memoryEventReader{
		byID:      map[nostr.ID]nostr.Event{root.ID: root},
		byAddress: map[string]nostr.Event{coordinate.String(): repository},
	}}
	repositoryRun := nostr.Event{Kind: nostr.KindTextNote, Tags: nostr.Tags{
		{"t", "ci"}, {"a", coordinate.String()}, {"c", strings.Repeat("a", 40)}, {"status", "success"},
	}}
	scope, err := resolver.Resolve(context.Background(), repositoryRun)
	require.NoError(t, err)
	require.Equal(t, []string{"repo"}, scope.Channels)

	prRun := repositoryRun
	prRun.Tags = append(prRun.Tags.CloneDeep(), nostr.Tag{"e", root.ID.Hex(), "", "root"})
	scope, err = resolver.Resolve(context.Background(), prRun)
	require.NoError(t, err)
	require.Equal(t, []string{"origin", "repo"}, scope.Channels)
}

func TestScopeResolverRequiresSubtaskInSameRepository(t *testing.T) {
	owner := pubkey(t, "4")
	parentID := id(t, "c")
	first := RepositoryCoordinate{Owner: owner, ID: "first"}
	second := RepositoryCoordinate{Owner: owner, ID: "second"}
	firstRepo := nostr.Event{Kind: nostr.KindRepositoryAnnouncement, PubKey: owner, Tags: nostr.Tags{{"d", "first"}, {TagBuzzChannel, "project"}}}
	secondRepo := nostr.Event{Kind: nostr.KindRepositoryAnnouncement, PubKey: owner, Tags: nostr.Tags{{"d", "second"}, {TagBuzzChannel, "project"}}}
	parent := nostr.Event{ID: parentID, Kind: nostr.KindIssue, Tags: nostr.Tags{{"a", first.String()}}}
	resolver := ScopeResolver{Reader: memoryEventReader{
		byID: map[nostr.ID]nostr.Event{parentID: parent},
		byAddress: map[string]nostr.Event{
			first.String():  firstRepo,
			second.String(): secondRepo,
		},
	}}
	child := nostr.Event{
		ID: id(t, "d"), Kind: nostr.KindIssue,
		Tags: nostr.Tags{{"a", second.String()}, {"parent", parentID.Hex()}, {"e", parentID.Hex()}},
	}

	_, err := resolver.Resolve(context.Background(), child)
	require.ErrorContains(t, err, "same repository")
}
