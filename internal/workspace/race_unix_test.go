//go:build linux || darwin

package workspace

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
)

func TestSymlinkReplacementRaceAndFIFO(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	secret := filepath.Join(outside, "sentinel")
	if err := os.WriteFile(secret, []byte("不得改动"), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := syscall.Mkfifo(filepath.Join(dir, "fifo"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Read(context.Background(), "fifo", 10); err == nil {
		t.Fatal("FIFO 未拒绝")
	}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			link := filepath.Join(dir, "link-tmp")
			_ = os.Symlink(outside, link)
			_ = os.Rename(link, filepath.Join(dir, "changing"))
			_ = os.Remove(filepath.Join(dir, "changing"))
			_ = os.Mkdir(filepath.Join(dir, "changing"), 0700)
			_ = os.Remove(filepath.Join(dir, "changing"))
		}
	}()
	for i := 0; i < 100; i++ {
		_ = r.Write(context.Background(), "changing/sentinel", []byte("攻击"))
		data, _, err := r.Read(context.Background(), "changing/sentinel", 32)
		if err == nil && string(data) == "不得改动" {
			t.Error("读操作逃出根目录")
		}
	}
	close(stop)
	wg.Wait()
	data, err := os.ReadFile(secret)
	if err != nil || string(data) != "不得改动" {
		t.Fatal("竞争中越界修改")
	}
}
