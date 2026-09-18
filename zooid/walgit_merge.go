package zooid

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"zooid/internal/forge"
	"zooid/internal/gitforge"
	"zooid/internal/projecttask"
)

func (policy *GitForgePolicy) advertiseCapabilities(ctx context.Context, resolved forge.ResolvedRepository, principal string) forge.Capabilities {
	capabilities := resolved.Capabilities
	if capabilities.Protection == nil || capabilities.Protection.GitHubAuthoritative {
		return capabilities
	}
	_, compiled := policy.hostedProtection(ctx, resolved, principal)
	protection := *capabilities.Protection
	protection.ProtectedRef = compiled.DefaultRef
	protection.DefaultBranch = strings.TrimPrefix(compiled.DefaultRef, "refs/heads/")
	protection.ProtectedPatterns = compiled.Patterns
	protection.PullRequestsRequired = true
	protection.MergeActors = "channel_maintainers"
	capabilities.Protection = &protection
	return capabilities
}

func (policy *GitForgePolicy) enqueueRelayMerge(ctx context.Context, actor nostr.PubKey, root nostr.Event, resolved forge.ResolvedRepository, expectedHead, method, title, message, idempotencyKey string) (forgeOperation, error) {
	payload, _ := json.Marshal(map[string]any{
		"commit_title": title, "commit_message": message, "merge_method": method, "state": "merged",
	})
	operationID, err := newOperationID("fop")
	if err != nil {
		return forgeOperation{}, err
	}
	return policy.enqueueForgeOperation(ctx, forgeOperation{
		ID: operationID, Provider: forge.BackendRelay, Coordinate: resolved.Repository.Coordinate,
		Command: "merge", ObjectType: "pull_request", Actor: actor, RootEventID: root.ID.Hex(),
		ExpectedHead: strings.ToLower(expectedHead), Payload: payload, IdempotencyKey: idempotencyKey,
	})
}

func (policy *GitForgePolicy) processRelayMerge(ctx context.Context, resolved forge.ResolvedRepository, operation forgeOperation) error {
	root, found, err := policy.eventByHexID(ctx, operation.RootEventID)
	if err != nil || !found {
		err = &forge.Error{Status: http.StatusConflict, Code: "work_item_not_found"}
		_ = policy.failForgeOperation(ctx, operation, err)
		return err
	}
	related := policy.pullRequestRelatedEvents(ctx, root.ID)
	state, err := projecttask.ReducePullRequest(root, resolved.Repository.Announcement, related, policy.ProjectTasks.repositoryAuthority(resolved.Repository.Announcement))
	if err != nil {
		_ = policy.failForgeOperation(ctx, operation, err)
		return err
	}
	refs, compiled := policy.hostedProtection(ctx, resolved, operation.Actor.Hex())
	protected := compiled.TargetRef(state.TargetBranch)
	owners := policy.loadProtectedOwners(ctx, resolved, protected)
	paths := policy.changedPullRequestPaths(ctx, resolved, state, operation.Actor.Hex())
	eligibility := projecttask.EvaluateMergeEligibility(
		state, operation.Actor, operation.ExpectedHead, compiled.DefaultRef,
		policy.ProjectTasks.repositoryAuthority(resolved.Repository.Announcement), owners, paths,
		compiled.Patterns...,
	)
	if !eligibility.OK {
		err = &forge.Error{Status: http.StatusConflict, Code: eligibility.Code}
		_ = policy.failForgeOperation(ctx, operation, err)
		return err
	}
	merger := policy.relayMerger(resolved)
	if merger == nil {
		err = &forge.Error{Status: http.StatusConflict, Code: "walgit_merge_unavailable"}
		_ = policy.failForgeOperation(ctx, operation, err)
		return err
	}
	var payload map[string]any
	_ = json.Unmarshal(operation.Payload, &payload)
	targetSHA := refs[protected]
	result, err := merger.MergeProtectedRef(ctx, resolved.Repository, forge.ProtectedRefMergeRequest{
		Principal:    gitforge.MergeCoordinatorPrincipal(resolved.Repository.Coordinate.Owner.Hex(), resolved.Repository.Coordinate.ID),
		TargetRef:    protected,
		ExpectedHead: operation.ExpectedHead,
		TargetSHA:    targetSHA,
		Method:       stringValue(payload["merge_method"]),
		Title:        stringValue(payload["commit_title"]),
		Message:      stringValue(payload["commit_message"]),
	})
	if err != nil {
		_ = policy.failForgeOperation(ctx, operation, err)
		return err
	}
	if err := policy.waitConfirmedProtectedRef(ctx, resolved.Repository.Coordinate, protected, result.MergedCommit); err != nil {
		_ = policy.failForgeOperation(ctx, operation, err)
		return err
	}
	if err := policy.storeForgeProviderResult(ctx, operation, result); err != nil {
		return err
	}
	return policy.finishForgeOperation(ctx, operation)
}

