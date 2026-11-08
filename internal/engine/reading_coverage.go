package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

type ReadingCoverage = schema.ReadingCoverage
type FileReadingCoverage = schema.FileReadingCoverage
type ByteInterval = schema.ByteInterval

func (r *runState) readonlyAnswer() bool {
	return r.state.Spec.Mode == tools.ModeReadOnly && r.state.Spec.Kind == TaskAnswer
}
func (r *runState) efficiencyEnabled() bool {
	return r.readonlyAnswer() && r.state.Spec.Delegation != ""
}

func (r *runState) discoverySnapshot() []schema.DiscoveryReceipt {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]schema.DiscoveryReceipt, 0, len(r.discoveries))
	for _, d := range r.discoveries {
		d.Files = append([]schema.DiscoveredFile(nil), d.Files...)
		result = append(result, d)
	}
	return result
}

func (r *runState) rememberDiscoveryLocked(ctx context.Context, result schema.ToolResult) {
	if result.Status != schema.ToolSucceeded || result.Discovery == nil {
		return
	}
	d := *result.Discovery
	d.Files = append([]schema.DiscoveredFile(nil), d.Files...)
	r.discoveries = append(r.discoveries, d)
	if r.discoveryOwners == nil {
		r.discoveryOwners = make(map[string]string)
	}
	r.discoveryOwners[d.ManifestID] = observe.Identity(ctx).AgentID
}

// 委派准入保留原规模策略；读取覆盖使用独立的聚合范围，不再受此选择影响。
func (r *runState) primaryDiscovery(ctx context.Context) *schema.DiscoveryReceipt {
	r.mu.Lock()
	defer r.mu.Unlock()
	agent := observe.Identity(ctx).AgentID
	var result *schema.DiscoveryReceipt
	for _, d := range r.discoveries {
		if r.discoveryOwners[d.ManifestID] != agent {
			continue
		}
		if result == nil || d.Path == result.Path && d.NameGlob == result.NameGlob {
			copy := d
			copy.Files = append([]schema.DiscoveredFile(nil), d.Files...)
			result = &copy
		}
	}
	return result
}

// latestDiscoveries 只接受当前 Agent 的清单；相同选择条件用最后一次显式刷新替换。
// 排序消除独立目录在同一批并发完成顺序对范围身份的影响。
func (r *runState) latestDiscoveries(ctx context.Context) []schema.DiscoveryReceipt {
	r.mu.Lock()
	defer r.mu.Unlock()
	owner := observe.Identity(ctx).AgentID
	latest := map[string]schema.DiscoveryReceipt{}
	for _, d := range r.discoveries {
		if r.discoveryOwners[d.ManifestID] != owner {
			continue
		}
		d.Files = append([]schema.DiscoveredFile(nil), d.Files...)
		sort.Slice(d.Files, func(i, j int) bool { return d.Files[i].Path < d.Files[j].Path })
		latest[d.Path+"\x00"+d.NameGlob] = d
	}
	var keys []string
	for key := range latest {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var result []schema.DiscoveryReceipt
	for _, key := range keys {
		result = append(result, latest[key])
	}
	return result
}

// 输入与产物分开：创建回执和显式报告目标不自动扩充资料集合。
func (r *runState) coverageOutputs() map[string]bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	outputs := map[string]bool{}
	if r.state.Spec.Report != nil {
		outputs[r.state.Spec.Report.TargetPath] = true
	}
	if r.state.EffectLedger != nil {
		for _, effect := range r.state.EffectLedger.Effects {
			if effect.Effect == schema.EffectConfirmed && effect.File != nil && effect.File.BeforeVersion == tools.MissingVersion {
				outputs[effect.File.Path] = true
			}
		}
	}
	return outputs
}

type coverageSelection struct {
	kind       string
	selectors  []string
	glob       string
	files      map[string]int64
	unknown    map[string]bool
	manifests  []string
	unresolved []string
	incomplete bool
}

