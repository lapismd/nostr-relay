// Package projecttask defines Buzz-compatible NIP-34 project and task wire
// contracts. It deliberately has no dependency on the relay runtime or store.
package projecttask

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"fiatjaf.com/nostr"
)

const (
	KindProject nostr.Kind = 30621

	TagBuzzChannel    = "buzz-channel"
	TagBuzzVisibility = "buzz-visibility"

	maxProjectCoordinates = 64
)

// RepositoryCoordinate is the NIP-34 address referenced by an a tag.
type RepositoryCoordinate struct {
	Owner nostr.PubKey
	ID    string
}

func (coordinate RepositoryCoordinate) String() string {
	return fmt.Sprintf("%d:%s:%s", nostr.KindRepositoryAnnouncement, coordinate.Owner.Hex(), coordinate.ID)
}

// ParseRepositoryCoordinate accepts the coordinate value used by NIP-34.
// Relay hints are separate tag fields and are intentionally not parsed here.
func ParseRepositoryCoordinate(value string) (RepositoryCoordinate, error) {
	var coordinate RepositoryCoordinate
	parts := strings.SplitN(value, ":", 3)
	if len(parts) != 3 || parts[0] != kindNumber(nostr.KindRepositoryAnnouncement) {
		return coordinate, fmt.Errorf("repository coordinate must be 30617:<pubkey>:<id>")
	}
	owner, err := nostr.PubKeyFromHex(parts[1])
	if err != nil || owner.Hex() != parts[1] {
		return coordinate, fmt.Errorf("repository coordinate pubkey must be 64-character lowercase hex")
	}
	if parts[2] == "" {
		return coordinate, fmt.Errorf("repository coordinate id must not be empty")
	}
	return RepositoryCoordinate{Owner: owner, ID: parts[2]}, nil
}

// ValidateEvent validates project/task events owned by this extension. The
// bool is false for events outside the extension's contract.
func ValidateEvent(event nostr.Event) (bool, error) {
	switch event.Kind {
	case KindProject:
		return true, validateProject(event)
	case nostr.KindRepositoryAnnouncement:
		return true, validateRepository(event)
	case nostr.KindIssue:
		return true, validateIssue(event)
	case nostr.KindStatusOpen, nostr.KindStatusApplied, nostr.KindStatusClosed, nostr.KindStatusDraft:
		return true, validateStatus(event)
	case nostr.KindComment:
		if isNIP22TaskComment(event) {
			return true, validateNIP22TaskComment(event)
		}
	case nostr.KindTextNote:
		if isBuzzTaskNote(event) {
			return true, validateBuzzTaskNote(event)
		}
	case nostr.KindDeletion:
		if targetsProjectOrRepository(event) {
			return true, validateTombstone(event)
		}
	}
	return false, nil
}

