// Package executor 在明确隔离后端中执行命令，不回退宿主机。
package executor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var ErrUnavailable = errors.New("容器隔离后端不可用")

type Request struct {
	WorkDir, Command string
	Network          bool
}
type Result struct {
	Output                                            string
	Truncated, TimedOut, Cancelled, ResourceExhausted bool
}
type Executor interface {
	Execute(context.Context, Request) (Result, error)
}

type Options struct {
	Image                string
	CPUs, MemoryMB, PIDs int
}

func Defaults() Options { return Options{Image: "bash:5.2", CPUs: 1, MemoryMB: 256, PIDs: 64} }
func (o Options) Validate() error {
	if o.Image == "" || strings.ContainsAny(o.Image, " \t\r\n\x00") || strings.HasPrefix(o.Image, "-") || o.CPUs < 1 || o.CPUs > 8 || o.MemoryMB < 64 || o.MemoryMB > 4096 || o.PIDs < 16 || o.PIDs > 512 {
		return fmt.Errorf("容器配置无效")
	}
	return nil
}

type Docker struct {
	options Options
	gate    chan struct{}
	mu      sync.Mutex
	imageID string
	command func(context.Context, []string, *outputBuffer) error
}

func NewDocker(options Options) (*Docker, error) {
	if err := options.Validate(); err != nil {
		return nil, err
	}
	d := &Docker{options: options, gate: make(chan struct{}, 1)}
	d.command = localDockerCommand
	return d, nil
}

func localDockerCommand(ctx context.Context, args []string, output *outputBuffer) error {
	path, err := exec.LookPath("docker")
	if err != nil {
		return fmt.Errorf("%w: 未找到 Docker 客户端", ErrUnavailable)
	}
	cmd := exec.CommandContext(ctx, path, args...)
	// 只保留本机客户端定位配置所需环境；不继承网关密钥或代理变量。
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "LANG=C"}
	cmd.Stdout, cmd.Stderr = output, output
	cmd.WaitDelay = time.Second
	return cmd.Run()
}

func (d *Docker) control(ctx context.Context, args ...string) (string, error) {
	var out outputBuffer
	controlCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	err := d.command(controlCtx, args, &out)
	text, _ := out.snapshot()
	return strings.TrimSpace(text), err
}

