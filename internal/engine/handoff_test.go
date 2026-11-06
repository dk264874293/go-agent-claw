package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func handoffFixture(t *testing.T) (*runState, context.Context, context.Context, []schema.ReadReceipt, map[string][]byte) {
	t.Helper()
	dir := t.TempDir()
	writeFixture(t, dir, "a.txt", "1\n2\n3\n")
	writeFixture(t, dir, "b.txt", "完整材料\n")
	parent := tools.WithScope(observe.WithAgent(context.Background(), "parent", ""), tools.ExecutionScope{Mode: tools.ModeReadOnly, WorkDir: dir, AllowedPaths: []string{"a.txt", "b.txt"}})
	child := observe.WithAgent(parent, "child", "parent")
	material := make(map[string][]byte)
	r := &runState{state: taskState{Spec: TaskSpec{Kind: TaskAnswer, Mode: tools.ModeReadOnly, Delegation: "on"}}, runID: "run", artifact: func(id string, data []byte) error { material[id] = append([]byte(nil), data...); return nil }}
	readCatalogFixture(t, r, child, dir, "a.txt", 0, 4)
	readCatalogFixture(t, r, child, dir, "b.txt", 0, 8000)
	return r, context.WithValue(parent, runContextKey{}, r), context.WithValue(child, runContextKey{}, r), r.agentReads("child"), material
}

func TestHandoffIndexedParserAndLegacyCompatibility(t *testing.T) {
	body := `{"report_version":2,"findings":[{"id":"F1","statement":"短结论","status":"VERIFIED","evidence":[]}],"unverified":["未运行脚本"]}`
	_, items, indexed, err := decodeIndexedSubreport(body)
	if err != nil || !indexed || len(items) != 1 || items[0].Status != "" {
		t.Fatalf("结构化解析采用了模型状态: %+v %v", items, err)
	}
	legacy, items, indexed, err := decodeIndexedSubreport(`{"findings":{"发现":"内容"},"evidence":[]}`)
	if err != nil || indexed || items != nil || !strings.Contains(legacy.Findings, "内容") {
		t.Fatal("旧格式被错误升级或失去兼容")
	}
	for _, body := range []string{`{"report_version":3,"findings":[]}`, `{"report_version":2,"findings":[]}`, `{"report_version":2,"findings":[{"id":"F1","statement":"x","scope_requirement":"all"}]}`} {
		if _, _, _, err := decodeIndexedSubreport(body); err == nil {
			t.Fatalf("接受无效契约 %s", body)
		}
	}
}

func TestHandoffScopeCoverageAndVerificationAreIndependent(t *testing.T) {
	r, parent, child, reads, material := handoffFixture(t)
	items := []tools.ReportFinding{{ID: "negative", Statement: "整个文件没有后续内容", ScopeRequirement: "file", Evidence: []tools.EvidenceRef{{Path: "a.txt", Line: 1, Version: reads[0].Version}}}, {ID: "positive", Statement: "另一份材料有明确内容", Evidence: []tools.EvidenceRef{{Path: "b.txt", Line: 1, Version: reads[1].Version}}}}
	report, err := r.finalizeHandoff(child, "parent", []string{"a.txt", "b.txt"}, tools.SubtaskReport{Status: "SUCCEEDED", Unverified: []string{"未运行Python，没有看截图"}}, items, reads)
	if err != nil {
		t.Fatal(err)
	}
	if report.Handoff == nil || report.Status != "SUCCEEDED" || report.Handoff.Execution != "succeeded" || report.Handoff.Verification != "not_run" || report.Handoff.Coverage.Status != "partial" {
		t.Fatalf("状态维度耦合或覆盖误报: %+v", report)
	}
	if report.Handoff.Items[0].Status != "unverified" || report.Handoff.Items[1].Status != "position_valid" {
		t.Fatal("不完整范围结论未降级，或有效发现被连带撤销")
	}
	forged := tools.ReportFinding{ID: "F", Statement: "位置只能证明已读行", Evidence: []tools.EvidenceRef{{Path: "b.txt", Line: 1, Version: reads[1].Version, EvidenceID: "模型伪造ID", Quote: "未经过原文字节校验的摘录"}}}
	checked := r.checkHandoffFinding(child, forged, reads)
	if checked.Evidence[0].EvidenceID != "" || checked.Evidence[0].Quote != "" || checked.Status != "position_valid" {
		t.Fatal("普通位置核对被升级为模型自报的精确引用")
	}
	if !json.Valid(material[report.ArtifactID]) {
		t.Fatal("完整材料未可靠保存")
	}
	if !r.validHandoffReview(parent, "positive", "b.txt", reads[1].Version) {
		t.Fatal("父任务无法有目的核对已交接发现")
	}
	if r.validHandoffReview(observe.WithAgent(parent, "sibling", "parent"), "positive", "b.txt", reads[1].Version) {
		t.Fatal("兄弟任务借用父报告复核身份")
	}
	data, err := (readReportTool{}).Execute(parent, json.RawMessage(fmt.Sprintf(`{"report_id":%q,"finding_ids":["positive"]}`, report.Handoff.ReportID)))
	if err != nil || !strings.Contains(data, "另一份材料") {
		t.Fatalf("无法按发现读取: %s %v", data, err)
	}
}

