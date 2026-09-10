package githubforge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"zooid/internal/forge"
)

var collaborationPermissions = map[string]string{
	"metadata": "read", "contents": "read", "issues": "write", "pull_requests": "write",
}

var mergePermissions = map[string]string{
	"metadata": "read", "contents": "write",
}

func (backend *Backend) PullRequestDiff(ctx context.Context, repository forge.Repository, request forge.PullRequestDiffRequest) (forge.Diff, error) {
	if request.Number <= 0 || request.MaxFiles <= 0 || request.MaxPatchBytes <= 0 {
		return forge.Diff{}, &forge.Error{Status: http.StatusBadRequest, Code: "invalid_pull_request_diff"}
	}
	path := repoPath(repository) + "/pulls/" + strconv.FormatInt(request.Number, 10)
	response, err := backend.getRepository(ctx, repository, path, collaborationPermissions)
	if err != nil {
		return forge.Diff{}, err
	}
	var pull struct {
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
		Base struct {
			SHA string `json:"sha"`
		} `json:"base"`
	}
	if err := decodeAndClose(response, &pull, 4<<20); err != nil {
		return forge.Diff{}, err
	}
	if !fullObjectID(pull.Base.SHA) || !fullObjectID(pull.Head.SHA) {
		return forge.Diff{}, &forge.Error{Status: http.StatusBadGateway, Code: "invalid_github_response"}
	}

	files := make([]forge.DiffFile, 0, min(request.MaxFiles, 100))
	var patch strings.Builder
	for page := 1; ; page++ {
		response, err = backend.getRepository(ctx, repository, path+"/files?per_page=100&page="+strconv.Itoa(page), collaborationPermissions)
		if err != nil {
			return forge.Diff{}, err
		}
		var pageFiles []gitHubFile
		if err := decodeAndClose(response, &pageFiles, maxGitHubResponse); err != nil {
			return forge.Diff{}, err
		}
		if len(files)+len(pageFiles) > request.MaxFiles {
			return forge.Diff{}, &forge.Error{Status: http.StatusRequestEntityTooLarge, Code: "diff_too_large"}
		}
		for _, file := range pageFiles {
			files = append(files, forge.DiffFile{
				Path: file.Filename, OldPath: file.PreviousFilename, Status: normalizeFileStatus(file.Status),
				Additions: file.Additions, Deletions: file.Deletions, Binary: file.Patch == "" && file.Changes > 0,
			})
			appendPatch(&patch, file)
			if int64(patch.Len()) > request.MaxPatchBytes {
				return forge.Diff{}, &forge.Error{Status: http.StatusRequestEntityTooLarge, Code: "diff_too_large"}
			}
		}
		if len(pageFiles) < 100 {
			break
		}
	}
	limits, _ := backend.ContentCapabilities(ctx, repository)
	result := forge.Diff{
		Base: pull.Base.SHA, Head: pull.Head.SHA, Files: files, Patch: patch.String(),
		ResultMetadata: githubResultMetadata(),
	}
	result.CapabilityLimits = &limits.Limits
	return result, nil
}

