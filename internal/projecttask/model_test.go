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

func projectWithCoordinate(project nostr.Event, coordinate RepositoryCoordinate) nostr.Event {
	project.Tags = append(project.Tags.CloneDeep(), nostr.Tag{"a", coordinate.String()})
	return project
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
