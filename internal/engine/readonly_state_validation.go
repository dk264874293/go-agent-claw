package engine

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/dk264874293/go-agent-claw/internal/tools"
)

// validateReadonlyState 只校验可选持久状态的结构，不核对当前文件，也不提升旧日志的证据等级。
func validateReadonlyState(state taskState) error {
	ids := make(map[string]*ReadonlyFailureBoundary)
	for key, boundary := range state.ReadonlyFailures {
		if boundary == nil {
			return fmt.Errorf("只读失败边界 %q 为空", key)
		}
		if strings.TrimSpace(key) == "" || key != boundary.Key || strings.TrimSpace(boundary.ID) == "" {
			return fmt.Errorf("只读失败边界的键或身份无效: %q", key)
		}
		if _, exists := ids[boundary.ID]; exists {
			return fmt.Errorf("只读失败边界身份重复: %q", boundary.ID)
		}
		ids[boundary.ID] = boundary
		if !filepath.IsLocal(boundary.Path) || filepath.Clean(boundary.Path) != boundary.Path || strings.ContainsRune(boundary.Path, 0) {
			return fmt.Errorf("只读失败边界路径必须是规范的工作区相对路径: %q", boundary.Path)
		}
		if boundary.Tool != "list_dir" || boundary.ErrorCode != tools.ErrPathNotDirectory {
			return fmt.Errorf("只读失败边界工具或错误码不受支持: %q", key)
		}
		if boundary.Count < 1 || boundary.Stopped != (boundary.Count >= 3) || strings.TrimSpace(boundary.LastActionID) == "" {
			return fmt.Errorf("只读失败边界次数、停止标记或动作引用无效: %q", key)
		}
	}
	if state.ReadonlyStopID != "" {
		boundary := ids[state.ReadonlyStopID]
		if boundary == nil || !boundary.Stopped || boundary.Resolved {
			return fmt.Errorf("当前只读停止身份没有对应的未解决停止边界")
		}
	}
	for id, closing := range state.ReadonlyClosings {
		// 同一路径再次失败可以产生新边界；旧收尾材料无需仍在当前边界表中。
		if strings.TrimSpace(id) == "" || closing == nil || strings.TrimSpace(closing.OriginRunID) == "" {
			return fmt.Errorf("只读收尾记录身份或内容无效: %q", id)
		}
		switch closing.Status {
		case "started", "failed":
		case "completed":
			if strings.TrimSpace(closing.Text) == "" {
				return fmt.Errorf("已完成的只读收尾缺少内容: %q", id)
			}
		default:
			return fmt.Errorf("只读收尾状态无效: %q", id)
		}
	}
	return nil
}
