package engine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/taskstore"
)

type discoveryNoiseManifest struct {
	Synthetic   bool     `json:"synthetic"`
	Marker      string   `json:"marker"`
	Directories []string `json:"directories"`
	Files       []struct {
		Source string `json:"source"`
		Path   string `json:"path"`
		Kind   string `json:"kind"`
		Bytes  int    `json:"bytes"`
		SHA256 string `json:"sha256"`
	} `json:"files"`
}

func copyDiscoveryNoise(t *testing.T, dir string) discoveryNoiseManifest {
	t.Helper()
	base := filepath.Join("testdata", "task_analysis_v1", "noise")
	data, err := os.ReadFile(filepath.Join(base, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest discoveryNoiseManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if !manifest.Synthetic || manifest.Marker == "" || len(manifest.Files) != 3 {
		t.Fatal("噪声夹具的合成声明或规模不符")
	}
	for _, file := range manifest.Files {
		if !filepath.IsLocal(file.Source) || !filepath.IsLocal(file.Path) {
			t.Fatal("噪声夹具路径越界")
		}
		data, err := os.ReadFile(filepath.Join(base, file.Source))
		if err != nil {
			t.Fatal(err)
		}
		if len(data) != file.Bytes || fmt.Sprintf("%x", sha256.Sum256(data)) != file.SHA256 || !strings.Contains(string(data), manifest.Marker) {
			t.Fatalf("噪声夹具已改变: %s", file.Source)
		}
		target := filepath.Join(dir, file.Path)
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range manifest.Directories {
		if !filepath.IsLocal(path) {
			t.Fatal("噪声目录越界")
		}
		if err := os.MkdirAll(filepath.Join(dir, path), 0700); err != nil {
			t.Fatal(err)
		}
	}
	return manifest
}

// 实际工具完成发现和读取，脚本模型只控制流程；验收独立读取持久回执。
func TestDiscoveryFixtureFindsAndReadsMarkdownWithoutBinaryNoise(t *testing.T) {
	dir := t.TempDir()
	fixture := copyAnalysisFixture(t, dir)
	noise := copyDiscoveryNoise(t, dir)
	const agentID = "discovery-fixture-root"
	requests := 0
	provider := scriptedProvider(func(ctx context.Context, messages []schema.Message, defs []schema.ToolDefinition) (*schema.Message, error) {
		requests++
		for _, message := range messages {
			if strings.Contains(message.Content, noise.Marker) {
				return nil, fmt.Errorf("二进制噪声进入模型材料")
			}
		}
		if requests == 1 {
			visible := false
			for _, tool := range defs {
				visible = visible || tool.Name == "find_files"
			}
			if !visible {
				return nil, fmt.Errorf("模型看不到文件发现工具")
			}
			return modelCalls(schema.ToolCall{ID: "discover", Name: "find_files", Arguments: json.RawMessage(`{"path":".","name_glob":"*.md","limit":200}`)})
		}
		if requests == 2 {
			last := messages[len(messages)-1]
			if last.ToolCallID != "discover" || last.ToolStatus != schema.ToolSucceeded {
				return nil, fmt.Errorf("没有取得文件发现结果: %+v", last)
			}
			for _, file := range fixture.Files {
				if !strings.Contains(last.Content, file.Path) {
					return nil, fmt.Errorf("发现结果缺少 %s", file.Path)
				}
			}
			if strings.Contains(last.Content, "directory-with-md-name.md") || strings.Contains(last.Content, "image.png") || strings.Contains(last.Content, "lecture.pdf") {
				return nil, fmt.Errorf("文件发现包含附件或目录")
			}
		}
		reads := currentRun(ctx).agentReads(agentID)
		var calls []schema.ToolCall
		for _, file := range fixture.Files {
			var end int64
			for _, coverage := range mergeReadCoverage(reads) {
				if coverage.Path == file.Path && len(coverage.Ranges) > 0 && coverage.Ranges[0].Start == 0 {
					end = coverage.Ranges[0].End
				}
			}
			if end < int64(file.Bytes) {
				args, _ := json.Marshal(map[string]any{"path": file.Path, "offset": end, "limit": 8000})
				calls = append(calls, schema.ToolCall{ID: fmt.Sprintf("read-%s-%d", file.Path, end), Name: "read_file", Arguments: args})
			}
		}
		if len(calls) > 0 {
			return modelCalls(calls...)
		}
		return reply("已读取全部15份合成Markdown，文件清单与来源范围已记录；内容结论仍需独立核实。")
	})
	registry := NewWorkspaceRegistry(dir)
	engine := NewAgentEngine(provider, registry, EngineOptions{})
	ctx := observe.WithAgent(context.Background(), agentID, "")
	result, err := engine.Run(ctx, ctxpkg.NewSession("发现与读取离线夹具", dir), TaskSpec{Goal: "分析工作区的Markdown文档"}, nil)
	if err != nil || result.Status != Answered || result.ExitCode() != 0 || result.PartialReport != nil || result.FinalText == "" || result.Usage.ModelCalls != 4 || result.Usage.ToolCalls != 18 || requests != 4 {
		t.Fatalf("正常发现读取未收敛: %+v %v", result, err)
	}
	store, err := taskstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	task, err := store.OpenTask(result.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	defer task.Close()
	events, err := task.Events()
	if err != nil {
		t.Fatal(err)
	}
	var receipts []schema.ReadReceipt
	finds := 0
	for _, event := range events {
		if event.Kind != "action_result" {
			continue
		}
		var state taskState
		if err := json.Unmarshal(event.Payload, &state); err != nil {
			t.Fatal(err)
		}
		action := state.LastAction
		if action == nil || action.Result.Status != schema.ToolSucceeded {
			t.Fatalf("夹具动作不成功: %+v", action)
		}
		if strings.Contains(action.Result.Output, noise.Marker) {
			t.Fatal("噪声正文进入持久工具结果")
		}
		switch action.Tool {
		case "find_files":
			finds++
		case "read_file":
			if action.Result.Read == nil || action.Result.Read.AgentID != agentID || action.Result.Read.ActionID != action.ID {
				t.Fatalf("读取回执身份不符: %+v", action)
			}
			receipts = append(receipts, *action.Result.Read)
		default:
			t.Fatalf("正常流程出现额外探索或副作用工具: %s", action.Tool)
		}
	}
	coverage := mergeReadCoverage(receipts)
	if finds != 1 || len(receipts) != 17 || len(coverage) != 15 {
		t.Fatalf("独立发现/覆盖验收失败: finds=%d reads=%d files=%d", finds, len(receipts), len(coverage))
	}
	var bytes int64
	for _, read := range receipts {
		bytes += read.ByteEnd - read.ByteStart
	}
	if bytes != int64(fixture.TotalBytes) {
		t.Fatalf("发生缺读或重复读取: %d", bytes)
	}
	for _, entry := range coverage {
		if len(entry.Ranges) != 1 || entry.Ranges[0].Start != 0 || entry.Ranges[0].End != entry.Total {
			t.Fatalf("文件没有完整覆盖: %+v", entry)
		}
	}
	t.Logf("15份Markdown / %d源字节 / 1次发现+17次读取 / 4次脚本模型调用，未调用付费模型", bytes)
}

func TestDiscoveryNoiseSearchIsNotReturnedToModel(t *testing.T) {
	dir := t.TempDir()
	copyAnalysisFixture(t, dir)
	noise := copyDiscoveryNoise(t, dir)
	calls := 0
	provider := scriptedProvider(func(_ context.Context, messages []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		if calls == 1 {
			args, _ := json.Marshal(map[string]any{"path": ".", "query": noise.Marker, "limit": 100})
			return modelCalls(schema.ToolCall{ID: "noise-search", Name: "search_text", Arguments: args})
		}
		last := messages[len(messages)-1]
		if last.ToolCallID != "noise-search" || last.ToolStatus != schema.ToolSucceeded || strings.Contains(last.Content, noise.Marker) || strings.Contains(last.Content, ".png:") || strings.Contains(last.Content, ".pdf:") {
			return nil, fmt.Errorf("搜索返回了二进制正文: %s", last.Content)
		}
		return reply("附件及二进制未作为文本匹配返回。")
	})
	engine := NewAgentEngine(provider, NewWorkspaceRegistry(dir), EngineOptions{})
	result, err := engine.Run(context.Background(), ctxpkg.NewSession("附件搜索隔离", dir), TaskSpec{Goal: "检查工作区文本搜索的二进制隔离"}, nil)
	if err != nil || result.Status != Answered || result.Usage.ToolCalls != 1 || result.Usage.ModelCalls != 2 || calls != 2 {
		t.Fatalf("搜索隔离失败: %+v %v", result, err)
	}
}
