package tool

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

type Exec struct{}

func (_ *Exec) Schema() map[string]any {
	return map[string]any{
		"name":        "exec",
		"description": "Run a program in the working directory and return the last lines of its combined stdout and stderr, along with its exit code. The program runs directly, without a shell, so pipes, redirects, globs, and variables aren't expanded. To use them, run \"sh\" with the arguments \"-c\" and the command. Stdin is empty. Output that isn't text or has lines longer than 64 KiB isn't supported.",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Program to run. A name without a \"/\" is looked up in PATH.",
				},
				"arguments": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "string",
					},
					"description": "Arguments passed to the program as is, one per element, without shell quoting.",
				},
				"timeout": map[string]any{
					"type":        "integer",
					"description": "Seconds to wait before the program is killed. Defaults to 300.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum number of output lines to return, counted from the end. Defaults to 1000.",
				},
			},
			"required": []string{"path"},
		},
	}
}

func (_ *Exec) Call(args map[string]any) any {
	var metadata struct {
		Error     string   `json:"error,omitempty"`
		Path      string   `json:"path,omitempty"`
		Arguments []string `json:"arguments,omitempty"`
		Timeout   int      `json:"timeout,omitempty"`
		Limit     int      `json:"limit,omitempty"`
		ExitCode  *int     `json:"exit_code,omitempty"`
	}

	metadata.Path, _ = argString(args, "path")
	if len(metadata.Path) == 0 {
		metadata.Error = "invalid arguments: missing path string"
		return enc(metadata)
	}
	var arguments []string
	if argumentsAny, ok := args["arguments"]; ok {
		list, ok := argumentsAny.([]any)
		if !ok {
			metadata.Error = "invalid arguments: arguments must be a list of strings"
			return enc(metadata)
		}
		for _, item := range list {
			argument, ok := item.(string)
			if !ok {
				metadata.Error = "invalid arguments: arguments must be a list of strings"
				return enc(metadata)
			}
			arguments = append(arguments, argument)
		}
	}
	metadata.Arguments = arguments
	metadata.Timeout, _ = argInt(args, "timeout")
	if metadata.Timeout < 0 {
		metadata.Error = "invalid arguments: timeout must be >= 0 (default)"
		return enc(metadata)
	} else if metadata.Timeout == 0 {
		metadata.Timeout = 300
	} else if metadata.Timeout > 7200 {
		metadata.Error = fmt.Sprintf("invalid arguments: timeout must be <= 7200")
		return enc(metadata)
	}
	limit, err := argOptionalPositive(args, "limit")
	if err != nil {
		metadata.Error = err.Error()
		return enc(metadata)
	}
	metadata.Limit = limit
	if metadata.Limit == 0 {
		metadata.Limit = 1000
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(metadata.Timeout)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, metadata.Path, metadata.Arguments...)
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.CombinedOutput()
	if err != nil {
		metadata.Error = err.Error()
	}
	if cmd.ProcessState != nil {
		code := cmd.ProcessState.ExitCode()
		metadata.ExitCode = &code
	}
	if len(out) == 0 {
		return enc(metadata)
	}
	if !strings.Contains(http.DetectContentType(out), "text/") {
		metadata.Error = "output is not text"
		return enc(metadata)
	}
	var lines []string
	s := bufio.NewScanner(bytes.NewReader(out))
	for s.Scan() {
		lines = append(lines, s.Text())
		if len(lines) > metadata.Limit {
			lines = lines[1:]
		}
	}
	if err := s.Err(); err != nil {
		metadata.Error = fmt.Sprintf("scanning text: %s", err)
		return enc(metadata)
	}
	content := strings.Join(lines, "\n")
	return []map[string]any{
		map[string]any{
			"type": "text",
			"text": enc(metadata),
		},
		map[string]any{
			"type": "text",
			"text": content,
		},
	}
}
