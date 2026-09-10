package protocol

import (
	"encoding/base64"
	"errors"
	"fmt"
	"unicode"
	"unicode/utf8"

	"fiatjaf.com/nostr"
)

// ValidateDurableEnvelope enforces the public portions of Buzz agent events
// before storage. Ciphertext remains opaque to the relay.
func ValidateDurableEnvelope(event nostr.Event) error {
	switch event.Kind {
	case KindPresenceSnapshot:
		return errors.New("presence snapshots are relay-signed query projections only")
	case KindMemberAddedNotification, KindMemberRemovedNotification:
		return errors.New("membership notifications are relay-signed only")
	case KindAgentEngram:
		d, err := exactTag(event.Tags, "d")
		if err != nil {
			return fmt.Errorf("agent-engram %w", err)
		}
		if !isLowerHex(d, 64) {
			return errors.New("agent-engram d tag must be 64 lowercase hex chars")
		}
		owner, err := exactTag(event.Tags, "p")
		if err != nil {
			return fmt.Errorf("agent-engram %w", err)
		}
		if !isLowerHex(owner, 64) {
			return errors.New("agent-engram p tag must be 64 lowercase hex chars")
		}
		if err := ValidateNIP44V2(event.Content); err != nil {
			return fmt.Errorf("agent-engram content %w", err)
		}
	case KindAgentTurnMetric:
		owner, err := exactTag(event.Tags, "p")
		if err != nil {
			return fmt.Errorf("agent-turn-metric %w", err)
		}
		if !isLowerHex(owner, 64) {
			return errors.New("agent-turn-metric p tag must be 64 lowercase hex chars")
		}
		agent, err := exactTag(event.Tags, "agent")
		if err != nil {
			return fmt.Errorf("agent-turn-metric %w", err)
		}
		if !isLowerHex(agent, 64) || agent != event.PubKey.Hex() {
			return errors.New("agent-turn-metric agent tag must equal the event pubkey")
		}
		if hasTag(event.Tags, "h") {
			return errors.New("agent-turn-metric event must not have an h tag")
		}
		if err := ValidateNIP44V2(event.Content); err != nil {
			return fmt.Errorf("agent-turn-metric content %w", err)
		}
	case KindPersona:
		if err := ValidateSharing(event.Tags); err != nil {
			return fmt.Errorf("persona event %w", err)
		}
		d, err := boundedDTag(event.Tags, "persona event")
		if err != nil {
			return err
		}
		for index, char := range []byte(d) {
			if index == 0 {
				if !isASCIILower(char) && !isASCIIDigit(char) {
					return errors.New("persona event d tag must start with a lowercase letter or digit")
				}
				continue
			}
			if !isASCIILower(char) && !isASCIIDigit(char) && char != '_' && char != '-' {
				return errors.New("persona event d tag must match [a-z0-9_-] after the first character")
			}
		}
	case KindSharedTeamCatalog:
		if err := ValidateSharing(event.Tags); err != nil {
			return fmt.Errorf("team-catalog event %w", err)
		}
		if _, err := boundedDTag(event.Tags, "team-catalog event"); err != nil {
			return err
		}
	}
	return nil
}

// ValidateNIP44V2 checks the public encoding properties Buzz enforces for
// durable encrypted agent data. It cannot validate the MAC or plaintext.
func ValidateNIP44V2(content string) error {
	if content == "" || len(content)%4 != 0 {
		return errors.New("must be standard base64 NIP-44 v2")
	}
	decoded, err := base64.StdEncoding.DecodeString(content)
	if err != nil {
		return errors.New("must be standard base64 NIP-44 v2")
	}
	if len(decoded) < 99 {
		return errors.New("is too short for NIP-44 v2")
	}
	if decoded[0] != 0x02 {
		return errors.New("is not NIP-44 v2")
	}
	return nil
}

// IsGlobalOnly reports Buzz agent kinds whose h tag is metadata, rather than
// a NIP-29 routing or authorization scope.
func IsGlobalOnly(kind nostr.Kind) bool {
	switch kind {
	case KindAgentProfile, KindAgentEngram, KindPersona, KindAgentTeam,
		KindManagedAgent, KindPrivateManagedAgent, KindSharedTeamCatalog,
		KindAgentTurnMetric:
		return true
	default:
		return false
	}
}

func exactTag(tags nostr.Tags, name string) (string, error) {
	var value string
	count := 0
	for _, tag := range tags {
		if len(tag) == 0 || tag[0] != name {
			continue
		}
		count++
		if len(tag) >= 2 {
			value = tag[1]
		}
	}
	if count != 1 {
		return "", fmt.Errorf("event must have exactly one %s tag", name)
	}
	if value == "" {
		return "", fmt.Errorf("%s tag must not be empty", name)
	}
	return value, nil
}

func boundedDTag(tags nostr.Tags, label string) (string, error) {
	d, err := exactTag(tags, "d")
	if err != nil {
		return "", fmt.Errorf("%s %w", label, err)
	}
	if utf8.RuneCountInString(d) > 64 {
		return "", fmt.Errorf("%s d tag is longer than 64 characters", label)
	}
	for _, char := range d {
		if unicode.IsControl(char) || unicode.IsSpace(char) {
			return "", fmt.Errorf("%s d tag must not contain control characters or whitespace", label)
		}
	}
	return d, nil
}

// ValidateSharing accepts no shared tag or exactly ["shared","true"].
func ValidateSharing(tags nostr.Tags) error {
	count := 0
	for _, tag := range tags {
		if len(tag) == 0 || tag[0] != "shared" {
			continue
		}
		if len(tag) != 2 || tag[1] != "true" {
			return errors.New("shared tag must be exactly [shared,true]")
		}
		count++
	}
	if count > 1 {
		return errors.New("must have at most one shared tag")
	}
	return nil
}

func hasTag(tags nostr.Tags, name string) bool {
	for _, tag := range tags {
		if len(tag) > 0 && tag[0] == name {
			return true
		}
	}
	return false
}

func isLowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, char := range []byte(value) {
		if !isASCIIDigit(char) && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func isASCIILower(char byte) bool { return char >= 'a' && char <= 'z' }
func isASCIIDigit(char byte) bool { return char >= '0' && char <= '9' }
