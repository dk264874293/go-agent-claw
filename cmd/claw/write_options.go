package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/dk264874293/go-agent-claw/internal/engine"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

// readScopeFlags 不把逗号当分隔符，允许包含标点的真实文件名。
type readScopeFlags []string

func (v *readScopeFlags) String() string { return strings.Join(*v, "、") }
func (v *readScopeFlags) Set(value string) error {
	*v = append(*v, value)
	return nil
}

func applyCLIReadScope(spec *engine.TaskSpec, paths []string, glob string, complete, globExplicit, completeExplicit bool) error {
	if len(paths) == 0 {
		if complete || globExplicit || completeExplicit {
			return fmt.Errorf("-read-name-glob 和 -require-full-read 须同时提供 -read-scope；不能从任务文字推断完整范围。")
		}
		return nil
	}
	spec.ReadScope = &engine.ReadScopeSpec{Version: 1, Paths: append([]string(nil), paths...), NameGlob: glob, RequireComplete: complete}
	return engine.NormalizeReadScope(spec)
}

func applyCLIOutputStyle(spec *engine.TaskSpec, style string, explicit bool) error {
	if style != "concise" && style != "detailed" {
		return fmt.Errorf("输出篇幅必须为 concise 或 detailed。")
	}
	if !spec.Resume || explicit {
		spec.OutputStyle = style
	}
	return nil
}

// 未显式配置时保留零值，让新建和恢复分别使用各自的默认值与持久配置。
func applyCLIReadPageBytes(spec *engine.TaskSpec, pageBytes int, explicit bool) error {
	if pageBytes != 8000 && pageBytes != 16000 {
		return fmt.Errorf("读取档位必须为 8000 或 16000 字节。")
	}
	if explicit {
		spec.ReadPageBytes = pageBytes
	}
	return nil
}

func applyCLIReport(spec *engine.TaskSpec, target string, mode tools.Mode, analysis, plan bool) error {
	if target == "" {
		return nil
	}
	if spec.Resume {
		return fmt.Errorf("恢复时沿用原报告配置，不能重新指定 -report。")
	}
	if mode != tools.ModeWorkspace || spec.Kind != "" && spec.Kind != engine.TaskChange || analysis || plan || spec.VerifyCommand != "" {
		return fmt.Errorf("证据报告仅支持 workspace + change，不能与分析、规划、验收命令或回答任务组合。")
	}
	if !filepath.IsLocal(target) || filepath.Ext(target) != ".md" || strings.ContainsRune(target, 0) {
		return fmt.Errorf("报告目标必须是工作区内的相对 Markdown 路径（.md）。")
	}
	for _, part := range strings.Split(filepath.ToSlash(target), "/") {
		if part == ".." {
			return fmt.Errorf("报告目标不能包含上级目录跳转。")
		}
	}
	spec.Kind = engine.TaskChange
	spec.Report = &engine.ReportSpec{Version: 1, Format: "evidence_v1", TargetPath: filepath.ToSlash(filepath.Clean(target)), CreateOnly: true}
	return nil
}
