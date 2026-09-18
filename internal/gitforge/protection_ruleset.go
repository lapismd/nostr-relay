package gitforge

import (
	"sort"
	"strings"
)

const (
	RulesetEnforcementActive   = "active"
	RulesetEnforcementDisabled = "disabled"
	RulesetTargetDefault       = "default"
	RulesetTargetAll           = "all"
	RulesetTargetInclude       = "include"
	RulesetTargetExclude       = "exclude"
	RulesetBypassAdmin         = "admin"
	RulesetBypassMaintainer    = "maintainer"
	RulesetBypassMember        = "member"
	AllHeadsPattern            = "refs/heads/*"
	MaxProtectionRules         = 16
	MaxRulesetTargets          = 16
	MaxRulesetBypassActors     = 16
	maxRulesetNameLen          = 64
)

type RulesetTarget struct {
	Type    string `json:"type"`
	Pattern string `json:"pattern,omitempty"`
}

type RulesetBypass struct {
	Kind   string `json:"kind"`
	Pubkey string `json:"pubkey,omitempty"`
}

type ProtectionRule struct {
	ID                string          `json:"id"`
	Name              string          `json:"name"`
	Enforcement       string          `json:"enforcement"`
	Targets           []RulesetTarget `json:"targets"`
	Bypass            []RulesetBypass `json:"bypass"`
	RestrictCreations bool            `json:"restrict_creations"`
	RestrictUpdates   bool            `json:"restrict_updates"`
	RestrictDeletions bool            `json:"restrict_deletions"`
	RequiredApprovals int             `json:"required_approvals"`
}

type CompiledRule struct {
	ProtectionRule
}

func NormalizeRulesetPattern(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxProtectedPatternLen {
		return "", false
	}
	normalized := NormalizeRefName(raw)
	if normalized == AllHeadsPattern || normalized == "refs/heads/**" {
		return AllHeadsPattern, true
	}
	if strings.Contains(normalized, "**") {
		if !strings.HasSuffix(normalized, "/**") {
			return "", false
		}
		prefix := strings.TrimSuffix(normalized, "/**")
		if !strings.HasPrefix(prefix, "refs/heads/") || strings.Contains(prefix, "*") {
			return "", false
		}
		if prefix == strings.TrimSuffix(JJSyncRefPrefix, "/") || strings.HasPrefix(prefix, JJSyncRefPrefix) {
			return "", false
		}
		return normalized, true
	}
	return NormalizeProtectedPattern(raw)
}

func RulesetPatternMatches(pattern, ref string) bool {
	normalized := NormalizeRefName(ref)
	compiled, ok := NormalizeRulesetPattern(pattern)
	if !ok || !IsBranchHead(normalized) {
		return false
	}
	if compiled == AllHeadsPattern {
		return true
	}
	if compiled == normalized {
		return true
	}
	if strings.HasSuffix(compiled, "/**") {
		prefix := strings.TrimSuffix(compiled, "/**")
		return normalized == prefix || strings.HasPrefix(normalized, prefix+"/")
	}
	if strings.HasSuffix(compiled, "/*") {
		prefix := strings.TrimSuffix(compiled, "/*") + "/"
		rest := strings.TrimPrefix(normalized, prefix)
		return rest != normalized && rest != "" && !strings.Contains(rest, "/")
	}
	return false
}

func (rule ProtectionRule) Matches(ref, defaultRef string) bool {
	if rule.Enforcement != RulesetEnforcementActive {
		return false
	}
	normalized := NormalizeRefName(ref)
	if !IsBranchHead(normalized) {
		return false
	}
	included := false
	excluded := false
	for _, target := range rule.Targets {
		switch target.Type {
		case RulesetTargetDefault:
			if normalized == NormalizeRefName(defaultRef) {
				included = true
			}
		case RulesetTargetAll:
			included = true
		case RulesetTargetInclude:
			if RulesetPatternMatches(target.Pattern, normalized) {
				included = true
			}
		case RulesetTargetExclude:
			if RulesetPatternMatches(target.Pattern, normalized) {
				excluded = true
			}
		}
	}
	return included && !excluded
}

func (rule ProtectionRule) ActorBypasses(principal string, isAdmin, canMaintain bool) bool {
	for _, actor := range rule.Bypass {
		switch actor.Kind {
		case RulesetBypassAdmin:
			if isAdmin {
				return true
			}
		case RulesetBypassMaintainer:
			if canMaintain {
				return true
			}
		case RulesetBypassMember:
			if actor.Pubkey != "" && strings.EqualFold(actor.Pubkey, principal) {
				return true
			}
		}
	}
	return false
}

