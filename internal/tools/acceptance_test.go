package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/dk264874293/go-agent-claw/internal/executor"
	"github.com/dk264874293/go-agent-claw/internal/observability"
	"github.com/dk264874293/go-agent-claw/internal/policy"
	"github.com/dk264874293/go-agent-claw/internal/schema"
)

func TestAcceptanceEditLevelsAndAtomicFailure(t *testing.T) {
	for _, tc := range []struct {
		name, original, old, want string
		fail                      bool
	}{
		{"精确", "前\n目标\n后", "目标", "前\n替换\n后", false},
		{"换行", "前\r\n甲\r\n乙\r\n后", "甲\n乙", "前\n替换\n后", false},
		{"首尾空白", "前\n目标\n后", " \n目标\n ", "前\n替换\n后", false},
		{"逐行缩进", "前\n  甲\n    乙\n后", "甲\n乙", "前\n替换\n后", false},
		{"精确歧义", "甲\n甲", "甲", "", true},
		{"模糊歧义", "  甲\n  乙\n    甲\n    乙", "甲\n乙", "", true},
		{"不存在", "甲", "乙", "", true},
		{"空片段", "甲", "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "file")
			if err := os.WriteFile(path, []byte(tc.original), 0750); err != nil {
				t.Fatal(err)
			}
			args, _ := json.Marshal(editFileArgs{Path: "file", OldText: tc.old, NewText: "替换"})
			_, err := NewEditFileTool(dir).Execute(context.Background(), args)
			if (err != nil) != tc.fail {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			want := tc.want
			if tc.fail {
				want = tc.original
			}
			if string(data) != want {
				t.Fatalf("内容不符: %q", data)
			}
			info, _ := os.Stat(path)
			if info.Mode().Perm() != 0750 {
				t.Fatal("权限改变")
			}
			entries, _ := os.ReadDir(dir)
			if len(entries) != 1 {
				t.Fatal("临时文件泄漏")
			}
		})
	}
	t.Run("扩展超限", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "file")
		_ = os.WriteFile(path, []byte("甲"), 0600)
		args, _ := json.Marshal(editFileArgs{Path: "file", OldText: "甲", NewText: strings.Repeat("x", 1024*1024+1)})
		if _, err := NewEditFileTool(dir).Execute(context.Background(), args); err == nil {
			t.Fatal("超限写入未拒绝")
		}
		data, _ := os.ReadFile(path)
		if string(data) != "甲" {
			t.Fatal("失败修改了原文件")
		}
	})
}

