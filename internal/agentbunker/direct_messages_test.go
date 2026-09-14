package agentbunker

import (
	"encoding/json"
	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestManagedDirectMessageParticipantPolicy(t *testing.T) {
	service, _, _ := testService(t)
	owner, agentKey, peer, outsider := nostr.Generate(), nostr.Generate(), nostr.Generate(), nostr.Generate()
	agent := Agent{Pubkey: agentKey.Public().Hex(), OwnerPubkey: owner.Public().Hex(), Status: StatusActive}
	instance := nostr.Event{Kind: 30177, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"d", agent.Pubkey}}}
	setPolicy := func(content string, key nostr.SecretKey) {
		instance.Content = content
		require.NoError(t, instance.Sign(key))
	}
	service.CurrentEvent = func(kind nostr.Kind, author nostr.PubKey, d string) *nostr.Event {
		if kind == 30177 && author == owner.Public() && d == agent.Pubkey {
			return &instance
		}
		return nil
	}
	rumor := nostr.Event{Kind: 14, PubKey: agentKey.Public(), CreatedAt: nostr.Now(), Tags: nostr.Tags{{"p", peer.Public().Hex()}}, Content: "Private reply"}
	encode := func() string {
		rumor.ID = rumor.GetID()
		value, err := json.Marshal(rumor)
		require.NoError(t, err)
		return string(value)
	}
	setPolicy("{}", owner)
	require.Error(t, service.authorizeDirectContent(agent, peer.Public().Hex(), encode(), true))
	policy, _ := json.Marshal(map[string]any{"dm_allowlist": []string{peer.Public().Hex()}})
	setPolicy(string(policy), owner)
	require.NoError(t, service.authorizeDirectContent(agent, peer.Public().Hex(), encode(), true))
	require.NoError(t, service.authorizeDirectContent(agent, agent.Pubkey, encode(), true))
	require.Error(t, service.authorizeDirectContent(agent, peer.Public().Hex(), `{"command":"runtime_set"}`, true))
	require.NoError(t, service.authorizeDirectContent(agent, agent.OwnerPubkey, `{"command":"runtime_set"}`, true))
	rumor.Tags = append(rumor.Tags, nostr.Tag{"p", outsider.Public().Hex()})
	require.Error(t, service.authorizeDirectContent(agent, peer.Public().Hex(), encode(), true))
	rumor.Tags = rumor.Tags[:1]
	setPolicy(string(policy), peer)
	require.Error(t, service.authorizeDirectContent(agent, peer.Public().Hex(), encode(), true))
	setPolicy(`{"dm_allowlist":["invalid"]}`, owner)
	require.Error(t, service.authorizeDirectContent(agent, peer.Public().Hex(), encode(), true))
	setPolicy("{}", owner)
	require.Error(t, service.authorizeDirectContent(agent, peer.Public().Hex(), encode(), true))
}
