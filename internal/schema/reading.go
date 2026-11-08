package schema

// ReadingCoverage 只说明指定发现清单在核对时的读取范围，不证明分析或验收通过。
type ReadingCoverage struct {
	Status              string                `json:"status"`
	ScopeKind           string                `json:"scope_kind,omitempty"`
	ScopeID             string                `json:"scope_id,omitempty"`
	ScopeRevision       string                `json:"scope_revision,omitempty"`
	ManifestIDs         []string              `json:"manifest_ids,omitempty"`
	UnresolvedPaths     []string              `json:"unresolved_paths,omitempty"`
	ManifestID          string                `json:"manifest_id,omitempty"`
	Files               []FileReadingCoverage `json:"files"`
	CompleteFiles       int                   `json:"complete_files"`
	TotalFiles          int                   `json:"total_files"`
	RecordedOnly        bool                  `json:"recorded_only,omitempty"`
	DiscoveryIncomplete bool                  `json:"discovery_incomplete,omitempty"`
}

type ByteInterval struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}
type FileReadingCoverage struct {
	Unknown    bool           `json:"unknown,omitempty"`
	Path       string         `json:"path"`
	Version    string         `json:"version,omitempty"`
	TotalBytes int64          `json:"total_bytes"`
	ReadBytes  int64          `json:"read_bytes"`
	Complete   bool           `json:"complete"`
	Missing    []ByteInterval `json:"missing"`
}
