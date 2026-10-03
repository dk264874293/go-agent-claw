package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRootBoundariesAndAtomicWrite(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	secret := filepath.Join(outside, "测试秘密")
	if err := os.WriteFile(secret, []byte("外部原值"), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx := context.Background()
	if err := r.Write(ctx, "nested/file", []byte("新内容")); err != nil {
		t.Fatal(err)
	}
	data, truncated, err := r.Read(ctx, "nested/file", 3)
	if err != nil || !truncated || len(data) != 3 {
		t.Fatalf("读取界限错误: %q %v %v", data, truncated, err)
	}
	for _, name := range []string{"", ".", "../测试秘密", secret, "x/../file", `C:/file`, `x\file`, ".env", ".env.test", ".git/config"} {
		if err := r.Write(ctx, name, []byte("攻击")); err == nil {
			t.Errorf("写路径未拒绝: %q", name)
		}
		if _, _, err := r.Read(ctx, name, 10); err == nil {
			t.Errorf("读路径未拒绝: %q", name)
		}
	}
	if err := os.Symlink(outside, filepath.Join(dir, "parent-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, "file-link")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"parent-link/测试秘密", "file-link"} {
		if err := r.Write(ctx, name, []byte("攻击")); err == nil {
			t.Errorf("写链接未拒绝: %s", name)
		}
		if _, _, err := r.Read(ctx, name, 10); err == nil {
			t.Errorf("读链接未拒绝: %s", name)
		}
	}
	// 原子替换不原地修改硬链接指向的外部文件。
	if err := os.Link(secret, filepath.Join(dir, "hard-link")); err != nil {
		t.Fatal(err)
	}
	if err := r.Write(ctx, "hard-link", []byte("内部新值")); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(secret)
	if err != nil || string(data) != "外部原值" {
		t.Fatal("外部文件被修改")
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if err := r.Write(ctx, "cancelled", nil); err == nil {
		t.Fatal("取消后写入")
	}
	if err := r.Write(context.Background(), "oversize", []byte(strings.Repeat("a", MaxEditBytes+1))); err == nil {
		t.Fatal("超限写入")
	}
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".claw-write-") {
			t.Fatal("临时文件未清理")
		}
	}
}
