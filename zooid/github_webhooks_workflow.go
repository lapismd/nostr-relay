package zooid

import (
	"context"
	"database/sql"
	"net/http"
	"strconv"
	"strings"
	"zooid/internal/forge"
)

func (policy *GitForgePolicy) processGitHubWorkflowRun(ctx context.Context, repository forge.Repository, webhook githubWebhookWorkflowRun) error {
	if webhook.ID <= 0 || !fullForgeObjectID(webhook.HeadSHA) || !strings.HasPrefix(webhook.HeadBranch, "lapis-ci/") {
		return nil
	}
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	var operationID string
	err := GetDb().QueryRowContext(queryCtx, policy.Events.Schema.Render(`
		SELECT operation_id FROM {{.Name}}__forge_ci_runs
		WHERE provider = 'github' AND connection_id = $1 AND owner = $2 AND repo = $3
		  AND (provider_run_id = $4 OR (provider_run_id = '' AND mirror_ref = $5))
		ORDER BY updated_at DESC LIMIT 1`), repository.Integration.CIConnectionID,
		repository.Coordinate.Owner.Hex(), repository.Coordinate.ID,
		strconv.FormatInt(webhook.ID, 10), "refs/heads/"+webhook.HeadBranch).Scan(&operationID)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	run, found, err := policy.loadForgeCIRun(ctx, operationID)
	if err != nil || !found {
		return err
	}
	if !strings.EqualFold(run.Commit, webhook.HeadSHA) {
		return policy.failForgeCIRun(ctx, run, &forge.Error{Status: http.StatusBadGateway, Code: "github_ci_head_mismatch"})
	}
	status := strings.ToLower(webhook.Status)
	if status == "completed" && webhook.Conclusion != "" {
		status = strings.ToLower(webhook.Conclusion)
	}
	run.ProviderRunID = strconv.FormatInt(webhook.ID, 10)
	run.ProviderURL = webhook.HTMLURL
	run.Status = status
	run.ProviderUpdatedAt = webhook.UpdatedAt
	if err := policy.updateForgeCIRun(ctx, run); err != nil {
		return err
	}
	if !terminalCIStatus(run.Status) {
		return nil
	}
	resolved, found, err := policy.ResolveIntegration(ctx, run.Coordinate)
	if err != nil || !found || resolved.CI == nil {
		return err
	}
	return policy.finishForgeCIRun(ctx, resolved, run, webhook.UpdatedAt)
}
