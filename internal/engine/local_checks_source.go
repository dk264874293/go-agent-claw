package engine

import (
	"bytes"
	"context"
	"fmt"

	"github.com/dk264874293/go-agent-claw/internal/quality"
)

func validateLocalCheckSelector(sel localCheckSelector) error {
	if len(sel.FileRef) > 128 {
		return fmt.Errorf("file_ref最多128字节，须从当前目录复制")
	}
	if (sel.FileRef == "") == (len(sel.EvidenceIDs) == 0) || len(sel.EvidenceIDs) > 128 {
		return fmt.Errorf("提供file_ref或1至128个实际证据ID之一")
	}
	seen := map[string]bool{}
	for _, id := range sel.EvidenceIDs {
		if id == "" || len(id) > 128 || seen[id] {
			return fmt.Errorf("证据ID不能为空、重复或超过128字节")
		}
		seen[id] = true
	}
	line := sel.StartLine != nil || sel.EndLine != nil
	positions := sel.ByteStart != nil || sel.ByteEnd != nil
	if line == positions {
		return fmt.Errorf("成对指定start_line/end_line或byte_start/byte_end，不能混合；行号从1开始，字节从0开始")
	}
	if line && (sel.StartLine == nil || sel.EndLine == nil || *sel.StartLine < 1 || *sel.EndLine < *sel.StartLine) {
		return fmt.Errorf("start_line/end_line必须成对且1≤start_line≤end_line")
	}
	if positions && (sel.ByteStart == nil || sel.ByteEnd == nil || *sel.ByteStart < 0 || *sel.ByteEnd <= *sel.ByteStart || *sel.ByteEnd-*sel.ByteStart > quality.MaxJSONBytes) {
		return fmt.Errorf("byte_start/byte_end必须成对、非空且不超过64KiB")
	}
	return nil
}

func (r *runState) localCheckSourceLookup(ctx context.Context) quality.Lookup {
	return func(id string) (quality.Source, error) {
		entry, err := r.analysisLookup(ctx, true)(id)
		if err != nil {
			return quality.Source{}, err
		}
		content, err := r.readEvidenceContent(entry)
		if err != nil {
			return quality.Source{}, err
		}
		source := quality.Source{ID: entry.ID, Path: entry.Receipt.Path, Version: entry.Receipt.Version, ByteStart: entry.Receipt.ByteStart, ByteEnd: entry.Receipt.ByteEnd, TotalBytes: entry.Receipt.TotalBytes, FirstLine: entry.Receipt.FirstLine, LastLine: entry.Receipt.LastLine, Content: content}
		if boundary := entry.Receipt.LineBoundary; boundary != nil {
			source.Boundary = &quality.Boundary{StartsLine: boundary.StartsLine, EndsLine: boundary.EndsLine}
		}
		return source, nil
	}
}

// 行选择只依赖实际回执和原始字节。没有首尾行边界证明时拒绝猜测半行的剩余内容。
func resolveLocalCheckSelector(sel localCheckSelector, lookup quality.Lookup) (quality.Selector, error) {
	out := quality.Selector{EvidenceIDs: append([]string(nil), sel.EvidenceIDs...), RequireCompleteLine: sel.RequireCompleteLine}
	if sel.ByteStart != nil {
		out.ByteStart, out.ByteEnd = *sel.ByteStart, *sel.ByteEnd
		return out, nil
	}
	var start, end int64 = -1, -1
	for _, id := range sel.EvidenceIDs {
		source, err := lookup(id)
		if err != nil {
			return out, err
		}
		if source.FirstLine <= 0 || int64(len(source.Content)) != source.ByteEnd-source.ByteStart {
			return out, fmt.Errorf("证据%s缺少可验证的行边界信息", id)
		}
		line, begin := source.FirstLine, 0
		startsLine := source.ByteStart == 0 || source.Boundary != nil && source.Boundary.StartsLine
		for pos, ch := range source.Content {
			if ch != '\n' {
				continue
			}
			if line == *sel.StartLine && (begin > 0 || startsLine) {
				candidate := source.ByteStart + int64(begin)
				if start >= 0 && start != candidate {
					return out, fmt.Errorf("证据行号与实际字节起点不一致")
				}
				start = candidate
			}
			if line == *sel.EndLine {
				candidate := source.ByteStart + int64(pos) + 1
				if end >= 0 && end != candidate {
					return out, fmt.Errorf("证据行号与实际字节终点不一致")
				}
				end = candidate
			}
			line++
			begin = pos + 1
		}
		if begin < len(source.Content) {
			if line == *sel.StartLine && (begin > 0 || startsLine) {
				candidate := source.ByteStart + int64(begin)
				if start >= 0 && start != candidate {
					return out, fmt.Errorf("证据行号与实际字节起点不一致")
				}
				start = candidate
			}
			if line == *sel.EndLine && (source.ByteEnd == source.TotalBytes || source.Boundary != nil && source.Boundary.EndsLine) {
				if end >= 0 && end != source.ByteEnd {
					return out, fmt.Errorf("证据行号与实际字节终点不一致")
				}
				end = source.ByteEnd
			}
		}
	}
	if start < 0 || end <= start {
		return out, fmt.Errorf("已交付证据不能证明所选完整行；请使用实际续读位置补齐主体，不要猜偏移")
	}
	if end-start > quality.MaxJSONBytes {
		return out, fmt.Errorf("核对主体超过64KiB")
	}
	out.ByteStart, out.ByteEnd, out.RequireCompleteLine = start, end, true
	// 再由共享解析器检查同文件同版本、UTF-8、重叠一致性以及中间缺口。
	if _, err := quality.Resolve(out, lookup); err != nil {
		return out, err
	}
	return out, nil
}

