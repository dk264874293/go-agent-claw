package engine

import (
	"context"
	"encoding/json"
)

// discoveryContext 只投影当前Agent实际取得的清单，不重新扫描目录或复述文件列表。
func (r *runState) discoveryContext(ctx context.Context) string {
	if r.state.Spec.ReadContextVersion < 2 {
		return ""
	}
	type item struct {
		ID         string `json:"manifest_id"`
		Path       string `json:"path"`
		Glob       string `json:"name_glob"`
		Files      int    `json:"files"`
		Complete   bool   `json:"discovery_complete"`
		Unreadable int    `json:"unreadable,omitempty"`
	}
	var items []item
	omitted := 0
	for _, d := range r.latestDiscoveries(ctx) {
		next := item{d.ManifestID, d.Path, d.NameGlob, len(d.Files), !d.Truncated && d.Unreadable == 0, d.Unreadable}
		candidate, _ := json.Marshal(append(append([]item(nil), items...), next))
		if len(items) >= 6 || len(candidate) > 2*1024 {
			omitted++
			continue
		}
		items = append(items, next)
	}
	if len(items) == 0 && omitted == 0 {
		return ""
	}
	b, _ := json.Marshal(struct {
		Items   []item `json:"items"`
		Omitted int    `json:"omitted,omitempty"`
	}{items, omitted})
	return "\n已有发现清单（动作当时的选择条件，不保证当前目录成员仍未变化）：" + string(b) + "。同一范围的路径清单可继续利用；只有核对新增/删除成员、纠正选择条件或处理截断时才刷新find_files。目录结构本身需要检查时仍可list_dir，不用为已知文件逐层列目录。"
}
