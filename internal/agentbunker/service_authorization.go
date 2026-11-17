package agentbunker

import (
	"encoding/json"
	"errors"
	"fiatjaf.com/nostr"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"zooid/buzzagents/protocol"
)

func (s *Service) authorizeSigning(a Agent, event nostr.Event) error {
	event.PubKey = nostr.MustPubKeyFromHex(a.Pubkey)
	now := s.Now()
	created := time.Unix(int64(event.CreatedAt), 0)
	maxAge := 2 * time.Minute
	if event.Kind == nostr.KindSeal {
		maxAge = 48 * time.Hour
	}
	if event.CreatedAt < 0 || (created.Before(now.Add(-maxAge)) && !retainedContextTimestamp(event)) || created.After(now.Add(2*time.Minute)) {
		return errors.New("event timestamp is stale")
	}
	if a.Status == StatusPending {
		if now.After(a.ExpiresAt) {
			return errors.New("managed agent draft expired")
		}
		if templateMatches(event, s.profileTemplate(a)) {
			return nil
		}
		return errors.New("only the pending profile may be signed")
	}
	if a.Status != StatusActive {
		return errors.New("agent is inactive")
	}
	if !slices.Contains([]nostr.Kind{nostr.KindSeal, 22242, 9, 7, 5, 1111, 1621, 1630, 1631, 1632, 1633, 3901, 3902, 3903, 3904, 3905, 3906, 3907, 3908, 20001, 20002, 23908, 24200, 30180, 27235, 30023, 30617, 30621, 33901, 40100, nostr.KindDMRelayList}, event.Kind) {
		return errors.New("event kind is not allowed")
	}
	if event.Kind == nostr.KindSeal && (len(event.Tags) != 0 || event.Content == "") {
		return errors.New("invalid NIP-17 seal")
	}
	if event.Kind == nostr.KindDMRelayList && (event.Content != "" || len(event.Tags) != 1 || len(event.Tags[0]) != 2 || event.Tags[0][0] != "relay" || event.Tags[0][1] != s.inboxRelayURL()) {
		return errors.New("invalid NIP-17 inbox relay list")
	}
	if event.Kind == 22242 {
		if err := s.authorizeRelayAuthentication(a, event); err != nil {
			return err
		}
	}
	if event.Kind == 27235 {
		if err := s.authorizeGitAuthentication(a, event); err != nil {
			return err
		}
	}
	if event.Kind == protocol.KindRuntimeSnapshot {
		route, err := protocol.ValidateRuntimeEnvelope(event)
		if err != nil {
			return err
		}
		if route.Owner.Hex() != a.OwnerPubkey {
			return errors.New("runtime owner does not match")
		}
	}
	h := exactTag(event.Tags, "h")
	if event.Kind == 9 || event.Kind == 20002 || event.Kind == 5 || event.Kind == 1111 || isRelayToolKind(event.Kind) || isJJContextKind(event.Kind) {
		if h == "" {
			return errors.New("group tag is required")
		}
	}
	if h != "" && s.IsMember != nil && !s.IsMember(h, nostr.MustPubKeyFromHex(a.Pubkey)) {
		return errors.New("group membership is required")
	}
	if event.Kind == 5 {
		if event.Content != "" || exactTag(event.Tags, "k") != "7" {
			return errors.New("only reaction deletion is allowed")
		}
		target := s.referencedEvent(event.Tags, "e")
		if target == nil || target.Kind != 7 || target.PubKey.Hex() != a.Pubkey || exactTag(target.Tags, "h") != h {
			return errors.New("reaction deletion target is invalid")
		}
		for _, tag := range event.Tags {
			if len(tag) != 2 || !slices.Contains([]string{"h", "e", "k"}, tag[0]) {
				return errors.New("invalid deletion tags")
			}
		}
	}
	if event.Kind == 1111 && referenceTag(event.Tags, "K") == "11" {
		root, parent := s.referencedEvent(event.Tags, "E"), s.referencedEvent(event.Tags, "e")
		if root == nil || parent == nil || root.Kind != 11 || (parent.Kind != 11 && parent.Kind != 1111) ||
			exactTag(root.Tags, "h") != h || exactTag(parent.Tags, "h") != h ||
			referenceTag(event.Tags, "K") != "11" || referenceTag(event.Tags, "P") != root.PubKey.Hex() ||
			referenceTag(event.Tags, "k") != strconv.Itoa(int(parent.Kind)) || referenceTag(event.Tags, "p") != parent.PubKey.Hex() ||
			(parent.Kind == 11 && parent.ID != root.ID) || (parent.Kind == 1111 && referenceTag(parent.Tags, "E") != root.ID.Hex()) {
			return errors.New("forum root or parent is invalid")
		}
		for _, tag := range event.Tags {
			if len(tag) < 2 || !slices.Contains([]string{"h", "E", "K", "P", "e", "k", "p"}, tag[0]) {
				return errors.New("invalid forum tags")
			}
			if (tag[0] == "E" || tag[0] == "e") && len(tag) > 3 {
				author := root.PubKey.Hex()
				if tag[0] == "e" {
					author = parent.PubKey.Hex()
				}
				if len(tag) != 4 || tag[3] != author {
					return errors.New("forum reference author is invalid")
				}
			}
		}
	}
	if event.Kind == 1111 && !slices.Contains([]string{"11", "1618"}, referenceTag(event.Tags, "K")) {
		return errors.New("comment root kind is not allowed")
	}
	if isRelayToolKind(event.Kind) || (event.Kind == 1111 && referenceTag(event.Tags, "K") == "1618") {
		if err := s.authorizeRelayToolSigning(event, h); err != nil {
			return err
		}
	}
	if isJJContextKind(event.Kind) {
		if err := s.authorizeJJContextSigning(event, h); err != nil {
			return err
		}
	}
	if event.Kind == 24200 && exactTag(event.Tags, "p") != a.OwnerPubkey {
		return errors.New("owner tag is required")
	}
	return nil
}

