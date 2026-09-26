package agentbunker

import (
	"encoding/base64"
	"fiatjaf.com/nostr"
	"testing"
)

func TestManagedRuntimeSnapshotSigning(t *testing.T) {
	service, _, _ := testService(t)
	owner, agent := nostr.Generate().Public(), nostr.Generate().Public()
	a := Agent{Pubkey: agent.Hex(), OwnerPubkey: owner.Hex(), Status: StatusActive}
	event := nostr.Event{Kind: 30180, CreatedAt: nostr.Timestamp(service.Now().Unix()), Tags: nostr.Tags{{"p", owner.Hex()}, {"d", owner.Hex()}, {"agent", agent.Hex()}}, Content: base64.StdEncoding.EncodeToString(append([]byte{2}, make([]byte, 98)...))}
	if err := service.authorizeSigning(a, event); err != nil {
		t.Fatal(err)
	}
	event.Kind = 30181
	if service.authorizeSigning(a, event) == nil {
		t.Fatal("agent signed owner request")
	}
	event.Kind = 30180
	event.Tags[0][1] = nostr.Generate().Public().Hex()
	if service.authorizeSigning(a, event) == nil {
		t.Fatal("snapshot to stranger accepted")
	}
}
