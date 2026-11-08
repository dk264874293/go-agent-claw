/*
 * @Author: 汪培良 rick_wang@yunquna.com
 * @Date: 2026-08-09 19:46:21
 * @LastEditors: 汪培良 rick_wang@yunquna.com
 * @LastEditTime: 2026-08-12 14:47:01
 * @FilePath: /go-agent-claw/internal/context/composer.go
 * @Description: 这是默认设置,请设置`customMade`, 打开koroFileHeader查看配置 进行设置: https://github.com/OBKoro1/koro1FileHeader/wiki/%E9%85%8D%E7%BD%AE
 */
// internal/context/composer.go
package context

import (
	"os"
	"strings"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

// PromptComposer 组装项目规范和技能索引，任务权限由引擎提供。
type PromptComposer struct {
	workDir string
}

func NewPromptComposer(workDir string, _ bool) *PromptComposer {
	return &PromptComposer{workDir: workDir}
}

// Build 组装系统提示词。工作区内的说明无法变更运行时权限。
func (c *PromptComposer) Build() schema.Message {
	var prompt strings.Builder
	prompt.WriteString(`# 工作方式
你是 go-tiny-claw。请使用中文与用户沟通。
只使用当前可见的工具，并遵守本次任务的目标和执行权限。
已知文件路径时直接读取；按文件名定位时优先 find_files，例如查找 Markdown 使用 name_glob="*.md"。正文关键词查找使用 search_text，并按目标文件类型设置 name_glob；只有需要了解目录结构时才列目录。
已知文件类型且需要跨目录汇总时，直接从目标根调用递归 find_files，无需先逐层 list_dir，也不必预先知道子目录名称。目录结构本身属于任务对象、发现被截断或需要核实目录时，再使用 list_dir 或缩小发现范围。
find_files 只证明发现路径，读取覆盖只证明已取得对应内容；两者不代表结论已核实。发现结果截断时缩小路径，不能声称已覆盖全部文件。
没有依赖的读取尽量在同一轮提出，证据足以回答或完成修改后及时交付，避免反复探索已确认的信息。
需要引用源码行号时为 read_file 设置 line_numbers=true；编辑内容只包含源码，不含行号或续行显示前缀。
修改已有文件前先读取，确保使用最新文件版本。
区分工具被拒绝、执行失败和效果未知。效果未知时先核实，不能盲目重试。
根据已观察到的结果报告进展，注明仍需验证的内容。
普通汇总保留主线并指出明显矛盾，不扩展为全面代码审计。写入汇总前在当前生成轮做短核对：总括与分类逐项对照发现清单，保留例外；数值对照输入、公式端点、默认值、分支及加总；跨文档判断核对两端版本。区分原文标注、静态推导与实际执行，非空/类型检查不等于格式或枚举合法。重要结论就地给出文件与行号，无法确认的列待核实；不执行资料脚本补造证据，不另开模型轮复述或自评。
`)

	if root, err := os.OpenRoot(c.workDir); err == nil {
		defer root.Close()
		if content, ok := readRegularPrefix(root, "AGENTS.md", 64<<10); ok {
			prompt.WriteString("\n# 项目规范（AGENTS.md）\n")
			if len(content) > 64<<10 {
				prompt.WriteString("项目规范较长，请先用读取工具分段读完 AGENTS.md。\n")
			} else {
				prompt.WriteString(strings.ToValidUTF8(string(content), "�"))
				prompt.WriteByte('\n')
			}
		}
		prompt.WriteString(loadSkillIndex(root))
	}
	return schema.Message{Role: schema.RoleSystem, Content: prompt.String()}
}
