// Package gitforge contains storage-independent contracts for the optional
// Git forge boundary. Runtime persistence and NIP-29 policy remain in Zooid.
package gitforge

import (
	"fmt"
	"net/url"
	"strings"

	"fiatjaf.com/nostr"
	"zooid/internal/projecttask"
)

// RepositoryClass describes the backing selected by a repository
// announcement. The signed event remains authoritative and unchanged.
type RepositoryClass string

const (
	RepositoryHosted   RepositoryClass = "hosted"
	RepositoryExternal RepositoryClass = "external"
	RepositoryAnchor   RepositoryClass = "anchor"
)

// Repository is the normalized Git-forge view of a kind-30617 event.
type Repository struct {
	Coordinate   projecttask.RepositoryCoordinate
	Announcement nostr.Event
	Channel      string
	Class        RepositoryClass
	CloneURL     string
}

// ClassifyRepository deterministically classifies a validated kind-30617
// announcement. Unmarked announcements without clone locators use Buzz's
// relay-hosted default; an explicit local clone locator does the same.
func ClassifyRepository(event nostr.Event, publicURL string) (Repository, error) {
	if event.Kind != nostr.KindRepositoryAnnouncement {
		return Repository{}, fmt.Errorf("Git repository classification requires kind 30617")
	}
	if recognized, err := projecttask.ValidateEvent(event); !recognized || err != nil {
		if err == nil {
			err = fmt.Errorf("event is not a repository announcement")
		}
		return Repository{}, err
	}

	coordinate := projecttask.RepositoryCoordinate{Owner: event.PubKey, ID: event.Tags.GetD()}
	canonical, err := CanonicalCloneURL(publicURL, coordinate)
	if err != nil {
		return Repository{}, err
	}
	repository := Repository{
		Coordinate:   coordinate,
		Announcement: event,
		Channel:      tagValue(event.Tags, projecttask.TagBuzzChannel),
		CloneURL:     canonical,
	}
	if projecttask.IsProjectTaskAnchor(event) {
		repository.Class = RepositoryAnchor
		return repository, nil
	}

	clones := cloneLocators(event.Tags)
	if len(clones) == 0 {
		repository.Class = RepositoryHosted
	} else {
		repository.Class = RepositoryExternal
		repository.CloneURL = clones[0]
		for _, clone := range clones {
			if equivalentCloneURL(clone, canonical) {
				repository.Class = RepositoryHosted
				repository.CloneURL = canonical
				break
			}
		}
	}
	if repository.Class == RepositoryHosted {
		if repository.Channel == "" {
			return Repository{}, fmt.Errorf("hosted repository requires a nonempty buzz-channel")
		}
		if err := ValidateHostedRepositoryID(coordinate.ID); err != nil {
			return Repository{}, err
		}
	}
	return repository, nil
}

// ValidateHostedRepositoryID mirrors WalGit's repository-name contract without
// narrowing external NIP-34 repository identifiers.
func ValidateHostedRepositoryID(id string) error {
	if len(id) < 1 || len(id) > 100 {
		return fmt.Errorf("hosted repository id must be 1..=100 characters")
	}
	if id == ".." || strings.HasPrefix(id, ".") {
		return fmt.Errorf("hosted repository id must not be '..' or start with '.'")
	}
	for _, character := range []byte(id) {
		if !(character >= 'a' && character <= 'z') &&
			!(character >= 'A' && character <= 'Z') &&
			!(character >= '0' && character <= '9') &&
			character != '.' && character != '_' && character != '-' {
			return fmt.Errorf("hosted repository id must use ASCII [A-Za-z0-9._-]")
		}
	}
	return nil
}

// CanonicalCloneURL returns the public Buzz-compatible Git URL. publicURL may
// contain the relay's deployment path prefix but not query or fragment data.
func CanonicalCloneURL(publicURL string, coordinate projecttask.RepositoryCoordinate) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(publicURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("git public_url must be an absolute http(s) URL")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return "", fmt.Errorf("git public_url must not contain user info, query, or fragment")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/git/" + coordinate.Owner.Hex() + "/" + coordinate.ID
	parsed.RawPath = ""
	return parsed.String(), nil
}

func cloneLocators(tags nostr.Tags) []string {
	locators := make([]string, 0, 2)
	for _, tag := range tags {
		if len(tag) < 2 || tag[0] != "clone" {
			continue
		}
		locators = append(locators, tag[1:]...)
	}
	return locators
}

func equivalentCloneURL(left, right string) bool {
	normalize := func(raw string) string {
		parsed, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return ""
		}
		parsed.Scheme = strings.ToLower(parsed.Scheme)
		parsed.Host = strings.ToLower(parsed.Host)
		parsed.Path = strings.TrimSuffix(strings.TrimRight(parsed.Path, "/"), ".git")
		parsed.RawPath = ""
		return parsed.String()
	}
	return normalize(left) != "" && normalize(left) == normalize(right)
}

func tagValue(tags nostr.Tags, name string) string {
	if tag := tags.Find(name); tag != nil && len(tag) >= 2 {
		return tag[1]
	}
	return ""
}
