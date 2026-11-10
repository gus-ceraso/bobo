//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

func writeAnswer(ctx context.Context, w io.Writer, answer string, bell bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !strings.HasSuffix(answer, "\n") {
		answer += "\n"
	}
	if bell {
		answer += "\a"
	}
	var n int
	var err error
	if file, ok := w.(*os.File); ok {
		n, err = writeFileContext(ctx, file, []byte(answer))
	} else {
		n, err = io.WriteString(w, answer)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil && n != len(answer) {
		return io.ErrShortWrite
	}
	return err
}

// writeFileContext keeps stream output nonblocking, without changing the
// caller's descriptor flags or leaving a writer goroutine behind. Reopening a
// Linux proc fd gives pipes/terminals a private open-file description; a dup
// alone would share O_NONBLOCK with the caller and other inherited descriptors.
// Regular files retain their existing offset and append semantics.
func writeFileContext(ctx context.Context, file *os.File, body []byte) (int, error) {
	info, err := file.Stat()
	if err != nil {
		return 0, err
	}
	if info.Mode().IsRegular() {
		return file.Write(body)
	}
	raw, err := file.SyscallConn()
	if err != nil {
		return 0, err
	}
	fd := -1
	socket := info.Mode()&os.ModeSocket != 0
	var openErr error
	if err := raw.Control(func(source uintptr) {
		flags, err := unix.FcntlInt(source, unix.F_GETFL, 0)
		if err != nil {
			openErr = err
			return
		}
		if flags&unix.O_ACCMODE == unix.O_RDONLY || flags&unix.O_PATH != 0 {
			openErr = unix.EBADF
			return
		}
		if socket {
			// Sockets cannot be reopened through proc. MSG_DONTWAIT below is
			// per-call, so a duplicate is safe without changing shared flags.
			fd, openErr = unix.FcntlInt(source, unix.F_DUPFD_CLOEXEC, 0)
		} else {
			fd, openErr = unix.Open(fmt.Sprintf("/proc/self/fd/%d", source), unix.O_WRONLY|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOCTTY, 0)
		}
	}); err != nil {
		return 0, err
	}
	if openErr != nil {
		if errors.Is(openErr, unix.EACCES) || errors.Is(openErr, unix.EPERM) {
			return 0, fmt.Errorf("unsupported stdout: inherited stream must be reopenable through /proc/self/fd with write permission: %w", openErr)
		}
		return 0, fmt.Errorf("open cancellable output: %w", openErr)
	}
	defer unix.Close(fd)
	n := 0
	for n < len(body) {
		if err := ctx.Err(); err != nil {
			return n, err
		}
		var written int
		if socket {
			written, err = unix.SendmsgN(fd, body[n:], nil, nil, unix.MSG_DONTWAIT|unix.MSG_NOSIGNAL)
		} else {
			written, err = unix.Write(fd, body[n:])
		}
		if written > 0 {
			n += written
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
			fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLOUT}}
			// A bounded wait observes cancellation even if the consumer never
			// drains stdout. No blocked write syscall needs to be interrupted.
			_, err = unix.Poll(fds, 50)
			if errors.Is(err, unix.EINTR) {
				continue
			}
			if err != nil {
				return n, err
			}
			if fds[0].Revents&unix.POLLNVAL != 0 {
				return n, unix.EBADF
			}
			continue
		}
		if err != nil {
			return n, err
		}
		if written == 0 {
			return n, io.ErrShortWrite
		}
	}
	return n, nil
}
