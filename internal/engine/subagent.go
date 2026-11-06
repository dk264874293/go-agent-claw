package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func (e *AgentEngine) RunSub(ctx context.Context, spec tools.SubtaskSpec, registry tools.Registry, rawReporter any) (report tools.SubtaskReport, err error) {
	report = tools.SubtaskReport{Status: string(Failed)}
	parent := observe.Identity(ctx)
	r := currentRun(ctx)
	if r == nil {
		return report, errors.New("子任务必须使用父任务预算")
	}
	childID := observe.NewID()
	if admitErr := r.admitSubtask(ctx, spec, childID); admitErr != nil {
		return report, admitErr
	}
	if admitted := r.admittedSubtaskPaths(childID); admitted != nil {
		spec.AllowedPaths = admitted
	}
	ctx = observe.WithAgent(ctx, childID, parent.AgentID)
	ctx = observe.WithAction(observe.WithModelCall(ctx, ""), "")
	defer func() {
		if finishErr := r.finishSubtask(ctx, childID, report.Status); finishErr != nil {
			err = errors.Join(err, r.noteStorageError(finishErr))
			report.Status = string(Failed)
		}
	}()
	started := time.Now()
	observe.Emit(ctx, "agent_queued", observe.Payload{ParentActionID: parent.ActionID})
	defer func() {
		observe.Emit(ctx, "agent_finished", observe.Payload{ParentActionID: parent.ActionID, Status: report.Status, ErrorClass: observationError(err), DurationMS: time.Since(started).Milliseconds()})
	}()
	select {
	case r.subSlots <- struct{}{}:
		defer func() { <-r.subSlots }()
	case <-ctx.Done():
		stopped, _ := (RunResult{}).stop(ctx, ctx.Err())
		report.Status = string(stopped.Status)
		return report, ctx.Err()
	}
	observe.Emit(ctx, "agent_started", observe.Payload{ParentActionID: parent.ActionID})
	scope := tools.ScopeFromContext(ctx)
	if scope.DisableSubagent {
		return report, errors.New("子任务不允许再次委派")
	}
	if len(spec.AllowedPaths) > 0 {
		for _, p := range spec.AllowedPaths {
			if !filepath.IsLocal(p) || !insideParentPaths(p, scope.AllowedPaths) {
				return report, fmt.Errorf("子任务路径超出父任务范围: %q", p)
			}
		}
		scope.AllowedPaths = spec.AllowedPaths
	}
	scope.Mode, scope.DisableSubagent = tools.ModeReadOnly, true
	ctx = tools.WithScope(ctx, scope)
	maxTurns := spec.MaxTurns
	if maxTurns <= 0 || maxTurns > 10 {
		maxTurns = 10
	}

	session := ctxpkg.NewSession(newID(), scope.WorkDir)
	session.Append(schema.Message{Role: schema.RoleUser, Content: spec.Goal + "\n预期证据：" + spec.ExpectedEvidence})
	system := schema.Message{Role: schema.RoleSystem, Content: `你是只读代码探路者。用工具调查父任务指定的范围。
只交付一个 JSON 对象，findings 必须是字符串；evidence 为证据数组，每项包含 path、line、version，版本来自实际 read_file 回执；unverified 为字符串数组。
示例：{"findings":"发现摘要","evidence":[{"path":"src/main.go","line":12,"version":"实际读取的版本"}],"unverified":[]}。
不要自行报告成功状态。引用只说明读取依据，不等于分析结论已经验收。遇到矛盾或缺少文件时注明未核实项。`}
	if r.state.Delegation != nil {
		system = schema.Message{Role: schema.RoleSystem, Content: structuredSubtaskPrompt}
	}
	if r.analysisEnabled() {
		system = schema.Message{Role: schema.RoleSystem, Content: "你是只读分析子任务。仅调查父任务指定的范围，使用统一分析契约交付逐条发现和原文证据；不要自行报告成功或验收状态。"}
	}
	reporter, _ := rawReporter.(Reporter)
	text, runErr := e.converse(ctx, r, session, system, registry, reporter, "", "", maxTurns)
	reads := r.agentReads(observe.Identity(ctx).AgentID)
	if runErr != nil {
		r.noteStorageError(runErr)
		stopped, _ := (RunResult{}).stop(ctx, runErr)
		localCode := ""
		if local, ok := runErr.(interface{ SubtaskLimitCode() string }); ok {
			stopped.Status = Blocked
			localCode = local.SubtaskLimitCode()
		}
		failed, cause := r.failedSubreport(ctx, session, text, reads, string(stopped.Status), runErr)
		if errors.Is(cause, errStorage) {
			return failed, cause
		}
		failed, saveErr := r.finalizeHandoff(ctx, parent.AgentID, scope.AllowedPaths, failed, nil, reads, localCode)
		if saveErr != nil {
			return failed, saveErr
		}
		return failed, cause
	}
	var parsed tools.SubtaskReport
	var parseErr error
	var items []tools.ReportFinding
	var indexed bool
	if r.analysisEnabled() {
		if analysis := r.analysisReport(ctx); analysis != nil {
			items = analysisHandoffItems(*analysis)
			indexed = true
			parsed.Findings = handoffFindings(items)
			parsed.Unverified = append([]string(nil), analysis.Unverified...)
			for _, f := range analysis.Findings {
				for _, ref := range f.Evidence {
					parsed.Evidence = append(parsed.Evidence, tools.EvidenceRef{Path: ref.Path, Line: ref.StartLine, Version: ref.Version})
				}
			}
		} else {
			parseErr = errAnalysisReportInvalid
		}
	} else {
		parsed, items, indexed, parseErr = decodeIndexedSubreport(text)
	}
	if parseErr != nil {
		failed, cause := r.failedSubreport(ctx, session, text, reads, string(Failed), parseErr)
		if errors.Is(cause, errStorage) {
			return failed, cause
		}
		failed, saveErr := r.finalizeHandoff(ctx, parent.AgentID, scope.AllowedPaths, failed, nil, reads)
		if saveErr != nil {
			return failed, saveErr
		}
		return failed, cause
	}
	report = parsed
	report.Coverage = reads
	refs := report.Evidence[:0]
	for _, ref := range report.Evidence {
		ref.Path = filepath.Clean(ref.Path)
		version, err := tools.FileVersion(ctx, ref.Path)
		if err != nil || ref.Line < 1 || ref.Version == "" || version != ref.Version || !containsReceiptLine(reads, ref) {
			report.Unverified = append(report.Unverified, fmt.Sprintf("未能核实 %s:%d 的读取证据", ref.Path, ref.Line))
			continue
		}
		refs = append(refs, ref)
	}
	report.Evidence = refs
	report.Status = "SUCCEEDED"
	if !indexed && (len(refs) == 0 || len(report.Unverified) != 0) {
		report.Status = "PARTIAL"
	}
	report, err = r.finalizeHandoff(ctx, parent.AgentID, scope.AllowedPaths, report, items, reads)
	if err != nil {
		return report, err
	}
	// 摘要只传给父 Agent；其他子任务仅能得到权限范围内的覆盖元数据。
	r.rememberSubreport(parent.AgentID, report, reads)
	return report, nil
}

