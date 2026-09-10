package projecttask

import (
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
)

func TestValidateProjectMatchesBuzzNIPMPContract(t *testing.T) {
	owner := pubkey(t, "1")
	project := nostr.Event{
		Kind:   KindProject,
		PubKey: owner,
		Tags: nostr.Tags{
			{"d", "notes"},
			{"name", "Notes"},
			{TagBuzzChannel, "notes-channel"},
			{"a", RepositoryCoordinate{Owner: owner, ID: "desktop"}.String()},
		},
	}
	recognized, err := ValidateEvent(project)
	require.True(t, recognized)
	require.NoError(t, err)

	t.Run("requires exactly one nonempty d", func(t *testing.T) {
		invalid := project
		invalid.Tags = append(invalid.Tags.CloneDeep(), nostr.Tag{"d", "other"})
		_, err := ValidateEvent(invalid)
		require.ErrorContains(t, err, "singleton")
	})

	t.Run("rejects duplicate coordinates despite different hints", func(t *testing.T) {
		invalid := project
		invalid.Tags = append(invalid.Tags.CloneDeep(), nostr.Tag{
			"a", RepositoryCoordinate{Owner: owner, ID: "desktop"}.String(), "wss://other.example",
		})
		_, err := ValidateEvent(invalid)
		require.ErrorContains(t, err, "unique")
	})

	t.Run("requires lowercase coordinate pubkey", func(t *testing.T) {
		invalid := project
		invalid.Tags[3] = nostr.Tag{"a", "30617:" + strings.ToUpper(owner.Hex()) + ":desktop"}
		_, err := ValidateEvent(invalid)
		require.ErrorContains(t, err, "lowercase")
	})

	t.Run("limits raw a tags before parsing", func(t *testing.T) {
		invalid := project
		invalid.Tags = invalid.Tags[:3].CloneDeep()
		for i := range maxProjectCoordinates + 1 {
			invalid.Tags = append(invalid.Tags, nostr.Tag{"a", RepositoryCoordinate{
				Owner: owner,
				ID:    strings.Repeat("x", i+1),
			}.String()})
		}
		_, err := ValidateEvent(invalid)
		require.ErrorContains(t, err, "more than 64")
	})
}

func TestValidateEventOnlyClaimsTaskShapedNotesAndComments(t *testing.T) {
	plainNote := nostr.Event{Kind: nostr.KindTextNote, Content: "hello"}
	recognized, err := ValidateEvent(plainNote)
	require.False(t, recognized)
	require.NoError(t, err)

	plainComment := nostr.Event{Kind: nostr.KindComment, Tags: nostr.Tags{{"K", "1"}}}
	recognized, err = ValidateEvent(plainComment)
	require.False(t, recognized)
	require.NoError(t, err)

	taskComment := nostr.Event{
		Kind: nostr.KindComment,
		Tags: nostr.Tags{
			{"E", id(t, "2").Hex()},
			{"K", "1621"},
			{"P", pubkey(t, "3").Hex()},
			{"e", id(t, "2").Hex()},
			{"k", "1621"},
			{"p", pubkey(t, "3").Hex()},
		},
	}
	recognized, err = ValidateEvent(taskComment)
	require.True(t, recognized)
	require.NoError(t, err)

	missingParentAuthor := taskComment
	missingParentAuthor.Tags = taskComment.Tags[:5].CloneDeep()
	_, err = ValidateEvent(missingParentAuthor)
	require.ErrorContains(t, err, "e, k, and p")
}

func TestValidateCIActivity(t *testing.T) {
	owner := pubkey(t, "c")
	event := nostr.Event{Kind: nostr.KindTextNote, Content: "GitHub Actions success", Tags: nostr.Tags{
		{"t", "ci"}, {"a", RepositoryCoordinate{Owner: owner, ID: "notes"}.String()},
		{"c", strings.Repeat("a", 40)}, {"status", "success"}, {"external-run-id", "7001"},
	}}
	recognized, err := ValidateEvent(event)
	require.True(t, recognized)
	require.NoError(t, err)

	missingCommit := event
	missingCommit.Tags = removeNamedTags(event.Tags, "c")
	_, err = ValidateEvent(missingCommit)
	require.ErrorContains(t, err, "c tag is required")

	badRoot := event
	badRoot.Tags = append(event.Tags.CloneDeep(), nostr.Tag{"e", "not-an-id"})
	_, err = ValidateEvent(badRoot)
	require.ErrorContains(t, err, "valid event ID")
}