func (policy *GitForgePolicy) relayMerger(resolved forge.ResolvedRepository) forge.ProtectedRefMerger {
	if policy.RelayMerger != nil {
		return policy.RelayMerger
	}
	if merger, ok := resolved.Authority.(forge.ProtectedRefMerger); ok {
		return merger
	}
	return nil
}

func (policy *GitForgePolicy) loadProtectedOwners(ctx context.Context, resolved forge.ResolvedRepository, protected string) projecttask.OwnersDocument {
	if resolved.Content == nil {
		return projecttask.OwnersDocument{}
	}
	blob, err := resolved.Content.Blob(ctx, resolved.Repository, forge.RevisionRequest{
		Revision: protected, Path: "OWNERS.md", Principal: resolved.Repository.Coordinate.Owner.Hex(),
	})
	if err != nil || blob.Binary || blob.TooLarge {
		return projecttask.OwnersDocument{}
	}
	return projecttask.ParseOwnersDocument(blob.Contents)
}

func (policy *GitForgePolicy) changedPullRequestPaths(ctx context.Context, resolved forge.ResolvedRepository, state projecttask.PullRequestState, principal string) []string {
	if resolved.Content == nil || state.CurrentCommit == "" {
		return nil
	}
	base := state.BaseCommit
	if base == "" {
		base = state.MergeBase
	}
	if base == "" {
		return nil
	}
	diff, err := resolved.Content.Compare(ctx, resolved.Repository, forge.CompareRequest{
		Base: base, Head: state.CurrentCommit, UseMergeBase: true, Principal: principal,
		MaxFiles: 20000, MaxPatchBytes: 64 << 20,
	})
	if err != nil {
		return nil
	}
	paths := make([]string, 0, len(diff.Files))
	for _, file := range diff.Files {
		if file.Path != "" {
			paths = append(paths, file.Path)
		}
	}
	return paths
}

func (policy *GitForgePolicy) waitConfirmedProtectedRef(ctx context.Context, coordinate projecttask.RepositoryCoordinate, ref, oid string) error {
	if oid == "" {
		return &forge.Error{Status: http.StatusConflict, Code: "merge_ref_unconfirmed"}
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		current, found, err := policy.loadWalGitRef(ctx, coordinate, ref)
		if err != nil {
			return err
		}
		if found && strings.EqualFold(current, oid) {
			return nil
		}
		if time.Now().After(deadline) {
			if !found {
				return nil
			}
			return &forge.Error{Status: http.StatusConflict, Code: "merge_ref_unconfirmed", Retryable: true}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (policy *GitForgePolicy) loadWalGitRef(ctx context.Context, coordinate projecttask.RepositoryCoordinate, ref string) (string, bool, error) {
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	var oid string
	err := GetDb().QueryRowContext(queryCtx, policy.Events.Schema.Render(`
		SELECT oid FROM {{.Name}}__walgit_refs WHERE owner = $1 AND repo = $2 AND ref_name = $3`),
		coordinate.Owner.Hex(), coordinate.ID, ref).Scan(&oid)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	return oid, true, nil
}
