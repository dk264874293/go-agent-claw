package executor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const testImageID = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func fakeDocker(t *testing.T, state string, start func(context.Context, *outputBuffer) error) (*Docker, *[][]string) {
	t.Helper()
	d, _ := NewDocker(Defaults())
	var calls [][]string
	d.command = func(ctx context.Context, args []string, out *outputBuffer) error {
		calls = append(calls, append([]string(nil), args...))
		switch args[0] {
		case "context":
			fmt.Fprint(out, "unix:///测试/docker.sock")
		case "version":
			fmt.Fprint(out, "linux")
		case "image":
			fmt.Fprint(out, testImageID)
		case "create":
			fmt.Fprint(out, "容器ID")
		case "start":
			return start(ctx, out)
		case "inspect":
			fmt.Fprint(out, state)
		case "rm":
			if ctx.Err() != nil {
				t.Error("容器回收继承了取消 context")
			}
		default:
			t.Errorf("非预期 Docker 调用: %v", args)
		}
		return nil
	}
	return d, &calls
}

func TestDockerIsolationOptionsAndErrors(t *testing.T) {
	d, calls := fakeDocker(t, `{"ExitCode":7,"OOMKilled":false,"Running":false}`, func(_ context.Context, out *outputBuffer) error {
		fmt.Fprint(out, "错误输出")
		return errors.New("退出 7")
	})
	result, err := d.Execute(context.Background(), Request{WorkDir: t.TempDir(), Command: "原始命令"})
	if err == nil || !strings.Contains(err.Error(), "exit status 7") || result.Output != "错误输出" {
		t.Fatalf("退出码丢失: %+v %v", result, err)
	}
	var create []string
	for _, call := range *calls {
		if call[0] == "create" {
			create = call
		}
	}
	joined := strings.Join(create, " ")
	for _, flag := range []string{"--network none", "--read-only", "--cap-drop ALL", "--security-opt no-new-privileges", "--cpus 1", "--memory 256m", "--memory-swap 256m", "--pids-limit 64", "--pull=never", "--init", testImageID, "-i PATH=", "HOME=/tmp", "bash -c 原始命令"} {
		if !strings.Contains(joined, flag) {
			t.Errorf("缺少隔离选项: %s", flag)
		}
	}
	if strings.Contains(joined, "bash:5.2") || strings.Contains(joined, "--privileged") || (*calls)[len(*calls)-1][0] != "rm" {
		t.Fatal("未固定镜像或未回收")
	}
	// 即使 start 返回连接错误，仍要读取最终状态识别 OOM。
	d, _ = fakeDocker(t, `{"ExitCode":137,"OOMKilled":true,"Running":false}`, func(context.Context, *outputBuffer) error { return errors.New("退出137") })
	if result, err = d.Execute(context.Background(), Request{WorkDir: t.TempDir(), Command: "测试"}); err == nil || !result.ResourceExhausted {
		t.Fatal("内存上限未报告")
	}
}

func TestDockerCancellationAndNoFallback(t *testing.T) {
	d, calls := fakeDocker(t, `{}`, func(ctx context.Context, out *outputBuffer) error {
		fmt.Fprint(out, strings.Repeat("中", 5000))
		<-ctx.Done()
		return ctx.Err()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	result, err := d.Execute(ctx, Request{WorkDir: t.TempDir(), Command: "测试"})
	if !errors.Is(err, context.DeadlineExceeded) || !result.TimedOut || !result.Truncated || len(result.Output) > 8200 || (*calls)[len(*calls)-1][0] != "rm" {
		t.Fatalf("取消或输出上限错误: %+v %v", result, err)
	}
	d, _ = NewDocker(Defaults())
	d.command = func(_ context.Context, args []string, _ *outputBuffer) error {
		if args[0] != "context" {
			t.Fatal("后端失效后继续执行")
		}
		return ErrUnavailable
	}
	dir := t.TempDir()
	if _, err = d.Execute(context.Background(), Request{WorkDir: dir, Command: "touch forbidden"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("后端不可用未拒绝: %v", err)
	}
	if _, err = os.Stat(filepath.Join(dir, "forbidden")); !os.IsNotExist(err) {
		t.Fatal("回退执行了宿主命令")
	}
}

func TestLocalClientEnvironmentAllowlist(t *testing.T) {
	if os.PathSeparator != '/' {
		t.Skip("此后端仅支持本机 Unix socket")
	}
	dir := t.TempDir()
	// 替代客户端只回显环境中的测试标记；没有实际访问 Docker 或真实密钥。
	program := `#!/bin/sh
printf '%s|%s|%s|%s|%s' "${CLAW_TEST_SECRET-unset}" "${OPENAI_API_KEY-unset}" "${DOCKER_HOST-unset}" "${HTTP_PROXY-unset}" "$LANG"
`
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(program), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	for _, key := range []string{"CLAW_TEST_SECRET", "OPENAI_API_KEY", "DOCKER_HOST", "HTTP_PROXY"} {
		t.Setenv(key, "仅用于测试")
	}
	var out outputBuffer
	if err := localDockerCommand(context.Background(), []string{"测试"}, &out); err != nil {
		t.Fatal(err)
	}
	text, _ := out.snapshot()
	if text != "unset|unset|unset|unset|C" {
		t.Fatalf("宿主环境泄漏: %q", text)
	}
}

func TestReadyRejectsRemoteAndMissingImage(t *testing.T) {
	for _, tc := range []struct{ host, platform, image string }{{"tcp://remote:2375", "linux", testImageID}, {"unix:///tmp/docker.sock", "windows", testImageID}, {"unix:///tmp/docker.sock", "linux", ""}} {
		d, _ := NewDocker(Defaults())
		var calls []string
		d.command = func(_ context.Context, args []string, out *outputBuffer) error {
			calls = append(calls, args[0])
			value := map[string]string{"context": tc.host, "version": tc.platform, "image": tc.image}[args[0]]
			fmt.Fprint(out, value)
			return nil
		}
		if err := d.Ready(context.Background()); !errors.Is(err, ErrUnavailable) {
			t.Fatal("不支持的后端被接受")
		}
		if reflect.DeepEqual(calls, []string{"pull"}) {
			t.Fatal("自动拉取镜像")
		}
	}
}
