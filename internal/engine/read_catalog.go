package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

const readContextLimit = 16 * 1024
const readContextEntries = 128

// readCatalog 只服务于本次运行；完整回执保存在 action_result 中，恢复时不重建缓存。
// 所有访问受 runState.mu 保护。
type readCatalog struct {
	receipts    []schema.ReadReceipt
	reports     []catalogReport
	invalidated []catalogInvalidation
}

type catalogInvalidation struct {
	Path    string `json:"path"`
	Version string `json:"version"`
}

type catalogReport struct {
	parentID string
	report   tools.SubtaskReport
	reads    []schema.ReadReceipt
}

// noteStorageError 保留子工具包装前的关键证据存储错误，交由根任务按既有规则停止。
func (r *runState) noteStorageError(err error) error {
	if errors.Is(err, errStorage) {
		r.mu.Lock()
		r.fatalStorage = errors.Join(r.fatalStorage, err)
		r.mu.Unlock()
	}
	return err
}

func (r *runState) storageError() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.fatalStorage
}

func bindRead(ctx context.Context, actionID string, result *schema.ToolResult) {
	if result.Status != schema.ToolSucceeded || result.Read == nil {
		result.Read = nil
		return
	}
	copy := *result.Read
	copy.AgentID, copy.ActionID = observe.Identity(ctx).AgentID, actionID
	result.Read = &copy
}

// rememberReadLocked 与动作结果在同一次持久事务内更新，调用方必须已持有 r.mu。
func (r *runState) rememberReadLocked(result schema.ToolResult) {
	if result.Status == schema.ToolSucceeded && result.Read != nil {
		r.reads.receipts = append(r.reads.receipts, *result.Read)
		invalid := r.reads.invalidated[:0]
		for _, item := range r.reads.invalidated {
			if item.Path != result.Read.Path || item.Version != result.Read.Version {
				invalid = append(invalid, item)
			}
		}
		r.reads.invalidated = invalid
	}
}

func (r *runState) invalidateReadsLocked(path string, clearAll bool) {
	if clearAll {
		for _, read := range r.reads.receipts {
			r.addReadInvalidationLocked(read.Path, read.Version)
		}
		r.reads.receipts, r.reads.reports = nil, nil
		return
	}
	path = filepath.Clean(path)
	r.removeReadVersionLocked(path, "")
}

func (r *runState) removeReadVersionLocked(path, version string) {
	matches := func(read schema.ReadReceipt) bool {
		return read.Path == path && (version == "" || read.Version == version)
	}
	reads := r.reads.receipts[:0]
	for _, read := range r.reads.receipts {
		if !matches(read) {
			reads = append(reads, read)
		} else {
			r.addReadInvalidationLocked(read.Path, read.Version)
		}
	}
	r.reads.receipts = reads
	reports := r.reads.reports[:0]
	for _, report := range r.reads.reports {
		if report.report.Handoff != nil {
			reports = append(reports, report)
			continue
		}
		stale := false
		for _, read := range report.reads {
			stale = stale || matches(read)
		}
		if !stale {
			reports = append(reports, report)
		}
	}
	r.reads.reports = reports
}

func (r *runState) addReadInvalidationLocked(path, version string) {
	for _, item := range r.reads.invalidated {
		if item.Path == path && item.Version == version {
			return
		}
	}
	r.reads.invalidated = append(r.reads.invalidated, catalogInvalidation{path, version})
	if len(r.reads.invalidated) > readContextEntries {
		r.reads.invalidated = r.reads.invalidated[len(r.reads.invalidated)-readContextEntries:]
	}
}

func (r *runState) catalogSnapshot() readCatalog {
	r.mu.Lock()
	defer r.mu.Unlock()
	return readCatalog{
		receipts:    append([]schema.ReadReceipt(nil), r.reads.receipts...),
		reports:     append([]catalogReport(nil), r.reads.reports...),
		invalidated: append([]catalogInvalidation(nil), r.reads.invalidated...),
	}
}

func (r *runState) agentReads(agentID string) []schema.ReadReceipt {
	var result []schema.ReadReceipt
	for _, read := range r.catalogSnapshot().receipts {
		if read.AgentID == agentID {
			result = append(result, read)
		}
	}
	return result
}

