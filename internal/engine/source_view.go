package engine

import (
	"bytes"
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

func (r *runState) sourceViewsEnabled() bool {
	return r.readFeatureEnabled("source_views")
}

type sourceViewArgs struct {
	ID     string             `json:"id"`
	Offset int                `json:"offset"`
	Limit  int                `json:"limit"`
	Review *schema.ReadReview `json:"review,omitempty"`
}

func parseSourceViewArgs(raw json.RawMessage) (sourceViewArgs, error) {
	var a sourceViewArgs
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return a, errors.New("证据读取参数必须为JSON对象")
	}
	for k, v := range fields {
		if k != "id" && k != "offset" && k != "limit" && k != "review" {
			return a, fmt.Errorf("未知证据读取参数 %s", k)
		}
		if bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return a, fmt.Errorf("参数 %s 不能为null", k)
		}
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return a, err
	}
	if a.ID == "" {
		return a, errors.New("缺少材料id")
	}
	if a.Limit == 0 {
		a.Limit = 4000
	}
	if a.Offset < 0 || a.Limit < 1 || a.Limit > 4000 {
		return a, errors.New("offset必须非负；limit须为1至4000，0兼容使用默认4000")
	}
	if a.Review != nil && (strings.TrimSpace(a.Review.TargetID) == "" || len(a.Review.TargetID) > 128 || strings.TrimSpace(a.Review.Question) == "" || len(a.Review.Question) > 600) {
		return a, errors.New("review须有target_id（1至128字节）和具体question（1至600字节）")
	}
	return a, nil
}

