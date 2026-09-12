package auth

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"fiatjaf.com/nostr"
)

// ApplicationSigningPolicy is the narrow policy seam between Zooid's Lapis
// application contract and the managed/NIP-46 signer implementations.
type ApplicationSigningPolicy struct {
	PublicURL string
	RelayURL  string
}

var communitySigningKinds = map[nostr.Kind]struct{}{
	0: {}, 1: {}, 3: {}, 5: {}, 7: {}, 9: {}, 11: {}, 13: {}, 1111: {},
	1618: {}, 1619: {}, 1621: {}, 1630: {}, 1631: {}, 1632: {}, 1633: {},
	9000: {}, 9001: {}, 9002: {}, 9005: {}, 9007: {}, 9008: {}, 9009: {},
	9021: {}, 9022: {}, 22242: {}, 27235: {}, 30030: {}, 30078: {},
	30175: {}, 30177: {}, 30315: {}, 30617: {}, 30621: {}, 40003: {},
}

var groupTaggedKinds = map[nostr.Kind]struct{}{
	9: {}, 11: {}, 9000: {}, 9001: {}, 9002: {}, 9005: {}, 9007: {},
	9008: {}, 9009: {}, 9021: {}, 9022: {},
}

func CommunitySignPurpose(kind nostr.Kind) (SignPurpose, bool) {
	if _, ok := communitySigningKinds[kind]; !ok {
		return "", false
	}
	switch kind {
	case nostr.KindProfileMetadata:
		return SignPurposeProfile, true
	case nostr.KindTextNote:
		return SignPurposeNote, true
	case nostr.KindReaction, nostr.KindComment, nostr.KindIssue:
		return SignPurposeForum, true
	case nostr.KindRepositoryAnnouncement:
		return SignPurposeRepository, true
	case nostr.KindClientAuthentication:
		return SignPurposeRelayAuth, true
	case nostr.KindHTTPAuth:
		return SignPurposeHTTPAuth, true
	default:
		return SignPurposeCommunity, true
	}
}

func (policy ApplicationSigningPolicy) Authorize(_ context.Context, event nostr.Event, signCtx SignContext) error {
	if signCtx.AccountID == "" || signCtx.RequestedKind != event.Kind {
		return ErrSigningDenied
	}
	purpose, allowed := CommunitySignPurpose(event.Kind)
	if !allowed || purpose != signCtx.Purpose {
		return ErrSigningDenied
	}
	if _, groupTagged := groupTaggedKinds[event.Kind]; groupTagged && !hasNonEmptyTag(event.Tags, "h") {
		return ErrSigningDenied
	}
	if err := validateRepositoryTags(event); err != nil {
		return ErrSigningDenied
	}
	switch purpose {
	case SignPurposeRelayAuth:
		if event.Content != "" || strings.TrimSpace(signCtx.Source) == "" ||
			event.Tags.FindWithValue("challenge", signCtx.Source) == nil || !sameURL(event.Tags.Find("relay"), policy.RelayURL) {
			return ErrSigningDenied
		}
	case SignPurposeHTTPAuth:
		if event.Content != "" || !hasNonEmptyTag(event.Tags, "method") || !sameOrigin(event.Tags.Find("u"), policy.PublicURL) {
			return ErrSigningDenied
		}
	}
	return nil
}

func validateRepositoryTags(event nostr.Event) error {
	switch event.Kind {
	case 1618, 1619, 1621:
		if !hasNonEmptyTag(event.Tags, "a") {
			return errors.New("repository coordinate is required")
		}
	case 1630, 1631, 1632, 1633:
		if !hasNonEmptyTag(event.Tags, "e") || !hasNonEmptyTag(event.Tags, "a") {
			return errors.New("repository status anchors are required")
		}
	case 30617:
		if !hasNonEmptyTag(event.Tags, "d") {
			return errors.New("repository identifier is required")
		}
	case 30621:
		if !hasNonEmptyTag(event.Tags, "d") || !hasNonEmptyTag(event.Tags, "a") {
			return errors.New("project identifier and repository are required")
		}
	}
	return nil
}

func hasNonEmptyTag(tags nostr.Tags, name string) bool {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == name && strings.TrimSpace(tag[1]) != "" {
			return true
		}
	}
	return false
}

func sameURL(tag nostr.Tag, expected string) bool {
	if len(tag) < 2 {
		return false
	}
	foundURL, err := url.Parse(strings.ToLower(strings.TrimRight(tag[1], "/")))
	if err != nil {
		return false
	}
	expectedURL, err := url.Parse(strings.ToLower(strings.TrimRight(expected, "/")))
	return err == nil && foundURL.Scheme == expectedURL.Scheme && foundURL.Host == expectedURL.Host && foundURL.Path == expectedURL.Path
}

func sameOrigin(tag nostr.Tag, expected string) bool {
	if len(tag) < 2 {
		return false
	}
	foundURL, err := url.Parse(tag[1])
	if err != nil || foundURL.User != nil || foundURL.Scheme == "" || foundURL.Host == "" {
		return false
	}
	expectedURL, err := url.Parse(expected)
	return err == nil && strings.EqualFold(foundURL.Scheme, expectedURL.Scheme) && strings.EqualFold(foundURL.Host, expectedURL.Host)
}

func relayURLForPublicURL(publicURL string) (string, error) {
	parsed, err := url.Parse(publicURL)
	if err != nil {
		return "", err
	}
	switch parsed.Scheme {
	case "https":
		parsed.Scheme = "wss"
	case "http":
		parsed.Scheme = "ws"
	default:
		return "", errors.New("unsupported public URL scheme")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return parsed.String(), nil
}
