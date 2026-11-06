package taskstore

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func openTestStore(t *testing.T, workspace string) *Store {
	t.Helper()
	store, err := Open(workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	return store
}

func createTestTask(t *testing.T, store *Store, id string) *Task {
	t.Helper()
	task, err := store.CreateTask(id)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func appendTestEvent(t *testing.T, task *Task, kind string) Event {
	t.Helper()
	event, err := task.Append(kind, map[string]string{"action_id": "动作"})
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func TestRecoverInterruptedAppend(t *testing.T) {
	workspace := t.TempDir()
	store := openTestStore(t, workspace)
	id := NewID()
	task := createTestTask(t, store, id)
	appendTestEvent(t, task, "动作开始")
	if err := task.SaveSnapshot(map[string]string{"目标": "恢复任务"}); err != nil {
		t.Fatal(err)
	}
	appendTestEvent(t, task, "动作已确认")
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	logFile, err := os.OpenFile(filepath.Join(workspace, ".claw", "tasks", id, eventFile), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := logFile.WriteString(`{"version":1,"sequence":3,"kind":`)
	if err := errors.Join(writeErr, logFile.Close()); err != nil {
		t.Fatal(err)
	}

	reopened := openTestStore(t, workspace)
	task, err = reopened.OpenTask(id)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := task.LoadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]string
	if err := json.Unmarshal(snapshot.State, &state); err != nil {
		t.Fatal(err)
	}
	if snapshot.Sequence != 1 || state["目标"] != "恢复任务" {
		t.Fatalf("检查点丢失: %+v, %v", snapshot, state)
	}
	if got := appendTestEvent(t, task, "恢复完成").Sequence; got != 3 {
		t.Fatalf("恢复后事件序号 = %d，期望 3", got)
	}
	if err := task.Close(); err != nil {
		t.Fatal(err)
	}
	task, err = reopened.OpenTask(id)
	if err != nil {
		t.Fatal(err)
	}
	events, err := task.Events()
	if err != nil || len(events) != 3 || events[1].Kind != "动作已确认" || events[2].Kind != "恢复完成" {
		t.Fatalf("恢复后的事件 = %+v, 错误 = %v", events, err)
	}
}

func TestCorruptStateBlocksRecovery(t *testing.T) {
	workspace := t.TempDir()
	store := openTestStore(t, workspace)
	first := `{"version":1,"sequence":1,"kind":"开始","payload":null}` + "\n"
	for name, content := range map[string]string{
		"中间损坏": first + "损坏\n" + `{"version":1,"sequence":3,"kind":"完成","payload":null}` + "\n",
		"版本未知": `{"version":2,"sequence":1,"kind":"开始","payload":null}` + "\n",
		"序号跳跃": `{"version":1,"sequence":2,"kind":"开始","payload":null}` + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			id := NewID()
			task := createTestTask(t, store, id)
			if err := task.Close(); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(workspace, ".claw", "tasks", id, eventFile), []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.OpenTask(id); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("期望损坏错误，得到 %v", err)
			}
		})
	}
	id := NewID()
	task := createTestTask(t, store, id)
	if _, err := task.LoadSnapshot(); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("期望未找到检查点，得到 %v", err)
	}
	data := []byte(`{"version":1,"sequence":1,"state":{}}`)
	if err := os.WriteFile(filepath.Join(workspace, ".claw", "tasks", id, snapshotFile), data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := task.LoadSnapshot(); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("超前检查点未被拒绝: %v", err)
	}
}

func TestArtifactsAreScopedAndImmutable(t *testing.T) {
	workspace := t.TempDir()
	store := openTestStore(t, workspace)
	first := createTestTask(t, store, NewID())
	second := createTestTask(t, store, NewID())
	id := NewID()
	if err := first.SaveArtifact(id, []byte("012345")); err != nil {
		t.Fatal(err)
	}
	if err := first.SaveArtifact(id, []byte("替换")); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("同 ID 覆盖未被拒绝: %v", err)
	}
	got, err := first.ReadArtifact(id, 1, 3)
	if err != nil || string(got) != "123" {
		t.Fatalf("分段读取结果 = %q, %v", got, err)
	}
	got, err = first.ReadArtifact(id, 10, 3)
	if err != nil || len(got) != 0 {
		t.Fatalf("结尾之后读取结果 = %q, %v", got, err)
	}
	if _, err := second.ReadArtifact(id, 0, 6); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("跨任务读取未被拒绝: %v", err)
	}
	if err := first.SaveArtifact("../"+id, []byte("越界")); err == nil {
		t.Fatal("接受了带路径的材料 ID")
	}
}

func TestWorkspacePathsRejectSymlinks(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(workspace, ".claw")); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(workspace); err == nil {
		t.Fatal("接受了指向外部的运行目录")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("外部目录被修改: %v, %v", entries, err)
	}

	workspace = t.TempDir()
	store := openTestStore(t, workspace)
	id := NewID()
	if err := os.Symlink(outside, filepath.Join(workspace, ".claw", "tasks", id)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.OpenTask(id); err == nil {
		t.Fatal("接受了指向外部的任务目录")
	}
}

func TestWorkspaceLockAcrossProcesses(t *testing.T) {
	workspace := t.TempDir()
	store := openTestStore(t, workspace)
	run := func(expect string) {
		t.Helper()
		cmd := exec.Command(os.Args[0], "-test.run=^TestWorkspaceLockChild$")
		cmd.Env = append(os.Environ(), "CLAW_TEST_LOCK_WORKSPACE="+workspace, "CLAW_TEST_LOCK_EXPECT="+expect)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("子进程预期 %s: %v\n%s", expect, err, out)
		}
	}
	run("busy")
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	run("open")
}

func TestWorkspaceLockChild(t *testing.T) {
	workspace := os.Getenv("CLAW_TEST_LOCK_WORKSPACE")
	if workspace == "" {
		return
	}
	store, err := Open(workspace)
	if os.Getenv("CLAW_TEST_LOCK_EXPECT") == "busy" {
		if !errors.Is(err, ErrBusy) {
			t.Fatalf("期望工作区锁争用，得到 %v", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}
