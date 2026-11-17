package gitforge

import (
	"sort"
	"strings"
)

const (
	MergeCoordinatorPrefix = "merge-coordinator:"
	ReplicaPrincipalPrefix = "github-replica:"
	JJSyncRefPrefix        = "refs/heads/jj-sync/"
	DefaultBootstrapRef    = "refs/heads/main"
	maxProtectedPatternLen = 256
)

type ProtectedRefDecision struct {
	Allow bool
	Code  string
}

type ProtectedRefRequest struct {
	Principal            string
	Commands             []ReceivePackCommand
	ProtectedRef         string
	ProtectedPatterns    []string
	AnnouncementPatterns []string
	Rules                []ProtectionRule
	UseRulesets          bool
	EmptyRepo            bool
	CanMaintain          bool
	AdminBypass          bool
	IsAdmin              bool
}

type CompiledProtection struct {
	DefaultRef           string
	Patterns             []string
	DocumentPatterns     []string
	AnnouncementPatterns []string
	RequiredApprovals    int
	AdminBypass          bool
	UseRulesets          bool
	Rules                []CompiledRule
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
	for name := range refs {
		if NormalizeRefName(name) == DefaultBootstrapRef {
			return DefaultBootstrapRef
		}
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

func CompileProtection(defaultRef string, rawPatterns []string) CompiledProtection {
	compiled := CompiledProtection{
		DefaultRef:        NormalizeRefName(defaultRef),
		RequiredApprovals: DefaultRequiredApprovals,
	}
	seen := map[string]struct{}{compiled.DefaultRef: {}}
	for _, raw := range rawPatterns {
		pattern, ok := NormalizeProtectedPattern(raw)
		if !ok {
			continue
		}
		if _, exists := seen[pattern]; exists {
			continue
		}
		seen[pattern] = struct{}{}
		compiled.Patterns = append(compiled.Patterns, pattern)
	}
	sort.Strings(compiled.Patterns)
	return compiled
}

func NormalizeProtectedPattern(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxProtectedPatternLen || strings.Contains(raw, "**") {
		return "", false
	}
	normalized := NormalizeRefName(raw)
	wildcard := strings.HasSuffix(normalized, "/*")
	prefix := normalized
	if wildcard {
		prefix = strings.TrimSuffix(normalized, "/*")
	}
	if prefix == "refs/heads" || !strings.HasPrefix(prefix, "refs/heads/") || strings.Contains(prefix, "*") {
		return "", false
	}
	if prefix == strings.TrimSuffix(JJSyncRefPrefix, "/") || strings.HasPrefix(prefix, JJSyncRefPrefix) {
		return "", false
	}
	return normalized, true
}

func ProtectedRefMatches(ref, defaultRef string, patterns []string) bool {
	normalized := NormalizeRefName(ref)
	if normalized == NormalizeRefName(defaultRef) {
		return true
	}
	for _, pattern := range patterns {
		if RulesetPatternMatches(pattern, normalized) {
			return true
		}
	}
	return false
}

func (protection CompiledProtection) Matches(ref string) bool {
	if protection.UseRulesets {
		if NormalizeRefName(ref) == protection.DefaultRef {
			return true
		}
		return protection.AnyActiveRuleMatches(ref)
	}
	return ProtectedRefMatches(ref, protection.DefaultRef, protection.Patterns)
}

func (protection CompiledProtection) TargetRef(target string) string {
	if strings.TrimSpace(target) == "" {
		return protection.DefaultRef
	}
	normalized := NormalizeRefName(target)
	if protection.Matches(normalized) {
		return normalized
	}
	return protection.DefaultRef
}

func EvaluateProtectedRefUpdates(request ProtectedRefRequest) ProtectedRefDecision {
	if request.UseRulesets {
		return evaluateRulesetRefUpdates(request)
	}
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
		if !ProtectedRefMatches(ref, protected, request.ProtectedPatterns) {
			continue
		}
		if creating && !deleting && request.CanMaintain && (ref != protected || request.EmptyRepo) {
			continue
		}
		if deleting {
			return ProtectedRefDecision{Code: "protected_ref_delete_denied"}
		}
		if request.AdminBypass && request.IsAdmin {
			continue
		}
		return ProtectedRefDecision{Code: "protected_ref_update_denied"}
	}
	return ProtectedRefDecision{Allow: true}
}
