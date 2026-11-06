package engine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func TestDecodeSubtaskFindingsCompatibility(t *testing.T) {
	for _, tc := range []struct{ name, findings, expected string }{
		{"字符串", `"发现"`, "发现"},
		{"对象", `{"z":9007199254740993,"a":"发现"}`, `{"a":"发现","z":9007199254740993}`},
		{"数组", `[{"z":2,"a":1},"发现"]`, `[{"a":1,"z":2},"发现"]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report, err := decodeSubtaskReport("```json\n" + `{"findings":` + tc.findings + `,"evidence":[],"unverified":[],"status":"VERIFIED"}` + "\n```")
			if err != nil || report.Findings != tc.expected || report.Status != "" {
				t.Fatalf("兼容转换错误或采用了模型状态: %+v %v", report, err)
			}
		})
	}
	for _, body := range []string{`{}`, `{"findings":null}`, `{"findings":1}`, `{"findings":true}`, `{"findings":" "}`, `{"findings":"ok","evidence":"wrong"}`, `{"findings":"ok"} {}`, `{"findings":`} {
		if _, err := decodeSubtaskReport(body); err == nil {
			t.Fatalf("接受无效报告 %s", body)
		}
	}
}

func readCatalogFixture(t *testing.T, r *runState, ctx context.Context, dir, name string, offset, limit int) schema.ReadReceipt {
	t.Helper()
	args, _ := json.Marshal(map[string]any{"path": name, "offset": offset, "limit": limit})
	result := NewExplorerRegistry(dir).Execute(ctx, schema.ToolCall{ID: fmt.Sprint(offset), Name: "read_file", Arguments: args})
	if result.Status != schema.ToolSucceeded || result.Read == nil {
		t.Fatalf("读取夹具失败: %+v", result)
	}
	bindRead(ctx, fmt.Sprintf("action-%s-%d", name, offset), &result)
	r.mu.Lock()
	r.rememberReadLocked(result)
	r.mu.Unlock()
	return *result.Read
}

func TestReadCatalogMergesRangesFiltersScopeAndInvalidatesReports(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "a.txt", "0123456789")
	writeFixture(t, dir, "b.txt", "秘密")
	r := &runState{}
	parent := tools.WithScope(observe.WithAgent(context.Background(), "parent", ""), tools.ExecutionScope{Mode: tools.ModeReadOnly, WorkDir: dir})
	child := observe.WithAgent(parent, "child", "parent")
	first := readCatalogFixture(t, r, child, dir, "a.txt", 0, 3)
	readCatalogFixture(t, r, child, dir, "a.txt", 3, 2)
	readCatalogFixture(t, r, child, dir, "a.txt", 7, 2)
	readCatalogFixture(t, r, child, dir, "b.txt", 0, 6)
	merged := mergeReadCoverage(r.agentReads("child"))
	var ranges []catalogRange
	for _, c := range merged {
		if c.Path == "a.txt" {
			ranges = c.Ranges
		}
	}
	if len(ranges) != 2 || ranges[0].Start != 0 || ranges[0].End != 5 || ranges[1].Start != 7 || ranges[1].End != 9 {
		t.Fatalf("跨未读缺口错误合并: %+v", ranges)
	}
	r.rememberSubreport("parent", tools.SubtaskReport{Findings: "已核对的子报告", Evidence: []tools.EvidenceRef{{Path: "a.txt", Line: 1, Version: first.Version}}, Status: "SUCCEEDED"}, r.agentReads("child"))
	if got := r.readContext(parent); !strings.Contains(got, "已核对的子报告") || !strings.Contains(got, `"byte`) && !strings.Contains(got, `"start":0`) {
		t.Fatalf("父任务缺少可复用报告和覆盖: %s", got)
	}
	sibling := tools.WithScope(observe.WithAgent(parent, "sibling", "parent"), tools.ExecutionScope{Mode: tools.ModeReadOnly, WorkDir: dir, AllowedPaths: []string{"a.txt"}})
	if got := r.readContext(sibling); strings.Contains(got, "b.txt") || strings.Contains(got, "已核对的子报告") || !strings.Contains(got, "a.txt") {
		t.Fatalf("兄弟任务泄漏报告或越界路径: %s", got)
	}
	if got := r.readContext(parent); !strings.Contains(got, "b.txt") {
		t.Fatalf("子任务范围过滤不应销毁父任务数据: %s", got)
	}
	writeFixture(t, dir, "a.txt", "changed")
	if got := r.readContext(parent); strings.Contains(got, "已核对的子报告") || !strings.Contains(got, "已失效") {
		t.Fatalf("沿用陈旧摘要或版本: %s", got)
	}
	r.mu.Lock()
	r.invalidateReadsLocked("", true)
	r.mu.Unlock()
	if got := r.readContext(parent); strings.Contains(got, "读取覆盖：") || !strings.Contains(got, "已失效") {
		t.Fatal("Bash 后可复用覆盖目录应清空，并明确旧记录失效")
	}
}

func TestReadContextBoundAndConcurrentFormatting(t *testing.T) {
	var reads []schema.ReadReceipt
	var reports []tools.SubtaskReport
	for i := 0; i < 200; i++ {
		reads = append(reads, schema.ReadReceipt{Path: fmt.Sprintf("%03d.txt", i), Version: strings.Repeat("a", 64), ByteEnd: 1, FirstLine: 1, LastLine: 1, TotalBytes: 1})
		reports = append(reports, tools.SubtaskReport{Findings: "重要发现" + strings.Repeat("中", 1000), Unverified: []string{strings.Repeat("文", 1000)}, Coverage: reads})
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got := formatReadContext(reads, reports, true)
			if len(got) > readContextLimit || strings.Count(got, "读取覆盖：")+strings.Count(got, "子报告摘要（可能节选）：") > readContextEntries {
				t.Errorf("上下文超出界限: %d", len(got))
			}
			if !strings.Contains(got, "重要发现") || strings.Contains(got, `"coverage"`) {
				t.Error("完整回执挤掉摘要或被重复注入")
			}
		}()
	}
	wg.Wait()
	if len(reports[0].Unverified[0]) != 3000 {
		t.Fatal("格式化修改了共享报告")
	}
}

func TestResumeDoesNotPromoteHistoricalReadToCurrentCatalog(t *testing.T) {
	r := &runState{state: taskState{LastAction: &actionRecord{Result: schema.ToolResult{Read: &schema.ReadReceipt{Path: "old.txt", Version: "old"}}}}}
	if got := r.readContext(context.Background()); strings.Contains(got, "old.txt") || !strings.Contains(got, "当前没有有效") {
		t.Fatalf("历史输出被当作本次已核对证据: %s", got)
	}
}

func TestSubtaskInvalidReportKeepsArtifactAndTypedCoverage(t *testing.T) {
	for _, turns := range []int{1, 2} {
		t.Run(fmt.Sprint(turns), func(t *testing.T) {
			dir := t.TempDir()
			writeFixture(t, dir, "source.txt", "第一行\n第二行")
			calls := 0
			provider := scriptedProvider(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
				calls++
				if calls == 1 {
					return modelCalls(readCall("read", "source.txt"))
				}
				return reply(`{"findings":`)
			})
			engine := NewAgentEngine(provider, NewExplorerRegistry(dir), EngineOptions{})
			var artifact []byte
			r := &runState{state: taskState{Limits: Budget{}.defaults()}, subSlots: make(chan struct{}, 2), artifact: func(_ string, data []byte) error { artifact = data; return nil }}
			ctx := context.WithValue(tools.WithScope(context.Background(), tools.ExecutionScope{Mode: tools.ModeReadOnly, WorkDir: dir}), runContextKey{}, r)
			report, err := engine.RunSub(ctx, tools.SubtaskSpec{Goal: "读取", MaxTurns: turns}, NewExplorerRegistry(dir), nil)
			if err == nil || report.Status == "SUCCEEDED" || report.ArtifactID == "" || len(report.Coverage) != 1 || len(report.Evidence) != 1 || !json.Valid(artifact) {
				t.Fatalf("失败丢失材料或误报成功: %+v %v %s", report, err, artifact)
			}
			if !strings.Contains(string(artifact), "source.txt") || report.Coverage[0].ActionID == "" || report.Coverage[0].AgentID == "" {
				t.Fatalf("缺少完整动作身份: %+v %s", report, artifact)
			}
		})
	}
}

