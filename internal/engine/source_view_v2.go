package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

type sourceViewArgsV2 struct {
	ID         string             `json:"id,omitempty"`
	EvidenceID string             `json:"evidence_id,omitempty"`
	Offset     int                `json:"offset,omitempty"`
	Limit      int                `json:"limit,omitempty"`
	Review     *schema.ReadReview `json:"review,omitempty"`
}

func parseSourceViewArgsV2(raw json.RawMessage) (sourceViewArgsV2, error) {
	var a sourceViewArgsV2
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return a, tools.NewRejectedError("参数错误", "invalid_evidence_range", fmt.Errorf("证据读取参数必须是JSON对象"))
	}
	for k, v := range fields {
		if k != "id" && k != "evidence_id" && k != "offset" && k != "limit" && k != "review" {
			return a, tools.NewRejectedError("参数错误", "invalid_evidence_range", fmt.Errorf("未知参数%s", k))
		}
		if bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return a, tools.NewRejectedError("参数错误", "invalid_evidence_range", fmt.Errorf("参数%s不能为null", k))
		}
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return a, tools.NewRejectedError("参数错误", "invalid_evidence_range", err)
	}
	if a.ID != "" && a.EvidenceID != "" {
		return a, tools.NewRejectedError("身份冲突", "ambiguous_evidence_identity", fmt.Errorf("id与evidence_id只能提供一个；文件证据使用evidence_id，材料使用id"))
	}
	if a.ID == "" && a.EvidenceID == "" {
		return a, tools.NewRejectedError("缺少身份", "missing_evidence_identity", fmt.Errorf("提供evidence_id或旧材料id之一"))
	}
	if a.Limit == 0 {
		a.Limit = 4000
	}
	if a.Offset < 0 || a.Limit < 1 || a.Limit > 4000 {
		return a, tools.NewRejectedError("范围错误", "invalid_evidence_range", fmt.Errorf("offset须非负，limit为1至4000，0兼容默认4000"))
	}
	if a.Review != nil && (strings.TrimSpace(a.Review.TargetID) == "" || len(a.Review.TargetID) > 128 || strings.TrimSpace(a.Review.Question) == "" || len(a.Review.Question) > 600) {
		return a, tools.NewRejectedError("复核参数错误", "invalid_review_target", fmt.Errorf("review需要target_id及具体question"))
	}
	return a, nil
}

func sourceViewDefinitionV2(def schema.ToolDefinition) schema.ToolDefinition {
	def = sourceViewDefinition(def)
	base := def.InputSchema.(map[string]any)
	props := base["properties"].(map[string]any)
	delete(base, "required")
	props["evidence_id"] = map[string]any{"type": "string", "description": "文件证据短ID，例如当前目录中的E19；精确选择该E实际读取切片，offset相对该切片。与id互斥。"}
	props["id"] = map[string]any{"type": "string", "description": "兼容旧的32位不可变材料ID，offset相对原材料；不是E短证据ID。与evidence_id互斥。"}
	def.Description = "取回已保存材料：文件证据用evidence_id，通用材料用id，必须且只能提供一个。offset相对所选对象，下一页复制next_call，不将游标改绑到新生成E。此工具仍计正常工具预算，读取不代表核验。"
	return def
}

