package engine

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

func carrierProjectionFixture(t *testing.T) ([]schema.Message, schema.ToolCall) {
	t.Helper()
	call := schema.ToolCall{ID: "call_notes", Name: "write_file", Arguments: json.RawMessage(`{"path":"result.md", "_work_notes":{"schema_version":1,"findings":[],"summaries":{"E1":"已核对"}}, "content":"业务_work_notes字样完整保留", "count":9007199254740993, "future":{"value":1.00}}`)}
	encoded, _ := json.Marshal(string(call.Arguments))
	items := []json.RawMessage{
		json.RawMessage(`{"type":"reasoning","id":"rs1","encrypted_content":"opaque-exact","summary":[]}`),
		json.RawMessage(`{"type":"future_kind","arguments":{"opaque":true},"bytes":[1,2]}`),
		json.RawMessage(`{"type":"function_call","call_id":"call_notes","id":"fc1","name":"write_file","arguments":` + string(encoded) + `,"status":"completed","future":9007199254740993}`),
	}
	return []schema.Message{{Role: schema.RoleAssistant, Content: "保持文字", ToolCalls: []schema.ToolCall{call}, ResponseItems: items}}, call
}

func TestCarrierProjectionOnlyRemovesConsumedMetadataAndPreservesOriginal(t *testing.T) {
	messages, originalCall := carrierProjectionFixture(t)
	before, _ := json.Marshal(messages)
	r := &runState{runID: "run", carriers: workNotesCarrierRuntime{consumed: map[workNotesCarrierIdentity]bool{workNotesIdentity("root", "run", originalCall): true}}}
	for _, tc := range []struct {
		name, agent, run string
		consumed         bool
	}{
		{"当前消费", "root", "run", true},
		{"其他Agent", "child", "run", false},
		{"历史运行", "root", "older", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			projected, removed, retained := projectWorkNotesMetadata(messages, func(call schema.ToolCall) bool { return r.isConsumedWorkNotesCarrier(tc.agent, tc.run, call) })
			if tc.consumed {
				if removed <= 0 || retained != 0 || bytes.Contains(projected[0].ToolCalls[0].Arguments, []byte(`"summaries"`)) {
					t.Fatalf("消费元数据未移除: %s %d/%d", projected[0].ToolCalls[0].Arguments, removed, retained)
				}
				args := string(projected[0].ToolCalls[0].Arguments)
				if !strings.Contains(args, `"count":9007199254740993, "future":{"value":1.00}`) || !strings.Contains(args, "业务_work_notes字样完整保留") {
					t.Fatal("业务参数被重编码或截断")
				}
				for i := 0; i < 2; i++ {
					if !bytes.Equal(projected[0].ResponseItems[i], messages[0].ResponseItems[i]) {
						t.Fatal("不透明项或未知项被改写")
					}
				}
				var item struct {
					Arguments string `json:"arguments"`
					ID        string `json:"id"`
					CallID    string `json:"call_id"`
				}
				if json.Unmarshal(projected[0].ResponseItems[2], &item) != nil || item.Arguments != args || item.ID != "fc1" || item.CallID != originalCall.ID {
					t.Fatal("Responses参数未同步或身份被改写")
				}
			} else if removed != 0 || retained <= 0 || !bytes.Equal(projected[0].ToolCalls[0].Arguments, originalCall.Arguments) {
				t.Fatal("跨Agent/运行元数据被移除")
			}
			after, _ := json.Marshal(messages)
			if !bytes.Equal(before, after) {
				t.Fatal("原始Session响应发生变化")
			}
		})
	}
	changed := originalCall
	changed.Arguments = append(append(json.RawMessage(nil), changed.Arguments...), ' ')
	if r.isConsumedWorkNotesCarrier("root", "run", changed) {
		t.Fatal("参数哈希未严格匹配")
	}
}

func TestCarrierProjectionFallsBackWhenResponsesCannotReliablyMatch(t *testing.T) {
	for _, name := range []string{"参数不同", "缺少调用", "重复调用", "同名重复字段", "调用名称不同"} {
		t.Run(name, func(t *testing.T) {
			messages, _ := carrierProjectionFixture(t)
			switch name {
			case "参数不同":
				messages[0].ResponseItems[2] = json.RawMessage(`{"type":"function_call","call_id":"call_notes","name":"write_file","arguments":"{}"}`)
			case "缺少调用":
				messages[0].ResponseItems = messages[0].ResponseItems[:2]
			case "重复调用":
				messages[0].ResponseItems = append(messages[0].ResponseItems, messages[0].ResponseItems[2])
			case "同名重复字段":
				messages[0].ToolCalls[0].Arguments = json.RawMessage(`{"path":"a","_work_notes":{},"_work_notes":{}}`)
			case "调用名称不同":
				messages[0].ResponseItems[2] = bytes.Replace(messages[0].ResponseItems[2], []byte(`"name":"write_file"`), []byte(`"name":"edit_file"`), 1)
			}
			before, _ := json.Marshal(messages)
			projected, removed, retained := projectWorkNotesMetadata(messages, func(schema.ToolCall) bool { return true })
			after, _ := json.Marshal(projected)
			if !bytes.Equal(before, after) || removed != 0 || retained <= 0 {
				t.Fatalf("无法可靠匹配却改写了调用: %s removed=%d retained=%d", after, removed, retained)
			}
		})
	}
}

func TestRemoveCarrierMetadataPreservesBusinessRawValuesInEveryPosition(t *testing.T) {
	for _, raw := range []string{
		`{"_work_notes":{},"content":"完整业务"}`,
		`{"content":"完整业务","_work_notes":{}}`,
		` { "_work_notes" : {} } `,
		`{"content":"完整业务", "_work_notes":{}, "future":1.00}`,
	} {
		clean, removed, ok := removeWorkNotesMetadata([]byte(raw))
		if !ok || removed <= 0 || !json.Valid(clean) || bytes.Contains(clean, []byte(`"_work_notes"`)) || len(raw)-len(clean) != int(removed) {
			t.Fatalf("顶层删除错误: %q -> %q %d %v", raw, clean, removed, ok)
		}
	}
}
