package projecttask

import (
	"fiatjaf.com/nostr"
	"fmt"
	"unicode/utf8"
)

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
	if err := validateCommitSingleton(event.Tags, "base-commit", false); err != nil {
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
	if err := validateCommitSingleton(event.Tags, "base-commit", false); err != nil {
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

func isBuzzWorkItemNote(event nostr.Event) bool {
	return hasRootEventTag(event.Tags) && len(tagsNamed(event.Tags, "a")) > 0
}

func isCIActivity(event nostr.Event) bool {
	return event.Kind == nostr.KindTextNote && tagHasValue(event.Tags, "t", "ci") && len(tagsNamed(event.Tags, "a")) > 0
}

func validateCIActivity(event nostr.Event) error {
	if !utf8.ValidString(event.Content) || len(event.Content) > maxPullRequestContent {
		return fmt.Errorf("CI activity content must not exceed %d UTF-8 bytes", maxPullRequestContent)
	}
	if _, err := validateRequiredRepositoryTag(event.Tags, "CI activity"); err != nil {
		return err
	}
	if err := validateCommitSingleton(event.Tags, "c", true); err != nil {
		return err
	}
	if err := validateSingleton(event.Tags, "status", 64, true); err != nil {
		return err
	}
	for _, singleton := range []struct {
		name string
		max  int
	}{{"external-run-id", 256}, {"external-url", 2048}} {
		if err := validateSingleton(event.Tags, singleton.name, singleton.max, false); err != nil {
			return err
		}
	}
	roots := tagsNamed(event.Tags, "e")
	if len(roots) > 1 {
		return fmt.Errorf("CI activity must not contain more than one PR root e tag")
	}
	if len(roots) == 1 {
		if len(roots[0]) < 2 {
			return fmt.Errorf("CI activity e tag must contain an event ID")
		}
		if _, err := nostr.IDFromHex(roots[0][1]); err != nil {
			return fmt.Errorf("CI activity e tag must contain a valid event ID")
		}
	}
	return nil
}

func isNIP22WorkItemComment(event nostr.Event) bool {
	return tagHasValue(event.Tags, "K", kindNumber(nostr.KindIssue)) ||
		tagHasValue(event.Tags, "K", kindNumber(KindPullRequest))
}
