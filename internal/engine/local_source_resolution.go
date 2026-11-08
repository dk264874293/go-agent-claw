package engine

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"unicode/utf8"

	"github.com/dk264874293/go-agent-claw/internal/quality"
)

type LocalSourceAction struct {
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments"`
}
type LocalSourceResolution struct {
	FileRef               string              `json:"file_ref,omitempty"`
	Path                  string              `json:"path,omitempty"`
	Version               string              `json:"version,omitempty"`
	ReasonCode            string              `json:"reason_code,omitempty"`
	Requested             localCheckSelector  `json:"requested_range"`
	ResolvedEvidenceIDs   []string            `json:"resolved_evidence_ids,omitempty"`
	AvailableRanges       []ByteInterval      `json:"available_ranges,omitempty"`
	MissingByteRanges     []ByteInterval      `json:"missing_byte_ranges,omitempty"`
	MissingLineBoundaries bool                `json:"missing_line_boundaries,omitempty"`
	NextActions           []LocalSourceAction `json:"next_actions,omitempty"`
}
type localSourceError struct {
	code, text string
	resolution *LocalSourceResolution
}

func (e *localSourceError) Error() string { return e.text }
func sourceResolutionError(code, text string, res *LocalSourceResolution) error {
	return &localSourceError{code, text, res}
}

