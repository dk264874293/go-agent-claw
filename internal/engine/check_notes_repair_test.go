package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/quality"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func TestLocalCheckAdaptiveBindingCorrectionUsesActualDiagnostic(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "source.md", "只验证给定测试公式，不代表运行原文代码。\n")
	calls := 0
	var input map[string]any
	p := scriptedProvider(func(ctx context.Context, messages []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		switch calls {
		case 1:
			return modelCalls(readCall("read", "source.md"))
		case 2:
			e := analysisIntegrationEvidence(ctx)[0]
			input = map[string]any{"kind": "arithmetic", "subject": map[string]any{"file_ref": e.Receipt.FileRef, "start_line": 1, "end_line": 1}, "expression": "a+b", "premise": "translated", "cases": []any{map[string]any{"inputs": map[string]string{"a": "2", "b": "3"}, "expected": "5"}}}
		case 3:
			text := messages[len(messages)-1].Content
			start := strings.Index(text, "{")
			var response struct {
				Checks []struct {
					Result     string
					Diagnostic *quality.InputDiagnostic
				}
			}
			if start < 0 || json.Unmarshal([]byte(text[start:]), &response) != nil || len(response.Checks) != 1 || response.Checks[0].Diagnostic == nil {
				return nil, fmt.Errorf("没有收到真实结构化诊断: %s", text)
			}
			diagnostic := response.Checks[0].Diagnostic
			if diagnostic.Code != "binding_identity_missing" || !strings.Contains(diagnostic.Hint, "扩大subject") {
				return nil, fmt.Errorf("纠正提示不具体: %+v", diagnostic)
			}
			bindings := map[string]any{}
			for _, name := range diagnostic.Variables {
				bindings[name] = map[string]any{"kind": "test_input"}
			}
			input["bindings"] = bindings
		case 4:
			if !strings.Contains(messages[len(messages)-1].Content, "\"result\":\"pass\"") {
				return nil, fmt.Errorf("修正未通过实际检查")
			}
			return reply("给定测试样本加总为5；未执行资料代码，原文前提未核实。")
		default:
			return nil, fmt.Errorf("额外模型阶段")
		}
		raw, _ := json.Marshal(map[string]any{"checks": []any{input}})
		return modelCalls(schema.ToolCall{ID: fmt.Sprintf("check-%d", calls), Name: "check_evidence", Arguments: raw})
	})
	spec := TaskSpec{Goal: "核对给定数学样本", Mode: tools.ModeReadOnly, Kind: TaskAnswer, ReadContextVersion: 4, ReadFeatures: &ReadFeatureSpec{Version: 1, LocalChecks: true}}
	result, err := NewAgentEngine(p, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeReadOnly, Budget: Budget{MaxModelCalls: 4, MaxTurns: 4, MaxToolCalls: 4}}).Run(context.Background(), ctxpkg.NewSession("绑定纠正", dir), spec, nil)
	if err != nil || result.Status != Answered || calls != 4 || result.Usage.ToolCalls != 3 || len(result.LocalChecks) != 2 || result.LocalChecks[0].ReasonCode != "binding_identity_missing" || result.LocalChecks[1].Result != "pass" {
		t.Fatalf("纠正、预算或持久分类失败: %+v %v", result, err)
	}
}

func TestWorkNotesSuggestSmallIncrementAndOversizeFeedback(t *testing.T) {
	ctx, r, messages := memoryV2Fixture(t)
	prepared, _, _, err := NewAgentEngine(nil, nil, EngineOptions{}).prepareAnalysisRequest(ctx, r, messages, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"suggested_increment", "suggested_note_bytes", "carrier_limit_bytes", "不要求本次处理全部候选"} {
		if !strings.Contains(prepared[0].Content, want) {
			t.Fatalf("未提供有界增量机会 %s", want)
		}
	}
	detail := workNotesReceiptDetail("too_large", false, AnalysisReport{}, nil)
	receipt := boundWorkNotesReceipt(detail)
	if len(receipt) > 512 || !strings.Contains(receipt, "limit_bytes") || !strings.Contains(receipt, "本次整份笔记未接受") {
		t.Fatalf("超限回执不可纠正: %s", receipt)
	}
	if len(r.state.ReadMemory.ProcessedReads["root"]) != 0 {
		t.Fatal("仅给出建议就授予处理资格")
	}
	// 已处理小增量先折叠，剩余原文完整保留；不增加专用笔记调用。
	r.markAnalysisDelivered(ctx, []string{"E1", "E2"})
	b, err := r.consumeWorkNotesCarriers(ctx, []schema.ToolCall{carrierCall("notes", carrierSummary("E1", "来源一的明确事实和条件；来源二仍待处理"))})
	if err != nil || !b.valid {
		t.Fatalf("最小增量未采纳: %+v %v", b, err)
	}
	next, _, stats, err := NewAgentEngine(nil, nil, EngineOptions{}).prepareAnalysisRequest(ctx, r, messages, nil)
	if err != nil || stats.folded != 1 || next[5].Content != messages[5].Content {
		t.Fatalf("增量没有折叠旧正文或错误删除未处理内容: %+v %v", stats, err)
	}
}

func TestDeliverySourcePositionHintHasExplicitLimits(t *testing.T) {
	facts := []deliveryPathFact{{location: schema.ContentSourceLocation{Path: "材料/指南.md", EvidenceID: "E1"}}}
	prose := strings.Repeat("这是需要提供依据的分析说明。", 60)
	got := reviewDeliveryMarkdown(prose, facts)
	if len(got) != 1 || got[0].Code != "source_locations_absent" {
		t.Fatalf("长正文无位置未提示: %+v", got)
	}
	for _, text := range []string{prose + "（材料/指南.md:3-5）", prose + "（材料/指南.md：3）", "简短答复", "{\"业务数据\":\"" + prose + "\"}", strings.Repeat("\x60", 3) + "text\n" + prose + "\n" + strings.Repeat("\x60", 3)} {
		if issues := reviewDeliveryMarkdown(text, facts); len(issues) != 0 {
			t.Fatalf("业务JSON、围栏或有位置正文被误报: %+v", issues)
		}
	}
	if len(reviewDeliveryMarkdown(prose, nil)) != 0 {
		t.Fatal("没有当前来源仍伪造引用要求")
	}
}
