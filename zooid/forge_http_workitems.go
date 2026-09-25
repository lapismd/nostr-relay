package zooid

import (
	"context"
	"encoding/json"
	"errors"
	"fiatjaf.com/nostr"
	"net/http"
	"strings"
	"zooid/internal/forge"
	"zooid/internal/projecttask"
)

func (policy *GitForgePolicy) serveWorkItemForgeAPI(w http.ResponseWriter, r *http.Request, actor nostr.PubKey, idempotencyKey string, body []byte) {
	eventID, action, ok := splitObjectAction(strings.TrimPrefix(r.URL.Path, "/api/forge/work-items/"))
	if !ok || r.Method != http.MethodPost || (action != "comments" && action != "state") {
		http.NotFound(w, r)
		return
	}
	root, resolved, mapping, status, code := policy.resolveMappedWorkItem(r.Context(), eventID, actor, true)
	if status != 0 {
		writeGitError(w, status, code)
		return
	}
	var payload map[string]any
	if !decodeStrictJSON(body, &payload) {
		writeGitError(w, http.StatusBadRequest, "invalid_forge_command")
		return
	}
	command := "comment"
	objectType := "comment"
	if action == "comments" {
		if text := stringValue(payload["body"]); text == "" || len(text) > 64<<10 || len(payload) != 1 {
			writeGitError(w, http.StatusBadRequest, "invalid_comment_command")
			return
		}
	} else {
		command, objectType = "state", mapping.ObjectType
		state := strings.ToLower(stringValue(payload["state"]))
		allowed := state == "open" || state == "closed"
		if root.Kind == projecttask.KindPullRequest {
			allowed = allowed || state == "draft" || state == "ready"
		}
		if !allowed || len(payload) != 1 {
			writeGitError(w, http.StatusBadRequest, "invalid_state_command")
			return
		}
		payload["state"] = state
	}
	payload["external_number"] = mapping.ExternalNumber
	payload["object_type"] = mapping.ObjectType
	encoded, _ := json.Marshal(payload)
	operationID, err := newOperationID("fop")
	if err != nil {
		writeGitError(w, http.StatusInternalServerError, "operation_id_failed")
		return
	}
	operation, err := policy.enqueueForgeOperation(r.Context(), forgeOperation{
		ID: operationID, Provider: forge.BackendGitHub,
		ConnectionID: resolved.Repository.Integration.ConnectionID,
		Coordinate:   resolved.Repository.Coordinate, Command: command, ObjectType: objectType,
		Actor: actor, RootEventID: root.ID.Hex(), Payload: encoded, IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		writeGitBackendFailure(w, err)
		return
	}
	writeForgeJSON(w, http.StatusAccepted, publicForgeOperation(operation))
}

func (policy *GitForgePolicy) servePullRequestForgeAPI(w http.ResponseWriter, r *http.Request, actor nostr.PubKey, idempotencyKey string, body []byte) {
	eventID, action, ok := splitObjectAction(strings.TrimPrefix(r.URL.Path, "/api/forge/pull-requests/"))
	if !ok || r.Method != http.MethodPost || action != "merge" {
		http.NotFound(w, r)
		return
	}
	root, resolved, mapping, status, code := policy.resolveMergeWorkItem(r.Context(), eventID, actor)
	if status != 0 {
		writeGitError(w, status, code)
		return
	}
	if root.Kind != projecttask.KindPullRequest || !policy.ProjectTasks.Groups.CanMaintain(resolved.Repository.Channel, actor) {
		writeGitError(w, http.StatusForbidden, "merge_not_authorized")
		return
	}
	var request struct {
		ExpectedHead string `json:"expected_head"`
		Method       string `json:"method"`
		Title        string `json:"title"`
		Message      string `json:"message"`
	}
	if !decodeStrictJSON(body, &request) || !fullForgeObjectID(request.ExpectedHead) {
		writeGitError(w, http.StatusBadRequest, "invalid_merge_request")
		return
	}
	state, err := projecttask.ReducePullRequest(root, resolved.Repository.Announcement, policy.pullRequestRelatedEvents(r.Context(), root.ID), policy.ProjectTasks.repositoryAuthority(resolved.Repository.Announcement))
	if err != nil || !strings.EqualFold(state.CurrentCommit, request.ExpectedHead) {
		writeGitError(w, http.StatusConflict, "pull_request_head_changed")
		return
	}
	if request.Method == "" {
		request.Method = "merge"
	}
	if request.Method != "merge" && request.Method != "squash" && request.Method != "rebase" {
		writeGitError(w, http.StatusBadRequest, "invalid_merge_request")
		return
	}
	if resolved.Repository.Integration.CollaborationBackend == forge.BackendRelay &&
		resolved.Repository.Integration.GitAuthorityBackend == forge.BackendWalGit {
		_, compiled := policy.hostedProtection(r.Context(), resolved, actor.Hex())
		state = logReviewEligibilityState(state, compiled)
		protected := compiled.TargetRef(state.TargetBranch)
		eligibility := projecttask.EvaluateMergeEligibilityPolicy(
			state, actor, request.ExpectedHead, compiled.DefaultRef,
			policy.ProjectTasks.repositoryAuthority(resolved.Repository.Announcement),
			policy.loadProtectedOwners(r.Context(), resolved, protected),
			policy.changedPullRequestPaths(r.Context(), resolved, state, actor.Hex()),
			mergePolicyForRef(compiled, protected),
			compiled.Patterns,
		)
		if !eligibility.OK {
			if len(eligibility.MissingOwners) > 0 {
				writeForgeJSON(w, http.StatusConflict, map[string]any{
					"error": eligibility.Code, "missing_owners": eligibility.MissingOwners,
					"message": "OWNERS.md reviewers are still required: " + strings.Join(eligibility.MissingOwners, ", "),
				})
				return
			}
			writeGitError(w, http.StatusConflict, eligibility.Code)
			return
		}
		operation, err := policy.enqueueRelayMerge(r.Context(), actor, root, resolved, request.ExpectedHead, request.Method, request.Title, request.Message, idempotencyKey)
		if err != nil {
			writeGitBackendFailure(w, err)
			return
		}
		writeForgeJSON(w, http.StatusAccepted, publicForgeOperation(operation))
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"external_number": mapping.ExternalNumber, "object_type": mapping.ObjectType,
		"commit_title": request.Title, "commit_message": request.Message, "merge_method": request.Method,
		"state": "merged",
	})
	operationID, err := newOperationID("fop")
	if err != nil {
		writeGitError(w, http.StatusInternalServerError, "operation_id_failed")
		return
	}
	operation, err := policy.enqueueForgeOperation(r.Context(), forgeOperation{
		ID: operationID, Provider: forge.BackendGitHub,
		ConnectionID: resolved.Repository.Integration.ConnectionID,
		Coordinate:   resolved.Repository.Coordinate, Command: "merge", ObjectType: "pull_request",
		Actor: actor, RootEventID: root.ID.Hex(), ExpectedHead: strings.ToLower(request.ExpectedHead),
		Payload: payload, IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		writeGitBackendFailure(w, err)
		return
	}
	writeForgeJSON(w, http.StatusAccepted, publicForgeOperation(operation))
}

func (policy *GitForgePolicy) resolveMergeWorkItem(ctx context.Context, rawID string, actor nostr.PubKey) (nostr.Event, forge.ResolvedRepository, forgeObjectMapping, int, string) {
	id, err := nostr.IDFromHex(rawID)
	if err != nil || id.Hex() != rawID {
		return nostr.Event{}, forge.ResolvedRepository{}, forgeObjectMapping{}, http.StatusNotFound, "work_item_not_found"
	}
	root, found, err := policy.ProjectTasks.EventByID(ctx, id)
	if err != nil || !found || policy.ProjectTasks.IsTombstoned(ctx, root) || root.Kind != projecttask.KindPullRequest {
		return nostr.Event{}, forge.ResolvedRepository{}, forgeObjectMapping{}, http.StatusNotFound, "work_item_not_found"
	}
	coordinate, err := repositoryCoordinateFromEvent(root)
	if err != nil {
		return nostr.Event{}, forge.ResolvedRepository{}, forgeObjectMapping{}, http.StatusNotFound, "repository_not_found"
	}
	resolved, found, err := policy.ResolveIntegration(ctx, coordinate)
	if err != nil || !found {
		return nostr.Event{}, forge.ResolvedRepository{}, forgeObjectMapping{}, http.StatusNotFound, "repository_not_found"
	}
	if status, code := policy.authorizeWorkItem(ctx, actor, root, resolved.Repository, true); status != 0 {
		return nostr.Event{}, forge.ResolvedRepository{}, forgeObjectMapping{}, status, code
	}
	if resolved.Repository.Integration.CollaborationBackend == forge.BackendRelay &&
		resolved.Repository.Integration.GitAuthorityBackend == forge.BackendWalGit {
		return root, resolved, forgeObjectMapping{}, 0, ""
	}
	if resolved.Collaboration == nil || resolved.Repository.Integration.CollaborationBackend != forge.BackendGitHub {
		return nostr.Event{}, forge.ResolvedRepository{}, forgeObjectMapping{}, http.StatusConflict, "provider_collaboration_required"
	}
	mapping, found, err := policy.loadMappingByNostrRoot(ctx, forge.BackendGitHub, resolved.Repository.Integration.ConnectionID, root.ID.Hex())
	if err != nil || !found || mapping.ExternalNumber <= 0 {
		return nostr.Event{}, forge.ResolvedRepository{}, forgeObjectMapping{}, http.StatusConflict, "forge_mapping_missing"
	}
	return root, resolved, mapping, 0, ""
}

func (policy *GitForgePolicy) resolveMappedWorkItem(ctx context.Context, rawID string, actor nostr.PubKey, write bool) (nostr.Event, forge.ResolvedRepository, forgeObjectMapping, int, string) {
	id, err := nostr.IDFromHex(rawID)
	if err != nil || id.Hex() != rawID {
		return nostr.Event{}, forge.ResolvedRepository{}, forgeObjectMapping{}, http.StatusNotFound, "work_item_not_found"
	}
	root, found, err := policy.ProjectTasks.EventByID(ctx, id)
	if err != nil || !found || policy.ProjectTasks.IsTombstoned(ctx, root) || root.Kind != nostr.KindIssue && root.Kind != projecttask.KindPullRequest {
		return nostr.Event{}, forge.ResolvedRepository{}, forgeObjectMapping{}, http.StatusNotFound, "work_item_not_found"
	}
	coordinate, err := repositoryCoordinateFromEvent(root)
	if err != nil {
		return nostr.Event{}, forge.ResolvedRepository{}, forgeObjectMapping{}, http.StatusNotFound, "repository_not_found"
	}
	resolved, found, err := policy.ResolveIntegration(ctx, coordinate)
	if err != nil || !found || resolved.Collaboration == nil || resolved.Repository.Integration.CollaborationBackend != forge.BackendGitHub {
		return nostr.Event{}, forge.ResolvedRepository{}, forgeObjectMapping{}, http.StatusConflict, "provider_collaboration_required"
	}
	if status, code := policy.authorizeWorkItem(ctx, actor, root, resolved.Repository, write); status != 0 {
		return nostr.Event{}, forge.ResolvedRepository{}, forgeObjectMapping{}, status, code
	}
	mapping, found, err := policy.loadMappingByNostrRoot(ctx, forge.BackendGitHub, resolved.Repository.Integration.ConnectionID, root.ID.Hex())
	if err != nil || !found || mapping.ExternalNumber <= 0 {
		return nostr.Event{}, forge.ResolvedRepository{}, forgeObjectMapping{}, http.StatusConflict, "forge_mapping_missing"
	}
	return root, resolved, mapping, 0, ""
}

func (policy *GitForgePolicy) authorizeWorkItem(ctx context.Context, actor nostr.PubKey, root nostr.Event, repository forge.Repository, write bool) (int, string) {
	if status, code := policy.authorizeRepository(actor, repository, write); status != 0 {
		return status, code
	}
	scope, err := policy.ProjectTasks.resolveStored(ctx, root)
	if err != nil || !scope.Relevant {
		return http.StatusNotFound, "work_item_scope_not_found"
	}
	for _, channel := range scope.Channels {
		metadata, found := policy.Groups.GetMetadata(channel)
		if !found {
			return http.StatusNotFound, "work_item_scope_not_found"
		}
		if !policy.Groups.HasAccess(channel, actor) {
			if HasTag(metadata.Tags, "private") || HasTag(metadata.Tags, "hidden") {
				return http.StatusNotFound, "work_item_not_found"
			}
			return http.StatusForbidden, "work_item_access_denied"
		}
		if write && !policy.Groups.CanWrite(channel, actor) {
			return http.StatusForbidden, "work_item_write_denied"
		}
	}
	return 0, ""
}

func repositoryCoordinateFromEvent(event nostr.Event) (projecttask.RepositoryCoordinate, error) {
	tag := event.Tags.Find("a")
	if tag == nil || len(tag) < 2 {
		return projecttask.RepositoryCoordinate{}, errors.New("missing repository")
	}
	return projecttask.ParseRepositoryCoordinate(tag[1])
}

func splitObjectAction(value string) (string, string, bool) {
	parts := strings.Split(strings.Trim(value, "/"), "/")
	if len(parts) != 2 || len(parts[0]) != 64 || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func validGitHubRef(value string) bool {
	return value != "" && len(value) <= 1024 && !strings.ContainsAny(value, "\x00\r\n") && !strings.HasPrefix(value, "-")
}
