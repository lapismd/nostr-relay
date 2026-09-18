package zooid

import (
	"bytes"
	"context"
	"io"
	"net/http"

	"fiatjaf.com/nostr"
	"zooid/internal/forge"
	"zooid/internal/gitforge"
	"zooid/internal/projecttask"
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
	decision := gitforge.EvaluateProtectedRefUpdates(gitforge.ProtectedRefRequest{
		Principal:         actor.Hex(),
		Commands:          commands,
		ProtectedRef:      protection.DefaultRef,
		ProtectedPatterns: protection.Patterns,
		EmptyRepo:         !gitforge.RepoHasBranchHeads(refs),
		CanMaintain:       policy.ProjectTasks.Groups.CanMaintain(resolved.Repository.Channel, actor),
	})
	if decision.Allow {
		return 0, ""
	}
	return http.StatusForbidden, decision.Code
}

func (policy *GitForgePolicy) hostedProtection(ctx context.Context, resolved forge.ResolvedRepository, principal string) (map[string]string, gitforge.CompiledProtection) {
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
	return refs, gitforge.CompileProtection(
		gitforge.LiveDefaultRef(headName, refs),
		projecttask.AnnouncementProtectedRefTags(resolved.Repository.Announcement),
	)
}
