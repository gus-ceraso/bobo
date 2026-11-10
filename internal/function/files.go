package function

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

type fileLocks struct {
	mu      sync.Mutex
	entries map[string]*fileLock
}

type fileLock struct {
	token chan struct{}
	refs  int
}

// NewFileFunctions creates initialized file tools that serialize access to the
// same path. Separate sets, Bash commands, and external processes do not share
// this coordination.
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

// openRegular rejects final-component symlinks and opens special files without
// blocking, then rejects them. The caller owns the returned descriptor.
func openRegular(path string) (*os.File, os.FileInfo, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err == nil && !info.Mode().IsRegular() {
		err = fmt.Errorf("%s is not a regular file", path)
	}
	if err != nil {
		file.Close()
		return nil, nil, err
	}
	return file, info, nil
}

// replaceFile commits only complete output. New files respect the umask; existing
// files keep their permissions and remain private while output is incomplete.
// This promises atomic visibility, not the credential store's crash durability.
func replaceFile(ctx context.Context, path string, previous os.FileInfo, source io.Reader) error {
	mode := os.FileMode(0o644)
	if previous != nil {
		mode = 0o600
	}
	tempPath := filepath.Join(filepath.Dir(path), ".bobo-"+rand.Text())
	temp, err := os.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	defer os.Remove(tempPath)
	defer temp.Close()
	buf := make([]byte, 1<<15)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := source.Read(buf)
		if n > 0 {
			if written, err := temp.Write(buf[:n]); err != nil {
				return err
			} else if written != n {
				return io.ErrShortWrite
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if previous != nil {
		if err := temp.Chmod(previous.Mode()); err != nil {
			return err
		}
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}
