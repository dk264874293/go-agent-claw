package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/schema"
)

var deliveryAliasPattern = regexp.MustCompile(`\b[PEF][0-9]+:[1-9][0-9]*(?:-[1-9][0-9]*)?\b`)
var deliveryPathPattern = regexp.MustCompile(`(?:\.?[A-Za-z_][A-Za-z0-9_.-]*/)+[A-Za-z0-9_.-]*`)

type deliveryPathFact struct {
	path     string
	location schema.ContentSourceLocation
}

// 只使用本人本轮已成功交付且仍有效的读取，不扫描工作区，不执行原文。
func (r *runState) deliveryPathFacts(ctx context.Context) []deliveryPathFact {
	identity := observe.Identity(ctx)
	r.mu.Lock()
	entries := make(map[string]AnalysisEvidence)
	for id, e := range r.analysisRuntime.sources {
		if e.AgentID == identity.AgentID && e.RunID == r.runID && r.analysisRuntime.delivered[identity.AgentID][id] {
			entries[id] = e
		}
	}
	r.mu.Unlock()
	var facts []deliveryPathFact
	seen := map[string]bool{}
	seenFiles := map[string]bool{}
	for i, id := range sortedAnalysisIDs(entries) {
		if i >= 128 || len(facts) >= 256 {
			break
		}
		e, err := r.analysisLookup(ctx, true)(id)
		if err != nil {
			continue
		}
		if !seenFiles[e.Receipt.Path] {
			facts = append(facts, deliveryPathFact{location: schema.ContentSourceLocation{Path: e.Receipt.Path, Version: e.Receipt.Version, EvidenceID: id, FirstLine: e.Receipt.FirstLine, LastLine: e.Receipt.LastLine}})
			seenFiles[e.Receipt.Path] = true
		}
		if len(facts) >= 256 {
			break
		}
		lines := strings.Split(string(e.Source), "\n")
		for n, line := range lines {
			if n == 0 && e.Receipt.ByteStart > 0 && (e.Receipt.LineBoundary == nil || !e.Receipt.LineBoundary.StartsLine) {
				continue
			}
			if n == len(lines)-1 && e.Receipt.ByteEnd < e.Receipt.TotalBytes && (e.Receipt.LineBoundary == nil || !e.Receipt.LineBoundary.EndsLine) {
				continue
			}
			for _, span := range deliveryPathPattern.FindAllStringIndex(line, -1) {
				// URL、绝对路径和被切开的token不升级为工作区相对路径事实。
				if span[0] > 0 && strings.ContainsRune("/:abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_.-", rune(line[span[0]-1])) {
					continue
				}
				path := filepath.ToSlash(filepath.Clean(line[span[0]:span[1]]))
				if !filepath.IsLocal(path) || !strings.Contains(path, "/") || seen[path] {
					continue
				}
				seen[path] = true
				facts = append(facts, deliveryPathFact{path: path, location: schema.ContentSourceLocation{Path: e.Receipt.Path, Version: e.Receipt.Version, EvidenceID: id, FirstLine: e.Receipt.FirstLine + n, LastLine: e.Receipt.FirstLine + n}})
				if len(facts) >= 256 {
					break
				}
			}
			if len(facts) >= 256 {
				break
			}
		}
	}
	return facts
}

func deliveryTreePaths(lines []string) map[int]string {
	paths := map[int]string{}
	var parents []string
	base := -1
	root := ""
	for n, line := range lines {
		at := strings.Index(line, "├── ")
		if at < 0 {
			at = strings.Index(line, "└── ")
		}
		if at < 0 {
			parents, base = nil, -1
			root = ""
			trim := strings.TrimSpace(line)
			// 明确的隐藏配置目录根不能被丢掉；项目名称等未知外层不猜测。
			if strings.HasPrefix(trim, ".") && strings.HasSuffix(trim, "/") && filepath.IsLocal(trim) && !strings.ContainsAny(trim, " \t`*?{}<>") {
				root = trim
			}
			continue
		}
		depth := len([]rune(line[:at])) / 4
		if base < 0 {
			base = depth
		}
		depth -= base
		if depth < 0 || depth > len(parents) {
			continue
		}
		name := strings.TrimSpace(line[at+len("├── "):])
		if before, _, ok := strings.Cut(name, " #"); ok {
			name = strings.TrimSpace(before)
		}
		if !filepath.IsLocal(name) || strings.ContainsAny(name, "*?{}<>`") {
			continue
		}
		parents = parents[:depth]
		path := filepath.ToSlash(filepath.Join(append(append([]string(nil), parents...), name)...))
		paths[n+1] = filepath.ToSlash(filepath.Join(root, path))
		if strings.HasSuffix(name, "/") {
			parents = append(parents, strings.TrimSuffix(name, "/"))
		}
	}
	return paths
}

