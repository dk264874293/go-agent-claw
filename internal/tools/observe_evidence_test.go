package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

func TestEvidenceObservationOnlyRecordsWhitelistedMetadata(t *testing.T) {
	id := "0123456789abcdef0123456789abcdef"
	call := schema.ToolCall{ID: "读取材料", Name: "read_evidence", Arguments: json.RawMessage(`{"id":"` + id + `","offset":4000,"limit":2000,"content":"不可观测正文"}`)}
	payload := observationForTool(call)
	if payload.ArtifactID != id || payload.Offset != 4000 || payload.Limit != 2000 || payload.ContentBytes != 0 {
		t.Fatalf("材料观测元数据不正确: %+v", payload)
	}
	data, _ := json.Marshal(payload)
	if strings.Contains(string(data), "不可观测正文") {
		t.Fatalf("材料正文泄漏进观测: %s", data)
	}
	call.Arguments = json.RawMessage(`{"id":"sk-秘密材料口令","offset":3,"limit":4}`)
	payload = observationForTool(call)
	data, _ = json.Marshal(payload)
	if payload.ArtifactID != "" || strings.Contains(string(data), "秘密") {
		t.Fatalf("非材料 ID 被当作元数据保存: %s", data)
	}
}
