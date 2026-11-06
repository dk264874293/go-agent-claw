package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

const structuredSubtaskPrompt = `你是只读代码探路者。仅调查指定范围，首次读取优先启用行号，不得因为补行号而全文重读。
最终只交付一个紧凑 JSON 对象，不要另写一份长报告：
{"report_version":2,"findings":[{"id":"F1","statement":"短结论","context":"版本和适用条件","limitations":"尚未核实的边界","scope_requirement":"fragment","evidence":[{"path":"src/main.go","line":12,"end_line":12,"version":"实际读取版本"}]}],"unverified":[]}
最多16条主要发现，单条结论尽量不超过600字节、背景和限制各尽量不超过300字节、引用尽量不超过4条；只选择能改变父任务结论的问题，不逐篇复述课程或源码。
scope_requirement 为 fragment、file 或 manifest；涉及“整份文件/整个范围不存在、全部完成”的判断必须用 file 或 manifest，缺少完整读取时只列待核实。
不要自报覆盖或验收成功，引擎会根据读取回执计算。未运行脚本、未看截图作为限制，不代表原文阅读失败。
区分V1/V2迁移与矛盾；核对默认值、可选字段及反例；跨文档判断引用两端。引用只说明读取位置，不代表结论已经独立验收。`

type handoffRuntime struct{ reports map[string]storedHandoff }
type storedHandoff struct {
	ParentID string
	RunID    string
	Scope    []string
	Report   tools.SubtaskReport
	Items    []tools.ReportFinding
}

// decodeIndexedSubreport 不从自由文本提取发现；旧格式仍走既有兼容入口。
func decodeIndexedSubreport(text string) (tools.SubtaskReport, []tools.ReportFinding, bool, error) {
	body := strings.TrimSpace(text)
	if strings.HasPrefix(body, "```") {
		if i := strings.IndexByte(body, '\n'); i >= 0 && strings.HasSuffix(body, "```") {
			body = strings.TrimSpace(strings.TrimSuffix(body[i+1:], "```"))
		}
	}
	var header struct {
		Version int `json:"report_version"`
	}
	if err := json.Unmarshal([]byte(body), &header); err != nil {
		return tools.SubtaskReport{}, nil, false, err
	}
	if header.Version == 0 {
		r, err := decodeSubtaskReport(text)
		return r, nil, false, err
	}
	if header.Version != 2 || len(body) > 128*1024 {
		return tools.SubtaskReport{}, nil, true, errors.New("子报告版本未知或超过128 KiB")
	}
	var report struct {
		Version    int                   `json:"report_version"`
		Findings   []tools.ReportFinding `json:"findings"`
		Unverified []string              `json:"unverified"`
	}
	if err := json.Unmarshal([]byte(body), &report); err != nil {
		return tools.SubtaskReport{}, nil, true, err
	}
	if len(report.Findings) == 0 || len(report.Findings) > 16 || len(report.Unverified) > 32 {
		return tools.SubtaskReport{}, nil, true, errors.New("结构化子报告需要1至16条发现，待核实项最多32条")
	}
	seen := make(map[string]bool)
	for i := range report.Findings {
		f := &report.Findings[i]
		if !analysisIDValid(f.ID) || seen[f.ID] || strings.TrimSpace(f.Statement) == "" || len(f.Statement) > 4000 || len(f.Context) > 2000 || len(f.Limitations) > 2000 || len(f.Evidence) > 8 || len(f.DependsOn) > 16 {
			return tools.SubtaskReport{}, nil, true, fmt.Errorf("发现 %q 的身份、结论或引用超过紧凑报告约束", f.ID)
		}
		seen[f.ID] = true
		if f.ScopeRequirement == "" {
			f.ScopeRequirement = "fragment"
		}
		if f.ScopeRequirement != "fragment" && f.ScopeRequirement != "file" && f.ScopeRequirement != "manifest" {
			return tools.SubtaskReport{}, nil, true, errors.New("scope_requirement 必须为 fragment/file/manifest")
		}
		f.Status = ""
	}
	for _, s := range report.Unverified {
		if len(s) > 1000 {
			return tools.SubtaskReport{}, nil, true, errors.New("单条待核实项超过1000字节")
		}
	}
	return tools.SubtaskReport{Unverified: report.Unverified}, report.Findings, true, nil
}

