package agentbunker

import (
	"encoding/json"
	"errors"
	"fiatjaf.com/nostr"
)

func (s *Service) directMessagePeers(a Agent) map[string]bool {
	allowed := map[string]bool{a.OwnerPubkey: true, a.Pubkey: true}
	instance := a.OwnerEvent
	if s.CurrentEvent != nil {
		instance = s.CurrentEvent(30177, nostr.MustPubKeyFromHex(a.OwnerPubkey), a.Pubkey)
	}
	if instance == nil || !validSigned(*instance) || instance.Kind != 30177 || instance.PubKey.Hex() != a.OwnerPubkey || exactTag(instance.Tags, "d") != a.Pubkey {
		return allowed
	}
	var content struct {
		Allowlist []string `json:"dm_allowlist"`
	}
	if json.Unmarshal([]byte(instance.Content), &content) != nil || len(content.Allowlist) > 128 {
		return allowed
	}
	for _, value := range content.Allowlist {
		key, err := nostr.PubKeyFromHex(value)
		if err != nil || key.Hex() != value {
			return allowed
		}
	}
	for _, value := range content.Allowlist {
		allowed[value] = true
	}
	return allowed
}

func (s *Service) authorizeDirectContent(a Agent, peer, plaintext string, encrypt bool) error {
	allowed := s.directMessagePeers(a)
	if !allowed[peer] {
		return errors.New("peer is outside the agent DM allowlist")
	}
	ownerPeer := peer == a.OwnerPubkey || peer == a.Pubkey
	var rumor nostr.Event
	err := json.Unmarshal([]byte(plaintext), &rumor)
	if err != nil || (rumor.Kind != 14 && rumor.Kind != 7 && rumor.Kind != 5) {
		if ownerPeer {
			return nil
		}
		return errors.New("only direct-message rumors may use approved peers")
	}
	if !ownerPeer && !rumor.CheckID() {
		return errors.New("invalid direct-message rumor")
	}
	if encrypt && rumor.PubKey.Hex() != a.Pubkey {
		return errors.New("direct-message author must be the agent")
	}
	if !encrypt && rumor.PubKey.Hex() != peer {
		return errors.New("direct-message author must match its seal")
	}
	if len(rumor.Content) > 65536 {
		return errors.New("direct-message content exceeds 64 KiB")
	}
	participants := map[string]bool{rumor.PubKey.Hex(): true}
	for _, tag := range rumor.Tags {
		if len(tag) > 0 && tag[0] == "p" {
			if len(tag) < 2 {
				return errors.New("invalid direct-message recipient")
			}
			participants[tag[1]] = true
		}
	}
	if len(participants) < 2 || len(participants) > 9 {
		return errors.New("invalid direct-message participant count")
	}
	for participant := range participants {
		if !allowed[participant] {
			return errors.New("direct-message participant is not approved")
		}
	}
	if !participants[a.Pubkey] || !participants[peer] {
		return errors.New("direct-message peer is absent from conversation")
	}
	return nil
}
