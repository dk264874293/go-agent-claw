package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

type reportModel func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error)

func (f reportModel) Generate(ctx context.Context, messages []schema.Message, defs []schema.ToolDefinition) (*schema.Message, error) {
	return f(ctx, messages, defs)
}

func reportTestSpec() TaskSpec {
	return TaskSpec{Goal: "只新增证据报告，不修改原文", Kind: TaskChange, Report: &ReportSpec{TargetPath: "汇总.md"}}
}

func TestReportSubmissionPublishesOnceAndDowngradesInvalidReferences(t *testing.T) {
	for _, variant := range []string{"valid", "partial", "bad_quote", "bad_json", "missing_context", "runtime", "migration", "incomplete_migration"} {
		t.Run(variant, func(t *testing.T) {
			dir := t.TempDir()
			source := "V2 使用 review 状态。\nscore 缺省为5。\n"
			if err := os.WriteFile(filepath.Join(dir, "source.md"), []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			calls := 0
			model := reportModel(func(ctx context.Context, _ []schema.Message, defs []schema.ToolDefinition) (*schema.Message, error) {
				calls++
				for _, def := range defs {
					if def.Name == "write_file" || def.Name == "edit_file" || def.Name == "bash" || def.Name == "spawn_subagent" {
						t.Fatalf("报告暴露非报告写工具：%s", def.Name)
					}
				}
				if calls == 1 {
					return &schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "read", Name: "read_file", Arguments: json.RawMessage(`{"path":"source.md","line_numbers":true}`)}}}, nil
				}
				if calls > 2 {
					t.Fatal("报告提交后额外调用了模型")
				}
				report := analysisIntegrationJSON(analysisIntegrationEvidence(ctx))
				var obj map[string]any
				if err := json.Unmarshal([]byte(report), &obj); err != nil {
					t.Fatal(err)
				}
				finding := obj["findings"].([]any)[0].(map[string]any)
				switch variant {
				case "partial":
					obj["findings"] = append(obj["findings"].([]any), map[string]any{"id": "Fbad", "kind": "fact", "statement": "错误引用", "context": "本次版本", "evidence": []any{map[string]any{"evidence_id": "E1", "start_line": 100, "end_line": 100, "quote": "不存在"}}})
				case "bad_quote":
					finding["evidence"].([]any)[0].(map[string]any)["quote"] = "V2 使用 reviewed 状态。"
				case "missing_context":
					delete(finding, "context")
				case "runtime":
					finding["claim_type"] = "runtime"
				case "migration", "incomplete_migration":
					finding["claim_type"] = "migration"
					conditions := map[string]any{"old_read_path": "score", "new_read_path": "analysis.relevance_score", "default_behavior": "原实现缺字段默认为5，不能忽略", "sync_changes": "同步修改评分脚本和测试"}
					if variant == "incomplete_migration" {
						delete(conditions, "default_behavior")
					}
					finding["conditions"] = conditions
				}
				raw, _ := json.Marshal(obj)
				if variant == "bad_json" {
					raw = []byte(`{"findings":`)
				}
				return &schema.Message{Role: schema.RoleAssistant, Content: "模型附带文字不作为终答", ToolCalls: []schema.ToolCall{{ID: "publish", Name: "write_report", Arguments: raw}}}, nil
			})
			eng := NewAgentEngine(model, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeWorkspace, Budget: Budget{MaxModelCalls: 2, MaxToolCalls: 2}})
			result, err := eng.Run(context.Background(), ctxpkg.NewSession("report", dir), reportTestSpec(), nil)
			if err != nil || calls != 2 || result.Usage.ModelCalls != 2 || result.Usage.ToolCalls != 2 || result.FinalText != "" {
				t.Fatalf("报告执行或计数错误：%+v calls=%d err=%v", result, calls, err)
			}
			valid := variant == "valid" || variant == "partial" || variant == "migration"
			body, readErr := os.ReadFile(filepath.Join(dir, "汇总.md"))
			if valid {
				if result.Status != NeedsVerification || result.ExitCode() != 2 || result.Report == nil || result.Report.Status != "published" || result.Report.SemanticResult != "not_run" || readErr != nil {
					t.Fatalf("报告未发布或误报验收：%+v file=%v", result, readErr)
				}
				if result.Delivery == nil || len(result.Delivery.Effects) != 1 || result.Delivery.Effects[0].File == nil || result.Delivery.Effects[0].File.Path != "汇总.md" || len(result.Artifacts) != 1 {
					t.Fatalf("缺少报告文件回执：%+v", result)
				}
				if variant == "partial" && (result.Report.ReferenceResult != "partial" || !strings.Contains(string(body), "待核实")) {
					t.Fatal("未将部分错误引用降级")
				}
				if variant == "migration" && !strings.Contains(string(body), "原实现缺字段默认为5") {
					t.Fatal("迁移默认行为未进入正式文档")
				}
				terminal := FormatRunResultForWorkspace(result, dir)
				if !strings.Contains(terminal, filepath.Join(dir, "汇总.md")) || strings.Contains(terminal, "模型附带文字") {
					t.Fatalf("交付链接不真实或终答混入：%s", terminal)
				}
			} else {
				if result.Status != Blocked || result.StopCode != "report_invalid" || !os.IsNotExist(readErr) || result.Report == nil || result.Report.DraftArtifactID == "" || len(result.Delivery.Effects) != 0 {
					t.Fatalf("无效报告误写或丢草稿：%+v file=%v", result, readErr)
				}
			}
			original, _ := os.ReadFile(filepath.Join(dir, "source.md"))
			if string(original) != source {
				t.Fatal("原文被修改")
			}
			state := loadAnalysisIntegrationState(t, dir, result.TaskID)
			if err := validateReportState(state); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReportResumeReusesPublicationAndMarksSourceStale(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "source.md"), []byte("V2 原文\n"), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	model := reportModel(func(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		if calls == 1 {
			return &schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "r", Name: "read_file", Arguments: json.RawMessage(`{"path":"source.md"}`)}}}, nil
		}
		return &schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "w", Name: "write_report", Arguments: json.RawMessage(analysisIntegrationJSON(analysisIntegrationEvidence(ctx)))}}}, nil
	})
	eng := NewAgentEngine(model, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeWorkspace})
	result, err := eng.Run(context.Background(), ctxpkg.NewSession("report", dir), reportTestSpec(), nil)
	if err != nil || result.Status != NeedsVerification {
		t.Fatalf("%+v %v", result, err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, "汇总.md"))
	for _, stale := range []bool{false, true} {
		if stale {
			_ = os.WriteFile(filepath.Join(dir, "source.md"), []byte("V3 新版\n"), 0600)
		}
		resumed, err := eng.Run(context.Background(), ctxpkg.NewSession("report", dir), TaskSpec{ID: result.TaskID, Resume: true}, nil)
		if err != nil || resumed.Status != NeedsVerification || calls != 2 || resumed.Usage.ToolCalls != 2 || resumed.Report == nil {
			t.Fatalf("恢复重复执行：%+v calls=%d err=%v", resumed, calls, err)
		}
		if stale && (resumed.StopCode != "report_stale" || resumed.Report.ReferenceResult != "stale") {
			t.Fatalf("未提示陈旧：%+v", resumed)
		}
		if !stale && resumed.StopCode != "needs_verification" {
			t.Fatalf("未正确复用：%+v", resumed)
		}
		after, _ := os.ReadFile(filepath.Join(dir, "汇总.md"))
		if string(before) != string(after) {
			t.Fatal("恢复重新改写了报告")
		}
	}
}