func analysisHandoffItems(report AnalysisReport) []tools.ReportFinding {
	items := make([]tools.ReportFinding, 0, len(report.Findings))
	for _, f := range report.Findings {
		item := tools.ReportFinding{ID: f.ID, Statement: f.Statement, Context: f.Context, Limitations: f.Limitations, ScopeRequirement: f.ScopeRequirement, DependsOn: append([]string(nil), f.DependsOn...), Status: "exact_valid"}
		for _, ref := range f.Evidence {
			item.Evidence = append(item.Evidence, tools.EvidenceRef{Path: ref.Path, Line: ref.StartLine, EndLine: ref.EndLine, Version: ref.Version, ActionID: ref.ActionID, EvidenceID: ref.EvidenceID, Quote: ref.Quote})
		}
		// 推断可以只依赖其他发现。保留依赖说明，不谎称拥有自己的直接引用。
		if len(item.Evidence) == 0 {
			item.Status = "inference"
			item.Limitations = boundedHandoffText(item.Limitations+" 依据发现："+strings.Join(f.DependsOn, ","), 1000)
		}
		items = append(items, item)
	}
	return items
}

func (r *runState) checkHandoffFinding(ctx context.Context, f tools.ReportFinding, reads []schema.ReadReceipt) tools.ReportFinding {
	if f.ScopeRequirement == "" {
		f.ScopeRequirement = "fragment"
	}
	if f.Status == "inference" && len(f.Evidence) == 0 {
		return f
	}
	valid := len(f.Evidence) > 0
	for i, ref := range f.Evidence {
		ref.Path = filepath.Clean(ref.Path)
		if ref.EndLine == 0 {
			ref.EndLine = ref.Line
		}
		match := false
		if filepath.IsLocal(ref.Path) && insideParentPaths(ref.Path, tools.ScopeFromContext(ctx).AllowedPaths) {
			version, err := tools.FileVersion(ctx, ref.Path)
			if err == nil && version == ref.Version {
				for _, read := range reads {
					if read.Path == ref.Path && read.Version == ref.Version && ref.Line >= read.FirstLine && ref.EndLine >= ref.Line && ref.EndLine <= read.LastLine && read.FirstLine > 0 && (ref.ActionID == "" || ref.ActionID == read.ActionID) {
						ref.ActionID = read.ActionID
						ref.EvidenceID = read.EvidenceID
						if f.Status != "exact_valid" {
							ref.Quote = ""
						}
						match = true
						break
					}
				}
			}
		}
		valid = valid && match
		f.Evidence[i] = ref
	}
	if valid && f.ScopeRequirement == "file" {
		for _, ref := range f.Evidence {
			coverage := r.coverageForPaths(ctx, []string{ref.Path})
			valid = valid && coverage != nil && coverage.Status == "complete"
		}
	}
	if valid && f.ScopeRequirement == "manifest" {
		coverage := r.coverageForPaths(ctx, tools.ScopeFromContext(ctx).AllowedPaths)
		valid = coverage != nil && coverage.Status == "complete"
	}
	if !valid {
		f.Status = "unverified"
		f.Limitations = boundedHandoffText(f.Limitations+" 引用、版本或所需读取覆盖未核实，不能沿用为确定结论。", 1000)
	} else if f.Status != "exact_valid" {
		f.Status = "position_valid"
	}
	return f
}

func boundedHandoffText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	end := n - 3
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end] + "…"
}

func handoffFindings(items []tools.ReportFinding) string {
	var out strings.Builder
	for _, f := range items {
		fmt.Fprintf(&out, "%s [%s] %s", f.ID, f.Status, f.Statement)
		if f.Context != "" {
			out.WriteString("（" + f.Context + "）")
		}
		out.WriteByte('\n')
	}
	return strings.TrimSpace(out.String())
}

