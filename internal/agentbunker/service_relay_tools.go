package agentbunker

import (
	"errors"
	"fiatjaf.com/nostr"
	"slices"
	"zooid/internal/projecttask"
)

func (s *Service) authorizeJJContextSigning(event nostr.Event, group string) error {
	if event.Content == "" || !validJSONObject(event.Content) || exactTag(event.Tags, "r") == "" || exactTag(event.Tags, "a") == "" ||
		!s.repositoryMatchesGroup(exactTag(event.Tags, "a"), group) {
		return errors.New("invalid JJ context template")
	}
	allowed := map[string]bool{
		"t": true, "r": true, "o": true, "x": true, "O": true, "v": true,
		"c": true, "i": true, "d": true, "sequence": true, "a": true, "h": true,
	}
	for _, tag := range event.Tags {
		if len(tag) != 2 || !allowed[tag[0]] {
			return errors.New("invalid JJ context tags")
		}
	}
	if event.Kind == 33901 && exactTag(event.Tags, "d") == "" {
		return errors.New("JJ heads identifier is required")
	}
	return nil
}

func isRelayToolKind(kind nostr.Kind) bool {
	return slices.Contains([]nostr.Kind{1621, 1630, 1631, 1632, 1633, 30023, 30617, 30621, 40100}, kind)
}

func (s *Service) authorizeRelayToolSigning(event nostr.Event, group string) error {
	if event.Kind == 40100 {
		return s.authorizeCanvasSigning(event, group)
	}
	if group == "" {
		return errors.New("relay tool group is required")
	}
	if event.Kind == 30023 {
		if len(event.Tags) != 3 || exactTag(event.Tags, "d") == "" || exactTag(event.Tags, "title") == "" || event.Content == "" {
			return errors.New("invalid long-form note template")
		}
		return nil
	}

	recognized, err := projecttask.ValidateEvent(event)
	if !recognized || err != nil {
		return errors.New("invalid relay tool event")
	}
	switch event.Kind {
	case 30617:
		if len(event.Tags) != 4 || exactTag(event.Tags, "d") == "" || exactTag(event.Tags, "name") == "" || exactTag(event.Tags, "buzz-channel") != group {
			return errors.New("invalid repository template")
		}
	case 30621:
		if exactTag(event.Tags, "d") == "" || exactTag(event.Tags, "name") == "" || exactTag(event.Tags, "buzz-channel") != group || countTags(event.Tags, "a") == 0 || len(event.Tags) != 4+countTags(event.Tags, "a") {
			return errors.New("invalid project template")
		}
		for _, tag := range event.Tags {
			if len(tag) > 0 && tag[0] == "a" && !s.repositoryMatchesGroup(tag[1], group) {
				return errors.New("project repository is outside the group")
			}
		}
	case 1621:
		if len(event.Tags) != 3 || exactTag(event.Tags, "subject") == "" || event.Content == "" || !s.repositoryMatchesGroup(exactTag(event.Tags, "a"), group) {
			return errors.New("invalid issue template")
		}
	case 1630, 1631, 1632, 1633:
		if len(event.Tags) != 3 || !s.repositoryMatchesGroup(exactTag(event.Tags, "a"), group) {
			return errors.New("invalid issue status template")
		}
		root := s.referencedEvent(event.Tags, "e")
		if root == nil || root.Kind != nostr.KindIssue || exactTag(root.Tags, "a") != exactTag(event.Tags, "a") {
			return errors.New("issue status target is invalid")
		}
	case 1111:
		if len(event.Tags) != 8 || exactTag(event.Tags, "a") == "" || referenceTag(event.Tags, "K") != "1618" || referenceTag(event.Tags, "k") != "1618" || referenceTag(event.Tags, "E") != referenceTag(event.Tags, "e") || !s.repositoryMatchesGroup(exactTag(event.Tags, "a"), group) {
			return errors.New("invalid pull-request comment template")
		}
		root := s.referencedEvent(event.Tags, "E")
		if root == nil || root.Kind != projecttask.KindPullRequest || exactTag(root.Tags, "a") != exactTag(event.Tags, "a") || referenceTag(event.Tags, "P") != root.PubKey.Hex() || referenceTag(event.Tags, "p") != root.PubKey.Hex() {
			return errors.New("pull-request comment target is invalid")
		}
	default:
		return errors.New("event kind is not a relay tool write")
	}
	return nil
}

func (s *Service) repositoryMatchesGroup(value, group string) bool {
	coordinate, err := projecttask.ParseRepositoryCoordinate(value)
	if err != nil || s.CurrentEvent == nil {
		return false
	}
	repository := s.CurrentEvent(nostr.KindRepositoryAnnouncement, coordinate.Owner, coordinate.ID)
	if repository == nil || repository.Kind != nostr.KindRepositoryAnnouncement || repository.PubKey != coordinate.Owner || repository.Tags.GetD() != coordinate.ID {
		return false
	}
	repositoryGroup := exactTag(repository.Tags, "buzz-channel")
	if h := exactTag(repository.Tags, "h"); h != "" && repositoryGroup != "" && h != repositoryGroup {
		return false
	} else if repositoryGroup == "" {
		repositoryGroup = h
	}
	return repositoryGroup == group
}

func referenceTag(tags nostr.Tags, name string) string {
	value := ""
	count := 0
	for _, tag := range tags {
		if len(tag) > 0 && tag[0] == name {
			if len(tag) < 2 || len(tag) > 4 {
				return ""
			}
			value = tag[1]
			count++
		}
	}
	if count != 1 {
		return ""
	}
	return value
}

func (s *Service) referencedEvent(tags nostr.Tags, name string) *nostr.Event {
	id, err := nostr.IDFromHex(referenceTag(tags, name))
	if err != nil || s.EventByID == nil {
		return nil
	}
	return s.EventByID(id)
}
