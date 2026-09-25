package projecttask

import (
	"encoding/hex"
	"fiatjaf.com/nostr"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

func validateRequiredRepositoryTag(tags nostr.Tags, label string) (RepositoryCoordinate, error) {
	coordinates := tagsNamed(tags, "a")
	if len(coordinates) != 1 || len(coordinates[0]) < 2 {
		return RepositoryCoordinate{}, fmt.Errorf("%s must contain exactly one repository a tag", label)
	}
	return ParseRepositoryCoordinate(coordinates[0][1])
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

func validateCloneLocators(tags nostr.Tags) error {
	count := 0
	for _, tag := range tagsNamed(tags, "clone") {
		for _, locator := range tag[1:] {
			if locator == "" || !utf8.ValidString(locator) {
				return fmt.Errorf("clone locators must be nonempty UTF-8 values")
			}
			count++
		}
	}
	if count == 0 {
		return fmt.Errorf("at least one nonempty clone locator is required")
	}
	return nil
}

func cloneLocators(tags nostr.Tags) []string {
	locators := make([]string, 0)
	for _, tag := range tagsNamed(tags, "clone") {
		for _, locator := range tag[1:] {
			if locator != "" {
				locators = append(locators, locator)
			}
		}
	}
	return locators
}

func validateCommitSingleton(tags nostr.Tags, name string, required bool) error {
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
	if len(matching[0]) < 2 || !validCommitID(matching[0][1]) {
		return fmt.Errorf("%s must be a full SHA-1 or SHA-256 commit ID", name)
	}
	return nil
}

func validateCommitTags(tags nostr.Tags, names ...string) error {
	for _, name := range names {
		for _, tag := range tagsNamed(tags, name) {
			if len(tag) < 2 {
				return fmt.Errorf("%s must contain a full SHA-1 or SHA-256 commit ID", name)
			}
			for _, value := range tag[1:] {
				if !validCommitID(value) {
					return fmt.Errorf("%s must contain only full SHA-1 or SHA-256 commit IDs", name)
				}
			}
		}
	}
	return nil
}

func validCommitID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validateExactHexSingleton(tags nostr.Tags, name, label string) error {
	matching := tagsNamed(tags, name)
	if len(matching) != 1 || len(matching[0]) < 2 {
		return fmt.Errorf("%s must contain exactly one %s", name, label)
	}
	if _, err := nostr.IDFromHex(matching[0][1]); err != nil {
		return fmt.Errorf("%s must contain a valid %s", name, label)
	}
	return nil
}

func validatePubkeySingleton(tags nostr.Tags, name, label string) error {
	matching := tagsNamed(tags, name)
	if len(matching) != 1 || len(matching[0]) < 2 {
		return fmt.Errorf("%s must contain exactly one %s", name, label)
	}
	if _, err := nostr.PubKeyFromHex(matching[0][1]); err != nil {
		return fmt.Errorf("%s must contain a valid %s", name, label)
	}
	return nil
}

func hasPubkeyValue(tags nostr.Tags, name string, pubkey nostr.PubKey) bool {
	for _, tag := range tagsNamed(tags, name) {
		if len(tag) >= 2 && tag[1] == pubkey.Hex() {
			return true
		}
	}
	return false
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
