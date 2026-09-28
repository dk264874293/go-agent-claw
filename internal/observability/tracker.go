/*
 * @Author: 汪培良 rick_wang@yunquna.com
 * @Date: 2026-08-14 12:35:47
 * @LastEditors: 汪培良 rick_wang@yunquna.com
 * @LastEditTime: 2026-08-14 16:28:07
 * @FilePath: /go-agent-claw/internal/observability/tracker.go
 * @Description: 这是默认设置,请设置`customMade`, 打开koroFileHeader查看配置 进行设置: https://github.com/OBKoro1/koro1FileHeader/wiki/%E9%85%8D%E7%BD%AE
 */
// internal/observability/tracker.go
package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/dk264874293/go-agent-claw/internal/budget"
	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/provider"
	"github.com/dk264874293/go-agent-claw/internal/schema"
)

// traceKey 是 Context 中存放 Span 的专属 Key
type traceKey struct{}

// Span 代表链路追踪中的一个时间跨度和操作节点
type Span struct {
	Name       string                 `json:"name"`
	StartTime  time.Time              `json:"start_time"`
	EndTime    time.Time              `json:"end_time"`
	DurationMs int64                  `json:"duration_ms"`
	Attributes map[string]interface{} `json:"attributes,omitempty"` // 存放元数据 (如消耗的 Token, 执行的命令)
	Children   []*Span                `json:"children,omitempty"`   // 子跨度
	mu         sync.Mutex             // 保护 Children 的并发写入
}

// StartSpan 开启一个新的追踪跨度，并将其级联到 Context 中
func StartSpan(ctx context.Context, name string) (context.Context, *Span) {
	span := &Span{
		Name:       name,
		StartTime:  time.Now(),
		Attributes: make(map[string]interface{}),
	}
	// 从 context 中尝试获取父 Span
	if parent, ok := ctx.Value(traceKey{}).(*Span); ok {
		parent.mu.Lock()
		parent.Children = append(parent.Children, span)
		parent.mu.Unlock()
	}
	// 将当前新创建的 Span 作为最新的父节点，塞入衍生 Context 并返回
	newCtx := context.WithValue(ctx, traceKey{}, span)
	return newCtx, span
}

// EndSpan 结束跨度，计算耗时
func (s *Span) EndSpan() {
	s.EndTime = time.Now()
	s.DurationMs = s.EndTime.Sub(s.StartTime).Milliseconds()
}

// AddAttribute 为当前 Span 记录关键的元数据
func (s *Span) AddAttribute(key string, value interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Attributes[key] = value
}

// ExportTraceToFile 当整个根 Span 结束时，将其序列化并保存为本地 JSON 文件
func ExportTraceToFile(rootSpan *Span, workDir string, runID string) (string, error) {
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(runID) {
		return "", fmt.Errorf("RunID 无效")
	}
	traceDir := filepath.Join(workDir, ".claw", "traces")
	if err := os.MkdirAll(traceDir, 0700); err != nil {
		return "", fmt.Errorf("创建 Trace 目录失败: %w", err)
	}
	filename := filepath.Join(traceDir, fmt.Sprintf("trace_%s.json", runID))
	// 美化输出 JSON，便于人类和工具阅读
	data, err := json.MarshalIndent(rootSpan, "", "  ")
	if err != nil {
		return "", err
	}
	file, err := os.OpenFile(filename, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", fmt.Errorf("创建 Trace 文件失败: %w", err)
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil {
		return "", fmt.Errorf("写入 Trace 失败: %w", writeErr)
	}
	if closeErr != nil {
		return "", fmt.Errorf("关闭 Trace 失败: %w", closeErr)
	}
	return filename, nil
}

// CostTracker 是一个包装了真实 LLMProvider 的装饰器中间件
type CostTracker struct {
	nextProvider provider.LLMProvider
	modelName    string
	session      *ctxpkg.Session // 当前所属的会话 (用于累加总成本)
}

// EnforcesBudget 表示该包装器在模型请求前执行共享预算准入。
func (*CostTracker) EnforcesBudget() bool { return true }

// NewCostTracker 构造函数：接收一个现有的 Provider，返回一个被监控的 Provider
func NewCostTracker(next provider.LLMProvider, modelName string, session *ctxpkg.Session) *CostTracker {
	return &CostTracker{
		nextProvider: next,
		modelName:    modelName,
		session:      session,
	}
}

// Generate 实现了 LLMProvider 接口！这意味着它可以被无缝注入到 Main Loop 中。
func (t *CostTracker) Generate(ctx context.Context, msgs []schema.Message, availableTools []schema.ToolDefinition) (*schema.Message, error) {
	ledger := budget.FromContext(ctx)
	var reservation *budget.Reservation
	var estimated int64
	if ledger != nil {
		if estimator, ok := t.nextProvider.(provider.InputEstimator); ok {
			var err error
			estimated, err = estimator.EstimateInput(ctx, msgs, availableTools)
			if err != nil {
				return nil, fmt.Errorf("估算模型输入失败: %w", err)
			}
		} else if ledger.RequiresEstimate() {
			return nil, fmt.Errorf("当前 Provider 不支持预算输入估算")
		}
		var err error
		reservation, err = ledger.Reserve(estimated)
		if err != nil {
			return nil, err
		}
	}
	startTime := time.Now()
	respMsg, err := t.nextProvider.Generate(ctx, msgs, availableTools)
	latency := time.Since(startTime)
	var usage *schema.Usage
	if respMsg != nil {
		usage = respMsg.Usage
	}
	if usage != nil && (usage.PromptTokens < 0 || usage.CompletionTokens < 0) {
		usage = nil
	}
	var settleErr error
	if ledger != nil {
		settleErr = ledger.Settle(reservation, usage)
	}
	var price *budget.Price
	if ledger != nil {
		price = ledger.Price()
	}
	var cost int64
	costKnown := usage != nil && price != nil
	if costKnown {
		var costErr error
		cost, costErr = price.CostMicros(int64(usage.PromptTokens), int64(usage.CompletionTokens))
		if costErr != nil {
			costKnown = false
			if settleErr == nil {
				settleErr = costErr
			}
		}
	}
	if t.session != nil {
		currency := ""
		if price != nil {
			currency = price.Currency
		}
		t.session.RecordUsage(usage, currency, cost, costKnown)
		call := ctxpkg.ModelCall{Model: t.modelName, RequestNumber: reservation.Sequence(), Currency: currency,
			CostMicros: cost, CostKnown: costKnown, UsageKnown: usage != nil, EstimatedInputTokens: estimated, EstimateMethod: provider.InputEstimateMethod}
		if ledger != nil {
			snapshot := ledger.Snapshot()
			call.RunID, call.Provider, call.Model = snapshot.RunID, snapshot.Provider, snapshot.Model
			call.PriceVersion = snapshot.PriceVersion
		}
		if usage != nil {
			call.PromptTokens, call.CompletionTokens = usage.PromptTokens, usage.CompletionTokens
		}
		if usage == nil {
			call.UnknownReason = "供应商未返回 Usage"
		} else if !costKnown {
			call.UnknownReason = "价格未知或费用计算失败"
		}
		t.session.RecordModelCall(call)
	}
	log.Printf("[Tracker] 模型调用结束 | 模型: %s | 耗时: %v | 费用状态: %s", t.modelName, latency, func() string {
		if costKnown {
			return price.Currency + " " + budget.FormatMicros(cost)
		}
		return "未知"
	}())
	if err != nil {
		return respMsg, err
	}
	if settleErr != nil {
		return respMsg, settleErr
	}
	return respMsg, nil
}