func newCoverageSelection(kind string) coverageSelection {
	return coverageSelection{kind: kind, files: map[string]int64{}, unknown: map[string]bool{}}
}

func (r *runState) readingCoverage(ctx context.Context) *ReadingCoverage {
	r.mu.Lock()
	declared := r.state.Spec.ReadScope
	if declared != nil {
		copy := *declared
		copy.Paths = append([]string(nil), declared.Paths...)
		declared = &copy
	}
	r.mu.Unlock()
	// 子任务只投影其权限内的观察集合，不继承根任务的全范围完成声明。
	if declared != nil && observe.Identity(ctx).ParentAgentID == "" {
		return r.selectedCoverage(ctx, declared.Paths, declared.NameGlob, true)
	}
	return r.observedCoverage(ctx)
}

func (r *runState) observedCoverage(ctx context.Context) *ReadingCoverage {
	selection := newCoverageSelection("observed")
	discoveries := r.latestDiscoveries(ctx)
	outputs := r.coverageOutputs()
	scope := tools.ScopeFromContext(ctx)
	include := func(path string) bool {
		return filepath.IsLocal(path) && insideParentPaths(path, scope.AllowedPaths) && !outputs[path]
	}
	selection.incomplete = len(discoveries) == 0
	for _, d := range discoveries {
		selection.selectors = append(selection.selectors, d.Path+"\x00"+d.NameGlob)
		selection.manifests = append(selection.manifests, d.ManifestID)
		selection.incomplete = selection.incomplete || d.Truncated || d.Unreadable > 0
		for _, f := range d.Files {
			if include(f.Path) {
				// 发现时大小仅作未读提示；完成判定以当前版本的成功读取长度为准。
				selection.files[f.Path] = max(selection.files[f.Path], f.Bytes)
			}
		}
	}
	identity := observe.Identity(ctx)
	for _, read := range r.catalogSnapshot().receipts {
		if identity.ParentAgentID != "" && read.AgentID != identity.AgentID {
			continue
		}
		if include(read.Path) {
			if _, known := selection.files[read.Path]; !known {
				selection.files[read.Path] = read.TotalBytes
			}
		}
	}
	return r.calculateCoverage(ctx, selection)
}

func (r *runState) coverageForPaths(ctx context.Context, paths []string) *ReadingCoverage {
	if len(paths) == 0 {
		paths = append([]string(nil), tools.ScopeFromContext(ctx).AllowedPaths...)
	}
	if len(paths) == 0 {
		return r.observedCoverage(ctx)
	}
	return r.selectedCoverage(ctx, paths, "*", false)
}

