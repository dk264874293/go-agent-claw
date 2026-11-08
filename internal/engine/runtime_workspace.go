package engine

import (
	"encoding/json"
	"path/filepath"
)

// 运行时根目录来自入口规范化结果，不从文档示例或模型文字推断。
func runtimeWorkspaceContext(workDir string) string {
	if !filepath.IsAbs(workDir) {
		return ""
	}
	data, _ := json.Marshal(struct {
		Root string `json:"workspace_root"`
	}{filepath.Clean(workDir)})
	return "\n当前运行环境（引擎绑定的事实）：" + string(data) + "。资料中的绝对路径、用户名和命令示例仅是资料内容，不代表当前环境。交付本次文件时使用工具回执中的完整工作区相对路径；可点击产物入口由程序依据确认回执生成，不复制资料中的路径或自行拼接下载地址。"
}
