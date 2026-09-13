package agentbunker

import (
	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestManagedCanvasSigning(t *testing.T) {
	service, _, _ := testService(t)
	secret, owner := nostr.Generate(), nostr.Generate()
	agent := Agent{Pubkey: secret.Public().Hex(), OwnerPubkey: owner.Public().Hex(), Status: StatusActive}
	metadata := nostr.Event{Kind: 39000, Tags: nostr.Tags{{"d", "room"}}}
	admins := nostr.Event{Kind: 39001, Tags: nostr.Tags{{"d", "room"}, {"p", secret.Public().Hex()}}}
	service.CurrentEvent = func(kind nostr.Kind, author nostr.PubKey, d string) *nostr.Event {
		if author != service.Config.RelayPubkey || d != "room" {
			return nil
		}
		if kind == 39000 {
			return &metadata
		}
		if kind == 39001 {
			return &admins
		}
		return nil
	}
	service.IsMember = func(group string, pubkey nostr.PubKey) bool { return group == "room" && pubkey == secret.Public() }
	event := nostr.Event{Kind: 40100, CreatedAt: nostr.Timestamp(service.Now().Unix()), Tags: nostr.Tags{{"h", "room"}}, Content: "# Shared"}
	require.NoError(t, service.authorizeSigning(agent, event))
	event.Content = ""
	require.NoError(t, service.authorizeSigning(agent, event))
	event.Content = strings.Repeat("é", 32769)
	require.Error(t, service.authorizeSigning(agent, event))
	event.Content = "draft"
	metadata.Tags = append(metadata.Tags, nostr.Tag{"archived"})
	require.Error(t, service.authorizeSigning(agent, event))
	metadata.Tags = nostr.Tags{{"d", "room"}}
	admins.Tags = nostr.Tags{{"d", "room"}}
	require.Error(t, service.authorizeSigning(agent, event))
	event.Tags = append(event.Tags, nostr.Tag{"h", "room"})
	require.Error(t, service.authorizeSigning(agent, event))
}