func TestValidateTombstoneRequiresDirectAddressOwner(t *testing.T) {
	owner := pubkey(t, "4")
	other := pubkey(t, "5")
	tombstone := nostr.Event{
		Kind:   nostr.KindDeletion,
		PubKey: other,
		Tags: nostr.Tags{{
			"a", "30621:" + owner.Hex() + ":notes",
		}},
	}
	recognized, err := ValidateEvent(tombstone)
	require.True(t, recognized)
	require.ErrorContains(t, err, "address owner")

	tombstone.PubKey = owner
	_, err = ValidateEvent(tombstone)
	require.NoError(t, err)
}

func TestValidatePullRequestMatchesBuzzBuilderContract(t *testing.T) {
	owner := pubkey(t, "7")
	pr := validPullRequestEvent(t, id(t, "7"), owner, owner, "notes", strings.Repeat("a", 40))
	pr.Tags = append(pr.Tags,
		nostr.Tag{"r", strings.Repeat("b", 64)},
		nostr.Tag{"base-commit", strings.Repeat("d", 40)},
		nostr.Tag{"merge-base", strings.Repeat("c", 40)},
		nostr.Tag{"branch-name", "feature/reviews"},
		nostr.Tag{"target-branch", "main"},
		nostr.Tag{"h", "opaque-project-origin"},
		nostr.Tag{"imeta", "url https://media.example/review.png"},
		nostr.Tag{"x-vendor", "preserve-me"},
	)
	recognized, err := ValidateEvent(pr)
	require.True(t, recognized)
	require.NoError(t, err)
	require.Equal(t, []string{"https://one.example/notes.git", "ssh://two/notes.git"}, cloneLocators(pr.Tags))
	require.Equal(t, "preserve-me", pr.Tags.Find("x-vendor")[1], "validation must not rewrite unknown tags")

	for _, length := range []int{40, 64} {
		candidate := pr
		candidate.Tags = replaceNamedTag(pr.Tags, "c", nostr.Tag{"c", strings.Repeat("d", length)})
		_, err := ValidateEvent(candidate)
		require.NoError(t, err, "full %d-character commit IDs are valid", length)
	}

	tests := []struct {
		name string
		edit func(nostr.Event) nostr.Event
		want string
	}{
		{"missing repository", func(event nostr.Event) nostr.Event { event.Tags = removeNamedTags(event.Tags, "a"); return event }, "exactly one repository"},
		{"owner recipient", func(event nostr.Event) nostr.Event { event.Tags = removeNamedTags(event.Tags, "p"); return event }, "repository owner"},
		{"empty subject", func(event nostr.Event) nostr.Event {
			event.Tags = replaceNamedTag(event.Tags, "subject", nostr.Tag{"subject", ""})
			return event
		}, "nonempty"},
		{"subject bytes", func(event nostr.Event) nostr.Event {
			event.Tags = replaceNamedTag(event.Tags, "subject", nostr.Tag{"subject", strings.Repeat("é", 129)})
			return event
		}, "256"},
		{"missing commit", func(event nostr.Event) nostr.Event { event.Tags = removeNamedTags(event.Tags, "c"); return event }, "required"},
		{"short commit", func(event nostr.Event) nostr.Event {
			event.Tags = replaceNamedTag(event.Tags, "c", nostr.Tag{"c", "abc"})
			return event
		}, "full SHA"},
		{"nonhex commit", func(event nostr.Event) nostr.Event {
			event.Tags = replaceNamedTag(event.Tags, "c", nostr.Tag{"c", strings.Repeat("z", 40)})
			return event
		}, "full SHA"},
		{"missing clone", func(event nostr.Event) nostr.Event { event.Tags = removeNamedTags(event.Tags, "clone"); return event }, "clone locator"},
		{"empty clone", func(event nostr.Event) nostr.Event {
			event.Tags = replaceNamedTag(event.Tags, "clone", nostr.Tag{"clone", ""})
			return event
		}, "nonempty"},
		{"bad merge base", func(event nostr.Event) nostr.Event {
			event.Tags = replaceNamedTag(event.Tags, "merge-base", nostr.Tag{"merge-base", "deadbeef"})
			return event
		}, "merge-base"},
		{"bad base commit", func(event nostr.Event) nostr.Event {
			event.Tags = replaceNamedTag(event.Tags, "base-commit", nostr.Tag{"base-commit", "deadbeef"})
			return event
		}, "base-commit"},
		{"bad revision", func(event nostr.Event) nostr.Event {
			event.Tags = append(event.Tags.CloneDeep(), nostr.Tag{"e", "bad"})
			return event
		}, "revision"},
		{"oversize origin", func(event nostr.Event) nostr.Event {
			event.Tags = replaceNamedTag(event.Tags, "h", nostr.Tag{"h", strings.Repeat("x", 257)})
			return event
		}, "256"},
		{"oversize content", func(event nostr.Event) nostr.Event {
			event.Content = strings.Repeat("x", maxPullRequestContent+1)
			return event
		}, "64 KiB"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ValidateEvent(test.edit(pr))
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestValidatePullRequestUpdateAndMergeStatus(t *testing.T) {
	owner := pubkey(t, "8")
	author := pubkey(t, "9")
	rootID := id(t, "8")
	coordinate := RepositoryCoordinate{Owner: owner, ID: "notes"}
	update := nostr.Event{
		Kind: KindPullRequestUpdate, Content: "rebased",
		Tags: nostr.Tags{
			{"a", coordinate.String()}, {"p", owner.Hex()},
			{"E", rootID.Hex()}, {"P", author.Hex()},
			{"c", strings.Repeat("d", 64)},
			{"clone", "https://one.example/notes.git"},
			{"clone", "ssh://two/notes.git"},
			{"r", strings.Repeat("e", 40)},
			{"base-commit", strings.Repeat("b", 40)},
			{"merge-base", strings.Repeat("f", 64)},
		},
	}
	recognized, err := ValidateEvent(update)
	require.True(t, recognized)
	require.NoError(t, err)

	for _, name := range []string{"a", "p", "E", "P", "c", "clone"} {
		candidate := update
		candidate.Tags = removeNamedTags(update.Tags, name)
		_, err := ValidateEvent(candidate)
		require.Error(t, err, "missing %s must fail", name)
	}

	mergeCommit := strings.Repeat("a", 40)
	merged := nostr.Event{
		Kind: nostr.KindStatusApplied,
		Tags: nostr.Tags{{"e", rootID.Hex(), "", "root"}, {"merge-commit", mergeCommit}, {"r", mergeCommit}, {"q", id(t, "9").Hex()}, {"applied-as-commits", strings.Repeat("b", 64)}},
	}
	_, err = ValidateEvent(merged)
	require.NoError(t, err)

	wrongKind := merged
	wrongKind.Kind = nostr.KindStatusClosed
	_, err = ValidateEvent(wrongKind)
	require.ErrorContains(t, err, "only valid on kind 1631")

	missingRevision := merged
	missingRevision.Tags = removeNamedTags(merged.Tags, "r")
	_, err = ValidateEvent(missingRevision)
	require.ErrorContains(t, err, "matching r")

	badAppliedCommit := merged
	badAppliedCommit.Tags = replaceNamedTag(merged.Tags, "applied-as-commits", nostr.Tag{"applied-as-commits", "short"})
	_, err = ValidateEvent(badAppliedCommit)
	require.ErrorContains(t, err, "applied-as-commits")
}

func TestValidateEventClaimsBothPullRequestCommentEncodings(t *testing.T) {
	owner := pubkey(t, "a")
	rootID := id(t, "a")
	coordinate := RepositoryCoordinate{Owner: owner, ID: "notes"}
	buzz := nostr.Event{Kind: nostr.KindTextNote, Tags: nostr.Tags{
		{"e", rootID.Hex(), "", "root"}, {"a", coordinate.String()}, {"t", "approval"}, {"c", strings.Repeat("a", 40)},
	}}
	recognized, err := ValidateEvent(buzz)
	require.True(t, recognized)
	require.NoError(t, err)

	nip22 := nostr.Event{Kind: nostr.KindComment, Tags: nostr.Tags{
		{"E", rootID.Hex()}, {"K", "1618"}, {"P", owner.Hex()},
		{"e", id(t, "b").Hex()}, {"k", "1111"}, {"p", owner.Hex()},
		{"t", "changes-requested"},
	}}
	recognized, err = ValidateEvent(nip22)
	require.True(t, recognized)
	require.NoError(t, err)
}

func TestBuildProjectTaskAnchorIsLogicalOnly(t *testing.T) {
	owner := pubkey(t, "6")
	project := nostr.Event{
		Kind:    KindProject,
		PubKey:  owner,
		Content: "project content",
		Tags: nostr.Tags{
			{"d", "notes-project"},
			{"name", "Notes Project"},
			{"description", "Ship Notes v2"},
			{TagBuzzChannel, "notes"},
			{"x-vendor", "preserved-on-project"},
		},
	}

	anchor, coordinate, err := BuildProjectTaskAnchor(project)
	require.NoError(t, err)
	require.Equal(t, RepositoryCoordinate{Owner: owner, ID: "notes-project"}, coordinate)
	require.Equal(t, nostr.KindRepositoryAnnouncement, anchor.Kind)
	require.Equal(t, "Ship Notes v2", anchor.Content)
	require.Equal(t, "notes-project", anchor.Tags.GetD())
	require.Equal(t, "notes", anchor.Tags.Find(TagBuzzChannel)[1])
	require.Equal(t, nostr.Tag{TagProjectTaskAnchor, "true"}, anchor.Tags.Find(TagProjectTaskAnchor))
	require.True(t, IsProjectTaskAnchor(nostr.Event{Kind: anchor.Kind, Tags: anchor.Tags}))
	require.Nil(t, anchor.Tags.Find("clone"))
	require.Nil(t, anchor.Tags.Find("web"))
	require.Nil(t, anchor.Tags.Find("synthetic"))

	updated, err := AddRepository(project, coordinate, "wss://relay.example")
	require.NoError(t, err)
	require.Equal(t, "x-vendor", updated.Tags[4][0])
	require.Equal(t, coordinate.String(), updated.Tags[5][1])

	_, err = AddRepository(projectWithCoordinate(project, coordinate), coordinate, "")
	require.ErrorContains(t, err, "unique")
}

func TestRepositoryTaskAnchorMarkerValidation(t *testing.T) {
	base := nostr.Event{
		Kind:   nostr.KindRepositoryAnnouncement,
		PubKey: pubkey(t, "6"),
		Tags:   nostr.Tags{{"d", "notes"}, {TagBuzzChannel, "notes"}},
	}
	for _, tag := range []nostr.Tag{
		{TagProjectTaskAnchor},
		{TagProjectTaskAnchor, "false"},
		{TagProjectTaskAnchor, "true", "extra"},
	} {
		event := base
		event.Tags = append(base.Tags.CloneDeep(), tag)
		recognized, err := ValidateEvent(event)
		require.True(t, recognized)
		require.ErrorContains(t, err, TagProjectTaskAnchor)
	}

	event := base
	event.Tags = append(base.Tags.CloneDeep(), nostr.Tag{TagProjectTaskAnchor, "true"}, nostr.Tag{TagProjectTaskAnchor, "true"})
	_, err := ValidateEvent(event)
	require.ErrorContains(t, err, "singleton")

	for _, forbidden := range []nostr.Tag{{"clone", "https://example.com/notes.git"}, {"web", "https://example.com/notes"}} {
		event = base
		event.Tags = append(base.Tags.CloneDeep(), nostr.Tag{TagProjectTaskAnchor, "true"}, forbidden)
		_, err = ValidateEvent(event)
		require.ErrorContains(t, err, "must not contain clone or web")
	}
}

func projectWithCoordinate(project nostr.Event, coordinate RepositoryCoordinate) nostr.Event {
	project.Tags = append(project.Tags.CloneDeep(), nostr.Tag{"a", coordinate.String()})
	return project
}

func validPullRequestEvent(t *testing.T, eventID nostr.ID, author, owner nostr.PubKey, repositoryID, commit string) nostr.Event {
	t.Helper()
	return nostr.Event{
		ID: eventID, Kind: KindPullRequest, PubKey: author, Content: "Review body",
		Tags: nostr.Tags{
			{"a", RepositoryCoordinate{Owner: owner, ID: repositoryID}.String()},
			{"p", owner.Hex()}, {"subject", "Review title"}, {"c", commit},
			{"clone", "https://one.example/notes.git", "ssh://two/notes.git"},
		},
	}
}

func removeNamedTags(tags nostr.Tags, name string) nostr.Tags {
	result := make(nostr.Tags, 0, len(tags))
	for _, tag := range tags {
		if len(tag) == 0 || tag[0] != name {
			result = append(result, append(nostr.Tag(nil), tag...))
		}
	}
	return result
}

func replaceNamedTag(tags nostr.Tags, name string, replacement nostr.Tag) nostr.Tags {
	result := removeNamedTags(tags, name)
	return append(result, replacement)
}

func pubkey(t *testing.T, digit string) nostr.PubKey {
	t.Helper()
	secret, err := nostr.SecretKeyFromHex(digit)
	require.NoError(t, err)
	return secret.Public()
}

func id(t *testing.T, digit string) nostr.ID {
	t.Helper()
	value, err := nostr.IDFromHex(strings.Repeat(digit, 64))
	require.NoError(t, err)
	return value
}