// Retained pages keep stable event IDs even when a turn or delivery takes time.
func retainedContextTimestamp(event nostr.Event) bool {
	if event.Kind != 3908 {
		return false
	}
	var page struct {
		Schema  string `json:"schema"`
		Version int    `json:"version"`
		Records []struct {
			CreatedAtMS *int64 `json:"created_at_ms"`
		} `json:"records"`
	}
	if json.Unmarshal([]byte(event.Content), &page) != nil || page.Schema != "jj-sync/agent-context-page" || page.Version != 2 || len(page.Records) != 1 {
		return false
	}
	stamp := page.Records[0].CreatedAtMS
	return stamp != nil && *stamp >= 0 && *stamp/1000 == int64(event.CreatedAt)
}

func isJJContextKind(kind nostr.Kind) bool {
	return (kind >= 3901 && kind <= 3908) || kind == 33901 || kind == 23908
}

func (s *Service) authorizeRelayAuthentication(a Agent, event nostr.Event) error {
	if event.Content != "" || exactTag(event.Tags, "relay") != s.inboxRelayURL() || exactTag(event.Tags, "challenge") == "" {
		return errors.New("invalid relay authentication template")
	}
	auth := ""
	for _, tag := range event.Tags {
		if len(tag) == 0 || !slices.Contains([]string{"relay", "challenge", "lapis-managed"}, tag[0]) {
			return errors.New("invalid relay authentication tags")
		}
		if tag[0] == "lapis-managed" {
			if auth != "" || len(tag) != 3 || tag[1] != a.OwnerPubkey || tag[2] != s.Config.RelayPubkey.Hex() {
				return errors.New("invalid managed authentication tag")
			}
			auth = tag[1]
		}
	}
	if auth == "" || len(event.Tags) != 3 {
		return errors.New("managed authentication tag is required")
	}
	return nil
}

func (s *Service) authorizeGitAuthentication(a Agent, event nostr.Event) error {
	if event.Content != "" || len(event.Tags) != 2 {
		return errors.New("invalid Git authentication template")
	}
	method := exactTag(event.Tags, "method")
	if method != http.MethodGet && method != http.MethodPost {
		return errors.New("invalid Git authentication method")
	}
	group, ok := s.gitRepositoryGroup(exactTag(event.Tags, "u"))
	if !ok {
		return errors.New("invalid Git repository URL")
	}
	if s.IsMember == nil || !s.IsMember(group, nostr.MustPubKeyFromHex(a.Pubkey)) {
		return errors.New("Git repository membership is required")
	}
	return nil
}

func (s *Service) gitRepositoryGroup(raw string) (string, bool) {
	base, baseErr := url.Parse(strings.TrimRight(s.Config.GitPublicURL, "/"))
	target, targetErr := url.Parse(strings.TrimSpace(raw))
	if baseErr != nil || targetErr != nil || base.Scheme == "" || base.Host == "" || target.User != nil || target.RawQuery != "" || target.Fragment != "" ||
		!strings.EqualFold(base.Scheme, target.Scheme) || !strings.EqualFold(base.Host, target.Host) {
		return "", false
	}
	prefix := strings.TrimRight(base.Path, "/") + "/git/"
	if !strings.HasPrefix(target.Path, prefix) {
		return "", false
	}
	parts := strings.Split(strings.TrimPrefix(target.Path, prefix), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", false
	}
	owner, err := nostr.PubKeyFromHex(parts[0])
	if err != nil {
		return "", false
	}
	repositoryID, err := url.PathUnescape(strings.TrimSuffix(parts[1], ".git"))
	if err != nil || repositoryID == "" || strings.Contains(repositoryID, "/") || s.CurrentEvent == nil {
		return "", false
	}
	repository := s.CurrentEvent(nostr.KindRepositoryAnnouncement, owner, repositoryID)
	if repository == nil {
		return "", false
	}
	group := exactTag(repository.Tags, "buzz-channel")
	if h := exactTag(repository.Tags, "h"); h != "" && group != "" && h != group {
		return "", false
	} else if group == "" {
		group = h
	}
	return group, group != ""
}
