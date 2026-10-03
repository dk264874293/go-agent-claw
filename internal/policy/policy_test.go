package policy

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestApprovalBinding(t *testing.T) {
	ctx, cancel := context.WithTimeout(WithIdentity(context.Background(), "运行", "会话"), time.Second)
	defer cancel()
	r, err := NewRequest(ctx, "write_file", []byte("\x1b[31m修改参数"))
	if err != nil {
		t.Fatal(err)
	}
	g := GrantFor(r, "本地主体")
	if err := Validate(r, g, time.Now()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.Summary, "\x1b") || len(r.ArgumentsDigest) != 64 {
		t.Fatal("摘要未转义或摘要无效")
	}
	for name, mutate := range map[string]func(*Grant){
		"请求": func(g *Grant) { g.RequestID = "其他" }, "运行": func(g *Grant) { g.RunID = "其他" },
		"会话": func(g *Grant) { g.SessionID = "其他" }, "工具": func(g *Grant) { g.Tool = "bash" },
		"参数": func(g *Grant) { g.ArgumentsDigest = Digest([]byte("变更")) }, "主体": func(g *Grant) { g.Subject = "" },
		"拒绝": func(g *Grant) { g.Allowed = false }, "过期": func(g *Grant) { g.ExpiresAt = time.Now().Add(-time.Second) },
		"延长": func(g *Grant) { g.ExpiresAt = r.ExpiresAt.Add(time.Second) },
	} {
		t.Run(name, func(t *testing.T) {
			copy := g
			mutate(&copy)
			if !errors.Is(Validate(r, copy, time.Now()), ErrApprovalRejected) {
				t.Fatal("不匹配的授权未拒绝")
			}
		})
	}
	other, _ := NewRequest(ctx, r.Tool, []byte("\x1b[31m修改参数"))
	if other.ID == r.ID || Validate(other, g, time.Now()) == nil {
		t.Fatal("授权被重放")
	}
}

func TestRestrictionsCannotExpand(t *testing.T) {
	p := Development(true)
	ctx := Restrict(context.Background(), ReadOnly())
	ctx = Restrict(ctx, p)
	if p.Check(ctx, []Capability{ReadWorkspace}) != nil || !errors.Is(p.Check(ctx, []Capability{WriteWorkspace}), ErrDenied) {
		t.Fatal("限制被扩大")
	}
	if p.Check(context.Background(), nil) == nil || ReadOnly().Check(context.Background(), []Capability{Delegate}) == nil {
		t.Fatal("未声明或不允许的能力未拒绝")
	}
	if p.Check(context.Background(), []Capability{WriteWorkspace, Network}) != nil {
		t.Fatal("限制污染原始策略")
	}
}
