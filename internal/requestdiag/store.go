package requestdiag

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

func (a *Attempt) writer() {
	defer close(a.done)
	for job := range a.queue {
		meta, diagnostic := job.meta, job.diagnostic
		bundle := id()
		diagnostic.ID = bundle
		var err error
		a.mu.Lock()
		abandon := a.abandon
		a.mu.Unlock()
		if abandon {
			err = errors.New("close_timeout")
		} else if bundle == "" {
			err = errors.New("id_unavailable")
		} else {
			err = a.save(bundle, job, &meta, &diagnostic)
		}
		if err != nil {
			diagnostic.ID = ""
			diagnostic.State = "unavailable"
			diagnostic.Reason = storageReason(err)
			diagnostic.Complete = false
			diagnostic.SavedBytes = 0
			diagnostic.PreviousAvailable = false
			meta.BodyCaptureState = "unavailable"
			a.warn(diagnostic.Reason)
		} else {
			meta.ManifestID = bundle
			diagnostic.State = "saved"
			if diagnostic.Complete {
				meta.BodyCaptureState = "saved"
			}
		}
		a.emit(Event{Kind: "request_metadata", Identity: job.identity, Metadata: &meta, Diagnostic: &diagnostic})
		a.mu.Lock()
		a.releaseLocked(job.current)
		a.releaseLocked(job.previous)
		a.mu.Unlock()
	}
}

func storageReason(err error) string {
	for _, word := range []string{"directory_quota", "attempt_quota", "quota_busy", "unsafe_path", "metadata_limit", "id_unavailable", "close_timeout"} {
		if err.Error() == word {
			return word
		}
	}
	return "storage_unavailable"
}

