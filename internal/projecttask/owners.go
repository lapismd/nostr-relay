package projecttask

import (
	"strings"
	"unicode"
)

// OwnersRule maps a repository path prefix to extra required reviewers.
type OwnersRule struct {
	Path    string
	Pubkeys []string
}

// OwnersDocument is the fail-closed OWNERS.md subset: comments, blank lines,
// and `path 64-hex` rows. A parse error yields no extra owners.
type OwnersDocument struct {
	Rules []OwnersRule
}

func ParseOwnersDocument(text string) OwnersDocument {
	if strings.ContainsRune(text, '\x00') {
		return OwnersDocument{}
	}
	document := OwnersDocument{}
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		path, pubkeys, ok := parseOwnersLine(line)
		if !ok {
			return OwnersDocument{}
		}
		document.Rules = append(document.Rules, OwnersRule{Path: path, Pubkeys: pubkeys})
	}
	return document
}

func parseOwnersLine(line string) (string, []string, bool) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return "", nil, false
	}
	start := 0
	path := "*"
	if !isOwnersPubkey(fields[0]) {
		path = normalizeOwnersPath(fields[0])
		start = 1
	}
	if start >= len(fields) {
		return "", nil, false
	}
	pubkeys := make([]string, 0, len(fields)-start)
	for _, field := range fields[start:] {
		if !isOwnersPubkey(field) {
			return "", nil, false
		}
		pubkeys = append(pubkeys, strings.ToLower(field))
	}
	return path, pubkeys, true
}

func normalizeOwnersPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || path == "*" {
		return "*"
	}
	path = strings.TrimPrefix(path, "./")
	return path
}

func isOwnersPubkey(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !unicode.Is(unicode.ASCII_Hex_Digit, character) {
			return false
		}
	}
	return true
}

func (document OwnersDocument) OwnersForPath(path string) []string {
	matched := OwnersRule{Path: ""}
	for _, rule := range document.Rules {
		if ownersPathMatches(rule.Path, path) && len(rule.Path) >= len(matched.Path) {
			matched = rule
		}
	}
	if matched.Path == "" {
		return nil
	}
	return append([]string(nil), matched.Pubkeys...)
}

func (document OwnersDocument) RequiredGroups(paths []string) []OwnersRule {
	groups := make(map[string]OwnersRule)
	for _, path := range paths {
		owners := document.OwnersForPath(path)
		if len(owners) == 0 {
			continue
		}
		key := strings.Join(owners, ",")
		if _, found := groups[key]; !found {
			groups[key] = OwnersRule{Path: longestOwnersPath(document, path), Pubkeys: owners}
		}
	}
	result := make([]OwnersRule, 0, len(groups))
	for _, group := range groups {
		result = append(result, group)
	}
	return result
}

func longestOwnersPath(document OwnersDocument, path string) string {
	best := ""
	for _, rule := range document.Rules {
		if ownersPathMatches(rule.Path, path) && len(rule.Path) >= len(best) {
			best = rule.Path
		}
	}
	return best
}

func ownersPathMatches(rulePath, filePath string) bool {
	if rulePath == "*" || rulePath == "" {
		return true
	}
	filePath = strings.TrimPrefix(filePath, "./")
	if rulePath == filePath {
		return true
	}
	if strings.HasSuffix(rulePath, "/") {
		return strings.HasPrefix(filePath, rulePath)
	}
	return strings.HasPrefix(filePath, rulePath+"/")
}
