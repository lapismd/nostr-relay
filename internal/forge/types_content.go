package forge

type RefsRequest struct {
	Kind         string
	Continuation string
	Principal    string
}

type RevisionRequest struct {
	Revision  string
	Path      string
	Principal string
}

type CommitsRequest struct {
	Ref          string
	Path         string
	Skip         int
	Limit        int
	Continuation string
	Principal    string
}

type CompareRequest struct {
	Base          string
	Head          string
	UseMergeBase  bool
	MaxFiles      int
	MaxPatchBytes int64
	Principal     string
}

type PullRequestDiffRequest struct {
	Number        int64
	MaxFiles      int
	MaxPatchBytes int64
	Principal     string
}

type Revision struct {
	Ref  string `json:"ref"`
	SHA  string `json:"sha"`
	Path string `json:"path"`
	Kind string `json:"kind"`
	ResultMetadata
}

type Ref struct {
	Name string `json:"name"`
	SHA  string `json:"sha"`
}

type RefPage struct {
	Head *Ref  `json:"head,omitempty"`
	Refs []Ref `json:"refs,omitempty"`
	More bool  `json:"more,omitempty"`
	ResultMetadata
}

type TreeEntry struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Mode string `json:"mode"`
	Size int64  `json:"size"`
	SHA  string `json:"sha"`
}

type Tree struct {
	Ref     string      `json:"ref"`
	SHA     string      `json:"sha"`
	Path    string      `json:"path"`
	Entries []TreeEntry `json:"entries"`
	ResultMetadata
}

type Blob struct {
	Ref      string `json:"ref"`
	SHA      string `json:"sha"`
	Path     string `json:"path"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Contents string `json:"contents,omitempty"`
	Binary   bool   `json:"binary,omitempty"`
	TooLarge bool   `json:"too_large,omitempty"`
	ResultMetadata
}

type Commit struct {
	SHA         string   `json:"sha"`
	Parents     []string `json:"parents"`
	Author      string   `json:"author"`
	AuthorEmail string   `json:"author_email"`
	AuthorDate  string   `json:"author_date"`
	Committer   string   `json:"committer"`
	CommitDate  string   `json:"commit_date"`
	Subject     string   `json:"subject"`
	Body        string   `json:"body"`
	Trailers    []any    `json:"trailers"`
}

type CommitPage struct {
	Ref     string   `json:"ref"`
	SHA     string   `json:"sha"`
	Commits []Commit `json:"commits"`
	More    bool     `json:"more"`
	ResultMetadata
}

type CommitStat struct {
	Path      string `json:"path"`
	Additions int64  `json:"additions"`
	Deletions int64  `json:"deletions"`
}

type CommitDetail struct {
	Commit Commit       `json:"commit"`
	Stats  []CommitStat `json:"stats"`
	Patch  string       `json:"patch"`
	ResultMetadata
}

type DiffFile struct {
	Path      string `json:"path"`
	OldPath   string `json:"old_path,omitempty"`
	Status    string `json:"status"`
	Additions int64  `json:"additions"`
	Deletions int64  `json:"deletions"`
	Binary    bool   `json:"binary,omitempty"`
}

type Diff struct {
	Base      string     `json:"base"`
	Head      string     `json:"head"`
	MergeBase string     `json:"merge_base,omitempty"`
	Files     []DiffFile `json:"files"`
	Patch     string     `json:"patch"`
	ResultMetadata
}
