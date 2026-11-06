// Package taskstore 按工作区隔离持久任务记录。
package taskstore

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"sync"
	"syscall"
	"unicode/utf8"
)

const (
	formatVersion = 1
	eventFile     = "events.jsonl"
	snapshotFile  = "snapshot.json"
)

var (
	ErrBusy    = errors.New("工作区已有任务在运行")
	ErrCorrupt = errors.New("任务记录损坏")
)

// Event 是一个完整提交的事件，Sequence 从 1 开始连续递增。
type Event struct {
	Version  int             `json:"version"`
	Sequence uint64          `json:"sequence"`
	Kind     string          `json:"kind"`
	Payload  json.RawMessage `json:"payload"`
}

// Snapshot 记录截止 Sequence 的任务状态。
type Snapshot struct {
	Version  int             `json:"version"`
	Sequence uint64          `json:"sequence"`
	State    json.RawMessage `json:"state"`
}

type Store struct {
	mu    sync.Mutex
	lock  *os.File
	root  *os.Root
	tasks map[string]*Task
}

type Task struct {
	store      *Store
	id         string
	root       *os.Root
	log        *os.File
	sequence   uint64
	offset     int64
	writeError error
}

func NewID() string {
	var id [16]byte
	rand.Read(id[:])
	return hex.EncodeToString(id[:])
}

// Open 持有工作区独占锁，直到 Store.Close。争用时立即返回 ErrBusy。
func Open(workDir string) (*Store, error) {
	workspace, err := os.OpenRoot(workDir)
	if err != nil {
		return nil, fmt.Errorf("打开任务工作区: %w", err)
	}
	defer workspace.Close()

	runtimeRoot, err := openDirectory(workspace, ".claw", true)
	if err != nil {
		return nil, fmt.Errorf("打开工作区运行目录: %w", err)
	}
	defer runtimeRoot.Close()

	lock, err := openRegular(runtimeRoot, "taskstore.lock", os.O_RDWR|os.O_CREATE)
	if err != nil {
		return nil, fmt.Errorf("打开工作区锁: %w", err)
	}
	for {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		lock.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, ErrBusy
		}
		return nil, fmt.Errorf("锁定任务工作区: %w", err)
	}

	root, err := openDirectory(runtimeRoot, "tasks", true)
	if err != nil {
		lock.Close()
		return nil, fmt.Errorf("打开任务存储目录: %w", err)
	}
	return &Store{lock: lock, root: root, tasks: make(map[string]*Task)}, nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lock == nil {
		return nil
	}
	var errs []error
	for _, task := range s.tasks {
		errs = append(errs, task.close())
	}
	errs = append(errs, s.root.Close(), s.lock.Close())
	s.lock = nil
	return errors.Join(errs...)
}

func (s *Store) CreateTask(id string) (*Task, error) {
	return s.openTask(id, true)
}

func (s *Store) OpenTask(id string) (*Task, error) {
	return s.openTask(id, false)
}