func (evidenceTool) ExecuteReadSource(ctx context.Context, raw json.RawMessage) (string, *schema.ReadReceipt, []byte, error) {
	r := currentRun(ctx)
	if r == nil || !r.readFeaturesV2() {
		return (evidenceTool{}).executeSourceLegacy3(ctx, raw)
	}
	if !r.sourceViewsEnabled() {
		if r.localChecksEnabled() {
			args, err := parseSourceViewArgsV2(raw)
			if err != nil {
				return "", nil, nil, err
			}
			if args.EvidenceID != "" {
				return "", nil, nil, tools.NewRejectedError("实验能力关闭", "source_view_unavailable", fmt.Errorf("source_views未开启；局部核对仅允许读取已授权的回执材料"))
			}
			if observe.Identity(ctx).ParentAgentID != "" {
				known, allowed, _ := r.localCheckMaterialAccess(ctx, args.ID)
				if !known || !allowed {
					return "", nil, nil, tools.NewRejectedError("材料权限不足", "local_check_material_forbidden", fmt.Errorf("当前子任务只能查看自己本次运行已确认的核对回执"))
				}
			}
			legacy, _ := json.Marshal(map[string]any{"id": args.ID, "offset": args.Offset, "limit": args.Limit})
			out, err := (evidenceTool{}).executeLegacy(ctx, legacy)
			return out, nil, nil, err
		}
		return (evidenceTool{}).executeSourceLegacy3(ctx, raw)
	}
	args, err := parseSourceViewArgsV2(raw)
	if err != nil {
		return "", nil, nil, err
	}
	if args.EvidenceID == "" {
		// 只给已授权、当前存在的E提供可复制纠正，不能将猜测ID当成授权。
		if entry, lookupErr := r.selectedSourceEvidence(ctx, args.ID); lookupErr == nil && entry.ID == args.ID {
			correction := args
			correction.EvidenceID, correction.ID = args.ID, ""
			rawCorrection, _ := json.Marshal(correction)
			return "", nil, nil, tools.NewRejectedError("身份类型错误", "evidence_id_required", fmt.Errorf("%s是文件证据ID，请使用%s；本次未读取", args.ID, rawCorrection))
		}

		if len(args.ID) > 1 && args.ID[0] == 'E' {
			numeric := true
			for _, c := range args.ID[1:] {
				if c < '0' || c > '9' {
					numeric = false
				}
			}
			if numeric {
				return "", nil, nil, tools.NewRejectedError("证据不可用", "source_evidence_unavailable", fmt.Errorf("短证据ID不是材料id，且当前没有可授权的同名证据；请检查当前证据目录"))
			}
		}
		legacy, _ := json.Marshal(sourceViewArgs{ID: args.ID, Offset: args.Offset, Limit: args.Limit, Review: args.Review})
		return (evidenceTool{}).executeSourceLegacy3(ctx, legacy)
	}
	entry, err := r.selectedSourceEvidence(ctx, args.EvidenceID)
	if err != nil {
		return "", nil, nil, tools.NewRejectedError("证据不可用", "source_evidence_unavailable", err)
	}
	data, err := r.readEvidenceContent(entry)
	if err != nil {
		return "", nil, nil, err
	}
	if !utf8.Valid(data) {
		return "", nil, nil, tools.NewRejectedError("编码错误", "invalid_evidence_encoding", fmt.Errorf("证据不是有效UTF-8文本"))
	}
	start := min(args.Offset, len(data))
	if start < len(data) && !utf8.RuneStart(data[start]) {
		before, after := start, start
		for before > 0 && !utf8.RuneStart(data[before]) {
			before--
		}
		for after < len(data) && !utf8.RuneStart(data[after]) {
			after++
		}
		return "", nil, nil, tools.NewRejectedError("字符边界错误", "invalid_evidence_range", fmt.Errorf("offset切断UTF-8字符，请改为%d或%d", before, after))
	}
	end := min(start+args.Limit, len(data))
	for end > start && end < len(data) && !utf8.RuneStart(data[end]) {
		end--
	}
	first := entry.Receipt.FirstLine + bytes.Count(data[:start], []byte{'\n'})
	starts := start > 0 && data[start-1] == '\n' || start == 0 && entry.Receipt.LineBoundary != nil && entry.Receipt.LineBoundary.StartsLine
	body, consumed := numberSourceView(data[start:end], first, !starts, args.Limit)
	if consumed == 0 && end > start {
		return "", nil, nil, tools.NewRejectedError("显示范围过小", "invalid_evidence_range", fmt.Errorf("limit无法容纳编号及完整字符，请增大limit"))
	}
	content := append([]byte{}, data[start:start+consumed]...)
	end = start + consumed
	last := first + bytes.Count(content, []byte{'\n'})
	if len(content) == 0 {
		first, last = 0, 0
	} else if content[len(content)-1] == '\n' {
		last--
	}
	ends := len(content) > 0 && content[len(content)-1] == '\n' || end == len(data) && entry.Receipt.LineBoundary != nil && entry.Receipt.LineBoundary.EndsLine
	origin, ok := r.sourceOrigin(entry.ArtifactID)
	if !ok {
		return "", nil, nil, r.noteStorageError(fmt.Errorf("%w：证据缺少原始来源", errStorage))
	}
	base := int64(0)
	if v := entry.Receipt.SourceView; v != nil {
		base = v.ArtifactStart
	}
	receipt := &schema.ReadReceipt{Path: entry.Receipt.Path, Version: entry.Receipt.Version, ByteStart: entry.Receipt.ByteStart + int64(start), ByteEnd: entry.Receipt.ByteStart + int64(end), FirstLine: first, LastLine: last, TotalBytes: entry.Receipt.TotalBytes, Review: args.Review, LineBoundary: &schema.ReadBoundary{StartsLine: starts, EndsLine: ends}, SourceView: &schema.SourceView{OriginEvidenceID: origin.ID, OriginActionID: origin.ActionID, ArtifactID: origin.ArtifactID, ArtifactStart: base + int64(start), ArtifactEnd: base + int64(end)}}
	next := "无"
	nextCall := "无"
	if end < len(data) {
		next = fmt.Sprint(end)
		args.Offset = end
		call, _ := json.Marshal(args)
		nextCall = string(call)
	}
	out := fmt.Sprintf("选中证据: %s\n证据内范围: [%d,%d)\n总字节数: %d\n下一段 offset: %s\nEOF: %t\nnext_call: %s\n文件: %s\n版本: %s\n源字节范围: [%d,%d)\n行号范围: [%d,%d]\n源文件 EOF: %t\n\n%s", entry.ID, start, end, len(data), next, end >= len(data), nextCall, receipt.Path, receipt.Version, receipt.ByteStart, receipt.ByteEnd, first, last, receipt.ByteEnd >= receipt.TotalBytes, body)
	return out, receipt, content, nil
}