func TestSubtaskArtifactStorageFailureStopsRoot(t *testing.T) {
	dir := t.TempDir()
	provider := scriptedProvider(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
		return reply("invalid")
	})
	r := &runState{state: taskState{Limits: Budget{}.defaults()}, subSlots: make(chan struct{}, 2), artifact: func(string, []byte) error { return errors.New("磁盘错误") }}
	ctx := context.WithValue(tools.WithScope(context.Background(), tools.ExecutionScope{Mode: tools.ModeReadOnly, WorkDir: dir}), runContextKey{}, r)
	engine := NewAgentEngine(provider, NewExplorerRegistry(dir), EngineOptions{})
	_, err := engine.RunSub(ctx, tools.SubtaskSpec{Goal: "读取"}, NewExplorerRegistry(dir), nil)
	if !errors.Is(err, errStorage) || !errors.Is(r.storageError(), errStorage) {
		t.Fatalf("子任务关键证据错误被吞掉: %v %v", err, r.storageError())
	}
}

func TestFailedSubreportMaterialDoesNotCopyPrivateSession(t *testing.T) {
	dir := t.TempDir()
	session := ctxpkg.NewSession("材料边界", dir)
	session.Append(schema.Message{Role: schema.RoleSystem, Content: "系统请求私密标记"})
	session.Append(schema.Message{Role: schema.RoleUser, Content: "用户请求私密标记"})
	session.Append(schema.Message{
		Role: schema.RoleAssistant, Content: "尚未整理的公开发现",
		ResponseItems: []json.RawMessage{json.RawMessage(`{"type":"reasoning","encrypted_content":"私密思考标记"}`)},
		ToolCalls:     []schema.ToolCall{{ID: "read", Name: "read_file", Arguments: json.RawMessage(`{"path":"工具参数私密标记"}`)}},
	})
	session.Append(schema.Message{Role: schema.RoleUser, ToolCallID: "read", Content: "完整工具正文私密标记"})
	var artifact []byte
	r := &runState{artifact: func(_ string, data []byte) error { artifact = data; return nil }}
	reads := []schema.ReadReceipt{{Path: "source.txt", Version: "版本", ByteStart: 0, ByteEnd: 4, TotalBytes: 4}}
	report, err := r.failedSubreport(context.Background(), session, "原始格式错误报告", reads, string(Blocked), errors.New("子任务轮次耗尽"))
	if err == nil || report.ArtifactID == "" {
		t.Fatalf("失败材料缺失: %+v %v", report, err)
	}
	var saved struct {
		Response   string               `json:"response"`
		Fragments  []string             `json:"assistant_fragments"`
		Reads      []schema.ReadReceipt `json:"reads"`
		StopReason string               `json:"stop_reason"`
	}
	if json.Unmarshal(artifact, &saved) != nil || saved.Response != "原始格式错误报告" || len(saved.Fragments) != 1 || saved.Fragments[0] != "尚未整理的公开发现" || len(saved.Reads) != 1 || saved.Reads[0].Path != "source.txt" || saved.StopReason != "子任务轮次耗尽" {
		t.Fatalf("未保留所需的公开报告、片段、覆盖和停止原因: %s", artifact)
	}
	for _, forbidden := range []string{"系统请求私密标记", "用户请求私密标记", "私密思考标记", "工具参数私密标记", "完整工具正文私密标记", "response_items", `"messages"`} {
		if strings.Contains(string(artifact), forbidden) {
			t.Fatalf("失败材料复制了私密会话字段 %s: %s", forbidden, artifact)
		}
	}
}

