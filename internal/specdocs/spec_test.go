package specdocs

import (
	"bufio"
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
