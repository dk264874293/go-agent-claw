package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本测试只使用已安装镜像和本次创建的容器，不发布宿主端口。
func TestDockerAcceptance(t *testing.T) {
	if os.Getenv("CLAW_TEST_DOCKER") != "1" {
		t.Skip("设置 CLAW_TEST_DOCKER=1 验证实际容器")
	}
	d, err := NewDocker(Defaults())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err = d.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err = os.Chmod(dir, 0777); err != nil {
		t.Fatal(err)
	}
	// 检查 daemon 实际采用的参数，而不只检查客户端构造的 argv。
	command := d.command
	var names []string
	d.command = func(ctx context.Context, args []string, out *outputBuffer) error {
		err := command(ctx, args, out)
		if args[0] == "create" && err == nil {
			name := args[2]
			names = append(names, name)
			var inspect outputBuffer
			if e := command(ctx, []string{"inspect", "--format", "{{json .}}", name}, &inspect); e != nil {
				return e
			}
			data, _ := inspect.snapshot()
			var actual struct {
				Image  string
				Mounts []struct {
					Type, Source, Destination string
					RW                        bool
				}
				HostConfig struct {
					ReadonlyRootfs                          bool
					CapDrop, SecurityOpt                    []string
					Tmpfs                                   map[string]string
					NetworkMode                             string
					NanoCPUs, Memory, MemorySwap, PidsLimit int64
				}
			}
			if e := json.Unmarshal([]byte(data), &actual); e != nil {
				return e
			}
			if actual.Image != d.imageID || !actual.HostConfig.ReadonlyRootfs || !strings.Contains(strings.Join(actual.HostConfig.CapDrop, ","), "ALL") || !strings.Contains(strings.Join(actual.HostConfig.SecurityOpt, ","), "no-new-privileges") || actual.HostConfig.NanoCPUs != 1000000000 || actual.HostConfig.Memory != 268435456 || actual.HostConfig.MemorySwap != 268435456 || actual.HostConfig.PidsLimit != 64 {
				return fmt.Errorf("实际容器配置不符")
			}
			if len(actual.Mounts) != 1 || actual.Mounts[0].Type != "bind" || actual.Mounts[0].Destination != "/workspace" || !actual.Mounts[0].RW {
				return fmt.Errorf("存在非预期挂载: %+v", actual.Mounts)
			}
			resolved, _ := filepath.EvalSymlinks(dir)
			if actual.Mounts[0].Source != resolved {
				return fmt.Errorf("工作区挂载不符")
			}
			tmp := actual.HostConfig.Tmpfs["/tmp"]
			for _, flag := range []string{"nosuid", "nodev", "noexec", "size=16m"} {
				if !strings.Contains(tmp, flag) {
					return fmt.Errorf("tmpfs 缺少 %s: %s", flag, tmp)
				}
			}
			t.Logf("容器=%s image=%s network=%s tmpfs=%s", name, actual.Image, actual.HostConfig.NetworkMode, tmp)
		}
		return err
	}
	t.Run("权限与挂载", func(t *testing.T) {
		result, err := d.Execute(ctx, Request{WorkDir: dir, Command: `set -eu
test "$(id -u)" -ne 0
grep -Eq '^CapEff:[[:space:]]+0000000000000000$' /proc/self/status
grep -Eq '^NoNewPrivs:[[:space:]]+1$' /proc/self/status
grep -Eq '^[^ ]+ / [^ ]+ ro(,| )' /proc/mounts
test ! -e /var/run/docker.sock
test ! -e /run/docker.sock
grep -Eq ' /tmp .*nosuid.*nodev.*noexec.*size=16384k' /proc/mounts
printf '安全检查通过'`})
		if err != nil || result.Output != "安全检查通过" {
			t.Fatal(result, err)
		}
	})
	t.Run("受控网络对照", func(t *testing.T) {
		// 服务仅含合成响应，同样使用无特权、只读根目录及有限配额。
		service, err := d.control(ctx, "run", "-d", "--pull=never", "--network", "bridge", "--read-only", "--user", "65534:65534", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--memory", "64m", "--pids-limit", "16", "--cpus", "1", "--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=1m", "--entrypoint", "/usr/bin/env", d.imageID, "-i", "PATH=/usr/local/bin:/usr/bin:/bin", "bash", "-c", `exec busybox nc -lk -p 8080 -e /bin/cat`)
		if err != nil {
			t.Fatal(service, err)
		}
		defer func() {
			cleanup, stop := context.WithTimeout(context.Background(), 3*time.Second)
			defer stop()
			if _, e := d.control(cleanup, "rm", "-f", service); e != nil {
				t.Error(e)
			}
		}()
		ip, err := d.control(ctx, "inspect", "--format", "{{.NetworkSettings.Networks.bridge.IPAddress}}", service)
		if err != nil || ip == "" {
			t.Fatal(ip, err)
		}
		request := Request{WorkDir: dir, Network: true, Command: "printf '受控服务标记' | busybox nc -w 1 " + ip + " 8080"}
		result, err := d.Execute(ctx, request)
		if err != nil || result.Output != "受控服务标记" {
			t.Fatalf("对照服务不可达: %+v %v", result, err)
		}
		request.Network = false
		if result, err = d.Execute(ctx, request); err == nil || strings.Contains(result.Output, "受控服务标记") {
			t.Fatal("none 模式仍访问了服务", result, err)
		}
		t.Logf("服务=%s ip=%s；bridge 成功，none 拒绝", service, ip)
	})
	t.Run("正常结束回收后台进程", func(t *testing.T) {
		result, err := d.Execute(ctx, Request{WorkDir: dir, Command: `(while :; do printf x >>background.txt; sleep 0.05; done) & sleep 0.2; printf '主进程结束'`})
		if err != nil || !strings.Contains(result.Output, "主进程结束") {
			t.Fatal(result, err)
		}
		before, err := os.ReadFile(filepath.Join(dir, "background.txt"))
		if err != nil || len(before) == 0 {
			t.Fatal("后台进程没有实际运行", err)
		}
		time.Sleep(350 * time.Millisecond)
		after, err := os.ReadFile(filepath.Join(dir, "background.txt"))
		if err != nil || string(before) != string(after) {
			t.Fatal("后台写入在返回后继续", err)
		}
	})
	t.Run("PID耗尽与取消后恢复", func(t *testing.T) {
		short, stop := context.WithTimeout(ctx, 4*time.Second)
		start := time.Now()
		result, err := d.Execute(short, Request{WorkDir: dir, Command: `for ((n=0;n<80;n++)); do sleep 60 & done; wait`})
		stop()
		elapsed := time.Since(start)
		if !strings.Contains(result.Output, "Resource temporarily unavailable") || err == nil {
			t.Fatalf("PID 上限未实际触发: %+v %v", result, err)
		}
		if errors.Is(err, context.DeadlineExceeded) && !result.TimedOut {
			t.Fatal("超时标志丢失")
		}
		if elapsed > 9*time.Second {
			t.Fatal("取消回收超过 5 秒目标", elapsed)
		}
		t.Logf("PID 耗尽输出=%q；耗时=%s", result.Output, elapsed)
		result, err = d.Execute(ctx, Request{WorkDir: dir, Command: `printf '恢复成功'`})
		if err != nil || result.Output != "恢复成功" {
			t.Fatal(result, err)
		}
	})
	t.Run("主动取消回收时间", func(t *testing.T) {
		active, stop := context.WithCancel(ctx)
		defer stop()
		type completed struct {
			result Result
			err    error
		}
		done := make(chan completed, 1)
		go func() {
			result, err := d.Execute(active, Request{WorkDir: dir, Command: `printf ready >running; sleep 100 & wait`})
			done <- completed{result, err}
		}()
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, err := os.Stat(filepath.Join(dir, "running")); err == nil {
				break
			}
			if time.Now().After(deadline) {
				stop()
				<-done
				t.Fatal("命令未实际开始")
			}
			time.Sleep(10 * time.Millisecond)
		}
		start := time.Now()
		stop()
		select {
		case value := <-done:
			if !errors.Is(value.err, context.Canceled) || !value.result.Cancelled || value.result.TimedOut {
				t.Fatal(value.result, value.err)
			}
			t.Logf("主动取消至回收返回=%s", time.Since(start))
		case <-time.After(5 * time.Second):
			<-done
			t.Fatal("主动取消回收超过 5 秒")
		}
		if _, err := d.Execute(ctx, Request{WorkDir: dir, Command: "true"}); err != nil {
			t.Fatal("取消后执行失败", err)
		}
	})
	for _, name := range names {
		if _, err := d.control(ctx, "inspect", name); err == nil {
			t.Errorf("本次容器未删除: %s", name)
		}
	}
}
