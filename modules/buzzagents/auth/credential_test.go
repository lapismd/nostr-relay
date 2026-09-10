package auth

import (
	"testing"

	"fiatjaf.com/nostr"
)

const (
	testAgent = "c6047f9441ed7d6d3045406e95c07cd85c778e4b8cef3ca7abac09b95c709ee5"
	testOwner = "79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"
	testSig   = "8b7df2575caf0a108374f8471722b233c53f9ff827a8b0f91861966c3b9dd5cb2e189eae9f49d72187674c2f5bd244145e10ff86c9f257ffe65a1ee5f108b369"
)

func TestNIPOATestVector(t *testing.T) {
	agent := nostr.MustPubKeyFromHex(testAgent)
	tag := nostr.Tag{"auth", testOwner, "kind=1&created_at<1713957000", testSig}
	credential, err := Verify(tag, agent)
	if err != nil {
		t.Fatalf("verify published NIP-OA vector: %v", err)
	}
	if credential.Owner.Hex() != testOwner || !credential.AllowsKind(1) || credential.AllowsKind(7) {
		t.Fatalf("unexpected parsed credential: %+v", credential)
	}
	if !credential.AllowsCreatedAt(1713956400) || credential.AllowsCreatedAt(1713957000) {
		t.Fatal("created_at condition was not enforced")
	}
}

func TestNIPOAStrictGrammar(t *testing.T) {
	agent := nostr.MustPubKeyFromHex(testAgent)
	invalid := []nostr.Tag{
		{"auth", testOwner, "kind=01", testSig},
		{"auth", testOwner, "kind=1&", testSig},
		{"auth", testOwner, "created_at<=1", testSig},
		{"auth", testOwner, "kind=65536", testSig},
		{"auth", testOwner, "kind=1 ", testSig},
		{"auth", stringsToUpper(testOwner), "kind=1", testSig},
		{"auth", testAgent, "kind=1", testSig},
	}
	for _, tag := range invalid {
		if _, err := Verify(tag, agent); err == nil {
			t.Fatalf("accepted invalid tag: %#v", tag)
		}
	}
}

func TestExtractRejectsMultipleTags(t *testing.T) {
	tags := nostr.Tags{{"auth", "one"}, {"p", testOwner}, {"auth", "two"}}
	if _, err := Extract(tags); err != ErrMultipleCredentials {
		t.Fatalf("got %v, want %v", err, ErrMultipleCredentials)
	}
}

func stringsToUpper(raw string) string {
	result := []byte(raw)
	for index, char := range result {
		if char >= 'a' && char <= 'f' {
			result[index] = char - ('a' - 'A')
		}
	}
	return string(result)
}