// 句柄模式只组合同文件已交付片段，不能从文件系统补充正文或让旧E扩大范围。
func (r *runState) resolveLocalSelector(ctx context.Context, sel localCheckSelector) (localCheckSelector, *LocalSourceResolution, error) {
	res := &LocalSourceResolution{FileRef: sel.FileRef, Requested: sel}
	if sel.FileRef == "" {
		parts := []quality.Source{}
		for _, id := range sel.EvidenceIDs {
			part, err := r.localCheckSourceLookup(ctx)(id)
			if err != nil {
				res.ReasonCode = "source_unavailable"
				return sel, res, sourceResolutionError(res.ReasonCode, "指定证据当前不可用", res)
			}
			parts = append(parts, part)
		}
		if code, reason := preflightLocalParts(sel, parts); code != "" {
			res.ReasonCode = code
			return sel, res, sourceResolutionError(code, reason, res)
		}
		return sel, res, nil
	}
	ref, err := r.resolveReadFileRef(ctx, sel.FileRef)
	if err != nil {
		res.ReasonCode = "source_ref_stale"
		if e, ok := err.(*localSourceError); ok {
			res.ReasonCode = e.code
		}
		return sel, res, err
	}
	res.Path, res.Version = ref.Path, ref.Version
	anchor, anchorErr := r.analysisLookup(ctx, false)(ref.AnchorEvidenceID)
	if anchorErr != nil {
		res.ReasonCode = "source_ref_stale"
		return sel, res, sourceResolutionError(res.ReasonCode, "文件句柄原始读取已失效", res)
	}
	if sel.ByteEnd != nil && (*sel.ByteEnd > anchor.Receipt.TotalBytes || *sel.ByteStart > anchor.Receipt.TotalBytes) {
		res.ReasonCode = "source_range_out_of_bounds"
		return sel, res, sourceResolutionError(res.ReasonCode, "所选字节超过已确认源文件EOF，没有可补读页面", res)
	}

	r.mu.Lock()
	ids := make([]string, 0, len(r.analysisRuntime.sources))
	for id, e := range r.analysisRuntime.sources {
		if e.RunID == r.runID && e.Receipt.Path == ref.Path && e.Receipt.Version == ref.Version {
			ids = append(ids, id)
		}
	}
	r.mu.Unlock()
	sort.Strings(ids)
	var eligible []string
	var parts []quality.Source
	for _, id := range ids {
		e, lookupErr := r.analysisLookup(ctx, true)(id)
		if lookupErr != nil {
			continue
		}
		part, sourceErr := r.localCheckSourceLookup(ctx)(id)
		if sourceErr != nil {
			continue
		}
		parts = append(parts, part)
		eligible = append(eligible, id)
		res.AvailableRanges = append(res.AvailableRanges, ByteInterval{Start: e.Receipt.ByteStart, End: e.Receipt.ByteEnd})
	}
	sortIntervals(res.AvailableRanges)
	// 工具预算可能调高，输入仍遵守既有解析器最多128段的限制。
	if len(eligible) > 128 {
		res.ReasonCode = "source_resolution_limit"
		return sel, res, sourceResolutionError(res.ReasonCode, "主体候选超过128段，请改为明确证据子集", res)
	}
	if code, reason := preflightLocalParts(sel, parts); code != "" {
		res.ReasonCode = code
		return sel, res, sourceResolutionError(code, reason, res)
	}
	if len(eligible) == 0 {
		res.ReasonCode = "source_not_delivered"
		return sel, res, sourceResolutionError(res.ReasonCode, "当前文件没有完整进入成功模型请求的可用原文", res)
	}
	out := sel
	out.FileRef = ""
	out.EvidenceIDs = eligible
	resolved, resolveErr := resolveLocalCheckSelector(out, r.localCheckSourceLookup(ctx))
	if resolveErr != nil {
		res.ReasonCode = "source_incomplete"
		if sel.StartLine != nil && resolved.ByteEnd <= resolved.ByteStart {
			res.MissingLineBoundaries = true
			res.ReasonCode = "source_line_boundary_unknown"
			res.NextActions = []LocalSourceAction{{Tool: "read_lines", Arguments: map[string]any{"path": ref.Path, "start_line": *sel.StartLine, "end_line": *sel.EndLine, "review": map[string]any{"target_id": ref.AnchorEvidenceID, "question": "补齐局部核对主体及完整行边界"}}}}
		} else {
			rangeStart, rangeEnd := resolved.ByteStart, resolved.ByteEnd
			if sel.ByteStart != nil {
				rangeStart, rangeEnd = *sel.ByteStart, *sel.ByteEnd
			}
			cursor := rangeStart
			for _, x := range res.AvailableRanges {
				if x.End <= cursor {
					continue
				}
				if x.Start > cursor {
					res.MissingByteRanges = append(res.MissingByteRanges, ByteInterval{Start: cursor, End: min(x.Start, rangeEnd)})
				}
				cursor = max(cursor, x.End)
				if cursor >= rangeEnd {
					break
				}
			}
			if cursor < rangeEnd {
				res.MissingByteRanges = append(res.MissingByteRanges, ByteInterval{Start: cursor, End: rangeEnd})
			}
			for _, gap := range res.MissingByteRanges {
				if gap.End > gap.Start {
					res.NextActions = append(res.NextActions, LocalSourceAction{Tool: "read_file", Arguments: map[string]any{"path": ref.Path, "offset": gap.Start, "limit": min(gap.End-gap.Start, int64(r.effectiveReadPageBytes())), "line_numbers": true}})
				}
			}
		}
		return out, res, sourceResolutionError(res.ReasonCode, resolveErr.Error(), res)
	}
	// 固定实际字节范围，既有检查核心继续验证完整主体、UTF-8和重叠一致性。
	out.ByteStart, out.ByteEnd = &resolved.ByteStart, &resolved.ByteEnd
	out.StartLine, out.EndLine = nil, nil
	out.RequireCompleteLine = resolved.RequireCompleteLine
	res.ResolvedEvidenceIDs = append([]string(nil), eligible...)
	return out, res, nil
}

