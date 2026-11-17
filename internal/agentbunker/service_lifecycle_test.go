package agentbunker

import (
	"context"
	"encoding/json"
	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip46"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
	"zooid/internal/keywrap"
)

func TestManagedAgentCreateBootstrapActivationAndReplay(t *testing.T) {
	service, store, _ := testService(t)
	owner := nostr.Generate()
	client := nostr.Generate()
	body, _ := json.Marshal(createRequest{ClientPubkey: client.Public().Hex(), ProfileContent: `{"name":"Helper"}`, ProfileCreatedAt: service.Now().Unix(), IdempotencyKey: "create-1"})
	request := authRequest(t, http.MethodPost, "/api/agents/managed", body, owner, service.Now())
	recorder := httptest.NewRecorder()
	service.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusCreated, recorder.Code, recorder.Body.String())
	var created createResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &created))
	agent := nostr.MustPubKeyFromHex(created.Agent.Pubkey)
	parsed, err := urlParse(created.BunkerURL)
	require.NoError(t, err)
	invitation := parsed.Query().Get("secret")
	require.NotEmpty(t, invitation)
	cachedCreate := store.http[owner.Public().Hex()+":create-1"].body
	require.NotContains(t, string(cachedCreate), invitation)
	retryRequest := authRequest(t, http.MethodPost, "/api/agents/managed", body, owner, service.Now().Add(time.Second))
	retryRecorder := httptest.NewRecorder()
	service.ServeHTTP(retryRecorder, retryRequest)
	require.Equal(t, http.StatusOK, retryRecorder.Code, retryRecorder.Body.String())
	var retried createResponse
	require.NoError(t, json.Unmarshal(retryRecorder.Body.Bytes(), &retried))
	retryURL, err := urlParse(retried.BunkerURL)
	require.NoError(t, err)
	require.Equal(t, invitation, retryURL.Query().Get("secret"))
	connect := rpcEvent(t, agent, nostr.SecretKey{}, client, nip46.Request{ID: "connect-1", Method: "connect", Params: []string{"{}", invitation}}, service.Now())
	response, err := service.HandleNIP46(context.Background(), connect)
	require.NoError(t, err)
	require.Equal(t, "ack", decodeBunkerResponse(t, response, agent, client).Result)
	cached, err := service.HandleNIP46(context.Background(), connect)
	require.NoError(t, err)
	require.Equal(t, response.ID, cached.ID)
	pairedRetry := authRequest(t, http.MethodPost, "/api/agents/managed", body, owner, service.Now().Add(2*time.Second))
	pairedRecorder := httptest.NewRecorder()
	service.ServeHTTP(pairedRecorder, pairedRetry)
	require.Equal(t, http.StatusOK, pairedRecorder.Code, pairedRecorder.Body.String())
	var pairedCreated createResponse
	require.NoError(t, json.Unmarshal(pairedRecorder.Body.Bytes(), &pairedCreated))
	pairedURL, err := urlParse(pairedCreated.BunkerURL)
	require.NoError(t, err)
	require.Empty(t, pairedURL.Query().Get("secret"))
	different := rpcEvent(t, agent, nostr.SecretKey{}, client, nip46.Request{ID: "connect-1", Method: "ping"}, service.Now())
	_, err = service.HandleNIP46(context.Background(), different)
	require.ErrorIs(t, err, ErrReplay)
	profileJSON, _ := json.Marshal(created.ProfileTemplate)
	sign := rpcEvent(t, agent, nostr.SecretKey{}, client, nip46.Request{ID: "sign-1", Method: "sign_event", Params: []string{string(profileJSON)}}, service.Now())
	signedResponse, err := service.HandleNIP46(context.Background(), sign)
	require.NoError(t, err)
	decoded := decodeBunkerResponse(t, signedResponse, agent, client)
	require.Empty(t, decoded.Error)
	var profile nostr.Event
	require.NoError(t, json.Unmarshal([]byte(decoded.Result), &profile))
	require.True(t, profile.VerifySignature())
	ownerEvent := nostr.Event{Kind: 30177, CreatedAt: nostr.Timestamp(service.Now().Unix()), Tags: nostr.Tags{{"d", agent.Hex()}}, Content: "{}"}
	require.NoError(t, ownerEvent.Sign(owner))
	activationBody, _ := json.Marshal(activationRequest{OwnerEvent: ownerEvent, ProfileEvent: profile})
	activation := authRequest(t, http.MethodPost, "/api/agents/managed/"+agent.Hex()+"/activate", activationBody, owner, service.Now())
	activationRecorder := httptest.NewRecorder()
	service.ServeHTTP(activationRecorder, activation)
	require.Equal(t, http.StatusOK, activationRecorder.Code, activationRecorder.Body.String())
	stored, err := store.Agent(context.Background(), agent.Hex())
	require.NoError(t, err)
	require.Equal(t, StatusActive, stored.Status)
	require.True(t, service.VerifyActiveOwnership(context.Background(), agent, owner.Public(), service.Config.RelayPubkey))

	replacementClient := nostr.Generate()
	replacementBody, _ := json.Marshal(replacementRequest{ClientPubkey: replacementClient.Public().Hex(), IdempotencyKey: "replace-1"})
	replacementPath := "/api/agents/managed/" + agent.Hex() + "/connections"
	replacementRequest := authRequest(t, http.MethodPost, replacementPath, replacementBody, owner, service.Now().Add(3*time.Second))
	replacementRecorder := httptest.NewRecorder()
	service.ServeHTTP(replacementRecorder, replacementRequest)
	require.Equal(t, http.StatusCreated, replacementRecorder.Code, replacementRecorder.Body.String())
	var replacement replacementResponse
	require.NoError(t, json.Unmarshal(replacementRecorder.Body.Bytes(), &replacement))
	replacementURL, err := urlParse(replacement.BunkerURL)
	require.NoError(t, err)
	replacementInvitation := replacementURL.Query().Get("secret")
	require.NotEmpty(t, replacementInvitation)
	cachedReplacement := store.http[owner.Public().Hex()+":connection:"+agent.Hex()+":replace-1"].body
	require.NotContains(t, string(cachedReplacement), replacementInvitation)

	replacementRetry := authRequest(t, http.MethodPost, replacementPath, replacementBody, owner, service.Now().Add(4*time.Second))
	replacementRetryRecorder := httptest.NewRecorder()
	service.ServeHTTP(replacementRetryRecorder, replacementRetry)
	require.Equal(t, http.StatusOK, replacementRetryRecorder.Code, replacementRetryRecorder.Body.String())
	var retriedReplacement replacementResponse
	require.NoError(t, json.Unmarshal(replacementRetryRecorder.Body.Bytes(), &retriedReplacement))
	retriedReplacementURL, err := urlParse(retriedReplacement.BunkerURL)
	require.NoError(t, err)
	require.Equal(t, replacementInvitation, retriedReplacementURL.Query().Get("secret"))

	replacementConnect := rpcEvent(t, agent, nostr.SecretKey{}, replacementClient, nip46.Request{ID: "replacement-connect", Method: "connect", Params: []string{"{}", replacementInvitation}}, service.Now())
	replacementConnectResponse, err := service.HandleNIP46(context.Background(), replacementConnect)
	require.NoError(t, err)
	require.Equal(t, "ack", decodeBunkerResponse(t, replacementConnectResponse, agent, replacementClient).Result)
	require.Equal(t, ClientRevoked, store.clients[agent.Hex()+":"+client.Public().Hex()].Status)

	pairedReplacementRetry := authRequest(t, http.MethodPost, replacementPath, replacementBody, owner, service.Now().Add(5*time.Second))
	pairedReplacementRecorder := httptest.NewRecorder()
	service.ServeHTTP(pairedReplacementRecorder, pairedReplacementRetry)
	require.Equal(t, http.StatusOK, pairedReplacementRecorder.Code, pairedReplacementRecorder.Body.String())
	var pairedReplacement replacementResponse
	require.NoError(t, json.Unmarshal(pairedReplacementRecorder.Body.Bytes(), &pairedReplacement))
	pairedReplacementURL, err := urlParse(pairedReplacement.BunkerURL)
	require.NoError(t, err)
	require.Empty(t, pairedReplacementURL.Query().Get("secret"))
}

