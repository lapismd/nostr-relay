package gitforge

import "strings"

const (
	MergeCoordinatorPrefix = "merge-coordinator:"
	ReplicaPrincipalPrefix = "github-replica:"
	JJSyncRefPrefix        = "refs/heads/jj-sync/"
	DefaultBootstrapRef    = "refs/heads/main"
)

type ProtectedRefDecision struct {
	Allow bool
	Code  string
}

type ProtectedRefRequest struct {
	Principal    string
	Commands     []ReceivePackCommand
	ProtectedRef string
	EmptyRepo    bool
	CanMaintain  bool
}

func NormalizeRefName(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" || ref == "HEAD" {
		return DefaultBootstrapRef
	}
	if strings.HasPrefix(ref, "refs/") {
		return ref
	}
	return "refs/heads/" + strings.TrimPrefix(ref, "heads/")
}

func IsReplicaPrincipal(principal string) bool {
	return strings.HasPrefix(principal, ReplicaPrincipalPrefix)
}

func IsMergeCoordinator(principal string) bool {
	return strings.HasPrefix(principal, MergeCoordinatorPrefix)
}

func MergeCoordinatorPrincipal(ownerHex, repoID string) string {
	return MergeCoordinatorPrefix + ownerHex + "/" + repoID
}

func IsJJSyncRef(ref string) bool {
	return strings.HasPrefix(NormalizeRefName(ref), JJSyncRefPrefix)
}

func IsBranchHead(ref string) bool {
	normalized := NormalizeRefName(ref)
	return strings.HasPrefix(normalized, "refs/heads/") && !IsJJSyncRef(normalized)
}

func LiveDefaultRef(headName string, refs map[string]string) string {
	if head := strings.TrimSpace(headName); head != "" && head != "HEAD" {
		return NormalizeRefName(head)
	}
	if symbolic, ok := refs["HEAD"]; ok && symbolic != "" && !isFullObjectID(symbolic) {
		return NormalizeRefName(symbolic)
	}
	if _, ok := refs[DefaultBootstrapRef]; ok {
		return DefaultBootstrapRef
	}
	for name := range refs {
		if IsBranchHead(name) {
			return NormalizeRefName(name)
		}
	}
	return DefaultBootstrapRef
}

func RepoHasBranchHeads(refs map[string]string) bool {
	for name := range refs {
		if IsBranchHead(name) {
			return true
		}
	}
	return false
}

func EvaluateProtectedRefUpdates(request ProtectedRefRequest) ProtectedRefDecision {
	if IsReplicaPrincipal(request.Principal) || IsMergeCoordinator(request.Principal) {
		return ProtectedRefDecision{Allow: true}
	}
	protected := NormalizeRefName(request.ProtectedRef)
	for _, command := range request.Commands {
		ref := NormalizeRefName(command.Ref)
		if IsJJSyncRef(ref) || !IsBranchHead(ref) {
			continue
		}
		creating := IsZeroOID(command.OldOID)
		deleting := IsZeroOID(command.NewOID)
		if request.EmptyRepo && creating && !deleting && request.CanMaintain {
			continue
		}
		if ref != protected {
			continue
		}
		if deleting {
			return ProtectedRefDecision{Code: "protected_ref_delete_denied"}
		}
		return ProtectedRefDecision{Code: "protected_ref_update_denied"}
	}
	return ProtectedRefDecision{Allow: true}
}
