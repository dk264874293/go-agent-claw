package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/schema"
)

func TestQualityRejectedBatchArchivesOnlyBusinessReceipt(t *testing.T) {
	for _, phase := range []string{"main", "quality_revision"} {
		t.Run(phase, func(t *testing.T) {
			dir := t.TempDir()
			writeFixture(t, dir, "source.md", "版本二的材料事实。\n")
			calls := 0
			var rejected schema.Message
			model := reportModel(func(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
				calls++
				if phase == "quality_revision" && modelStage(ctx) == "main" {
					switch calls {
					case 1:
						return modelCalls(readCall("source-read", "source.md"))
					case 2:
						return qualityCall("verify_findings", "draft", qualityTestDraft(analysisIntegrationEvidence(ctx)[0]))
					}
				}
				if modelStage(ctx) != phase {
					return nil, fmt.Errorf("不应调用额外模型阶段：%s", modelStage(ctx))
				}
				rejected = schema.Message{
					Role: schema.RoleAssistant, Content: "仅归档这份可展示的错误候选与工具意图",
					ToolCalls: []schema.ToolCall{
						{ID: "invalid-verify", Name: "verify_findings", Arguments: json.RawMessage(`{"schema_version":1}`)},
						readCall("invalid-read", "source.md"),
					},
					ResponseItems: []json.RawMessage{
						json.RawMessage(`{"type":"reasoning","encrypted_content":"private-cipher-sentinel","summary":[{"type":"summary_text","text":"private-summary-sentinel"}]}`),
						json.RawMessage(`{"type":"future_kind","opaque":"private-future-sentinel"}`),
					},
					Usage: &schema.Usage{PromptTokens: 23, CompletionTokens: 7},
				}
				return &rejected, nil
			})
			session := ctxpkg.NewSession("质量拒绝白名单", dir)
			result, err := NewAgentEngine(model, NewWorkspaceRegistry(dir), EngineOptions{}).Run(context.Background(), session, qualitySpec(false), nil)
			models, tools := 1, 2
			if phase == "quality_revision" {
				models, tools = 3, 4
			}
			if err != nil || result.Status != Blocked || result.StopCode != "quality_contract_invalid" || calls != models || result.Usage.ModelCalls != models || result.Usage.ToolCalls != tools {
				t.Fatalf("拒绝结果、调用次数或预算改变：%+v calls=%d err=%v", result, calls, err)
			}
			found := false
			entries, err := os.ReadDir(filepath.Join(dir, ".claw", "tasks", result.TaskID, "artifacts"))
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				raw, err := os.ReadFile(filepath.Join(dir, ".claw", "tasks", result.TaskID, "artifacts", entry.Name()))
				if err != nil {
					t.Fatal(err)
				}
				for _, forbidden := range []string{"private-cipher-sentinel", "private-summary-sentinel", "private-future-sentinel", `"response_items"`} {
					if bytes.Contains(raw, []byte(forbidden)) {
						t.Fatalf("拒绝分支落盘完整响应或私密项：%s", forbidden)
					}
				}
				var fields map[string]json.RawMessage
				if json.Unmarshal(raw, &fields) != nil || string(fields["content"]) != fmt.Sprintf("%q", rejected.Content) {
					continue
				}
				if len(fields) != 2 || fields["tool_calls"] == nil {
					t.Fatalf("质量候选材料包含白名单外字段：%s", raw)
				}
				var receipt qualityModelReceipt
				if err := json.Unmarshal(raw, &receipt); err != nil || !reflect.DeepEqual(receipt.ToolCalls, rejected.ToolCalls) {
					t.Fatalf("业务工具身份或参数丢失：%+v err=%v", receipt, err)
				}
				found = true
			}
			if !found {
				t.Fatal("没有保存可审计的业务候选材料")
			}
			// 只收窄持久材料；原响应及Session中的opaque项仍可供协议层使用。
			retained := false
			for _, msg := range session.GetWorkingMemory(0) {
				if msg.Role == schema.RoleAssistant && msg.Content == rejected.Content {
					retained = reflect.DeepEqual(msg.ResponseItems, rejected.ResponseItems) && reflect.DeepEqual(msg.ToolCalls, rejected.ToolCalls)
				}
			}
			if !retained || len(rejected.ResponseItems) != 2 {
				t.Fatal("归档投影改写了原响应或Session的opaque项")
			}
		})
	}
}