// decodeSubtaskReport 保留对外字符串契约，并确定性兼容旧模型返回的对象和数组。
func decodeSubtaskReport(text string) (tools.SubtaskReport, error) {
	var report tools.SubtaskReport
	body := strings.TrimSpace(text)
	if strings.HasPrefix(body, "```") {
		if i := strings.IndexByte(body, '\n'); i >= 0 && strings.HasSuffix(body, "```") {
			body = strings.TrimSpace(strings.TrimSuffix(body[i+1:], "```"))
		}
	}
	var raw struct {
		Findings   json.RawMessage     `json:"findings"`
		Evidence   []tools.EvidenceRef `json:"evidence"`
		Unverified []string            `json:"unverified"`
	}
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		return report, fmt.Errorf("报告未通过结构校验: %w", err)
	}
	value := bytes.TrimSpace(raw.Findings)
	if len(value) == 0 {
		return report, errors.New("报告未通过结构校验：缺少 findings")
	}
	switch value[0] {
	case '"':
		if err := json.Unmarshal(value, &report.Findings); err != nil {
			return report, fmt.Errorf("报告 findings 无效: %w", err)
		}
	case '{', '[':
		var data any
		decoder := json.NewDecoder(bytes.NewReader(value))
		decoder.UseNumber()
		if err := decoder.Decode(&data); err != nil {
			return report, fmt.Errorf("报告 findings 无效: %w", err)
		}
		encoded, err := json.Marshal(data)
		if err != nil {
			return report, fmt.Errorf("报告 findings 无法编码: %w", err)
		}
		report.Findings = string(encoded)
	default:
		return report, errors.New("报告 findings 必须是字符串、对象或数组")
	}
	if strings.TrimSpace(report.Findings) == "" {
		return report, errors.New("报告 findings 不能为空")
	}
	report.Evidence, report.Unverified = raw.Evidence, raw.Unverified
	return report, nil
}

