package schema

// ContentReview 只记录有界的机械交付提示，不表示语义或运行验收。
type ContentReview struct {
	Version    int                  `json:"version"`
	RunID      string               `json:"run_id"`
	AgentID    string               `json:"agent_id"`
	ActionID   string               `json:"action_id,omitempty"`
	Path       string               `json:"path,omitempty"`
	ContentSHA string               `json:"content_sha256,omitempty"`
	Status     string               `json:"status"`
	Reason     string               `json:"reason,omitempty"`
	Issues     []ContentReviewIssue `json:"issues,omitempty"`
	Omitted    int                  `json:"omitted"`
}

type ContentReviewIssue struct {
	Code      string                  `json:"code"`
	Line      int                     `json:"line"`
	Message   string                  `json:"message"`
	Locations []ContentSourceLocation `json:"locations,omitempty"`
}

type ContentSourceLocation struct {
	Path       string `json:"path"`
	Version    string `json:"version"`
	EvidenceID string `json:"evidence_id"`
	FirstLine  int    `json:"first_line"`
	LastLine   int    `json:"last_line"`
}
