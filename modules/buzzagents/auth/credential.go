// Package auth verifies Buzz NIP-OA owner credentials without depending on a
// relay implementation or transport.
package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"fiatjaf.com/nostr"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
)

const domainSeparator = "nostr:agent-auth:"

var (
	ErrMissingCredential   = errors.New("missing owner credential")
	ErrMultipleCredentials = errors.New("multiple owner credentials")
)

// Credential is a verified NIP-OA owner attestation for one agent key.
// Conditions retains its exact signed spelling; parsed clauses are used only
// for evaluation and are never reserialized for signature verification.
type Credential struct {
	Agent      nostr.PubKey
	Owner      nostr.PubKey
	Conditions string
	kinds      []nostr.Kind
	before     []uint64
	after      []uint64
}

// Extract returns the sole auth tag. NIP-OA treats zero or multiple auth tags
// as having no valid credential.
func Extract(tags nostr.Tags) (nostr.Tag, error) {
	var found nostr.Tag
	for _, tag := range tags {
		if len(tag) == 0 || tag[0] != "auth" {
			continue
		}
		if found != nil {
			return nil, ErrMultipleCredentials
		}
		found = tag
	}
	if found == nil {
		return nil, ErrMissingCredential
	}
	return found, nil
}

// ParseJSON decodes an X-Auth-Tag header as a single NIP-OA tag.
func ParseJSON(raw string, agent nostr.PubKey) (Credential, error) {
	var tag nostr.Tag
	if err := json.Unmarshal([]byte(raw), &tag); err != nil {
		return Credential{}, fmt.Errorf("invalid owner credential JSON: %w", err)
	}
	return Verify(tag, agent)
}

// Verify validates the exact NIP-OA tag shape, grammar, and BIP-340 signature.
func Verify(tag nostr.Tag, agent nostr.PubKey) (Credential, error) {
	if len(tag) != 4 || tag[0] != "auth" {
		return Credential{}, errors.New("owner credential must contain exactly four elements")
	}
	if !canonicalLowerHex(tag[1], 64) {
		return Credential{}, errors.New("owner pubkey must be 64-character lowercase hex")
	}
	owner, err := nostr.PubKeyFromHex(tag[1])
	if err != nil {
		return Credential{}, errors.New("owner pubkey is invalid")
	}
	if owner == agent {
		return Credential{}, errors.New("owner credential cannot self-attest")
	}
	if !canonicalLowerHex(tag[3], 128) {
		return Credential{}, errors.New("owner signature must be 128-character lowercase hex")
	}

	credential := Credential{Agent: agent, Owner: owner, Conditions: tag[2]}
	if err := credential.parseConditions(); err != nil {
		return Credential{}, err
	}

	pubkey, err := schnorr.ParsePubKey(owner[:])
	if err != nil {
		return Credential{}, errors.New("owner pubkey is not a valid BIP-340 key")
	}
	sigBytes, _ := hex.DecodeString(tag[3])
	sig, err := schnorr.ParseSignature(sigBytes)
	if err != nil {
		return Credential{}, errors.New("owner signature is invalid")
	}
	digest := sha256.Sum256([]byte(domainSeparator + agent.Hex() + ":" + tag[2]))
	if !sig.Verify(digest[:], pubkey) {
		return Credential{}, errors.New("owner signature does not verify")
	}
	return credential, nil
}

// VerifyForAuthEvent applies the NIP-AA subset of NIP-OA conditions. Kind
// clauses are retained for optional per-event enforcement and do not affect
// connection admission.
func VerifyForAuthEvent(tag nostr.Tag, agent nostr.PubKey, createdAt uint64) (Credential, error) {
	credential, err := Verify(tag, agent)
	if err != nil {
		return Credential{}, err
	}
	if !credential.AllowsCreatedAt(createdAt) {
		return Credential{}, errors.New("owner credential does not allow AUTH timestamp")
	}
	return credential, nil
}

// VerifyEvent performs full NIP-OA condition evaluation for an ordinary event.
func VerifyEvent(event nostr.Event) (Credential, error) {
	tag, err := Extract(event.Tags)
	if err != nil {
		return Credential{}, err
	}
	credential, err := Verify(tag, event.PubKey)
	if err != nil {
		return Credential{}, err
	}
	if !credential.AllowsCreatedAt(uint64(event.CreatedAt)) || !credential.AllowsKind(event.Kind) {
		return Credential{}, errors.New("owner credential conditions do not allow event")
	}
	return credential, nil
}

func (credential *Credential) parseConditions() error {
	if credential.Conditions == "" {
		return nil
	}
	if !isASCII(credential.Conditions) || strings.ContainsAny(credential.Conditions, " \t\r\n") {
		return errors.New("owner credential conditions must be ASCII without whitespace")
	}
	for _, clause := range strings.Split(credential.Conditions, "&") {
		if clause == "" {
			return errors.New("owner credential contains an empty condition")
		}
		switch {
		case strings.HasPrefix(clause, "kind="):
			value, err := canonicalDecimal(strings.TrimPrefix(clause, "kind="), 65535)
			if err != nil {
				return fmt.Errorf("invalid kind condition: %w", err)
			}
			credential.kinds = append(credential.kinds, nostr.Kind(value))
		case strings.HasPrefix(clause, "created_at<"):
			value, err := canonicalDecimal(strings.TrimPrefix(clause, "created_at<"), 4294967295)
			if err != nil {
				return fmt.Errorf("invalid created_at condition: %w", err)
			}
			credential.before = append(credential.before, value)
		case strings.HasPrefix(clause, "created_at>"):
			value, err := canonicalDecimal(strings.TrimPrefix(clause, "created_at>"), 4294967295)
			if err != nil {
				return fmt.Errorf("invalid created_at condition: %w", err)
			}
			credential.after = append(credential.after, value)
		default:
			return fmt.Errorf("unsupported owner credential condition %q", clause)
		}
	}
	return nil
}

// AllowsCreatedAt evaluates every timestamp clause conjunctively.
func (credential Credential) AllowsCreatedAt(createdAt uint64) bool {
	for _, bound := range credential.before {
		if createdAt >= bound {
			return false
		}
	}
	for _, bound := range credential.after {
		if createdAt <= bound {
			return false
		}
	}
	return true
}

// AllowsKind evaluates every kind clause conjunctively. Multiple distinct
// kind clauses therefore authorize no event, as specified by NIP-OA/NIP-AA.
func (credential Credential) AllowsKind(kind nostr.Kind) bool {
	for _, allowed := range credential.kinds {
		if kind != allowed {
			return false
		}
	}
	return true
}

func canonicalDecimal(raw string, max uint64) (uint64, error) {
	if raw == "" || (len(raw) > 1 && raw[0] == '0') {
		return 0, errors.New("decimal must use canonical base-10 encoding")
	}
	for _, char := range raw {
		if char < '0' || char > '9' {
			return 0, errors.New("decimal must use canonical base-10 encoding")
		}
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || value > max {
		return 0, fmt.Errorf("decimal is outside 0..%d", max)
	}
	return value, nil
}

func canonicalLowerHex(raw string, length int) bool {
	if len(raw) != length {
		return false
	}
	for _, char := range raw {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return true
}

func isASCII(raw string) bool {
	for _, char := range raw {
		if char > 127 {
			return false
		}
	}
	return true
}