func (backend *Backend) Execute(ctx context.Context, command forge.CollaborationCommand) (forge.CollaborationResult, error) {
	var payload map[string]any
	if err := json.Unmarshal(command.Payload, &payload); err != nil {
		return forge.CollaborationResult{}, &forge.Error{Status: http.StatusBadRequest, Code: "invalid_forge_command"}
	}
	if command.CorrelationMarker != "" && correlatableCollaborationCommand(command.Kind) {
		body, _ := payload["body"].(string)
		if command.Kind == "review" || command.Kind == "review_comment" {
			body = appendLapisAttribution(body, command.Actor.Hex())
		}
		payload["body"] = strings.TrimRight(body, "\n") + "\n\n<!-- lapis:" + command.CorrelationMarker + " -->"
	}
	externalNumber, _ := numberValue(payload["external_number"])
	externalCommentID, _ := numberValue(payload["external_comment_id"])
	externalReactionID, _ := numberValue(payload["external_reaction_id"])
	objectType, _ := payload["object_type"].(string)
	commentType, _ := payload["comment_type"].(string)
	state, _ := payload["state"].(string)
	delete(payload, "external_number")
	delete(payload, "external_comment_id")
	delete(payload, "external_reaction_id")
	delete(payload, "head_sha")
	delete(payload, "object_type")
	delete(payload, "comment_type")

	method, path := "", ""
	switch command.Kind {
	case "create_issue":
		method, path = http.MethodPost, repoPath(command.Repository)+"/issues"
	case "create_pull_request":
		method, path = http.MethodPost, repoPath(command.Repository)+"/pulls"
	case "comment":
		if externalNumber <= 0 {
			return forge.CollaborationResult{}, &forge.Error{Status: http.StatusConflict, Code: "forge_mapping_missing"}
		}
		method, path = http.MethodPost, repoPath(command.Repository)+"/issues/"+strconv.FormatInt(externalNumber, 10)+"/comments"
	case "review":
		if externalNumber <= 0 {
			return forge.CollaborationResult{}, &forge.Error{Status: http.StatusConflict, Code: "forge_mapping_missing"}
		}
		method, path = http.MethodPost, repoPath(command.Repository)+"/pulls/"+strconv.FormatInt(externalNumber, 10)+"/reviews"
	case "review_comment":
		if externalNumber <= 0 {
			return forge.CollaborationResult{}, &forge.Error{Status: http.StatusConflict, Code: "forge_mapping_missing"}
		}
		method, path = http.MethodPost, repoPath(command.Repository)+"/pulls/"+strconv.FormatInt(externalNumber, 10)+"/comments"
	case "state":
		if externalNumber <= 0 {
			return forge.CollaborationResult{}, &forge.Error{Status: http.StatusConflict, Code: "forge_mapping_missing"}
		}
		if objectType == "pull_request" && state == "draft" {
			method, path = http.MethodPost, repoPath(command.Repository)+"/pulls/"+strconv.FormatInt(externalNumber, 10)+"/convert-to-draft"
			payload = map[string]any{}
		} else if objectType == "pull_request" && state == "ready" {
			method, path = http.MethodPost, repoPath(command.Repository)+"/pulls/"+strconv.FormatInt(externalNumber, 10)+"/ready-for-review"
			payload = map[string]any{}
		} else {
			method, path = http.MethodPatch, repoPath(command.Repository)+"/issues/"+strconv.FormatInt(externalNumber, 10)
		}
	case "merge":
		if externalNumber <= 0 || command.ExpectedHead == "" {
			return forge.CollaborationResult{}, &forge.Error{Status: http.StatusConflict, Code: "invalid_merge_request"}
		}
		payload["sha"] = command.ExpectedHead
		delete(payload, "state")
		method, path = http.MethodPut, repoPath(command.Repository)+"/pulls/"+strconv.FormatInt(externalNumber, 10)+"/merge"
	case "reaction_add", "reaction_delete":
		if externalCommentID <= 0 {
			return forge.CollaborationResult{}, &forge.Error{Status: http.StatusConflict, Code: "forge_mapping_missing"}
		}
		reactionPath := ""
		switch commentType {
		case "issue_comment":
			reactionPath = repoPath(command.Repository) + "/issues/comments/" + strconv.FormatInt(externalCommentID, 10) + "/reactions"
		case "pull_request_review_comment":
			reactionPath = repoPath(command.Repository) + "/pulls/comments/" + strconv.FormatInt(externalCommentID, 10) + "/reactions"
		default:
			return forge.CollaborationResult{}, &forge.Error{Status: http.StatusConflict, Code: "unsupported_github_comment_type"}
		}
		if command.Kind == "reaction_add" {
			content, _ := payload["content"].(string)
			if !isGitHubReactionContent(content) {
				return forge.CollaborationResult{}, &forge.Error{Status: http.StatusBadRequest, Code: "unsupported_github_reaction"}
			}
			method, path = http.MethodPost, reactionPath
			payload = map[string]any{"content": content}
		} else {
			if externalReactionID <= 0 {
				return forge.CollaborationResult{}, &forge.Error{Status: http.StatusConflict, Code: "forge_mapping_missing"}
			}
			method, path = http.MethodDelete, reactionPath+"/"+strconv.FormatInt(externalReactionID, 10)
			payload = map[string]any{}
		}
	default:
		return forge.CollaborationResult{}, &forge.Error{Status: http.StatusBadRequest, Code: "unsupported_forge_command"}
	}
	if command.CorrelationMarker != "" && correlatableCollaborationCommand(command.Kind) {
		if recovered, found, err := backend.recoverCorrelated(ctx, command.Repository, command.Kind, externalNumber, command.CorrelationMarker); err != nil {
			return forge.CollaborationResult{}, err
		} else if found {
			return recovered, nil
		}
	}
	body, _ := json.Marshal(payload)
	permissions := collaborationPermissions
	if command.Kind == "merge" {
		permissions = mergePermissions
	}
	response, err := backend.doRepository(ctx, command.Repository, method, path, bytes.NewReader(body), permissions)
	if err != nil {
		return forge.CollaborationResult{}, err
	}
	defer response.Body.Close()
	if command.Kind == "reaction_delete" && response.StatusCode == http.StatusNoContent {
		return forge.CollaborationResult{ExternalID: strconv.FormatInt(externalReactionID, 10)}, nil
	}
	var result struct {
		ID        int64  `json:"id"`
		Number    int64  `json:"number"`
		HTMLURL   string `json:"html_url"`
		State     string `json:"state"`
		Merged    bool   `json:"merged"`
		Message   string `json:"message"`
		CommitSHA string `json:"sha"`
		Draft     bool   `json:"draft"`
		User      struct {
			ID    int64  `json:"id"`
			Login string `json:"login"`
		} `json:"user"`
		Head struct {
			SHA string `json:"sha"`
			Ref string `json:"ref"`
		} `json:"head"`
		Base struct {
			SHA string `json:"sha"`
			Ref string `json:"ref"`
		} `json:"base"`
	}
	if err := decodeGitHub(response, &result, 4<<20); err != nil {
		return forge.CollaborationResult{}, err
	}
	if command.Kind == "merge" {
		if !result.Merged || !fullObjectID(result.CommitSHA) {
			return forge.CollaborationResult{}, &forge.Error{Status: http.StatusConflict, Code: "github_merge_rejected"}
		}
		return forge.CollaborationResult{ExternalID: result.CommitSHA, ExternalNumber: externalNumber, State: "merged", MergedCommit: result.CommitSHA}, nil
	}
	if command.Kind == "state" && (state == "draft" || state == "ready") {
		if state == "draft" && !result.Draft || state == "ready" && result.Draft {
			return forge.CollaborationResult{}, &forge.Error{Status: http.StatusConflict, Code: "github_state_rejected"}
		}
		return forge.CollaborationResult{
			ExternalID: strconv.FormatInt(result.ID, 10), ExternalNumber: externalNumber,
			ExternalURL: result.HTMLURL, State: state,
		}, nil
	}
	if result.ID <= 0 {
		return forge.CollaborationResult{}, &forge.Error{Status: http.StatusBadGateway, Code: "invalid_github_response"}
	}
	return forge.CollaborationResult{
		ExternalID: strconv.FormatInt(result.ID, 10), ExternalNumber: result.Number,
		ExternalURL: result.HTMLURL, State: result.State, HeadSHA: result.Head.SHA,
		HeadRef: result.Head.Ref, BaseSHA: result.Base.SHA, BaseRef: result.Base.Ref,
		ExternalAuthor: result.User.Login, ExternalAuthorID: strconv.FormatInt(result.User.ID, 10),
	}, nil
}