func (s *Store) openTask(id string, create bool) (*Task, error) {
	if !validID(id) {
		return nil, errors.New("任务 ID 必须是 32 位小写十六进制字符")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lock == nil {
		return nil, fs.ErrClosed
	}
	if s.tasks[id] != nil {
		return nil, fmt.Errorf("任务 %s 已打开", id)
	}
	flags := os.O_RDWR
	if create {
		if err := s.root.Mkdir(id, 0700); err != nil {
			return nil, fmt.Errorf("创建任务目录: %w", err)
		}
		if err := syncDirectory(s.root); err != nil {
			return nil, fmt.Errorf("同步任务存储目录: %w", err)
		}
		flags |= os.O_CREATE | os.O_EXCL
	}
	root, err := openDirectory(s.root, id, false)
	if err != nil {
		return nil, fmt.Errorf("打开任务目录: %w", err)
	}
	logFile, err := openRegular(root, eventFile, flags)
	if err != nil {
		root.Close()
		return nil, fmt.Errorf("打开任务事件日志: %w", err)
	}
	task := &Task{store: s, id: id, root: root, log: logFile}
	if create {
		err = syncDirectory(root)
	} else {
		var events []Event
		events, task.offset, err = readEvents(logFile)
		task.sequence = uint64(len(events))
	}
	if err != nil {
		task.close()
		return nil, fmt.Errorf("初始化任务记录: %w", err)
	}
	s.tasks[id] = task
	return task, nil
}

func (t *Task) Close() error {
	t.store.mu.Lock()
	defer t.store.mu.Unlock()
	return t.close()
}

func (t *Task) close() error {
	if t.log == nil {
		return nil
	}
	err := errors.Join(t.log.Close(), t.root.Close())
	t.log = nil
	delete(t.store.tasks, t.id)
	return err
}

// Append 同步落盘后才确认新事件。写入结果不确定时，须关闭并重开任务。
func (t *Task) Append(kind string, payload any) (Event, error) {
	if strings.TrimSpace(kind) == "" {
		return Event{}, errors.New("事件类型不能为空")
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return Event{}, fmt.Errorf("编码任务事件内容: %w", err)
	}

	t.store.mu.Lock()
	defer t.store.mu.Unlock()
	if t.log == nil {
		return Event{}, fs.ErrClosed
	}
	if t.writeError != nil {
		return Event{}, t.writeError
	}
	event := Event{Version: formatVersion, Sequence: t.sequence + 1, Kind: kind, Payload: data}
	line, err := json.Marshal(event)
	if err != nil {
		return Event{}, fmt.Errorf("编码任务事件: %w", err)
	}
	line = append(line, '\n')

	info, err := t.log.Stat()
	if err != nil {
		return Event{}, fmt.Errorf("检查任务事件日志: %w", err)
	}
	if info.Size() < t.offset {
		return Event{}, fmt.Errorf("%w: 事件日志被截短", ErrCorrupt)
	}
	if info.Size() > t.offset {
		tail := bufio.NewReader(io.NewSectionReader(t.log, t.offset, info.Size()-t.offset))
		if _, err := tail.ReadBytes('\n'); !errors.Is(err, io.EOF) {
			if err != nil {
				return Event{}, fmt.Errorf("检查任务事件末尾: %w", err)
			}
			return Event{}, fmt.Errorf("%w: 事件日志新增未识别的完整记录", ErrCorrupt)
		}
		// 恢复时留下的未完成行须先移除，避免与新事件拼接。
		if err := t.log.Truncate(t.offset); err != nil {
			t.writeError = fmt.Errorf("截除未完成的任务事件: %w", err)
			return Event{}, t.writeError
		}
	}
	if _, err := t.log.WriteAt(line, t.offset); err != nil {
		t.writeError = fmt.Errorf("追加任务事件: %w", err)
		return Event{}, t.writeError
	}
	if err := t.log.Sync(); err != nil {
		t.writeError = fmt.Errorf("同步任务事件: %w", err)
		return Event{}, t.writeError
	}
	t.offset += int64(len(line))
	t.sequence = event.Sequence
	return event, nil
}

func (t *Task) Events() ([]Event, error) {
	t.store.mu.Lock()
	defer t.store.mu.Unlock()
	if t.log == nil {
		return nil, fs.ErrClosed
	}
	events, offset, err := readEvents(t.log)
	if err != nil {
		return nil, err
	}
	if offset != t.offset || uint64(len(events)) != t.sequence {
		return nil, fmt.Errorf("%w: 事件日志在任务打开后变化", ErrCorrupt)
	}
	return events, nil
}

func (t *Task) SaveSnapshot(state any) error {
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("编码任务状态: %w", err)
	}

	t.store.mu.Lock()
	defer t.store.mu.Unlock()
	if t.log == nil {
		return fs.ErrClosed
	}
	if t.writeError != nil {
		return t.writeError
	}
	snapshot, err := json.Marshal(Snapshot{Version: formatVersion, Sequence: t.sequence, State: data})
	if err != nil {
		return fmt.Errorf("编码任务检查点: %w", err)
	}

	if err := writeAtomic(t.root, snapshotFile, snapshot, true); err != nil {
		return fmt.Errorf("提交任务检查点: %w", err)
	}
	return nil
}

