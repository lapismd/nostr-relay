package architecture

import "testing"

func TestCountedLines(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want int
	}{
		{
			name: "empty file counts nothing",
			src:  "",
			want: 0,
		},
		{
			name: "trailing newline does not add a line",
			src:  "package a\n",
			want: 1,
		},
		{
			name: "blank lines are not counted",
			src:  "package a\n\n\nfunc f() {}\n",
			want: 2,
		},
		{
			name: "line comments are not counted",
			src:  "package a\n\n// a note\n// another note\nfunc f() {}\n",
			want: 2,
		},
		{
			name: "single line block comment is not counted",
			src:  "package a\n/* inline block */\nfunc f() {}\n",
			want: 2,
		},
		{
			name: "multi line block comment is not counted",
			src:  "package a\n/* first\n   second\n   third */\nfunc f() {}\n",
			want: 2,
		},
		{
			name: "doc comment continuation lines are not counted",
			src:  "package a\n\n// f does work.\n//\n// It returns nothing.\nfunc f() {}\n",
			want: 2,
		},
		{
			name: "code with a trailing comment still counts",
			src:  "package a\n\nfunc f() {} // trailing\n",
			want: 2,
		},
		{
			name: "build directives are comments and are not counted",
			src:  "//go:build integration\n\npackage a\n\nfunc f() {}\n",
			want: 2,
		},
		{
			// Four physical lines, one blank, so three counted. Both comment
			// markers sit inside the raw string and must not blank the lines.
			name: "comment markers inside raw strings are not comments",
			src:  "package a\n\nvar s = `// not a comment\n/* also not */`\n",
			want: 3,
		},
		{
			// A naive line-prefix scanner that skips lines starting with "*"
			// would drop the assignment and undercount real code.
			name: "pointer dereference lines are code",
			src:  "package a\n\nfunc f(p *int) {\n\t*p = 1\n}\n",
			want: 4,
		},
		{
			name: "star prefixed doc continuation is a comment",
			src:  "package a\n\n/*\n * documented\n */\nfunc f() {}\n",
			want: 2,
		},
		{
			name: "url literal is not read as a comment",
			src:  "package a\n\nvar u = \"https://example.com/x\"\n",
			want: 2,
		},
		{
			name: "whitespace only lines are blank",
			src:  "package a\n   \n\t\nfunc f() {}\n",
			want: 2,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := CountedLines([]byte(test.src))
			if err != nil {
				t.Fatalf("CountedLines returned an error: %v", err)
			}
			if got != test.want {
				t.Errorf("CountedLines = %d, want %d", got, test.want)
			}
		})
	}
}

func TestCountedLinesRejectsUnparsableSource(t *testing.T) {
	if _, err := CountedLines([]byte("package a\n\nfunc f( {\n")); err == nil {
		t.Fatal("CountedLines accepted source that does not parse")
	}
}
