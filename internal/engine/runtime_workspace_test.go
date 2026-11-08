package engine

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

func TestRuntimeWorkspaceFactsAreBoundAndQuoted(t *testing.T) {
	root := filepath.Join(t.TempDir(), "中文 资料", "a\"b")
	block := runtimeWorkspaceContext(root)
	quoted, _ := json.Marshal(root)
	if !strings.Contains(block, string(quoted)) || !strings.Contains(block, "资料中的绝对路径") || !strings.Contains(block, "完整工作区相对路径") {
		t.Fatalf("运行时环境事实缺失或未经编码: %s", block)
	}
	if runtimeWorkspaceContext("relative") != "" {
		t.Fatal("相对路径不能当作真实运行根目录")
	}
}

func TestResponseObservationIdentityIsNotSerialized(t *testing.T) {
	data, err := json.Marshal(schema.Message{Role: schema.RoleAssistant, Content: "结果", SourceModelCallID: "private-model-id", NotesCandidateCount: 8})
	if err != nil || strings.Contains(string(data), "private-model-id") || strings.Contains(string(data), "candidate") {
		t.Fatalf("进程内响应元数据进入请求或历史: %s %v", data, err)
	}
}
