package agentbunker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fiatjaf.com/nostr"
	"net/http"
	"time"
	"zooid/internal/keywrap"
)

func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	event, err := s.authenticate(r, nil)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid_authorization")
		return
	}
	agents, err := s.Store.ListAgents(r.Context(), event.PubKey.Hex())
	if err != nil {
		writeError(w, 500, "managed_agent_store_failed")
		return
	}
	writeJSON(w, 200, map[string]any{"agents": agents})
}

func (s *Service) create(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(r)
	if err != nil || len(body) > maxBody {
		writeError(w, 400, "invalid_request")
		return
	}
	ownerEvent, err := s.authenticate(r, body)
	if err != nil {
		writeError(w, 401, "invalid_authorization")
		return
	}
	var input createRequest
	if json.Unmarshal(body, &input) != nil || !nostr.IsValid32ByteHex(input.ClientPubkey) || input.IdempotencyKey == "" || len(input.IdempotencyKey) > 128 || len(input.ProfileContent) > 65535 || !validJSONObject(input.ProfileContent) {
		writeError(w, 400, "invalid_request")
		return
	}
	owner := ownerEvent.PubKey.Hex()
	hash := sha256.Sum256(body)
	requestHash := hex.EncodeToString(hash[:])
	if savedHash, saved, err := s.Store.HTTPResult(r.Context(), owner, input.IdempotencyKey); err == nil {
		if savedHash != requestHash {
			writeError(w, 409, "idempotency_conflict")
			return
		}
		saved, err = s.restorePendingInvitation(r.Context(), input.ClientPubkey, saved)
		if err != nil {
			writeError(w, 500, "managed_agent_key_failed")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write(saved)
		return
	}
	active, pending, recent, err := s.Store.OwnerCounts(r.Context(), owner, s.Now().Add(-time.Hour))
	if err != nil {
		writeError(w, 500, "managed_agent_store_failed")
		return
	}
	if active >= s.Config.MaxActivePerOwner || pending >= s.Config.MaxPendingPerOwner || recent >= s.Config.CreationsPerHour {
		writeError(w, 429, "managed_agent_quota_exceeded")
		return
	}
	secret := nostr.Generate()
	defer clear(secret[:])
	agentPubkey := secret.Public().Hex()
	created := s.Now()
	profileAt := input.ProfileCreatedAt
	if profileAt == 0 {
		profileAt = created.Unix()
	}
	if time.Unix(profileAt, 0).Before(created.Add(-2*time.Minute)) || time.Unix(profileAt, 0).After(created.Add(2*time.Minute)) {
		writeError(w, 400, "invalid_profile_timestamp")
		return
	}
	key, err := keywrap.Encrypt(secret[:], s.agentAAD(agentPubkey, owner), s.Wrapper)
	if err != nil {
		writeError(w, 500, "managed_agent_key_failed")
		return
	}
	invitation, invitationHash, invitationEnvelope, err := s.newInvitation(agentPubkey, input.ClientPubkey)
	if err != nil {
		writeError(w, 500, "managed_agent_key_failed")
		return
	}
	expires := created.Add(s.Config.InvitationLifetime)
	agent := Agent{Pubkey: agentPubkey, OwnerPubkey: owner, RelayPubkey: s.Config.RelayPubkey.Hex(), Status: StatusPending, ProfileContent: input.ProfileContent, ProfileCreatedAt: profileAt, Key: key, CreatedAt: created, ExpiresAt: expires}
	client := Client{AgentPubkey: agentPubkey, ClientPubkey: input.ClientPubkey, Status: ClientPending, InvitationHash: invitationHash, Invitation: invitationEnvelope, CreatedAt: created}
	if err = s.Store.CreateAgent(r.Context(), agent, client); err != nil {
		writeError(w, 409, "managed_agent_conflict")
		return
	}
	response := createResponse{Agent: AgentView{Pubkey: agentPubkey, OwnerPubkey: owner, Status: StatusPending, ConnectionState: ClientPending, CreatedAt: created, ExpiresAt: expires}, BunkerURL: s.bunkerURL(agentPubkey, invitation), ProfileTemplate: s.profileTemplate(agent)}
	cached := response
	cached.BunkerURL = s.bunkerURL(agentPubkey, "")
	encoded, _ := json.Marshal(cached)
	_ = s.Store.SaveHTTPResult(r.Context(), owner, input.IdempotencyKey, requestHash, encoded, created)
	s.audit(r.Context(), Audit{OwnerPubkey: owner, AgentPubkey: agentPubkey, ClientPubkey: input.ClientPubkey, Operation: "create", Result: "success", CreatedAt: created})
	writeJSON(w, 201, response)
}

func (s *Service) activate(w http.ResponseWriter, r *http.Request, pubkey string) {
	body, _ := readBody(r)
	ownerProof, err := s.authenticate(r, body)
	if err != nil {
		writeError(w, 401, "invalid_authorization")
		return
	}
	agent, err := s.ownedAgent(r.Context(), pubkey, ownerProof.PubKey.Hex())
	if err != nil {
		writeError(w, 404, "managed_agent_not_found")
		return
	}
	var input activationRequest
	if json.Unmarshal(body, &input) != nil || !s.validActivation(agent, input) {
		writeError(w, 400, "invalid_activation")
		return
	}
	ownerRaw, _ := json.Marshal(input.OwnerEvent)
	profileRaw, _ := json.Marshal(input.ProfileEvent)
	if agent.Status == StatusActive {
		writeJSON(w, 200, map[string]string{"status": "active"})
		return
	}
	if agent.Status != StatusPending {
		writeError(w, 409, "activation_conflict")
		return
	}
	if s.Now().After(agent.ExpiresAt) {
		writeError(w, 410, "managed_agent_draft_expired")
		return
	}
	if s.PersistEvent != nil {
		if err = s.PersistEvent(input.OwnerEvent); err == nil {
			err = s.PersistEvent(input.ProfileEvent)
		}
		if err != nil {
			writeError(w, 500, "activation_publish_failed")
			return
		}
	}
	if err = s.Store.Activate(r.Context(), pubkey, ownerRaw, profileRaw, s.Now()); err != nil {
		writeError(w, 409, "activation_conflict")
		return
	}
	s.audit(r.Context(), Audit{OwnerPubkey: agent.OwnerPubkey, AgentPubkey: pubkey, Operation: "activate", Result: "success", CreatedAt: s.Now()})
	writeJSON(w, 200, map[string]string{"status": "active"})
}

func (s *Service) replace(w http.ResponseWriter, r *http.Request, pubkey string) {
	body, _ := readBody(r)
	ownerProof, err := s.authenticate(r, body)
	if err != nil {
		writeError(w, 401, "invalid_authorization")
		return
	}
	agent, err := s.ownedAgent(r.Context(), pubkey, ownerProof.PubKey.Hex())
	if err != nil || agent.Status != StatusActive {
		writeError(w, 404, "managed_agent_not_found")
		return
	}
	var input replacementRequest
	if json.Unmarshal(body, &input) != nil || !nostr.IsValid32ByteHex(input.ClientPubkey) || input.IdempotencyKey == "" || len(input.IdempotencyKey) > 128 {
		writeError(w, 400, "invalid_request")
		return
	}
	requestHashBytes := sha256.Sum256(body)
	requestHash := hex.EncodeToString(requestHashBytes[:])
	resultKey := "connection:" + pubkey + ":" + input.IdempotencyKey
	if savedHash, saved, loadErr := s.Store.HTTPResult(r.Context(), agent.OwnerPubkey, resultKey); loadErr == nil {
		if savedHash != requestHash {
			writeError(w, 409, "idempotency_conflict")
			return
		}
		saved, err = s.restoreReplacementInvitation(r.Context(), pubkey, input.ClientPubkey, saved)
		if err != nil {
			writeError(w, 500, "managed_agent_key_failed")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write(saved)
		return
	}
	invitation, hash, envelope, err := s.newInvitation(pubkey, input.ClientPubkey)
	if err != nil {
		writeError(w, 500, "managed_agent_key_failed")
		return
	}
	client := Client{AgentPubkey: pubkey, ClientPubkey: input.ClientPubkey, Status: ClientPending, InvitationHash: hash, Invitation: envelope, CreatedAt: s.Now()}
	if err = s.Store.SaveReplacement(r.Context(), client); err != nil {
		writeError(w, 409, "connection_conflict")
		return
	}
	response := replacementResponse{BunkerURL: s.bunkerURL(pubkey, invitation), ExpiresAt: client.CreatedAt.Add(s.Config.InvitationLifetime)}
	cached := response
	cached.BunkerURL = s.bunkerURL(pubkey, "")
	encoded, _ := json.Marshal(cached)
	_ = s.Store.SaveHTTPResult(r.Context(), agent.OwnerPubkey, resultKey, requestHash, encoded, client.CreatedAt)
	s.audit(r.Context(), Audit{OwnerPubkey: agent.OwnerPubkey, AgentPubkey: pubkey, ClientPubkey: input.ClientPubkey, Operation: "replace_connection", Result: "success", CreatedAt: client.CreatedAt})
	writeJSON(w, 201, response)
}

func (s *Service) revoke(w http.ResponseWriter, r *http.Request, pubkey string) {
	body, _ := readBody(r)
	proof, err := s.authenticate(r, body)
	if err != nil {
		writeError(w, 401, "invalid_authorization")
		return
	}
	if _, err = s.ownedAgent(r.Context(), pubkey, proof.PubKey.Hex()); err != nil {
		writeError(w, 404, "managed_agent_not_found")
		return
	}
	if err = s.Store.RevokeCurrent(r.Context(), pubkey, s.Now()); err != nil {
		writeError(w, 500, "revocation_failed")
		return
	}
	s.audit(r.Context(), Audit{OwnerPubkey: proof.PubKey.Hex(), AgentPubkey: pubkey, Operation: "revoke", Result: "success", CreatedAt: s.Now()})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) delete(w http.ResponseWriter, r *http.Request, pubkey string) {
	body, _ := readBody(r)
	proof, err := s.authenticate(r, body)
	if err != nil {
		writeError(w, 401, "invalid_authorization")
		return
	}
	agent, err := s.ownedAgent(r.Context(), pubkey, proof.PubKey.Hex())
	if err != nil {
		writeError(w, 404, "managed_agent_not_found")
		return
	}
	var input deletionRequest
	if json.Unmarshal(body, &input) != nil || !validUnlink(input.UnlinkEvent, agent) {
		writeError(w, 400, "invalid_unlink")
		return
	}
	if s.PersistEvent != nil {
		if err = s.PersistEvent(input.UnlinkEvent); err != nil {
			writeError(w, 500, "unlink_publish_failed")
			return
		}
	}
	if err = s.Store.DeleteAgent(r.Context(), pubkey); err != nil {
		writeError(w, 500, "managed_agent_delete_failed")
		return
	}
	s.audit(r.Context(), Audit{OwnerPubkey: agent.OwnerPubkey, AgentPubkey: pubkey, Operation: "delete", Result: "success", CreatedAt: s.Now()})
	w.WriteHeader(http.StatusNoContent)
}
