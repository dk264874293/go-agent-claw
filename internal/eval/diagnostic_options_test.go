package observability

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/requestdiag"
	"github.com/dk264874293/go-agent-claw/internal/schema"
)

type diagnosticCountingProvider struct {
	calls int
	mode  string
}

func (p *diagnosticCountingProvider) Generate(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
	p.calls++
	p.mode = requestdiag.Mode(ctx)
	return &schema.Message{Role: schema.RoleAssistant, Content: "已回答"}, nil
}

func TestBenchmarkExplicitDiagnosticsWorksWhenObservationIsOff(t *testing.T) {
	t.Setenv("CLAW_OBSERVE", "off")
	t.Setenv("CLAW_REQUEST_DIAGNOSTICS", "off")
	p := &diagnosticCountingProvider{}
	r := NewBenchmarkRunner("local-fixture", p)
	result := r.runSingleTest(context.Background(), TestCase{ID: "独立诊断", TaskPrompt: "回答", ValidateScript: "true", RequestDiagnostics: "metadata", RequestDiagnosticsDir: t.TempDir()}, p)
	if p.calls != 1 || p.mode != requestdiag.MetadataOnly || result.ErrorMsg != "" {
		t.Fatalf("用例配置未独立生效: calls=%d mode=%s %+v", p.calls, p.mode, result)
	}
}

func TestBenchmarkInvalidDiagnosticsDoesNotRunSetupOrModel(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "setup-ran")
	p := &diagnosticCountingProvider{}
	r := NewBenchmarkRunner("local-fixture", p)
	result := r.runSingleTest(context.Background(), TestCase{ID: "无效诊断", Analysis: true, TaskPrompt: "分析", RequestDiagnostics: "all", SetupScript: "touch '" + marker + "'"}, p)
	if p.calls != 0 || result.ErrorMsg == "" {
		t.Fatalf("无效诊断启动了模型: calls=%d %+v", p.calls, result)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("无效诊断启动了夹具脚本: %v", err)
	}
}
