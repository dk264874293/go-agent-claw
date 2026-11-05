package tools

import (
	"encoding/json"
	"path/filepath"

	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/schema"
)

// observationForTool 只提供固定字段，文件正文、命令与任意工具参数不进入观测流。
func observationForTool(call schema.ToolCall) observe.Payload {
	p := observe.Payload{Tool: call.Name, ToolCallID: call.ID, RequestDigest: observe.Digest(string(call.Arguments))}
	var canonical any
	if json.Unmarshal(call.Arguments, &canonical) == nil {
		if b, err := json.Marshal(canonical); err == nil {
			p.RequestDigest = observe.Digest(string(b))
		}
	}
	var args struct {
		Path      string `json:"path"`
		Offset    int64  `json:"offset"`
		Limit     int64  `json:"limit"`
		Content   string `json:"content"`
		OldString string `json:"old_string"`
		NewString string `json:"new_string"`
	}
	if json.Unmarshal(call.Arguments, &args) != nil {
		return p
	}
	switch call.Name {
	case "read_file", "write_file", "edit_file", "list_dir", "search_text":
		if filepath.IsLocal(args.Path) {
			p.Path = filepath.Clean(args.Path)
		}
		p.Offset, p.Limit = args.Offset, args.Limit
		p.ContentBytes = int64(len(args.Content) + len(args.OldString) + len(args.NewString))
	}
	return p
}
