//go:build linux || darwin

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

func TestShellLargeSuccessAndFailure(t *testing.T) {
	r := NewRegistry()
	r.Register(NewBashTool(t.TempDir()))
	for _, code := range []int{0, 7} {
		args, _ := json.Marshal(map[string]string{"command": fmt.Sprintf("i=0; while [ $i -lt 5000 ]; do printf '输出很多字符'; i=$((i+1)); done; exit %d", code)})
		result := r.Execute(context.Background(), schema.ToolCall{ID: "shell", Name: "bash", Arguments: args})
		if result.IsError != (code != 0) || !result.Truncated || len(result.Output) > maxOutputBytes+100 {
			t.Fatalf("大输出结果不正确: bytes=%d meta=%+v error=%v", len(result.Output), result.ToolMetadata, result.IsError)
		}
		if code != 0 && !strings.Contains(result.Output, "exit status 7") {
			t.Fatal("截断丢失退出原因")
		}
	}
}

func TestShellCancellationKillsProcessGroup(t *testing.T) {
	r := NewRegistry()
	r.Register(NewBashTool(t.TempDir()))
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	result := r.Execute(ctx, schema.ToolCall{ID: "kill", Name: "bash", Arguments: json.RawMessage(`{"command":"sleep 30 & printf 'CHILD=%s\\n' $!; wait"}`)})
	if time.Since(start) > 2*time.Second || !result.IsError || !result.TimedOut {
		t.Fatalf("未及时取消进程组: %+v", result)
	}
	assertChildStopped(t, result.Output)
}

func TestShellExitCleansBackgroundChild(t *testing.T) {
	tool := NewBashTool(t.TempDir())
	start := time.Now()
	output, _ := tool.Execute(context.Background(), json.RawMessage(`{"command":"sleep 30 >/dev/null 2>&1 & printf 'CHILD=%s\\n' $!"}`))
	if time.Since(start) > 2*time.Second {
		t.Fatal("正常退出未及时清理后台进程")
	}
	assertChildStopped(t, output)
}

func TestShellExitClosesInheritedPipe(t *testing.T) {
	tool := NewBashTool(t.TempDir())
	start := time.Now()
	output, err := tool.Execute(context.Background(), json.RawMessage(`{"command":"sleep 30 & printf 'CHILD=%s\\n' $!"}`))
	if time.Since(start) > 2*time.Second {
		t.Fatal("后台进程继承的输出管道阻塞了退出")
	}
	if err == nil {
		t.Fatal("输出管道未正常关闭应报告执行失败")
	}
	assertChildStopped(t, output)
}

func assertChildStopped(t *testing.T, output string) {
	t.Helper()
	idx := strings.Index(output, "CHILD=")
	if idx < 0 {
		t.Fatalf("未取得子进程 PID: %q", output)
	}
	pidText := strings.Fields(output[idx+len("CHILD="):])[0]
	pid, err := strconv.Atoi(pidText)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Kill(pid, syscall.SIGKILL)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
			return
		}
		// Linux 的孤儿僵尸等待 init 回收，不再运行也不持有输出管道。
		state, _ := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
		if strings.HasPrefix(strings.TrimSpace(string(state)), "Z") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("子进程仍在运行: %d", pid)
}

func TestReadFileRejectsFIFOWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe"), 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := NewReadFileTool(dir).Execute(context.Background(), json.RawMessage(`{"path":"pipe"}`))
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("FIFO 应被拒绝")
		}
	case <-time.After(time.Second):
		t.Fatal("读取 FIFO 阻塞")
	}
}
