package githubforge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"zooid/internal/forge"
)

type importObject struct {
	ID          int64           `json:"id"`
	Number      int64           `json:"number"`
	State       string          `json:"state"`
	Draft       bool            `json:"draft"`
	Merged      bool            `json:"merged"`
	UpdatedAt   time.Time       `json:"updated_at"`
	PullRequest json.RawMessage `json:"pull_request"`
	Raw         json.RawMessage `json:"-"`
}

func (object *importObject) UnmarshalJSON(raw []byte) error {
	type alias importObject
	if err := json.Unmarshal(raw, (*alias)(object)); err != nil {
		return err
	}
	object.Raw = append(object.Raw[:0], raw...)
	return nil
}

func (backend *Backend) InitialImport(ctx context.Context, repository forge.Repository, request forge.ProviderImportRequest) (forge.ProviderImportResult, error) {
	request.OpenOnly = true
	if request.UpdatedSince.IsZero() {
		request.UpdatedSince = time.Now().Add(-90 * 24 * time.Hour)
	}
	return backend.collectProviderObjects(ctx, repository, request, "import")
}

func (backend *Backend) Reconcile(ctx context.Context, repository forge.Repository, request forge.ProviderImportRequest) (forge.ProviderImportResult, error) {
	request.OpenOnly = false
	if request.UpdatedSince.IsZero() {
		request.UpdatedSince = time.Now().Add(-10 * time.Minute)
	}
	return backend.collectProviderObjects(ctx, repository, request, "reconcile")
}

func (backend *Backend) collectProviderObjects(ctx context.Context, repository forge.Repository, request forge.ProviderImportRequest, prefix string) (forge.ProviderImportResult, error) {
	if request.MaximumRoots <= 0 || request.MaximumRoots > 2000 {
		request.MaximumRoots = 1000
	}
	if request.MaximumComments <= 0 || request.MaximumComments > 500 {
		request.MaximumComments = 500
	}
	connection, repositoryID, err := backend.connectionFor(ctx, repository)
	if err != nil {
		return forge.ProviderImportResult{}, err
	}
	metadata, err := backend.Repository(ctx, repository)
	if err != nil {
		return forge.ProviderImportResult{}, err
	}
	result := forge.ProviderImportResult{Events: make([]forge.ProviderEvent, 0)}
	state := "all"
	if request.OpenOnly {
		state = "open"
	}
	since := url.QueryEscape(request.UpdatedSince.UTC().Format(time.RFC3339))
	issuesPath := repoPath(repository) + "/issues?state=" + state + "&sort=updated&direction=desc&since=" + since
	issues, truncated, err := backend.listImportObjects(ctx, repository, issuesPath, request.MaximumRoots)
	if err != nil {
		return result, err
	}
	result.Truncated = result.Truncated || truncated
	for _, issue := range issues {
		if len(issue.PullRequest) != 0 && string(issue.PullRequest) != "null" {
			continue
		}
		if issue.UpdatedAt.Before(request.UpdatedSince) {
			continue
		}
		action := "opened"
		if issue.State == "closed" {
			action = "closed"
		}
		event, err := importedEvent(prefix, connection, metadata, "issues", action, issue.ID, issue.Raw, "issue")
		if err != nil {
			return result, err
		}
		result.Events = append(result.Events, event)
		result.Roots++
		comments, limited, err := backend.importIssueComments(ctx, repository, prefix, connection, metadata, issue.ID, issue.Number, request)
		if err != nil {
			return result, err
		}
		result.Events = append(result.Events, comments...)
		result.Comments += len(comments)
		result.Truncated = result.Truncated || limited
	}
	pullsPath := repoPath(repository) + "/pulls?state=" + state + "&sort=updated&direction=desc"
	pulls, truncated, err := backend.listImportObjects(ctx, repository, pullsPath, request.MaximumRoots-result.Roots)
	if err != nil {
		return result, err
	}
	result.Truncated = result.Truncated || truncated
	for _, pull := range pulls {
		if pull.UpdatedAt.Before(request.UpdatedSince) {
			continue
		}
		action := "ready_for_review"
		if pull.Draft {
			action = "converted_to_draft"
		}
		if pull.State == "closed" {
			action = "closed"
		}
		event, err := importedEvent(prefix, connection, metadata, "pull_request", action, pull.ID, pull.Raw, "pull_request")
		if err != nil {
			return result, err
		}
		result.Events = append(result.Events, event)
		result.Roots++
		comments, limited, err := backend.importPullComments(ctx, repository, prefix, connection, metadata, pull, request)
		if err != nil {
			return result, err
		}
		result.Events = append(result.Events, comments...)
		result.Comments += len(comments)
		result.Truncated = result.Truncated || limited
	}
	if result.Truncated {
		result.Incomplete = []string{"roots-or-comments"}
	}
	_ = repositoryID
	return result, nil
}

