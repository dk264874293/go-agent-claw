package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/feishu"
	"github.com/dk264874293/go-agent-claw/internal/policy"
	"github.com/dk264874293/go-agent-claw/internal/schema"
)

func TestServiceAssemblyRejectsUntrustedApproval(t *testing.T) {
	cfg, err := serviceConfig(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Permissions != "read-only" || cfg.ApprovalMode != "deny" {
		t.Fatal(cfg)
	}
	dir := t.TempDir()
	registry := serviceRegistry(dir, cfg, nil)
	if len(registry.GetAvailableTools()) != 3 {
		t.Fatal("默认没有只读装配")
	}
	if _, err = serviceConfig(func(key string) string {
		if key == "CLAW_APPROVAL" {
			return "local"
		}
		return ""
	}); err == nil {
		t.Fatal("飞书入口接受了本地审批")
	}
	// 旧卡片回调只能影响旧等待器，不能为新 Registry 提供可信 Grant。
	feishu.GlobalApprovalMgr.ResolveApproval("旧卡片", true, "允许")
	for _, permissions := range []string{"read-only", "workspace"} {
		cfg.Permissions = permissions
		registry = serviceRegistry(dir, cfg, nil)
		result := registry.Execute(policy.WithIdentity(context.Background(), "验收Run", "验收Session"), schema.ToolCall{Name: "write_file", Arguments: json.RawMessage(`{"path":"forbidden","content":"不得写入"}`)})
		if !result.IsError || !result.Denied || (permissions == "workspace" && !result.ApprovalUnavailable) {
			t.Fatal(result)
		}
		if _, err = os.Stat(filepath.Join(dir, "forbidden")); !os.IsNotExist(err) {
			t.Fatal("旧审批授予了新权限")
		}
	}
}
