package githubforge

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
	"zooid/internal/forge"
)

type userTokenResolverFunc func(context.Context, nostr.PubKey) (string, error)

func (resolve userTokenResolverFunc) ResolveGitHubUserToken(ctx context.Context, pubkey nostr.PubKey) (string, error) {
	return resolve(ctx, pubkey)
}

func (userTokenResolverFunc) MarkGitHubUserReconnectRequired(context.Context, nostr.PubKey) error {
	return nil
}

type trackingUserTokenResolver struct {
	token  string
	marked nostr.PubKey
}

func (resolver *trackingUserTokenResolver) ResolveGitHubUserToken(context.Context, nostr.PubKey) (string, error) {
	return resolver.token, nil
}

func (resolver *trackingUserTokenResolver) MarkGitHubUserReconnectRequired(_ context.Context, pubkey nostr.PubKey) error {
	resolver.marked = pubkey
	return nil
}

func TestCollaborationUsesLinkedUserCredentialWithoutBotFallback(t *testing.T) {
	actor := nostr.Generate().Public()
	backend, repository := testGitHubBackend(t, func(w http.ResponseWriter, r *http.Request) {
		require.NotEqual(t, "/app/installations/99/access_tokens", r.URL.Path)
		require.Equal(t, "Bearer github-user-token", r.Header.Get("Authorization"))
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/repos/lapis/notes/issues", r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": 8001, "number": 12, "html_url": "https://github.com/lapis/notes/issues/12",
			"state": "open", "user": map[string]any{"id": 123, "login": "octo"},
		})
	})
	backend.SetUserTokenResolver(userTokenResolverFunc(func(_ context.Context, pubkey nostr.PubKey) (string, error) {
		require.Equal(t, actor, pubkey)
		return "github-user-token", nil
	}))
	result, err := backend.Execute(context.Background(), forge.CollaborationCommand{
		Kind: "create_issue", Actor: actor, CredentialMode: forge.CredentialUser, Repository: repository,
		Payload: json.RawMessage(`{"title":"Issue","body":"Body"}`),
	})
	require.NoError(t, err)
	require.Equal(t, "octo", result.ExternalAuthor)

	backend.SetUserTokenResolver(userTokenResolverFunc(func(context.Context, nostr.PubKey) (string, error) {
		return "", forge.ErrGitHubUserReconnectRequired
	}))
	_, err = backend.Execute(context.Background(), forge.CollaborationCommand{
		Kind: "create_issue", Actor: actor, CredentialMode: forge.CredentialUser, Repository: repository,
		Payload: json.RawMessage(`{"title":"Issue","body":"Body"}`),
	})
	var forgeErr *forge.Error
	require.ErrorAs(t, err, &forgeErr)
	require.Equal(t, "github_user_reconnect_required", forgeErr.Code)
}

func TestCollaborationPermissionDenialRequiresReconnectWithoutInstallationFallback(t *testing.T) {
	actor := nostr.Generate().Public()
	backend, repository := testGitHubBackend(t, func(w http.ResponseWriter, r *http.Request) {
		require.NotEqual(t, "/app/installations/99/access_tokens", r.URL.Path)
		require.Equal(t, "Bearer user-without-permission", r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusForbidden)
	})
	resolver := &trackingUserTokenResolver{token: "user-without-permission"}
	backend.SetUserTokenResolver(resolver)
	_, err := backend.Execute(context.Background(), forge.CollaborationCommand{
		Kind: "create_issue", Actor: actor, CredentialMode: forge.CredentialUser, Repository: repository,
		Payload: json.RawMessage(`{"title":"Issue","body":"Body"}`),
	})
	var forgeErr *forge.Error
	require.ErrorAs(t, err, &forgeErr)
	require.Equal(t, "github_user_reconnect_required", forgeErr.Code)
	require.Equal(t, actor, resolver.marked)
}

