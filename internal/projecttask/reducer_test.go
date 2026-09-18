package projecttask

import (
	"testing"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
)

func TestReduceTaskMatchesBuzzAuthorityAndCausalAssignmentRules(t *testing.T) {
	issueAuthor := pubkey(t, "1")
	repoOwner := pubkey(t, "2")
	maintainer := pubkey(t, "3")
	member := pubkey(t, "4")
	outsider := pubkey(t, "5")
	issueID := id(t, "a")

	issue := nostr.Event{ID: issueID, Kind: nostr.KindIssue, PubKey: issueAuthor}
	repository := nostr.Event{
		Kind:   nostr.KindRepositoryAnnouncement,
		PubKey: repoOwner,
		Tags:   nostr.Tags{{"d", "notes"}, {"maintainers", maintainer.Hex()}},
	}

	selfAssignID := id(t, "b")
	ownerUnassignID := id(t, "c")
	events := []nostr.Event{
		assignmentEvent(t, selfAssignID, 30, member, issueID, true, member, nil),
		// Applied first within Buzz's authoritative category, even though its
		// timestamp is earlier than the uncaused self-assignment.
		assignmentEvent(t, ownerUnassignID, 20, repoOwner, issueID, false, member, nil),
		// This causal self-operation matches the authoritative head and wins.
		assignmentEvent(t, id(t, "d"), 10, member, issueID, true, member, &ownerUnassignID),
		// An unrelated signer cannot assign somebody else.
		assignmentEvent(t, id(t, "e"), 40, outsider, issueID, true, member, nil),
		// A stale prior is ignored.
		assignmentEvent(t, id(t, "f"), 50, member, issueID, false, member, &selfAssignID),
		statusEvent(id(t, "6"), 60, outsider, issueID, nostr.KindStatusClosed),
		statusEvent(id(t, "8"), 70, maintainer, issueID, nostr.KindStatusApplied),
		statusEvent(id(t, "7"), 70, issueAuthor, issueID, nostr.KindStatusDraft),
		buzzComment(id(t, "9"), 80, member, issueID),
		nip22Comment(id(t, "0"), 90, member, issueID),
	}

	state, err := ReduceTask(issue, repository, events, Maintainers(issueAuthor, maintainer))
	require.NoError(t, err)
	require.Equal(t, nostr.KindStatusDraft, state.Status, "lowest id wins equal timestamp")
	require.NotNil(t, state.StatusEvent)
	require.Equal(t, []nostr.PubKey{member}, state.Assignees)
	require.Equal(t, id(t, "d"), state.AssignmentHeads[member])

	// Every kind-1 root note remains visible as Buzz activity, including
	// assignment notes that were untrusted or causally rejected.
	require.Len(t, state.Activity, 7)
	require.Equal(t, CommentBuzzKind1, state.Activity[0].Encoding)
	require.Equal(t, CommentNIP22, state.Activity[len(state.Activity)-1].Encoding)
}

func TestReduceTaskDefaultsToOpen(t *testing.T) {
	issue := nostr.Event{ID: id(t, "a"), Kind: nostr.KindIssue, PubKey: pubkey(t, "1")}
	repository := nostr.Event{Kind: nostr.KindRepositoryAnnouncement, PubKey: pubkey(t, "2")}
	state, err := ReduceTask(issue, repository, nil)
	require.NoError(t, err)
	require.Equal(t, nostr.KindStatusOpen, state.Status)
	require.Nil(t, state.StatusEvent)
}

func assignmentEvent(
	t *testing.T,
	eventID nostr.ID,
	createdAt nostr.Timestamp,
	signer nostr.PubKey,
	issueID nostr.ID,
	assign bool,
	assignee nostr.PubKey,
	prior *nostr.ID,
) nostr.Event {
	t.Helper()
	label := "assignment"
	if !assign {
		label = "unassignment"
	}
	tags := nostr.Tags{
		{"e", issueID.Hex(), "", "root"},
		{"a", RepositoryCoordinate{Owner: pubkey(t, "2"), ID: "notes"}.String()},
		{"p", assignee.Hex()},
		{"t", label},
	}
	if prior != nil {
		tags = append(tags, nostr.Tag{"prior", prior.Hex()})
	}
	return nostr.Event{ID: eventID, Kind: nostr.KindTextNote, CreatedAt: createdAt, PubKey: signer, Tags: tags}
}

func statusEvent(eventID nostr.ID, createdAt nostr.Timestamp, signer nostr.PubKey, issueID nostr.ID, kind nostr.Kind) nostr.Event {
	return nostr.Event{
		ID: eventID, Kind: kind, CreatedAt: createdAt, PubKey: signer,
		Tags: nostr.Tags{{"e", issueID.Hex(), "", "root"}},
	}
}

func buzzComment(eventID nostr.ID, createdAt nostr.Timestamp, signer nostr.PubKey, issueID nostr.ID) nostr.Event {
	return nostr.Event{
		ID: eventID, Kind: nostr.KindTextNote, CreatedAt: createdAt, PubKey: signer,
		Tags: nostr.Tags{
			{"e", issueID.Hex(), "", "root"},
			{"a", RepositoryCoordinate{Owner: pubkeyUnchecked("2"), ID: "notes"}.String()},
		},
	}
}

func nip22Comment(eventID nostr.ID, createdAt nostr.Timestamp, signer nostr.PubKey, issueID nostr.ID) nostr.Event {
	return nostr.Event{
		ID: eventID, Kind: nostr.KindComment, CreatedAt: createdAt, PubKey: signer,
		Tags: nostr.Tags{
			{"E", issueID.Hex()}, {"K", "1621"}, {"P", signer.Hex()},
			{"e", issueID.Hex()}, {"k", "1621"}, {"p", signer.Hex()},
		},
	}
}

func pubkeyUnchecked(digit string) nostr.PubKey {
	secret, _ := nostr.SecretKeyFromHex(digit)
	return secret.Public()
}
