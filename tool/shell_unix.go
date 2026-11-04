//go:build unix && !linux

package tool

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"

	"ceraso.dev/bobo/provider"
)

type Shell struct{}

func (*Shell) Schema() map[string]any {
	return map[string]any{
		"type":        "custom",
		"name":        "shell",
		"description": "Run a POSIX sh script in a fresh shell. Returns the last 32KiB of combined stdout and stderr. Invalid UTF-8 is replaced with �. Times out after 5 minutes.",
	}
}

func (*Shell) Call(in string) []provider.Part {
	if !utf8.ValidString(in) {
		return []provider.Part{{ContentType: "text/plain", Content: []byte("<shell exit_code=\"-1\" truncated=\"false\" error=\"invalid arguments: invalid utf-8\"/>")}}
	}
	if strings.IndexByte(in, 0) >= 0 {
		return []provider.Part{{ContentType: "text/plain", Content: []byte("<shell exit_code=\"-1\" truncated=\"false\" error=\"invalid arguments: script contains a null byte\"/>")}}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", in)
	cmd.WaitDelay = 5 * time.Second
	reader, writer := io.Pipe()
	defer reader.Close()
	cmd.Stdout = writer
	cmd.Stderr = writer
	body := make([]byte, (1<<15)+1)
	size, pos := 0, 0
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			n, err := reader.Read(body[pos:])
			size = min(size+n, len(body))
			pos = (pos + n) % len(body)
			if err != nil {
				return
			}
		}
	}()
	err := cmd.Run()
	writer.Close()
	<-readDone
	if size == len(body) {
		body = append(body[pos:], body[:pos]...)
	} else {
		body = body[:size]
	}

	exitCode := -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	message := ""
	if ctx.Err() != nil {
		exitCode = 124
		message = "timed out after 5m"
	} else if err != nil {
		message = err.Error()
	}
	truncated := len(body) > 1<<15
	if truncated {
		body = body[1:]
	}
	body = bytes.ToValidUTF8(body, []byte("\uFFFD"))
	if len(body) > 1<<15 {
		truncated = true
		// Replacement can expand the output; keep a suffix at a rune boundary.
		start := len(body) - (1 << 15)
		for !utf8.RuneStart(body[start]) {
			start++
		}
		body = body[start:]
	}
	text := string(body)
	if len(text) > 0 && text[len(text)-1] != '\n' && len(text) < 1<<15 {
		text += "\n"
	}
	return []provider.Part{{ContentType: "text/plain", Content: []byte(fmt.Sprintf("<shell exit_code=\"%d\" truncated=\"%t\" error=%q>\n%s</shell>", exitCode, truncated, message, text))}}
}