func TestCollaborationCreateUsesMarkerAndRecoversWithoutDuplicate(t *testing.T) {
	var created atomic.Int32
	marker := strings.Repeat("a", 32)
	backend, repository := testGitHubBackend(t, func(w http.ResponseWriter, r *http.Request) {
		if tokenResponse(t, w, r) {
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/lapis/notes/issues":
			if created.Load() == 0 {
				_ = json.NewEncoder(w).Encode([]any{})
				return
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id": 8001, "number": 12, "html_url": "https://github.com/lapis/notes/issues/12",
				"body": "Body\n\n<!-- lapis:" + marker + " -->", "state": "open",
				"user": map[string]any{"id": 55, "login": "lapis-bot"},
			}})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/lapis/notes/issues":
			created.Add(1)
			var payload map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
			require.Contains(t, payload["body"], "<!-- lapis:"+marker+" -->")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": 8001, "number": 12, "html_url": "https://github.com/lapis/notes/issues/12",
				"state": "open", "user": map[string]any{"id": 55, "login": "lapis-bot"},
			})
		default:
			http.NotFound(w, r)
		}
	})
	command := forge.CollaborationCommand{
		Kind: "create_issue", Repository: repository, CorrelationMarker: marker,
		Payload: json.RawMessage(`{"title":"Issue","body":"Body"}`),
	}
	first, err := backend.Execute(context.Background(), command)
	require.NoError(t, err)
	require.Equal(t, "8001", first.ExternalID)
	require.Equal(t, "lapis-bot", first.ExternalAuthor)
	second, err := backend.Execute(context.Background(), command)
	require.NoError(t, err)
	require.Equal(t, first.ExternalID, second.ExternalID)
	require.EqualValues(t, 1, created.Load())
}

func TestCollaborationMergePinsExpectedHead(t *testing.T) {
	head := strings.Repeat("b", 40)
	backend, repository := testGitHubBackend(t, func(w http.ResponseWriter, r *http.Request) {
		if tokenResponseWithPermissions(t, w, r, map[string]string{
			"metadata": "read", "contents": "write",
		}) {
			return
		}
		require.Equal(t, http.MethodPut, r.Method)
		require.Equal(t, "/repos/lapis/notes/pulls/7/merge", r.URL.Path)
		var payload map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		require.Equal(t, head, payload["sha"])
		require.NotContains(t, payload, "state")
		_ = json.NewEncoder(w).Encode(map[string]any{"merged": true, "sha": strings.Repeat("c", 40)})
	})
	result, err := backend.Execute(context.Background(), forge.CollaborationCommand{
		Kind: "merge", Repository: repository, ExpectedHead: head,
		Payload: json.RawMessage(`{"external_number":7,"state":"merged","merge_method":"squash"}`),
	})
	require.NoError(t, err)
	require.Equal(t, strings.Repeat("c", 40), result.MergedCommit)
}

func TestPullRequestDiffRetainsProviderFileSetAfterMerge(t *testing.T) {
	base := strings.Repeat("a", 40)
	head := strings.Repeat("b", 40)
	backend, repository := testGitHubBackend(t, func(w http.ResponseWriter, r *http.Request) {
		if tokenResponseWithPermissions(t, w, r, collaborationPermissions) {
			return
		}
		switch r.URL.Path {
		case "/repos/lapis/notes/pulls/7":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"base": map[string]any{"sha": base}, "head": map[string]any{"sha": head},
			})
		case "/repos/lapis/notes/pulls/7/files":
			require.Equal(t, "100", r.URL.Query().Get("per_page"))
			require.Equal(t, "1", r.URL.Query().Get("page"))
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"filename": "docs/merged.md", "status": "added", "additions": 3,
				"deletions": 0, "changes": 3, "patch": "@@ -0,0 +1,3 @@\n+# Merged",
			}})
		default:
			http.NotFound(w, r)
		}
	})

	diff, err := backend.PullRequestDiff(context.Background(), repository, forge.PullRequestDiffRequest{
		Number: 7, MaxFiles: 10, MaxPatchBytes: 1 << 20,
	})
	require.NoError(t, err)
	require.Equal(t, base, diff.Base)
	require.Equal(t, head, diff.Head)
	require.Equal(t, "docs/merged.md", diff.Files[0].Path)
	require.Contains(t, diff.Patch, "diff --git a/docs/merged.md b/docs/merged.md")
}

func TestCollaborationCreatesNativePullRequestReviews(t *testing.T) {
	marker := strings.Repeat("b", 32)
	actor := nostr.Generate().Public()
	actorHex := actor.Hex()
	backend, repository := testGitHubBackend(t, func(w http.ResponseWriter, r *http.Request) {
		if tokenResponse(t, w, r) {
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/lapis/notes/pulls/7/reviews":
			_ = json.NewEncoder(w).Encode([]any{})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/lapis/notes/pulls/7/reviews":
			var payload map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
			require.Equal(t, "REQUEST_CHANGES", payload["event"])
			require.Equal(t, strings.Repeat("d", 40), payload["commit_id"])
			require.Contains(t, payload["body"], "Please add coverage")
			require.Contains(t, payload["body"], "Submitted through Lapis by Nostr pubkey `"+actorHex+"`.")
			require.Contains(t, payload["body"], "<!-- lapis:"+marker+" -->")
			require.NotContains(t, payload, "external_number")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": 88, "html_url": "https://github.com/lapis/notes/pull/7#pullrequestreview-88",
				"state": "CHANGES_REQUESTED", "user": map[string]any{"id": 55, "login": "lapis-bot"},
			})
		default:
			http.NotFound(w, r)
		}
	})
	result, err := backend.Execute(context.Background(), forge.CollaborationCommand{
		Kind: "review", Actor: actor, Repository: repository, CorrelationMarker: marker,
		Payload: json.RawMessage(`{"external_number":7,"event":"REQUEST_CHANGES","commit_id":"` + strings.Repeat("d", 40) + `","body":"Please add coverage"}`),
	})
	require.NoError(t, err)
	require.Equal(t, "88", result.ExternalID)
	require.Equal(t, "lapis-bot", result.ExternalAuthor)
}

