package agentbunker

import (
	"encoding/json"
	"errors"
	"fiatjaf.com/nostr"
)

func (s *Service) authorizeCanvasSigning(event nostr.Event, group string) error {
	if len(event.Tags) != 1 || exactTag(event.Tags, "h") != group || group == "" || len(event.Content) > 65536 {
		return errors.New("invalid canvas snapshot")
	}
	if s.CurrentEvent == nil || s.IsMember == nil || !s.IsMember(group, event.PubKey) {
		return errors.New("current canvas membership is required")
	}
	metadata := s.CurrentEvent(39000, s.Config.RelayPubkey, group)
	admins := s.CurrentEvent(39001, s.Config.RelayPubkey, group)
	if metadata == nil || admins == nil {
		return errors.New("current canvas management permission is required")
	}
	var content struct {
		Archived bool `json:"archived"`
	}
	if metadata.Content != "" && json.Unmarshal([]byte(metadata.Content), &content) != nil {
		return errors.New("invalid channel metadata")
	}
	for _, tag := range metadata.Tags {
		if len(tag) > 0 && tag[0] == "archived" {
			content.Archived = true
		}
	}
	if content.Archived {
		return errors.New("archived channel canvas is read-only")
	}
	for _, tag := range admins.Tags {
		if len(tag) >= 2 && tag[0] == "p" && tag[1] == event.PubKey.Hex() {
			return nil
		}
	}
	return errors.New("canvas management permission is required")
}
