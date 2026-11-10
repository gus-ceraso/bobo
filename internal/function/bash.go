package function

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"ceraso.dev/bobo/internal/provider"
)

// Bash runs scripts in fresh /bin/bash shells without interactive input and
// returns the last 32 KiB of combined stdout and stderr. Its zero value is ready
// to use. Cancellation stops only the immediate shell; descendants may keep running.
type Bash struct{}

func (*Bash) Schema() map[string]any {
	return map[string]any{
		"type":        "function",
		"name":        "bash",
		"description": "Run a Bash script in a fresh shell. Returns the last 32KiB of combined stdout and stderr. Use background jobs for important or slow commands.",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{
					"type":        "string",
					"description": "Bash script to execute.",
				},
			},
			"required":             []string{"command"},
			"additionalProperties": false,
		},
		"strict": true,
	}
}

func (*Bash) Call(ctx context.Context, args map[string]any) []provider.Part {
	out := newToolResult("bash")
	command, err := stringArgument(args, "command")
	if err != nil {
		out.attribute("exit_code", -1)
		out.attribute("truncated", false)
		return out.status(err)
	}
	preview := previewText(command)
	out.attribute("command", preview)
	if strings.IndexByte(command, 0) >= 0 {
		out.attribute("exit_code", -1)
		out.attribute("truncated", false)
		return out.status(errors.New("invalid arguments: script contains a null byte"))
	}
	cmd := exec.CommandContext(ctx, "/bin/bash", "-c", command)
	// Keep the shell and its descendants out of bobo's foreground terminal
	// group. Keyboard Ctrl+C must reach bobo, not bypass shell-only cancellation.
	// CommandContext still kills only this shell, never its process group.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
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
	err = cmd.Run()
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
	if ctxErr := ctx.Err(); ctxErr != nil {
		exitCode = -1
		if ctxErr == context.DeadlineExceeded {
			exitCode = 124
		}
		err = ctxErr
	}
	truncated := len(body) > 1<<15
	if truncated {
		body = body[1:]
	}
	body = bytes.ToValidUTF8(body, []byte("\uFFFD"))
	if len(body) > 1<<15 {
		truncated = true
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
	out.attribute("exit_code", exitCode)
	out.attribute("truncated", truncated)
	return out.text(text, err)
}
