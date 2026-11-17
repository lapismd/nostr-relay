package protocol

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"fiatjaf.com/nostr"
)

const MaxPersonaContentBytes = 65_535

// Persona is the owner-controlled presentation and configuration subset that
// the relay validates. Unknown fields remain forward-compatible and opaque.
type Persona struct {
	DisplayName  string
	AvatarURL    string
	SystemPrompt string
}

// ManagedAgent is the public instance/link subset used by relay authorization.
// Empty content remains valid for legacy standalone records.
type ManagedAgent struct {
	PersonaID   string
	Name        string
	NameMode    string
	RespondTo   string
	Parallelism int
}

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
		if err := ValidatePersonaSlug(d); err != nil {
			return fmt.Errorf("persona event %w", err)
		}
		if _, err := ParsePersona(event.Content); err != nil {
			return fmt.Errorf("persona event content %w", err)
		}
	case KindManagedAgent:
		d, err := boundedDTag(event.Tags, "managed-agent event")
		if err != nil {
			return err
		}
		if !isLowerHex(d, 64) {
			return errors.New("managed-agent event d tag must be an agent public key")
		}
		if _, err := ParseManagedAgent(event.Content); err != nil {
			return fmt.Errorf("managed-agent event content %w", err)
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

// ValidatePersonaSlug enforces the NIP-AP address grammar.
func ValidatePersonaSlug(value string) error {
	if value == "" || len(value) > 64 {
		return errors.New("d tag must contain between 1 and 64 ASCII characters")
	}
	for index, char := range []byte(value) {
		if index == 0 {
			if !isASCIILower(char) && !isASCIIDigit(char) {
				return errors.New("d tag must start with a lowercase letter or digit")
			}
			continue
		}
		if !isASCIILower(char) && !isASCIIDigit(char) && char != '_' && char != '-' {
			return errors.New("d tag must match [a-z0-9_-] after the first character")
		}
	}
	return nil
}

// ParsePersona validates the plaintext NIP-AP content that affects management
// presentation. It does not execute prompt or runtime fields.
func ParsePersona(content string) (Persona, error) {
	var persona Persona
	if len([]byte(content)) > MaxPersonaContentBytes {
		return persona, fmt.Errorf("is larger than %d bytes", MaxPersonaContentBytes)
	}
	fields, err := objectFields(content)
	if err != nil {
		return persona, err
	}
	for key := range fields {
		normalized := strings.NewReplacer("_", "", "-", "", ".", "").Replace(strings.ToLower(key))
		switch normalized {
		case "credential", "credentials", "privatekey", "secretkey", "secrets",
			"environmentsecrets", "envsecrets", "envvars", "apikey", "apikeys",
			"token", "tokens":
			return persona, fmt.Errorf("must not contain plaintext secret field %q", key)
		}
	}
	if err := requiredJSONText(fields, "display_name", &persona.DisplayName); err != nil {
		return persona, err
	}
	if strings.TrimSpace(persona.DisplayName) == "" {
		return persona, errors.New("display_name must not be empty")
	}
	if err := optionalJSONText(fields, "avatar_url", &persona.AvatarURL); err != nil {
		return persona, err
	}
	if err := optionalJSONText(fields, "system_prompt", &persona.SystemPrompt); err != nil {
		return persona, err
	}
	return persona, nil
}

// ParseManagedAgent validates the optional persona link and Lapis name-mode
// extension while retaining compatibility with Buzz's legacy empty object.
func ParseManagedAgent(content string) (ManagedAgent, error) {
	var instance ManagedAgent
	fields, err := objectFields(content)
	if err != nil {
		return instance, err
	}
	for key, target := range map[string]*string{
		"persona_id": &instance.PersonaID,
		"name":       &instance.Name,
		"name_mode":  &instance.NameMode,
		"respond_to": &instance.RespondTo,
	} {
		if err := optionalJSONText(fields, key, target); err != nil {
			return instance, err
		}
	}
	if instance.PersonaID != "" {
		if err := ValidatePersonaSlug(instance.PersonaID); err != nil {
			return instance, fmt.Errorf("persona_id %w", err)
		}
	}
	if instance.NameMode != "" && instance.NameMode != "inherited" && instance.NameMode != "instance" {
		return instance, errors.New("name_mode must be inherited or instance")
	}
	if raw, ok := fields["parallelism"]; ok {
		if err := json.Unmarshal(raw, &instance.Parallelism); err != nil || instance.Parallelism < 1 {
			return instance, errors.New("parallelism must be a positive integer")
		}
	}
	return instance, nil
}

func objectFields(content string) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(content), &fields); err != nil || fields == nil {
		return nil, errors.New("must be a JSON object")
	}
	return fields, nil
}

func requiredJSONText(fields map[string]json.RawMessage, key string, target *string) error {
	raw, ok := fields[key]
	if !ok {
		return fmt.Errorf("must contain %s", key)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("%s must be a string", key)
	}
	return nil
}

func optionalJSONText(fields map[string]json.RawMessage, key string, target *string) error {
	raw, ok := fields[key]
	if !ok {
		return nil
	}
	if string(raw) == "null" {
		return nil
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("%s must be a string", key)
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
		KindAgentTurnMetric, KindRuntimeSnapshot, KindRuntimeRequest:
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