func (backend *Backend) listImportObjects(ctx context.Context, repository forge.Repository, basePath string, maximum int) ([]importObject, bool, error) {
	if maximum <= 0 {
		return nil, true, nil
	}
	objects := make([]importObject, 0, min(maximum, 100))
	for page := 1; len(objects) < maximum; page++ {
		separator := "&"
		if !containsQuery(basePath) {
			separator = "?"
		}
		path := basePath + separator + "per_page=100&page=" + strconv.Itoa(page)
		response, err := backend.doRepository(ctx, repository, http.MethodGet, path, nil, collaborationPermissions)
		if err != nil {
			return nil, false, err
		}
		var batch []importObject
		if err := decodeAndClose(response, &batch, 16<<20); err != nil {
			return nil, false, err
		}
		remaining := maximum - len(objects)
		if len(batch) > remaining {
			objects = append(objects, batch[:remaining]...)
			return objects, true, nil
		}
		objects = append(objects, batch...)
		if len(batch) < 100 {
			return objects, false, nil
		}
	}
	return objects, true, nil
}

func (backend *Backend) importIssueComments(ctx context.Context, repository forge.Repository, prefix string, connection Connection, metadata RepositoryMetadata, rootID, number int64, request forge.ProviderImportRequest) ([]forge.ProviderEvent, bool, error) {
	path := repoPath(repository) + "/issues/" + strconv.FormatInt(number, 10) + "/comments?since=" + url.QueryEscape(request.UpdatedSince.UTC().Format(time.RFC3339))
	comments, truncated, err := backend.listImportObjects(ctx, repository, path, request.MaximumComments)
	if err != nil {
		return nil, false, err
	}
	events := make([]forge.ProviderEvent, 0, len(comments))
	for _, comment := range comments {
		payload, _ := json.Marshal(map[string]any{"issue": map[string]int64{"id": rootID}, "comment": json.RawMessage(comment.Raw)})
		events = append(events, normalizedImportedEvent(prefix, connection, metadata.ID, "issue_comment", "created", comment.ID, payload))
	}
	return events, truncated, nil
}

func (backend *Backend) importPullComments(ctx context.Context, repository forge.Repository, prefix string, connection Connection, metadata RepositoryMetadata, pull importObject, request forge.ProviderImportRequest) ([]forge.ProviderEvent, bool, error) {
	remaining := request.MaximumComments
	events := make([]forge.ProviderEvent, 0)
	limited := false
	issueComments, truncated, err := backend.importIssueComments(ctx, repository, prefix, connection, metadata, pull.ID, pull.Number, forge.ProviderImportRequest{UpdatedSince: request.UpdatedSince, MaximumComments: remaining})
	if err != nil {
		return nil, false, err
	}
	events = append(events, issueComments...)
	remaining -= len(issueComments)
	limited = limited || truncated
	for _, family := range []struct {
		path      string
		eventType string
		field     string
	}{
		{repoPath(repository) + "/pulls/" + strconv.FormatInt(pull.Number, 10) + "/reviews", "pull_request_review", "review"},
		{repoPath(repository) + "/pulls/" + strconv.FormatInt(pull.Number, 10) + "/comments", "pull_request_review_comment", "comment"},
	} {
		if remaining <= 0 {
			limited = true
			break
		}
		objects, familyTruncated, err := backend.listImportObjects(ctx, repository, family.path, remaining)
		if err != nil {
			return nil, false, err
		}
		for _, object := range objects {
			payload, _ := json.Marshal(map[string]any{"pull_request": map[string]int64{"id": pull.ID}, family.field: json.RawMessage(object.Raw)})
			action := "created"
			if family.eventType == "pull_request_review" {
				action = "submitted"
			}
			events = append(events, normalizedImportedEvent(prefix, connection, metadata.ID, family.eventType, action, object.ID, payload))
		}
		remaining -= len(objects)
		limited = limited || familyTruncated
	}
	return events, limited, nil
}

func importedEvent(prefix string, connection Connection, repository RepositoryMetadata, eventType, action string, externalID int64, object json.RawMessage, field string) (forge.ProviderEvent, error) {
	payload, err := json.Marshal(map[string]any{
		"installation": map[string]int64{"id": connection.InstallationID},
		"repository":   repository, field: object,
	})
	if err != nil {
		return forge.ProviderEvent{}, err
	}
	return normalizedImportedEvent(prefix, connection, repository.ID, eventType, action, externalID, payload), nil
}

func normalizedImportedEvent(prefix string, connection Connection, repositoryID int64, eventType, action string, externalID int64, payload []byte) forge.ProviderEvent {
	digest := sha256.Sum256(payload)
	return forge.ProviderEvent{
		DeliveryID: fmt.Sprintf("%s:%s:%s:%d:%s", prefix, connection.ID, eventType, externalID, hex.EncodeToString(digest[:8])),
		Provider:   forge.BackendGitHub, ConnectionID: connection.ID, InstallationID: connection.InstallationID,
		EventType: eventType, Action: action, ExternalRepositoryID: strconv.FormatInt(repositoryID, 10),
		ExternalObjectID: strconv.FormatInt(externalID, 10), Payload: payload,
	}
}

func containsQuery(value string) bool {
	for _, character := range value {
		if character == '?' {
			return true
		}
	}
	return false
}

var _ forge.CollaborationImporter = (*Backend)(nil)
var _ forge.CollaborationReconciler = (*Backend)(nil)
