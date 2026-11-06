package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"syscall"

	"github.com/dk264874293/go-agent-claw/internal/schema"
)

const findReadBatch = 64

type findDirectory interface {
	ReadDir(int) ([]fs.DirEntry, error)
	Close() error
}

type findWalkResult struct {
	paths               []string
	files               []schema.DiscoveredFile
	visited, unreadable int
	truncated           bool
}

// openFindDirectory 固定目录身份，既不跟随遍历链接，也避免目录被换成 FIFO 后阻塞打开。
func openFindDirectory(ctx context.Context, root *os.Root, path string) (findDirectory, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := checkPathLinks(root, path, false); err != nil {
		return nil, err
	}
	before, err := root.Stat(path)
	if err != nil {
		return nil, rootError(err)
	}
	if !before.IsDir() {
		return nil, reject("路径类型错误", fmt.Errorf("发现期间目标已不是目录: %s", path))
	}
	dir, err := root.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, rootError(err)
	}
	after, err := dir.Stat()
	if err != nil || !after.IsDir() || !os.SameFile(before, after) {
		dir.Close()
		if err == nil {
			err = reject("版本冲突", fmt.Errorf("发现期间目录已变化: %s", path))
		}
		return nil, err
	}
	if err := checkPathLinks(root, path, false); err != nil {
		dir.Close()
		return nil, err
	}
	return dir, nil
}

// walkFindFiles 每次只读取一个有界目录批次，不使用会全量装载单层目录的 WalkDir。
func walkFindFiles(ctx context.Context, root *os.Root, start, pattern string, limit int, open func(context.Context, *os.Root, string) (findDirectory, error)) (result findWalkResult, err error) {
	result.visited = 1 // 起点也计入检查上限，子目录不重复计数。
	directories := []string{start}
	scope := ScopeFromContext(ctx)
	for len(directories) > 0 && !result.truncated {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		path := directories[len(directories)-1]
		directories = directories[:len(directories)-1]
		dir, err := open(ctx, root, path)
		if err != nil {
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
			if path == start {
				return result, err
			}
			result.unreadable++
			continue
		}
		err = func() error {
			defer dir.Close()
			for {
				if err := ctx.Err(); err != nil {
					return err
				}
				remaining := maxFindEntries - result.visited
				if remaining == 0 {
					result.truncated = true
					return nil
				}
				entries, readErr := dir.ReadDir(min(findReadBatch, remaining))
				for _, entry := range entries {
					if err := ctx.Err(); err != nil {
						return err
					}
					result.visited++
					child := filepath.Join(path, entry.Name())
					if protectedRuntimePath(child) || !scope.allowsPath(child, entry.IsDir()) {
						continue
					}
					if entry.IsDir() {
						if entry.Name() != ".git" && entry.Name() != ".claw" {
							directories = append(directories, child)
						}
						continue
					}
					if !entry.Type().IsRegular() || !matchesNameGlob(pattern, entry.Name()) {
						continue
					}
					current, err := root.Lstat(child)
					if err != nil {
						result.unreadable++
						continue
					}
					if !current.Mode().IsRegular() {
						continue
					}
					if err := checkPathLinks(root, child, false); err != nil {
						result.unreadable++
						continue
					}
					result.paths = append(result.paths, filepath.ToSlash(child))
					result.files = append(result.files, schema.DiscoveredFile{Path: filepath.ToSlash(child), Bytes: current.Size()})
					if len(result.paths) == limit {
						result.truncated = true
						return nil
					}
				}
				if readErr != nil {
					if errors.Is(readErr, io.EOF) {
						return nil
					}
					if ctx.Err() != nil {
						return ctx.Err()
					}
					if path == start {
						return readErr
					}
					result.unreadable++
					return nil
				}
				if len(entries) == 0 {
					return nil
				}
			}
		}()
		if err != nil {
			return result, err
		}
	}
	sort.Strings(result.paths)
	return result, nil
}