func (r *runState) runLocalCheckResolved(ctx context.Context, id string, input localCheckInput) (quality.Receipt, *LocalSourceResolution) {
	if !r.readFeaturesV2() {
		return r.runLocalCheckLegacy(ctx, id, input), nil
	}
	converted, res, err := r.resolveLocalSelector(ctx, input.Subject)
	if err != nil {
		return localCheckSourceFailure(id, input.Kind, err), res
	}
	input.Subject = converted
	copyBindings := make(map[string]localCheckBinding, len(input.Bindings))
	for name, b := range input.Bindings {
		if b.Source != nil {
			v, x, e := r.resolveLocalSelector(ctx, *b.Source)
			if e != nil {
				return localCheckSourceFailure(id, input.Kind, fmt.Errorf("绑定%s：%w", name, e)), x
			}
			b.Source = &v
		}
		copyBindings[name] = b
	}
	input.Bindings = copyBindings
	if input.Anchor != nil {
		v, x, e := r.resolveLocalSelector(ctx, input.Anchor.Subject)
		if e != nil {
			return localCheckSourceFailure(id, input.Kind, e), x
		}
		a := *input.Anchor
		a.Subject = v
		input.Anchor = &a
	}
	receipt := r.runLocalCheckLegacy(ctx, id, input)
	if receipt.Result == "not_run" && res.ReasonCode == "" {
		res.ReasonCode = "source_or_premise_unavailable"
		// 兼容显式ID只检验原集合；可提示切换句柄，但绝不透明补入其他片段。
		if res.FileRef == "" && len(input.Subject.EvidenceIDs) > 0 && len(receipt.Sources) == 0 {
			var path, version, fid string
			same := true
			for _, eid := range input.Subject.EvidenceIDs {
				e, err := r.analysisLookup(ctx, true)(eid)
				if err != nil {
					same = false
					break
				}
				if path != "" && (path != e.Receipt.Path || version != e.Receipt.Version) {
					same = false
					res.ReasonCode = "source_version_mismatch"
					break
				}
				path, version = e.Receipt.Path, e.Receipt.Version
				if fid == "" {
					fid = r.fileRefForEvidence(ctx, e)
				}
				res.AvailableRanges = append(res.AvailableRanges, ByteInterval{Start: e.Receipt.ByteStart, End: e.Receipt.ByteEnd})
			}
			if same && fid != "" {
				res.FileRef, res.Path, res.Version = fid, path, version
				next := input
				next.Subject.FileRef = fid
				next.Subject.EvidenceIDs = nil
				res.NextActions = []LocalSourceAction{{Tool: "check_evidence", Arguments: map[string]any{"checks": []localCheckInput{next}}}}
			}
		}
	}
	return receipt, res
}

func (r *runState) runLocalCheck(ctx context.Context, id string, input localCheckInput) quality.Receipt {
	receipt, _ := r.runLocalCheckResolved(ctx, id, input)
	return receipt
}

// 明确检查结构化元数据与实际字节，错误分类不解析自由文本错误消息。
func preflightLocalParts(sel localCheckSelector, parts []quality.Source) (string, string) {
	if len(parts) == 0 {
		return "", ""
	}
	total := parts[0].TotalBytes
	if sel.ByteEnd != nil && (*sel.ByteEnd > total || *sel.ByteStart > total) {
		return "source_range_out_of_bounds", "所选字节范围超出已确认源文件总长度；不能对EOF外补读"
	}
	eofLine := 0
	eofKnown := false
	for _, part := range parts {
		if part.Path != parts[0].Path || part.Version != parts[0].Version {
			return "source_version_mismatch", "指定证据不属于同一文件和版本"
		}
		if part.ByteStart < 0 || part.ByteEnd < part.ByteStart || int64(len(part.Content)) != part.ByteEnd-part.ByteStart || part.TotalBytes < part.ByteEnd {
			return "source_metadata_conflict", "来源区间和原始字节长度不一致"
		}
	}
	for i, a := range parts {
		if a.Path != parts[0].Path || a.Version != parts[0].Version {
			return "source_version_mismatch", "指定证据不属于同一文件和版本"
		}
		if a.TotalBytes != total {
			return "source_metadata_conflict", "同版本证据的源文件长度不一致"
		}
		if !utf8.Valid(a.Content) {
			return "source_encoding_unsupported", "来源不是完整UTF-8文本"
		}
		if a.ByteEnd == total && a.Boundary != nil && a.Boundary.EndsLine {
			eofKnown = true
			eofLine = max(eofLine, a.LastLine)
		}
		for _, b := range parts[i+1:] {
			start, end := max(a.ByteStart, b.ByteStart), min(a.ByteEnd, b.ByteEnd)
			if start < end {
				if !bytes.Equal(a.Content[start-a.ByteStart:end-a.ByteStart], b.Content[start-b.ByteStart:end-b.ByteStart]) {
					return "source_overlap_conflict", "同版本来源的重叠字节不一致，不能拼接为证明"
				}
				if a.FirstLine > 0 && b.FirstLine > 0 && a.FirstLine+bytes.Count(a.Content[:start-a.ByteStart], []byte{'\n'}) != b.FirstLine+bytes.Count(b.Content[:start-b.ByteStart], []byte{'\n'}) {
					return "source_line_metadata_conflict", "重叠来源的绝对行号不一致，不能猜测主体边界"
				}
			}
		}
	}
	if sel.EndLine != nil && eofKnown && *sel.EndLine > eofLine {
		return "source_range_out_of_bounds", "所选行范围超出已读取并确认的源文件EOF；没有可补读页面"
	}
	return "", ""
}