// Ready 只接受本机 Linux daemon，并固定本次装配使用的已安装镜像 ID。
func (d *Docker) Ready(ctx context.Context) error {
	select {
	case d.gate <- struct{}{}:
		defer func() { <-d.gate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	d.mu.Lock()
	ready := d.imageID != ""
	d.mu.Unlock()
	if ready {
		return nil
	}
	host, err := d.control(ctx, "context", "inspect", "--format", "{{.Endpoints.docker.Host}}")
	if err != nil || !strings.HasPrefix(host, "unix://") {
		return fmt.Errorf("%w: 需要本机 Unix socket", ErrUnavailable)
	}
	platform, err := d.control(ctx, "version", "--format", "{{.Server.Os}}")
	if err != nil || platform != "linux" {
		return fmt.Errorf("%w: 需要 Linux Docker Engine", ErrUnavailable)
	}
	image, err := d.control(ctx, "image", "inspect", "--format", "{{.Id}}", d.options.Image)
	if err != nil || !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(image) {
		return fmt.Errorf("%w: 镜像未安装，请先安装配置的 Shell 镜像", ErrUnavailable)
	}
	d.mu.Lock()
	d.imageID = image
	d.mu.Unlock()
	return nil
}

func (d *Docker) Execute(ctx context.Context, request Request) (result Result, runErr error) {
	defer func() {
		result.TimedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
		result.Cancelled = errors.Is(ctx.Err(), context.Canceled)
	}()
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if strings.TrimSpace(request.Command) == "" {
		return result, fmt.Errorf("command 不能为空")
	}
	if err := d.Ready(ctx); err != nil {
		return result, err
	}
	workDir, err := filepath.Abs(request.WorkDir)
	if err != nil {
		return result, err
	}
	workDir, err = filepath.EvalSymlinks(workDir)
	if err != nil {
		return result, err
	}
	info, err := os.Stat(workDir)
	if err != nil || !info.IsDir() || strings.ContainsAny(workDir, ",\r\n\x00") {
		return result, fmt.Errorf("容器工作目录无效")
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return result, err
	}
	name := "claw-run-" + hex.EncodeToString(random[:])
	uid, gid := os.Getuid(), os.Getgid()
	if uid <= 0 {
		uid, gid = 65534, 65534
	}
	network := "none"
	if request.Network {
		network = "bridge"
	}
	d.mu.Lock()
	image := d.imageID
	d.mu.Unlock()
	args := []string{"create", "--name", name, "--pull=never", "--label", "go-agent-claw.managed=true", "--user", fmt.Sprintf("%d:%d", uid, gid), "--network", network,
		"--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--cpus", strconv.Itoa(d.options.CPUs),
		"--memory", fmt.Sprintf("%dm", d.options.MemoryMB), "--memory-swap", fmt.Sprintf("%dm", d.options.MemoryMB), "--pids-limit", strconv.Itoa(d.options.PIDs),
		"--ulimit", "nofile=256:256", "--init", "--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=16m",
		"--mount", "type=bind,src=" + workDir + ",dst=/workspace", "--workdir", "/workspace", "--entrypoint", "/usr/bin/env", image,
		"-i", "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "HOME=/tmp", "LANG=C.UTF-8", "bash", "-c", request.Command}
	if _, err := d.control(ctx, args...); err != nil {
		// create 失败也尝试删除本次唯一名称，处理请求已到达 daemon 的不确定情况。
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		cleanupOutput, cleanupErr := d.control(cleanupCtx, "rm", "-f", name)
		if cleanupErr != nil && !strings.Contains(cleanupOutput, "No such container") {
			err = errors.Join(err, fmt.Errorf("回收隔离容器失败: %w", cleanupErr))
		}
		return result, fmt.Errorf("创建隔离容器失败: %w", err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if _, err := d.control(cleanupCtx, "rm", "-f", name); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("回收隔离容器失败: %w", err))
		}
	}()
	var output outputBuffer
	err = d.command(ctx, []string{"start", "-a", name}, &output)
	result.Output, result.Truncated = output.snapshot()
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	startErr := err
	stateJSON, err := d.control(ctx, "inspect", "--format", "{{json .State}}", name)
	if err != nil {
		return result, errors.Join(startErr, fmt.Errorf("查询隔离进程结果失败: %w", err))
	}
	var state struct {
		ExitCode  int
		OOMKilled bool
		Running   bool
	}
	if err := json.Unmarshal([]byte(stateJSON), &state); err != nil || state.Running {
		return result, fmt.Errorf("隔离进程结果无效")
	}
	result.ResourceExhausted = state.OOMKilled
	if state.OOMKilled {
		return result, fmt.Errorf("隔离进程达到内存上限")
	}
	if state.ExitCode != 0 {
		return result, fmt.Errorf("隔离命令执行失败: exit status %d", state.ExitCode)
	}
	if startErr != nil {
		return result, fmt.Errorf("隔离进程连接失败: %w", startErr)
	}
	return result, nil
}

// outputBuffer 持续排空 Docker 输出，只保留固定大小的前缀。
type outputBuffer struct {
	mu        sync.Mutex
	data      []byte
	truncated bool
}

func (o *outputBuffer) Write(data []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	n := min(len(data), 8000-len(o.data))
	o.data = append(o.data, data[:n]...)
	o.truncated = o.truncated || n < len(data)
	return len(data), nil
}
func (o *outputBuffer) snapshot() (string, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	text := strings.ToValidUTF8(string(o.data), "?")
	if o.truncated {
		text += "\n[工具输出超过 8000 字节，已截断]"
	}
	return text, o.truncated
}