// 检查结果是非阻断提示；别名不猜映射，路径差异不自动判作错误设计。
func reviewDeliveryMarkdown(text string, facts []deliveryPathFact) []schema.ContentReviewIssue {
	if json.Valid([]byte(strings.TrimSpace(text))) {
		return nil
	}
	lines := strings.Split(text, "\n")
	var issues []schema.ContentReviewIssue
	inFence := false
	fence := ""
	var visible strings.Builder
	definitions := map[string]bool{}
	for _, line := range lines {
		id, value, ok := strings.Cut(strings.TrimSpace(line), " = ")
		if !ok {
			continue
		}
		for _, fact := range facts {
			if fact.location.Path != "" && strings.Contains(value, fact.location.Path) {
				definitions[id] = true
			}
		}
	}
	for n, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "```") || strings.HasPrefix(trim, "~~~") {
			mark := trim[:3]
			if !inFence {
				inFence = true
				fence = mark
			} else if mark == fence {
				inFence = false
			}
			continue
		}
		if inFence {
			continue
		}
		visible.WriteString(line)
		visible.WriteByte('\n')
		for _, span := range deliveryAliasPattern.FindAllStringIndex(line, -1) {
			// URL路径中的同形片段不能当作内部引用缺陷。
			if span[0] > 0 && line[span[0]-1] == '/' {
				continue
			}
			alias := line[span[0]:span[1]]
			id := strings.SplitN(alias, ":", 2)[0]
			if !definitions[id] {
				issues = append(issues, schema.ContentReviewIssue{Code: "citation_alias_unresolved", Line: n + 1, Message: alias + "未识别到明确文件映射；请改用证据目录的完整相对路径和行号，不按内部编号猜文件。"})
			}
		}
	}
	known := map[string]bool{}
	for _, fact := range facts {
		if fact.path != "" {
			known[fact.path] = true
		}
	}
	for line, path := range deliveryTreePaths(lines) {
		if known[path] {
			continue
		}
		matches := map[string]deliveryPathFact{}
		for _, fact := range facts {
			if strings.HasSuffix(fact.path, "/"+path) {
				matches[fact.path] = fact
			}
		}
		if len(matches) != 1 {
			continue
		}
		for _, fact := range matches {
			issues = append(issues, schema.ContentReviewIssue{Code: "source_path_difference", Line: line, Message: fmt.Sprintf("目录示意%q与已读原文路径%q不同；请核对加载位置，或明确标为建议布局，不能把建议当作原文事实。", path, fact.path), Locations: []schema.ContentSourceLocation{fact.location}})
		}
	}
	// 仅提示长篇自由文本中完全没有可识别位置的情况，不推断哪些命题重要或引用是否支持结论。
	if len(issues) == 0 && visible.Len() >= 1024 && len(facts) > 0 && !deliveryHasSourcePosition(visible.String(), facts) {
		issues = append(issues, schema.ContentReviewIssue{Code: "source_locations_absent", Line: 1, Message: "正文未识别到已读文件的完整相对路径:行号。请给重要结论就地补位置，比较引用两端；文末清单不能代替依据。此提示不证明已有其他格式的引用错误，也不校验语义支持。"})
	}
	sort.SliceStable(issues, func(i, j int) bool {
		if issues[i].Line != issues[j].Line {
			return issues[i].Line < issues[j].Line
		}
		return issues[i].Code < issues[j].Code
	})
	return issues
}

func deliveryHasSourcePosition(text string, facts []deliveryPathFact) bool {
	for _, fact := range facts {
		if fact.location.Path == "" {
			continue
		}
		for _, separator := range []string{":", "："} {
			prefix := fact.location.Path + separator
			remaining := text
			for {
				at := strings.Index(remaining, prefix)
				if at < 0 {
					break
				}
				remaining = remaining[at+len(prefix):]
				if len(remaining) > 0 && remaining[0] >= '1' && remaining[0] <= '9' {
					return true
				}
			}
		}
	}
	return false
}

func (r *runState) reviewDeliveryContent(ctx context.Context, path, text string) *schema.ContentReview {
	if !r.deliveryReviewEnabled() || path != "" && strings.ToLower(filepath.Ext(path)) != ".md" {
		return nil
	}
	identity := observe.Identity(ctx)
	review := &schema.ContentReview{Version: 1, RunID: r.runID, AgentID: identity.AgentID, ActionID: ActionID(ctx), Path: path, ContentSHA: deliveryContentSHA(text), Status: "no_issue_found"}
	if len(text) > 256*1024 || len(path) > 512 {
		review.Status = "not_run"
		review.Reason = "content_or_path_over_limit"
		if len(path) > 512 {
			review.Path = ""
		}
		return review
	}
	issues := reviewDeliveryMarkdown(text, r.deliveryPathFacts(ctx))
	if len(issues) > 0 {
		review.Status = "issues_found"
	}
	for _, issue := range issues {
		review.Issues = append(review.Issues, issue)
		review.Omitted = len(issues) - len(review.Issues)
		raw, _ := json.Marshal(review)
		if len(raw) > 3000 || len(review.Issues) > 8 {
			review.Issues = review.Issues[:len(review.Issues)-1]
			review.Omitted = len(issues) - len(review.Issues)
			break
		}
	}
	return review
}

