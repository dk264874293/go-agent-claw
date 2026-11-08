// Package quality 对明确来源和有限命题做本地核验，不执行资料中的代码。
package quality

import "encoding/json"

const (
	CheckerVersion = "quality_v1"
	MaxChecks      = 64
	MaxCases       = 16
	MaxNodes       = 128
	MaxDepth       = 16
	MaxJSONBytes   = 64 << 10
	MaxJSONDepth   = 32
	MaxLiteral     = 128
	MaxBits        = 4096
)

type Boundary struct {
	StartsLine bool `json:"starts_line"`
	EndsLine   bool `json:"ends_line"`
}

// Source 只接受读取实现保存的原始字节。当前权限、版本、实际交付和Agent归属由Lookup校验。
type Source struct {
	ID, Path, Version              string
	ByteStart, ByteEnd, TotalBytes int64
	FirstLine, LastLine            int
	Content                        []byte
	Boundary                       *Boundary
}
type Lookup func(string) (Source, error)

// Selector 使用文件绝对字节位置；长主体与短引文独立，不扩展短引文限制。
type Selector struct {
	EvidenceIDs         []string `json:"evidence_ids"`
	ByteStart           int64    `json:"byte_start"`
	ByteEnd             int64    `json:"byte_end"`
	RequireCompleteLine bool     `json:"require_complete_line,omitempty"`
}
type SourceRef struct {
	EvidenceIDs []string `json:"evidence_ids"`
	Path        string   `json:"path"`
	Version     string   `json:"version"`
	SHA256      string   `json:"sha256"`
	ByteStart   int64    `json:"byte_start"`
	ByteEnd     int64    `json:"byte_end"`
	FirstLine   int      `json:"first_line,omitempty"`
	LastLine    int      `json:"last_line,omitempty"`
}
type Resolved struct {
	SourceRef
	Content              []byte
	Before, After        *byte
	StartsLine, EndsLine bool
}

type Binding struct {
	Kind    string    `json:"kind"` // source_value / test_input / proposed_constant
	Value   string    `json:"value,omitempty"`
	Source  *Selector `json:"source,omitempty"`
	Pointer string    `json:"pointer,omitempty"`
}
type Case struct {
	Name     string            `json:"name,omitempty"`
	Inputs   map[string]string `json:"inputs,omitempty"`
	Expected string            `json:"expected"`
}
type Assertion struct {
	Op       string          `json:"op"` // exists / absent / type / equals / default
	Pointer  string          `json:"pointer"`
	Type     string          `json:"type,omitempty"`
	Expected json.RawMessage `json:"expected,omitempty"`
	Default  json.RawMessage `json:"default,omitempty"`
}
type Check struct {
	ID         string             `json:"id"`
	FindingID  string             `json:"finding_id"`
	Kind       string             `json:"kind"` // numeric / json
	Subject    Selector           `json:"subject"`
	Expression string             `json:"expression,omitempty"`
	Bindings   map[string]Binding `json:"bindings,omitempty"`
	Cases      []Case             `json:"cases,omitempty"`
	Assertions []Assertion        `json:"assertions,omitempty"`
	Premise    string             `json:"premise,omitempty"` // source_arithmetic / translated / proposed
}
type CaseResult struct {
	Name     string            `json:"name,omitempty"`
	Inputs   map[string]string `json:"inputs,omitempty"`
	Expected string            `json:"expected"`
	Actual   string            `json:"actual,omitempty"`
	Result   string            `json:"result"`
}
type AssertionResult struct {
	Pointer  string          `json:"pointer"`
	Op       string          `json:"op"`
	Present  bool            `json:"present"`
	Actual   json.RawMessage `json:"actual,omitempty"`
	Expected json.RawMessage `json:"expected,omitempty"`
	Default  json.RawMessage `json:"default,omitempty"`
	Result   string          `json:"result"`
}
type BoundValue struct {
	Kind   string     `json:"kind"`
	Value  string     `json:"value,omitempty"`
	Source *SourceRef `json:"source,omitempty"`
}
type Receipt struct {
	Diagnostic     *InputDiagnostic      `json:"diagnostic,omitempty"`
	CheckerVersion string                `json:"checker_version"`
	ID             string                `json:"id"`
	FindingID      string                `json:"finding_id"`
	Kind           string                `json:"kind"`
	Result         string                `json:"result"`
	PremiseResult  string                `json:"premise_result"`
	Reason         string                `json:"reason,omitempty"`
	Formula        string                `json:"formula,omitempty"`
	Bindings       map[string]BoundValue `json:"bindings,omitempty"`
	Sources        []SourceRef           `json:"sources,omitempty"`
	Cases          []CaseResult          `json:"cases,omitempty"`
	Assertions     []AssertionResult     `json:"assertions,omitempty"`
	Scope          string                `json:"scope"`
}

// InputDiagnostic 是可纠正的输入问题，不从错误文字猜测检查器是否损坏。
type InputDiagnostic struct {
	Code      string   `json:"code"`
	Variables []string `json:"variables,omitempty"`
	Omitted   int      `json:"omitted_variables,omitempty"`
	Hint      string   `json:"hint"`
}
