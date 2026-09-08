package projecttask

import (
	"context"
	"fmt"
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
	require.Equal(t, ResolvedScope{Relevant: true, Channel: "private-notes"}, scope)
	require.Nil(t, comment.Tags.Find("h"), "resolver must not mutate the signed event")
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
