package zooid

import (
	"context"
	"encoding/json"
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
	compiled := gitforge.CompileRulesets(defaultRef, document, projecttask.AnnouncementProtectedRefTags(resolved.Repository.Announcement))
	return refs, compiled
}

func (policy *GitForgePolicy) hostedLiveRefs(ctx context.Context, resolved forge.ResolvedRepository, principal string) (map[string]string, string) {
	refs := map[string]string{}
	headName := ""
	if resolved.Authority != nil {
		if snapshot, err := resolved.Authority.SnapshotRefs(ctx, resolved.Repository); err == nil {
			refs = snapshot
		}
		if page, err := resolved.Authority.Refs(ctx, resolved.Repository, forge.RefsRequest{Principal: principal}); err == nil && page.Head != nil {
			headName = page.Head.Name
		}
		if page, err := resolved.Authority.Refs(ctx, resolved.Repository, forge.RefsRequest{
			Kind: "branches", Principal: principal,
		}); err == nil {
			if headName == "" && page.Head != nil {
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
		Rules:                advertisedProtectionRules(compiled.Rules),
	}
}

func advertisedProtectionRules(rules []gitforge.CompiledRule) []forge.BranchProtectionRule {
	advertised := make([]forge.BranchProtectionRule, 0, len(rules))
	for _, rule := range rules {
		targets := make([]forge.BranchProtectionTarget, 0, len(rule.Targets))
		for _, target := range rule.Targets {
			targets = append(targets, forge.BranchProtectionTarget{Type: target.Type, Pattern: target.Pattern})
		}
		bypass := make([]forge.BranchProtectionBypass, 0, len(rule.Bypass))
		for _, actor := range rule.Bypass {
			bypass = append(bypass, forge.BranchProtectionBypass{Kind: actor.Kind, Pubkey: actor.Pubkey})
		}
		advertised = append(advertised, forge.BranchProtectionRule{
			ID:                rule.ID,
			Name:              rule.Name,
			Enforcement:       rule.Enforcement,
			Targets:           targets,
			Bypass:            bypass,
			RestrictCreations: rule.RestrictCreations,
			RestrictUpdates:   rule.RestrictUpdates,
			RestrictDeletions: rule.RestrictDeletions,
			RequiredApprovals: rule.RequiredApprovals,
		})
	}
	return advertised
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
	if policy.Events == nil {
		return gitforge.DefaultProtectionDocument()
	}
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	var documentRaw, patternsRaw []byte
	var approvals int
	var adminBypass bool
	err := GetDb().QueryRowContext(queryCtx, policy.Events.Schema.Render(`
		SELECT document, protected_patterns, required_approvals, admin_bypass
		FROM {{.Name}}__repository_protection
		WHERE owner = $1 AND repo = $2`),
		coordinate.Owner.Hex(), coordinate.ID).Scan(&documentRaw, &patternsRaw, &approvals, &adminBypass)
	if err != nil {
		return gitforge.DefaultProtectionDocument()
	}
	if hasRulesKey(documentRaw) {
		parsed, parseErr := gitforge.ParseProtectionDocument(documentRaw)
		if parseErr != nil {
			return gitforge.DefaultProtectionDocument()
		}
		return parsed
	}
	legacy := map[string]any{
		"protected_patterns": json.RawMessage(patternsRaw),
		"required_approvals": approvals,
		"admin_bypass":       adminBypass,
	}
	if patternsRaw == nil {
		legacy["protected_patterns"] = []string{}
	}
	payload, err := json.Marshal(legacy)
	if err != nil {
		return gitforge.DefaultProtectionDocument()
	}
	normalized, err := gitforge.ParseProtectionDocument(payload)
	if err != nil {
		return gitforge.DefaultProtectionDocument()
	}
	return normalized
}

func hasRulesKey(raw []byte) bool {
	var peek map[string]json.RawMessage
	return json.Unmarshal(raw, &peek) == nil && peek["rules"] != nil
}

func (policy *GitForgePolicy) saveProtectionDocument(ctx context.Context, coordinate projecttask.RepositoryCoordinate, document gitforge.ProtectionDocument) error {
	payload, err := json.Marshal(document)
	if err != nil {
		return err
	}
	patterns := document.AdvertisedPatterns()
	if patterns == nil {
		patterns = []string{}
	}
	patternPayload, err := json.Marshal(patterns)
	if err != nil {
		return err
	}
	queryCtx, cancel := context.WithTimeout(ctx, dbOpTimeout)
	defer cancel()
	_, err = GetDb().ExecContext(queryCtx, policy.Events.Schema.Render(`
		INSERT INTO {{.Name}}__repository_protection
		  (owner, repo, document, protected_patterns, required_approvals, admin_bypass, updated_at)
		VALUES ($1, $2, $3::jsonb, $4::jsonb, $5, $6, NOW())
		ON CONFLICT (owner, repo) DO UPDATE SET
		  document = EXCLUDED.document,
		  protected_patterns = EXCLUDED.protected_patterns,
		  required_approvals = EXCLUDED.required_approvals,
		  admin_bypass = EXCLUDED.admin_bypass,
		  updated_at = NOW()`),
		coordinate.Owner.Hex(), coordinate.ID, payload, patternPayload, document.AdvertisedApprovals(), document.AdvertisedAdminBypass())
	return err
}

func protectionRules(rules []gitforge.CompiledRule) []gitforge.ProtectionRule {
	out := make([]gitforge.ProtectionRule, 0, len(rules))
	for _, rule := range rules {
		out = append(out, rule.ProtectionRule)
	}
	return out
}

func mergePolicyForRef(compiled gitforge.CompiledProtection, ref string) projecttask.MergePolicy {
	approvals, adminBypass := compiled.PolicyForRef(ref)
	return projecttask.MergePolicy{RequiredApprovals: approvals, AdminBypass: adminBypass}
}
