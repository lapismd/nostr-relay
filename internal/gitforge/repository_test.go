package gitforge

import (
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
	"zooid/internal/projecttask"
)

func TestClassifyRepository(t *testing.T) {
	owner := nostr.MustSecretKeyFromHex(strings.Repeat("1", 64)).Public()
	base := nostr.Event{
		Kind:   nostr.KindRepositoryAnnouncement,
		PubKey: owner,
		Tags:   nostr.Tags{{"d", "notes"}, {projecttask.TagBuzzChannel, "notes-channel"}},
	}

	hosted, err := ClassifyRepository(base, "https://relay.example/prefix")
	require.NoError(t, err)
	require.Equal(t, RepositoryHosted, hosted.Class)
	require.Equal(t, "notes-channel", hosted.Channel)
	require.Equal(t, "https://relay.example/prefix/git/"+owner.Hex()+"/notes", hosted.CloneURL)

	local := base
	local.Tags = append(base.Tags.CloneDeep(), nostr.Tag{"clone", hosted.CloneURL + ".git", "https://github.com/acme/notes.git"})
	classified, err := ClassifyRepository(local, "https://relay.example/prefix/")
	require.NoError(t, err)
	require.Equal(t, RepositoryHosted, classified.Class)

	external := base
	external.Tags = append(base.Tags.CloneDeep(), nostr.Tag{"clone", "https://github.com/acme/notes.git"})
	classified, err = ClassifyRepository(external, "https://relay.example")
	require.NoError(t, err)
	require.Equal(t, RepositoryExternal, classified.Class)

	anchor := base
	anchor.Tags = append(base.Tags.CloneDeep(), nostr.Tag{projecttask.TagProjectTaskAnchor, "true"})
	classified, err = ClassifyRepository(anchor, "https://relay.example")
	require.NoError(t, err)
	require.Equal(t, RepositoryAnchor, classified.Class)
}

func TestHostedRepositoryConstraintsDoNotNarrowExternalRepositories(t *testing.T) {
	owner := nostr.MustSecretKeyFromHex(strings.Repeat("2", 64)).Public()
	base := nostr.Event{Kind: nostr.KindRepositoryAnnouncement, PubKey: owner, Tags: nostr.Tags{
		{"d", "external/repository"}, {projecttask.TagBuzzChannel, "channel"},
	}}

	_, err := ClassifyRepository(base, "https://relay.example")
	require.ErrorContains(t, err, "ASCII")

	external := base
	external.Tags = append(base.Tags.CloneDeep(), nostr.Tag{"clone", "https://example.com/external.git"})
	classified, err := ClassifyRepository(external, "https://relay.example")
	require.NoError(t, err)
	require.Equal(t, RepositoryExternal, classified.Class)
}

func TestClassifyHostedRequiresChannel(t *testing.T) {
	owner := nostr.MustSecretKeyFromHex(strings.Repeat("3", 64)).Public()
	event := nostr.Event{Kind: nostr.KindRepositoryAnnouncement, PubKey: owner, Tags: nostr.Tags{{"d", "notes"}}}
	_, err := ClassifyRepository(event, "https://relay.example")
	require.ErrorContains(t, err, "buzz-channel")
}

func TestCanonicalCloneURLValidation(t *testing.T) {
	owner := nostr.MustSecretKeyFromHex(strings.Repeat("4", 64)).Public()
	coordinate := projecttask.RepositoryCoordinate{Owner: owner, ID: "notes"}
	for _, invalid := range []string{"", "relay.example", "ssh://relay.example", "https://user@relay.example", "https://relay.example?q=x"} {
		_, err := CanonicalCloneURL(invalid, coordinate)
		require.Error(t, err, invalid)
	}
	clone, err := CanonicalCloneURL("https://relay.example/a%20b", coordinate)
	require.NoError(t, err)
	require.Equal(t, "https://relay.example/a%20b/git/"+owner.Hex()+"/notes", clone)
}
