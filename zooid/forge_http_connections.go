package zooid

import (
	"bytes"
	"fiatjaf.com/nostr"
	"net/http"
	"strings"
	"zooid/internal/githubforge"
)

func (policy *GitForgePolicy) createGitHubConnection(w http.ResponseWriter, r *http.Request, actor nostr.PubKey, body []byte) {
	if !policy.Config.CanManage(actor) {
		writeGitError(w, http.StatusForbidden, "relay_admin_required")
		return
	}
	if policy.GitHub == nil || policy.Credentials == nil {
		writeGitError(w, http.StatusServiceUnavailable, "github_disabled")
		return
	}
	var request githubConnectionRequest
	if !decodeStrictJSON(body, &request) || request.AppID == "" || request.InstallationID <= 0 {
		writeGitError(w, http.StatusBadRequest, "invalid_github_connection")
		return
	}
	for _, reference := range []string{request.CredentialRef, request.WebhookSecretRef, request.BridgeCredentialRef} {
		if githubforge.ValidateCredentialReference(reference) != nil {
			writeGitError(w, http.StatusBadRequest, "invalid_credential_reference")
			return
		}
	}
	existing, found, err := policy.FindGitHubConnectionByInstallation(r.Context(), request.InstallationID)
	if err != nil {
		writeGitError(w, http.StatusServiceUnavailable, "connection_store_failed")
		return
	}
	if found {
		writeForgeJSON(w, http.StatusConflict, map[string]any{
			"error": "github_connection_exists", "connection": publicGitHubConnection(existing),
		})
		return
	}
	if !policy.validateBridgeCredentials(request.WebhookSecretRef, request.BridgeCredentialRef, request.BridgePubkey) {
		writeGitError(w, http.StatusConflict, "github_credentials_unavailable")
		return
	}
	id, err := newOperationID("ghc")
	if err != nil {
		writeGitError(w, http.StatusInternalServerError, "connection_id_failed")
		return
	}
	connection := githubforge.Connection{
		ID: id, Host: "github.com", AppID: request.AppID, InstallationID: request.InstallationID,
		CredentialRef: request.CredentialRef, WebhookSecretRef: request.WebhookSecretRef,
		BridgeCredentialRef: request.BridgeCredentialRef, BridgePubkey: request.BridgePubkey,
	}
	connection, err = policy.GitHub.ValidateConnection(r.Context(), connection)
	if err != nil {
		writeGitBackendFailure(w, err)
		return
	}
	if err := policy.saveGitHubConnection(r.Context(), connection); err != nil {
		writeGitError(w, http.StatusServiceUnavailable, "connection_store_failed")
		return
	}
	writeForgeJSON(w, http.StatusCreated, publicGitHubConnection(connection))
}

func (policy *GitForgePolicy) validateBridgeCredentials(webhookReference, bridgeReference, bridgePubkey string) bool {
	webhookSecret, err := policy.Credentials.Load(webhookReference)
	if err != nil || len(bytes.TrimSpace(webhookSecret)) == 0 {
		return false
	}
	bridgeSecret, err := policy.Credentials.Load(bridgeReference)
	if err != nil {
		return false
	}
	secret, err := nostr.SecretKeyFromHex(strings.TrimSpace(string(bridgeSecret)))
	if err != nil {
		return false
	}
	pubkey, err := nostr.PubKeyFromHex(bridgePubkey)
	return err == nil && secret.Public() == pubkey
}

func publicGitHubConnection(connection githubforge.Connection) map[string]any {
	value := map[string]any{
		"id": connection.ID, "provider": "github", "host": connection.Host,
		"installation_id": connection.InstallationID, "account_id": connection.AccountID,
		"account_login": connection.AccountLogin, "bridge_pubkey": connection.BridgePubkey,
		"status": connection.Status,
	}
	if connection.LastError != "" {
		value["detail"] = sanitizeGitErrorString(connection.LastError)
	}
	if !connection.UpdatedAt.IsZero() {
		value["updated_at"] = connection.UpdatedAt
	}
	return value
}

type githubConnectionRequest struct {
	AppID               string `json:"app_id"`
	InstallationID      int64  `json:"installation_id"`
	CredentialRef       string `json:"credential_ref"`
	WebhookSecretRef    string `json:"webhook_secret_ref"`
	BridgeCredentialRef string `json:"bridge_credential_ref"`
	BridgePubkey        string `json:"bridge_pubkey"`
}

func validGitHubConnectionRequest(request githubConnectionRequest) bool {
	if request.AppID == "" || request.InstallationID <= 0 {
		return false
	}
	for _, reference := range []string{request.CredentialRef, request.WebhookSecretRef, request.BridgeCredentialRef} {
		if githubforge.ValidateCredentialReference(reference) != nil {
			return false
		}
	}
	return true
}

func githubConnectionPathID(path, suffix string) (string, bool) {
	id := strings.TrimPrefix(path, "connections/github/")
	if suffix != "" {
		if !strings.HasSuffix(id, suffix) {
			return "", false
		}
		id = strings.TrimSuffix(id, suffix)
	}
	return id, id != "" && !strings.Contains(id, "/")
}

