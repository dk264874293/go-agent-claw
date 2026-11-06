package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

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

func (r *runState) readingCoverage(ctx context.Context) *ReadingCoverage {
	selected := r.primaryDiscovery(ctx)
	if selected != nil {
		paths := make([]string, 0, len(selected.Files))
		for _, f := range selected.Files {
			paths = append(paths, f.Path)
		}
		coverage := r.coverageFromManifest(ctx, selected, paths)
		return coverage
	}
	return r.coverageForPaths(ctx, nil)
}

// 第一份当前 Agent 的发现确定观察范围；相同范围的显式刷新可更新它，
// 不让后续局部目录或不同过滤条件悄悄缩小任务清单。
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

func (r *runState) coverageForPaths(ctx context.Context, paths []string) *ReadingCoverage {
	discoveries := r.discoverySnapshot()
	scope := tools.ScopeFromContext(ctx)
	if len(paths) == 0 {
		paths = append([]string(nil), scope.AllowedPaths...)
	}
	if len(paths) == 0 {
		return r.coverageFromManifest(ctx, nil, nil)
	}
	var selected *schema.DiscoveryReceipt
	for i := len(discoveries) - 1; i >= 0; i-- {
		d := discoveries[i]
		if d.Truncated || d.Unreadable > 0 {
			continue
		}
		covered := len(paths) == 0
		if len(paths) > 0 {
			covered = true
			for _, p := range paths {
				if d.Path != "." && p != d.Path && !strings.HasPrefix(filepath.ToSlash(p), strings.TrimSuffix(filepath.ToSlash(d.Path), "/")+"/") {
					covered = false
				}
				known := false
				for _, file := range d.Files {
					if file.Path == p {
						known = true
						break
					}
				}
				if !known {
					isDir, err := tools.PathIsDirectory(ctx, p)
					if err != nil || !isDir {
						covered = false
					}
				}
			}
		}
		if covered {
			selected = &d
			break
		}
	}
	return r.coverageFromManifest(ctx, selected, paths)
}

func (r *runState) coverageFromManifest(ctx context.Context, manifest *schema.DiscoveryReceipt, paths []string) *ReadingCoverage {
	result := &ReadingCoverage{Status: "unknown", Files: []FileReadingCoverage{}, DiscoveryIncomplete: manifest == nil}
	files := make(map[string]int64)
	scope := tools.ScopeFromContext(ctx)
	identity := observe.Identity(ctx)
	include := func(path string) bool {
		return filepath.IsLocal(path) && insideParentPaths(path, scope.AllowedPaths) && (len(paths) == 0 || insideParentPaths(path, paths))
	}
	if manifest != nil {
		result.ManifestID = manifest.ManifestID
		result.DiscoveryIncomplete = manifest.Truncated || manifest.Unreadable > 0
		for _, file := range manifest.Files {
			if include(file.Path) {
				files[file.Path] = file.Bytes
			}
		}
	}
	reads := r.catalogSnapshot().receipts
	for _, read := range reads {
		if identity.ParentAgentID != "" && read.AgentID != identity.AgentID {
			continue
		}
		if include(read.Path) {
			if manifest != nil {
				if _, belongs := files[read.Path]; !belongs {
					continue
				}
			}
			if _, exists := files[read.Path]; !exists {
				files[read.Path] = read.TotalBytes
			}
		}
	}
	// 显式文件不能因不匹配发现 glob 而被丢掉，制造 complete(0/0)。
	unknown := make(map[string]bool)
	for _, path := range paths {
		if _, ok := files[path]; ok {
			continue
		}
		if !filepath.IsLocal(path) || !insideParentPaths(path, scope.AllowedPaths) {
			result.DiscoveryIncomplete = true
			continue
		}
		isDir, err := tools.PathIsDirectory(ctx, path)
		if err != nil || !isDir {
			files[path] = 0
			unknown[path] = true
			result.DiscoveryIncomplete = true
		}
	}
	// 明确的文件路径也可以作为范围，但没有发现清单时不推断目录完整。
	if manifest == nil && len(paths) > 0 {
		allFiles := true
		for _, p := range paths {
			if _, ok := files[p]; !ok || unknown[p] {
				allFiles = false
			}
		}
		result.DiscoveryIncomplete = !allFiles
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, path := range names {
		file := FileReadingCoverage{Path: path, TotalBytes: files[path], Missing: []ByteInterval{}}
		if unknown[path] {
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
		file.Complete = seen && len(file.Missing) == 0
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
	return result
}

func readingCoverageText(coverage *ReadingCoverage) string {
	if coverage == nil {
		return ""
	}
	text := fmt.Sprintf("读取覆盖：%s，%d/%d 个文件完整；仅针对发现清单 %s，不代表分析或验收完成。", coverage.Status, coverage.CompleteFiles, coverage.TotalFiles, coverage.ManifestID)
	if coverage.RecordedOnly {
		text += " 当前版本未确认。"
	}
	return text
}

func (r *runState) readingContext(ctx context.Context) string {
	if !r.efficiencyEnabled() {
		return ""
	}
	coverage := r.readingCoverage(ctx)
	type gap struct {
		Path    string         `json:"path"`
		Missing []ByteInterval `json:"missing"`
	}
	var gaps []gap
	omitted := 0
	for _, file := range coverage.Files {
		if !file.Complete && file.ReadBytes > 0 {
			if len(gaps) < 8 {
				gaps = append(gaps, gap{file.Path, file.Missing})
			} else {
				omitted++
			}
		}
	}
	data, _ := json.Marshal(struct {
		Status   string `json:"status"`
		Manifest string `json:"manifest"`
		Complete int    `json:"complete"`
		Total    int    `json:"total"`
		Partial  []gap  `json:"partial_files,omitempty"`
		Omitted  int    `json:"omitted_partial_files,omitempty"`
	}{coverage.Status, coverage.ManifestID, coverage.CompleteFiles, coverage.TotalFiles, gaps, omitted})
	policy := "委派策略 " + r.state.Spec.Delegation + "；当前不可委派（未知/较小范围或已达上限），由根任务继续。"
	if r.delegationVisible(ctx) {
		policy = "委派可用；累计最多2个子任务，每子最多4次模型和8次工具，均计共享额度。"
	}
	return "\n程序读取覆盖（不证明分析完成；未读文件见发现清单）：" + string(data) + "\n" + policy + "\n完整读取后优先交付。初读就启用编号；字节分页用read_file，行定位用read_lines，禁止只为补行号全文二读。复核已有材料时提供review.target_id和具体question。跨文档先核对版本演进、可选字段及默认值；未执行仅作静态推导，未读部分不能支撑全文件不存在的断言。"
}
