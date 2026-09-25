package projecttask

import (
	"fiatjaf.com/nostr"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	KindProject           nostr.Kind = 30621
	KindPullRequest       nostr.Kind = 1618
	KindPullRequestUpdate nostr.Kind = 1619

	TagBuzzChannel    = "buzz-channel"
	TagBuzzVisibility = "buzz-visibility"
	// TagProtectedRef adds an extra compiled branch pattern beside the live
	// default. Invalid values are ignored at compile time and cannot loosen
	// default-branch protection.
	TagProtectedRef = "protected-ref"
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
		if isCIActivity(event) {
			return true, validateCIActivity(event)
		}
		if isBuzzWorkItemNote(event) {
			return true, validateBuzzWorkItemNote(event)
		}
	case nostr.KindReaction:
		if hasRepositoryCoordinate(event.Tags) {
			return true, validateWorkItemReaction(event)
		}
	case nostr.KindDeletion:
		if targetsProjectOrRepository(event) {
			return true, validateTombstone(event)
		}
	}
	return false, nil
}

func validateWorkItemReaction(event nostr.Event) error {
	if !utf8.ValidString(event.Content) || event.Content == "" || len(event.Content) > 64 {
		return fmt.Errorf("work-item reaction content must be 1 to 64 UTF-8 bytes")
	}
	if _, err := validateRequiredRepositoryTag(event.Tags, "work-item reaction"); err != nil {
		return err
	}
	if err := validateExactHexSingleton(event.Tags, "e", "reaction target event ID"); err != nil {
		return err
	}
	if err := validatePubkeySingleton(event.Tags, "p", "reaction target author"); err != nil {
		return err
	}
	kinds := tagsNamed(event.Tags, "k")
	if len(kinds) != 1 || len(kinds[0]) < 2 {
		return fmt.Errorf("k must contain exactly one reaction target kind")
	}
	if _, err := strconv.ParseUint(kinds[0][1], 10, 16); err != nil {
		return fmt.Errorf("k must contain a valid reaction target kind")
	}
	return nil
}
