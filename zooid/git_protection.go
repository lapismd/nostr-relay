package zooid

import (
	"bytes"
	"io"
	"net/http"

	"fiatjaf.com/nostr"
	"zooid/internal/forge"
	"zooid/internal/gitforge"
)

func (policy *GitForgePolicy) authorizeProtectedReceivePack(r *http.Request, resolved forge.ResolvedRepository, actor nostr.PubKey) (int, string) {
	if resolved.Repository.Integration.GitAuthorityBackend != forge.BackendWalGit {
		return 0, ""
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		return http.StatusBadRequest, "invalid_git_request"
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	commands, err := gitforge.ParseReceivePackCommands(body)
	if err != nil {
		return http.StatusForbidden, "protected_ref_update_denied"
	}
	refs, protection := policy.hostedProtection(r.Context(), resolved, actor.Hex())
	authority := policy.ProjectTasks.repositoryAuthority(resolved.Repository.Announcement)
	decision := gitforge.EvaluateProtectedRefUpdates(gitforge.ProtectedRefRequest{
		Principal:         actor.Hex(),
		Commands:          commands,
		ProtectedRef:      protection.DefaultRef,
		ProtectedPatterns: protection.Patterns,
		EmptyRepo:         !gitforge.RepoHasBranchHeads(refs),
		CanMaintain:       authority.Allows(actor),
		AdminBypass:       protection.AdminBypass,
		IsAdmin:           authority.Administers(actor),
	})
	if decision.Allow {
		return 0, ""
	}
	return http.StatusForbidden, decision.Code
}
