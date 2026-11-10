//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"unicode/utf8"
)

func editPrompt(ctx context.Context, text string) (string, error) {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()
	file, err := os.CreateTemp("", "bobo-prompt-*.txt")
	if err != nil {
		return "", err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := io.WriteString(file, text); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "vi"
	}
	// Editor settings are trusted shell commands, including their arguments.
	// The file path is passed positionally; prompt contents are never shell code.
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "exec "+editor+" \"$@\"", "bobo-editor", file.Name())
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("editor: %w", err)
	}
	body, err := os.ReadFile(file.Name())
	if err != nil {
		return "", err
	}
	if !utf8.Valid(body) {
		return "", errors.New("edited prompt must contain valid UTF-8")
	}
	// Strip only the conventional final line ending added by text editors.
	return strings.TrimSuffix(strings.TrimSuffix(string(body), "\n"), "\r"), nil
}
