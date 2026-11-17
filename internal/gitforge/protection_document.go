package gitforge

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
)

const (
	MaxProtectedPatterns      = 16
	MaxRequiredApprovals      = 16
	DefaultRequiredApprovals  = 1
	ProtectionDocumentVersion = 1
	LegacyDefaultRuleID       = "default"
	LegacyDefaultRuleName     = "Default"
)

var (
	ErrInvalidProtectionDocument = errors.New("invalid_protection_document")
	ErrInvalidProtectionPattern  = errors.New("invalid_protection_pattern")
)

type ProtectionDocument struct {
	Version int              `json:"version,omitempty"`
	Rules   []ProtectionRule `json:"rules"`
}

type legacyProtectionDocument struct {
	Patterns          []string `json:"protected_patterns"`
	RequiredApprovals int      `json:"required_approvals"`
	AdminBypass       bool     `json:"admin_bypass"`
}

func NormalizeRequiredApprovals(value int) int {
	if value <= 0 {
		return DefaultRequiredApprovals
	}
	if value > MaxRequiredApprovals {
		return MaxRequiredApprovals
	}
	return value
}

func ParseProtectionDocument(body []byte) (ProtectionDocument, error) {
	var peek map[string]json.RawMessage
	if err := json.Unmarshal(body, &peek); err != nil {
		return ProtectionDocument{}, ErrInvalidProtectionDocument
	}
	if _, ok := peek["rules"]; ok {
		return parseRulesetProtectionDocument(body)
	}
	legacy, err := parseLegacyProtectionDocument(body)
	if err != nil {
		return ProtectionDocument{}, err
	}
	return migrateLegacyProtectionDocument(legacy), nil
}

func parseRulesetProtectionDocument(body []byte) (ProtectionDocument, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var document ProtectionDocument
	if err := decoder.Decode(&document); err != nil {
		return ProtectionDocument{}, ErrInvalidProtectionDocument
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return ProtectionDocument{}, ErrInvalidProtectionDocument
	}
	return NormalizeProtectionDocument(document)
}

func parseLegacyProtectionDocument(body []byte) (legacyProtectionDocument, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var document legacyProtectionDocument
	if err := decoder.Decode(&document); err != nil {
		return legacyProtectionDocument{}, ErrInvalidProtectionDocument
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return legacyProtectionDocument{}, ErrInvalidProtectionDocument
	}
	if document.RequiredApprovals < 0 || document.RequiredApprovals > MaxRequiredApprovals {
		return legacyProtectionDocument{}, ErrInvalidProtectionDocument
	}
	normalized := legacyProtectionDocument{
		Patterns:          make([]string, 0, len(document.Patterns)),
		RequiredApprovals: NormalizeRequiredApprovals(document.RequiredApprovals),
		AdminBypass:       document.AdminBypass,
	}
	seen := map[string]struct{}{}
	for _, raw := range document.Patterns {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return legacyProtectionDocument{}, ErrInvalidProtectionPattern
		}
		pattern, ok := NormalizeProtectedPattern(raw)
		if !ok {
			return legacyProtectionDocument{}, ErrInvalidProtectionPattern
		}
		if _, exists := seen[pattern]; exists {
			continue
		}
		seen[pattern] = struct{}{}
		normalized.Patterns = append(normalized.Patterns, pattern)
		if len(normalized.Patterns) > MaxProtectedPatterns {
			return legacyProtectionDocument{}, ErrInvalidProtectionDocument
		}
	}
	return normalized, nil
}

func migrateLegacyProtectionDocument(document legacyProtectionDocument) ProtectionDocument {
	targets := []RulesetTarget{{Type: RulesetTargetDefault}}
	for _, pattern := range document.Patterns {
		targets = append(targets, RulesetTarget{Type: RulesetTargetInclude, Pattern: pattern})
	}
	var bypass []RulesetBypass
	if document.AdminBypass {
		bypass = []RulesetBypass{{Kind: RulesetBypassAdmin}}
	}
	return ProtectionDocument{
		Version: ProtectionDocumentVersion,
		Rules: []ProtectionRule{{
			ID:                LegacyDefaultRuleID,
			Name:              LegacyDefaultRuleName,
			Enforcement:       RulesetEnforcementActive,
			Targets:           targets,
			Bypass:            bypass,
			RestrictUpdates:   true,
			RestrictDeletions: true,
			RequiredApprovals: NormalizeRequiredApprovals(document.RequiredApprovals),
		}},
	}
}

func NormalizeProtectionDocument(document ProtectionDocument) (ProtectionDocument, error) {
	if document.Version != 0 && document.Version != ProtectionDocumentVersion {
		return ProtectionDocument{}, ErrInvalidProtectionDocument
	}
	if len(document.Rules) > MaxProtectionRules {
		return ProtectionDocument{}, ErrInvalidProtectionDocument
	}
	normalized := ProtectionDocument{
		Version: ProtectionDocumentVersion,
		Rules:   make([]ProtectionRule, 0, len(document.Rules)),
	}
	seenIDs := map[string]struct{}{}
	for index, rule := range document.Rules {
		item, err := normalizeProtectionRule(rule, index, seenIDs)
		if err != nil {
			return ProtectionDocument{}, err
		}
		normalized.Rules = append(normalized.Rules, item)
	}
	return normalized, nil
}

