package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func TestLocalCheckBatchAdmissionKeepsReadsAndRejectsMutations(t *testing.T) {
	r := &runState{state: taskState{Spec: TaskSpec{ReadContextVersion: 3}}}
	ctx := context.WithValue(context.Background(), localCheckBatchKey{}, true)
	for _, name := range []string{"write_file", "edit_file", "bash", "write_report"} {
		result := r.rejectLocalCheckCall(ctx, schema.ToolCall{ID: "write", Name: name})
		if result == nil || result.Status != schema.ToolRejected || result.Effect != schema.EffectNone || result.ErrorCode != "check_result_pending" {
			t.Fatalf("同批写入没有阻止: %s %+v", name, result)
		}
	}
	for _, name := range []string{"read_file", "read_evidence", "check_evidence"} {
		if result := r.rejectLocalCheckCall(ctx, schema.ToolCall{Name: name}); result != nil {
			t.Fatalf("独立核对或读取被误拒绝: %s", name)
		}
	}
	r.state.Spec.ReadContextVersion = 2
	if result := r.rejectLocalCheckCall(context.Background(), schema.ToolCall{Name: "check_evidence"}); result == nil || result.ErrorCode != "local_check_unavailable" {
		t.Fatal("旧策略可绕过可见性调用新工具")
	}
}

func TestLocalCheckBatchRejectsMutationBeforeResumeReceiptReuse(t *testing.T) {
	call := schema.ToolCall{ID: "write", Name: "write_file", Arguments: []byte(`{"path":"out.md","content":"x"}`)}
	r := &runState{started: time.Now(), state: taskState{Spec: TaskSpec{ReadContextVersion: 3}, Limits: Budget{MaxToolCalls: 2}}, resumeReceipts: map[string]schema.ToolResult{actionKey("", call): schema.NewToolResult("old", "历史已写入", schema.ToolSucceeded, schema.EffectConfirmed, "", nil)}}
	ctx := context.WithValue(context.Background(), localCheckBatchKey{}, true)
	result, err := NewAgentEngine(nil, nil, EngineOptions{}).execute(ctx, r, tools.NewRegistry(), nil, call, "", false)
	if err != nil || result.ErrorCode != "check_result_pending" || result.Status != schema.ToolRejected || r.state.Usage.ToolCalls != 1 || len(r.state.Pending) != 0 {
		t.Fatalf("恢复回执绕过同批核对准入: %+v %v", result, err)
	}
}

func TestLocalChecksDeliveryDoesNotBecomeSemanticVerification(t *testing.T) {
	r := RunResult{RunID: "current", Status: NeedsVerification, Reason: "尚待验收", LocalChecks: []LocalCheckSummary{{ID: "C1", OriginRunID: "old", Kind: "json", Result: "pass", PremiseResult: "pass"}}}
	text := FormatRunResult(r)
	if !strings.Contains(text, "历史运行") || !strings.Contains(text, "不代表语义或运行验收") || strings.Contains(text, "验收已通过") || r.Status != NeedsVerification {
		t.Fatal(text)
	}
}
