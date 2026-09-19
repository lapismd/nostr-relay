package agentbunker

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip44"
	"fiatjaf.com/nostr/nip46"
	"zooid/internal/gitforge"
	"zooid/internal/keywrap"
	"zooid/internal/projecttask"
)

const maxBody = 128 << 10

type Service struct {
	Config       Config
	Store        Store
	Wrapper      keywrap.Wrapper
	Now          func() time.Time
	AdmitOwner   func(context.Context, nostr.PubKey) bool
	IsMember     func(string, nostr.PubKey) bool
	PersistEvent func(nostr.Event) error
	CurrentEvent func(kind nostr.Kind, author nostr.PubKey, d string) *nostr.Event
	EventByID    func(nostr.ID) *nostr.Event
	mu           sync.Mutex
	rates        map[string]rateWindow
}
type rateWindow struct {
	started time.Time
	count   int
}

type createRequest struct {
	ClientPubkey     string `json:"client_pubkey"`
	ProfileContent   string `json:"profile_content"`
	ProfileCreatedAt int64  `json:"profile_created_at"`
	IdempotencyKey   string `json:"idempotency_key"`
}
type createResponse struct {
	Agent           AgentView     `json:"agent"`
	BunkerURL       string        `json:"bunker_url"`
	ProfileTemplate eventTemplate `json:"profile_template"`
}
type eventTemplate struct {
	Kind      int        `json:"kind"`
	CreatedAt int64      `json:"created_at"`
	Tags      [][]string `json:"tags"`
	Content   string     `json:"content"`
}
type activationRequest struct {
	OwnerEvent   nostr.Event `json:"owner_event"`
	ProfileEvent nostr.Event `json:"profile_event"`
}
type replacementRequest struct {
	ClientPubkey   string `json:"client_pubkey"`
	IdempotencyKey string `json:"idempotency_key"`
}
type replacementResponse struct {
	BunkerURL string    `json:"bunker_url"`
	ExpiresAt time.Time `json:"expires_at"`
}
type deletionRequest struct {
	UnlinkEvent nostr.Event `json:"unlink_event"`
}

func NewService(config Config, store Store, wrapper keywrap.Wrapper) (*Service, error) {
	if store == nil || wrapper == nil || config.RelayPubkey == nostr.ZeroPK {
		return nil, errors.New("managed agent store, wrapper, and relay pubkey are required")
	}
	if config.InvitationLifetime <= 0 {
		config.InvitationLifetime = 24 * time.Hour
	}
	if config.MaxActivePerOwner <= 0 {
		config.MaxActivePerOwner = 10
	}
	if config.MaxPendingPerOwner <= 0 {
		config.MaxPendingPerOwner = 3
	}
	if config.CreationsPerHour <= 0 {
		config.CreationsPerHour = 5
	}
	if config.RequestsPerMinute <= 0 {
		config.RequestsPerMinute = 120
	}
	return &Service{Config: config, Store: store, Wrapper: wrapper, Now: time.Now, rates: map[string]rateWindow{}}, nil
}

