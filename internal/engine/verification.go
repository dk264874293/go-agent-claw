package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"syscall"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

type fileVersion struct {
	Path    string `json:"path"`
	Version string `json:"version"`
}

// 工作区摘要包含未跟踪的源文件。运行元数据和 Git 内部状态不参与验收。
func WorkspaceVersion(ctx context.Context, workDir string) (string, error) {
	root, err := os.OpenRoot(workDir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	h := sha256.New()
	err = fs.WalkDir(root.FS(), ".", func(name string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		base := path.Base(name)
		if strings.EqualFold(base, ".claw") || strings.EqualFold(base, ".git") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			link, err := root.Readlink(name)
			if err != nil {
				return err
			}
			fmt.Fprintf(h, "%q:链接:%q\n", name, link)
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			return err
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("验收时文件类型发生变化: %s", name)
		}
		digest := sha256.New()
		buffer := make([]byte, 32*1024)
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			n, err := f.Read(buffer)
			if n > 0 {
				digest.Write(buffer[:n])
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}
		}
		fmt.Fprintf(h, "%q:%o:%x\n", name, info.Mode().Perm(), digest.Sum(nil))
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (e *AgentEngine) verify(ctx context.Context, r *runState, reporter Reporter) (*Verification, error) {
	before, err := WorkspaceVersion(ctx, r.state.WorkDir)
	if err != nil {
		return nil, fmt.Errorf("无法获取验收前的工作区版本: %w", err)
	}
	args, err := json.Marshal(map[string]string{"command": r.state.Spec.VerifyCommand})
	if err != nil {
		return nil, err
	}
	call := schema.ToolCall{ID: "verify-" + newID(), Name: "bash", Arguments: args}
	result, err := e.execute(ctx, r, e.registry, reporter, call, "", true)
	if err != nil {
		return nil, err
	}
	if result.Status == schema.ToolRejected {
		return nil, fmt.Errorf("%w：指定验收命令未获准执行", errBlocked)
	}
	after, err := WorkspaceVersion(ctx, r.state.WorkDir)
	if err != nil {
		return nil, fmt.Errorf("无法获取验收后的工作区版本: %w", err)
	}
	out := observation(result)
	if before != after {
		out += "\n验收前后工作区版本不同，本次验收结果失效。"
	}
	return &Verification{
		Passed:  result.Status == schema.ToolSucceeded && before == after,
		Version: after,
		Output:  truncateUTF8(out, 6000),
	}, nil
}

func truncateUTF8(value string, n int) string {
	value = strings.ToValidUTF8(value, "�")
	if len(value) <= n {
		return value
	}
	for i := n; i > 0; i-- {
		if value[i]&0xc0 != 0x80 {
			return value[:i] + "…"
		}
	}
	return "…"
}