func (r *runState) rememberSubreport(parentID string, report tools.SubtaskReport, reads []schema.ReadReceipt) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reads.reports = append(r.reads.reports, catalogReport{parentID, report, append([]schema.ReadReceipt(nil), reads...)})
}

type catalogRange struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
	First int   `json:"first_line"`
	Last  int   `json:"last_line"`
}

type catalogCoverage struct {
	Path    string         `json:"path"`
	Version string         `json:"version"`
	Total   int64          `json:"total_bytes"`
	Ranges  []catalogRange `json:"ranges"`
}

// mergeReadCoverage 按最近读取顺序列出文件，逐个保留实际连续区间，不跨缺口合并。
func mergeReadCoverage(reads []schema.ReadReceipt) []catalogCoverage {
	var result []catalogCoverage
	indices := make(map[string]int)
	for i := len(reads) - 1; i >= 0; i-- {
		read := reads[i]
		key := read.Path + "\x00" + read.Version
		idx, ok := indices[key]
		if !ok {
			idx = len(result)
			indices[key] = idx
			result = append(result, catalogCoverage{Path: read.Path, Version: read.Version, Total: read.TotalBytes})
		}
		result[idx].Ranges = append(result[idx].Ranges, catalogRange{read.ByteStart, read.ByteEnd, read.FirstLine, read.LastLine})
	}
	for i := range result {
		ranges := result[i].Ranges
		sort.Slice(ranges, func(i, j int) bool { return ranges[i].Start < ranges[j].Start })
		merged := ranges[:0]
		for _, next := range ranges {
			if len(merged) > 0 && merged[len(merged)-1].End >= next.Start {
				last := &merged[len(merged)-1]
				last.End, last.Last = max(last.End, next.End), max(last.Last, next.Last)
			} else {
				merged = append(merged, next)
			}
		}
		result[i].Ranges = merged
	}
	return result
}

// readContext 复核当前权限和版本后，仅向父任务注入子报告。子任务只得到覆盖目录。
// 返回的块只用于本次请求，不能追加到会话而在后续请求中累积。
func (r *runState) readContext(ctx context.Context) string {
	snapshot := r.catalogSnapshot()
	scope, identity := tools.ScopeFromContext(ctx), observe.Identity(ctx)
	versions := make(map[string]string)
	checked := make(map[string]bool)
	valid := func(read schema.ReadReceipt) bool {
		if !filepath.IsLocal(read.Path) || !insideParentPaths(read.Path, scope.AllowedPaths) {
			return false
		}
		if !checked[read.Path] {
			versions[read.Path], _ = tools.FileVersion(ctx, read.Path)
			checked[read.Path] = true
		}
		if read.Version == "" || versions[read.Path] != read.Version {
			r.mu.Lock()
			r.removeReadVersionLocked(read.Path, read.Version)
			r.mu.Unlock()
			return false
		}
		return true
	}
	var reads []schema.ReadReceipt
	for _, read := range snapshot.receipts {
		if valid(read) {
			reads = append(reads, read)
		}
	}
	var reports []tools.SubtaskReport
	for _, item := range snapshot.reports {
		if item.parentID != identity.AgentID {
			continue
		}
		if item.report.Handoff != nil {
			reports = append(reports, r.refreshHandoff(ctx, item.report))
			continue
		}
		ok := len(item.reads) > 0
		for _, read := range item.reads {
			ok = valid(read) && ok
		}
		if ok {
			reports = append(reports, item.report)
		}
	}
	var invalid []catalogInvalidation
	for _, item := range r.catalogSnapshot().invalidated {
		if insideParentPaths(item.Path, scope.AllowedPaths) {
			invalid = append(invalid, item)
		}
	}
	return formatReadContext(reads, reports, true, invalid...)
}

// recordedReadSummary 仅复述动作当时的记录；取消或期限结束时不做新的文件读取。
func (r *runState) recordedReadSummary() string {
	snapshot := r.catalogSnapshot()
	var reports []tools.SubtaskReport
	for _, item := range snapshot.reports {
		reports = append(reports, item.report)
	}
	return formatReadContext(snapshot.receipts, reports, false, snapshot.invalidated...)
}