func TestManagedAgentDiscoveryAndBrowserOriginCORS(t *testing.T) {
	service, _, _ := testService(t)
	discovery := service.NIP11()
	require.Equal(t, 1, discovery["version"])
	require.Equal(t, "wss://relay.example/nip46", discovery["bunker_url"])
	require.Equal(t, "lapis-managed-v1", discovery["authorization_type"])

	approved := httptest.NewRequest(http.MethodOptions, "/api/agents/managed", nil)
	approved.Header.Set("Origin", "https://community.example")
	approvedRecorder := httptest.NewRecorder()
	service.ServeHTTP(approvedRecorder, approved)
	require.Equal(t, http.StatusNoContent, approvedRecorder.Code)
	require.Equal(t, "https://community.example", approvedRecorder.Header().Get("Access-Control-Allow-Origin"))

	service.Config.AllowedBrowserOrigins = append(service.Config.AllowedBrowserOrigins, "http://127.0.0.1:*")
	desktop := httptest.NewRequest(http.MethodOptions, "/api/agents/managed", nil)
	desktop.Header.Set("Origin", "http://127.0.0.1:53337")
	desktopRecorder := httptest.NewRecorder()
	service.ServeHTTP(desktopRecorder, desktop)
	require.Equal(t, http.StatusNoContent, desktopRecorder.Code)
	require.Equal(t, "http://127.0.0.1:53337", desktopRecorder.Header().Get("Access-Control-Allow-Origin"))

	unapproved := httptest.NewRequest(http.MethodOptions, "/api/agents/managed", nil)
	unapproved.Header.Set("Origin", "https://other.example")
	unapprovedRecorder := httptest.NewRecorder()
	service.ServeHTTP(unapprovedRecorder, unapproved)
	require.Equal(t, http.StatusForbidden, unapprovedRecorder.Code)
	require.Empty(t, unapprovedRecorder.Header().Get("Access-Control-Allow-Origin"))
}

