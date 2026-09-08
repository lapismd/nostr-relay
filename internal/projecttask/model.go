// Package projecttask defines Buzz-compatible NIP-34 project and task wire
// contracts. It deliberately has no dependency on the relay runtime or store.
package projecttask

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"fiatjaf.com/nostr"
)

const (
	KindProject           nostr.Kind = 30621
	KindPullRequest       nostr.Kind = 1618
	KindPullRequestUpdate nostr.Kind = 1619

	TagBuzzChannel    = "buzz-channel"
	TagBuzzVisibility = "buzz-visibility"
	// TagProjectTaskAnchor marks a repository announcement as a logical
	// project-level task namespace. It deliberately has no Git backing.
	TagProjectTaskAnchor = "project-task-anchor"

	maxProjectCoordinates = 64
	maxPullRequestContent = 64 * 1024
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
	case KindPullRequest:
		return true, validatePullRequest(event)
	case KindPullRequestUpdate:
		return true, validatePullRequestUpdate(event)
	case nostr.KindStatusOpen, nostr.KindStatusApplied, nostr.KindStatusClosed, nostr.KindStatusDraft:
		return true, validateStatus(event)
	case nostr.KindComment:
		if isNIP22WorkItemComment(event) {
			return true, validateNIP22WorkItemComment(event)
		}
	case nostr.KindTextNote:
		if isBuzzWorkItemNote(event) {
			return true, validateBuzzWorkItemNote(event)
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
	if err := validateSingleton(event.Tags, TagBuzzChannel, 256, false); err != nil {
		return err
	}
	anchors := tagsNamed(event.Tags, TagProjectTaskAnchor)
	if len(anchors) > 1 {
		return fmt.Errorf("%s tag must be a singleton", TagProjectTaskAnchor)
	}
	if len(anchors) == 1 && (len(anchors[0]) != 2 || anchors[0][1] != "true") {
		return fmt.Errorf("%s tag must be exactly [\"%s\",\"true\"]", TagProjectTaskAnchor, TagProjectTaskAnchor)
	}
	if len(anchors) == 1 && (len(tagsNamed(event.Tags, "clone")) != 0 || len(tagsNamed(event.Tags, "web")) != 0) {
		return fmt.Errorf("%s must not contain clone or web tags", TagProjectTaskAnchor)
	}
	return nil
}

// IsProjectTaskAnchor reports whether a valid repository announcement is the
// explicit logical namespace used for project-level tasks.
func IsProjectTaskAnchor(event nostr.Event) bool {
	tag := event.Tags.Find(TagProjectTaskAnchor)
	return tag != nil && len(tag) == 2 && tag[1] == "true"
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

func validatePullRequest(event nostr.Event) error {
	coordinate, err := validateRequiredRepositoryTag(event.Tags, "pull request")
	if err != nil {
		return err
	}
	if !hasPubkeyValue(event.Tags, "p", coordinate.Owner) {
		return fmt.Errorf("pull request p tags must contain the repository owner")
	}
	if !utf8.ValidString(event.Content) || len(event.Content) > maxPullRequestContent {
		return fmt.Errorf("pull request content must not exceed 64 KiB of UTF-8")
	}
	if err := validateSingleton(event.Tags, "subject", 256, true); err != nil {
		return err
	}
	if err := validateCommitSingleton(event.Tags, "c", true); err != nil {
		return err
	}
	if err := validateCloneLocators(event.Tags); err != nil {
		return err
	}
	for _, singleton := range []struct {
		name string
		max  int
	}{
		{"h", 256},
		{"branch-name", 1024},
		{"target-branch", 1024},
	} {
		if err := validateSingleton(event.Tags, singleton.name, singleton.max, false); err != nil {
			return err
		}
	}
	if err := validateCommitTags(event.Tags, "r", "merge-base"); err != nil {
		return err
	}
	for _, tag := range tagsNamed(event.Tags, "e") {
		if len(tag) < 2 {
			return fmt.Errorf("pull request revision e tag must contain an event ID")
		}
		if _, err := nostr.IDFromHex(tag[1]); err != nil {
			return fmt.Errorf("pull request revision e tag must contain a valid event ID")
		}
	}
	return nil
}

func validatePullRequestUpdate(event nostr.Event) error {
	coordinate, err := validateRequiredRepositoryTag(event.Tags, "pull request update")
	if err != nil {
		return err
	}
	if !hasPubkeyValue(event.Tags, "p", coordinate.Owner) {
		return fmt.Errorf("pull request update p tags must contain the repository owner")
	}
	if !utf8.ValidString(event.Content) || len(event.Content) > maxPullRequestContent {
		return fmt.Errorf("pull request update content must not exceed 64 KiB of UTF-8")
	}
	if err := validateExactHexSingleton(event.Tags, "E", "pull request root event ID"); err != nil {
		return err
	}
	if err := validatePubkeySingleton(event.Tags, "P", "pull request root author"); err != nil {
		return err
	}
	if err := validateCommitSingleton(event.Tags, "c", true); err != nil {
		return err
	}
	if err := validateCloneLocators(event.Tags); err != nil {
		return err
	}
	return validateCommitTags(event.Tags, "r", "merge-base")
}

func validateStatus(event nostr.Event) error {
	if !hasRootEventTag(event.Tags) {
		return fmt.Errorf("status must contain a valid root e tag")
	}
	if err := validateOptionalRepositoryTag(event.Tags); err != nil {
		return err
	}
	if err := validateCommitTags(event.Tags, "r", "applied-as-commits"); err != nil {
		return err
	}
	mergeCommits := tagsNamed(event.Tags, "merge-commit")
	if len(mergeCommits) > 1 {
		return fmt.Errorf("merge-commit tag must be a singleton")
	}
	if len(mergeCommits) == 0 {
		return nil
	}
	if event.Kind != nostr.KindStatusApplied {
		return fmt.Errorf("merge-commit is only valid on kind 1631")
	}
	if len(mergeCommits[0]) < 2 || !validCommitID(mergeCommits[0][1]) {
		return fmt.Errorf("merge-commit must be a full SHA-1 or SHA-256 commit ID")
	}
	if !tagHasValue(event.Tags, "r", mergeCommits[0][1]) {
		return fmt.Errorf("merge-commit must have a matching r tag")
	}
	return nil
}

func validateBuzzWorkItemNote(event nostr.Event) error {
	if !hasRootEventTag(event.Tags) {
		return fmt.Errorf("task note must contain a valid root e tag")
	}
	if err := validateOptionalRepositoryTag(event.Tags); err != nil {
		return err
	}
	return validateCommitTags(event.Tags, "c")
}

func validateNIP22WorkItemComment(event nostr.Event) error {
	if !tagHasValue(event.Tags, "K", kindNumber(nostr.KindIssue)) &&
		!tagHasValue(event.Tags, "K", kindNumber(KindPullRequest)) {
		return fmt.Errorf("NIP-22 work-item comment K tag must reference kind 1621 or 1618")
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
	return validateCommitTags(event.Tags, "c")
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
	for _, tag := range tagsNamed(event.Tags, "e") {
		if len(tag) < 2 {
			return fmt.Errorf("deletion e tag must contain an event ID")
		}
		if _, err := nostr.IDFromHex(tag[1]); err != nil {
			return fmt.Errorf("deletion e tag must contain a valid event ID")
		}
	}
	return nil
}

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

func isBuzzWorkItemNote(event nostr.Event) bool {
	return hasRootEventTag(event.Tags) && len(tagsNamed(event.Tags, "a")) > 0
}

func isNIP22WorkItemComment(event nostr.Event) bool {
	return tagHasValue(event.Tags, "K", kindNumber(nostr.KindIssue)) ||
		tagHasValue(event.Tags, "K", kindNumber(KindPullRequest))
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
