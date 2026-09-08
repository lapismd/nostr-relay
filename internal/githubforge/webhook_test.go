package githubforge

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"zooid/internal/forge"
)

func TestWebhookVerificationPrecedesParsingAndBindsInstallation(t *testing.T) {
	connection := Connection{
		ID: "connection", Host: "github.com", AppID: "123", InstallationID: 99,
		CredentialRef: "env:APP_KEY", WebhookSecretRef: "env:WEBHOOK", Status: "active",
	}
	backend := &Backend{connections: staticConnectionLoader{connection}, credentials: mapCredentials{"env:WEBHOOK": []byte("secret")}}
	body := []byte(`{"action":"opened","installation":{"id":99},"repository":{"id":42},"issue":{"id":88}}`)
	headers := make(http.Header)
	headers.Set("X-GitHub-Delivery", "opaque-delivery-id")
	headers.Set("X-GitHub-Event", "issues")
	headers.Set("X-Hub-Signature-256", WebhookSignature("secret", body))
	events, err := backend.VerifyAndDecode(context.Background(), headers, body)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, "connection", events[0].ConnectionID)
	require.Equal(t, "42", events[0].ExternalRepositoryID)
	require.Equal(t, "88", events[0].ExternalObjectID)

	headers.Set("X-Hub-Signature-256", "sha256="+strings.Repeat("0", 64))
	_, err = backend.VerifyAndDecode(context.Background(), headers, []byte(`not-json`))
	var forgeError *forge.Error
	require.ErrorAs(t, err, &forgeError)
	require.Equal(t, "invalid_github_signature", forgeError.Code)

	body = []byte(`{"installation":{"id":100}}`)
	headers.Set("X-Hub-Signature-256", WebhookSignature("secret", body))
	_, err = backend.VerifyAndDecode(context.Background(), headers, body)
	require.ErrorAs(t, err, &forgeError)
	require.Equal(t, "github_installation_mismatch", forgeError.Code)
}

func TestWebhookRejectsEventsOutsideSubscription(t *testing.T) {
	connection := Connection{ID: "connection", InstallationID: 99, WebhookSecretRef: "env:WEBHOOK"}
	backend := &Backend{connections: staticConnectionLoader{connection}, credentials: mapCredentials{"env:WEBHOOK": []byte("secret")}}
	body := []byte(`{}`)
	headers := make(http.Header)
	headers.Set("X-GitHub-Delivery", "delivery")
	headers.Set("X-GitHub-Event", "fork")
	headers.Set("X-Hub-Signature-256", WebhookSignature("secret", body))
	_, err := backend.VerifyAndDecode(context.Background(), headers, body)
	var forgeError *forge.Error
	require.ErrorAs(t, err, &forgeError)
	require.Equal(t, "unsupported_github_webhook", forgeError.Code)
}