func TestManagedAgentPolicyRejectsForbiddenSigningAndNonOwnerEncryption(t *testing.T) {
	service, store, _ := testService(t)
	owner := nostr.Generate()
	agentSecret := nostr.Generate()
	clientSecret := nostr.Generate()
	key, err := keywrap.Encrypt(agentSecret[:], service.agentAAD(agentSecret.Public().Hex(), owner.Public().Hex()), service.Wrapper)
	require.NoError(t, err)
	now := service.Now()
	store.agents[agentSecret.Public().Hex()] = Agent{Pubkey: agentSecret.Public().Hex(), OwnerPubkey: owner.Public().Hex(), RelayPubkey: service.Config.RelayPubkey.Hex(), Status: StatusActive, Key: key, CreatedAt: now}
	store.clients[agentSecret.Public().Hex()+":"+clientSecret.Public().Hex()] = Client{AgentPubkey: agentSecret.Public().Hex(), ClientPubkey: clientSecret.Public().Hex(), Status: ClientActive, CreatedAt: now}
	template := eventTemplate{Kind: 1, CreatedAt: now.Unix(), Tags: [][]string{}, Content: ""}
	raw, _ := json.Marshal(template)
	request := rpcEvent(t, agentSecret.Public(), agentSecret, clientSecret, nip46.Request{ID: "forbidden", Method: "sign_event", Params: []string{string(raw)}}, now)
	event, err := service.HandleNIP46(context.Background(), request)
	require.NoError(t, err)
	require.Contains(t, decodeBunkerResponse(t, event, agentSecret.Public(), clientSecret).Error, "kind is not allowed")
	stranger := nostr.Generate()
	encrypt := rpcEvent(t, agentSecret.Public(), agentSecret, clientSecret, nip46.Request{ID: "encrypt", Method: "nip44_encrypt", Params: []string{stranger.Public().Hex(), "secret"}}, now)
	event, err = service.HandleNIP46(context.Background(), encrypt)
	require.NoError(t, err)
	require.Contains(t, decodeBunkerResponse(t, event, agentSecret.Public(), clientSecret).Error, "not approved by the owner")
}

func TestManagedAgentRequiresPairingAndExpiresPendingProfile(t *testing.T) {
	service, store, _ := testService(t)
	owner := nostr.Generate()
	agentSecret := nostr.Generate()
	clientSecret := nostr.Generate()
	now := service.Now()
	key, err := keywrap.Encrypt(agentSecret[:], service.agentAAD(agentSecret.Public().Hex(), owner.Public().Hex()), service.Wrapper)
	require.NoError(t, err)
	profile := eventTemplate{Kind: 0, CreatedAt: now.Unix(), Tags: [][]string{{"lapis-managed", owner.Public().Hex(), service.Config.RelayPubkey.Hex()}}, Content: `{"name":"Expired"}`}
	store.agents[agentSecret.Public().Hex()] = Agent{
		Pubkey: agentSecret.Public().Hex(), OwnerPubkey: owner.Public().Hex(), RelayPubkey: service.Config.RelayPubkey.Hex(),
		Status: StatusPending, ProfileContent: profile.Content, ProfileCreatedAt: profile.CreatedAt, Key: key,
		CreatedAt: now.Add(-25 * time.Hour), ExpiresAt: now.Add(-time.Hour),
	}
	store.clients[agentSecret.Public().Hex()+":"+clientSecret.Public().Hex()] = Client{
		AgentPubkey: agentSecret.Public().Hex(), ClientPubkey: clientSecret.Public().Hex(), Status: ClientPending, CreatedAt: now.Add(-25 * time.Hour),
	}
	raw, _ := json.Marshal(profile)
	request := rpcEvent(t, agentSecret.Public(), agentSecret, clientSecret, nip46.Request{ID: "unpaired", Method: "sign_event", Params: []string{string(raw)}}, now)
	event, err := service.HandleNIP46(context.Background(), request)
	require.NoError(t, err)
	require.Contains(t, decodeBunkerResponse(t, event, agentSecret.Public(), clientSecret).Error, "not paired")

	client := store.clients[agentSecret.Public().Hex()+":"+clientSecret.Public().Hex()]
	client.Status = ClientActive
	store.clients[agentSecret.Public().Hex()+":"+clientSecret.Public().Hex()] = client
	request = rpcEvent(t, agentSecret.Public(), agentSecret, clientSecret, nip46.Request{ID: "expired", Method: "sign_event", Params: []string{string(raw)}}, now)
	event, err = service.HandleNIP46(context.Background(), request)
	require.NoError(t, err)
	require.Contains(t, decodeBunkerResponse(t, event, agentSecret.Public(), clientSecret).Error, "draft expired")
}