func (r *runState) finalizeHandoff(ctx context.Context, parentID string, scope []string, report tools.SubtaskReport, items []tools.ReportFinding, reads []schema.ReadReceipt, stopCode ...string) (tools.SubtaskReport, error) {
	if !r.efficiencyEnabled() && !r.analysisEnabled() {
		return report, nil
	}
	if r.artifact == nil {
		return report, r.noteStorageError(fmt.Errorf("%w：结构化子报告材料存储不可用", errStorage))
	}
	if items == nil {
		// 旧字符串只作兼容材料，不能通过适配伪造逐条精准引用。
		items = []tools.ReportFinding{{ID: "legacy", Statement: boundedHandoffText(report.Findings, 600), Evidence: append([]tools.EvidenceRef(nil), report.Evidence...), Status: "legacy", ScopeRequirement: "fragment"}}
	} else {
		for i := range items {
			items[i] = r.checkHandoffFinding(ctx, items[i], reads)
		}
	}
	items = checkHandoffDependencies(items)
	if report.Status == "" {
		report.Status = "SUCCEEDED"
	}
	report.Coverage = append([]schema.ReadReceipt(nil), reads...)
	execution := "succeeded"
	if report.Status != "SUCCEEDED" && report.Status != "PARTIAL" {
		execution = "stopped"
	}
	citations := "position_valid"
	valid := 0
	for _, f := range items {
		if f.Status == "position_valid" || f.Status == "exact_valid" || f.Status == "inference" {
			valid++
		}
		if f.Status == "exact_valid" {
			citations = "exact_valid"
		}
	}
	if valid < len(items) {
		citations = "partial"
	}
	if valid == 0 {
		citations = "unverified"
	}
	id := newID()
	h := &tools.ReportHandoff{ReportID: id, ArtifactID: id, OriginRunID: r.runID, Execution: execution, Citations: citations, Verification: "not_run", Coverage: r.coverageForPaths(ctx, scope), Items: append([]tools.ReportFinding(nil), items...)}
	if len(stopCode) > 0 {
		h.StopCode = stopCode[0]
	}
	report.Handoff = h
	report.ArtifactID = id
	if len(items) > 0 && items[0].Status != "legacy" {
		report.Findings = handoffFindings(items)
	}
	var refs []tools.EvidenceRef
	for _, f := range items {
		if f.Status == "position_valid" || f.Status == "exact_valid" {
			refs = append(refs, f.Evidence...)
		}
	}
	if len(refs) > 0 {
		report.Evidence = refs
	}
	material, err := json.Marshal(struct {
		Version  int                 `json:"report_version"`
		ParentID string              `json:"parent_agent_id"`
		RunID    string              `json:"origin_run_id"`
		Scope    []string            `json:"scope"`
		Report   tools.SubtaskReport `json:"report"`
	}{2, parentID, r.runID, scope, report})
	if err != nil {
		return report, r.noteStorageError(fmt.Errorf("%w：编码结构化子报告失败: %w", errStorage, err))
	}
	if err := r.artifact(id, material); err != nil {
		return report, r.noteStorageError(fmt.Errorf("%w：保存结构化子报告失败: %w", errStorage, err))
	}
	r.mu.Lock()
	if r.handoffs.reports == nil {
		r.handoffs.reports = make(map[string]storedHandoff)
	}
	r.handoffs.reports[id] = storedHandoff{ParentID: parentID, RunID: r.runID, Scope: append([]string(nil), scope...), Report: report, Items: append([]tools.ReportFinding(nil), items...)}
	r.mu.Unlock()
	observe.Emit(ctx, "artifact_saved", observe.Payload{ArtifactID: id, ContentBytes: int64(len(material))})
	return report, nil
}