// selectedCoverage 不枚举目录；目录必须有当前 Agent 的准确选择条件回执。
// requestedFiles 可作为逐文件范围；缺少当前读取与长度时仍为未知。
func (r *runState) selectedCoverage(ctx context.Context, paths []string, glob string, declared bool) *ReadingCoverage {
	selection := newCoverageSelection("declared")
	selection.selectors = append([]string(nil), paths...)
	sort.Strings(selection.selectors)
	selection.glob = glob
	discoveries := r.latestDiscoveries(ctx)
	reads := r.catalogSnapshot().receipts
	outputs := r.coverageOutputs()
	scope := tools.ScopeFromContext(ctx)
	identity := observe.Identity(ctx)
	include := func(path string) bool {
		return filepath.IsLocal(path) && insideParentPaths(path, scope.AllowedPaths) && !outputs[path]
	}
	addFile := func(path string, bytes int64) {
		if include(path) {
			selection.files[path] = bytes
		}
	}
	for _, path := range selection.selectors {
		if !include(path) {
			selection.incomplete = true
			selection.unresolved = append(selection.unresolved, path)
			continue
		}
		isDir, err := tools.PathIsDirectory(ctx, path)
		if err != nil {
			selection.incomplete = true
			selection.unresolved = append(selection.unresolved, path)
			continue
		}
		if isDir {
			found := false
			for _, d := range discoveries {
				if d.Path != path || d.NameGlob != glob {
					continue
				}
				found = true
				selection.manifests = append(selection.manifests, d.ManifestID)
				if d.Truncated || d.Unreadable > 0 {
					selection.incomplete = true
					selection.unresolved = append(selection.unresolved, path)
				}
				for _, f := range d.Files {
					// 工具回执证明目录成员；安全解析内部符号链接后文件路径
					// 可能与选择路径前缀不同，不能静默丢成 complete(0/0)。
					if include(f.Path) {
						addFile(f.Path, f.Bytes)
					} else if !outputs[f.Path] {
						selection.incomplete = true
						selection.unresolved = append(selection.unresolved, path)
					}
				}
			}
			if !found {
				selection.incomplete = true
				selection.unresolved = append(selection.unresolved, path)
			}
			continue
		}
		// 显式文件不受目录 glob 排除；未读时不能制造 complete(0/0)。
		addFile(path, 0)
		known := false
		for _, d := range discoveries {
			for _, f := range d.Files {
				if f.Path == path {
					addFile(path, f.Bytes)
					known = true
				}
			}
		}
		for _, read := range reads {
			if read.Path == path && (identity.ParentAgentID == "" || read.AgentID == identity.AgentID) {
				addFile(path, read.TotalBytes)
				known = true
			}
		}
		if !known {
			selection.unknown[path] = true
			selection.incomplete = true
		}
	}
	if !declared {
		// 仅供文件/子报告局部检查，不把它当作整个任务声明。
		selection.kind = "observed"
	}
	return r.calculateCoverage(ctx, selection)
}

func coverageHash(value any) string {
	encoded, _ := json.Marshal(value)
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:])
}

func (r *runState) calculateCoverage(ctx context.Context, selected coverageSelection) *ReadingCoverage {
	sort.Strings(selected.manifests)
	selected.manifests = uniqueAnalysisIDs(selected.manifests)
	sort.Strings(selected.unresolved)
	selected.unresolved = uniqueAnalysisIDs(selected.unresolved)
	result := &ReadingCoverage{Status: "unknown", ScopeKind: selected.kind, ManifestIDs: selected.manifests,
		UnresolvedPaths: selected.unresolved, Files: []FileReadingCoverage{}, DiscoveryIncomplete: selected.incomplete}
	if len(selected.manifests) == 1 {
		result.ManifestID = selected.manifests[0] // 兼容旧展示字段。
	}
	r.mu.Lock()
	taskID := r.state.Spec.ID
	r.mu.Unlock()
	owner := ""
	if selected.kind != "declared" {
		owner = observe.Identity(ctx).AgentID
	}
	result.ScopeID = coverageHash(struct {
		Task, Owner, Kind, Glob string
		Selectors               []string
	}{taskID, owner, selected.kind, selected.glob, selected.selectors})
	var names []string
	for name := range selected.files {
		names = append(names, name)
	}
	sort.Strings(names)
	reads := r.catalogSnapshot().receipts
	identity := observe.Identity(ctx)
	versions := map[string]string{}
	for _, path := range names {
		file := FileReadingCoverage{Path: path, TotalBytes: selected.files[path], Missing: []ByteInterval{}}
		if selected.unknown[path] {
			file.Unknown = true
			result.Files = append(result.Files, file)
			continue
		}
		version := ""
		if ctx.Err() == nil {
			version, _ = tools.FileVersion(ctx, path)
		} else {
			result.RecordedOnly = true
		}
		versions[path] = version
		if version == "" || version == tools.MissingVersion {
			file.Unknown = true
			result.DiscoveryIncomplete = true
		}
		var ranges []ByteInterval
		seen := false
		for _, read := range reads {
			if read.Path != path || identity.ParentAgentID != "" && read.AgentID != identity.AgentID {
				continue
			}
			if version == "" || read.Version != version {
				continue
			}
			seen = true
			file.Version = read.Version
			file.TotalBytes = read.TotalBytes
			ranges = append(ranges, ByteInterval{read.ByteStart, read.ByteEnd})
		}
		sort.Slice(ranges, func(i, j int) bool { return ranges[i].Start < ranges[j].Start })
		cursor := int64(0)
		for _, interval := range ranges {
			start, end := max(int64(0), interval.Start), min(file.TotalBytes, interval.End)
			if start > cursor {
				file.Missing = append(file.Missing, ByteInterval{cursor, start})
			}
			if end > cursor {
				file.ReadBytes += end - max(cursor, start)
				cursor = end
			}
		}
		if cursor < file.TotalBytes {
			file.Missing = append(file.Missing, ByteInterval{cursor, file.TotalBytes})
		}
		file.Complete = seen && !file.Unknown && len(file.Missing) == 0
		if file.Complete {
			result.CompleteFiles++
		}
		result.Files = append(result.Files, file)
	}
	result.TotalFiles = len(result.Files)
	if !result.DiscoveryIncomplete && !result.RecordedOnly {
		result.Status = "partial"
		if result.CompleteFiles == result.TotalFiles {
			result.Status = "complete"
		}
	}
	// 修订绑定成员与版本，不随读完了多少字节变化；刷新清单产生新修订。
	type member struct {
		Path, Version string
		Bytes         int64
	}
	var members []member
	for _, f := range result.Files {
		members = append(members, member{f.Path, versions[f.Path], f.TotalBytes})
	}
	result.ScopeRevision = coverageHash(struct {
		Members               []member
		Manifests, Unresolved []string
		Incomplete            bool
	}{members, result.ManifestIDs, result.UnresolvedPaths, result.DiscoveryIncomplete})
	return result
}

