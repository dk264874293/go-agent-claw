package taskstore

import "io/fs"

// SavePlanViews 更新任务目录里的可读视图。恢复始终使用事件与检查点。
func (t *Task) SavePlanViews(plan, todo string) error {
	t.store.mu.Lock()
	defer t.store.mu.Unlock()
	if t.log == nil {
		return fs.ErrClosed
	}
	if err := writeAtomic(t.root, "PLAN.md", []byte(plan), true); err != nil {
		return err
	}
	return writeAtomic(t.root, "TODO.md", []byte(todo), true)
}
