//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// readAllContext reads inherited streams without blocking a syscall, changing
// shared descriptor flags, closing the caller's file, or leaving a goroutine
// behind. Closing os.Stdin cannot interrupt a blocking inherited pipe read.
// Regular files and non-file readers retain their ordinary Read semantics.
func readAllContext(ctx context.Context, reader io.Reader) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, ok := reader.(*os.File)
	if !ok {
		return io.ReadAll(reader)
	}
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Mode().IsRegular() {
		return io.ReadAll(file)
	}
	raw, err := file.SyscallConn()
	if err != nil {
		return nil, err
	}
	pipe := info.Mode()&os.ModeNamedPipe != 0
	socket := info.Mode()&os.ModeSocket != 0
	fd := -1
	var openErr error
	if err := raw.Control(func(source uintptr) {
		flags, err := unix.FcntlInt(source, unix.F_GETFL, 0)
		if err != nil {
			openErr = err
			return
		}
		if flags&unix.O_ACCMODE == unix.O_WRONLY || flags&unix.O_PATH != 0 {
			openErr = unix.EBADF
			return
		}
		if pipe || socket {
			fd, openErr = unix.FcntlInt(source, unix.F_DUPFD_CLOEXEC, 0)
		} else {
			fd, openErr = unix.Open(fmt.Sprintf("/proc/self/fd/%d", source), unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOCTTY, 0)
		}
	}); err != nil {
		return nil, err
	}
	if openErr != nil {
		return nil, fmt.Errorf("open cancellable input: %w", openErr)
	}
	defer unix.Close(fd)

	read := func(buf []byte) (int, error) { return unix.Read(fd, buf) }
	if socket {
		read = func(buf []byte) (int, error) {
			n, _, err := unix.Recvfrom(fd, buf, unix.MSG_DONTWAIT)
			return n, err
		}
	} else if pipe {
		// A dup alone shares O_NONBLOCK. Instead, nonblocking pipe-to-pipe
		// splice transfers available bytes into our own staging pipe. This
		// also supports inherited pipes whose permissions forbid reopening.
		stage := make([]int, 2)
		if err := unix.Pipe2(stage, unix.O_NONBLOCK|unix.O_CLOEXEC); err != nil {
			return nil, err
		}
		defer unix.Close(stage[0])
		defer unix.Close(stage[1])
		read = func(buf []byte) (int, error) {
			n, err := unix.Splice(fd, nil, stage[1], nil, len(buf), unix.SPLICE_F_NONBLOCK)
			if n <= 0 {
				return 0, err
			}
			// No other reader can consume the bytes just staged, so draining
			// exactly n bytes cannot wait for the producer.
			copied := 0
			for copied < int(n) {
				m, err := unix.Read(stage[0], buf[copied:int(n)])
				if m > 0 {
					copied += m
				}
				if errors.Is(err, unix.EINTR) {
					continue
				}
				if err != nil {
					return copied, err
				}
				if m == 0 {
					return copied, io.ErrUnexpectedEOF
				}
			}
			return copied, nil
		}
	}

	var body []byte
	buf := make([]byte, 32<<10)
	for {
		if err := ctx.Err(); err != nil {
			return body, err
		}
		n, err := read(buf)
		if n > 0 {
			body = append(body, buf[:n]...)
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
			fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
			_, err = unix.Poll(fds, 50)
			if errors.Is(err, unix.EINTR) {
				continue
			}
			if err != nil {
				return body, err
			}
			if fds[0].Revents&unix.POLLNVAL != 0 {
				return body, unix.EBADF
			}
			continue
		}
		if err != nil || n == 0 {
			return body, err
		}
	}
}