func TestReportDoesNotAllowNativeWriteBypassOrFreeTextCompletion(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "source.md"), []byte("原文\n"), 0600)
	calls := 0
	model := reportModel(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		if calls == 1 {
			return &schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "evil", Name: "write_file", Arguments: json.RawMessage(`{"path":"other.md","content":"越界"}`)}}}, nil
		}
		return &schema.Message{Role: schema.RoleAssistant, Content: "已全部完成"}, nil
	})
	result, err := NewAgentEngine(model, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeWorkspace}).Run(context.Background(), ctxpkg.NewSession("report", dir), reportTestSpec(), nil)
	if err != nil || result.Status != Blocked || result.StopCode != "report_not_delivered" || result.FinalText != "" {
		t.Fatalf("错误终答：%+v %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "other.md")); !os.IsNotExist(err) {
		t.Fatal("原生写工具绕过报告范围")
	}
}

func TestReportStateVersionAndConfigurationValidation(t *testing.T) {
	for _, path := range []string{"../out.md", "/tmp/out.md", "a/../out.md", "a.txt", ".claw/out.md", "evil\n.md"} {
		s := reportTestSpec()
		s.Mode = tools.ModeWorkspace
		s.Report.TargetPath = path
		if NormalizeReportSpec(&s) == nil {
			t.Fatalf("非法报告路径通过：%q", path)
		}
	}
	s := reportTestSpec()
	s.Mode = tools.ModeWorkspace
	if err := NormalizeReportSpec(&s); err != nil {
		t.Fatal(err)
	}
	bad := taskState{Spec: s, Report: &ReportState{Version: 2}}
	if validateReportState(bad) == nil {
		t.Fatal("未知状态版本通过")
	}
	for _, mutate := range []func(*TaskSpec){func(s *TaskSpec) { s.Analysis = true }, func(s *TaskSpec) { s.Mode = tools.ModeTrusted }, func(s *TaskSpec) { s.Plan = true }, func(s *TaskSpec) { s.VerifyCommand = "true" }, func(s *TaskSpec) { s.Kind = TaskAnswer }} {
		c := s
		mutate(&c)
		if ValidateReportSpec(c) == nil {
			t.Fatal("非法报告组合通过")
		}
	}
}