func (r *runState) selectedSourceEvidence(ctx context.Context, id string) (AnalysisEvidence, error) {
	entry, err := r.analysisLookup(ctx, false)(id)
	if err != nil {
		return entry, err
	}
	r.mu.Lock()
	saved, ok := ensureAnalysisState(&r.state).Evidence[id]
	r.mu.Unlock()
	if !ok || saved.ActionID != entry.ActionID || saved.ArtifactID != entry.ArtifactID {
		return entry, fmt.Errorf("证据没有已提交的成功读取记录")
	}
	origin, ok := r.sourceOrigin(entry.ArtifactID)
	if !ok {
		return entry, fmt.Errorf("证据没有可核对的原始材料")
	}
	if err := r.currentSourceOrigin(ctx, origin); err != nil {
		return entry, err
	}
	return entry, nil
}

func (r *runState) normalizedViewRequestV2(ctx context.Context, call schema.ToolCall) (reviewReadArgs, string, bool) {
	var out reviewReadArgs
	a, err := parseSourceViewArgsV2(call.Arguments)
	if err != nil {
		return out, "", false
	}
	if a.EvidenceID == "" { // 旧材料ID仍使用完全相同的原材料偏移。
		legacy, _ := json.Marshal(sourceViewArgs{ID: a.ID, Offset: a.Offset, Limit: a.Limit, Review: a.Review})
		copy := call
		copy.Arguments = legacy
		return r.normalizedArtifactRequest(ctx, copy)
	}
	entry, err := r.selectedSourceEvidence(ctx, a.EvidenceID)
	if err != nil {
		return out, "", false
	}
	data, err := r.readEvidenceContent(entry)
	if err != nil || !utf8.Valid(data) {
		return out, "", false
	}
	start := min(a.Offset, len(data))
	if start < len(data) && !utf8.RuneStart(data[start]) {
		return out, "", false
	}
	end := min(start+a.Limit, len(data))
	for end > start && end < len(data) && !utf8.RuneStart(data[end]) {
		end--
	}
	line := entry.Receipt.FirstLine + bytes.Count(data[:start], []byte{'\n'})
	starts := start > 0 && data[start-1] == '\n' || start == 0 && entry.Receipt.LineBoundary != nil && entry.Receipt.LineBoundary.StartsLine
	_, n := numberSourceView(data[start:end], line, !starts, a.Limit)
	if n == 0 {
		return out, "", false
	}
	offset, limit, numbered := entry.Receipt.ByteStart+int64(start), n, false
	out = reviewReadArgs{Path: entry.Receipt.Path, Offset: &offset, Limit: &limit, LineNumbers: &numbered, Review: a.Review}
	return out, fmt.Sprintf("%s:bytes:%d:%d", out.Path, offset, limit), true
}