func normalizeProtectionRule(rule ProtectionRule, index int, seenIDs map[string]struct{}) (ProtectionRule, error) {
	name := strings.TrimSpace(rule.Name)
	if name == "" || len(name) > maxRulesetNameLen {
		return ProtectionRule{}, ErrInvalidProtectionDocument
	}
	enforcement := strings.TrimSpace(rule.Enforcement)
	if enforcement == "" {
		enforcement = RulesetEnforcementActive
	}
	if enforcement != RulesetEnforcementActive && enforcement != RulesetEnforcementDisabled {
		return ProtectionRule{}, ErrInvalidProtectionDocument
	}
	if len(rule.Targets) > MaxRulesetTargets || len(rule.Bypass) > MaxRulesetBypassActors {
		return ProtectionRule{}, ErrInvalidProtectionDocument
	}
	id := strings.TrimSpace(rule.ID)
	if id == "" {
		id = allocateRuleID(name, index, seenIDs)
	}
	if _, exists := seenIDs[id]; exists {
		return ProtectionRule{}, ErrInvalidProtectionDocument
	}
	seenIDs[id] = struct{}{}
	targets, err := normalizeRulesetTargets(rule.Targets)
	if err != nil {
		return ProtectionRule{}, err
	}
	bypass, err := normalizeRulesetBypass(rule.Bypass)
	if err != nil {
		return ProtectionRule{}, err
	}
	return ProtectionRule{
		ID:                id,
		Name:              name,
		Enforcement:       enforcement,
		Targets:           targets,
		Bypass:            bypass,
		RestrictCreations: rule.RestrictCreations,
		RestrictUpdates:   rule.RestrictUpdates,
		RestrictDeletions: rule.RestrictDeletions,
		RequiredApprovals: NormalizeRequiredApprovals(rule.RequiredApprovals),
	}, nil
}

func normalizeRulesetTargets(targets []RulesetTarget) ([]RulesetTarget, error) {
	normalized := make([]RulesetTarget, 0, len(targets))
	seen := map[string]struct{}{}
	for _, target := range targets {
		item, key, err := normalizeRulesetTarget(target)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, item)
	}
	return normalized, nil
}

func normalizeRulesetTarget(target RulesetTarget) (RulesetTarget, string, error) {
	switch target.Type {
	case RulesetTargetDefault, RulesetTargetAll:
		return RulesetTarget{Type: target.Type}, target.Type, nil
	case RulesetTargetInclude, RulesetTargetExclude:
		pattern, ok := NormalizeRulesetPattern(target.Pattern)
		if !ok {
			return RulesetTarget{}, "", ErrInvalidProtectionPattern
		}
		return RulesetTarget{Type: target.Type, Pattern: pattern}, target.Type + ":" + pattern, nil
	default:
		return RulesetTarget{}, "", ErrInvalidProtectionDocument
	}
}

func normalizeRulesetBypass(actors []RulesetBypass) ([]RulesetBypass, error) {
	normalized := make([]RulesetBypass, 0, len(actors))
	seen := map[string]struct{}{}
	for _, actor := range actors {
		item, key, err := normalizeRulesetBypassActor(actor)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, item)
	}
	return normalized, nil
}

func normalizeRulesetBypassActor(actor RulesetBypass) (RulesetBypass, string, error) {
	switch actor.Kind {
	case RulesetBypassAdmin, RulesetBypassMaintainer:
		return RulesetBypass{Kind: actor.Kind}, actor.Kind, nil
	case RulesetBypassMember:
		pubkey, ok := NormalizeMemberPubkey(actor.Pubkey)
		if !ok {
			return RulesetBypass{}, "", ErrInvalidProtectionDocument
		}
		return RulesetBypass{Kind: RulesetBypassMember, Pubkey: pubkey}, "member:" + pubkey, nil
	default:
		return RulesetBypass{}, "", ErrInvalidProtectionDocument
	}
}

func NormalizeMemberPubkey(value string) (string, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) != 64 {
		return "", false
	}
	for _, r := range value {
		if !unicode.Is(unicode.ASCII_Hex_Digit, r) {
			return "", false
		}
	}
	return value, true
}

func allocateRuleID(name string, index int, seen map[string]struct{}) string {
	base := slugRuleID(name)
	if base == "" {
		base = "rule"
	}
	id := base
	if _, exists := seen[id]; exists {
		id = fmt.Sprintf("%s-%d", base, index+1)
	}
	for n := 2; ; n++ {
		if _, exists := seen[id]; !exists {
			return id
		}
		id = fmt.Sprintf("%s-%d", base, n)
	}
}

func slugRuleID(name string) string {
	var b strings.Builder
	lastHyphen := true
	for _, r := range strings.ToLower(name) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			lastHyphen = false
			continue
		}
		if !lastHyphen {
			b.WriteByte('-')
			lastHyphen = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func DefaultProtectionDocument() ProtectionDocument {
	return ProtectionDocument{Version: ProtectionDocumentVersion, Rules: []ProtectionRule{}}
}

func (document ProtectionDocument) AdvertisedPatterns() []string {
	compiled := CompileRulesets(DefaultBootstrapRef, document, nil)
	return compiled.DocumentPatterns
}

func (document ProtectionDocument) AdvertisedApprovals() int {
	return CompileRulesets(DefaultBootstrapRef, document, nil).RequiredApprovals
}

func (document ProtectionDocument) AdvertisedAdminBypass() bool {
	return CompileRulesets(DefaultBootstrapRef, document, nil).AdminBypass
}
