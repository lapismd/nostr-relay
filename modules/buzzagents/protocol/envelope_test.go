package protocol

import (
	"encoding/base64"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
)

func TestValidateDurableEnvelope(t *testing.T) {
	agent := nostr.Generate().Public()
	owner := nostr.Generate().Public()
	ciphertext := testNIP44V2()
	tests := []struct {
		name    string
		event   nostr.Event
		wantErr bool
	}{
		{"engram", nostr.Event{Kind: KindAgentEngram, PubKey: agent, Tags: nostr.Tags{{"d", strings.Repeat("a", 64)}, {"p", owner.Hex()}}, Content: ciphertext}, false},
		{"engram bad d", nostr.Event{Kind: KindAgentEngram, PubKey: agent, Tags: nostr.Tags{{"d", "memory"}, {"p", owner.Hex()}}, Content: ciphertext}, true},
		{"engram plaintext", nostr.Event{Kind: KindAgentEngram, PubKey: agent, Tags: nostr.Tags{{"d", strings.Repeat("a", 64)}, {"p", owner.Hex()}}, Content: strings.Repeat("x", 132)}, true},
		{"metric", nostr.Event{Kind: KindAgentTurnMetric, PubKey: agent, Tags: nostr.Tags{{"p", owner.Hex()}, {"agent", agent.Hex()}}, Content: ciphertext}, false},
		{"metric channel", nostr.Event{Kind: KindAgentTurnMetric, PubKey: agent, Tags: nostr.Tags{{"p", owner.Hex()}, {"agent", agent.Hex()}, {"h", "channel"}}, Content: ciphertext}, true},
		{"persona", nostr.Event{Kind: KindPersona, PubKey: owner, Tags: nostr.Tags{{"d", "code-review_1"}, {"shared", "true"}}}, false},
		{"persona invalid slug", nostr.Event{Kind: KindPersona, PubKey: owner, Tags: nostr.Tags{{"d", "Code review"}}}, true},
		{"catalog builtin", nostr.Event{Kind: KindSharedTeamCatalog, PubKey: owner, Tags: nostr.Tags{{"d", "builtin-team:welcome"}}}, false},
		{"catalog duplicate shared", nostr.Event{Kind: KindSharedTeamCatalog, PubKey: owner, Tags: nostr.Tags{{"d", "team"}, {"shared", "true"}, {"shared", "true"}}}, true},
		{"relay presence snapshot", nostr.Event{Kind: KindPresenceSnapshot}, true},
		{"relay notification", nostr.Event{Kind: KindMemberAddedNotification}, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateDurableEnvelope(test.event)
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v, wantErr=%v", err, test.wantErr)
			}
		})
	}
}

func testNIP44V2() string {
	payload := make([]byte, 99)
	payload[0] = 0x02
	return base64.StdEncoding.EncodeToString(payload)
}
