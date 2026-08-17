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
    "os"
    "path/filepath"
    "sync"
    "time"
	"fmt"
	"log"

    "github.com/dk264874293/go-agent-claw/internal/provider"
    "github.com/dk264874293/go-agent-claw/internal/schema"
    ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
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
    mu sync.Mutex // 保护 Children 的并发写入
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
func ExportTraceToFile(rootSpan *Span, workDir string, sessionID string) error {
    traceDir := filepath.Join(workDir, ".claw", "traces")
    os.MkdirAll(traceDir, 0755)
    filename := filepath.Join(traceDir, fmt.Sprintf("trace_%s_%d.json", sessionID, time.Now().Unix()))
    // 美化输出 JSON，便于人类和工具阅读
    data, err := json.MarshalIndent(rootSpan, "", "  ")
    if err != nil {
        return err
    }
    return os.WriteFile(filename, data, 0644)
}

// PricingModel 定义了不同大模型的计费标准 (单位: 美元/1M Tokens)
// 为了演示，这里硬编码了当前市面上几个主流模型的官方大致定价。
var PricingModel = map[string]struct {
    InputPrice  float64
    OutputPrice float64
}{
	"glm-4.7":              {InputPrice: 0.15, OutputPrice: 0.15}, // 这里假定的大模型价格(每百万Token，tk)
	"glm-4.7-flashx":       {InputPrice: 0.15, OutputPrice: 0.15}, // 当前主力模型，暂沿用 glm-4.7 的假定价格，待官方定价校准
}

// CostTracker 是一个包装了真实 LLMProvider 的装饰器中间件
type CostTracker struct {
    nextProvider provider.LLMProvider
    modelName    string
    session      *ctxpkg.Session // 当前所属的会话 (用于累加总成本)
}

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

    // 1. 记录请求发起的时刻
    startTime := time.Now()

    // 2. 调用真实的底层大模型去执行耗时的网络请求
    respMsg, err := t.nextProvider.Generate(ctx, msgs, availableTools)

    // 3. 计算耗时
    latency := time.Since(startTime)

    // 如果报错了，只打印报错时间，不计费
    if err != nil {
        log.Printf("[Tracker] ❌ API 调用失败，耗时: %v\n", latency)
        return respMsg, err
    }

    // 4. 解析 Token 并计算成本
    if respMsg.Usage != nil {
        promptTokens := respMsg.Usage.PromptTokens
        completionTokens := respMsg.Usage.CompletionTokens

        var cost float64
        if price, exists := PricingModel[t.modelName]; exists {
            // 计算美元花费 = (输入Tokens * 输入单价 + 输出Tokens * 输出单价) / 1000000
            cost = (float64(promptTokens)*price.InputPrice + float64(completionTokens)*price.OutputPrice) / 1000000.0
        }

        // 5. 打印精美的仪表盘日志
        log.Printf("[Tracker] 📊 API 调用完成 | 耗时: %v | 输入: %d tk | 输出: %d tk | 花费: ¥%.6f\n", 
            latency, promptTokens, completionTokens, cost)

        // 6. 将账单累加到当前的 Session 中，供人类后续随时查询
        if t.session != nil {
            t.session.RecordUsage(promptTokens, completionTokens, cost)
            log.Printf("[Tracker] 💰 当前会话 (%s) 累计花费: ¥%.6f\n", t.session.ID, t.session.TotalCostCNY)
        }
    } else {
        log.Printf("[Tracker] ⚠️ API 调用完成，但未返回 Usage 数据 | 耗时: %v\n", latency)
    }

    return respMsg, nil
}