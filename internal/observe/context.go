package observe

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
)

type contextKey struct{}
type scope struct {
	attempt *Attempt
	ids     IdentityInfo
}

func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("无法生成观测身份")
	}
	return hex.EncodeToString(b[:])
}
func Digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func WorkspaceID(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	abs = filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	return Digest(abs)[:32]
}

func DefaultDir() string {
	if dir := os.Getenv("CLAW_OBSERVE_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claw", "observations")
}

func Identity(ctx context.Context) IdentityInfo {
	s, _ := ctx.Value(contextKey{}).(scope)
	return s.ids
}
func WithTask(ctx context.Context, id string) context.Context {
	s, _ := ctx.Value(contextKey{}).(scope)
	s.ids.TaskID = id
	if s.attempt != nil {
		s.attempt.mu.Lock()
		s.attempt.taskID = id
		s.attempt.mu.Unlock()
	}
	return context.WithValue(ctx, contextKey{}, s)
}
func WithAction(ctx context.Context, id string) context.Context {
	s, _ := ctx.Value(contextKey{}).(scope)
	s.ids.ActionID = id
	return context.WithValue(ctx, contextKey{}, s)
}
func WithModelCall(ctx context.Context, id string) context.Context {
	s, _ := ctx.Value(contextKey{}).(scope)
	s.ids.ModelCallID = id
	return context.WithValue(ctx, contextKey{}, s)
}
func WithAgent(ctx context.Context, id, parentID string) context.Context {
	s, _ := ctx.Value(contextKey{}).(scope)
	s.ids.AgentID = id
	s.ids.ParentAgentID = parentID
	s.ids.ActionID = ""
	s.ids.ModelCallID = ""
	return context.WithValue(ctx, contextKey{}, s)
}
func WithSourceSequence(ctx context.Context, n uint64) context.Context {
	s, _ := ctx.Value(contextKey{}).(scope)
	s.ids.SourceTaskSequence = n
	return context.WithValue(ctx, contextKey{}, s)
}
func Emit(ctx context.Context, kind string, payload Payload) {
	s, _ := ctx.Value(contextKey{}).(scope)
	if s.attempt != nil {
		s.attempt.emit(s.ids, kind, payload)
	}
}

// EmitWithIdentity 用复制的调用身份发布异步诊断摘要，不持有请求或会话对象。
// attempt/workspace 仍由原Recorder绑定，调用方不能借此切换观测目录。
func EmitWithIdentity(ctx context.Context, ids IdentityInfo, kind string, payload Payload) {
	s, _ := ctx.Value(contextKey{}).(scope)
	if s.attempt != nil {
		s.attempt.emit(ids, kind, payload)
	}
}