func containsReceiptLine(reads []schema.ReadReceipt, ref tools.EvidenceRef) bool {
	for _, read := range reads {
		if read.Path == ref.Path && read.Version == ref.Version && ref.Line >= read.FirstLine && ref.Line <= read.LastLine && read.FirstLine > 0 {
			return true
		}
	}
	return false
}

// failedSubreport 保存原始材料及已确认的读取回执，不把失败步骤文字冒充有效结论。
func (r *runState) failedSubreport(ctx context.Context, session *ctxpkg.Session, text string, reads []schema.ReadReceipt, status string, cause error) (tools.SubtaskReport, error) {
	report := tools.SubtaskReport{
		Status: status, Findings: "子任务未完成；以下读取回执仅说明动作当时的观察。",
		Coverage: reads, Unverified: []string{cause.Error()},
	}
	for _, ref := range readReferences(reads) {
		version, err := tools.FileVersion(ctx, ref.Path)
		if err == nil && version == ref.Version {
			report.Evidence = append(report.Evidence, ref)
		}
	}
	if r.artifact != nil {
		// 只保存对外内容和读取元数据；完整请求、工具正文及 Responses 私密项不复制到材料。
		var fragments []string
		for _, message := range session.GetWorkingMemory(0) {
			if message.Role == schema.RoleAssistant && strings.TrimSpace(message.Content) != "" {
				fragments = append(fragments, message.Content)
			}
		}
		material, err := json.Marshal(struct {
			Response           string               `json:"response"`
			AssistantFragments []string             `json:"assistant_fragments"`
			Reads              []schema.ReadReceipt `json:"reads"`
			StopReason         string               `json:"stop_reason"`
		}{text, fragments, reads, observe.Redact(cause.Error())})
		if err != nil {
			return report, r.noteStorageError(fmt.Errorf("%w：编码子任务材料失败: %w", errStorage, err))
		}
		id := newID()
		if err := r.artifact(id, material); err != nil {
			return report, r.noteStorageError(fmt.Errorf("%w：保存子任务材料失败: %w", errStorage, err))
		}
		report.ArtifactID = id
		report.Unverified = append(report.Unverified, "原始材料引用："+id+"，可用 read_evidence 分页读取；这些材料未作为成功报告接收")
		observe.Emit(ctx, "artifact_saved", observe.Payload{ArtifactID: id, ContentBytes: int64(len(material))})
	}
	return report, cause
}

func insideParentPaths(p string, paths []string) bool {
	if len(paths) == 0 {
		return true
	}
	for _, parent := range paths {
		rel, err := filepath.Rel(parent, p)
		if err == nil && filepath.IsLocal(rel) {
			return true
		}
	}
	return false
}
