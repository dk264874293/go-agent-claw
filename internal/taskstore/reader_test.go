package taskstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestReadOnlyReaderConcurrentAndPartial(t *testing.T) {
	work := t.TempDir()
	store, e := Open(work)
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	id := NewID()
	task, e := store.CreateTask(id)
	if e != nil {
		t.Fatal(e)
	}
	task.Append("created", map[string]int{"version": 1})
	r, e := NewReadOnlyReader(work, id)
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	events, reset, e := r.Poll()
	if e != nil || reset || len(events) != 1 {
		t.Fatalf("%v %v %v", events, reset, e)
	}
	path := filepath.Join(work, ".claw/tasks", id, "events.jsonl")
	line, _ := json.Marshal(Event{Version: 1, Sequence: 2, Kind: "next", Payload: json.RawMessage(`{"version":1}`)})
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	f.Write(line[:10])
	f.Close()
	before, _ := os.ReadFile(path)
	events, _, e = r.Poll()
	if e != nil || len(events) != 0 {
		t.Fatal("半行被消费", e)
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatal("只读观察修改了日志")
	}
	f, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	f.Write(append(line[10:], '\n'))
	f.Close()
	events, _, e = r.Poll()
	if e != nil || len(events) != 1 || events[0].Sequence != 2 {
		t.Fatal(e, events)
	}
	os.WriteFile(path, []byte("{bad}\n"), 0600)
	_, reset, e = r.Poll()
	if !reset || e == nil {
		t.Fatal("截短坏行未检测", reset, e)
	}
}

func TestReadOnlyReaderSameInodeRewriteLarger(t *testing.T) {
	work := t.TempDir()
	id := NewID()
	dir := filepath.Join(work, ".claw/tasks", id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "events.jsonl")
	line := func(sequence uint64, text string) []byte {
		payload, _ := json.Marshal(map[string]string{"text": text})
		raw, _ := json.Marshal(Event{Version: 1, Sequence: sequence, Kind: "created", Payload: payload})
		return append(raw, '\n')
	}
	if err := os.WriteFile(path, line(1, "old"), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := NewReadOnlyReader(work, id)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if events, _, err := r.Poll(); err != nil || len(events) != 1 {
		t.Fatal(events, err)
	}
	newContent := append(line(1, strings.Repeat("new", 100)), line(2, "next")...)
	if err := os.WriteFile(path, newContent, 0600); err != nil {
		t.Fatal(err)
	}
	events, reset, err := r.Poll()
	if err != nil || !reset || len(events) != 2 || events[0].Sequence != 1 {
		t.Fatalf("原inode重写未重建: %v %v %v", events, reset, err)
	}
}

func TestReadOnlyReaderRejectsFIFOWithoutBlocking(t *testing.T) {
	work := t.TempDir()
	id := NewID()
	dir := filepath.Join(work, ".claw/tasks", id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "events.jsonl"), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := NewReadOnlyReader(work, id)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	done := make(chan error, 1)
	go func() { _, _, err := r.Poll(); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("FIFO被视作日志")
		}
	case <-time.After(time.Second):
		t.Fatal("FIFO打开阻塞")
	}
}
func TestReadOnlyReaderRejectsEscapingSymlink(t *testing.T) {
	work := t.TempDir()
	os.MkdirAll(filepath.Join(work, ".claw/tasks"), 0700)
	id := NewID()
	os.Symlink(t.TempDir(), filepath.Join(work, ".claw/tasks", id))
	r, e := NewReadOnlyReader(work, id)
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	if _, _, e = r.Poll(); e == nil {
		t.Fatal("越界链接被读取")
	}
}
