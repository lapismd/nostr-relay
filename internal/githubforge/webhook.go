package githubforge

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"zooid/internal/forge"
)

var subscribedWebhookEvents = map[string]struct{}{
	"installation": {}, "installation_repositories": {}, "repository": {}, "push": {},
	"issues": {}, "issue_comment": {}, "pull_request": {}, "pull_request_review": {},
	"pull_request_review_comment": {}, "workflow_run": {},
}

type WebhookConnectionLoader interface {
	GitHubWebhookConnections(context.Context) ([]Connection, error)
}

func (backend *Backend) Provider() string { return forge.BackendGitHub }

func (backend *Backend) VerifyAndDecode(ctx context.Context, headers http.Header, body []byte) ([]forge.ProviderEvent, error) {
	deliveryID := strings.TrimSpace(headers.Get("X-GitHub-Delivery"))
	eventType := strings.TrimSpace(headers.Get("X-GitHub-Event"))
	signature := strings.TrimSpace(headers.Get("X-Hub-Signature-256"))
	if deliveryID == "" || len(deliveryID) > 256 || eventType == "" || len(eventType) > 128 {
		return nil, &forge.Error{Status: http.StatusBadRequest, Code: "invalid_github_webhook"}
	}
	if _, allowed := subscribedWebhookEvents[eventType]; !allowed {
		return nil, &forge.Error{Status: http.StatusBadRequest, Code: "unsupported_github_webhook"}
	}
	provided, err := hex.DecodeString(strings.TrimPrefix(signature, "sha256="))
	if err != nil || !strings.HasPrefix(signature, "sha256=") || len(provided) != sha256.Size {
		return nil, &forge.Error{Status: http.StatusUnauthorized, Code: "invalid_github_signature"}
	}
	loader, ok := backend.connections.(WebhookConnectionLoader)
	if !ok {
		return nil, &forge.Error{Status: http.StatusServiceUnavailable, Code: "github_connection_store_unavailable", Retryable: true}
	}
	connections, err := loader.GitHubWebhookConnections(ctx)
	if err != nil {
		return nil, err
	}
	matchedID := ""
	var matchedInstallation int64
	for _, connection := range connections {
		secret, loadErr := backend.credentials.Load(connection.WebhookSecretRef)
		if loadErr != nil {
			continue
		}
		mac := hmac.New(sha256.New, secret)
		_, _ = mac.Write(body)
		if hmac.Equal(provided, mac.Sum(nil)) {
			matchedID = connection.ID
			matchedInstallation = connection.InstallationID
		}
	}
	if matchedID == "" {
		return nil, &forge.Error{Status: http.StatusUnauthorized, Code: "invalid_github_signature"}
	}
	var envelope struct {
		Action       string `json:"action"`
		Installation struct {
			ID int64 `json:"id"`
		} `json:"installation"`
		Repository struct {
			ID int64 `json:"id"`
		} `json:"repository"`
		Issue struct {
			ID int64 `json:"id"`
		} `json:"issue"`
		PullRequest struct {
			ID int64 `json:"id"`
		} `json:"pull_request"`
		Comment struct {
			ID int64 `json:"id"`
		} `json:"comment"`
		Review struct {
			ID int64 `json:"id"`
		} `json:"review"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, &forge.Error{Status: http.StatusBadRequest, Code: "invalid_github_webhook"}
	}
	if envelope.Installation.ID != 0 && envelope.Installation.ID != matchedInstallation {
		return nil, &forge.Error{Status: http.StatusUnauthorized, Code: "github_installation_mismatch"}
	}
	externalID := envelope.PullRequest.ID
	if externalID == 0 {
		externalID = envelope.Issue.ID
	}
	if envelope.Comment.ID != 0 {
		externalID = envelope.Comment.ID
	}
	if envelope.Review.ID != 0 {
		externalID = envelope.Review.ID
	}
	event := forge.ProviderEvent{
		DeliveryID: deliveryID, Provider: forge.BackendGitHub, ConnectionID: matchedID,
		InstallationID: matchedInstallation, EventType: eventType, Action: envelope.Action,
		ExternalObjectID: strconv.FormatInt(externalID, 10), Payload: append([]byte(nil), body...),
	}
	if envelope.Repository.ID > 0 {
		event.ExternalRepositoryID = strconv.FormatInt(envelope.Repository.ID, 10)
	}
	return []forge.ProviderEvent{event}, nil
}

func WebhookSignature(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

var _ forge.EventSource = (*Backend)(nil)