func formatReadContext(reads []schema.ReadReceipt, reports []tools.SubtaskReport, current bool, invalid ...catalogInvalidation) string {
	if !current && len(reads) == 0 && len(reports) == 0 && len(invalid) == 0 {
		return ""
	}
	var out strings.Builder
	if current {
		out.WriteString("\n\n历史工具结果及子报告只说明动作当时的观察，当前可复用证据以本次复核目录为准，不能沿用已失效的历史结论。\n")
		if len(reads) == 0 && len(reports) == 0 {
			out.WriteString("当前没有有效的读取覆盖或可复用子报告，需要证据时应重新读取。\n")
		}
		out.WriteString("\n\n本次运行的读取覆盖与子报告（已复核文件版本；结构和引用校验不等于结论验收）：\n")
	} else {
		out.WriteString("动作当时记录的读取覆盖与子报告（未重新核对当前版本）：\n")
	}
	appendJSON := func(label string, value any) bool {
		data, _ := json.Marshal(value)
		if out.Len()+len(label)+len(data)+1 > readContextLimit-220 {
			return false
		}
		out.WriteString(label)
		out.Write(data)
		out.WriteByte('\n')
		return true
	}
	count := 0
	for i := len(invalid) - 1; i >= 0 && count < readContextEntries; i-- {
		if appendJSON("已失效，历史读取和关联摘要不得用作当前结论依据：", invalid[i]) {
			count++
		}
	}
	for i := len(reports) - 1; i >= 0 && count < readContextEntries; i-- {
		report := reports[i]
		if report.Handoff != nil {
			// 完整短发现优先于旧格式长篇前缀；目录超限按整项省略并保留报告入口。
			data, err := tools.EncodeSubtaskHandoff(report)
			if err == nil {
				var wire any
				_ = json.Unmarshal(data, &wire)
				if appendJSON("结构化子报告目录（read_report可按发现取详情）：", wire) {
					count++
					continue
				}
			}
			index := map[string]any{"report_id": report.Handoff.ReportID, "artifact_id": report.Handoff.ArtifactID, "items_omitted": len(report.Handoff.Items) + report.Handoff.Omitted, "message": "本次上下文未展开；使用read_report读取目录或指定发现。"}
			if appendJSON("未展开子报告：", index) {
				count++
			}
			continue
		}
		report.Unverified = append([]string(nil), report.Unverified...)
		// 覆盖目录单独列出，避免完整回执挤掉可复用的发现摘要。
		report.Coverage = nil
		if len(report.Findings) > 2000 {
			report.Findings = truncateUTF8(report.Findings, 2000) + "（摘要节选，完整报告见动作结果或材料引用）"
		}
		if len(report.Evidence) > 16 {
			report.Evidence = report.Evidence[:16]
		}
		if len(report.Unverified) > 4 {
			report.Unverified = report.Unverified[:4]
		}
		for j := range report.Unverified {
			report.Unverified[j] = truncateUTF8(report.Unverified[j], 300)
		}
		if appendJSON("子报告摘要（可能节选）：", report) {
			count++
		}
	}
	for _, coverage := range mergeReadCoverage(reads) {
		if count >= readContextEntries || !appendJSON("读取覆盖：", coverage) {
			break
		}
		count++
	}
	out.WriteString("优先复用有效子报告，已知路径直接读取；覆盖目录不包含正文、不证明任务完成，必要时可复读。节选之外的回执见动作记录。\n")
	return out.String()
}

func readReferences(reads []schema.ReadReceipt) []tools.EvidenceRef {
	var refs []tools.EvidenceRef
	seen := make(map[string]bool)
	for _, read := range reads {
		if read.FirstLine < 1 || read.LastLine < read.FirstLine {
			continue
		}
		key := fmt.Sprintf("%s:%s:%d", read.Path, read.Version, read.FirstLine)
		if !seen[key] {
			refs = append(refs, tools.EvidenceRef{Path: read.Path, Version: read.Version, Line: read.FirstLine})
			seen[key] = true
		}
	}
	return refs
}