func CompileRulesets(defaultRef string, document ProtectionDocument, announcement []string) CompiledProtection {
	compiled := CompiledProtection{
		DefaultRef:        NormalizeRefName(defaultRef),
		RequiredApprovals: DefaultRequiredApprovals,
		UseRulesets:       true,
		Rules:             make([]CompiledRule, 0, len(document.Rules)),
	}
	seenPatterns := map[string]struct{}{}
	seenDocument := map[string]struct{}{}
	maxApprovals := 0
	for _, rule := range document.Rules {
		compiled.Rules = append(compiled.Rules, CompiledRule{ProtectionRule: rule})
		if rule.Enforcement != RulesetEnforcementActive {
			continue
		}
		if rule.RequiredApprovals > maxApprovals {
			maxApprovals = rule.RequiredApprovals
		}
		if rule.ActorBypasses("", true, false) {
			compiled.AdminBypass = true
		}
		for _, target := range rule.Targets {
			if target.Type == RulesetTargetAll {
				addCompiledPattern(&compiled.DocumentPatterns, seenDocument, AllHeadsPattern)
				addCompiledPattern(&compiled.Patterns, seenPatterns, AllHeadsPattern)
				continue
			}
			if target.Type != RulesetTargetInclude {
				continue
			}
			pattern, ok := NormalizeRulesetPattern(target.Pattern)
			if !ok {
				continue
			}
			addCompiledPattern(&compiled.DocumentPatterns, seenDocument, pattern)
			addCompiledPattern(&compiled.Patterns, seenPatterns, pattern)
		}
	}
	if maxApprovals > 0 {
		compiled.RequiredApprovals = NormalizeRequiredApprovals(maxApprovals)
	}
	for _, raw := range announcement {
		pattern, ok := NormalizeProtectedPattern(raw)
		if !ok {
			continue
		}
		addCompiledPattern(&compiled.AnnouncementPatterns, map[string]struct{}{}, pattern)
		addCompiledPattern(&compiled.Patterns, seenPatterns, pattern)
	}
	sort.Strings(compiled.Patterns)
	sort.Strings(compiled.DocumentPatterns)
	sort.Strings(compiled.AnnouncementPatterns)
	return compiled
}

func addCompiledPattern(dst *[]string, seen map[string]struct{}, pattern string) {
	if _, exists := seen[pattern]; exists {
		return
	}
	seen[pattern] = struct{}{}
	*dst = append(*dst, pattern)
}

func (protection CompiledProtection) AnyActiveRuleMatches(ref string) bool {
	for _, rule := range protection.Rules {
		if rule.Matches(ref, protection.DefaultRef) {
			return true
		}
	}
	for _, pattern := range protection.AnnouncementPatterns {
		if RulesetPatternMatches(pattern, ref) {
			return true
		}
	}
	return false
}

func (protection CompiledProtection) PolicyForRef(ref string) (int, bool) {
	approvals := 0
	adminBypass := false
	for _, rule := range protection.Rules {
		if !rule.Matches(ref, protection.DefaultRef) {
			continue
		}
		if rule.RequiredApprovals > approvals {
			approvals = rule.RequiredApprovals
		}
		if rule.ActorBypasses("", true, false) {
			adminBypass = true
		}
	}
	if approvals == 0 {
		approvals = DefaultRequiredApprovals
	}
	return NormalizeRequiredApprovals(approvals), adminBypass
}

func evaluateRulesetRefUpdates(request ProtectedRefRequest) ProtectedRefDecision {
	if IsReplicaPrincipal(request.Principal) || IsMergeCoordinator(request.Principal) {
		return ProtectedRefDecision{Allow: true}
	}
	for _, command := range request.Commands {
		ref := NormalizeRefName(command.Ref)
		if IsJJSyncRef(ref) || !IsBranchHead(ref) {
			continue
		}
		creating := IsZeroOID(command.OldOID)
		deleting := IsZeroOID(command.NewOID)
		updating := !creating && !deleting
		if err := denyRulesetCommand(request, ref, creating, updating, deleting); err != "" {
			return ProtectedRefDecision{Code: err}
		}
	}
	return ProtectedRefDecision{Allow: true}
}

func denyRulesetCommand(request ProtectedRefRequest, ref string, creating, updating, deleting bool) string {
	for _, rule := range request.Rules {
		if !rule.Matches(ref, request.ProtectedRef) || rule.ActorBypasses(request.Principal, request.IsAdmin, request.CanMaintain) {
			continue
		}
		if creating && rule.RestrictCreations {
			return "protected_ref_create_denied"
		}
		if deleting && rule.RestrictDeletions {
			return "protected_ref_delete_denied"
		}
		if updating && rule.RestrictUpdates {
			return "protected_ref_update_denied"
		}
	}
	for _, pattern := range request.AnnouncementPatterns {
		if !RulesetPatternMatches(pattern, ref) {
			continue
		}
		if deleting {
			return "protected_ref_delete_denied"
		}
		if updating {
			return "protected_ref_update_denied"
		}
	}
	return ""
}
