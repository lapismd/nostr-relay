package agentbunker

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fiatjaf.com/nostr"
	"net/url"
	"strings"
	"zooid/internal/keywrap"
)

func (s *Service) ownedAgent(ctx context.Context, pubkey, owner string) (Agent, error) {
	if !nostr.IsValid32ByteHex(pubkey) {
		return Agent{}, ErrNotFound
	}
	a, err := s.Store.Agent(ctx, pubkey)
	if err != nil || a.OwnerPubkey != owner {
		return Agent{}, ErrNotFound
	}
	return a, nil
}

func (s *Service) agentAAD(agent, owner string) []byte {
	return []byte("lapis-agent-bunker-v1\x00" + agent + "\x00" + owner + "\x00" + s.Config.RelayPubkey.Hex() + "\x00" + s.Wrapper.Version())
}

func (s *Service) invitationAAD(agent, client string) []byte {
	return []byte("lapis-agent-invitation-v1\x00" + agent + "\x00" + client + "\x00" + s.Config.RelayPubkey.Hex() + "\x00" + s.Wrapper.Version())
}

func (s *Service) newInvitation(agent, client string) (string, string, keywrap.Envelope, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", keywrap.Envelope{}, err
	}
	defer clear(raw)
	secret := base64.RawURLEncoding.EncodeToString(raw)
	digest := sha256.Sum256([]byte(secret))
	envelope, err := keywrap.Encrypt([]byte(secret), s.invitationAAD(agent, client), s.Wrapper)
	return secret, hex.EncodeToString(digest[:]), envelope, err
}

func (s *Service) bunkerURL(agent, secret string) string {
	u, _ := url.Parse(strings.TrimRight(s.Config.PublicURL, "/") + "/nip46")
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	result := "bunker://" + agent + "?relay=" + url.QueryEscape(u.String())
	if secret != "" {
		result += "&secret=" + url.QueryEscape(secret)
	}
	return result
}

func (s *Service) restorePendingInvitation(ctx context.Context, clientPubkey string, encoded []byte) ([]byte, error) {
	var response createResponse
	if err := json.Unmarshal(encoded, &response); err != nil {
		return nil, err
	}
	client, err := s.Store.Client(ctx, response.Agent.Pubkey, clientPubkey)
	if err != nil || client.Status != ClientPending {
		return encoded, nil
	}
	invitation, err := client.Invitation.Decrypt(s.invitationAAD(client.AgentPubkey, client.ClientPubkey), s.Wrapper)
	if err != nil {
		return nil, err
	}
	defer clear(invitation)
	response.BunkerURL = s.bunkerURL(response.Agent.Pubkey, string(invitation))
	return json.Marshal(response)
}

func (s *Service) restoreReplacementInvitation(ctx context.Context, agentPubkey, clientPubkey string, encoded []byte) ([]byte, error) {
	var response replacementResponse
	if err := json.Unmarshal(encoded, &response); err != nil {
		return nil, err
	}
	client, err := s.Store.Client(ctx, agentPubkey, clientPubkey)
	if err != nil || client.Status != ClientPending {
		return encoded, nil
	}
	invitation, err := client.Invitation.Decrypt(s.invitationAAD(client.AgentPubkey, client.ClientPubkey), s.Wrapper)
	if err != nil {
		return nil, err
	}
	defer clear(invitation)
	response.BunkerURL = s.bunkerURL(agentPubkey, string(invitation))
	return json.Marshal(response)
}

func (s *Service) profileTemplate(a Agent) eventTemplate {
	return eventTemplate{Kind: 0, CreatedAt: a.ProfileCreatedAt, Tags: [][]string{{"lapis-managed", a.OwnerPubkey, a.RelayPubkey}}, Content: a.ProfileContent}
}