func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.handleCORS(w, r) {
		return
	}
	path := strings.TrimSuffix(r.URL.Path, "/")
	if path == "/nip46" {
		s.serveWebSocket(w, r)
		return
	}
	if path == "/api/agents/managed" {
		switch r.Method {
		case http.MethodGet:
			s.list(w, r)
		case http.MethodPost:
			s.create(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}
	prefix := "/api/agents/managed/"
	if !strings.HasPrefix(path, prefix) {
		http.NotFound(w, r)
		return
	}
	rest := strings.TrimPrefix(path, prefix)
	parts := strings.Split(rest, "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	agent := parts[0]
	switch {
	case len(parts) == 2 && parts[1] == "activate" && r.Method == http.MethodPost:
		s.activate(w, r, agent)
	case len(parts) == 2 && parts[1] == "connections" && r.Method == http.MethodPost:
		s.replace(w, r, agent)
	case len(parts) == 3 && parts[1] == "connections" && parts[2] == "current" && r.Method == http.MethodDelete:
		s.revoke(w, r, agent)
	case len(parts) == 1 && r.Method == http.MethodDelete:
		s.delete(w, r, agent)
	default:
		http.NotFound(w, r)
	}
}

func (s *Service) handleCORS(w http.ResponseWriter, r *http.Request) bool {
	origin, allowed := managedBrowserOriginAllowed(r.Header.Get("Origin"), s.Config.AllowedBrowserOrigins)
	if allowed {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	}
	if r.Method == http.MethodOptions {
		if !allowed {
			http.Error(w, "origin not allowed", http.StatusForbidden)
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
		return true
	}
	return false
}

func managedBrowserOriginAllowed(rawOrigin string, allowedOrigins []string) (string, bool) {
	origin, err := normalizeManagedBrowserOrigin(rawOrigin)
	if err != nil {
		return "", false
	}
	for _, allowed := range allowedOrigins {
		if managedBrowserOriginMatches(allowed, origin) {
			return origin, true
		}
	}
	return "", false
}

func managedBrowserOriginMatches(rawAllowed, origin string) bool {
	allowed := strings.TrimSpace(rawAllowed)
	if strings.HasSuffix(allowed, ":*") {
		base := strings.TrimSuffix(allowed, ":*")
		parsedAllowed, err := url.Parse(base)
		if err != nil || parsedAllowed == nil || parsedAllowed.Scheme != "http" || !isManagedLocalBrowserHost(parsedAllowed.Hostname()) {
			return false
		}
		parsedOrigin, err := url.Parse(origin)
		return err == nil && parsedOrigin != nil && parsedOrigin.Scheme == "http" && parsedOrigin.Port() != "" && strings.EqualFold(parsedOrigin.Hostname(), parsedAllowed.Hostname())
	}
	normalized, err := normalizeManagedBrowserOrigin(allowed)
	return err == nil && normalized == origin
}

func normalizeManagedBrowserOrigin(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed == nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", errors.New("browser origin must be an absolute origin")
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isManagedLocalBrowserHost(parsed.Hostname())) {
		return "", errors.New("browser origin must use HTTPS, except for localhost development")
	}
	return strings.ToLower(parsed.Scheme + "://" + parsed.Host), nil
}

func isManagedLocalBrowserHost(hostname string) bool {
	hostname = strings.ToLower(strings.TrimSuffix(hostname, "."))
	return hostname == "localhost" || hostname == "127.0.0.1" || hostname == "::1" || strings.HasSuffix(hostname, ".localhost")
}

func (s *Service) authenticate(r *http.Request, body []byte) (nostr.Event, error) {
	expected := strings.TrimRight(s.Config.PublicURL, "/") + r.URL.Path
	event, err := gitforge.VerifyRequestAuth(r, expected, body, s.Now())
	if err != nil {
		return event, err
	}
	if err = s.Store.UseAuthEvent(r.Context(), event.ID.Hex(), s.Now()); err != nil {
		return event, ErrReplay
	}
	if s.AdmitOwner != nil && !s.AdmitOwner(r.Context(), event.PubKey) {
		return event, errors.New("owner is not admitted")
	}
	if _, err = s.Store.Agent(r.Context(), event.PubKey.Hex()); err == nil {
		return event, errors.New("managed agents cannot provision agents")
	}
	return event, nil
}
func readBody(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	return io.ReadAll(io.LimitReader(r.Body, maxBody+1))
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

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
func validJSONObject(value string) bool {
	var object map[string]any
	return json.Unmarshal([]byte(value), &object) == nil
}
func (s *Service) validActivation(a Agent, input activationRequest) bool {
	return validSigned(input.OwnerEvent) && input.OwnerEvent.Kind == 30177 && input.OwnerEvent.PubKey.Hex() == a.OwnerPubkey && exactTag(input.OwnerEvent.Tags, "d") == a.Pubkey && validSigned(input.ProfileEvent) && input.ProfileEvent.PubKey.Hex() == a.Pubkey && templateMatches(input.ProfileEvent, s.profileTemplate(a))
}
func validUnlink(event nostr.Event, a Agent) bool {
	return validSigned(event) && event.Kind == nostr.KindDeletion && event.PubKey.Hex() == a.OwnerPubkey && slices.ContainsFunc(event.Tags, func(t nostr.Tag) bool {
		return len(t) == 2 && t[0] == "a" && t[1] == "30177:"+a.OwnerPubkey+":"+a.Pubkey
	})
}
func validSigned(e nostr.Event) bool { return e.CheckID() && e.VerifySignature() }
func exactTag(tags nostr.Tags, name string) string {
	value := ""
	count := 0
	for _, tag := range tags {
		if len(tag) > 0 && tag[0] == name {
			if len(tag) != 2 {
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
func templateMatches(e nostr.Event, t eventTemplate) bool {
	return int(e.Kind) == t.Kind && int64(e.CreatedAt) == t.CreatedAt && e.Content == t.Content && equalTags(e.Tags, t.Tags)
}
func equalTags(a nostr.Tags, b [][]string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !slices.Equal([]string(a[i]), b[i]) {
			return false
		}
	}
	return true
}
func (s *Service) audit(ctx context.Context, a Audit) { _ = s.Store.Audit(ctx, a) }

func (s *Service) NIP11() map[string]any {
	u, _ := url.Parse(strings.TrimRight(s.Config.PublicURL, "/") + "/nip46")
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	return map[string]any{"version": 1, "provisioning_url": strings.TrimRight(s.Config.PublicURL, "/") + "/api/agents/managed", "bunker_url": u.String(), "relay_pubkey": s.Config.RelayPubkey.Hex(), "authorization_type": "lapis-managed-v1"}
}
func (s *Service) VerifyActiveOwnership(ctx context.Context, agent, owner, relay nostr.PubKey) bool {
	a, err := s.Store.Agent(ctx, agent.Hex())
	if err != nil || a.Status != StatusActive || a.OwnerPubkey != owner.Hex() || a.RelayPubkey != relay.Hex() {
		return false
	}
	ownerEvent, profileEvent := a.OwnerEvent, a.ProfileEvent
	if s.CurrentEvent != nil {
		ownerEvent = s.CurrentEvent(30177, owner, agent.Hex())
		profileEvent = s.CurrentEvent(nostr.KindProfileMetadata, agent, "")
	}
	return ownerEvent != nil && profileEvent != nil && s.validActivation(a, activationRequest{OwnerEvent: *ownerEvent, ProfileEvent: *profileEvent})
}

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
func (s *Service) authorizeSigning(a Agent, event nostr.Event) error {
	event.PubKey = nostr.MustPubKeyFromHex(a.Pubkey)
	now := s.Now()
	created := time.Unix(int64(event.CreatedAt), 0)
	maxAge := 2 * time.Minute
	if event.Kind == nostr.KindSeal {
		maxAge = 48 * time.Hour
	}
	if created.Before(now.Add(-maxAge)) || created.After(now.Add(2*time.Minute)) {
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
	if !slices.Contains([]nostr.Kind{nostr.KindSeal, 22242, 9, 7, 5, 1111, 1621, 1630, 1631, 1632, 1633, 3901, 3902, 3903, 3904, 3905, 3906, 3907, 3908, 20001, 20002, 24200, 27235, 30023, 30617, 30621, 33901, 40100, nostr.KindDMRelayList}, event.Kind) {
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

func isJJContextKind(kind nostr.Kind) bool {
	return (kind >= 3901 && kind <= 3908) || kind == 33901
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

func countTags(tags nostr.Tags, name string) int {
	count := 0
	for _, tag := range tags {
		if len(tag) > 0 && tag[0] == name {
			count++
		}
	}
	return count
}

func (s *Service) inboxRelayURL() string {
	u, err := url.Parse(s.Config.PublicURL)
	if err != nil {
		return ""
	}
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else if u.Scheme == "http" {
		u.Scheme = "ws"
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}
func (s *Service) allowRequest(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.Now()
	window := s.rates[key]
	if window.started.IsZero() || now.Sub(window.started) >= time.Minute {
		window = rateWindow{started: now}
	}
	if window.count >= s.Config.RequestsPerMinute {
		return false
	}
	window.count++
	s.rates[key] = window
	return true
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