func TestHandoffLongDirectoryKeepsTailReachableAndJSONComplete(t *testing.T) {
	r, parent, child, reads, _ := handoffFixture(t)
	var items []tools.ReportFinding
	for i := 0; i < 64; i++ {
		items = append(items, tools.ReportFinding{ID: fmt.Sprintf("F%02d", i), Statement: strings.Repeat("短发现", 60), Context: strings.Repeat("版本背景", 15), Evidence: []tools.EvidenceRef{{Path: "b.txt", Line: 1, Version: reads[1].Version}}})
	}
	report, err := r.finalizeHandoff(child, "parent", []string{"a.txt", "b.txt"}, tools.SubtaskReport{Status: "SUCCEEDED"}, items, reads)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := tools.EncodeSubtaskHandoff(report)
	if err != nil || len(wire) > 8192 || !json.Valid(wire) {
		t.Fatalf("目录被截断或超限: len=%d %v", len(wire), err)
	}
	var parsed tools.SubtaskReport
	_ = json.Unmarshal(wire, &parsed)
	if parsed.Handoff.Omitted == 0 {
		t.Fatal("长报告没有记录省略数量")
	}
	seen := make(map[string]bool)
	after := ""
	for page := 0; page < 10; page++ {
		args, _ := json.Marshal(map[string]any{"report_id": report.Handoff.ReportID, "after_id": after})
		data, err := (readReportTool{}).Execute(parent, args)
		if err != nil || len(data) > 8192 || !json.Valid([]byte(data)) {
			t.Fatalf("目录不可解析 %v", err)
		}
		var got tools.SubtaskReport
		if json.Unmarshal([]byte(data), &got) != nil {
			t.Fatal("目录不符合返回契约")
		}
		for _, f := range got.Handoff.Items {
			if seen[f.ID] {
				t.Fatal("游标重复条目")
			}
			seen[f.ID] = true
		}
		after = got.Handoff.NextAfterID
		if after == "" {
			break
		}
	}
	if len(seen) != 64 || !seen["F63"] {
		t.Fatalf("尾部发现不可达: %d", len(seen))
	}
	data, err := (readReportTool{}).Execute(parent, json.RawMessage(fmt.Sprintf(`{"report_id":%q,"finding_ids":["F63"]}`, report.Handoff.ReportID)))
	if err != nil || !strings.Contains(data, "F63") {
		t.Fatal("尾部详情不可读取")
	}
}

func TestHandoffStaleFindingDoesNotEraseOtherFindings(t *testing.T) {
	r, parent, child, reads, _ := handoffFixture(t)
	items := []tools.ReportFinding{{ID: "a", Statement: "A的事实", Evidence: []tools.EvidenceRef{{Path: "a.txt", Line: 1, Version: reads[0].Version}}}, {ID: "b", Statement: "B的事实", Evidence: []tools.EvidenceRef{{Path: "b.txt", Line: 1, Version: reads[1].Version}}}}
	report, err := r.finalizeHandoff(child, "parent", []string{"a.txt", "b.txt"}, tools.SubtaskReport{Status: "SUCCEEDED"}, items, reads)
	if err != nil {
		t.Fatal(err)
	}
	r.rememberSubreport("parent", report, reads)
	writeFixture(t, tools.ScopeFromContext(parent).WorkDir, "a.txt", "新版内容\n")
	context := r.readContext(parent)
	if !strings.Contains(context, "B的事实") || !strings.Contains(context, `"status":"stale"`) {
		t.Fatalf("一项变更删除整份交接或没标陈旧: %s", context)
	}
	data, err := (readReportTool{}).Execute(parent, json.RawMessage(fmt.Sprintf(`{"report_id":%q,"finding_ids":["b"]}`, report.Handoff.ReportID)))
	if err != nil || !strings.Contains(data, "position_valid") {
		t.Fatal("有效其他发现被撤销")
	}
	narrow := tools.WithScope(parent, tools.ExecutionScope{Mode: tools.ModeReadOnly, WorkDir: tools.ScopeFromContext(parent).WorkDir, AllowedPaths: []string{"a.txt"}})
	data, err = (readReportTool{}).Execute(narrow, json.RawMessage(fmt.Sprintf(`{"report_id":%q}`, report.Handoff.ReportID)))
	if err != nil || strings.Contains(data, "B的事实") {
		t.Fatal("目录泄漏越权发现")
	}
	if known, allowed := r.handoffArtifactAccess(narrow, report.ArtifactID); !known || allowed {
		t.Fatal("通用材料入口未保持范围权限")
	}
	sibling := observe.WithAgent(parent, "sibling", "parent")
	if _, err := (readReportTool{}).Execute(sibling, json.RawMessage(fmt.Sprintf(`{"report_id":%q}`, report.Handoff.ReportID))); err == nil {
		t.Fatal("兄弟可读取父报告")
	}
	r.runID = "new-run"
	if _, err := (readReportTool{}).Execute(parent, json.RawMessage(fmt.Sprintf(`{"report_id":%q}`, report.Handoff.ReportID))); err == nil {
		t.Fatal("恢复沿用历史交接为当前结果")
	}
}

