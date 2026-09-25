// Package architecture enforces counted-line file budgets and internal package
// boundaries for Lapis-authored Go sources in this fork.
//
// Files inherited from the pinned Unicity upstream and the vendored Khatru tree
// are out of scope; see Scope. The budgets and boundary matrix are documented
// in spec/src/maintenance/architecture-budgets.md.
package architecture

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
)

// CountedLines reports the counted-line size of a Go source file: physical
// lines minus blank lines minus comment-only lines. A line carrying code plus a
// trailing comment still counts, and a trailing newline does not add a line.
//
// Comments are located with the Go parser rather than a line-prefix scan. That
// keeps comment markers inside raw string literals from being mistaken for
// comments, and it keeps a line beginning with "*" counted when it is really a
// pointer dereference or a multiplication rather than documentation.
func CountedLines(src []byte) (int, error) {
	if len(bytes.TrimSpace(src)) == 0 {
		return 0, nil
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "counted.go", src, parser.ParseComments)
	if err != nil {
		return 0, fmt.Errorf("parse go source: %w", err)
	}

	lines := bytes.Split(blankComments(fset, src, file), []byte("\n"))
	if n := len(lines); n > 0 && len(lines[n-1]) == 0 {
		lines = lines[:n-1]
	}

	counted := 0
	for _, line := range lines {
		if len(bytes.TrimSpace(line)) > 0 {
			counted++
		}
	}
	return counted, nil
}

// blankComments returns a copy of src with every comment byte replaced by a
// space. Newlines inside block comments are preserved so line numbering and
// blank-line detection survive the rewrite.
func blankComments(fset *token.FileSet, src []byte, file *ast.File) []byte {
	out := make([]byte, len(src))
	copy(out, src)

	for _, group := range file.Comments {
		for _, comment := range group.List {
			start := fset.Position(comment.Slash).Offset
			for i := start; i < start+len(comment.Text) && i < len(out); i++ {
				if out[i] != '\n' {
					out[i] = ' '
				}
			}
		}
	}
	return out
}
