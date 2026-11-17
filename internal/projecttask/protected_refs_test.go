package projecttask

import (
	"testing"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
)

func TestAnnouncementProtectedRefTagsDedupesAndCaps(t *testing.T) {
	event := nostr.Event{Tags: nostr.Tags{
		{TagProtectedRef, "release/*"},
		{TagProtectedRef, " release/* "},
		{"d", "notes"},
		{TagProtectedRef, ""},
		{TagProtectedRef, "hotfix/*"},
	}}
	require.Equal(t, []string{"release/*", "hotfix/*"}, AnnouncementProtectedRefTags(event))
}
