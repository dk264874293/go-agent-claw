package provider_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/provider"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

type fixtureTransport func(*http.Request) (*http.Response, error)

func (f fixtureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestEvaluationToolSchemaOnResponsesWire(t *testing.T) {
	workDir := t.TempDir()
	defs := []schema.ToolDefinition{
		tools.NewListDirTool(workDir).Definition(),
		tools.NewReadFileTool(workDir).Definition(),
		tools.NewWriteFileTool(workDir).Definition(),
		tools.NewSearchTextTool(workDir).Definition(),
	}
	requiredByTool := map[string][]string{
		"list_dir": nil, "read_file": {"path"}, "write_file": {"path", "content"}, "search_text": {"query"},
	}
	propertiesByTool := map[string]map[string]string{
		"list_dir":    {"path": "string"},
		"read_file":   {"path": "string", "offset": "integer", "limit": "integer"},
		"write_file":  {"path": "string", "content": "string", "expected_version": "string"},
		"search_text": {"path": "string", "query": "string", "limit": "integer"},
	}
	check := func(name string, raw json.RawMessage) error {
		var parsed struct {
			Type                 string                    `json:"type"`
			Properties           map[string]map[string]any `json:"properties"`
			AdditionalProperties *bool                     `json:"additionalProperties"`
			Required             json.RawMessage           `json:"required"`
		}
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return fmt.Errorf("%s 参数定义无效: %w", name, err)
		}
		wantProperties := propertiesByTool[name]
		if parsed.Type != "object" || parsed.AdditionalProperties == nil || *parsed.AdditionalProperties || len(parsed.Properties) != len(wantProperties) {
			return fmt.Errorf("%s 的对象参数结构改变: %s", name, raw)
		}
		for prop, typ := range wantProperties {
			if parsed.Properties[prop]["type"] != typ {
				return fmt.Errorf("%s 的 %s 参数类型改变: %s", name, prop, raw)
			}
		}
		want := requiredByTool[name]
		if want == nil && parsed.Required != nil {
			return fmt.Errorf("%s 无必填项，required 字段应省略，实际为 %s", name, parsed.Required)
		}
		if want != nil {
			var got []string
			if err := json.Unmarshal(parsed.Required, &got); err != nil || !reflect.DeepEqual(got, want) {
				return fmt.Errorf("%s 必填项应为 %v，实际为 %s", name, want, parsed.Required)
			}
		}
		return nil
	}
	for _, def := range defs {
		raw, err := json.Marshal(def.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		if err := check(def.Name, raw); err != nil {
			t.Error(err)
		}
	}

	originalTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = originalTransport })
	http.DefaultTransport = fixtureTransport(func(r *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		var body struct {
			Tools []struct {
				Name       string          `json:"name"`
				Type       string          `json:"type"`
				Strict     *bool           `json:"strict"`
				Parameters json.RawMessage `json:"parameters"`
			} `json:"tools"`
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" || json.NewDecoder(r.Body).Decode(&body) != nil || len(body.Tools) != len(defs) {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return w.Result(), nil
		}
		for i, entry := range body.Tools {
			if entry.Name != defs[i].Name || entry.Type != "function" || entry.Strict == nil || *entry.Strict {
				http.Error(w, "unexpected function schema", http.StatusBadRequest)
				return w.Result(), nil
			}
			if err := check(entry.Name, entry.Parameters); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return w.Result(), nil
			}
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"resp_eval","object":"response","status":"completed","output":[{"type":"message","id":"msg_eval","status":"completed","role":"assistant","content":[{"type":"output_text","text":"通过","annotations":[]}]}]}`)
		return w.Result(), nil
	})
	t.Setenv("OPENAI_API_KEY", "offline-fixture-placeholder")
	t.Setenv("OPENAI_BASE_URL", "https://fixture.invalid/v1/")
	p := provider.NewOpenAIProvider(provider.DefaultOpenAIModel)
	got, err := p.Generate(context.Background(), []schema.Message{{Role: schema.RoleUser, Content: "本地协议测试"}}, defs)
	if err != nil || got == nil || got.Content != "通过" {
		t.Fatalf("Responses 请求未被本地协议端点接受: %v, %+v", err, got)
	}
}