func TestAcceptanceExplorationLimits(t *testing.T) {
	t.Run("扫描数量", func(t *testing.T) {
		dir := t.TempDir()
		for i := 0; i < 1100; i++ {
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprint(i)), []byte("无匹配"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		_, meta, err := NewSearchTextTool(dir).ExecuteDetailed(context.Background(), json.RawMessage(`{"query":"不存在"}`))
		if err != nil || !meta.Truncated {
			t.Fatalf("扫描上限: %+v %v", meta, err)
		}
		out, meta, err := NewListFilesTool(dir).ExecuteDetailed(context.Background(), json.RawMessage(`{}`))
		if err != nil || !meta.Truncated || strings.Count(out, "文件 ") != 200 {
			t.Fatalf("列表上限: %+v %v", meta, err)
		}
	})
	t.Run("目录深度", func(t *testing.T) {
		dir := t.TempDir()
		for _, n := range []int{16, 17} {
			path := filepath.Join(dir, strings.Repeat("d/", n), "file")
			_ = os.MkdirAll(filepath.Dir(path), 0700)
			_ = os.WriteFile(path, []byte(fmt.Sprintf("标记%d", n)), 0600)
		}
		out, meta, err := NewSearchTextTool(dir).ExecuteDetailed(context.Background(), json.RawMessage(`{"query":"标记"}`))
		if err != nil || !meta.Truncated || !strings.Contains(out, "标记16") || strings.Contains(out, "标记17") {
			t.Fatalf("深度边界: %s %+v %v", out, meta, err)
		}
	})
	t.Run("文件读取", func(t *testing.T) {
		dir := t.TempDir()
		_ = os.WriteFile(filepath.Join(dir, "file"), []byte("头部\n"+strings.Repeat("x", 128*1024)+"尾部"), 0600)
		out, meta, err := NewSearchTextTool(dir).ExecuteDetailed(context.Background(), json.RawMessage(`{"path":"file","query":"尾部"}`))
		if err != nil || !meta.Truncated || strings.Contains(out, ":1:") {
			t.Fatal(out, meta, err)
		}
	})
	t.Run("匹配与输出", func(t *testing.T) {
		dir := t.TempDir()
		_ = os.WriteFile(filepath.Join(dir, "many"), []byte(strings.Repeat("标记\n", 101)), 0600)
		out, meta, err := NewSearchTextTool(dir).ExecuteDetailed(context.Background(), json.RawMessage(`{"query":"标记"}`))
		if err != nil || !meta.Truncated || strings.Count(out, "标记") != 100 {
			t.Fatal(meta, err)
		}
		_ = os.WriteFile(filepath.Join(dir, "long"), []byte("标记"+strings.Repeat("中", 5000)), 0600)
		out, meta, err = NewSearchTextTool(dir).ExecuteDetailed(context.Background(), json.RawMessage(`{"path":"long","query":"标记"}`))
		if err != nil || !meta.Truncated || len(out) > 8200 || !utf8.ValidString(out) {
			t.Fatal(meta, err)
		}
	})
}

func TestAcceptanceApprovalAuditAndAuthority(t *testing.T) {
	dir := t.TempDir()
	var request policy.Request
	r := NewRegistryWithPolicy(policy.Development(true), approverFunc(func(_ context.Context, r policy.Request) (policy.Grant, error) {
		request = r
		return policy.GrantFor(r, "可信测试主体"), nil
	}))
	r.Register(NewWriteFileTool(dir))
	called := false
	r.Use(func(context.Context, schema.ToolCall) (bool, string) { called = true; return true, "" })
	ctx, span := observability.StartSpan(executionTestContext(context.Background()), "验收根跨度")
	call := schema.ToolCall{Name: "write_file", Arguments: json.RawMessage(`{"path":"file","content":"不得出现在追踪中的测试文本"}`)}
	if result := r.Execute(ctx, call); result.IsError {
		t.Fatal(result)
	}
	span.EndSpan()
	encoded, err := json.Marshal(span)
	if err != nil {
		t.Fatal(err)
	}
	child := span.Children[0]
	attrs := child.Attributes
	if !called || attrs["approval_id"] != request.ID || attrs["arguments_digest"] != policy.Digest(call.Arguments) || attrs["approval_subject"] != "可信测试主体" || attrs["approval_expires_at"] == nil || strings.Contains(string(encoded), "不得出现在追踪中的测试文本") {
		t.Fatalf("审批证据错误: %s", encoded)
	}
	called = false
	if result := r.Execute(ctx, schema.ToolCall{Name: "不存在"}); !result.IsError || !result.Denied || called {
		t.Fatal("未知工具越过权限检查")
	}
	limited := policy.Restrict(ctx, policy.ReadOnly())
	limited = policy.Restrict(limited, policy.Development(true))
	if result := r.Execute(limited, call); !result.Denied || called {
		t.Fatal("再次 Restrict 扩大了权限")
	}
}

func TestAcceptanceNetworkApprovalAndFixedBackend(t *testing.T) {
	for _, network := range []bool{false, true} {
		t.Run(fmt.Sprint(network), func(t *testing.T) {
			called := false
			approved := false
			backend := executorFunc(func(_ context.Context, req executor.Request) (executor.Result, error) {
				called = true
				if req.Network != network || req.Command != "测试命令" {
					t.Error("模型参数改变了执行配置")
				}
				return executor.Result{Output: "完成"}, nil
			})
			r := NewRegistryWithPolicy(policy.Development(true), approverFunc(func(_ context.Context, r policy.Request) (policy.Grant, error) {
				approved = true
				return policy.GrantFor(r, "测试主体"), nil
			}))
			r.Register(NewBashToolWithExecutor(t.TempDir(), backend, network))
			call := schema.ToolCall{Name: "bash", Arguments: json.RawMessage(`{"command":"测试命令","network":true,"image":"evil","mounts":["/"],"memory":"unlimited"}`)}
			if result := r.Execute(executionTestContext(context.Background()), call); result.IsError || !called || !approved {
				t.Fatal(result)
			}
		})
	}
}