func (t *Task) LoadSnapshot() (Snapshot, error) {
	t.store.mu.Lock()
	defer t.store.mu.Unlock()
	if t.log == nil {
		return Snapshot{}, fs.ErrClosed
	}
	file, err := openRegular(t.root, snapshotFile, os.O_RDONLY)
	if err != nil {
		return Snapshot{}, fmt.Errorf("打开任务检查点: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		return Snapshot{}, fmt.Errorf("读取任务检查点: %w", err)
	}
	var snapshot Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("%w: 解码任务检查点: %v", ErrCorrupt, err)
	}
	if snapshot.Version != formatVersion || snapshot.Sequence > t.sequence || len(snapshot.State) == 0 {
		return Snapshot{}, fmt.Errorf("%w: 检查点版本、事件序号或任务状态无效", ErrCorrupt)
	}
	return snapshot, nil
}

// SaveArtifact 保存当前任务的不可变材料。已存在的 ID 不可覆盖。
func (t *Task) SaveArtifact(id string, data []byte) error {
	if !validID(id) {
		return errors.New("材料 ID 必须是 32 位小写十六进制字符")
	}
	t.store.mu.Lock()
	defer t.store.mu.Unlock()
	if t.log == nil {
		return fs.ErrClosed
	}
	root, err := openDirectory(t.root, "artifacts", true)
	if err != nil {
		return fmt.Errorf("打开任务材料目录: %w", err)
	}
	defer root.Close()
	if err := writeAtomic(root, id, data, false); err != nil {
		return fmt.Errorf("提交任务材料: %w", err)
	}
	return nil
}

// ReadArtifact 按字节读取当前任务的材料。
func (t *Task) ReadArtifact(id string, offset, limit int) ([]byte, error) {
	page, err := t.readArtifactPage(id, offset, limit, false)
	return page.Data, err
}

// ArtifactPage 的范围和下一段位置均按源材料字节计数，不按显示字符计数。
type ArtifactPage struct {
	Data       []byte
	Offset     int64
	NextOffset int64
	Total      int64
	EOF        bool
}

// ReadArtifactPage 在 UTF-8 字符边界分页；原 ReadArtifact 保留原始字节读取语义。
func (t *Task) ReadArtifactPage(id string, offset, limit int) (ArtifactPage, error) {
	return t.readArtifactPage(id, offset, limit, true)
}

