package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

type rejectedTool struct{ err error }

func (rejectedTool) Name() string { return "read_file" }
func (rejectedTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{Name: "read_file"}
}
func (t rejectedTool) Execute(context.Context, json.RawMessage) (string, error) { return "", t.err }

func TestNewRejectedErrorPreservesCauseAndClassification(t *testing.T) {
	cause := errors.New("结构化材料无效")
	err := NewRejectedError("报告校验拒绝", "report_invalid", cause)
	if !errors.Is(err, cause) {
		t.Fatal("拒绝错误丢失原始原因")
	}
	r := NewRegistry()
	r.Register(rejectedTool{err: err})
	result := r.Execute(context.Background(), schema.ToolCall{ID: "rejected", Name: "read_file", Arguments: json.RawMessage(`{}`)})
	if result.Status != schema.ToolRejected || result.Effect != schema.EffectNone || result.ErrorKind != "报告校验拒绝" || result.ErrorCode != "report_invalid" {
		t.Fatalf("拒绝分类丢失：%+v", result)
	}
}
