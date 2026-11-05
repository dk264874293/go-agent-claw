package observability

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/provider"
	"github.com/dk264874293/go-agent-claw/internal/schema"
)

type traceKey struct{}

// Span 记录一次操作的耗时及其子跨度。
type Span struct {
	Name       string         `json:"name"`
	StartTime  time.Time      `json:"start_time"`
	EndTime    time.Time      `json:"end_time"`
	DurationMs int64          `json:"duration_ms"`
	Attributes map[string]any `json:"attributes,omitempty"`
	Children   []*Span        `json:"children,omitempty"`
	mu         sync.Mutex
}

func StartSpan(ctx context.Context, name string) (context.Context, *Span) {
	span := &Span{Name: name, StartTime: time.Now(), Attributes: make(map[string]any)}
	if parent, ok := ctx.Value(traceKey{}).(*Span); ok {
		parent.mu.Lock()
		parent.Children = append(parent.Children, span)
		parent.mu.Unlock()
	}
	return context.WithValue(ctx, traceKey{}, span), span
}

func (s *Span) EndSpan() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.EndTime = time.Now()
	s.DurationMs = s.EndTime.Sub(s.StartTime).Milliseconds()
}

func (s *Span) AddAttribute(key string, value any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Attributes[key] = value
}

func traceIdentifier(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func traceAttribute(key string, value any) bool {
	switch strings.ReplaceAll(strings.ToLower(key), "_", "") {
	case "runid", "taskid", "stepid", "actionid", "toolcallid", "toolname", "status", "effect",
		"effectstatus", "errorcode", "stopreason", "stopcode", "mode":
		name := reflect.ValueOf(value)
		return name.Kind() == reflect.String && traceIdentifier(name.String())
	case "errorkind":
		// 错误类别是程序提供的短标签，不输出详细错误内容。
		kind, ok := value.(string)
		return ok && len(kind) <= 32 && !strings.ContainsAny(kind, "\r\n:\\/")
	case "interactive", "contextmessagecount", "contextbytes", "attempt", "turn", "toolcount",
		"modelcalls", "llmcalls", "toolcalls", "turns", "durationms", "prompttokens", "completiontokens", "costcny", "costknown",
		"tokensknown", "exitcode", "iserror", "elapsedms":
		switch value.(type) {
		case bool, int, int64, float64:
			return true
		}
	}
	return false
}

func (s *Span) snapshot() *Span {
	s.mu.Lock()
	copySpan := &Span{
		Name: s.Name, StartTime: s.StartTime, EndTime: s.EndTime,
		DurationMs: s.DurationMs, Attributes: make(map[string]any),
	}
	for key, value := range s.Attributes {
		if traceAttribute(key, value) {
			copySpan.Attributes[key] = value
		}
	}
	children := append([]*Span(nil), s.Children...)
	s.mu.Unlock()

	for _, child := range children {
		copySpan.Children = append(copySpan.Children, child.snapshot())
	}
	return copySpan
}

func traceNonce() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes[:]), nil
}

// ExportTraceToFile 在工作区内保存仅含运行元数据的追踪文件，不覆盖现有文件。
func ExportTraceToFile(rootSpan *Span, workDir string, runID string) error {
	if rootSpan == nil {
		return errors.New("追踪跨度为空")
	}
	if !traceIdentifier(runID) {
		var err error
		if runID, err = traceNonce(); err != nil {
			return err
		}
	}

	snapshot := rootSpan.snapshot()
	snapshot.Attributes["RunID"] = runID
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("编码追踪失败: %w", err)
	}

	root, err := os.OpenRoot(workDir)
	if err != nil {
		return fmt.Errorf("打开追踪工作区失败: %w", err)
	}
	defer root.Close()
	dir := filepath.Join(".claw", "traces")
	if err := root.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("创建追踪目录失败: %w", err)
	}
	path := filepath.Join(dir, "trace_"+runID+".json")
	file, err := root.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		nonce, idErr := traceNonce()
		if idErr != nil {
			return idErr
		}
		path = filepath.Join(dir, "trace_"+runID+"_"+nonce+".json")
		file, err = root.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	}
	if err != nil {
		return fmt.Errorf("创建追踪文件失败: %w", err)
	}
	_, writeErr := file.Write(data)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return fmt.Errorf("写入追踪文件失败: %w", errors.Join(err, root.Remove(path)))
	}
	return nil
}

// ModelPrice 是用户配置的人民币每百万 token 单价，缺少字段时视为未知。
type ModelPrice struct {
	Currency         string   `json:"currency"`
	InputPerMillion  *float64 `json:"input_per_million"`
	OutputPerMillion *float64 `json:"output_per_million"`
}

type CostTracker struct {
	nextProvider provider.LLMProvider
	price        ModelPrice
	session      *ctxpkg.Session
}

// NewCostTracker 按 CLAW_MODEL_PRICES 中的显式单价统计用量与费用。
func NewCostTracker(next provider.LLMProvider, modelName string, session *ctxpkg.Session) *CostTracker {
	var prices map[string]ModelPrice
	if raw := os.Getenv("CLAW_MODEL_PRICES"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &prices); err != nil {
			log.Print("[计费] CLAW_MODEL_PRICES 格式无效，费用将标为未知")
			prices = nil
		}
	}
	return &CostTracker{nextProvider: next, price: prices[modelName], session: session}
}

func (t *CostTracker) Generate(ctx context.Context, msgs []schema.Message, availableTools []schema.ToolDefinition) (*schema.Message, error) {
	start := time.Now()
	response, err := t.nextProvider.Generate(ctx, msgs, availableTools)
	var usage *schema.Usage
	if response != nil {
		usage = response.Usage
	}
	if usage != nil && (usage.PromptTokens < 0 || usage.CompletionTokens < 0) {
		usage = nil
	}
	var cost *float64
	price := t.price
	if usage != nil && price.Currency == "CNY" && price.InputPerMillion != nil && price.OutputPerMillion != nil &&
		*price.InputPerMillion >= 0 && *price.OutputPerMillion >= 0 {
		amount := float64(usage.PromptTokens)*(*price.InputPerMillion/1_000_000) +
			float64(usage.CompletionTokens)*(*price.OutputPerMillion/1_000_000)
		if !math.IsInf(amount, 0) && !math.IsNaN(amount) {
			cost = &amount
		}
	}
	if t.session != nil {
		t.session.RecordUsageReport(usage, cost)
	}
	tokensText := "未知"
	if usage != nil {
		tokensText = fmt.Sprintf("输入 %d / 输出 %d", usage.PromptTokens, usage.CompletionTokens)
	}
	costText := "未知"
	if cost != nil {
		costText = fmt.Sprintf("¥%.6f", *cost)
	}
	status := "完成"
	if err != nil {
		status = "失败"
	}
	log.Printf("[计费] 调用%s | 耗时 %s | token %s | 费用 %s", status, time.Since(start).Round(time.Millisecond), tokensText, costText)
	return response, err
}
