package auth

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"fiatjaf.com/nostr"
)

type ApplicationSigningPolicy struct {
	RelayURL string
}

func (policy ApplicationSigningPolicy) Authorize(_ context.Context, event nostr.Event, signCtx SignContext) error {
	if signCtx.AccountID == "" || signCtx.RequestedKind != event.Kind {
		return ErrSigningDenied
	}
	switch signCtx.Purpose {
	case SignPurposeRelayAuth:
		if event.Kind != nostr.KindClientAuthentication || event.Content != "" || strings.TrimSpace(signCtx.Source) == "" {
			return ErrSigningDenied
		}
		if event.Tags.FindWithValue("challenge", signCtx.Source) == nil || !sameURL(event.Tags.Find("relay"), policy.RelayURL) {
			return ErrSigningDenied
		}
		return nil
	case SignPurposeNote:
		if event.Kind == nostr.KindTextNote {
			return nil
		}
	case SignPurposeProfile:
		if event.Kind == nostr.KindProfileMetadata {
			return nil
		}
	case SignPurposeForum:
		if event.Kind == nostr.KindComment || event.Kind == nostr.KindIssue || event.Kind == nostr.KindReaction {
			return nil
		}
	case SignPurposeRepository:
		if event.Kind == nostr.KindRepositoryAnnouncement {
			return nil
		}
	case SignPurposeHTTPAuth:
		if event.Kind == nostr.KindHTTPAuth {
			return nil
		}
	}
	return ErrSigningDenied
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
