package architecture

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Module is a Go module discovered in the repository. This fork has two: the
// root module and modules/buzzagents, joined by a replace directive. Imports are
// resolved against both so the boundary matrix covers the whole tree without
// needing a second module context.
type Module struct {
	// Root is the module directory relative to the repository root, or "" for
	// the root module.
	Root string
	// Path is the module path declared in go.mod.
	Path string
}

// findModules returns every module rooted inside the repository, deepest root
// first so a directory resolves to its nearest module.
func findModules(root string) ([]Module, error) {
	var modules []Module
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if excludedDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Name() != "go.mod" {
			return nil
		}

		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		modulePath, modErr := readModulePath(filepath.Join(root, filepath.FromSlash(rel)))
		if modErr != nil {
			return modErr
		}
		moduleRoot := filepath.ToSlash(filepath.Dir(rel))
		if moduleRoot == "." {
			moduleRoot = ""
		}
		modules = append(modules, Module{Root: moduleRoot, Path: modulePath})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(modules) == 0 {
		return nil, fmt.Errorf("no go.mod found under %s", root)
	}

	rooted := false
	for _, module := range modules {
		if module.Root == "" {
			rooted = true
			break
		}
	}
	if !rooted {
		return nil, fmt.Errorf("no go.mod at the repository root")
	}

	sort.SliceStable(modules, func(i, j int) bool {
		return strings.Count(modules[i].Root, "/") > strings.Count(modules[j].Root, "/")
	})
	return modules, nil
}

// readModulePath reads the module path from a go.mod file.
func readModulePath(goMod string) (string, error) {
	file, err := os.Open(goMod)
	if err != nil {
		return "", err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && fields[0] == "module" {
			return strings.Trim(fields[1], `"`), nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("%s declares no module path", goMod)
}

// packageDirs maps every directory containing .go files to its import path.
func packageDirs(root string, modules []Module) (map[string]string, error) {
	dirs := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if excludedDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".go") {
			return nil
		}

		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		dir := filepath.ToSlash(filepath.Dir(rel))
		if _, ok := dirs[dir]; ok {
			return nil
		}
		module, ok := moduleFor(dir, modules)
		if !ok {
			return fmt.Errorf("%s is not inside any module", dir)
		}
		dirs[dir] = importPathFor(dir, module)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return dirs, nil
}

// moduleFor returns the module whose root is the longest prefix of dir. Modules
// are pre-sorted deepest first, so the first match is the nearest one.
func moduleFor(dir string, modules []Module) (Module, bool) {
	for _, module := range modules {
		if module.Root == "" || dir == module.Root || strings.HasPrefix(dir, module.Root+"/") {
			return module, true
		}
	}
	return Module{}, false
}

// importPathFor converts a directory to its import path within a module.
func importPathFor(dir string, module Module) string {
	rel := strings.TrimPrefix(strings.TrimPrefix(dir, module.Root), "/")
	if rel == "" {
		return module.Path
	}
	return module.Path + "/" + rel
}

// isInternal reports whether an import path belongs to a module in this
// repository rather than to a third-party dependency.
func isInternal(importPath string, modules []Module) bool {
	for _, module := range modules {
		if importPath == module.Path || strings.HasPrefix(importPath, module.Path+"/") {
			return true
		}
	}
	return false
}