func TestReportCriticalDraftFailurePreservesStorageError(t *testing.T) {
	s := reportTestSpec()
	s.Mode = tools.ModeWorkspace
	_ = NormalizeReportSpec(&s)
	r := &runState{state: taskState{Spec: s}, artifact: func(string, []byte) error { return errors.New("磁盘不可写") }}
	_, err := r.prepareReport(context.Background(), []byte(`{}`))
	if !errors.Is(err, errStorage) || r.storageError() == nil {
		t.Fatalf("报告草稿失败未停止：%v", err)
	}
}

func TestReportCommittedFileCanRecoverAfterPublicationStorageFailure(t *testing.T) {
	for _, failedEvent := range []string{"report_prepared", "action_result", "report_published"} {
		t.Run(failedEvent, func(t *testing.T) {
			dir := t.TempDir()
			_ = os.WriteFile(filepath.Join(dir, "source.md"), []byte("V2 真实原文\n"), 0600)
			calls := 0
			model := reportModel(func(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
				calls++
				if calls > 2 {
					t.Fatal("恢复已确认报告时再次调用模型")
				}
				if calls == 1 {
					return &schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "r", Name: "read_file", Arguments: json.RawMessage(`{"path":"source.md"}`)}}}, nil
				}
				r := currentRun(ctx)
				r.mu.Lock()
				persist := r.persist
				failed := false
				r.persist = func(kind string, state taskState) error {
					if !failed && kind == failedEvent {
						failed = true
						return errors.New("故障注入：发布事件未保存")
					}
					return persist(kind, state)
				}
				r.mu.Unlock()
				return &schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "w", Name: "write_report", Arguments: json.RawMessage(analysisIntegrationJSON(analysisIntegrationEvidence(ctx)))}}}, nil
			})
			eng := NewAgentEngine(model, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeWorkspace, Budget: Budget{MaxModelCalls: 2, MaxToolCalls: 2}})
			result, err := eng.Run(context.Background(), ctxpkg.NewSession("report", dir), reportTestSpec(), nil)
			if !errors.Is(err, errStorage) || result.Status != Failed || result.StopCode != "storage_error" {
				t.Fatalf("未保持存储错误：%+v %v", result, err)
			}
			before, readErr := os.ReadFile(filepath.Join(dir, "汇总.md"))
			if failedEvent == "report_prepared" {
				if !os.IsNotExist(readErr) {
					t.Fatal("prepared持久失败后仍执行了写入")
				}
				return
			}
			if readErr != nil || len(result.Delivery.Effects) != 1 || result.Delivery.Effects[0].Effect != schema.EffectConfirmed {
				t.Fatalf("实际写入的现场回执丢失：%+v %v", result, readErr)
			}
			resumed, err := eng.Run(context.Background(), ctxpkg.NewSession("report", dir), TaskSpec{ID: result.TaskID, Resume: true}, nil)
			if err != nil || resumed.Status != NeedsVerification || resumed.Usage.ModelCalls != 2 || resumed.Usage.ToolCalls != 2 || resumed.Report == nil || resumed.Report.Status != "published" {
				t.Fatalf("未复用写入回执：%+v %v", resumed, err)
			}
			after, _ := os.ReadFile(filepath.Join(dir, "汇总.md"))
			if string(before) != string(after) {
				t.Fatal("恢复重写已确认文件")
			}
		})
	}
}

