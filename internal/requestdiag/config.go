package requestdiag

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ResolveConfig 在入口加载.env后调用；诊断策略不是持久任务属性。
func ResolveConfig(mode, dir string) (Config, error) {
	if mode == "" {
		mode = strings.TrimSpace(os.Getenv("CLAW_REQUEST_DIAGNOSTICS"))
	}
	if mode == "" {
		mode = MetadataOnly
		if strings.EqualFold(strings.TrimSpace(os.Getenv("CLAW_OBSERVE")), "off") {
			mode = Off
		}
	}
	if dir == "" {
		dir = strings.TrimSpace(os.Getenv("CLAW_REQUEST_DIAGNOSTICS_DIR"))
	}
	return (Config{Mode: mode, Dir: dir}).normalized()
}

func (c Config) normalized() (Config, error) {
	if c.Mode == "" {
		c.Mode = MetadataOnly
	}
	if c.Mode != Off && c.Mode != MetadataOnly && c.Mode != OnError {
		return c, errors.New("请求诊断模式必须为 off、metadata 或 on_error")
	}
	if c.Dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return c, err
		}
		c.Dir = filepath.Join(home, ".claw", "request-diagnostics")
	}
	c.Dir = filepath.Clean(c.Dir)
	if c.MaxRequestBytes <= 0 {
		c.MaxRequestBytes = 1 << 20
	}
	if c.MaxMemoryBytes <= 0 {
		c.MaxMemoryBytes = 8 << 20
	}
	if c.MaxAttemptBytes <= 0 {
		c.MaxAttemptBytes = 16 << 20
	}
	if c.MaxDirectoryBytes <= 0 {
		c.MaxDirectoryBytes = 256 << 20
	}
	if c.CloseTimeout <= 0 || c.CloseTimeout > time.Second {
		c.CloseTimeout = time.Second
	}
	if c.QueueSize <= 0 {
		c.QueueSize = 16
	}
	return c, nil
}
