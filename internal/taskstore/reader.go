package taskstore

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// ReadOnlyReader 不取得执行锁，也不创建或修复任何任务文件。
type ReadOnlyReader struct {
	root     *os.Root
	name     string
	info     os.FileInfo
	offset   int64
	sequence uint64
	anchor   []byte
}

func NewReadOnlyReader(workDir, taskID string) (*ReadOnlyReader, error) {
	if !validID(taskID) {
		return nil, errors.New("无效任务标识")
	}
	root, err := os.OpenRoot(workDir)
	if err != nil {
		return nil, err
	}
	return &ReadOnlyReader{root: root, name: ".claw/tasks/" + taskID + "/events.jsonl"}, nil
}

func (r *ReadOnlyReader) Close() error { return r.root.Close() }

// Poll 仅返回完整且连续的新事件。reset 表示旧投影必须清除。
func (r *ReadOnlyReader) Poll() (events []Event, reset bool, err error) {
	// 与执行侧共用安全普通文件打开，但始终只读、不取得锁、不执行修复。
	f, err := openRegular(r.root, r.name, os.O_RDONLY)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, errors.New("任务日志不是普通文件")
	}
	if r.info != nil && (!os.SameFile(r.info, info) || info.Size() < r.offset || (info.Size() <= r.offset && !info.ModTime().Equal(r.info.ModTime()))) {
		r.offset, r.sequence = 0, 0
		r.anchor = nil
		reset = true
	}
	// 同一 inode 可能在两次轮询间被截短并重写到更大尺寸。
	// 检查已消费边界，避免直接把旧 offset 接到新内容中。
	if len(r.anchor) > 0 {
		anchor := make([]byte, len(r.anchor))
		if _, readErr := f.ReadAt(anchor, r.offset-int64(len(anchor))); readErr != nil || !bytes.Equal(anchor, r.anchor) {
			r.offset, r.sequence, r.anchor = 0, 0, nil
			reset = true
		}
	}
	r.info = info
	reader := bufio.NewReader(io.NewSectionReader(f, r.offset, info.Size()-r.offset))
	for {
		// 有界读取，避免损坏日志的一条超大行耗尽监测进程内存。
		var line []byte
		for {
			part, e := reader.ReadSlice('\n')
			line = append(line, part...)
			if len(line) > 32<<20 {
				return events, reset, fmt.Errorf("%w: 单条事件过大", ErrCorrupt)
			}
			if errors.Is(e, bufio.ErrBufferFull) {
				continue
			}
			if errors.Is(e, io.EOF) {
				return events, reset, nil
			}
			if e != nil {
				return events, reset, e
			}
			break
		}
		var event Event
		if json.Unmarshal(line, &event) != nil || event.Version != formatVersion || event.Sequence != r.sequence+1 || strings.TrimSpace(event.Kind) == "" || len(event.Payload) == 0 {
			return events, reset, fmt.Errorf("%w: 事件 %d 无效", ErrCorrupt, r.sequence+1)
		}
		events = append(events, event)
		r.sequence = event.Sequence
		r.offset += int64(len(line))
		r.anchor = bytes.Clone(line[max(0, len(line)-128):])
	}
}