func TestCollaborationCreatesInlinePullRequestReviewComments(t *testing.T) {
	marker := strings.Repeat("e", 32)
	actor := nostr.Generate().Public()
	backend, repository := testGitHubBackend(t, func(w http.ResponseWriter, r *http.Request) {
		if tokenResponse(t, w, r) {
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/lapis/notes/pulls/7/comments":
			_ = json.NewEncoder(w).Encode([]any{})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/lapis/notes/pulls/7/comments":
			var payload map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
			require.Equal(t, strings.Repeat("f", 40), payload["commit_id"])
			require.Equal(t, "README.md", payload["path"])
			require.Equal(t, "RIGHT", payload["side"])
			require.EqualValues(t, 4, payload["line"])
			require.Contains(t, payload["body"], "Submitted through Lapis by Nostr pubkey")
			require.Contains(t, payload["body"], "<!-- lapis:"+marker+" -->")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": 89, "html_url": "https://github.com/lapis/notes/pull/7#discussion_r89",
				"user": map[string]any{"id": 55, "login": "lapis-bot"},
			})
		default:
			http.NotFound(w, r)
		}
	})
	result, err := backend.Execute(context.Background(), forge.CollaborationCommand{
		Kind: "review_comment", Actor: actor, Repository: repository, CorrelationMarker: marker,
		Payload: json.RawMessage(`{"external_number":7,"commit_id":"` + strings.Repeat("f", 40) + `","path":"README.md","line":4,"side":"RIGHT","body":"Please clarify this"}`),
	})
	require.NoError(t, err)
	require.Equal(t, "89", result.ExternalID)
}

func TestCollaborationReviewCommentReactionsUseGitHubAllowlist(t *testing.T) {
	backend, repository := testGitHubBackend(t, func(w http.ResponseWriter, r *http.Request) {
		if tokenResponse(t, w, r) {
			return
		}
		switch r.Method {
		case http.MethodPost:
			require.Equal(t, "/repos/lapis/notes/pulls/comments/44/reactions", r.URL.Path)
			var payload map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
			require.Equal(t, "heart", payload["content"])
			require.NotContains(t, payload, "external_comment_id")
			require.NotContains(t, payload, "comment_type")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": 77, "content": "heart",
				"user": map[string]any{"id": 55, "login": "lapis-bot"},
			})
		case http.MethodDelete:
			require.Equal(t, "/repos/lapis/notes/pulls/comments/44/reactions/77", r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})

	created, err := backend.Execute(context.Background(), forge.CollaborationCommand{
		Kind: "reaction_add", Repository: repository,
		Payload: json.RawMessage(`{"external_comment_id":44,"comment_type":"pull_request_review_comment","content":"heart"}`),
	})
	require.NoError(t, err)
	require.Equal(t, "77", created.ExternalID)
	require.Equal(t, "lapis-bot", created.ExternalAuthor)

	deleted, err := backend.Execute(context.Background(), forge.CollaborationCommand{
		Kind: "reaction_delete", Repository: repository,
		Payload: json.RawMessage(`{"external_comment_id":44,"external_reaction_id":77,"comment_type":"pull_request_review_comment"}`),
	})
	require.NoError(t, err)
	require.Equal(t, "77", deleted.ExternalID)

	_, err = backend.Execute(context.Background(), forge.CollaborationCommand{
		Kind: "reaction_add", Repository: repository,
		Payload: json.RawMessage(`{"external_comment_id":44,"comment_type":"pull_request_review_comment","content":"fire"}`),
	})
	var forgeErr *forge.Error
	require.ErrorAs(t, err, &forgeErr)
	require.Equal(t, "unsupported_github_reaction", forgeErr.Code)
}
