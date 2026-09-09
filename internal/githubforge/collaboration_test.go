package githubforge

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"zooid/internal/forge"
)

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
		if tokenResponse(t, w, r) {
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