func sourceViewDefinition(def schema.ToolDefinition) schema.ToolDefinition {
	// 先复制顶层及properties，不修改注册表原Schema。
	copy := map[string]any{}
	base, ok := def.InputSchema.(map[string]any)
	if !ok {
		return def
	}
	for k, v := range base {
		copy[k] = v
	}
	props := map[string]any{}
	if original, ok := copy["properties"].(map[string]any); ok {
		for k, v := range original {
			props[k] = v
		}
	}
	props["review"] = map[string]any{"type": "object", "description": "重新核对已读来源时，关联当前动作、证据或发现并说明问题；不绕过权限或预算", "required": []string{"target_id", "question"}, "additionalProperties": false, "properties": map[string]any{"target_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}, "question": map[string]any{"type": "string", "minLength": 1, "maxLength": 600}}}
	copy["properties"] = props
	def.InputSchema = copy
	def.Description = "分页查看已保存材料。当前文件原文返回编号来源视图和新证据ID；通用工具输出及历史材料保持普通分页，不成为新来源依据。offset为材料内字节，不是源文件行号。"
	return def
}

// sourceOrigin只从原始读取身份查询；视图不会反向覆盖同一个artifact的来源。
func (r *runState) sourceOrigin(id string) (AnalysisEvidence, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if m := r.state.ReadMemory; m != nil {
		if eid := m.SourceArtifacts[id]; eid != "" {
			e, ok := m.Evidence[eid]
			if ok && e.ArtifactID == id && e.Receipt.SourceView == nil {
				if live, exists := r.analysisRuntime.sources[eid]; exists {
					e = live
				}
				return e, true
			}
			return AnalysisEvidence{}, false
		}
	}
	// 旧记录没有索引，只用于历史展示，不授予当前读取资格。
	if a := analysisStateOf(r.state); a != nil {
		var found AnalysisEvidence
		count := 0
		for _, e := range a.Evidence {
			if e.ArtifactID == id && e.Receipt.SourceView == nil {
				found = e
				count++
			}
		}
		if count == 1 {
			return found, true
		}
	}
	return AnalysisEvidence{}, false
}

// readEvidenceContent是所有消费者读取来源材料的唯一切片入口；不扩大证据范围。
func (r *runState) readEvidenceContent(entry AnalysisEvidence) ([]byte, error) {
	if view := entry.Receipt.SourceView; view != nil {
		origin, ok := r.sourceOrigin(view.ArtifactID)
		if !ok || origin.ID != view.OriginEvidenceID || origin.ActionID != view.OriginActionID || origin.Receipt.SourceView != nil || entry.ArtifactID != view.ArtifactID || view.ArtifactStart < 0 || view.ArtifactEnd < view.ArtifactStart || view.ArtifactEnd > origin.Receipt.ByteEnd-origin.Receipt.ByteStart || entry.Receipt.Path != origin.Receipt.Path || entry.Receipt.Version != origin.Receipt.Version || entry.Receipt.ByteStart != origin.Receipt.ByteStart+view.ArtifactStart || entry.Receipt.ByteEnd != origin.Receipt.ByteStart+view.ArtifactEnd || entry.RunID != origin.RunID {
			return nil, r.noteStorageError(fmt.Errorf("%w：来源视图原始身份或切片无效", errStorage))
		}
	}
	if entry.Source != nil {
		if int64(len(entry.Source)) != entry.Receipt.ByteEnd-entry.Receipt.ByteStart {
			return nil, r.noteStorageError(fmt.Errorf("%w：来源缓存长度不符", errStorage))
		}
		return append([]byte(nil), entry.Source...), nil
	}
	data, err := r.readReportMaterial(entry.ArtifactID)
	if err != nil {
		return nil, err
	}
	if view := entry.Receipt.SourceView; view != nil {
		if view.ArtifactID != entry.ArtifactID || view.ArtifactStart < 0 || view.ArtifactEnd < view.ArtifactStart || view.ArtifactEnd > int64(len(data)) {
			return nil, r.noteStorageError(fmt.Errorf("%w：来源视图区间无效", errStorage))
		}
		data = data[view.ArtifactStart:view.ArtifactEnd]
	}
	if int64(len(data)) != entry.Receipt.ByteEnd-entry.Receipt.ByteStart {
		return nil, r.noteStorageError(fmt.Errorf("%w：来源材料长度不符", errStorage))
	}
	return data, nil
}

// currentSourceOrigin必须同时通过当前运行、代次、权限和源版本校验。
func (r *runState) currentSourceOrigin(ctx context.Context, origin AnalysisEvidence) error {
	who := observe.Identity(ctx)
	r.mu.Lock()
	_, live := r.analysisRuntime.sources[origin.ID]
	indexed := r.state.ReadMemory != nil && r.state.ReadMemory.SourceArtifacts[origin.ArtifactID] == origin.ID
	if indexed {
		saved, ok := r.state.ReadMemory.Evidence[origin.ID]
		indexed = ok && saved.ArtifactID == origin.ArtifactID && saved.Receipt.SourceView == nil && saved.ActionID == origin.ActionID && saved.RunID == origin.RunID
	}
	invalid := r.analysisRuntime.invalidated[origin.ID] != ""
	owner := origin.AgentID == who.AgentID
	parent := who.ParentAgentID == "" && r.analysisRuntime.parents[origin.AgentID] == who.AgentID && r.analysisRuntime.delivered[origin.AgentID][origin.ID]
	r.mu.Unlock()
	if origin.RunID != r.runID || !live || !indexed || invalid {
		return errors.New("原材料属于历史或已失效读取，请使用read_file/read_lines重新取得当前依据")
	}
	if !owner && !parent {
		return errors.New("原材料未交付给当前Agent")
	}
	if !filepath.IsLocal(origin.Receipt.Path) || !insideParentPaths(origin.Receipt.Path, tools.ScopeFromContext(ctx).AllowedPaths) {
		return errors.New("来源路径超出当前权限")
	}
	_, err := r.analysisLookup(ctx, false)(origin.ID)
	return err
}

func (evidenceTool) executeSourceLegacy3(ctx context.Context, raw json.RawMessage) (string, *schema.ReadReceipt, []byte, error) {
	r := currentRun(ctx)
	if r == nil || !r.sourceViewsEnabled() {
		if r != nil && observe.Identity(ctx).ParentAgentID != "" {
			return "", nil, nil, tools.NewRejectedError("权限不足", "source_view_unavailable", errors.New("历史任务子Agent未启用来源视图入口"))
		}
		out, err := (evidenceTool{}).executeLegacy(ctx, raw)
		return out, nil, nil, err
	}
	args, err := parseSourceViewArgs(raw)
	if err != nil {
		return "", nil, nil, tools.NewRejectedError("参数错误", "invalid_evidence_range", err)
	}
	origin, source := r.sourceOrigin(args.ID)
	if !source {
		if known, allowed, historical := r.localCheckMaterialAccess(ctx, args.ID); known {
			if !allowed {
				return "", nil, nil, tools.NewRejectedError("权限不足", "local_check_material_forbidden", errors.New("核对材料超出当前Agent或路径权限"))
			}
			clean, _ := json.Marshal(map[string]any{"id": args.ID, "offset": args.Offset, "limit": args.Limit})
			out, err := (evidenceTool{}).executeLegacy(ctx, clean)
			if historical {
				out = "历史核对材料（不作为当前核对或来源依据）\n" + out
			}
			return out, nil, nil, err
		}
		if args.Review != nil {
			return "", nil, nil, tools.NewRejectedError("参数错误", "invalid_review_target", errors.New("通用材料没有文件来源复核身份，review仅用于当前来源视图"))
		}
		out, err := (evidenceTool{}).executeLegacy(ctx, raw)
		return out, nil, nil, err
	}
	who := observe.Identity(ctx)
	if !filepath.IsLocal(origin.Receipt.Path) || !insideParentPaths(origin.Receipt.Path, tools.ScopeFromContext(ctx).AllowedPaths) {
		return "", nil, nil, tools.NewRejectedError("权限不足", "source_view_forbidden", errors.New("来源路径超出权限"))
	}
	if origin.RunID != r.runID {
		if args.Review != nil {
			return "", nil, nil, tools.NewRejectedError("复核目标无效", "invalid_review_target", errors.New("历史材料没有当前复核资格，请重新读取工作区来源"))
		}
		if who.ParentAgentID != "" {
			return "", nil, nil, tools.NewRejectedError("权限不足", "source_view_forbidden", errors.New("子任务不能读取其他运行来源"))
		}
		legacy, _ := json.Marshal(map[string]any{"id": args.ID, "offset": args.Offset, "limit": args.Limit})
		out, err := (evidenceTool{}).executeLegacy(ctx, legacy)
		observe.Emit(ctx, "source_view_historical", observe.Payload{ArtifactID: args.ID, OriginEvidenceID: origin.ID, SourceKind: "historical"})
		return "历史来源材料（不取得当前读取或处理资格）\n" + out, nil, nil, err
	}
	if err := r.currentSourceOrigin(ctx, origin); err != nil {
		return "", nil, nil, tools.NewRejectedError("来源失效", "source_view_invalidated", err)
	}
	data, err := r.readEvidenceContent(origin)
	if err != nil {
		return "", nil, nil, err
	}
	page, err := r.readArtifactPage(args.ID, args.Offset, args.Limit)
	if err != nil {
		return "", nil, nil, err
	}
	if page.Total != int64(len(data)) {
		return "", nil, nil, r.noteStorageError(fmt.Errorf("%w：原材料长度变化", errStorage))
	}
	start := min(page.Offset, page.Total)
	first := origin.Receipt.FirstLine + bytes.Count(data[:start], []byte{'\n'})
	starts := start > 0 && data[start-1] == '\n' || start == 0 && origin.Receipt.LineBoundary != nil && origin.Receipt.LineBoundary.StartsLine
	body, consumed := numberSourceView(page.Data, first, !starts, args.Limit)
	if consumed == 0 && len(page.Data) > 0 {
		return "", nil, nil, tools.NewRejectedError("参数错误", "invalid_evidence_range", errors.New("limit无法容纳编号和完整字符，请增大limit"))
	}
	content := append([]byte{}, page.Data[:consumed]...)
	end := start + int64(consumed)
	last := first + bytes.Count(content, []byte{'\n'})
	if len(content) == 0 {
		first, last = 0, 0
	} else if content[len(content)-1] == '\n' {
		last--
	}
	ends := len(content) > 0 && content[len(content)-1] == '\n' || end == int64(len(data)) && origin.Receipt.LineBoundary != nil && origin.Receipt.LineBoundary.EndsLine
	receipt := &schema.ReadReceipt{Path: origin.Receipt.Path, Version: origin.Receipt.Version, ByteStart: origin.Receipt.ByteStart + start, ByteEnd: origin.Receipt.ByteStart + end, FirstLine: first, LastLine: last, TotalBytes: origin.Receipt.TotalBytes, Review: args.Review, LineBoundary: &schema.ReadBoundary{StartsLine: starts, EndsLine: ends}, SourceView: &schema.SourceView{OriginEvidenceID: origin.ID, OriginActionID: origin.ActionID, ArtifactID: args.ID, ArtifactStart: start, ArtifactEnd: end}}
	next := "无"
	if end < page.Total {
		next = fmt.Sprint(end)
	}
	out := fmt.Sprintf("证据 %s，范围 [%d,%d)\n总字节数: %d\n下一段 offset: %s\nEOF: %t\n文件: %s\n版本: %s\n源字节范围: [%d,%d)，源文件共 %d 字节\n行号范围: [%d,%d]\n源文件 EOF: %t\n注意：上方 offset/EOF 针对已保存材料；工作区文件续读使用 read_file 的源文件字节 offset。\n\n%s", args.ID, start, end, page.Total, next, end >= page.Total, receipt.Path, receipt.Version, receipt.ByteStart, receipt.ByteEnd, receipt.TotalBytes, first, last, receipt.ByteEnd >= receipt.TotalBytes, body)
	return out, receipt, content, nil
}

// 显示预算包括编号；消费量只计算源字节，不把编号前缀写入证据。
func numberSourceView(data []byte, line int, continued bool, limit int) (string, int) {
	var out strings.Builder
	consumed := 0
	lineStart := true
	for consumed < len(data) {
		prefix := ""
		if lineStart {
			if continued {
				prefix = fmt.Sprintf("%d（续行） | ", line)
			} else {
				prefix = fmt.Sprintf("%d | ", line)
			}
		}
		r, n := utf8.DecodeRune(data[consumed:])
		if out.Len()+len(prefix)+n > limit {
			break
		}
		out.WriteString(prefix)
		out.Write(data[consumed : consumed+n])
		consumed += n
		lineStart = r == '\n'
		if lineStart {
			line++
		}
		continued = false
	}
	return out.String(), consumed
}

func validateSourceViews(s taskState) error {
	if s.ReadMemory == nil {
		return nil
	}
	m := s.ReadMemory
	originals := map[string]string{}
	for id, e := range m.Evidence {
		if e.Receipt.SourceView == nil {
			if previous := originals[e.ArtifactID]; previous != "" && previous != id {
				return errors.New("同一材料对应多个原始来源")
			}
			originals[e.ArtifactID] = id
		}
	}
	for artifact, id := range m.SourceArtifacts {
		e, ok := m.Evidence[id]
		if !ok || e.ArtifactID != artifact || e.Receipt.SourceView != nil {
			return errors.New("原文材料索引不一致")
		}
	}
	for _, e := range m.Evidence {
		v := e.Receipt.SourceView
		if v == nil {
			continue
		}
		if s.Spec.ReadContextVersion == 4 && (s.Spec.ReadFeatures == nil || !s.Spec.ReadFeatures.SourceViews) {
			return errors.New("来源视图与当前实验配置不一致")
		}
		origin, ok := m.Evidence[v.OriginEvidenceID]
		if (s.Spec.ReadContextVersion != 3 && s.Spec.ReadContextVersion != 4) || !ok || origin.Receipt.SourceView != nil || origin.ArtifactID != v.ArtifactID || origin.ActionID != v.OriginActionID || e.ArtifactID != v.ArtifactID || m.SourceArtifacts[v.ArtifactID] != origin.ID || v.ArtifactStart < 0 || v.ArtifactEnd < v.ArtifactStart || v.ArtifactEnd > origin.Receipt.ByteEnd-origin.Receipt.ByteStart || e.Receipt.Path != origin.Receipt.Path || e.Receipt.Version != origin.Receipt.Version || e.RunID != origin.RunID || e.Receipt.ByteStart != origin.Receipt.ByteStart+v.ArtifactStart || e.Receipt.ByteEnd != origin.Receipt.ByteStart+v.ArtifactEnd {
			return errors.New("来源视图身份或切片范围无效")
		}
	}
	return nil
}

// normalizedReadRequest只使用当前运行已有源字节计算区间，不增加工作区IO或返回缓存正文。
func (r *runState) normalizedReadRequest(ctx context.Context, call schema.ToolCall) (reviewReadArgs, string, bool) {
	if call.Name != "read_evidence" {
		return readRequest(call, r.effectiveReadPageBytes())
	}
	if !r.sourceViewsEnabled() {
		return reviewReadArgs{}, "", false
	}
	if r.readFeaturesV2() {
		return r.normalizedViewRequestV2(ctx, call)
	}
	return r.normalizedArtifactRequest(ctx, call)
}

func (r *runState) normalizedArtifactRequest(ctx context.Context, call schema.ToolCall) (reviewReadArgs, string, bool) {
	var result reviewReadArgs
	args, err := parseSourceViewArgs(call.Arguments)
	if err != nil {
		return result, "", false
	}
	origin, ok := r.sourceOrigin(args.ID)
	if !ok || origin.RunID != r.runID {
		return result, "", false
	}
	r.mu.Lock()
	origin, ok = r.analysisRuntime.sources[origin.ID]
	invalid := r.analysisRuntime.invalidated[origin.ID] != ""
	r.mu.Unlock()
	if !ok || invalid || origin.Receipt.SourceView != nil || int64(len(origin.Source)) != origin.Receipt.ByteEnd-origin.Receipt.ByteStart {
		return result, "", false
	}
	start := min(int64(args.Offset), int64(len(origin.Source)))
	end := min(start+int64(args.Limit), int64(len(origin.Source)))
	if start < end && !utf8.RuneStart(origin.Source[start]) {
		return result, "", false
	}
	for end > start && end < int64(len(origin.Source)) && !utf8.RuneStart(origin.Source[end]) {
		end--
	}
	first := origin.Receipt.FirstLine + bytes.Count(origin.Source[:start], []byte{'\n'})
	starts := start > 0 && origin.Source[start-1] == '\n' || start == 0 && origin.Receipt.LineBoundary != nil && origin.Receipt.LineBoundary.StartsLine
	_, consumed := numberSourceView(origin.Source[start:end], first, !starts, args.Limit)
	offset := origin.Receipt.ByteStart + start
	limit := consumed
	numbered := false
	// EOF空页有独立身份，不能把limit=0变成默认读取请求。
	if limit == 0 {
		return result, "", false
	}
	result = reviewReadArgs{Path: origin.Receipt.Path, Offset: &offset, Limit: &limit, LineNumbers: &numbered, Review: args.Review}
	return result, fmt.Sprintf("%s:bytes:%d:%d", origin.Receipt.Path, offset, limit), true
}

// 核对材料可作为受限材料查看，但永不升级为文件来源证据。
func (r *runState) localCheckMaterialAccess(ctx context.Context, id string) (bool, bool, bool) {
	who := observe.Identity(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	known, historical := false, false
	for _, item := range r.state.LocalChecks {
		if item.ReceiptArtifactID != id {
			continue
		}
		known = true
		old := item.OriginRunID != r.runID
		historical = historical || old
		if item.Reason == "receipt_not_confirmed" {
			return true, false, historical
		}
		if who.ParentAgentID != "" && (old || item.AgentID != who.AgentID) {
			return true, false, historical
		}
		if !old && item.AgentID != who.AgentID && r.analysisRuntime.parents[item.AgentID] != who.AgentID {
			return true, false, false
		}
		for _, source := range item.Sources {
			if !filepath.IsLocal(source.Path) || !insideParentPaths(source.Path, tools.ScopeFromContext(ctx).AllowedPaths) {
				return true, false, historical
			}
		}
	}
	return known, known, historical
}
