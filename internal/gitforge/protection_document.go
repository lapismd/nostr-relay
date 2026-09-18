package gitforge

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

const (
	MaxProtectedPatterns     = 16
	MaxRequiredApprovals     = 16
	DefaultRequiredApprovals = 1
)

var (
	ErrInvalidProtectionDocument = errors.New("invalid_protection_document")
	ErrInvalidProtectionPattern  = errors.New("invalid_protection_pattern")
)

type ProtectionDocument struct {
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

func NormalizeProtectionDocument(document ProtectionDocument) (ProtectionDocument, error) {
	if document.RequiredApprovals < 0 || document.RequiredApprovals > MaxRequiredApprovals {
		return ProtectionDocument{}, ErrInvalidProtectionDocument
	}
	normalized := ProtectionDocument{
		Patterns:          make([]string, 0, len(document.Patterns)),
		RequiredApprovals: NormalizeRequiredApprovals(document.RequiredApprovals),
		AdminBypass:       document.AdminBypass,
	}
	seen := map[string]struct{}{}
	for _, raw := range document.Patterns {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return ProtectionDocument{}, ErrInvalidProtectionPattern
		}
		pattern, ok := NormalizeProtectedPattern(raw)
		if !ok {
			return ProtectionDocument{}, ErrInvalidProtectionPattern
		}
		if _, exists := seen[pattern]; exists {
			continue
		}
		seen[pattern] = struct{}{}
		normalized.Patterns = append(normalized.Patterns, pattern)
		if len(normalized.Patterns) > MaxProtectedPatterns {
			return ProtectionDocument{}, ErrInvalidProtectionDocument
		}
	}
	return normalized, nil
}

func DefaultProtectionDocument() ProtectionDocument {
	return ProtectionDocument{RequiredApprovals: DefaultRequiredApprovals}
}
