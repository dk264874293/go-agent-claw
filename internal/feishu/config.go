package feishu

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/dk264874293/go-agent-claw/internal/engine"
	"github.com/dk264874293/go-agent-claw/internal/requestdiag"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

// Config 由服务端维护。用户消息不能改变工作区、权限或验收命令。
type Config struct {
	EventLedger string                 `json:"event_ledger"`
	Chats       map[string]ChatBinding `json:"chats"`
}

type ChatBinding struct {
	WorkDir               string                  `json:"work_dir"`
	AllowedUsers          []string                `json:"allowed_users"`
	Approvers             []string                `json:"approvers"`
	Mode                  tools.Mode              `json:"mode"`
	VerifyCommand         string                  `json:"verify_command,omitempty"`
	Analysis              bool                    `json:"analysis,omitempty"`
	Delegation            string                  `json:"delegation,omitempty"`
	ReadPageBytes         int                     `json:"read_page_bytes,omitempty"`
	ReadFeatures          *engine.ReadFeatureSpec `json:"read_features,omitempty"`
	ReadScope             *engine.ReadScopeSpec   `json:"read_scope,omitempty"`
	OutputStyle           string                  `json:"output_style,omitempty"`
	RequestDiagnostics    string                  `json:"request_diagnostics,omitempty"`
	RequestDiagnosticsDir string                  `json:"request_diagnostics_dir,omitempty"`
}

func LoadConfig(path string) (Config, error) {
	var config Config
	data, err := os.ReadFile(path)
	if err != nil {
		return config, fmt.Errorf("读取飞书服务配置失败: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return config, fmt.Errorf("飞书服务配置无效: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return config, errors.New("飞书服务配置包含多余内容")
	}
	return config, config.validate()
}

func (c *Config) validate() error {
	if !filepath.IsAbs(c.EventLedger) || len(c.Chats) == 0 {
		return errors.New("飞书服务配置必须包含绝对事件台账目录和群聊绑定")
	}
	ledger, err := resolveLedgerDirectory(filepath.Clean(c.EventLedger))
	if err != nil {
		return fmt.Errorf("无法定位飞书事件台账目录: %w", err)
	}
	for id, binding := range c.Chats {
		if _, err := requestdiag.ResolveConfig(binding.RequestDiagnostics, binding.RequestDiagnosticsDir); err != nil {
			return fmt.Errorf("群聊 %q 的请求诊断配置无效: %w", id, err)
		}
		if strings.TrimSpace(id) == "" || len(binding.AllowedUsers) == 0 || !filepath.IsAbs(binding.WorkDir) {
			return fmt.Errorf("群聊 %q 缺少成员或绝对工作区路径", id)
		}
		if slices.ContainsFunc(binding.AllowedUsers, blankID) || slices.ContainsFunc(binding.Approvers, blankID) {
			return fmt.Errorf("群聊 %q 包含空的用户 ID", id)
		}
		dir, err := filepath.EvalSymlinks(binding.WorkDir)
		if err != nil {
			return fmt.Errorf("无法定位群聊 %q 的工作区: %w", id, err)
		}
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			return fmt.Errorf("群聊 %q 的工作区不是有效目录", id)
		}
		binding.WorkDir = filepath.Clean(dir)
		for _, pair := range [][2]string{{binding.WorkDir, ledger}, {ledger, binding.WorkDir}} {
			overlaps, err := directoryContains(pair[0], pair[1])
			if err != nil {
				return fmt.Errorf("无法核对群聊 %q 的工作区与事件台账目录: %w", id, err)
			}
			if overlaps {
				return fmt.Errorf("群聊 %q 的工作区与事件台账目录不能重叠", id)
			}
		}
		if binding.Mode == "" {
			binding.Mode = tools.ModeReadOnly
		}
		switch binding.Mode {
		case tools.ModeReadOnly, tools.ModeWorkspace, tools.ModeTrusted:
		default:
			return fmt.Errorf("群聊 %q 的执行模式无效", id)
		}
		if binding.Mode != tools.ModeReadOnly && len(binding.Approvers) == 0 {
			return fmt.Errorf("群聊 %q 的写入模式需要审批人", id)
		}
		if binding.VerifyCommand != "" && binding.Mode != tools.ModeTrusted {
			return fmt.Errorf("群聊 %q 的验收命令需要可信本机模式", id)
		}
		if binding.ReadPageBytes != 0 && binding.ReadPageBytes != 8000 && binding.ReadPageBytes != 16000 {
			return fmt.Errorf("群聊 %q 的读取档位必须为 8000 或 16000 字节", id)
		}
		reading := engine.TaskSpec{ReadScope: binding.ReadScope}
		if err := engine.NormalizeReadScope(&reading); err != nil {
			return fmt.Errorf("群聊 %q 的读取范围无效: %w", id, err)
		}
		binding.ReadScope = reading.ReadScope
		if binding.OutputStyle == "" {
			binding.OutputStyle = "concise"
		}
		if binding.OutputStyle != "concise" && binding.OutputStyle != "detailed" {
			return fmt.Errorf("群聊 %q 的输出篇幅必须为 concise 或 detailed", id)
		}
		if binding.Analysis && (binding.Mode != tools.ModeReadOnly || binding.VerifyCommand != "") {
			return fmt.Errorf("群聊 %q 的分析模式仅支持只读模式且不能设置验收命令", id)
		}
		featureSpec := engine.TaskSpec{Analysis: binding.Analysis, ReadFeatures: binding.ReadFeatures}
		if err := engine.NormalizeReadFeatures(&featureSpec); err != nil {
			return fmt.Errorf("群聊 %q 的读取实验配置无效: %w", id, err)
		}
		binding.ReadFeatures = featureSpec.ReadFeatures
		if binding.Delegation != "" && (binding.Delegation != "auto" && binding.Delegation != "off" && binding.Delegation != "on" || binding.Mode != tools.ModeReadOnly || binding.VerifyCommand != "") {
			return fmt.Errorf("群聊 %q 的委派策略必须是只读回答模式下的 auto、off 或 on", id)
		}
		if binding.Mode == tools.ModeReadOnly && binding.Delegation == "" {
			binding.Delegation = "auto"
		}
		c.Chats[id] = binding
	}
	c.EventLedger = ledger
	return nil
}

func blankID(s string) bool { return strings.TrimSpace(s) == "" }

// resolveLedgerDirectory 从最近存在的祖先开始解析路径，保留尚未创建的后缀。
func resolveLedgerDirectory(dir string) (string, error) {
	if _, err := os.Lstat(dir); err != nil {
		parent := filepath.Dir(dir)
		if !errors.Is(err, os.ErrNotExist) || parent == dir {
			return "", err
		}
		resolved, err := resolveLedgerDirectory(parent)
		if err != nil {
			return "", err
		}
		return filepath.Join(resolved, filepath.Base(dir)), nil
	}
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(realDir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("事件台账路径必须是目录")
	}
	return realDir, nil
}

// directoryContains 比较实际目录身份，也适用于不区分大小写的文件系统。
func directoryContains(root, dir string) (bool, error) {
	ancestor, err := os.Stat(root)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for {
		info, err := os.Stat(dir)
		if err == nil && os.SameFile(ancestor, info) {
			return true, nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false, nil
		}
		dir = parent
	}
}
