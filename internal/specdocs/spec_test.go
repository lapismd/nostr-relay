package specdocs

import (
	"bufio"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var markdownLink = regexp.MustCompile(`!?\[[^]]*\]\(([^)]+)\)`)

func TestLocalMarkdownLinksResolve(t *testing.T) {
	repositoryRoot := filepath.Clean(filepath.Join("..", ".."))
	specRoot := filepath.Join(repositoryRoot, "spec", "src")

	err := filepath.WalkDir(specRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".md" {
			return nil
		}

		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()

		scanner := bufio.NewScanner(file)
		lineNumber := 0
		for scanner.Scan() {
			lineNumber++
			for _, match := range markdownLink.FindAllStringSubmatch(scanner.Text(), -1) {
				target := strings.TrimSpace(match[1])
				if target == "" || strings.HasPrefix(target, "#") {
					continue
				}
				parsed, err := url.Parse(target)
				if err != nil {
					t.Errorf("%s:%d: invalid link %q: %v", path, lineNumber, target, err)
					continue
				}
				if parsed.IsAbs() || parsed.Host != "" || parsed.Path == "" {
					continue
				}
				decodedPath, err := url.PathUnescape(parsed.Path)
				if err != nil {
					t.Errorf("%s:%d: invalid escaped path %q: %v", path, lineNumber, target, err)
					continue
				}
				resolved := filepath.Clean(filepath.Join(filepath.Dir(path), filepath.FromSlash(decodedPath)))
				if _, err := os.Stat(resolved); err != nil {
					t.Errorf("%s:%d: local link %q does not resolve: %v", path, lineNumber, target, err)
				}
			}
		}
		return scanner.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNIPReferenceInventoryAndMetadata(t *testing.T) {
	repositoryRoot := filepath.Clean(filepath.Join("..", ".."))
	specRoot := filepath.Join(repositoryRoot, "spec", "src")
	nipRoot := filepath.Join(specRoot, "nips")
	index := readSpecFile(t, filepath.Join(nipRoot, "index.md"))
	summary := readSpecFile(t, filepath.Join(specRoot, "SUMMARY.md"))
	pages, err := filepath.Glob(filepath.Join(nipRoot, "NIP-*.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) == 0 {
		t.Fatal("NIP reference has no protocol pages")
	}

	requiredHeadings := []string{
		"## Protocol status and dependencies",
		"## Source and audit",
		"## Purpose, roles and trust boundaries",
		"## Event and transport contract",
		"## Validation, lifecycle and visibility",
		"## Component support",
		"## Divergences and limitations",
		"## Evidence and refresh",
	}
	for _, path := range pages {
		name := filepath.Base(path)
		content := readSpecFile(t, path)
		for _, heading := range requiredHeadings {
			if !strings.Contains(content, heading) {
				t.Errorf("%s is missing required section %q", name, heading)
			}
		}
		if !strings.Contains(content, "Source:") ||
			!regexp.MustCompile(`Lapis implementation audit:.*[0-9]{4}-[0-9]{2}-[0-9]{2}`).MatchString(content) ||
			!strings.Contains(content, "| Source requirement | Lapis behavior | Classification | Reason and compatibility impact | Evidence/status |") {
			t.Errorf("%s is missing source, audit date, or divergence metadata", name)
		}
		link := "(" + filepath.ToSlash(filepath.Join("nips", name)) + ")"
		if !strings.Contains(summary, link) {
			t.Errorf("%s is not linked from SUMMARY.md", name)
		}
		if !strings.Contains(index, "("+name+")") {
			t.Errorf("%s is not listed in the NIP inventory", name)
		}
	}

	for _, match := range regexp.MustCompile(`\(NIP-([0-9A-Z]+)\.md\)`).FindAllStringSubmatch(index, -1) {
		path := filepath.Join(nipRoot, fmt.Sprintf("NIP-%s.md", match[1]))
		if _, err := os.Stat(path); err != nil {
			t.Errorf("inventory entry NIP-%s has no page: %v", match[1], err)
		}
	}
}

func readSpecFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}