func localCheckSourceFailure(id, kind string, err error) quality.Receipt {
	return quality.Receipt{CheckerVersion: quality.CheckerVersion, ID: id, FindingID: id, Kind: kind, Result: "not_run", PremiseResult: "not_run", Reason: err.Error(), Scope: "来源、主体或锚点未确认；没有核验结论"}
}

func (r *runState) runLocalCheckLegacy(ctx context.Context, id string, input localCheckInput) quality.Receipt {
	liveLookup := r.localCheckSourceLookup(ctx)
	cache := map[string]quality.Source{}
	lookup := func(id string) (quality.Source, error) {
		if source, ok := cache[id]; ok {
			return source, nil
		}
		source, err := liveLookup(id)
		if err == nil {
			cache[id] = source
		}
		return source, err
	}
	subject, err := resolveLocalCheckSelector(input.Subject, lookup)
	if err != nil {
		return localCheckSourceFailure(id, input.Kind, err)
	}
	check := quality.Check{ID: id, FindingID: id, Kind: input.Kind, Subject: subject, Expression: input.Expression, Bindings: map[string]quality.Binding{}, Cases: input.Cases, Assertions: input.Assertions, Premise: input.Premise}
	if check.Kind == "arithmetic" {
		check.Kind = "numeric"
	}
	for name, binding := range input.Bindings {
		converted := quality.Binding{Kind: binding.Kind, Value: binding.Value, Pointer: binding.Pointer}
		if binding.Source != nil {
			source, err := resolveLocalCheckSelector(*binding.Source, lookup)
			if err != nil {
				return localCheckSourceFailure(id, input.Kind, err)
			}
			converted.Source = &source
		}
		check.Bindings[name] = converted
	}
	var anchor *quality.SourceRef
	if input.Anchor != nil {
		anchorSelector, err := resolveLocalCheckSelector(input.Anchor.Subject, lookup)
		if err != nil {
			return localCheckSourceFailure(id, input.Kind, err)
		}
		resolved, err := quality.Resolve(anchorSelector, lookup)
		if err != nil {
			return localCheckSourceFailure(id, input.Kind, err)
		}
		resolvedSubject, err := quality.Resolve(subject, lookup)
		if err != nil {
			return localCheckSourceFailure(id, input.Kind, err)
		}
		if resolved.Path != resolvedSubject.Path || resolved.Version != resolvedSubject.Version || !bytes.Contains(resolved.Content, []byte(input.Anchor.Quote)) {
			return localCheckSourceFailure(id, input.Kind, fmt.Errorf("锚点不属于同文件同版本，或精确短原文未出现在实际已读范围"))
		}
		anchor = &resolved.SourceRef
	}
	receipt := quality.Run([]quality.Check{check}, lookup)[0]
	receipt.Kind = input.Kind
	if anchor != nil {
		receipt.Sources = append(receipt.Sources, *anchor)
	}
	// 只证明本次核对时仍有效的来源，不把调用中发生的变化当成通过。
	for id := range cache {
		if _, err := liveLookup(id); err != nil {
			return localCheckSourceFailure(check.ID, input.Kind, err)
		}
	}
	for _, assertion := range input.Assertions {
		if assertion.Op == "default" {
			receipt.PremiseResult = "not_run"
			receipt.Scope += "；default只证明调用方指定的缺失替代值，不证明原代码实际采用该默认行为"
			break
		}
	}
	return receipt
}
