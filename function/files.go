package function

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type fileLocks struct {
	mu      sync.Mutex
	entries map[string]*fileLock
}

type fileLock struct {
	token chan struct{}
	refs  int
}

func NewFileFunctions() (read *Read, view *View, write *Write, edit *Edit) {
	locks := &fileLocks{entries: make(map[string]*fileLock)}
	return &Read{locks: locks}, &View{locks: locks}, &Write{locks: locks}, &Edit{locks: locks}
}

func (locks *fileLocks) acquire(ctx context.Context, path string) (string, func(), error) {
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	if locks == nil {
		return "", nil, fmt.Errorf("file functions are uninitialized; use NewFileFunctions")
	}
	if !filepath.IsAbs(path) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", nil, err
		}
		path = cwd + string(os.PathSeparator) + path
	}
	parent, name := filepath.Split(path)
	if name == "" || name == "." || name == ".." {
		return "", nil, fmt.Errorf("%s is not a regular file", path)
	}
	suffix := []string{name}
	for {
		candidate := strings.TrimRight(parent, string(os.PathSeparator))
		if candidate == "" {
			candidate = string(os.PathSeparator)
		}
		_, err := os.Lstat(candidate)
		if err == nil {
			resolved, err := filepath.EvalSymlinks(parent)
			if err != nil {
				return "", nil, err
			}
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			path = resolved
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", nil, err
		}
		parent, name = filepath.Split(candidate)
		if name == "" || name == ".." {
			return "", nil, err
		}
		suffix = append(suffix, name)
	}
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}

	locks.mu.Lock()
	entry := locks.entries[path]
	if entry == nil {
		entry = &fileLock{token: make(chan struct{}, 1)}
		locks.entries[path] = entry
	}
	entry.refs++
	locks.mu.Unlock()

	select {
	case entry.token <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-entry.token
			locks.release(path, entry)
			return "", nil, err
		}
		return path, func() {
			<-entry.token
			locks.release(path, entry)
		}, nil
	case <-ctx.Done():
		locks.release(path, entry)
		return "", nil, ctx.Err()
	}
}

func (locks *fileLocks) release(path string, entry *fileLock) {
	locks.mu.Lock()
	defer locks.mu.Unlock()
	entry.refs--
	if entry.refs == 0 {
		delete(locks.entries, path)
	}
}
