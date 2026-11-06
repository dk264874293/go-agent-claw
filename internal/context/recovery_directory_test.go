package context

import (
	"strings"
	"testing"
)

func TestListDirectoryRecoveryRecognizesTypedAndLegacyErrors(t *testing.T) {
	for _, message := range []string{
		"执行状态：REJECTED，效果：NONE\nlist_dir: 路径不是目录: claw",
		"执行状态：FAILED，效果：NONE\nlist_dir: fdopendir /workspace/claw: not a directory",
	} {
		hint := NewRecoveryManager().AnalyzeAndInject("list_dir", message)
		if !strings.Contains(hint, "[系统救援指南]") || !strings.Contains(hint, "不要重复列目录") || !strings.Contains(hint, "read_file") {
			t.Fatalf("目录错误未提供明确恢复提示: %s", hint)
		}
	}
	const unknown = "list_dir: permission denied"
	if got := NewRecoveryManager().AnalyzeAndInject("list_dir", unknown); got != unknown {
		t.Fatalf("无关错误被误判为非目录: %s", got)
	}
}