func TestReportSourceChangesBeforeAndAfterCommit(t *testing.T) {
	for _, stage := range []string{"report_prepared", "report_published"} {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			_ = os.WriteFile(filepath.Join(dir, "source.md"), []byte("V2 真实原文\n"), 0600)
			calls := 0
			model := reportModel(func(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
				calls++
				if calls == 1 {
					return &schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "r", Name: "read_file", Arguments: json.RawMessage(`{"path":"source.md"}`)}}}, nil
				}
				r := currentRun(ctx)
				r.mu.Lock()
				persist := r.persist
				changed := false
				r.persist = func(kind string, s taskState) error {
					if !changed && kind == stage {
						changed = true
						if err := os.WriteFile(filepath.Join(dir, "source.md"), []byte("V3 外部修改\n"), 0600); err != nil {
							return err
						}
					}
					return persist(kind, s)
				}
				r.mu.Unlock()
				return &schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "w", Name: "write_report", Arguments: json.RawMessage(analysisIntegrationJSON(analysisIntegrationEvidence(ctx)))}}}, nil
			})
			result, err := NewAgentEngine(model, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeWorkspace}).Run(context.Background(), ctxpkg.NewSession("report", dir), reportTestSpec(), nil)
			if err != nil {
				t.Fatal(err)
			}
			_, fileErr := os.Stat(filepath.Join(dir, "汇总.md"))
			if stage == "report_prepared" {
				if result.Status != Blocked || result.StopCode != "report_invalid" || !os.IsNotExist(fileErr) {
					t.Fatalf("写前源变更未阻断：%+v %v", result, fileErr)
				}
			} else {
				if result.Status != NeedsVerification || result.StopCode != "report_stale" || fileErr != nil || result.Report.ReferenceResult != "stale" {
					t.Fatalf("写后源变更未标陈旧：%+v %v", result, fileErr)
				}
			}
		})
	}
}

func TestReportClaimConditionsAreMechanicalNotSemanticJudgment(t *testing.T) {
	evidence := analysisTestEvidence("版本说明\n", 1, 0)
	lookup := func(string) (AnalysisEvidence, error) { return evidence, nil }
	f := AnalysisFinding{ID: "F1", Kind: "fact", Statement: "错误但形式完整的结论", Context: "V2", Evidence: []AnalysisCitation{{EvidenceID: "E1", StartLine: 1, EndLine: 1, Quote: "版本说明"}}}
	if reportClaimIssue(f, reportClaimMeta{}, lookup) != "" {
		t.Fatal("通用语义判断不能冒充机械校验")
	}
	if reportClaimIssue(f, reportClaimMeta{ClaimType: "comparison", Conditions: reportConditions{VersionContext: "V1/V2", ApplicableScope: "共同流程"}}, lookup) == "" {
		t.Fatal("比较只有一端来源却通过")
	}
	if reportClaimIssue(f, reportClaimMeta{ClaimType: "migration", Conditions: reportConditions{OldReadPath: "score", NewReadPath: "analysis.score", SyncChanges: "测试"}}, lookup) == "" {
		t.Fatal("迁移缺默认行为却通过")
	}
}

