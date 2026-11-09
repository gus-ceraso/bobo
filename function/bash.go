package function

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
	out := fmt.Sprintf("<bash date=%q", time.Now().Format(time.RFC3339))
	command, err := stringArgument(args, "command")
	if err != nil {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" exit_code=\"-1\" truncated=\"false\" error=%q/>", err.Error()))}}
	}
	preview := fmt.Sprintf("%.80s", command)
	if len(preview) < len(command) {
		preview += "..."
	}
	out += fmt.Sprintf(" command=%q", preview)
	if strings.IndexByte(command, 0) >= 0 {
		return []provider.Part{{ContentType: "text/plain", Content: []byte(out + " exit_code=\"-1\" truncated=\"false\" error=\"invalid arguments: script contains a null byte\"/>")}}
	}
	cmd := exec.CommandContext(ctx, "/bin/bash", "-c", command)
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
	message := ""
	if ctxErr := ctx.Err(); ctxErr != nil {
		exitCode = -1
		if ctxErr == context.DeadlineExceeded {
			exitCode = 124
		}
		message = ctxErr.Error()
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
	return []provider.Part{{ContentType: "text/plain", Content: []byte(out + fmt.Sprintf(" exit_code=\"%d\" truncated=\"%t\" error=%q>\n%s</bash>", exitCode, truncated, message, text))}}
}
