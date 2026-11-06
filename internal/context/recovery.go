/*
 * @Author: 汪培良 rick_wang@yunquna.com
 * @Date: 2026-08-12 20:55:53
 * @LastEditors: 汪培良 rick_wang@yunquna.com
 * @LastEditTime: 2026-08-12 20:55:58
 * @FilePath: /go-agent-claw/internal/context/recovery.go
 * @Description: 这是默认设置,请设置`customMade`, 打开koroFileHeader查看配置 进行设置: https://github.com/OBKoro1/koro1FileHeader/wiki/%E9%85%8D%E7%BD%AE
 */
// internal/context/recovery.go
package context

import (
	"fmt"
	"strings"
)

// RecoveryManager 负责在工具执行失败时，根据报错特征分析并注入恢复建议
type RecoveryManager struct{}

func NewRecoveryManager() *RecoveryManager {
	return &RecoveryManager{}
}

// AnalyzeAndInject 接收原始报错，匹配已知特征模式，返回增强后的报错信息
func (rm *RecoveryManager) AnalyzeAndInject(toolName string, rawError string) string {
	var hint string

	// 我们使用相对稳定的英文系统级报错关键字，或者我们自己手写的工具内部固定报错格式
	lowerError := strings.ToLower(rawError)

	switch toolName {
	case "list_dir":
		if strings.Contains(rawError, "路径不是目录") || strings.Contains(lowerError, "not a directory") {
			hint = "list_dir 只接受目录，列表中带 / 后缀的是目录；当前路径是文件或其上级路径不是目录，同参数重试不能解决。请跳过该目标继续处理已知文档，或按任务需要使用 read_file，不要重复列目录。"
		}

	case "read_evidence":
		if strings.Contains(rawError, "证据读取范围无效") || strings.Contains(rawError, "UTF-8") || strings.Contains(rawError, "材料偏移量") {
			hint = "证据按源字节分页：offset 必须非负，limit 为 1 至 4000（默认 4000）。请修正范围或字符边界，继续读取使用上一页返回的下一段 offset；不要对不同材料重复提交同类非法范围。"
		}

	case "edit_file":
		// 匹配我们在 07 讲中手写的 fuzzyReplace 的固定报错抛出
		if strings.Contains(rawError, "在文件中未找到 old_text") || strings.Contains(rawError, "找不到该代码片段") {
			hint = "你提供的 old_text 与文件当前内容不一致，或者缺少必要的缩进。请先使用 `read_file` 工具重新读取该文件，获取最新、准确的内容后，再重新发起编辑。"
		} else if strings.Contains(rawError, "匹配到了多处") || strings.Contains(rawError, "提供更多上下文") {
			hint = "你的 old_text 不够具体，命中了多个相同代码块。请在 old_text 中增加上下相邻的几行代码，以确保替换的唯一性。"
		}

	case "read_file", "write_file":
		// 匹配 Go 原生 os 包抛出的 POSIX 标准错误
		if strings.Contains(lowerError, "no such file or directory") {
			hint = "路径似乎不正确。请不要凭空猜测，先使用 `list_dir` 或 `search_text` 查找正确的目录结构和文件名。"
		} else if strings.Contains(lowerError, "permission denied") {
			hint = "你没有权限操作该文件。请检查工作区限制，或者思考是否需要修改其他文件。"
		}

	case "bash":
		if strings.Contains(lowerError, "command not found") {
			hint = "系统中未安装该命令。请先思考：是否有替代命令？或者你需要先编写脚本进行安装？"
		} else if strings.Contains(rawError, "超时") || strings.Contains(rawError, "DeadlineExceeded") {
			// 匹配我们手写的 30s context.WithTimeout 报错
			hint = "命令已超时。先核实已经产生的效果，确认状态后再决定后续动作。"
		} else if strings.Contains(lowerError, "syntax error") {
			hint = "Bash 语法错误。请检查引号转义或特殊字符，确保命令在终端中可直接运行。"
		}
	}

	// 如果没有匹配到特定特征，原样返回原始错误；
	// 如果匹配到了，拼接成强有力的、带有浓厚“系统指导意味”的行动指南。
	if hint == "" {
		return rawError
	}

	return fmt.Sprintf("%s\n\n[系统救援指南]: %s", rawError, hint)
}
