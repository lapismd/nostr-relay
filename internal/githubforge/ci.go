package githubforge

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"zooid/internal/forge"
)

var ciPermissions = map[string]string{"metadata": "read", "contents": "write", "actions": "write"}

type githubWorkflowRun struct {
	ID         int64     `json:"id"`
	HeadSHA    string    `json:"head_sha"`
	HeadBranch string    `json:"head_branch"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion"`
	HTMLURL    string    `json:"html_url"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func (backend *Backend) Start(ctx context.Context, request forge.CIRequest) (forge.CIRun, error) {
	if !fullObjectID(request.Commit) || !validCIIdentifier(request.OperationID) || !validWorkflow(request.Workflow) {
		return forge.CIRun{}, &forge.Error{Status: http.StatusBadRequest, Code: "invalid_ci_request"}
	}
	repository, err := ciRepository(request.Repository)
	if err != nil {
		return forge.CIRun{}, err
	}
	mirrorRef := request.MirrorRef
	if mirrorRef == "" {
		mirrorRef = "refs/heads/lapis-ci/" + request.OperationID
	}
	if mirrorRef != "refs/heads/lapis-ci/"+request.OperationID {
		return forge.CIRun{}, &forge.Error{Status: http.StatusBadRequest, Code: "invalid_ci_ref"}
	}
	if err := backend.ensureCIRef(ctx, repository, mirrorRef, strings.ToLower(request.Commit)); err != nil {
		return forge.CIRun{}, err
	}
	request.Repository, request.MirrorRef = repository, mirrorRef
	if run, err := backend.Discover(ctx, request); err != nil {
		return forge.CIRun{}, err
	} else if run.ProviderRunID != "" {
		return run, nil
	}
	payload := map[string]any{"ref": strings.TrimPrefix(mirrorRef, "refs/heads/")}
	if len(request.Inputs) != 0 {
		payload["inputs"] = request.Inputs
	}
	body, _ := json.Marshal(payload)
	response, err := backend.doRepository(ctx, repository, http.MethodPost,
		repoPath(repository)+"/actions/workflows/"+url.PathEscape(request.Workflow)+"/dispatches",
		bytes.NewReader(body), ciPermissions)
	if err != nil {
		return forge.CIRun{}, err
	}
	if response.StatusCode != http.StatusNoContent && response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusOK {
		response.Body.Close()
		return forge.CIRun{}, githubStatusError(response.StatusCode, response.Header)
	}
	if response.StatusCode != http.StatusNoContent {
		var run githubWorkflowRun
		decodeErr := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&run)
		response.Body.Close()
		if decodeErr == nil && run.ID > 0 {
			if !strings.EqualFold(run.HeadSHA, request.Commit) {
				return forge.CIRun{}, &forge.Error{Status: http.StatusBadGateway, Code: "github_ci_head_mismatch"}
			}
			return normalizedCIRun(run, mirrorRef), nil
		}
	} else {
		response.Body.Close()
	}
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(time.Duration(attempt) * 200 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return forge.CIRun{}, &forge.Error{Status: http.StatusGatewayTimeout, Code: "github_ci_discovery_cancelled", Retryable: true}
			case <-timer.C:
			}
		}
		run, err := backend.Discover(ctx, request)
		if err != nil {
			return forge.CIRun{}, err
		}
		if run.ProviderRunID != "" {
			return run, nil
		}
	}
	return forge.CIRun{Commit: strings.ToLower(request.Commit), Status: "requested", MirrorRef: mirrorRef, UpdatedAt: time.Now().UTC()}, nil
}

func (backend *Backend) Discover(ctx context.Context, request forge.CIRequest) (forge.CIRun, error) {
	repository, err := ciRepository(request.Repository)
	if err != nil {
		return forge.CIRun{}, err
	}
	mirrorRef := request.MirrorRef
	if mirrorRef == "" {
		mirrorRef = "refs/heads/lapis-ci/" + request.OperationID
	}
	query := make(url.Values)
	query.Set("branch", strings.TrimPrefix(mirrorRef, "refs/heads/"))
	query.Set("event", "workflow_dispatch")
	query.Set("per_page", "20")
	response, err := backend.doRepository(ctx, repository, http.MethodGet,
		repoPath(repository)+"/actions/workflows/"+url.PathEscape(request.Workflow)+"/runs?"+query.Encode(), nil, ciPermissions)
	if err != nil {
		return forge.CIRun{}, err
	}
	var result struct {
		WorkflowRuns []githubWorkflowRun `json:"workflow_runs"`
	}
	if err := decodeAndClose(response, &result, 8<<20); err != nil {
		return forge.CIRun{}, err
	}
	for _, run := range result.WorkflowRuns {
		if strings.EqualFold(run.HeadSHA, request.Commit) && run.HeadBranch == strings.TrimPrefix(mirrorRef, "refs/heads/") && run.ID > 0 {
			return normalizedCIRun(run, mirrorRef), nil
		}
	}
	return forge.CIRun{Commit: strings.ToLower(request.Commit), Status: "requested", MirrorRef: mirrorRef, UpdatedAt: time.Now().UTC()}, nil
}

func (backend *Backend) Status(ctx context.Context, repository forge.Repository, providerRunID string) (forge.CIRun, error) {
	repository, err := ciRepository(repository)
	if err != nil {
		return forge.CIRun{}, err
	}
	if id, err := strconv.ParseInt(providerRunID, 10, 64); err != nil || id <= 0 {
		return forge.CIRun{}, &forge.Error{Status: http.StatusBadRequest, Code: "invalid_ci_run"}
	}
	response, err := backend.doRepository(ctx, repository, http.MethodGet, repoPath(repository)+"/actions/runs/"+providerRunID, nil, ciPermissions)
	if err != nil {
		return forge.CIRun{}, err
	}
	var run githubWorkflowRun
	if err := decodeAndClose(response, &run, 4<<20); err != nil {
		return forge.CIRun{}, err
	}
	if strconv.FormatInt(run.ID, 10) != providerRunID || !fullObjectID(run.HeadSHA) {
		return forge.CIRun{}, &forge.Error{Status: http.StatusBadGateway, Code: "invalid_github_ci_run"}
	}
	return normalizedCIRun(run, ""), nil
}

func (backend *Backend) Cancel(ctx context.Context, repository forge.Repository, providerRunID string) error {
	repository, err := ciRepository(repository)
	if err != nil {
		return err
	}
	if id, err := strconv.ParseInt(providerRunID, 10, 64); err != nil || id <= 0 {
		return &forge.Error{Status: http.StatusBadRequest, Code: "invalid_ci_run"}
	}
	response, err := backend.doRepository(ctx, repository, http.MethodPost, repoPath(repository)+"/actions/runs/"+providerRunID+"/cancel", nil, ciPermissions)
	if err != nil {
		return err
	}
	response.Body.Close()
	return nil
}

func (backend *Backend) Cleanup(ctx context.Context, repository forge.Repository, mirrorRef string) error {
	repository, err := ciRepository(repository)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(mirrorRef, "refs/heads/lapis-ci/") || !validCIIdentifier(strings.TrimPrefix(mirrorRef, "refs/heads/lapis-ci/")) {
		return &forge.Error{Status: http.StatusBadRequest, Code: "invalid_ci_ref"}
	}
	response, err := backend.doRepository(ctx, repository, http.MethodDelete,
		repoPath(repository)+"/git/refs/heads/lapis-ci/"+url.PathEscape(strings.TrimPrefix(mirrorRef, "refs/heads/lapis-ci/")), nil, ciPermissions)
	if err != nil {
		if backendError, ok := err.(*forge.Error); ok && backendError.Code == "github_object_not_found" {
			return nil
		}
		return err
	}
	response.Body.Close()
	return nil
}

func (backend *Backend) MirrorTarget(ctx context.Context, repository forge.Repository) (forge.CIMirrorTarget, error) {
	repository, err := ciRepository(repository)
	if err != nil {
		return forge.CIMirrorTarget{}, err
	}
	connection, repositoryID, err := backend.connectionFor(ctx, repository)
	if err != nil {
		return forge.CIMirrorTarget{}, err
	}
	token, err := backend.tokens.installation(ctx, connection, repositoryID, ciPermissions)
	if err != nil {
		return forge.CIMirrorTarget{}, err
	}
	name, _ := repositoryName(repository)
	return forge.CIMirrorTarget{CloneURL: "https://github.com/" + name + ".git", Username: "x-access-token", Password: token}, nil
}

func (backend *Backend) ensureCIRef(ctx context.Context, repository forge.Repository, ref, commit string) error {
	path := repoPath(repository) + "/git/ref/heads/lapis-ci/" + url.PathEscape(strings.TrimPrefix(ref, "refs/heads/lapis-ci/"))
	response, err := backend.doRepository(ctx, repository, http.MethodGet, path, nil, ciPermissions)
	if err == nil {
		var found gitHubRef
		if err := decodeAndClose(response, &found, 2<<20); err != nil {
			return err
		}
		if !strings.EqualFold(found.Object.SHA, commit) {
			return &forge.Error{Status: http.StatusConflict, Code: "github_ci_ref_conflict"}
		}
		return nil
	}
	if backendError, ok := err.(*forge.Error); !ok || backendError.Code != "github_object_not_found" {
		return err
	}
	body, _ := json.Marshal(map[string]string{"ref": ref, "sha": commit})
	response, err = backend.doRepository(ctx, repository, http.MethodPost, repoPath(repository)+"/git/refs", bytes.NewReader(body), ciPermissions)
	if err != nil {
		return err
	}
	var created gitHubRef
	if err := decodeAndClose(response, &created, 2<<20); err != nil {
		return err
	}
	if created.Ref != ref || !strings.EqualFold(created.Object.SHA, commit) {
		return &forge.Error{Status: http.StatusBadGateway, Code: "github_ci_ref_mismatch"}
	}
	return nil
}

func ciRepository(repository forge.Repository) (forge.Repository, error) {
	integration := repository.Integration
	if integration.CIConnectionID == "" || integration.CIExternalRepositoryID == "" || integration.CIExternalRepository == "" {
		return forge.Repository{}, &forge.Error{Status: http.StatusConflict, Code: "github_ci_repository_unconfigured"}
	}
	integration.ConnectionID = integration.CIConnectionID
	integration.ExternalRepositoryID = integration.CIExternalRepositoryID
	integration.ExternalRepository = integration.CIExternalRepository
	repository.Integration = integration
	return repository, nil
}

func normalizedCIRun(run githubWorkflowRun, mirrorRef string) forge.CIRun {
	status := strings.ToLower(run.Status)
	if status == "completed" && run.Conclusion != "" {
		status = strings.ToLower(run.Conclusion)
	}
	return forge.CIRun{
		ProviderRunID: strconv.FormatInt(run.ID, 10), Commit: strings.ToLower(run.HeadSHA),
		Status: status, URL: run.HTMLURL, MirrorRef: mirrorRef, UpdatedAt: run.UpdatedAt,
	}
}

func validCIIdentifier(value string) bool {
	if value == "" || len(value) > 100 {
		return false
	}
	for _, character := range value {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '-' || character == '_') {
			return false
		}
	}
	return true
}

func validWorkflow(value string) bool {
	if value == "" || len(value) > 255 || strings.ContainsAny(value, "\x00\r\n/") {
		return false
	}
	for _, character := range value {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '.' || character == '_' || character == '-') {
			return false
		}
	}
	return true
}

var (
	_ forge.CIBackend             = (*Backend)(nil)
	_ forge.CIDiscoveryBackend    = (*Backend)(nil)
	_ forge.CICleanupBackend      = (*Backend)(nil)
	_ forge.CIMirrorTargetBackend = (*Backend)(nil)
)
