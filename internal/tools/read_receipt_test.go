package tools

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

func TestReadReceiptTracksConsumedSourceBytes(t *testing.T) {
	dir := t.TempDir()
	source := strings.Repeat("中文\n", 1500)
	if err := os.WriteFile(filepath.Join(dir, "source.txt"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := WithScope(context.Background(), ExecutionScope{Mode: ModeReadOnly, WorkDir: dir})
	registry := NewRegistry()
	registry.Register(NewReadFileTool(dir))
	var offset int64
	for page := 0; offset < int64(len(source)); page++ {
		args, _ := json.Marshal(map[string]any{"path": "source.txt", "offset": offset, "line_numbers": true})
		result := registry.Execute(ctx, schema.ToolCall{ID: fmt.Sprint(page), Name: "read_file", Arguments: args})
		read := result.Read
		if result.Status != schema.ToolSucceeded || read == nil {
			t.Fatalf("成功读取缺少结构化回执: %+v", result)
		}
		if read.Path != "source.txt" || read.Version != fmt.Sprintf("%x", sha256.Sum256([]byte(source))) || read.ByteStart != offset || read.ByteEnd <= offset || read.TotalBytes != int64(len(source)) || read.FirstLine != 1+strings.Count(source[:offset], "\n") {
			t.Fatalf("回执与实际源字节不符: %+v", read)
		}
		if !strings.Contains(result.Output, fmt.Sprintf("字节范围: [%d, %d)", offset, read.ByteEnd)) {
			t.Fatalf("回执与兼容元数据头不一致: %+v", read)
		}
		offset = read.ByteEnd
	}
	result := registry.Execute(ctx, schema.ToolCall{ID: "missing", Name: "read_file", Arguments: json.RawMessage(`{"path":"missing"}`)})
	if result.Read != nil || result.Status == schema.ToolSucceeded {
		t.Fatalf("失败读取误造回执: %+v", result)
	}
}

func TestReadReceiptEmptyFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "empty"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	ctx := WithScope(context.Background(), ExecutionScope{Mode: ModeReadOnly, WorkDir: dir})
	registry := NewRegistry()
	registry.Register(NewReadFileTool(dir))
	result := registry.Execute(ctx, schema.ToolCall{ID: "empty", Name: "read_file", Arguments: json.RawMessage(`{"path":"empty"}`)})
	if result.Status != schema.ToolSucceeded || result.Read == nil || result.Read.FirstLine != 0 || result.Read.LastLine != 0 || result.Read.ByteEnd != 0 {
		t.Fatalf("空文件回执错误: %+v", result)
	}
}
