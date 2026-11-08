package observe

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestReadMemoryMetricPayloadContainsOnlyCounts(t *testing.T) {
	payload := cleanPayload(Payload{UnprocessedReadCount: 3, FoldedSourceBytes: 16384, OmittedNoteEntries: 4, Summary: "API_KEY=private-value"}, nil)
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"unprocessed_read_count":3`, `"folded_source_bytes":16384`, `"omitted_note_entries":4`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("缺少新计数白名单: %s", data)
		}
	}
	if strings.Contains(string(data), "private-value") {
		t.Fatal("新增元数据破坏了凭据脱敏")
	}
	cleaned := cleanPayload(Payload{UnprocessedReadCount: -1, FoldedSourceBytes: -2, OmittedNoteEntries: -3}, nil)
	if cleaned.UnprocessedReadCount != 0 || cleaned.FoldedSourceBytes != 0 || cleaned.OmittedNoteEntries != 0 {
		t.Fatal("非法负计数被保存为真实用量")
	}
}

func TestNotesResponseCountsAreBoundedMetadata(t *testing.T) {
	p := cleanPayload(Payload{NotesPreviouslyDelivered: -1, NotesThisRequest: -1, NotesCandidates: -1, NotesProcessedSources: -1, NotesDeferredSources: -1, NotesSubmitted: -1, NotesResponseRejected: -1, NotesDuplicates: -1}, nil)
	if p.NotesPreviouslyDelivered != 0 || p.NotesThisRequest != 0 || p.NotesCandidates != 0 || p.NotesProcessedSources != 0 || p.NotesDeferredSources != 0 || p.NotesSubmitted != 0 || p.NotesResponseRejected != 0 || p.NotesDuplicates != 0 {
		t.Fatal("响应负计数进入白名单")
	}
	data, err := json.Marshal(Payload{NotesCandidates: 8, NotesOpportunity: true, NotesResponseAccepted: true, NotesProcessedSources: 8})
	if err != nil || !strings.Contains(string(data), `"notes_candidates":8`) || !strings.Contains(string(data), `"notes_opportunity":true`) || strings.Contains(string(data), "summaries") {
		t.Fatalf("响应统计编码错误: %s %v", data, err)
	}
}