func (a *Attempt) save(bundle string, job *saveJob, meta *Metadata, diagnostic *Diagnostic) error {
	if err := os.MkdirAll(a.config.Dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(a.config.Dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("unsafe_path")
	}
	root, err := os.OpenRoot(a.config.Dir)
	if err != nil {
		return err
	}
	defer root.Close()
	opened, err := root.Stat(".")
	if err != nil {
		return err
	}
	if !os.SameFile(info, opened) {
		return errors.New("unsafe_path")
	}
	if err := root.Chmod(".", 0700); err != nil {
		return err
	}
	lock, err := openLock(root)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("quota_busy")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	files := map[string][]byte{}
	if len(job.body) > 0 {
		files["request.redacted.json"] = job.body
	}
	if job.previous != nil && len(job.previous.body) > 0 {
		files["previous-success.redacted.json"] = job.previous.body
	}
	// manifest存的是请求参数和来源索引，不保存正文。诊断文件名固定，不能提供任意路径。
	manifest := struct {
		Version     int        `json:"schema_version"`
		Metadata    Metadata   `json:"metadata"`
		Diagnostic  Diagnostic `json:"diagnostic"`
		Previous    *Metadata  `json:"previous_success,omitempty"`
		Limitations string     `json:"limitations"`
	}{1, *meta, *diagnostic, nil, "诊断副本经过脱敏和字段省略，不能作为原始请求精确重放；业务正文不能保证全部去敏。"}
	manifest.Metadata.ManifestID = bundle
	manifest.Diagnostic.State = "saved"
	if diagnostic.Complete {
		manifest.Metadata.BodyCaptureState = "saved"
	}
	if job.previous != nil {
		previous := job.previous.meta
		manifest.Previous = &previous
	}
	b, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	// 64KiB清单超限时省略完整来源项，仍保留数量及字段状态。
	for len(b) > 64<<10 && (len(manifest.Metadata.Items) > 0 || (manifest.Previous != nil && len(manifest.Previous.Items) > 0)) {
		if manifest.Previous != nil && len(manifest.Previous.Items) > 0 {
			copy := *manifest.Previous
			copy.Items = append([]InputItem(nil), copy.Items[:len(copy.Items)-1]...)
			copy.OmittedItems++
			manifest.Previous = &copy
		} else {
			manifest.Metadata.Items = manifest.Metadata.Items[:len(manifest.Metadata.Items)-1]
			manifest.Metadata.OmittedItems++
		}
		b, err = json.MarshalIndent(manifest, "", "  ")
		if err != nil {
			return err
		}
	}
	if len(b) > 64<<10 {
		return errors.New("metadata_limit")
	}
	if manifest.Metadata.OmittedItems > 0 || (manifest.Previous != nil && manifest.Previous.OmittedItems > 0) {
		manifest.Metadata.DiffComplete = false
		b, err = json.MarshalIndent(manifest, "", "  ")
		if err != nil {
			return err
		}
	}
	files["manifest.json"] = b
	var bytes int64
	for _, data := range files {
		bytes += int64(len(data))
	}
	reserve := int64(64 << 10)
	if a.config.MaxAttemptBytes < reserve {
		reserve = a.config.MaxAttemptBytes / 4
	}
	limit := a.config.MaxAttemptBytes
	if len(files) > 1 {
		limit -= reserve
	}
	// 失败时遗留的.pending也属于本次额度，不能只累计成功发布的文件。
	var attemptBytes int64
	if _, err := root.Lstat(a.identity.CaptureAttemptID); err == nil {
		existing, err := safeDirectory(root, a.identity.CaptureAttemptID, false)
		if err != nil {
			return err
		}
		attemptBytes, err = directoryBytes(existing)
		existing.Close()
		if err != nil {
			return err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if attemptBytes+bytes > limit {
		return errors.New("attempt_quota")
	}
	size, err := directoryBytes(root)
	if err != nil {
		return err
	}
	if size+bytes > a.config.MaxDirectoryBytes {
		return errors.New("directory_quota")
	}
	attempt, err := safeDirectory(root, a.identity.CaptureAttemptID, true)
	if err != nil {
		return err
	}
	defer attempt.Close()
	if _, err := attempt.Lstat(bundle); !errors.Is(err, fs.ErrNotExist) {
		if err != nil {
			return err
		}
		return errors.New("unsafe_path")
	}
	temp := ".pending-" + bundle
	dir, err := safeDirectory(attempt, temp, true)
	if err != nil {
		return err
	}
	for name, data := range files {
		file, openErr := dir.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
		if openErr != nil {
			dir.Close()
			return openErr
		}
		_, writeErr := file.Write(data)
		if writeErr == nil {
			writeErr = file.Sync()
		}
		closeErr := file.Close()
		if writeErr != nil {
			dir.Close()
			return writeErr
		}
		if closeErr != nil {
			dir.Close()
			return closeErr
		}
	}
	dir.Close()
	if err := attempt.Rename(temp, bundle); err != nil {
		return err
	}
	*meta = manifest.Metadata
	return nil
}

func openLock(root *os.Root) (*os.File, error) {
	file, err := root.OpenFile(".quota.lock", os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, errors.New("unsafe_path")
	}
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func safeDirectory(root *os.Root, name string, create bool) (*os.Root, error) {
	if !filepath.IsLocal(name) || name == "." || filepath.Base(name) != name {
		return nil, errors.New("unsafe_path")
	}
	if create {
		err := root.Mkdir(name, 0700)
		if err != nil && !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
	}
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("unsafe_path")
	}
	opened, err := root.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	after, err := opened.Stat(".")
	if err != nil || !os.SameFile(info, after) {
		opened.Close()
		return nil, errors.New("unsafe_path")
	}
	if err := opened.Chmod(".", 0700); err != nil {
		opened.Close()
		return nil, err
	}
	return opened, nil
}

// quota锁下计算物理文件总量，遗留.pending也计入；不自动清理任何历史材料。
func directoryBytes(root *os.Root) (int64, error) {
	var total int64
	entries := 0
	var walk func(*os.Root, int) error
	walk = func(dir *os.Root, depth int) error {
		if depth > 8 {
			return fmt.Errorf("unsafe_path")
		}
		file, err := dir.Open(".")
		if err != nil {
			return err
		}
		defer file.Close()
		for {
			items, readErr := file.ReadDir(128)
			for _, item := range items {
				entries++
				if entries > 100000 {
					return errors.New("unsafe_path")
				}
				info, err := dir.Lstat(item.Name())
				if err != nil {
					return err
				}
				if info.Mode().IsRegular() {
					if total > 1<<62-info.Size() {
						return errors.New("directory_quota")
					}
					total += info.Size()
					continue
				}
				if info.IsDir() {
					child, err := safeDirectory(dir, item.Name(), false)
					if err != nil {
						return err
					}
					err = walk(child, depth+1)
					child.Close()
					if err != nil {
						return err
					}
					continue
				}
				return errors.New("unsafe_path")
			}
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				return readErr
			}
		}
		return nil
	}
	err := walk(root, 0)
	return total, err
}
