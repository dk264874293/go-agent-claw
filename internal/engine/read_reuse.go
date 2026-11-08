package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

type readReuseRecord struct {
	Read             schema.ReadReceipt
	Key, Observation string
	Source           []byte
}
type readReuseRuntime struct {
	records map[string]readReuseRecord
	exposed map[string]map[string]bool
}
type reviewReadArgs struct {
	Path        string             `json:"path"`
	Offset      *int64             `json:"offset"`
	Limit       *int               `json:"limit"`
	StartLine   *int               `json:"start_line"`
	EndLine     *int               `json:"end_line"`
	LineNumbers *bool              `json:"line_numbers"`
	Review      *schema.ReadReview `json:"review"`
}

func readRequest(call schema.ToolCall, pages ...int) (reviewReadArgs, string, bool) {
	page := 8000
	if len(pages) > 0 {
		page = effectivePageBytes(pages[0])
	}
	var args reviewReadArgs
	if call.Name != "read_file" && call.Name != "read_lines" {
		return args, "", false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(call.Arguments, &fields) != nil || json.Unmarshal(call.Arguments, &args) != nil || args.Path == "" {
		return args, "", false
	}
	for k := range fields {
		switch k {
		case "path", "offset", "limit", "start_line", "end_line", "line_numbers", "review":
		default:
			return args, "", false
		}
	}
	if (args.StartLine == nil) != (args.EndLine == nil) || args.StartLine != nil && (args.Offset != nil || args.Limit != nil) {
		return args, "", false
	}
	path := filepath.ToSlash(filepath.Clean(args.Path))
	if !filepath.IsLocal(path) {
		return args, "", false
	}
	if args.StartLine != nil {
		if *args.StartLine < 1 || *args.EndLine < *args.StartLine {
			return args, "", false
		}
		return args, fmt.Sprintf("%s:lines:%d:%d", path, *args.StartLine, *args.EndLine), true
	}
	if call.Name == "read_lines" {
		return args, "", false
	}
	offset, limit := int64(0), page
	if args.Offset != nil {
		offset = *args.Offset
	}
	if args.Limit != nil {
		limit = *args.Limit
	}
	if offset < 0 || limit < 1 || limit > page {
		return args, "", false
	}
	return args, fmt.Sprintf("%s:bytes:%d:%d", path, offset, limit), true
}

func (r *runState) visibleTools(ctx context.Context, registry tools.Registry) []schema.ToolDefinition {
	defs := registry.VisibleTools(ctx)
	delegate := r.delegationVisible(ctx)
	r.mu.Lock()
	reportAvailable := false
	for _, report := range r.handoffs.reports {
		if report.ParentID == observe.Identity(ctx).AgentID && report.RunID == r.runID {
			reportAvailable = true
			break
		}
	}
	r.mu.Unlock()
	result := make([]schema.ToolDefinition, 0, len(defs))
	for _, def := range defs {
		if def.Name == "check_evidence" && !r.localChecksEnabled() {
			continue
		}
		if def.Name == "check_evidence" && r.readFeaturesV2() {
			def = localCheckDefinitionV2()
		}
		if def.Name == "read_evidence" && !(r.sourceViewsEnabled() || r.readFeaturesV2() && r.localChecksEnabled()) && observe.Identity(ctx).ParentAgentID != "" {
			continue
		}
		if def.Name == "read_evidence" && r.sourceViewsEnabled() {
			if r.readFeaturesV2() {
				def = sourceViewDefinitionV2(def)
			} else {
				def = sourceViewDefinition(def)
			}
		}
		if def.Name == "verify_findings" && !r.qualityEnabled() {
			continue
		}
		if r.qualityEnabled() {
			if r.state.Quality != nil && r.state.Quality.DraftID != "" && def.Name != "write_report" {
				continue
			}
			if def.Name == "write_report" {
				def = qualityPublicationDefinition()
			}
		}
		if def.Name == "write_report" && r.state.Spec.Report == nil {
			continue
		}
		if (def.Name != "spawn_subagent" || delegate) && (def.Name != "read_report" || reportAvailable) {
			result = append(result, r.withWorkNotesCarrier(def))
		}
	}
	return result
}

// 只把仍完整存在于成功请求中的原文标为可复用；被窗口裁掉的材料不能据此拒绝。
func (r *runState) markReadExposure(ctx context.Context, messages []schema.Message) {
	if !r.readTrackingEnabled() {
		return
	}
	agent := observe.Identity(ctx).AgentID
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.readReuse.exposed == nil {
		r.readReuse.exposed = make(map[string]map[string]bool)
	}
	visible := make(map[string]bool)
	for _, msg := range messages {
		if record, ok := r.readReuse.records[msg.ReadActionID]; ok && record.Read.AgentID == agent && msg.ToolCallID != "" && msg.Content == record.Observation {
			visible[msg.ReadActionID] = true
		}
	}
	r.readReuse.exposed[agent] = visible
	if r.analysisRuntime.notesExposed == nil {
		r.analysisRuntime.notesExposed = map[string]map[string]bool{}
	}
	r.analysisRuntime.notesExposed[agent] = map[string]bool{}
	for _, id := range r.analysisRuntime.projectedNotes[agent] {
		r.analysisRuntime.notesExposed[agent][id] = true
	}
}

func (r *runState) rememberReadReuseLocked(call schema.ToolCall, result schema.ToolResult) {
	if !r.readTrackingEnabled() || result.Status != schema.ToolSucceeded || result.Read == nil {
		return
	}
	_, key, ok := readRequest(call, r.effectiveReadPageBytes())
	if call.Name == "read_evidence" && r.sourceViewsEnabled() && result.Read.SourceView != nil {
		key = fmt.Sprintf("%s:bytes:%d:%d", result.Read.Path, result.Read.ByteStart, result.Read.ByteEnd-result.Read.ByteStart)
		ok = true
	}
	if !ok {
		return
	}
	if r.readReuse.records == nil {
		r.readReuse.records = make(map[string]readReuseRecord)
	}
	r.readReuse.records[result.Read.ActionID] = readReuseRecord{Read: *result.Read, Key: key, Observation: observation(result), Source: append([]byte(nil), result.ReadContent...)}
}

// 使用真正交付给模型的观察做曝光匹配；笔记回执不应改变原文的复用资格。
func (r *runState) rememberReadObservation(ctx context.Context, result schema.ToolResult, content string) {
	if result.Read == nil || result.Status != schema.ToolSucceeded {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	id := result.Read.ActionID
	record, ok := r.readReuse.records[id]
	if ok && record.Read.ActionID == id && record.Read.AgentID == observe.Identity(ctx).AgentID {
		record.Observation = content
		r.readReuse.records[id] = record
	}
}

func (r *runState) readReusableLocked(agent, id string) bool {
	if r.readReuse.exposed[agent][id] {
		return true
	}
	if analysisStateOf(r.state) != nil {
		record := r.readReuse.records[id]
		if containsString(analysisStateOf(r.state).Processed[agent], record.Read.EvidenceID) && (!r.readMemoryEnabled() || r.analysisRuntime.notesExposed[agent][record.Read.EvidenceID]) {
			return true
		}
	}
	return false
}

// 无目的重复只返回位置，不返回缓存原文，也不制造新的读取回执。
func (r *runState) rejectDuplicateRead(ctx context.Context, call schema.ToolCall) *schema.ToolResult {
	if !r.readTrackingEnabled() {
		return nil
	}
	args, key, ok := r.normalizedReadRequest(ctx, call)
	if !ok {
		return nil
	}
	agent := observe.Identity(ctx).AgentID
	r.mu.Lock()
	records := make(map[string]readReuseRecord)
	owned := make(map[string]readReuseRecord)
	for id, entry := range r.readReuse.records {
		if entry.Read.AgentID == agent {
			owned[id] = entry
			if r.readReusableLocked(agent, id) {
				records[id] = entry
			}
		}
	}
	r.mu.Unlock()
	version, err := tools.FileVersion(ctx, filepath.ToSlash(filepath.Clean(args.Path)))
	if err != nil {
		return nil
	}
	if args.Review != nil {
		target := args.Review.TargetID
		valid := strings.TrimSpace(target) != "" && strings.TrimSpace(args.Review.Question) != ""
		if valid {
			valid = false
			for id, record := range owned {
				if (target == id || target == record.Read.EvidenceID) && record.Read.Version == version && record.Read.Path == filepath.ToSlash(filepath.Clean(args.Path)) {
					valid = true
				}
			}
			// 结构化发现只能关联本 Agent 已持有的有效引用。
			r.mu.Lock()
			if report := analysisStateOf(r.state); report != nil {
				for _, f := range report.Reports[agent].Findings {
					if f.ID == target {
						for _, ref := range f.Evidence {
							if ref.Path == filepath.ToSlash(filepath.Clean(args.Path)) && ref.Version == version {
								valid = true
							}
						}
					}
				}
			}
			r.mu.Unlock()
			valid = valid || r.validHandoffReview(ctx, target, filepath.ToSlash(filepath.Clean(args.Path)), version)
		}
		if valid {
			return nil
		}
		result := schema.NewToolResult(call.ID, "复核目标无效：review.target_id 必须关联当前可复用的读取动作、证据或发现，question 必须说明待核对的问题。材料已不在上下文时请作为新的普通读取。", schema.ToolRejected, schema.EffectNone, "复核目标无效", nil)
		result.ErrorCode = "invalid_review_target"
		return &result
	}
	for id, record := range records {
		knownSame := false
		for _, source := range owned {
			if source.Read.Path == record.Read.Path && source.Read.Version == version {
				if start, end, known := knownReadInterval(args, source, r.effectiveReadPageBytes()); known && start == record.Read.ByteStart && end == record.Read.ByteEnd {
					knownSame = true
					break
				}
			}
		}
		if (record.Key == key || knownSame) && record.Read.Version == version && record.Read.Path == filepath.ToSlash(filepath.Clean(args.Path)) {
			text := fmt.Sprintf("重复读取未执行：同一版本的原文已完整提供且仍可复用。原动作 %s，文件 %s，版本 %s，字节 [%d,%d)。如需核对具体问题，请增加 review:{\"target_id\":\"%s\",\"question\":\"待核对的具体问题\"}。尝试已计入工具预算。", id, record.Read.Path, version, record.Read.ByteStart, record.Read.ByteEnd, id)
			result := schema.NewToolResult(call.ID, text, schema.ToolRejected, schema.EffectNone, "重复读取", nil)
			result.ErrorCode = "duplicate_read"
			retry := false
			result.Retryable = &retry
			return &result
		}
	}
	return nil
}

// 仅根据已经读取的源码推算确定区间，不做新文件IO，也不把缓存正文当作工具结果。
// 编号会挤占8KiB显示预算；无法证明整段可显示时保守放行，不猜测截断位置。
func knownReadInterval(args reviewReadArgs, record readReuseRecord, pages ...int) (int64, int64, bool) {
	page := 8000
	if len(pages) > 0 {
		page = effectivePageBytes(pages[0])
	}
	read, source := record.Read, record.Source
	if !utf8.Valid(source) || int64(len(source)) != read.ByteEnd-read.ByteStart {
		return 0, 0, false
	}
	start, end := int64(0), int64(0)
	numbered := false
	if args.StartLine != nil {
		numbered = true
		if *args.StartLine < read.FirstLine || *args.EndLine > read.LastLine || *args.StartLine == read.FirstLine && read.ByteStart != 0 {
			return 0, 0, false
		}
		line, pos := read.FirstLine, 0
		begin, finish := -1, -1
		for pos < len(source) {
			if line == *args.StartLine {
				begin = pos
			}
			n := bytes.IndexByte(source[pos:], '\n')
			if n < 0 {
				if line == *args.EndLine && read.ByteEnd == read.TotalBytes {
					finish = len(source)
				}
				break
			}
			next := pos + n + 1
			if line == *args.EndLine {
				finish = next
				break
			}
			line++
			pos = next
		}
		if begin < 0 || finish < begin {
			return 0, 0, false
		}
		start, end = read.ByteStart+int64(begin), read.ByteStart+int64(finish)
	} else {
		limit := int64(page)
		if args.Offset != nil {
			start = *args.Offset
		}
		if args.Limit != nil {
			limit = int64(*args.Limit)
		}
		if start > read.TotalBytes {
			return 0, 0, false
		}
		end = start + min(limit, read.TotalBytes-start)
		if start < read.ByteStart || end > read.ByteEnd {
			return 0, 0, false
		}
		localStart, localEnd := int(start-read.ByteStart), int(end-read.ByteStart)
		if localStart < len(source) && !utf8.RuneStart(source[localStart]) {
			return 0, 0, false
		}
		for localEnd > localStart && localEnd < len(source) && !utf8.RuneStart(source[localEnd]) {
			localEnd--
		}
		end = read.ByteStart + int64(localEnd)
	}
	if args.LineNumbers != nil {
		numbered = *args.LineNumbers
	}
	if end-start > int64(page) {
		return 0, 0, false
	}
	if numbered {
		body := source[start-read.ByteStart : end-read.ByteStart]
		lines := bytes.Count(body, []byte{'\n'}) + 1
		if len(body)+lines*(len(fmt.Sprint(read.LastLine))+len("（续行） | ")) > page {
			return 0, 0, false
		}
	}
	return start, end, true
}

func (r *runState) annotateReadOverlap(ctx context.Context, result *schema.ToolResult) {
	if !r.readTrackingEnabled() || result.Read == nil {
		return
	}
	read := result.Read
	var ranges []ByteInterval
	for _, old := range r.catalogSnapshot().receipts {
		if old.Path == read.Path && old.Version == read.Version {
			ranges = append(ranges, ByteInterval{max(old.ByteStart, read.ByteStart), min(old.ByteEnd, read.ByteEnd)})
		}
	}
	sortIntervals(ranges)
	cursor, overlap := read.ByteStart, int64(0)
	for _, x := range ranges {
		if x.End > max(cursor, x.Start) {
			overlap += x.End - max(cursor, x.Start)
			cursor = x.End
		}
	}
	newBytes := max(int64(0), read.ByteEnd-read.ByteStart-overlap)
	result.Output = fmt.Sprintf("读取动作: %s\n本次新覆盖: %d 字节；与已有读取重叠: %d 字节（复核可能仍有必要）。\n", read.ActionID, newBytes, overlap) + result.Output
}

func sortIntervals(ranges []ByteInterval) {
	for i := 1; i < len(ranges); i++ {
		for j := i; j > 0 && ranges[j].Start < ranges[j-1].Start; j-- {
			ranges[j], ranges[j-1] = ranges[j-1], ranges[j]
		}
	}
}