func (backend *Backend) recoverCorrelated(ctx context.Context, repository forge.Repository, kind string, externalNumber int64, marker string) (forge.CollaborationResult, bool, error) {
	path := repoPath(repository) + "/issues?state=all&sort=updated&direction=desc&per_page=100"
	if kind == "comment" || kind == "review" || kind == "review_comment" {
		if externalNumber <= 0 {
			return forge.CollaborationResult{}, false, nil
		}
		switch kind {
		case "comment":
			path = repoPath(repository) + "/issues/" + strconv.FormatInt(externalNumber, 10) + "/comments?per_page=100"
		case "review":
			path = repoPath(repository) + "/pulls/" + strconv.FormatInt(externalNumber, 10) + "/reviews?per_page=100"
		case "review_comment":
			path = repoPath(repository) + "/pulls/" + strconv.FormatInt(externalNumber, 10) + "/comments?per_page=100"
		}
	}
	response, err := backend.doRepository(ctx, repository, http.MethodGet, path, nil, collaborationPermissions)
	if err != nil {
		return forge.CollaborationResult{}, false, err
	}
	defer response.Body.Close()
	var objects []struct {
		ID      int64     `json:"id"`
		Number  int64     `json:"number"`
		HTMLURL string    `json:"html_url"`
		Body    string    `json:"body"`
		State   string    `json:"state"`
		Pull    *struct{} `json:"pull_request"`
		User    struct {
			ID    int64  `json:"id"`
			Login string `json:"login"`
		} `json:"user"`
	}
	if err := decodeGitHub(response, &objects, 8<<20); err != nil {
		return forge.CollaborationResult{}, false, err
	}
	wanted := "<!-- lapis:" + marker + " -->"
	for _, object := range objects {
		if !strings.Contains(object.Body, wanted) {
			continue
		}
		if kind == "create_issue" && object.Pull != nil || kind == "create_pull_request" && object.Pull == nil {
			continue
		}
		result := forge.CollaborationResult{
			ExternalID: strconv.FormatInt(object.ID, 10), ExternalNumber: object.Number,
			ExternalURL: object.HTMLURL, State: object.State,
			ExternalAuthor: object.User.Login, ExternalAuthorID: strconv.FormatInt(object.User.ID, 10),
		}
		if kind == "create_pull_request" {
			response, err := backend.doRepository(ctx, repository, http.MethodGet, repoPath(repository)+"/pulls/"+strconv.FormatInt(object.Number, 10), nil, collaborationPermissions)
			if err != nil {
				return forge.CollaborationResult{}, false, err
			}
			var pull struct {
				Head struct {
					SHA string `json:"sha"`
					Ref string `json:"ref"`
				} `json:"head"`
				Base struct {
					SHA string `json:"sha"`
					Ref string `json:"ref"`
				} `json:"base"`
			}
			if err := decodeAndClose(response, &pull, 4<<20); err != nil {
				return forge.CollaborationResult{}, false, err
			}
			result.HeadSHA, result.HeadRef = pull.Head.SHA, pull.Head.Ref
			result.BaseSHA, result.BaseRef = pull.Base.SHA, pull.Base.Ref
		}
		return result, true, nil
	}
	return forge.CollaborationResult{}, false, nil
}

func correlatableCollaborationCommand(kind string) bool {
	switch kind {
	case "create_issue", "create_pull_request", "comment", "review", "review_comment":
		return true
	default:
		return false
	}
}

func appendLapisAttribution(body, actor string) string {
	body = strings.TrimSpace(body)
	attribution := "Submitted through Lapis by Nostr pubkey `" + actor + "`."
	if body == "" {
		return attribution
	}
	return body + "\n\n---\n" + attribution
}

func numberValue(value any) (int64, error) {
	switch typed := value.(type) {
	case float64:
		return int64(typed), nil
	case json.Number:
		return typed.Int64()
	case string:
		return strconv.ParseInt(typed, 10, 64)
	default:
		return 0, fmt.Errorf("not a number")
	}
}

var _ forge.CollaborationBackend = (*Backend)(nil)
var _ forge.PullRequestDiffBackend = (*Backend)(nil)