func readingCoverageText(coverage *ReadingCoverage) string {
	if coverage == nil {
		return ""
	}
	label, boundary := "历史清单覆盖", "历史记录缺少任务范围身份，不能推断整个任务资料完整"
	switch coverage.ScopeKind {
	case "observed":
		label, boundary = "已发现材料覆盖", "任务来源范围未声明，仅针对当前发现及读取的材料"
	case "declared":
		label, boundary = "声明资料范围覆盖", "仅针对声明选择条件及本次清单成员，不证明目录此后没有新增资料"
	}
	text := fmt.Sprintf("%s：%s，%d/%d 个文件完整；%s，不代表分析或验收完成。", label, coverage.Status, coverage.CompleteFiles, coverage.TotalFiles, boundary)
	if coverage.RecordedOnly {
		text += " 当前版本未确认。"
	}
	if len(coverage.UnresolvedPaths) > 0 {
		text += fmt.Sprintf(" %d 个来源路径尚未完整发现。", len(coverage.UnresolvedPaths))
	}
	var missing int64
	for _, file := range coverage.Files {
		for _, gap := range file.Missing {
			missing += max(int64(0), gap.End-gap.Start)
		}
	}
	if missing > 0 {
		text += fmt.Sprintf(" 已知未读 %d 字节。", missing)
	}
	return text
}

