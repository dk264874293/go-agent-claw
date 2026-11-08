package engine

import (
	"context"
	"fmt"

	"github.com/dk264874293/go-agent-claw/internal/tools"
)

// 完整阅读只在调用方明确声明后成为交付条件，不限制普通中间编辑。
func (r *runState) checkReadingComplete(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.state.Spec.ReadScope == nil || !r.state.Spec.ReadScope.RequireComplete {
		return nil
	}
	c := r.readingCoverage(ctx)
	if c == nil || c.ScopeKind != "declared" || c.Status != "complete" || c.DiscoveryIncomplete || c.RecordedOnly {
		return reportError{"reading_incomplete", "声明来源尚未完整读取；" + readingCoverageText(c)}
	}
	return nil
}

func validateReadPolicy(s taskState) error {
	if err := ValidateReadScopeSpec(s.Spec.ReadScope); err != nil {
		return err
	}
	if s.Spec.OutputStyle != "" && s.Spec.OutputStyle != "concise" && s.Spec.OutputStyle != "detailed" {
		return fmt.Errorf("持久输出偏好无效")
	}
	return nil
}

// ReportScopeDependency 保存完整性判断的全部来源，独立于逐条摘录来源。
type ReportScopeDependency struct {
	ScopeID        string            `json:"scope_id"`
	ScopeRevision  string            `json:"scope_revision"`
	SourceVersions map[string]string `json:"source_versions"`
}

func dependencyFromCoverage(c *ReadingCoverage) ReportScopeDependency {
	d := ReportScopeDependency{ScopeID: c.ScopeID, ScopeRevision: c.ScopeRevision, SourceVersions: map[string]string{}}
	for _, f := range c.Files {
		d.SourceVersions[f.Path] = f.Version
	}
	return d
}

// 只核对已冻结成员的版本；不扫描目录来假装证明没有新增成员。
func checkScopeDependencies(ctx context.Context, deps []ReportScopeDependency) error {
	for _, dep := range deps {
		for path, expected := range dep.SourceVersions {
			actual, err := tools.FileVersion(ctx, path)
			if err != nil || actual != expected {
				return fmt.Errorf("完整阅读依赖的来源版本已变化：%s", path)
			}
		}
	}
	return nil
}
