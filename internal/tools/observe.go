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
		ID         string   `json:"id"`
		Path       string   `json:"path"`
		Offset     int64    `json:"offset"`
		Limit      int64    `json:"limit"`
		StartLine  int64    `json:"start_line"`
		EndLine    int64    `json:"end_line"`
		Content    string   `json:"content"`
		OldString  string   `json:"old_string"`
		NewString  string   `json:"new_string"`
		ReportID   string   `json:"report_id"`
		FindingIDs []string `json:"finding_ids"`
	}
	if json.Unmarshal(call.Arguments, &args) != nil {
		return p
	}
	switch call.Name {
	case "read_report":
		if validObservationArtifactID(args.ReportID) {
			p.ReportID = args.ReportID
		}
		p.FindingCount = int64(len(args.FindingIDs))
	case "read_evidence":
		if validObservationArtifactID(args.ID) {
			p.ArtifactID = args.ID
		}
		p.Offset, p.Limit = args.Offset, args.Limit
	case "read_file", "read_lines", "write_file", "edit_file", "list_dir", "search_text", "find_files":
		if filepath.IsLocal(args.Path) {
			p.Path = filepath.Clean(args.Path)
		}
		p.Offset, p.Limit = args.Offset, args.Limit
		p.StartLine, p.EndLine = args.StartLine, args.EndLine
		p.ContentBytes = int64(len(args.Content) + len(args.OldString) + len(args.NewString))
	}
	return p
}

func validObservationArtifactID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, char := range id {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return true
}
