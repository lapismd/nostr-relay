package forge

type ContentLimits struct {
	MaximumBlobBytes    int64 `json:"maximum_blob_bytes,omitempty"`
	MaximumChangedFiles int   `json:"maximum_changed_files,omitempty"`
	MaximumTreeEntries  int   `json:"maximum_tree_entries,omitempty"`
	MaximumPatchBytes   int64 `json:"maximum_patch_bytes,omitempty"`
	MayTruncateCompare  bool  `json:"may_truncate_compare,omitempty"`
	MayTruncateTree     bool  `json:"may_truncate_tree,omitempty"`
}

// Limits remains an internal source-compatibility alias. ContentLimits is the
// provider-neutral contract used by new code and exposed as capability_limits.
type Limits = ContentLimits

type GitAuthorityCapabilities struct {
	Clone     CloneDescriptor       `json:"clone"`
	SmartHTTP SmartHTTPCapabilities `json:"smart_http"`
	RefEvents bool                  `json:"ref_events"`
	Provision bool                  `json:"provision"`
	Import    bool                  `json:"import"`
}

type ContentCapabilities struct {
	Browse  bool          `json:"browse"`
	Compare bool          `json:"compare"`
	Limits  ContentLimits `json:"capability_limits"`
}

// Capabilities is intentionally explicit. A false value is meaningful and
// prevents callers from inferring functionality from a provider name.
type Capabilities struct {
	GitAuthorityBackend         string                `json:"git_authority_backend"`
	ContentBackend              string                `json:"content_backend"`
	GitBackend                  string                `json:"git_backend"`
	CollaborationBackend        string                `json:"collaboration_backend"`
	MirrorBackend               string                `json:"mirror_backend,omitempty"`
	GitReplicationTargetBackend string                `json:"git_replication_target_backend,omitempty"`
	CIBackend                   string                `json:"ci_backend,omitempty"`
	Clone                       CloneDescriptor       `json:"clone"`
	SmartHTTP                   SmartHTTPCapabilities `json:"smart_http"`
	Browse                      bool                  `json:"browse"`
	Compare                     bool                  `json:"compare"`
	RefEvents                   bool                  `json:"ref_events"`
	Provision                   bool                  `json:"provision"`
	Import                      bool                  `json:"import"`
	Issues                      bool                  `json:"issues"`
	PullRequests                bool                  `json:"pull_requests"`
	Reviews                     bool                  `json:"reviews"`
	Merge                       bool                  `json:"merge"`
	Protection                  *BranchProtection     `json:"protection,omitempty"`
	Mirror                      bool                  `json:"mirror"`
	GitReplicationTarget        bool                  `json:"git_replication_target,omitempty"`
	CI                          bool                  `json:"ci"`
	LocalReplica                bool                  `json:"local_replica,omitempty"`
	ReplicaState                string                `json:"replica_state,omitempty"`
	Limits                      ContentLimits         `json:"limits"`
	CapabilityLimits            ContentLimits         `json:"capability_limits"`
}

// ResultMetadata is additive metadata shared by every repository-content
// result. Continuation is opaque to callers. A missing continuation on a
// truncated result means the provider cannot retrieve the omitted portion.
type ResultMetadata struct {
	Truncated        bool           `json:"truncated,omitempty"`
	Continuation     string         `json:"continuation,omitempty"`
	Incomplete       []string       `json:"incomplete,omitempty"`
	LimitExceeded    *LimitExceeded `json:"limit_exceeded,omitempty"`
	CapabilityLimits *ContentLimits `json:"capability_limits,omitempty"`

	CacheControl string `json:"-"`
	ETag         string `json:"-"`
	LastModified string `json:"-"`
}

type LimitExceeded struct {
	Resource string `json:"resource"`
	Limit    int64  `json:"limit"`
	Actual   int64  `json:"actual,omitempty"`
}
