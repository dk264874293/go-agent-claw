/*
 * @Author: 汪培良 rick_wang@yunquna.com
 * @Date: 2026-08-09 19:46:30
 * @LastEditors: 汪培良 rick_wang@yunquna.com
 * @LastEditTime: 2026-08-09 19:52:17
 * @FilePath: /go-agent-claw/internal/context/skill.go
 * @Description: 这是默认设置,请设置`customMade`, 打开koroFileHeader查看配置 进行设置: https://github.com/OBKoro1/koro1FileHeader/wiki/%E9%85%8D%E7%BD%AE
 */
// internal/context/skill.go
package context

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"syscall"
)

const skillBase = ".claw/skills"

// readRegularPrefix 只读取工作区内普通文件的前缀。
func readRegularPrefix(root *os.Root, name string, maxBytes int64) ([]byte, bool) {
	// 非阻塞打开可避免普通文件在打开前被替换为管道时挂起。
	file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, false
	}
	content, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	return content, err == nil
}

func loadSkillIndex(root *os.Root) string {
	var index strings.Builder
	count := 0
	_ = fs.WalkDir(root.FS(), skillBase, func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry == nil || !entry.Type().IsRegular() || entry.Name() != "SKILL.md" {
			return nil
		}
		header, ok := readRegularPrefix(root, name, 8<<10)
		if !ok {
			return nil
		}
		title, description := skillMetadata(header)
		if title == "" {
			title = path.Base(path.Dir(name))
		}
		line := fmt.Sprintf("- %q %q。说明文件 %q。\n", title, description, name)
		if index.Len()+len(line) > 16<<10 {
			index.WriteString("其余技能可在工作区的 .claw/skills 目录中查找。\n")
			return fs.SkipAll
		}
		index.WriteString(line)
		count++
		if count == 100 {
			return fs.SkipAll
		}
		return nil
	})
	if count == 0 {
		return ""
	}
	return "\n# 可用技能\n匹配到技能时，用读取工具打开对应说明文件。\n" + index.String()
}

func skillMetadata(header []byte) (name, description string) {
	lines := bytes.Split(header, []byte{'\n'})
	if len(lines) < 2 || !bytes.Equal(bytes.TrimSpace(lines[0]), []byte("---")) {
		return "", ""
	}
	for _, line := range lines[1:] {
		line = bytes.TrimSpace(line)
		if bytes.Equal(line, []byte("---")) {
			return name, description
		}
		key, value, ok := bytes.Cut(line, []byte{':'})
		if !ok {
			continue
		}
		switch string(key) {
		case "name":
			name = strings.TrimSpace(string(value))
		case "description":
			description = strings.TrimSpace(string(value))
		}
	}
	return "", ""
}
