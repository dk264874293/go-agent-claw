package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/engine"
	"github.com/dk264874293/go-agent-claw/internal/feishu"
	"github.com/dk264874293/go-agent-claw/internal/observability"
	"github.com/dk264874293/go-agent-claw/internal/provider"
	"github.com/dk264874293/go-agent-claw/internal/tools"
	"github.com/joho/godotenv"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	"github.com/larksuite/oapi-sdk-go/v3/core/httpserverext"
)

func main() {
	_ = godotenv.Overload()
	if err := serve(); err != nil {
		log.Fatal(err)
	}
}

func serve() error {
	appID, secret := os.Getenv("FEISHU_APP_ID"), os.Getenv("FEISHU_APP_SECRET")
	verifyToken, encryptKey := os.Getenv("FEISHU_VERIFY_TOKEN"), os.Getenv("FEISHU_ENCRYPT_KEY")
	if appID == "" || secret == "" || verifyToken == "" || encryptKey == "" || os.Getenv("OPENAI_API_KEY") == "" {
		return errors.New("请设置 FEISHU_APP_ID、FEISHU_APP_SECRET、FEISHU_VERIFY_TOKEN、FEISHU_ENCRYPT_KEY 和 OPENAI_API_KEY")
	}
	configPath := os.Getenv("FEISHU_AGENTOPS_CONFIG")
	if configPath == "" {
		return errors.New("请用 FEISHU_AGENTOPS_CONFIG 指定群聊配置文件")
	}
	config, err := feishu.LoadConfig(configPath)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	const model = provider.DefaultOpenAIModel
	client := lark.NewClient(appID, secret, lark.WithReqTimeout(8*time.Second))
	llm := provider.NewOpenAIProvider(model)
	bot, err := feishu.NewFeishuBot(ctx, config, client, func(ctx context.Context, session *ctxpkg.Session, spec engine.TaskSpec, reporter engine.Reporter, admit tools.MiddlewareFunc) (engine.RunResult, error) {
		registry := engine.NewWorkspaceRegistry(session.WorkDir)
		registry.Use(admit)
		runner := engine.NewAgentEngine(observability.NewCostTracker(llm, model, session), registry, engine.EngineOptions{Mode: spec.Mode})
		registry.Register(tools.NewSubagentTool(runner, engine.NewExplorerRegistry(session.WorkDir), reporter))
		return runner.Run(ctx, session, spec, reporter)
	})
	if err != nil {
		return err
	}
	defer bot.Close()

	handler := httpserverext.NewEventHandlerFunc(bot.GetEventDispatcher(appID, verifyToken, encryptKey))
	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhook/event", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		handler(w, r)
	})
	address := os.Getenv("FEISHU_LISTEN_ADDR")
	if address == "" {
		address = "127.0.0.1:48080"
	}
	server := &http.Server{Addr: address, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	log.Printf("飞书 Webhook 正在监听 %s", address)
	err = server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