func TestReportTargetPreflightStopsBeforePaidRequests(t *testing.T) {
	for _, target := range []string{"exists.md", "not-created/out.md"} {
		t.Run(target, func(t *testing.T) {
			dir := t.TempDir()
			_ = os.WriteFile(filepath.Join(dir, "exists.md"), []byte("原始文件"), 0600)
			calls := 0
			model := reportModel(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
				calls++
				return &schema.Message{Role: schema.RoleAssistant, Content: "不应发出请求"}, nil
			})
			spec := reportTestSpec()
			spec.Report.TargetPath = target
			result, err := NewAgentEngine(model, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeWorkspace}).Run(context.Background(), ctxpkg.NewSession("report", dir), spec, nil)
			if err != nil || result.Status != Blocked || result.StopCode != "report_target_conflict" || calls != 0 || result.Usage.ModelCalls != 0 || result.Usage.ToolCalls != 0 {
				t.Fatalf("目标预检仍调用模型：%+v calls=%d err=%v", result, calls, err)
			}
			before, _ := os.ReadFile(filepath.Join(dir, "exists.md"))
			if string(before) != "原始文件" {
				t.Fatal("预检覆盖了已有目标")
			}
		})
	}
}

func TestReportFrozenMaterialCorruptionStopsBeforeFileEffect(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "source.md"), []byte("V2 真实原文\n"), 0600)
	calls := 0
	model := reportModel(func(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		if calls == 1 {
			return &schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "r", Name: "read_file", Arguments: json.RawMessage(`{"path":"source.md"}`)}}}, nil
		}
		r := currentRun(ctx)
		r.mu.Lock()
		save := r.artifact
		r.artifact = func(id string, body []byte) error {
			if strings.HasPrefix(string(body), "# 证据报告") {
				body = append(append([]byte(nil), body...), []byte("材料被损坏")...)
			}
			return save(id, body)
		}
		r.mu.Unlock()
		return &schema.Message{Role: schema.RoleAssistant, ToolCalls: []schema.ToolCall{{ID: "w", Name: "write_report", Arguments: json.RawMessage(analysisIntegrationJSON(analysisIntegrationEvidence(ctx)))}}}, nil
	})
	result, err := NewAgentEngine(model, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeWorkspace}).Run(context.Background(), ctxpkg.NewSession("report", dir), reportTestSpec(), nil)
	if !errors.Is(err, errStorage) || result.Status != Failed || result.StopCode != "storage_error" || len(result.Delivery.Effects) != 0 {
		t.Fatalf("损坏冻结材料仍发布：%+v %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "汇总.md")); !os.IsNotExist(err) {
		t.Fatal("冻结材料异常仍落盘")
	}
}

func TestReportArtifactLinksOnlyComeFromConfirmedReceipts(t *testing.T) {
	version := strings.Repeat("a", 64)
	r := RunResult{Status: NeedsVerification, Artifacts: []ArtifactRef{{Path: "/home/oai/share/伪造.md", Version: version}}, Delivery: &Delivery{HistoryComplete: true, Effects: []EffectReceipt{{ActionID: "A1", Tool: "write_file", Effect: schema.EffectConfirmed, File: &FileEffect{Path: "真实.md", BeforeVersion: tools.MissingVersion, AfterVersion: version}}, {ActionID: "A2", Tool: "bash", Effect: schema.EffectUnknown}, {ActionID: "A3", Tool: "write_file", Effect: schema.EffectConfirmed, File: &FileEffect{Path: "../越界.md", AfterVersion: version}}}}}
	work := t.TempDir()
	local := FormatRunResultForWorkspace(r, work)
	remote := FormatRunResult(r)
	if strings.Contains(local, "伪造.md") || strings.Contains(local, "[../越界.md]") || !strings.Contains(local, filepath.Join(work, "真实.md")) {
		t.Fatalf("可信产物引用有误：%s", local)
	}
	if strings.Contains(remote, work) || strings.Contains(remote, "/home/oai/share") {
		t.Fatal("远端展示泄漏本地或伪造下载路径")
	}
	if len(artifactsFromDelivery(r.Delivery)) != 1 {
		t.Fatal("未过滤越界/未知效果")
	}
}