func (r *runState) readingContext(ctx context.Context) string {
	if !r.readTrackingEnabled() {
		return ""
	}
	coverage := r.readingCoverage(ctx)
	type gap struct {
		Path             string         `json:"path"`
		Missing          []ByteInterval `json:"missing,omitempty"`
		Unknown          bool           `json:"unknown,omitempty"`
		Outside          bool           `json:"outside_declared_scope,omitempty"`
		NextOffset       int64          `json:"next_offset"`
		OmittedIntervals int            `json:"omitted_intervals,omitempty"`
	}
	var gaps []gap
	omitted := 0
	addGap := func(file FileReadingCoverage, outside bool) {
		if file.Complete {
			return
		}
		item := gap{Path: file.Path, Missing: file.Missing, Unknown: file.Unknown, Outside: outside}
		if len(item.Missing) > 0 {
			item.NextOffset = item.Missing[0].Start
		}
		if len(item.Missing) > 4 {
			item.OmittedIntervals = len(item.Missing) - 4
			item.Missing = item.Missing[:4]
		}
		candidate, _ := json.Marshal(append(append([]gap(nil), gaps...), item))
		if len(gaps) >= 8 || len(candidate) > 8*1024 {
			omitted++
			return
		}
		gaps = append(gaps, item)
	}
	for _, file := range coverage.Files {
		addGap(file, false)
	}
	if coverage.ScopeKind == "declared" {
		known := map[string]bool{}
		for _, file := range coverage.Files {
			known[file.Path] = true
		}
		for _, file := range r.observedCoverage(ctx).Files {
			if !known[file.Path] {
				addGap(file, true)
			}
		}
	}
	unresolved := []string{}
	for _, path := range coverage.UnresolvedPaths {
		candidate, _ := json.Marshal(append(append([]string(nil), unresolved...), path))
		if len(unresolved) >= 8 || len(candidate) > 2*1024 {
			break
		}
		unresolved = append(unresolved, path)
	}
	data, _ := json.Marshal(struct {
		Status            string   `json:"status"`
		Kind              string   `json:"scope_kind"`
		ID                string   `json:"scope_id"`
		Revision          string   `json:"scope_revision"`
		Complete          int      `json:"complete"`
		Total             int      `json:"total"`
		Partial           []gap    `json:"incomplete_files,omitempty"`
		Omitted           int      `json:"omitted_incomplete_files,omitempty"`
		Unresolved        []string `json:"unresolved_paths,omitempty"`
		UnresolvedOmitted int      `json:"omitted_unresolved_paths,omitempty"`
	}{coverage.Status, coverage.ScopeKind, coverage.ScopeID, coverage.ScopeRevision, coverage.CompleteFiles, coverage.TotalFiles, gaps, omitted, unresolved, len(coverage.UnresolvedPaths) - len(unresolved)})
	r.mu.Lock()
	declared := r.state.Spec.ReadScope
	policy := "按当前任务权限与原有委派策略执行；读取记忆不扩大委派权限。"
	if r.efficiencyEnabled() {
		policy = "委派策略 " + r.state.Spec.Delegation + "；当前不可委派（未知/较小范围或已达上限），由根任务继续。"
	}
	r.mu.Unlock()
	if r.delegationVisible(ctx) {
		policy = "委派可用；累计最多2个子任务，每子最多4次模型和8次工具，均计共享额度。"
	}
	sourcePolicy := "当前只是观察集合，不能把完整计数解释为整个任务资料全部读完。"
	if coverage.ScopeKind == "declared" && declared != nil {
		filters, _ := json.Marshal(struct {
			Paths    []string `json:"paths"`
			Glob     string   `json:"name_glob"`
			Required bool     `json:"require_complete"`
		}{declared.Paths, declared.NameGlob, declared.RequireComplete})
		if len(filters) <= 2*1024 {
			sourcePolicy = "声明的读取选择条件：" + string(filters) + "。目录通过find_files按准确path/name_glob完整发现；截断或局部清单不能证明父目录完整。"
		} else {
			sourcePolicy = "任务有显式读取范围；完整选择条件见原任务配置，省略超长投影不改变要求。"
		}
	}
	return "\n程序读取覆盖：" + string(data) + "\n" + sourcePolicy + "\n" + policy + r.discoveryContext(ctx) + "\n缺口按源字节计算，不要假设两页就是全文。续读示例read_file(path=缺口path,offset=next_offset)；完整要求尚未满足时优先补必要缺口，程序不会自动补读。合法行片段不等于完整阅读。初读就启用编号；字节分页用read_file，行定位用read_lines，禁止只为补行号全文二读。复核已有材料时提供review.target_id和具体question。跨文档先核对V1/V2演进、可选字段及.get默认值，引用重要结论的文件与行号；没有实际执行依据仅作静态推导或待实测，缺页不能支持整范围不存在的断言。读取完整不代表结论准确或验收通过。"
}
