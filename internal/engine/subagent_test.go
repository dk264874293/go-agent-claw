package engine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func TestSubagentReturnsOnlyReadEvidence(t *testing.T) {
	dir := t.TempDir()
	const source = "第一行\n第二行\n"
	writeFixture(t, dir, "source.txt", source)
	version := fmt.Sprintf("%x", sha256.Sum256([]byte(source)))
	rootCalls, childCalls := 0, 0
	provider := scriptedProvider(func(_ context.Context, messages []schema.Message, defs []schema.ToolDefinition) (*schema.Message, error) {
		if strings.HasPrefix(messages[0].Content, "你是只读代码探路者") {
			childCalls++
			for _, d := range defs {
				if !tools.IsReadOnly(d.Name) {
					t.Fatalf("子任务看到非只读工具: %s", d.Name)
				}
			}
			if childCalls == 1 {
				return modelCalls(readCall("read", "source.txt"))
			}
			body, _ := json.Marshal(tools.SubtaskReport{
				Findings: "读取了源文件",
				Evidence: []tools.EvidenceRef{
					{Path: "source.txt", Line: 2, Version: version},
					{Path: "source.txt", Line: 100, Version: version},
				},
			})
			return reply(string(body))
		}
		rootCalls++
		if rootCalls == 1 {
			args, _ := json.Marshal(tools.SubtaskSpec{Goal: "读取源文件", AllowedPaths: []string{"source.txt"}})
			return modelCalls(schema.ToolCall{ID: "delegate", Name: "spawn_subagent", Arguments: args})
		}
		last := messages[len(messages)-1]
		_, body, ok := strings.Cut(last.Content, "\n")
		var report tools.SubtaskReport
		if !ok || json.Unmarshal([]byte(body), &report) != nil {
			t.Fatalf("父任务未收到可解析报告: %s", last.Content)
		}
		if report.Status != "PARTIAL" || len(report.Evidence) != 1 || report.Evidence[0].Line != 2 || len(report.Unverified) != 1 {
			t.Fatalf("未正确核对实际读取行: %+v", report)
		}
		return reply("已核对第二行。")
	})
	registry := NewWorkspaceRegistry(dir)
	engine := NewAgentEngine(provider, registry, EngineOptions{Budget: Budget{MaxModelCalls: 4}})
	registry.Register(tools.NewSubagentTool(engine, NewExplorerRegistry(dir), nil))
	result, err := engine.Run(context.Background(), ctxpkg.NewSession("子任务", dir), TaskSpec{Goal: "确认第二行"}, nil)
	if err != nil || result.Status != Answered || result.Usage.ModelCalls != 4 || result.Usage.ToolCalls != 2 {
		t.Fatalf("主子任务未按同一预算结算: %+v, %v", result, err)
	}
}