// refreshHandoff 逐条撤销陈旧发现，不因一条引用过期删除整份报告。
func (r *runState) refreshHandoff(ctx context.Context, report tools.SubtaskReport) tools.SubtaskReport {
	if report.Handoff == nil {
		return report
	}
	h := *report.Handoff
	h.Items = nil
	stalePaths := make(map[string]bool)
	r.mu.Lock()
	saved, known := r.handoffs.reports[h.ReportID]
	r.mu.Unlock()
	narrow := false
	if known {
		for _, path := range saved.Scope {
			if !insideParentPaths(path, tools.ScopeFromContext(ctx).AllowedPaths) {
				narrow = true
				break
			}
		}
	}
	if narrow {
		h.UnverifiedOmitted += len(report.Unverified)
		report.Unverified = nil
	}
	for _, f := range report.Handoff.Items {
		allowed := true
		for _, ref := range f.Evidence {
			if !insideParentPaths(ref.Path, tools.ScopeFromContext(ctx).AllowedPaths) {
				allowed = false
				break
			}
		}
		if len(f.Evidence) == 0 && report.Handoff.Coverage != nil {
			for _, file := range report.Handoff.Coverage.Files {
				if !insideParentPaths(file.Path, tools.ScopeFromContext(ctx).AllowedPaths) {
					allowed = false
					break
				}
			}
		}
		if !allowed {
			h.Omitted++
			continue
		}
		h.Items = append(h.Items, f)
		i := len(h.Items) - 1
		if f.Status == "unverified" {
			continue
		}
		for _, ref := range f.Evidence {
			version, err := tools.FileVersion(ctx, ref.Path)
			if !insideParentPaths(ref.Path, tools.ScopeFromContext(ctx).AllowedPaths) || err != nil || version != ref.Version {
				stalePaths[ref.Path] = true
				h.Items[i].Status = "stale"
				h.Items[i].Limitations = "文件版本已变化，当前结论须重新核对。"
				break
			}
		}
	}
	if h.Coverage != nil {
		data, _ := json.Marshal(h.Coverage)
		var copy schema.ReadingCoverage
		_ = json.Unmarshal(data, &copy)
		permitted := copy.Files[:0]
		for _, file := range copy.Files {
			if insideParentPaths(file.Path, tools.ScopeFromContext(ctx).AllowedPaths) {
				if stalePaths[file.Path] {
					file.Complete = false
				}
				permitted = append(permitted, file)
			}
		}
		copy.Files = permitted
		copy.TotalFiles = len(permitted)
		copy.CompleteFiles = 0
		for _, file := range permitted {
			if file.Complete {
				copy.CompleteFiles++
			}
		}
		for _, f := range h.Items {
			if f.Status == "stale" {
				copy.Status = "unknown"
				copy.RecordedOnly = true
				break
			}
		}
		h.Coverage = &copy
	}
	h.Items = checkHandoffDependencies(h.Items)
	valid := 0
	for _, f := range h.Items {
		if f.Status == "position_valid" || f.Status == "exact_valid" || f.Status == "inference" {
			valid++
		}
	}
	if valid == 0 {
		h.Citations = "unverified"
	} else if valid < len(h.Items) {
		h.Citations = "partial"
	}
	report.Handoff = &h
	return report
}

// 依赖只在同一报告内解析；一条引用过期会撤销依赖它的推断，不影响独立发现。
func checkHandoffDependencies(items []tools.ReportFinding) []tools.ReportFinding {
	result := append([]tools.ReportFinding(nil), items...)
	indices := make(map[string]int)
	for i, f := range result {
		indices[f.ID] = i
	}
	visited := make(map[string]bool)
	active := make(map[string]bool)
	var valid func(string) bool
	valid = func(id string) bool {
		i, ok := indices[id]
		if !ok {
			return false
		}
		if active[id] {
			return false
		}
		if visited[id] {
			return result[i].Status != "stale" && result[i].Status != "unverified"
		}
		visited[id] = true
		active[id] = true
		defer delete(active, id)
		if result[i].Status == "stale" || result[i].Status == "unverified" {
			return false
		}
		for _, dependency := range result[i].DependsOn {
			if !valid(dependency) {
				result[i].Status = "unverified"
				result[i].Limitations = boundedHandoffText(result[i].Limitations+" 依赖发现未核实或已失效："+dependency, 1000)
				return false
			}
		}
		return true
	}
	for _, f := range result {
		_ = valid(f.ID)
	}
	return result
}

func handoffPaths(items []tools.ReportFinding) []string {
	seen := make(map[string]bool)
	var paths []string
	for _, f := range items {
		for _, ref := range f.Evidence {
			if !seen[ref.Path] {
				seen[ref.Path] = true
				paths = append(paths, ref.Path)
			}
		}
	}
	return paths
}

// validHandoffReview 只接受当前父任务实际持有、且目标文件版本仍有效的发现身份。
func (r *runState) validHandoffReview(ctx context.Context, target, path, version string) bool {
	if target == "" {
		return false
	}
	r.mu.Lock()
	var reports []storedHandoff
	for _, saved := range r.handoffs.reports {
		if saved.ParentID == observe.Identity(ctx).AgentID && saved.RunID == r.runID {
			reports = append(reports, saved)
		}
	}
	r.mu.Unlock()
	if !filepath.IsLocal(path) || !insideParentPaths(path, tools.ScopeFromContext(ctx).AllowedPaths) {
		return false
	}
	current, err := tools.FileVersion(ctx, path)
	if err != nil || current != version {
		return false
	}
	for _, saved := range reports {
		for _, f := range saved.Items {
			for _, ref := range f.Evidence {
				if ref.Path == path && ref.Version == version && (target == f.ID || target == ref.ActionID || target == ref.EvidenceID) {
					for _, read := range saved.Report.Coverage {
						if ref.ActionID != "" && ref.ActionID == read.ActionID && read.Path == path && read.Version == version && ref.Line >= read.FirstLine && ref.Line <= read.LastLine {
							return true
						}
					}
				}
			}
		}
	}
	return false
}