func (policy *GitForgePolicy) updateGitHubConnection(w http.ResponseWriter, r *http.Request, actor nostr.PubKey, path string, body []byte) {
	if !policy.Config.CanManage(actor) {
		writeGitError(w, http.StatusForbidden, "relay_admin_required")
		return
	}
	if policy.GitHub == nil || policy.Credentials == nil {
		writeGitError(w, http.StatusServiceUnavailable, "github_disabled")
		return
	}
	id, ok := githubConnectionPathID(path, "")
	if !ok {
		http.NotFound(w, r)
		return
	}
	if _, found, err := policy.LoadGitHubConnection(r.Context(), id); err != nil {
		writeGitError(w, http.StatusServiceUnavailable, "connection_store_failed")
		return
	} else if !found {
		http.NotFound(w, r)
		return
	}
	var request githubConnectionRequest
	if !decodeStrictJSON(body, &request) || !validGitHubConnectionRequest(request) {
		writeGitError(w, http.StatusBadRequest, "invalid_github_connection")
		return
	}
	if duplicate, found, err := policy.FindGitHubConnectionByInstallation(r.Context(), request.InstallationID); err != nil {
		writeGitError(w, http.StatusServiceUnavailable, "connection_store_failed")
		return
	} else if found && duplicate.ID != id {
		writeForgeJSON(w, http.StatusConflict, map[string]any{
			"error": "github_connection_exists", "connection": publicGitHubConnection(duplicate),
		})
		return
	}
	if !policy.validateBridgeCredentials(request.WebhookSecretRef, request.BridgeCredentialRef, request.BridgePubkey) {
		writeGitError(w, http.StatusConflict, "github_credentials_unavailable")
		return
	}
	connection := githubforge.Connection{
		ID: id, Host: "github.com", AppID: request.AppID, InstallationID: request.InstallationID,
		CredentialRef: request.CredentialRef, WebhookSecretRef: request.WebhookSecretRef,
		BridgeCredentialRef: request.BridgeCredentialRef, BridgePubkey: request.BridgePubkey,
	}
	connection, err := policy.GitHub.ValidateConnection(r.Context(), connection)
	if err != nil {
		writeGitBackendFailure(w, err)
		return
	}
	if err := policy.saveGitHubConnection(r.Context(), connection); err != nil {
		writeGitError(w, http.StatusServiceUnavailable, "connection_store_failed")
		return
	}
	writeForgeJSON(w, http.StatusOK, publicGitHubConnection(connection))
}

func (policy *GitForgePolicy) revalidateGitHubConnection(w http.ResponseWriter, r *http.Request, actor nostr.PubKey, path string) {
	if !policy.Config.CanManage(actor) {
		writeGitError(w, http.StatusForbidden, "relay_admin_required")
		return
	}
	if policy.GitHub == nil || policy.Credentials == nil {
		writeGitError(w, http.StatusServiceUnavailable, "github_disabled")
		return
	}
	id, ok := githubConnectionPathID(path, "/revalidate")
	if !ok {
		http.NotFound(w, r)
		return
	}
	connection, found, err := policy.LoadGitHubConnection(r.Context(), id)
	if err != nil {
		writeGitError(w, http.StatusServiceUnavailable, "connection_store_failed")
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	if !policy.validateBridgeCredentials(connection.WebhookSecretRef, connection.BridgeCredentialRef, connection.BridgePubkey) {
		_ = policy.updateGitHubConnectionStatus(r.Context(), id, "error", "github_credentials_unavailable")
		writeGitError(w, http.StatusConflict, "github_credentials_unavailable")
		return
	}
	validated, err := policy.GitHub.ValidateConnection(r.Context(), connection)
	if err != nil {
		_ = policy.updateGitHubConnectionStatus(r.Context(), id, "error", err.Error())
		writeGitBackendFailure(w, err)
		return
	}
	if err := policy.saveGitHubConnection(r.Context(), validated); err != nil {
		writeGitError(w, http.StatusServiceUnavailable, "connection_store_failed")
		return
	}
	writeForgeJSON(w, http.StatusOK, publicGitHubConnection(validated))
}

func (policy *GitForgePolicy) listGitHubConnections(w http.ResponseWriter, r *http.Request, actor nostr.PubKey) {
	if !policy.Config.CanManage(actor) {
		writeGitError(w, http.StatusForbidden, "relay_admin_required")
		return
	}
	if policy.GitHub == nil {
		writeGitError(w, http.StatusServiceUnavailable, "github_disabled")
		return
	}
	connections, err := policy.ListGitHubConnections(r.Context())
	if err != nil {
		writeGitError(w, http.StatusServiceUnavailable, "connection_store_failed")
		return
	}
	public := make([]map[string]any, 0, len(connections))
	for _, connection := range connections {
		public = append(public, publicGitHubConnection(connection))
	}
	writeForgeJSON(w, http.StatusOK, map[string]any{"connections": public})
}

func (policy *GitForgePolicy) listGitHubRepositories(w http.ResponseWriter, r *http.Request, actor nostr.PubKey, path string) {
	if !policy.Config.CanManage(actor) {
		writeGitError(w, http.StatusForbidden, "relay_admin_required")
		return
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, "connections/github/"), "/repositories")
	if id == "" || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}
	connection, found, err := policy.LoadGitHubConnection(r.Context(), id)
	if err != nil {
		writeGitError(w, http.StatusServiceUnavailable, "connection_store_failed")
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	repositories, err := policy.GitHub.ListRepositories(r.Context(), connection)
	if err != nil {
		writeGitBackendFailure(w, err)
		return
	}
	writeForgeJSON(w, http.StatusOK, map[string]any{"repositories": repositories})
}
