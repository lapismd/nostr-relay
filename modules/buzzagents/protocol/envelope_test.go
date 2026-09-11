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
		{"persona", nostr.Event{Kind: KindPersona, PubKey: owner, Tags: nostr.Tags{{"d", "code-review_1"}, {"shared", "true"}}, Content: `{"display_name":"Code review","avatar_url":"https://example.test/avatar.png","system_prompt":"Review carefully"}`}, false},
		{"persona nullable optionals", nostr.Event{Kind: KindPersona, PubKey: owner, Tags: nostr.Tags{{"d", "review"}}, Content: `{"display_name":"Review","avatar_url":null,"system_prompt":null}`}, false},
		{"persona invalid slug", nostr.Event{Kind: KindPersona, PubKey: owner, Tags: nostr.Tags{{"d", "Code review"}}, Content: `{"display_name":"Code review"}`}, true},
		{"persona missing name", nostr.Event{Kind: KindPersona, PubKey: owner, Tags: nostr.Tags{{"d", "review"}}, Content: `{}`}, true},
		{"persona invalid prompt", nostr.Event{Kind: KindPersona, PubKey: owner, Tags: nostr.Tags{{"d", "review"}}, Content: `{"display_name":"Review","system_prompt":3}`}, true},
		{"persona secret field", nostr.Event{Kind: KindPersona, PubKey: owner, Tags: nostr.Tags{{"d", "review"}}, Content: `{"display_name":"Review","private_key":"no"}`}, true},
		{"persona environment field", nostr.Event{Kind: KindPersona, PubKey: owner, Tags: nostr.Tags{{"d", "review"}}, Content: `{"display_name":"Review","env_vars":{"TOKEN":"no"}}`}, true},
		{"managed legacy", nostr.Event{Kind: KindManagedAgent, PubKey: owner, Tags: nostr.Tags{{"d", agent.Hex()}}, Content: `{}`}, false},
		{"managed linked", nostr.Event{Kind: KindManagedAgent, PubKey: owner, Tags: nostr.Tags{{"d", agent.Hex()}}, Content: `{"persona_id":"review","name":"Ada","name_mode":"instance","respond_to":"owner-only","parallelism":10}`}, false},
		{"managed invalid mode", nostr.Event{Kind: KindManagedAgent, PubKey: owner, Tags: nostr.Tags{{"d", agent.Hex()}}, Content: `{"name_mode":"matching-string"}`}, true},
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

func TestParsePersonaRejectsOversizeContent(t *testing.T) {
	content := `{"display_name":"` + strings.Repeat("x", MaxPersonaContentBytes) + `"}`
	if _, err := ParsePersona(content); err == nil {
		t.Fatal("expected oversized persona content to be rejected")
	}
}

func testNIP44V2() string {
	payload := make([]byte, 99)
	payload[0] = 0x02
	return base64.StdEncoding.EncodeToString(payload)
}
