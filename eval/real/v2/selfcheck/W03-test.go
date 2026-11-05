package tools_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func TestConcurrentEditRejectsStaleVersion(t *testing.T) {
	workDir := t.TempDir()
	const path = "example.txt"
	const original = "first: original alpha\nsecond: original beta\n"
	diskPath := filepath.Join(workDir, path)
	if err := os.WriteFile(diskPath, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}

	ctx := tools.WithScope(context.Background(), tools.ExecutionScope{
		Mode:    tools.ModeWorkspace,
		WorkDir: workDir,
	})
	registry := tools.NewRegistry()
	registry.Register(tools.NewReadFileTool(workDir))
	registry.Register(tools.NewEditFileTool(workDir))

	marshal := func(value any) json.RawMessage {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	read := registry.Execute(ctx, schema.ToolCall{
		ID:        "read-version",
		Name:      "read_file",
		Arguments: marshal(map[string]string{"path": path}),
	})
	if read.Status != schema.ToolSucceeded || read.Effect != schema.EffectNone {
		t.Fatalf("read_file did not succeed: %+v", read)
	}
	var version string
	for _, line := range strings.Split(read.Output, "\n") {
		if value, ok := strings.CutPrefix(line, "版本: "); ok {
			version = value
			break
		}
	}
	if len(version) != 64 {
		t.Fatalf("read_file did not return a SHA-256 version: %q", read.Output)
	}

	edits := []struct {
		id      string
		oldText string
		newText string
	}{
		{"edit-first", "first: original alpha", "first: updated alpha"},
		{"edit-second", "second: original beta", "second: updated beta"},
	}
	calls := make([]schema.ToolCall, len(edits))
	for i, edit := range edits {
		if count := strings.Count(original, edit.oldText); count != 1 {
			t.Fatalf("edit %d old_text has %d matches, want 1", i, count)
		}
		calls[i] = schema.ToolCall{
			ID:   edit.id,
			Name: "edit_file",
			Arguments: marshal(map[string]string{
				"path":             path,
				"old_text":         edit.oldText,
				"new_text":         edit.newText,
				"expected_version": version,
			}),
		}
	}

	type indexedResult struct {
		index  int
		result schema.ToolResult
	}
	results := make(chan indexedResult, len(calls))
	start := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(len(calls))
	for i, call := range calls {
		go func(index int, call schema.ToolCall) {
			ready.Done()
			<-start
			results <- indexedResult{index, registry.Execute(ctx, call)}
		}(i, call)
	}
	ready.Wait()
	close(start)

	// Classify by outcome, not completion order: either edit may win.
	winner, loser := -1, -1
	succeeded, rejected := 0, 0
	for range calls {
		got := <-results
		switch {
		case got.result.Status == schema.ToolSucceeded && got.result.Effect == schema.EffectConfirmed:
			succeeded++
			winner = got.index
		case got.result.Status == schema.ToolRejected && got.result.Effect == schema.EffectNone && got.result.ErrorKind == "版本冲突":
			rejected++
			loser = got.index
		default:
			t.Errorf("unexpected result for %s: %+v", calls[got.index].ID, got.result)
		}
	}
	if succeeded != 1 || rejected != 1 {
		t.Fatalf("want exactly one SUCCEEDED/CONFIRMED and one REJECTED/NONE version conflict; got %d successes and %d conflicts", succeeded, rejected)
	}

	data, err := os.ReadFile(diskPath)
	if err != nil {
		t.Fatal(err)
	}
	final := string(data)
	want := strings.Replace(original, edits[winner].oldText, edits[winner].newText, 1)
	if final != want {
		t.Errorf("disk content must reflect only the successful edit:\ngot  %q\nwant %q", final, want)
	}
	// The stale request must be rejected even though its target still matches uniquely.
	if count := strings.Count(final, edits[loser].oldText); count != 1 {
		t.Errorf("rejected edit's old_text has %d matches on disk, want 1", count)
	}
}