func (t *Task) readArtifactPage(id string, offset, limit int, textPage bool) (page ArtifactPage, err error) {
	page.Offset, page.NextOffset = int64(offset), int64(offset)
	if !validID(id) {
		return page, errors.New("材料 ID 必须是 32 位小写十六进制字符")
	}
	if offset < 0 || limit <= 0 {
		return page, errors.New("材料偏移量必须为非负数，读取长度必须为正数")
	}
	t.store.mu.Lock()
	defer t.store.mu.Unlock()
	if t.log == nil {
		return page, fs.ErrClosed
	}
	root, err := openDirectory(t.root, "artifacts", false)
	if err != nil {
		return page, fmt.Errorf("打开任务材料目录: %w", err)
	}
	defer root.Close()
	file, err := openRegular(root, id, os.O_RDONLY)
	if err != nil {
		return page, fmt.Errorf("打开任务材料: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return page, fmt.Errorf("读取材料信息: %w", err)
	}
	page.Total = info.Size()
	if int64(offset) >= info.Size() {
		page.Data, page.EOF = []byte{}, true
		return page, nil
	}
	start, length := int64(offset), min(int64(limit), info.Size()-int64(offset))
	if textPage {
		// 最多额外读取三个前缀字节及四个后缀字节，判断片段两端的字符边界。
		start = max(int64(0), int64(offset)-(utf8.UTFMax-1))
		end := int64(offset) + length
		end += min(info.Size()-end, int64(utf8.UTFMax))
		length = end - start
	}
	data, err := io.ReadAll(io.NewSectionReader(file, start, length))
	if err != nil {
		return page, fmt.Errorf("读取任务材料: %w", err)
	}
	if textPage {
		prefix := int(int64(offset) - start)
		for i := 0; i < prefix; i++ {
			_, size := utf8.DecodeRune(data[i:])
			if i+size > prefix {
				return page, fmt.Errorf("offset %d 位于 UTF-8 字符中间，请使用 offset %d 或上一页返回的下一段 offset", offset, start+int64(i))
			}
		}
		data = data[prefix:]
		consumed := 0
		for consumed < len(data) && consumed < limit {
			_, size := utf8.DecodeRune(data[consumed:])
			if size > limit-consumed {
				break
			}
			consumed += size
		}
		if consumed == 0 && len(data) > 0 {
			_, size := utf8.DecodeRune(data)
			return page, fmt.Errorf("limit %d 无法容纳完整 UTF-8 字符，请使用至少 %d 字节", limit, size)
		}
		data = data[:consumed]
	}
	page.Data, page.NextOffset = data, int64(offset)+int64(len(data))
	page.EOF = page.NextOffset >= page.Total
	return page, nil
}

func validID(id string) bool {
	if len(id) != 32 || strings.ToLower(id) != id {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

// writeAtomic 先同步内容，再发布文件名并同步目录。
func writeAtomic(root *os.Root, name string, data []byte, replace bool) error {
	temp := ".tmp-" + NewID()
	file, err := openRegular(root, temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return err
	}
	defer root.Remove(temp)
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	if err = errors.Join(err, file.Close()); err != nil {
		return err
	}
	if replace {
		err = root.Rename(temp, name)
	} else {
		err = root.Link(temp, name)
	}
	if err != nil {
		return err
	}
	return syncDirectory(root)
}

// readEvents 仅容忍末尾未写完整的行，所有完整行必须能解码且序号连续。
func readEvents(file *os.File) ([]Event, int64, error) {
	reader := bufio.NewReader(io.NewSectionReader(file, 0, 1<<63-1))
	var events []Event
	var offset int64
	for {
		line, err := reader.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			return events, offset, nil
		}
		if err != nil {
			return nil, 0, fmt.Errorf("读取任务事件: %w", err)
		}
		var event Event
		if err := json.Unmarshal(line, &event); err != nil {
			return nil, 0, fmt.Errorf("%w: 第 %d 条事件无法解码: %v", ErrCorrupt, len(events)+1, err)
		}
		if event.Version != formatVersion || event.Sequence != uint64(len(events))+1 || strings.TrimSpace(event.Kind) == "" || len(event.Payload) == 0 {
			return nil, 0, fmt.Errorf("%w: 第 %d 条事件的版本、序号或内容无效", ErrCorrupt, len(events)+1)
		}
		events = append(events, event)
		offset += int64(len(line))
	}
}

func openDirectory(parent *os.Root, name string, create bool) (*os.Root, error) {
	if create {
		err := parent.Mkdir(name, 0700)
		if err != nil && !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		if err == nil {
			if err := syncDirectory(parent); err != nil {
				return nil, err
			}
		}
	}
	info, err := parent.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s 必须是普通目录", name)
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	if err != nil {
		root.Close()
		return nil, err
	}
	if !os.SameFile(info, opened) {
		root.Close()
		return nil, fmt.Errorf("%s 在打开时被替换", name)
	}
	return root, nil
}

func openRegular(root *os.Root, name string, flags int) (*os.File, error) {
	if flags&os.O_CREATE != 0 && flags&os.O_EXCL == 0 {
		file, err := openRegular(root, name, flags|os.O_EXCL)
		if !errors.Is(err, fs.ErrExist) {
			return file, err
		}
		flags &^= os.O_CREATE
	}
	var before fs.FileInfo
	if flags&os.O_CREATE == 0 {
		var err error
		before, err = root.Lstat(name)
		if err != nil {
			return nil, err
		}
		if !before.Mode().IsRegular() {
			return nil, fmt.Errorf("%s 必须是普通文件", name)
		}
	}
	file, err := root.OpenFile(name, flags|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		file.Close()
		return nil, fmt.Errorf("%s 必须是普通文件", name)
	}
	if before != nil && !os.SameFile(before, info) {
		file.Close()
		return nil, fmt.Errorf("%s 在打开时被替换", name)
	}
	return file, nil
}

func syncDirectory(root *os.Root) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
