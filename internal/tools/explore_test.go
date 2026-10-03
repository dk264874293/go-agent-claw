package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadOnlyExploration(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{"a.txt": "字面 a.*\n第二行 a.*", "nested/b.txt": "a.* 嵌套", ".env.test": "a.* 测试密钥", "binary": "a.*\x00二进制"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("a.* 外部"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	list := NewListFilesTool(dir)
	output, _, err := list.ExecuteDetailed(context.Background(), json.RawMessage(`{}`))
	if err != nil || !strings.Contains(output, "nested") || strings.Contains(output, ".env.test") {
		t.Fatalf("目录浏览错误: %s %v", output, err)
	}
	search := NewSearchTextTool(dir)
	output, meta, err := search.ExecuteDetailed(context.Background(), json.RawMessage(`{"query":"a.*"}`))
	if err != nil || meta.Truncated || !strings.Contains(output, `"a.txt":2:`) || !strings.Contains(output, "嵌套") || strings.Contains(output, "测试密钥") || strings.Contains(output, "外部") || strings.Contains(output, "二进制") {
		t.Fatalf("只读搜索错误: %s %+v %v", output, meta, err)
	}
	_, meta, err = search.ExecuteDetailed(context.Background(), json.RawMessage(`{"query":"a.*","limit":1}`))
	if err != nil || !meta.Truncated {
		t.Fatal("匹配上限未标记")
	}
	_, meta, err = list.ExecuteDetailed(context.Background(), json.RawMessage(`{"limit":1}`))
	if err != nil || !meta.Truncated {
		t.Fatal("条目上限未标记")
	}
	for _, args := range []string{`{"query":""}`, `{"query":"x","path":"../"}`, `{"query":"x","path":"link"}`, `{"query":"x","path":"missing"}`, `{"query":"x","limit":-1}`, `null`} {
		if _, _, err := search.ExecuteDetailed(context.Background(), json.RawMessage(args)); err == nil {
			t.Errorf("非法搜索未拒绝: %s", args)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := search.ExecuteDetailed(ctx, json.RawMessage(`{"query":"a.*"}`)); err == nil {
		t.Fatal("搜索未响应取消")
	}
	defs := NewReadOnlyRegistry(dir).GetAvailableTools()
	if len(defs) != 3 {
		t.Fatalf("子任务工具错误: %+v", defs)
	}
	for _, def := range defs {
		if def.Name == "bash" || def.Name == "write_file" || def.Name == "spawn_subagent" {
			t.Fatal("子任务暴露写入能力")
		}
	}
}