func validateProject(event nostr.Event) error {
	if err := validateSingleton(event.Tags, "d", 1024, true); err != nil {
		return err
	}
	for _, singleton := range []struct {
		name string
		max  int
	}{
		{"name", 256},
		{"description", 2048},
		{TagBuzzChannel, 256},
		{TagBuzzVisibility, 256},
	} {
		if err := validateSingleton(event.Tags, singleton.name, singleton.max, false); err != nil {
			return err
		}
	}

	seen := make(map[string]struct{})
	count := 0
	for _, tag := range event.Tags {
		if len(tag) == 0 || tag[0] != "a" {
			continue
		}
		count++
		if count > maxProjectCoordinates {
			return fmt.Errorf("project must not contain more than %d repository coordinates", maxProjectCoordinates)
		}
		if len(tag) != 2 && len(tag) != 3 {
			return fmt.Errorf("project a tag must contain a coordinate and optional relay hint")
		}
		coordinate, err := ParseRepositoryCoordinate(tag[1])
		if err != nil {
			return err
		}
		key := coordinate.String()
		if _, exists := seen[key]; exists {
			return fmt.Errorf("project repository coordinates must be unique")
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateRepository(event nostr.Event) error {
	if err := validateSingleton(event.Tags, "d", 1024, true); err != nil {
		return err
	}
	return validateSingleton(event.Tags, TagBuzzChannel, 256, false)
}

func validateIssue(event nostr.Event) error {
	tags := tagsNamed(event.Tags, "a")
	if len(tags) != 1 || len(tags[0]) < 2 {
		return fmt.Errorf("issue must contain exactly one repository a tag")
	}
	if _, err := ParseRepositoryCoordinate(tags[0][1]); err != nil {
		return err
	}
	parents := tagsNamed(event.Tags, "parent")
	if len(parents) > 1 || (len(parents) == 1 && len(parents[0]) < 2) {
		return fmt.Errorf("subtask parent tag must be a singleton event ID")
	}
	if len(parents) == 1 {
		if _, err := nostr.IDFromHex(parents[0][1]); err != nil {
			return fmt.Errorf("subtask parent tag must contain a valid event ID")
		}
		if !tagHasValue(event.Tags, "e", parents[0][1]) {
			return fmt.Errorf("subtask parent and e tags must reference the same event")
		}
	}
	return nil
}

func validateStatus(event nostr.Event) error {
	if !hasRootEventTag(event.Tags) {
		return fmt.Errorf("status must contain a valid root e tag")
	}
	return validateOptionalRepositoryTag(event.Tags)
}

func validateBuzzTaskNote(event nostr.Event) error {
	if !hasRootEventTag(event.Tags) {
		return fmt.Errorf("task note must contain a valid root e tag")
	}
	return validateOptionalRepositoryTag(event.Tags)
}

func validateNIP22TaskComment(event nostr.Event) error {
	if !tagHasValue(event.Tags, "K", kindNumber(nostr.KindIssue)) {
		return fmt.Errorf("NIP-22 task comment K tag must reference kind 1621")
	}
	if !hasHexTag(event.Tags, "E") {
		return fmt.Errorf("NIP-22 task comment must contain a valid E tag")
	}
	if !hasPubkeyTag(event.Tags, "P") {
		return fmt.Errorf("NIP-22 task comment must contain a valid P tag")
	}
	if !hasHexTag(event.Tags, "e") || event.Tags.Find("k") == nil || !hasPubkeyTag(event.Tags, "p") {
		return fmt.Errorf("NIP-22 task comment must contain valid immediate-parent e, k, and p tags")
	}
	return nil
}

func validateTombstone(event nostr.Event) error {
	for _, tag := range event.Tags {
		if len(tag) < 2 || tag[0] != "a" {
			continue
		}
		kind, owner, _, ok := parseAddress(tag[1])
		if !ok || (kind != KindProject && kind != nostr.KindRepositoryAnnouncement) {
			continue
		}
		if owner != event.PubKey {
			return fmt.Errorf("project and repository tombstones must be signed by the address owner")
		}
	}
	return nil
}

func validateOptionalRepositoryTag(tags nostr.Tags) error {
	coordinates := tagsNamed(tags, "a")
	if len(coordinates) > 1 {
		return fmt.Errorf("task event must not contain more than one repository a tag")
	}
	if len(coordinates) == 1 {
		if len(coordinates[0]) < 2 {
			return fmt.Errorf("repository a tag must contain a coordinate")
		}
		_, err := ParseRepositoryCoordinate(coordinates[0][1])
		return err
	}
	return nil
}

func validateSingleton(tags nostr.Tags, name string, maxBytes int, required bool) error {
	matching := tagsNamed(tags, name)
	if len(matching) > 1 {
		return fmt.Errorf("%s tag must be a singleton", name)
	}
	if len(matching) == 0 {
		if required {
			return fmt.Errorf("%s tag is required", name)
		}
		return nil
	}
	if len(matching[0]) < 2 || (required && matching[0][1] == "") {
		return fmt.Errorf("%s tag must have a nonempty value", name)
	}
	if !utf8.ValidString(matching[0][1]) || len(matching[0][1]) > maxBytes {
		return fmt.Errorf("%s tag must not exceed %d UTF-8 bytes", name, maxBytes)
	}
	return nil
}

func isBuzzTaskNote(event nostr.Event) bool {
	return hasRootEventTag(event.Tags) && len(tagsNamed(event.Tags, "a")) > 0
}

func isNIP22TaskComment(event nostr.Event) bool {
	return tagHasValue(event.Tags, "K", kindNumber(nostr.KindIssue))
}

func targetsProjectOrRepository(event nostr.Event) bool {
	for _, tag := range event.Tags {
		if len(tag) < 2 || tag[0] != "a" {
			continue
		}
		kind, _, _, ok := parseAddress(tag[1])
		if ok && (kind == KindProject || kind == nostr.KindRepositoryAnnouncement) {
			return true
		}
	}
	return false
}

func hasRootEventTag(tags nostr.Tags) bool {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == "e" && (len(tag) < 4 || tag[3] == "root") {
			if _, err := nostr.IDFromHex(tag[1]); err == nil {
				return true
			}
		}
	}
	return false
}

func hasHexTag(tags nostr.Tags, name string) bool {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == name {
			if _, err := nostr.IDFromHex(tag[1]); err == nil {
				return true
			}
		}
	}
	return false
}

func hasPubkeyTag(tags nostr.Tags, name string) bool {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == name {
			if _, err := nostr.PubKeyFromHex(tag[1]); err == nil {
				return true
			}
		}
	}
	return false
}

func tagHasValue(tags nostr.Tags, name, value string) bool {
	return tags.FindWithValue(name, value) != nil
}

func tagsNamed(tags nostr.Tags, name string) []nostr.Tag {
	result := make([]nostr.Tag, 0, 1)
	for _, tag := range tags {
		if len(tag) > 0 && tag[0] == name {
			result = append(result, tag)
		}
	}
	return result
}

func parseAddress(value string) (nostr.Kind, nostr.PubKey, string, bool) {
	var owner nostr.PubKey
	parts := strings.SplitN(value, ":", 3)
	if len(parts) != 3 || parts[2] == "" {
		return 0, owner, "", false
	}
	kind, err := strconv.ParseUint(parts[0], 10, 16)
	if err != nil {
		return 0, owner, "", false
	}
	parsedOwner, err := nostr.PubKeyFromHex(parts[1])
	if err != nil || parsedOwner.Hex() != parts[1] {
		return 0, owner, "", false
	}
	return nostr.Kind(kind), parsedOwner, parts[2], true
}

func kindNumber(kind nostr.Kind) string {
	return strconv.FormatUint(uint64(kind), 10)
}
