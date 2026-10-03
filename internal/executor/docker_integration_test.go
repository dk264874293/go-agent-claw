package executor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDockerIntegration 只在显式选择时使用本机 daemon，不调用 LLM。
func TestDockerIntegration(t *testing.T) {
	if os.Getenv("CLAW_TEST_DOCKER") != "1" {
		t.Skip("设置 CLAW_TEST_DOCKER=1 验证容器后端")
	}
	d, err := NewDocker(Defaults())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := d.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0777); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "测试秘密")
	if err := os.WriteFile(outside, []byte("测试宿主秘密"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAW_TEST_HOST_SECRET", "测试环境秘密")
	quoted, _ := json.Marshal(outside)
	command := `set -eu
test "$(id -u)" -ne 0
test "${CLAW_TEST_HOST_SECRET-unset}" = unset
test "${OPENAI_API_KEY-unset}" = unset
test "${ZHIPU_API_KEY-unset}" = unset
test ! -e ` + string(quoted) + `
if touch /outside-test 2>/dev/null; then exit 30; fi
test "$(cat /sys/fs/cgroup/memory.max)" = 268435456
test "$(cat /sys/fs/cgroup/pids.max)" = 64
test "$(cat /sys/fs/cgroup/cpu.max)" = '100000 100000'
if wget -T 2 -q -O /tmp/net http://1.1.1.1 2>/dev/null; then exit 31; fi
printf '容器写入成功' > result.txt
printf '隔离检查通过'
`
	result, err := d.Execute(ctx, Request{WorkDir: dir, Command: command})
	if err != nil || !strings.Contains(result.Output, "隔离检查通过") {
		t.Fatalf("隔离检查失败: %+v %v", result, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "result.txt"))
	if err != nil || string(data) != "容器写入成功" {
		t.Fatal("工作区写入失败")
	}
	data, _ = os.ReadFile(outside)
	if string(data) != "测试宿主秘密" {
		t.Fatal("宿主文件被改变")
	}
	result, err = d.Execute(ctx, Request{WorkDir: dir, Command: `printf '错误输出' >&2; exit 7`})
	if err == nil || !strings.Contains(err.Error(), "exit status 7") || !strings.Contains(result.Output, "错误输出") {
		t.Fatalf("非零退出失效: %+v %v", result, err)
	}
	// 内存耗尽发生在受限容器内，OOM 结果必须可见且容器仍被回收。
	oomCtx, oomCancel := context.WithTimeout(ctx, 20*time.Second)
	result, err = d.Execute(oomCtx, Request{WorkDir: dir, Command: `value=x; for ((n=0;n<30;n++)); do value=$value$value; done`})
	oomCancel()
	if err == nil || !result.ResourceExhausted {
		t.Fatalf("实际内存配额未生效: %+v %v", result, err)
	}
	result, err = d.Execute(ctx, Request{WorkDir: dir, Command: `head -c 100000 /dev/zero | tr '\0' x`})
	if err != nil || !result.Truncated || len(result.Output) > 8200 {
		t.Fatalf("大量输出失败: %+v %v", result, err)
	}
	short, stop := context.WithTimeout(ctx, 2*time.Second)
	result, err = d.Execute(short, Request{WorkDir: dir, Command: `sleep 100 & wait`})
	stop()
	if !errors.Is(err, context.DeadlineExceeded) || !result.TimedOut {
		t.Fatalf("容器未取消: %+v %v", result, err)
	}
	remaining, err := d.control(ctx, "ps", "-aq", "--filter", "label=go-agent-claw.managed=true")
	if err != nil || remaining != "" {
		t.Fatalf("容器未回收: %q %v", remaining, err)
	}
}
