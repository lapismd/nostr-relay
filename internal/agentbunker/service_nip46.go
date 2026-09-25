package agentbunker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip44"
	"fiatjaf.com/nostr/nip46"
	"strings"
	"time"
)

func (s *Service) HandleNIP46(ctx context.Context, event nostr.Event) (nostr.Event, error) {
	if !validSigned(event) || event.Kind != nostr.KindNostrConnect {
		return nostr.Event{}, errors.New("invalid NIP-46 event")
	}
	now := s.Now()
	created := time.Unix(int64(event.CreatedAt), 0)
	if created.Before(now.Add(-2*time.Minute)) || created.After(now.Add(2*time.Minute)) {
		return nostr.Event{}, errors.New("stale NIP-46 event")
	}
	target := exactTag(event.Tags, "p")
	if target == "" {
		return nostr.Event{}, errors.New("invalid NIP-46 target")
	}
	agent, err := s.Store.Agent(ctx, target)
	if err != nil {
		return nostr.Event{}, errors.New("unknown managed agent")
	}
	clientKey := event.PubKey.Hex()
	client, err := s.Store.Client(ctx, target, clientKey)
	if err != nil || client.Status == ClientRevoked {
		return nostr.Event{}, errors.New("unknown controller client")
	}
	if !s.allowRequest(target + ":" + clientKey) {
		return nostr.Event{}, errors.New("rate limit exceeded")
	}
	secretBytes, err := agent.Key.Decrypt(s.agentAAD(agent.Pubkey, agent.OwnerPubkey), s.Wrapper)
	if err != nil || len(secretBytes) != 32 {
		return nostr.Event{}, errors.New("managed signer unavailable")
	}
	defer clear(secretBytes)
	var secret nostr.SecretKey
	copy(secret[:], secretBytes)
	conversation, err := nip44.GenerateConversationKey(event.PubKey, secret)
	if err != nil {
		return nostr.Event{}, errors.New("managed signer unavailable")
	}
	plaintext, err := nip44.Decrypt(event.Content, conversation)
	if err != nil {
		return nostr.Event{}, errors.New("invalid encrypted request")
	}
	var request nip46.Request
	if json.Unmarshal([]byte(plaintext), &request) != nil || request.ID == "" || len(request.ID) > 256 {
		return nostr.Event{}, errors.New("invalid NIP-46 request")
	}
	hash := sha256.Sum256([]byte(plaintext))
	contentHash := hex.EncodeToString(hash[:])
	if cached, err := s.Store.CachedRequest(ctx, target, clientKey, request.ID); err == nil {
		if cached.ContentHash != contentHash {
			return nostr.Event{}, ErrReplay
		}
		return cached.ResponseEvent, nil
	}
	result, resultErr := s.execute(ctx, agent, client, request, secret)
	response := nip46.Response{ID: request.ID}
	if resultErr != nil {
		response.Error = resultErr.Error()
	} else {
		response.Result = result
	}
	encoded, _ := json.Marshal(response)
	ciphertext, err := nip44.Encrypt(string(encoded), conversation)
	if err != nil {
		return nostr.Event{}, errors.New("encrypt NIP-46 response")
	}
	out := nostr.Event{Kind: nostr.KindNostrConnect, CreatedAt: nostr.Timestamp(now.Unix()), Tags: nostr.Tags{{"p", clientKey}}, Content: ciphertext}
	if err = out.Sign(secret); err != nil {
		return nostr.Event{}, errors.New("sign NIP-46 response")
	}
	cache := CachedRequest{ContentHash: contentHash, ResponseEvent: out}
	if err = s.Store.SaveRequest(ctx, target, clientKey, request.ID, cache, now); errors.Is(err, ErrConflict) {
		existing, loadErr := s.Store.CachedRequest(ctx, target, clientKey, request.ID)
		if loadErr == nil && existing.ContentHash == contentHash {
			return existing.ResponseEvent, nil
		}
		return nostr.Event{}, ErrReplay
	}
	return out, err
}

func (s *Service) execute(ctx context.Context, a Agent, c Client, r nip46.Request, secret nostr.SecretKey) (string, error) {
	if r.Method != "connect" && c.Status != ClientActive {
		return "", errors.New("client is not paired")
	}
	switch r.Method {
	case "connect":
		if c.Status == ClientActive {
			return "ack", nil
		}
		if len(r.Params) < 2 || s.Now().After(c.CreatedAt.Add(s.Config.InvitationLifetime)) {
			return "", errors.New("invitation expired")
		}
		digest := sha256.Sum256([]byte(r.Params[1]))
		if !strings.EqualFold(c.InvitationHash, hex.EncodeToString(digest[:])) {
			return "", errors.New("invalid invitation")
		}
		if err := s.Store.PairClient(ctx, a.Pubkey, c.ClientPubkey, s.Now()); err != nil {
			return "", errors.New("pairing failed")
		}
		return "ack", nil
	case "get_public_key":
		return a.Pubkey, nil
	case "sign_event":
		if len(r.Params) != 1 {
			return "", errors.New("invalid sign_event request")
		}
		var event nostr.Event
		if json.Unmarshal([]byte(r.Params[0]), &event) != nil {
			return "", errors.New("invalid event")
		}
		if err := s.authorizeSigning(a, event); err != nil {
			return "", err
		}
		event.PubKey = secret.Public()
		if err := event.Sign(secret); err != nil {
			return "", errors.New("signing failed")
		}
		encoded, _ := json.Marshal(event)
		return string(encoded), nil
	case "nip44_encrypt", "nip44_decrypt":
		if a.Status != StatusActive || len(r.Params) != 2 || !s.directMessagePeers(a)[r.Params[0]] {
			return "", errors.New("NIP-44 peer is not approved by the owner")
		}
		peer, _ := nostr.PubKeyFromHex(r.Params[0])
		conversation, err := nip44.GenerateConversationKey(peer, secret)
		if err != nil {
			return "", errors.New("encryption failed")
		}
		if r.Method == "nip44_encrypt" {
			if err := s.authorizeDirectContent(a, r.Params[0], r.Params[1], true); err != nil {
				return "", err
			}
			return nip44.Encrypt(r.Params[1], conversation)
		}
		plaintext, err := nip44.Decrypt(r.Params[1], conversation)
		if err != nil {
			return "", err
		}
		if err := s.authorizeDirectContent(a, r.Params[0], plaintext, false); err != nil {
			return "", err
		}
		return plaintext, nil
	case "lapis_nip17_unwrap":
		if a.Status != StatusActive || len(r.Params) != 1 || len(r.Params[0]) > maxBody {
			return "", errors.New("invalid NIP-17 gift wrap")
		}
		var giftWrap nostr.Event
		if json.Unmarshal([]byte(r.Params[0]), &giftWrap) != nil || !validSigned(giftWrap) || giftWrap.Kind != nostr.KindGiftWrap || exactTag(giftWrap.Tags, "p") != a.Pubkey || giftWrap.Content == "" {
			return "", errors.New("invalid NIP-17 gift wrap")
		}
		conversation, err := nip44.GenerateConversationKey(giftWrap.PubKey, secret)
		if err != nil {
			return "", errors.New("NIP-17 unwrap failed")
		}
		plaintext, err := nip44.Decrypt(giftWrap.Content, conversation)
		if err != nil {
			return "", errors.New("NIP-17 unwrap failed")
		}
		return plaintext, nil
	case "ping":
		return "pong", nil
	default:
		return "", errors.New("method is not allowed")
	}
}
