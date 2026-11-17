package projecttask

import (
	"strings"

	"fiatjaf.com/nostr"
)

const maxAnnouncementProtectedRefs = 16

// AnnouncementProtectedRefTags returns the raw additive protected-ref values
// from a kind 30617 announcement. Invalid patterns stay in the list so the
// Git compile step can drop them fail-closed without rejecting the event.
func AnnouncementProtectedRefTags(event nostr.Event) []string {
	seen := make(map[string]struct{})
	values := make([]string, 0)
	for _, tag := range event.Tags {
		if len(tag) < 2 || tag[0] != TagProtectedRef {
			continue
		}
		value := strings.TrimSpace(tag[1])
		if value == "" {
			continue
		}
		if _, found := seen[value]; found {
			continue
		}
		seen[value] = struct{}{}
		values = append(values, value)
		if len(values) == maxAnnouncementProtectedRefs {
			break
		}
	}
	return values
}