// handoffArtifactAccess 供通用材料读取入口保持相同的父任务及路径权限边界。
func (r *runState) handoffArtifactAccess(ctx context.Context, id string) (bool, bool) {
	r.mu.Lock()
	saved, ok := r.handoffs.reports[id]
	r.mu.Unlock()
	if !ok {
		return false, false
	}
	if saved.RunID != r.runID || saved.ParentID != observe.Identity(ctx).AgentID {
		return true, false
	}
	for _, path := range saved.Scope {
		if !insideParentPaths(path, tools.ScopeFromContext(ctx).AllowedPaths) {
			return true, false
		}
	}
	return true, true
}

type readReportTool struct{}

func (readReportTool) Name() string { return "read_report" }
func (readReportTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{Name: "read_report", Description: "按发现 ID 读取本次子任务的完整结构化发现；不要按字节翻长 JSON，未展示项可先读取目录。", InputSchema: map[string]any{"type": "object", "required": []string{"report_id"}, "properties": map[string]any{"report_id": map[string]any{"type": "string"}, "finding_ids": map[string]any{"type": "array", "maxItems": 8, "items": map[string]any{"type": "string"}, "description": "最多8个发现ID；省略或空数组返回短目录"}, "after_id": map[string]any{"type": "string", "description": "目录续读使用上页next_after_id；不是字节offset，不能与finding_ids同时使用"}}}}
}
func (readReportTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var args struct {
		ReportID   string   `json:"report_id"`
		FindingIDs []string `json:"finding_ids"`
		AfterID    string   `json:"after_id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil || args.ReportID == "" || len(args.FindingIDs) > 8 || args.AfterID != "" && len(args.FindingIDs) > 0 {
		return "", errors.New("read_report 需要 report_id，finding_ids 最多8项")
	}
	r := currentRun(ctx)
	if r == nil {
		return "", errors.New("当前没有可读取的子报告")
	}
	r.mu.Lock()
	saved, ok := r.handoffs.reports[args.ReportID]
	r.mu.Unlock()
	if !ok || saved.RunID != r.runID || saved.ParentID != observe.Identity(ctx).AgentID {
		return "", errors.New("报告不属于本次运行或当前父任务；历史材料请使用已保存的材料引用")
	}
	report := r.refreshHandoff(ctx, saved.Report)
	h := report.Handoff
	if len(args.FindingIDs) == 0 {
		copy := *h
		copy.Items = append([]tools.ReportFinding(nil), h.Items...)
		copy.Omitted = 0
		if args.AfterID != "" {
			start := -1
			for i, f := range copy.Items {
				if f.ID == args.AfterID {
					start = i + 1
					break
				}
			}
			if start < 0 {
				return "", errors.New("目录游标不属于本报告，请重新读取目录")
			}
			copy.Items = copy.Items[start:]
		}
		for i := range copy.Items {
			copy.Items[i].Evidence = nil
			copy.Items[i].Context = ""
			copy.Items[i].Limitations = ""
			copy.Items[i].Statement = boundedHandoffText(copy.Items[i].Statement, 120)
		}
		wire := tools.SubtaskReport{Handoff: &copy, Status: report.Status}
		data, err := tools.EncodeSubtaskHandoff(wire)
		return string(data), err
	}
	var selected []tools.ReportFinding
	for _, id := range args.FindingIDs {
		found := false
		for _, f := range h.Items {
			if f.ID == id {
				for _, ref := range f.Evidence {
					if !insideParentPaths(ref.Path, tools.ScopeFromContext(ctx).AllowedPaths) {
						return "", errors.New("发现证据超出当前任务权限")
					}
				}
				selected = append(selected, f)
				found = true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("报告中没有发现 %q，请先读取目录", id)
		}
	}
	data, err := json.Marshal(struct {
		ReportID   string                `json:"report_id"`
		ArtifactID string                `json:"artifact_id"`
		Findings   []tools.ReportFinding `json:"findings"`
	}{h.ReportID, h.ArtifactID, selected})
	if err != nil {
		return "", err
	}
	if len(data) > 8192 {
		data, _ = json.Marshal(map[string]any{"report_id": h.ReportID, "artifact_id": h.ArtifactID, "detail_oversized": true, "message": "选定发现超过8 KiB，请减少 finding_ids；单项仍过大时通过 read_evidence 读取完整材料。"})
	}
	return string(data), nil
}
