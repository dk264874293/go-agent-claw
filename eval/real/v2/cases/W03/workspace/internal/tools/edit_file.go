/*
 * @Author: 汪培良 rick_wang@yunquna.com
 * @Date: 2026-08-09 11:49:34
 * @LastEditors: 汪培良 rick_wang@yunquna.com
 * @LastEditTime: 2026-08-09 13:22:53
 * @FilePath: /go-agent-claw/internal/tools/edit_file.go
 * @Description: 这是默认设置,请设置`customMade`, 打开koroFileHeader查看配置 进行设置: https://github.com/OBKoro1/koro1FileHeader/wiki/%E9%85%8D%E7%BD%AE
 */
// internal/tools/edit_file.go
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

type EditFileTool struct {
	workDir string
}

func NewEditFileTool(workDir string) *EditFileTool {
	return &EditFileTool{workDir: workDir}
}

func (t *EditFileTool) Name() string {
	return "edit_file"
}

func (t *EditFileTool) Definition() schema.ToolDefinition {
	return definition(t.Name(), "替换文件中唯一匹配的文本。先读取文件，再提供返回的版本与足够的匹配上下文。", map[string]any{
		"path":             stringParam("工作区相对路径"),
		"old_text":         stringParam("原有文本，需能唯一定位"),
		"new_text":         stringParam("替换后的文本，空字符串表示删除"),
		"expected_version": stringParam("read_file 返回的 SHA-256 版本"),
	}, "path", "old_text", "new_text", "expected_version")
}

type editFileArgs struct {
	Path            string `json:"path"`
	OldText         string `json:"old_text"`
	NewText         string `json:"new_text"`
	ExpectedVersion string `json:"expected_version"`
}

func parseEdit(ctx context.Context, args json.RawMessage) (input editFileArgs, err error) {
	if err := decodeArgs(args, &input, "path", "old_text", "new_text", "expected_version"); err != nil {
		return input, err
	}
	input.Path, err = writablePath(ctx, input.Path)
	if err != nil {
		return input, err
	}
	if input.OldText == "" {
		return input, reject("参数错误", fmt.Errorf("old_text 不能为空"))
	}
	return input, expectedVersion(input.ExpectedVersion, false)
}

// fuzzyReplace 实现了四级容错降级替换算法
func fuzzyReplace(originalContent, oldText, newText string) (string, error) {
	// L1: 精确匹配
	if replaced, matched, err := replaceUnique(originalContent, oldText, newText); matched || err != nil {
		return replaced, err
	}

	// L2: 换行符归一化 (统一将 \r\n 转换为 \n)
	normalizedContent := strings.ReplaceAll(originalContent, "\r\n", "\n")
	normalizedOld := strings.ReplaceAll(oldText, "\r\n", "\n")

	if replaced, matched, err := replaceUnique(normalizedContent, normalizedOld, newText); matched || err != nil {
		return replaced, err
	}

	// L3: Trim Space 匹配 (忽略首尾的空行和空格)
	trimmedOld := strings.TrimSpace(normalizedOld)
	if trimmedOld != "" {
		if replaced, matched, err := replaceUnique(normalizedContent, trimmedOld, newText); matched || err != nil {
			return replaced, err
		}
	}

	// L4: 逐行去缩进匹配 (最强力的容错：消除大模型遗漏缩进的幻觉)
	return lineByLineReplace(normalizedContent, normalizedOld, newText)
}

func replaceUnique(content, oldText, newText string) (string, bool, error) {
	if oldText == "" {
		return "", false, fmt.Errorf("old_text 不能为空")
	}
	start := strings.Index(content, oldText)
	if start < 0 {
		return "", false, nil
	}
	if strings.Contains(content[start+1:], oldText) {
		return "", false, fmt.Errorf("old_text 匹配到了多处，请提供更多的上下文代码以确保唯一性")
	}
	return content[:start] + newText + content[start+len(oldText):], true, nil
}

// lineByLineReplace 将文本按行切割，去除首尾空白后进行滑动窗口匹配
func lineByLineReplace(content, oldText, newText string) (string, error) {
	contentLines := strings.Split(content, "\n")
	oldLines := strings.Split(strings.TrimSpace(oldText), "\n")

	if len(oldLines) == 0 || len(contentLines) < len(oldLines) {
		return "", fmt.Errorf("找不到该代码片段")
	}

	// 清理 oldLines 的每行首尾空白
	for i := range oldLines {
		oldLines[i] = strings.TrimSpace(oldLines[i])
	}

	matchCount := 0
	matchStartIndex := -1
	matchEndIndex := -1

	// 滑动窗口在原始文件中寻找匹配块
	for i := 0; i <= len(contentLines)-len(oldLines); i++ {
		isMatch := true
		for j := 0; j < len(oldLines); j++ {
			if strings.TrimSpace(contentLines[i+j]) != oldLines[j] {
				isMatch = false
				break
			}
		}

		if isMatch {
			matchCount++
			matchStartIndex = i
			matchEndIndex = i + len(oldLines)
		}
	}

	if matchCount == 0 {
		return "", fmt.Errorf("在文件中未找到 old_text，请大模型先调用 read_file 仔细确认文件内容和缩进")
	}
	if matchCount > 1 {
		return "", fmt.Errorf("模糊匹配到了 %d 处相似代码，请提供更多上下行代码以精确定位", matchCount)
	}

	// 执行替换：将匹配到的原始行范围替换为 newText 拆分后的行
	// (这里简单处理，将 newText 直接作为整体替换进去)
	var newContentLines []string
	newContentLines = append(newContentLines, contentLines[:matchStartIndex]...)
	newContentLines = append(newContentLines, newText) // 插入新内容
	newContentLines = append(newContentLines, contentLines[matchEndIndex:]...)

	return strings.Join(newContentLines, "\n"), nil
}

func (t *EditFileTool) validate(ctx context.Context, args json.RawMessage) error {
	_, err := parseEdit(ctx, args)
	return err
}

func (t *EditFileTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	return mutateFile(ctx, t.workDir, t.Name(), args)
}
