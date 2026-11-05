package engine

import (
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
	ctx = observe.WithAgent(ctx, observe.NewID(), parent.AgentID)
	ctx = observe.WithAction(observe.WithModelCall(ctx, ""), "")
	started := time.Now()
	observe.Emit(ctx, "agent_queued", observe.Payload{ParentActionID: parent.ActionID})
	defer func() {
		observe.Emit(ctx, "agent_finished", observe.Payload{ParentActionID: parent.ActionID, Status: report.Status, ErrorClass: observationError(err), DurationMS: time.Since(started).Milliseconds()})
	}()
	r := currentRun(ctx)
	if r == nil {
		return report, errors.New("子任务必须使用父任务预算")
	}
	select {
	case r.subSlots <- struct{}{}:
		defer func() { <-r.subSlots }()
	case <-ctx.Done():
		report.Status = string(Cancelled)
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
用 JSON 对象交付 findings（发现摘要）、evidence（证据数组，每项含 path、line、version，其中版本来自 read_file）、unverified（未核实项数组）。
遇到矛盾或缺少文件时注明未核实项。`}
	reporter, _ := rawReporter.(Reporter)
	text, err := e.converse(ctx, r, session, system, registry, reporter, "", "", maxTurns)
	if err != nil {
		report.Findings = text
		if errors.Is(err, errBudget) || errors.Is(err, context.DeadlineExceeded) {
			report.Status = string(BudgetExhausted)
		} else if errors.Is(err, context.Canceled) {
			report.Status = string(Cancelled)
		} else if errors.Is(err, errBlocked) {
			report.Status = string(Blocked)
		}
		report.Unverified = []string{err.Error()}
		return report, err
	}
	body := strings.TrimSpace(text)
	if strings.HasPrefix(body, "```") {
		if i := strings.IndexByte(body, '\n'); i >= 0 {
			body = strings.TrimSpace(strings.TrimSuffix(body[i+1:], "```"))
		}
	}
	if err := json.Unmarshal([]byte(body), &report); err != nil {
		return tools.SubtaskReport{Findings: text, Status: string(Failed), Unverified: []string{"报告未通过结构校验"}}, err
	}
	reads := successfulSubReads(session)
	refs := report.Evidence[:0]
	for _, ref := range report.Evidence {
		version, err := tools.FileVersion(ctx, ref.Path)
		if err != nil || ref.Line < 1 || ref.Version == "" || version != ref.Version ||
			!containsReadLine(reads[filepath.Clean(ref.Path)], ref) {
			report.Unverified = append(report.Unverified, fmt.Sprintf("未能核实 %s:%d 的读取证据", ref.Path, ref.Line))
			continue
		}
		refs = append(refs, ref)
	}
	report.Evidence = refs
	report.Status = "SUCCEEDED"
	if len(refs) == 0 || len(report.Unverified) != 0 {
		report.Status = "PARTIAL"
	}
	return report, nil
}

type readRange struct {
	version     string
	first, last int
}

func successfulSubReads(session *ctxpkg.Session) map[string][]readRange {
	paths := make(map[string]string)
	reads := make(map[string][]readRange)
	for _, msg := range session.GetWorkingMemory(0) {
		for _, call := range msg.ToolCalls {
			if call.Name != "read_file" {
				continue
			}
			var args struct {
				Path string `json:"path"`
			}
			if json.Unmarshal(call.Arguments, &args) == nil {
				paths[call.ID] = filepath.Clean(args.Path)
			}
		}
		path, ok := paths[msg.ToolCallID]
		if !ok || !strings.HasPrefix(msg.Content, "执行状态：SUCCEEDED") {
			continue
		}
		header, _, _ := strings.Cut(msg.Content, "\n\n")
		var chunk readRange
		for _, line := range strings.Split(header, "\n") {
			if value, ok := strings.CutPrefix(line, "版本: "); ok {
				chunk.version = value
			}
			if strings.HasPrefix(line, "行号范围: ") {
				_, _ = fmt.Sscanf(line, "行号范围: [%d, %d]", &chunk.first, &chunk.last)
			}
		}
		if chunk.version != "" && chunk.first > 0 && chunk.last >= chunk.first {
			reads[path] = append(reads[path], chunk)
		}
	}
	return reads
}

func containsReadLine(ranges []readRange, ref tools.EvidenceRef) bool {
	for _, chunk := range ranges {
		if chunk.version == ref.Version && ref.Line >= chunk.first && ref.Line <= chunk.last {
			return true
		}
	}
	return false
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