func TestParentReusesObjectReportWithoutRereading(t *testing.T) {
	dir := t.TempDir()
	const source = "第一行\n第二行"
	writeFixture(t, dir, "source.txt", source)
	version := fmt.Sprintf("%x", sha256.Sum256([]byte(source)))
	rootCalls, subCalls := 0, 0
	provider := scriptedProvider(func(_ context.Context, messages []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		if strings.HasPrefix(messages[0].Content, "你是只读代码探路者") {
			subCalls++
			if subCalls == 1 {
				return modelCalls(readCall("read", "source.txt"))
			}
			return reply(fmt.Sprintf(`{"findings":{"发现":"第二行有效"},"evidence":[{"path":"source.txt","line":2,"version":%q}],"unverified":[],"status":"VERIFIED"}`, version))
		}
		rootCalls++
		if rootCalls == 1 {
			return modelCalls(schema.ToolCall{ID: "delegate", Name: "spawn_subagent", Arguments: json.RawMessage(`{"task_prompt":"读取源文件","allowed_paths":["source.txt"]}`)})
		}
		if !strings.Contains(messages[0].Content, "第二行有效") || !strings.Contains(messages[0].Content, "读取覆盖") {
			t.Fatalf("请求缺少可复用报告或覆盖: %s", messages[0].Content)
		}
		return reply("根据子任务已核对的证据，第二行有效。")
	})
	registry := NewWorkspaceRegistry(dir)
	engine := NewAgentEngine(provider, registry, EngineOptions{})
	registry.Register(tools.NewSubagentTool(engine, NewExplorerRegistry(dir), nil))
	result, err := engine.Run(context.Background(), ctxpkg.NewSession("复用", dir), TaskSpec{Goal: "核对源文件", Delegation: "on"}, nil)
	if err != nil || result.Status != Answered || result.Usage.ModelCalls != 4 || result.Usage.ToolCalls != 2 {
		t.Fatalf("报告格式兼容或复用失败: %+v %v", result, err)
	}
}
