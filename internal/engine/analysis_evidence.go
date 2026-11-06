package engine

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// ValidateAnalysisCitation 只校验不可变的实际读取字节；权限和当前版本由 lookup 核对。
// 即使读取从行中开始，也不会把未交付的行前后部分当作已读内容。
func ValidateAnalysisCitation(ref AnalysisCitation, evidence AnalysisEvidence) (AnalysisCitation, error) {
	if evidence.ID == "" || ref.EvidenceID != evidence.ID || evidence.ActionID == "" || evidence.RunID == "" || evidence.ArtifactID == "" {
		return ref, errors.New("证据身份不匹配")
	}
	read := evidence.Receipt
	if read.ActionID != "" && read.ActionID != evidence.ActionID || read.AgentID != "" && read.AgentID != evidence.AgentID {
		return ref, errors.New("读取回执与证据动作归属不一致")
	}
	if !filepath.IsLocal(read.Path) || read.Version == "" || read.FirstLine < 1 || read.LastLine < read.FirstLine || read.ByteStart < 0 || read.ByteEnd < read.ByteStart || read.ByteEnd > read.TotalBytes {
		return ref, errors.New("读取回执缺少有效路径、版本或范围")
	}
	if int64(len(evidence.Source)) != read.ByteEnd-read.ByteStart || !utf8.Valid(evidence.Source) {
		return ref, errors.New("实际原文字节不完整或不是有效 UTF-8")
	}
	if ref.StartLine < read.FirstLine || ref.EndLine < ref.StartLine || ref.EndLine > read.LastLine || strings.TrimSpace(ref.Quote) == "" || !utf8.ValidString(ref.Quote) || len(ref.Quote) > 1024 {
		return ref, errors.New("引用行范围不在实际读取内，或缺少有效短原文")
	}
	quote := []byte(ref.Quote)
	match, matches := -1, 0
	for offset := 0; offset <= len(evidence.Source)-len(quote); {
		idx := bytes.Index(evidence.Source[offset:], quote)
		if idx < 0 {
			break
		}
		idx += offset
		first, last := analysisQuoteLines(evidence.Source, read.FirstLine, idx, idx+len(quote))
		if first >= ref.StartLine && last <= ref.EndLine {
			match, matches = idx, matches+1
		}
		offset = idx + 1
	}
	if matches == 0 {
		return ref, errors.New("所引行范围没有实际读取的完全一致原文；不能引用未读部分")
	}
	if matches != 1 {
		return ref, errors.New("原文在所引范围中出现多次，请缩小行范围或补充更明确的摘录")
	}
	first, last := analysisQuoteLines(evidence.Source, read.FirstLine, match, match+len(quote))
	return AnalysisCitation{
		EvidenceID: evidence.ID, StartLine: first, EndLine: last, Quote: ref.Quote,
		Path: read.Path, Version: read.Version,
		ByteStart: read.ByteStart + int64(match), ByteEnd: read.ByteStart + int64(match+len(quote)),
		ArtifactID: evidence.ArtifactID, ActionID: evidence.ActionID, Status: "evidence_valid",
	}, nil
}

func analysisQuoteLines(source []byte, firstLine, start, end int) (int, int) {
	first := firstLine + bytes.Count(source[:start], []byte{'\n'})
	last := first + bytes.Count(source[start:end], []byte{'\n'})
	if end > start && source[end-1] == '\n' {
		last--
	}
	return first, last
}

// ValidateAnalysisState 校验可选扩展的版本和内部身份，不从历史正文补造字段。
func ValidateAnalysisState(state *AnalysisState) error {
	if state == nil {
		return nil
	}
	if state.Version != analysisSchemaVersion {
		return fmt.Errorf("未知分析状态版本 %d", state.Version)
	}
	for id, evidence := range state.Evidence {
		if !analysisIDValid(id) || evidence.ID != id || evidence.ActionID == "" || evidence.RunID == "" || !filepath.IsLocal(evidence.Receipt.Path) || evidence.Receipt.Version == "" || evidence.Receipt.ByteStart < 0 || evidence.Receipt.ByteEnd < evidence.Receipt.ByteStart || evidence.Receipt.ByteEnd > evidence.Receipt.TotalBytes {
			return fmt.Errorf("分析证据 %q 的身份或范围无效", id)
		}
	}
	for agentID, report := range state.Reports {
		if err := checkAnalysisStructure(report); err != nil {
			return fmt.Errorf("Agent %q 的分析状态无效：%w", agentID, err)
		}
	}
	return nil
}
