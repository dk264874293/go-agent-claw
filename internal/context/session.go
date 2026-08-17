// internal/engine/session.go
package context

import (
    "sync"
    "time"

    "github.com/dk264874293/go-agent-claw/internal/schema"
)

// Session 代表了一次持续的人机交互过程。它负责维护该会话的完整历史。
type Session struct {
    ID        string
    WorkDir   string // 该会话绑定的物理工作区
    CreatedAt time.Time
    UpdatedAt time.Time

    // 【新增】用于统计该 Session 累计消耗的资源
    TotalPromptTokens     int
    TotalCompletionTokens int
    TotalCostCNY          float64

    // 存放此 Session 中所有的用户输入、大模型回复和工具调用结果
    history []schema.Message
    mu      sync.RWMutex // 读写锁，防止并发读写历史时发生 Data Race
}

// RecordUsage 是一个给外部 Tracker 调用的辅助方法，用于累加账单
func (s *Session) RecordUsage(prompt int, completion int, cost float64) {
    s.mu.Lock()
    defer s.mu.Unlock()
    s.TotalPromptTokens += prompt
    s.TotalCompletionTokens += completion
    s.TotalCostCNY += cost
}

func NewSession(id string, workDir string) *Session {
    return &Session{
        ID:        id,
        WorkDir:   workDir,
        CreatedAt: time.Now(),
        UpdatedAt: time.Now(),
        history:   make([]schema.Message, 0),
    }
}

// Append 线程安全地向 Session 中追加消息
func (s *Session) Append(msgs ...schema.Message) {
    s.mu.Lock()
    defer s.mu.Unlock()
    s.history = append(s.history, msgs...)
    s.UpdatedAt = time.Now()

    // 【持久化预留点】：在真实的工业级实现中（如 Claude Code），
    // 我们会在这里将 s.history 以 JSONL 的格式 Append 到 workDir/.claw/sessions/xxx.jsonl 中。
    // s.SaveToDisk()
}

// syntheticContextNotice 是方案 A 兜底时注入的合成 user 前缀。
// 它不进入 Session 历史，每次开窗时现做，只为满足“会话以 user 开场”的 API 校验。
const syntheticContextNotice = "[系统提示] 更早的对话历史（含最初的任务指令）已被截断。以下是最近的工作记录，请据此继续执行当前任务，不要重复已完成的步骤。"

// GetWorkingMemory 是驾驭工程的核心！
// 它不返回全量历史，而是从后往前截取最近的 N 条消息，形成 Agent 的“短期工作记忆”。
func (s *Session) GetWorkingMemory(limit int) []schema.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()

	total := len(s.history)
	if total <= limit || limit <= 0 {
		// 如果历史总量小于限制，或者不设限，全量返回 (需要深拷贝以防外部修改)
		res := make([]schema.Message, total)
		copy(res, s.history)
		return res
	}

	// 截取最近的 limit 条消息
	start := total - limit

	// 【驾驭防线】：大模型 API 对消息结构有两道硬性校验：
	// 1. ToolCall↔ToolResult 配对：tool 消息的宿主 assistant(tool_calls) 必须在场；
	// 2. 开场角色：智谱 GLM 要求首条非 system 消息必须是普通 user 消息
	//    (实测 system 后直接跟 assistant 会返回 400 code=1214 “messages 参数非法”)。
	// 因此窗口起点必须落在“普通 user 消息”(RoleUser 且无 ToolCallID) 这个天然块边界上，
	// 它之后的每一段都是完整的 assistant(tool_calls) + tool 结果配对。策略分两档：
	// - 方案 B (优先)：回退补数不超过 maxBacktrack 条时，直接补齐，结构天然合法；
	// - 方案 A (兜底)：回退代价过大时放弃补数，保持原窗口大小，改为剔除头部
	//   失去宿主的孤儿 tool 结果，并注入一条合成的 user 前缀满足开场角色校验，
	//   避免远古历史把 token 预算吃光。
	const maxBacktrack = 3

	backtrack := start
	for backtrack > 0 && !(s.history[backtrack].Role == schema.RoleUser && s.history[backtrack].ToolCallID == "") {
		backtrack--
	}

	if start-backtrack <= maxBacktrack {
		// 方案 B：小幅回退补数，窗口略超 limit 条
		res := make([]schema.Message, total-backtrack)
		copy(res, s.history[backtrack:])
		return res
	}

	// 方案 A：先剔除窗口头部的孤儿 tool 结果（它们的宿主 assistant 已被截掉）
	head := start
	for head < total && s.history[head].Role == schema.RoleUser && s.history[head].ToolCallID != "" {
		head++
	}
	res := make([]schema.Message, 0, total-head+1)
	// 头部不是普通 user 消息时（通常是 assistant），注入合成前缀垫在开场
	if head >= total || s.history[head].Role != schema.RoleUser || s.history[head].ToolCallID != "" {
		res = append(res, schema.Message{
			Role:    schema.RoleUser,
			Content: syntheticContextNotice,
		})
	}
	res = append(res, s.history[head:]...)
	return res
}

// ==========================================
// 全局 Session Manager: 用于多用户/多终端隔离
// ==========================================

type SessionManager struct {
    sessions map[string]*Session
    mu       sync.RWMutex
}

var GlobalSessionMgr = &SessionManager{
    sessions: make(map[string]*Session),
}

// GetOrCreate 获取或创建一个会话
func (sm *SessionManager) GetOrCreate(id string, workDir string) *Session {
    sm.mu.Lock()
    defer sm.mu.Unlock()

    if sess, exists := sm.sessions[id]; exists {
        return sess
    }
    sess := NewSession(id, workDir)
    sm.sessions[id] = sess
    return sess
}