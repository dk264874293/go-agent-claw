package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/taskstore"
)

func TestEvidenceToolSchemaAndInvalidRangeGuidance(t *testing.T) {
	encoded, err := json.Marshal(evidenceTool{}.Definition().InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties map[string]struct {
			Minimum, Maximum, Default int
		}
	}
	if err := json.Unmarshal(encoded, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Properties["offset"].Minimum != 0 || schema.Properties["limit"].Minimum != 1 ||
		schema.Properties["limit"].Maximum != 4000 || schema.Properties["limit"].Default != 4000 {
		t.Fatalf("分页约束没有公开给模型: %s", encoded)
	}
	for _, raw := range []string{`{"id":"材料","limit":6000}`, `{"id":"材料","offset":-1}`, `{"id":"材料","limit":-1}`} {
		_, err := (evidenceTool{}).Execute(context.Background(), json.RawMessage(raw))
		if err == nil || !strings.Contains(err.Error(), "1 至 4000") {
			t.Fatalf("错误缺少具体范围: %v", err)
		}
		guided := ctxpkg.NewRecoveryManager().AnalyzeAndInject("read_evidence", err.Error())
		if !strings.Contains(guided, "[系统救援指南]") || !strings.Contains(guided, "下一段 offset") {
			t.Fatalf("没有分页纠正提示: %s", guided)
		}
	}
}

func TestEvidenceToolUTF8MetadataAndLegacyDefault(t *testing.T) {
	store, err := taskstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	task, err := store.CreateTask(taskstore.NewID())
	if err != nil {
		t.Fatal(err)
	}
	id := taskstore.NewID()
	source := strings.Repeat("中", 2000)
	if err := task.SaveArtifact(id, []byte(source)); err != nil {
		t.Fatal(err)
	}
	r := &runState{readArtifactPage: task.ReadArtifactPage}
	ctx := context.WithValue(context.Background(), runContextKey{}, r)
	for _, args := range []map[string]any{{"id": id}, {"id": id, "limit": 0}} {
		raw, _ := json.Marshal(args)
		output, err := (evidenceTool{}).Execute(ctx, raw)
		if err != nil || !utf8.ValidString(output) || !strings.HasPrefix(output, "证据 "+id+"，范围 [0,3999)") ||
			!strings.Contains(output, "总字节数: 6000\n下一段 offset: 3999\nEOF: false") {
			t.Fatalf("默认分页或元数据不正确: %q, %v", output, err)
		}
		_, body, ok := strings.Cut(output, "\n\n")
		if !ok || body != source[:3999] {
			t.Fatalf("默认分页正文损坏: %q", body)
		}
	}
	for _, offset := range []int{6000, 7000} {
		raw, _ := json.Marshal(map[string]any{"id": id, "offset": offset})
		output, err := (evidenceTool{}).Execute(ctx, raw)
		if err != nil || !strings.Contains(output, "下一段 offset: 无\nEOF: true\n\n") {
			t.Fatalf("尾后空页缺少明确 EOF: %q, %v", output, err)
		}
	}
}