func TestHandoffAnalysisDependenciesLoseOnlyAffectedConclusions(t *testing.T) {
	items := []tools.ReportFinding{{ID: "fact", Statement: "前提", Status: "stale"}, {ID: "inference", Statement: "依赖前提的推断", Status: "inference", DependsOn: []string{"fact"}}, {ID: "independent", Statement: "独立事实", Status: "position_valid"}}
	checked := checkHandoffDependencies(items)
	if checked[1].Status != "unverified" || checked[2].Status != "position_valid" || items[1].Status != "inference" {
		t.Fatal("依赖失效没有传播，或修改了原始报告")
	}
}

func TestHandoffWarningsCannotCrowdOutCompactFindings(t *testing.T) {
	report := tools.SubtaskReport{Status: "SUCCEEDED", Handoff: &tools.ReportHandoff{ReportID: "id", ArtifactID: "id", Items: []tools.ReportFinding{{ID: "F1", Statement: "关键发现", Status: "position_valid"}}}}
	for i := 0; i < 32; i++ {
		report.Unverified = append(report.Unverified, strings.Repeat("未跑环境限制。", 60))
	}
	wire, err := tools.EncodeSubtaskHandoff(report)
	if err != nil {
		t.Fatal(err)
	}
	var decoded tools.SubtaskReport
	_ = json.Unmarshal(wire, &decoded)
	if len(wire) > 8192 || len(decoded.Handoff.Items) != 1 || decoded.Handoff.UnverifiedOmitted == 0 || len(report.Unverified) != 32 {
		t.Fatal("环境限制挤掉有效发现，或省略没有标记")
	}
}

func TestHandoffRunSubAndReadReportWithoutExtraModelCalls(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "source.txt", "可核对的材料\n")
	rootCalls, childCalls := 0, 0
	provider := scriptedProvider(func(ctx context.Context, messages []schema.Message, defs []schema.ToolDefinition) (*schema.Message, error) {
		if observe.Identity(ctx).ParentAgentID != "" {
			childCalls++
			if childCalls == 1 {
				return modelCalls(readCall("child-read", "source.txt"))
			}
			reads := currentRun(ctx).agentReads(observe.Identity(ctx).AgentID)
			body, _ := json.Marshal(map[string]any{"report_version": 2, "findings": []tools.ReportFinding{{ID: "F1", Statement: "第一行有材料", Context: "当前版本", Evidence: []tools.EvidenceRef{{Path: "source.txt", Line: 1, Version: reads[0].Version}}}}, "unverified": []string{"尚未运行脚本"}})
			return reply(string(body))
		}
		rootCalls++
		switch rootCalls {
		case 1:
			return modelCalls(schema.ToolCall{ID: "delegate", Name: "spawn_subagent", Arguments: json.RawMessage(`{"task_prompt":"核对source.txt","allowed_paths":["source.txt"],"expected_evidence":"给出首行材料位置"}`)})
		case 2:
			r := currentRun(ctx)
			r.mu.Lock()
			id := ""
			for k := range r.handoffs.reports {
				id = k
			}
			r.mu.Unlock()
			if id == "" {
				t.Fatal("子报告没有可用交接身份")
			}
			args, _ := json.Marshal(map[string]any{"report_id": id, "finding_ids": []string{"F1"}})
			return modelCalls(schema.ToolCall{ID: "detail", Name: "read_report", Arguments: args})
		default:
			if rootCalls != 3 {
				t.Fatal("出现额外摘要或修复请求")
			}
			return reply("已核对首行，脚本尚未运行。")
		}
	})
	registry := NewWorkspaceRegistry(dir)
	engine := NewAgentEngine(provider, registry, EngineOptions{})
	registry.Register(tools.NewSubagentTool(engine, NewExplorerRegistry(dir), nil))
	result, err := engine.Run(context.Background(), ctxpkg.NewSession("交接实际入口", dir), TaskSpec{Goal: "核对文件", Delegation: "on"}, nil)
	if err != nil || result.Status != Answered || rootCalls != 3 || childCalls != 2 || result.Usage.ModelCalls != 5 || result.Usage.ToolCalls != 3 {
		t.Fatalf("交接或调用计数错误: %+v root%d child%d err%v", result, rootCalls, childCalls, err)
	}
}
