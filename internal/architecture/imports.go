package architecture

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// PackageGraph maps a package directory to the internal package directories it
// imports, across every .go file in that directory including tests.
type PackageGraph map[string][]string

// ImportGraph holds both the internal package graph and the external import paths
// each package uses, from one parse pass over the tree.
type ImportGraph struct {
	Internal PackageGraph
	External map[string][]string
}

// collectImportGraph parses every .go file under each package directory once and
// records both its internal package edges and its external import paths. Parsing
// sources rather than running "go list" keeps both modules in a single pass;
// "go build ./..." remains the authority for whether the tree compiles.
func collectImportGraph(root string, dirs map[string]string, modules []Module) (*ImportGraph, error) {
	byImport := make(map[string]string, len(dirs))
	for dir, importPath := range dirs {
		byImport[importPath] = dir
	}

	graph := &ImportGraph{
		Internal: make(PackageGraph, len(dirs)),
		External: make(map[string][]string, len(dirs)),
	}
	for dir := range dirs {
		graph.Internal[dir] = nil
		graph.External[dir] = nil
	}

	for dir := range dirs {
		internalSeen := map[string]bool{}
		externalSeen := map[string]bool{}

		names, err := goFileNames(filepath.Join(root, filepath.FromSlash(dir)))
		if err != nil {
			return nil, err
		}
		for _, name := range names {
			imports, err := fileImports(filepath.Join(root, filepath.FromSlash(dir), name))
			if err != nil {
				return nil, err
			}
			for _, importPath := range imports {
				if !isInternal(importPath, modules) {
					if !externalSeen[importPath] {
						externalSeen[importPath] = true
						graph.External[dir] = append(graph.External[dir], importPath)
					}
					continue
				}
				target, ok := byImport[importPath]
				if !ok {
					return nil, fmt.Errorf("%s imports %s, which is not a package in this repository", dir, importPath)
				}
				if target == dir || internalSeen[target] {
					continue
				}
				internalSeen[target] = true
				graph.Internal[dir] = append(graph.Internal[dir], target)
			}
		}
		sort.Strings(graph.Internal[dir])
		sort.Strings(graph.External[dir])
	}
	return graph, nil
}

// goFileNames returns the sorted .go file names directly inside a directory.
func goFileNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".go") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// fileImports returns the import paths declared by one Go file.
func fileImports(path string) ([]string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	imports := make([]string, 0, len(file.Imports))
	for _, spec := range file.Imports {
		imports = append(imports, strings.Trim(spec.Path.Value, `"`))
	}
	return imports, nil
}

// Node colours for the depth-first cycle search.
const (
	unvisited = 0
	onStack   = 1
	done      = 2
)

// sortedKeys returns the keys of a string-keyed map in ascending order, so
// traversal and reporting are deterministic.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// findCycle returns one import cycle in the graph, or nil when the graph is
// acyclic. The result starts and ends at the same directory.
func findCycle(graph PackageGraph) []string {
	state := make(map[string]int, len(graph))
	var stack []string
	var cycle []string

	for _, dir := range sortedKeys(graph) {
		if state[dir] == unvisited {
			if visit(dir, graph, state, &stack, &cycle) {
				return cycle
			}
		}
	}
	return nil
}

// visit is the depth-first step of findCycle. It returns true once a cycle has
// been recorded.
func visit(dir string, graph PackageGraph, state map[string]int, stack *[]string, cycle *[]string) bool {
	state[dir] = onStack
	*stack = append(*stack, dir)

	for _, next := range graph[dir] {
		switch state[next] {
		case onStack:
			*cycle = append(append([]string{}, (*stack)[indexOf(*stack, next):]...), next)
			return true
		case unvisited:
			if visit(next, graph, state, stack, cycle) {
				return true
			}
		}
	}

	*stack = (*stack)[:len(*stack)-1]
	state[dir] = done
	return false
}

func indexOf(haystack []string, needle string) int {
	for i, entry := range haystack {
		if entry == needle {
			return i
		}
	}
	return 0
}
