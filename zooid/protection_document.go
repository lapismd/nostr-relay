package zooid

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"fiatjaf.com/nostr"
	"zooid/internal/forge"
	"zooid/internal/gitforge"
	"zooid/internal/projecttask"
)

func (policy *GitForgePolicy) hostedProtection(ctx context.Context, resolved forge.ResolvedRepository, principal string) (map[string]string, gitforge.CompiledProtection) {
	refs, defaultRef := policy.hostedLiveRefs(ctx, resolved, principal)
	document := policy.loadProtectionDocument(ctx, resolved.Repository.Coordinate)
	compiled := gitforge.CompileProtection(defaultRef, append(append([]string{}, document.Patterns...), projecttask.AnnouncementProtectedRefTags(resolved.Repository.Announcement)...))
	compiled.DocumentPatterns = gitforge.CompileProtection(defaultRef, document.Patterns).Patterns
	compiled.RequiredApprovals = gitforge.NormalizeRequiredApprovals(document.RequiredApprovals)
	compiled.AdminBypass = document.AdminBypass
	return refs, compiled
}

func (policy *GitForgePolicy) hostedLiveRefs(ctx context.Context, resolved forge.ResolvedRepository, principal string) (map[string]string, string) {
	refs := map[string]string{}
	headName := ""
	if resolved.Authority != nil {
		if snapshot, err := resolved.Authority.SnapshotRefs(ctx, resolved.Repository); err == nil {
			refs = snapshot
		}
		if page, err := resolved.Authority.Refs(ctx, resolved.Repository, forge.RefsRequest{
			Kind: "branches", Principal: principal,
		}); err == nil {
			if page.Head != nil {
				headName = page.Head.Name
			}
			for _, ref := range page.Refs {
				refs[ref.Name] = ref.SHA
			}
		}
	}
	return refs, gitforge.LiveDefaultRef(headName, refs)
}

func (policy *GitForgePolicy) advertisedProtection(ctx context.Context, resolved forge.ResolvedRepository, principal string) forge.BranchProtection {
	_, compiled := policy.hostedProtection(ctx, resolved, principal)
	return forge.BranchProtection{
		DefaultBranch:        trimDefaultBranch(compiled.DefaultRef),
		ProtectedRef:         compiled.DefaultRef,
		ProtectedPatterns:    compiled.Patterns,
		DocumentPatterns:     compiled.DocumentPatterns,
		RequiredApprovals:    compiled.RequiredApprovals,
		AdminBypass:          compiled.AdminBypass,
		PullRequestsRequired: true,
		MergeActors:          "channel_maintainers",
	}
}

func trimDefaultBranch(ref string) string {
	return strings.TrimPrefix(gitforge.NormalizeRefName(ref), "refs/heads/")
}

func (policy *GitForgePolicy) serveRepositoryProtection(w http.ResponseWriter, r *http.Request, actor nostr.PubKey, resolved forge.ResolvedRepository) {
	if status, code := policy.authorizeRepository(actor, resolved.Repository, false); status != 0 {
		writeGitError(w, status, code)
		return
	}
	if resolved.Repository.Integration.GitAuthorityBackend != forge.BackendWalGit {
		writeForgeJSON(w, http.StatusOK, map[string]any{
			"repository": resolved.Repository.Coordinate.String(),
			"protection": forge.BranchProtection{GitHubAuthoritative: true, MergeActors: "github"},
		})
		return
	}
	writeForgeJSON(w, http.StatusOK, map[string]any{
		"repository": resolved.Repository.Coordinate.String(),
		"protection": policy.advertisedProtection(r.Context(), resolved, actor.Hex()),
	})
}

func (policy *GitForgePolicy) updateRepositoryProtection(w http.ResponseWriter, r *http.Request, actor nostr.PubKey, resolved forge.ResolvedRepository, body []byte) {
	if status, code := policy.authorizeRepository(actor, resolved.Repository, true); status != 0 {
		writeGitError(w, status, code)
		return
	}
	if !policy.ProjectTasks.Groups.CanMaintain(resolved.Repository.Channel, actor) {
		writeGitError(w, http.StatusForbidden, "maintainer_required")
		return
	}
	if resolved.Repository.Integration.GitAuthorityBackend != forge.BackendWalGit {
		writeGitError(w, http.StatusConflict, "github_authoritative")
		return
	}
	document, err := gitforge.ParseProtectionDocument(body)
	if err != nil {
		writeGitError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := policy.saveProtectionDocument(r.Context(), resolved.Repository.Coordinate, document); err != nil {
		writeGitError(w, http.StatusServiceUnavailable, "protection_store_failed")
		return
	}
	writeForgeJSON(w, http.StatusOK, map[string]any{
		"repository": resolved.Repository.Coordinate.String(),
		"protection": policy.advertisedProtection(r.Context(), resolved, actor.Hex()),
	})
}

func (policy *GitForgePolicy) loadProtectionDocument(ctx context.Context, coordinate projecttask.RepositoryCoordinate) gitforge.ProtectionDocument {
	document := gitforge.DefaultProtectionDocument()
	if policy.Events == nil {
		return document
	}
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	var raw []byte
	err := GetDb().QueryRowContext(queryCtx, policy.Events.Schema.Render(`
		SELECT protected_patterns, required_approvals, admin_bypass
		FROM {{.Name}}__repository_protection
		WHERE owner = $1 AND repo = $2`),
		coordinate.Owner.Hex(), coordinate.ID).Scan(&raw, &document.RequiredApprovals, &document.AdminBypass)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return gitforge.DefaultProtectionDocument()
		}
		return document
	}
	if err := json.Unmarshal(raw, &document.Patterns); err != nil {
		return gitforge.DefaultProtectionDocument()
	}
	normalized, err := gitforge.NormalizeProtectionDocument(document)
	if err != nil {
		return gitforge.DefaultProtectionDocument()
	}
	return normalized
}

func (policy *GitForgePolicy) saveProtectionDocument(ctx context.Context, coordinate projecttask.RepositoryCoordinate, document gitforge.ProtectionDocument) error {
	patterns := document.Patterns
	if patterns == nil {
		patterns = []string{}
	}
	payload, err := json.Marshal(patterns)
	if err != nil {
		return err
	}
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	_, err = GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
		INSERT INTO {{.Name}}__repository_protection
		  (owner, repo, protected_patterns, required_approvals, admin_bypass, updated_at)
		VALUES ($1, $2, $3::jsonb, $4, $5, NOW())
		ON CONFLICT (owner, repo) DO UPDATE SET
		  protected_patterns = EXCLUDED.protected_patterns,
		  required_approvals = EXCLUDED.required_approvals,
		  admin_bypass = EXCLUDED.admin_bypass,
		  updated_at = NOW()`),
		coordinate.Owner.Hex(), coordinate.ID, payload, document.RequiredApprovals, document.AdminBypass)
	return err
}
