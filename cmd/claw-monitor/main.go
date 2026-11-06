package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/dk264874293/go-agent-claw/internal/monitor"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

type paths []string

func (p *paths) String() string     { return fmt.Sprint([]string(*p)) }
func (p *paths) Set(s string) error { *p = append(*p, s); return nil }
func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, "观测台启动失败：", e)
		os.Exit(1)
	}
}
func run() error {
	var ws, eval paths
	flag.Var(&ws, "workspace", "登记工作区绝对路径，可重复")
	flag.Var(&eval, "eval-root", "登记评测目录，可重复")
	observeDir := flag.String("observe-dir", "", "观测目录，默认 CLAW_OBSERVE_DIR 或 ~/.claw/observations")
	port := flag.Int("port", 0, "本机端口，0 自动分配")
	modelSlow := flag.Duration("model-slow", 60*time.Second, "模型耗时提示阈值")
	toolSlow := flag.Duration("tool-slow", 30*time.Second, "工具耗时提示阈值")
	stale := flag.Duration("stale-after", 10*time.Second, "活动未知提示阈值")
	modelRemaining := flag.Int("model-remaining", 1, "模型预算提示剩余次数")
	toolRemaining := flag.Int("tool-remaining", 2, "工具预算提示剩余次数")
	repeat := flag.Int("repeat-count", 3, "重复探索提示次数")
	flag.Parse()
	if len(ws)+len(eval) == 0 {
		return fmt.Errorf("至少登记一个 -workspace 或 -eval-root")
	}
	if *port < 0 || *port > 65535 || *modelSlow <= 0 || *toolSlow <= 0 || *stale <= 0 || *modelRemaining < 1 || *toolRemaining < 1 || *repeat < 2 {
		return fmt.Errorf("参数无效")
	}
	index, e := monitor.NewIndex(monitor.Config{Workspaces: ws, EvalRoots: eval, ObserveDir: *observeDir, ModelSlow: *modelSlow, ToolSlow: *toolSlow, StaleAfter: *stale, ModelRemaining: *modelRemaining, ToolRemaining: *toolRemaining, RepeatCount: *repeat})
	if e != nil {
		return e
	}
	defer index.Close()
	ln, e := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port))
	if e != nil {
		return e
	}
	defer ln.Close()
	handler := monitor.NewServer(index, ln.Addr().String())
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if index.Refresh() {
					handler.Notify()
				}
			}
		}
	}()
	fmt.Println("本地任务观测台（只读）：", handler.URL())
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(ln) }()
	select {
	case <-ctx.Done():
	case e = <-serveErr:
		stop()
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	server.Shutdown(shutdown)
	<-done
	if e != nil && e != http.ErrServerClosed {
		return e
	}
	return nil
}