func contentReviewIssues(review *schema.ContentReview) []DeliveryReviewIssue {
	if review == nil {
		return nil
	}
	var issues []DeliveryReviewIssue
	for _, item := range review.Issues {
		id := "content_" + deliveryContentSHA(fmt.Sprintf("%s:%d:%s", review.Path, item.Line, item.Code))[:16]
		reason := fmt.Sprintf("候选/确认正文第%d行：%s", item.Line, item.Message)
		var evidence []string
		for _, location := range item.Locations {
			reason += fmt.Sprintf(" 原文位置：%s:%d-%d。", location.Path, location.FirstLine, location.LastLine)
			evidence = append(evidence, location.EvidenceID)
		}
		issues = append(issues, DeliveryReviewIssue{ID: id, Kind: item.Code, Path: review.Path, EvidenceIDs: evidence, Reason: reason})
	}
	if review.Omitted > 0 {
		issues = append(issues, DeliveryReviewIssue{ID: "content_omitted_" + deliveryContentSHA(review.Path)[:12], Kind: "content_review_omitted", Path: review.Path, Reason: fmt.Sprintf("另有%d条机械提示未展开；不能把省略等同于已解决", review.Omitted)})
	}
	if review.Status == "not_run" {
		issues = append(issues, DeliveryReviewIssue{ID: "content_not_run_" + deliveryContentSHA(review.Path)[:12], Kind: "content_review_not_run", Path: review.Path, Reason: "正文机械检查未完成：" + review.Reason + "；不表示先前提示已经解决"})
	}
	return issues
}

func (r *runState) reviewDeliveryCandidate(ctx context.Context, msg schema.Message) []DeliveryReviewIssue {
	if len(msg.ToolCalls) == 0 {
		return contentReviewIssues(r.reviewDeliveryContent(ctx, "", msg.Content))
	}
	var issues []DeliveryReviewIssue
	for _, call := range msg.ToolCalls {
		if call.Name != "write_file" {
			continue
		}
		var input struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if json.Unmarshal(call.Arguments, &input) == nil {
			issues = append(issues, contentReviewIssues(r.reviewDeliveryContent(ctx, input.Path, input.Content))...)
		}
	}
	return issues
}

func (r *runState) attachDeliveryContentReview(ctx context.Context, call schema.ToolCall, result *schema.ToolResult) {
	if !r.deliveryReviewEnabled() || result.Effect != schema.EffectConfirmed || result.Status != schema.ToolSucceeded {
		return
	}
	var input struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if json.Unmarshal(call.Arguments, &input) != nil {
		return
	}
	if call.Name == "write_file" {
		result.ContentReview = r.reviewDeliveryContent(ctx, input.Path, input.Content)
	}
	if call.Name == "edit_file" && strings.ToLower(filepath.Ext(input.Path)) == ".md" {
		path := input.Path
		if len(path) > 512 {
			path = ""
		}
		result.ContentReview = &schema.ContentReview{Version: 1, RunID: r.runID, AgentID: observe.Identity(ctx).AgentID, ActionID: ActionID(ctx), Path: path, Status: "not_run", Reason: "edit_fragment_not_full_document"}
	}
	if result.ContentReview != nil {
		raw, _ := json.Marshal(result.ContentReview)
		result.Output += "\n交付机械提示（不证明语义或运行验收）：" + string(raw)
	}
}

// 按效果账本的实际动作顺序选择最后一次文件检查，不按Completed映射遍历顺序累加。
// 回执只证明动作当时的检查；后续编辑没有完整正文时保留未检查提示。
func (r *runState) currentDeliveryContentIssues(ctx context.Context) []DeliveryReviewIssue {
	if !r.deliveryReviewEnabled() {
		return nil
	}
	r.mu.Lock()
	byAction := map[string]*schema.ContentReview{}
	for _, result := range r.state.Completed {
		c := result.ContentReview
		if c != nil && c.RunID == r.runID && c.AgentID == observe.Identity(ctx).AgentID && c.ActionID != "" {
			byAction[c.ActionID] = c
		}
	}
	latest := map[string]*schema.ContentReview{}
	if r.state.EffectLedger != nil {
		for _, effect := range r.state.EffectLedger.Effects {
			if effect.File != nil && effect.Effect == schema.EffectConfirmed {
				path := filepath.ToSlash(filepath.Clean(effect.File.Path))
				if c := byAction[effect.ActionID]; c != nil && filepath.ToSlash(filepath.Clean(c.Path)) == path {
					latest[path] = c
				} else {
					delete(latest, path)
				}
			}
		}
	}
	r.mu.Unlock()
	var paths []string
	for path := range latest {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	var issues []DeliveryReviewIssue
	for _, path := range paths {
		issues = append(issues, contentReviewIssues(latest[path])...)
	}
	return issues
}
