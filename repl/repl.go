package repl

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/term"
)

var ErrNotInteractive = errors.New("called from a non-interactive environment")

func REPL(eval func(string) (string, error)) error {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return ErrNotInteractive
	}

	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("enable raw mode: %s", err)
	}
	defer term.Restore(fd, oldState)

	t := term.NewTerminal(struct {
		io.Reader
		io.Writer
	}{os.Stdin, os.Stdout}, "> ")

	lastW, lastH, err := term.GetSize(fd)
	if err == nil {
		_ = t.SetSize(lastW, lastH)
	}
	quitResize := make(chan struct{})
	doneResize := make(chan struct{})
	go func() {
		defer close(doneResize)
		tick := time.NewTicker(250 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-quitResize:
				return
			case <-tick.C:
			}
			if w, h, err := term.GetSize(fd); err == nil && (w != lastW || h != lastH) {
				_ = t.SetSize(w, h)
				lastW, lastH = w, h
			}
		}
	}()
	defer func() {
		close(quitResize)
		<-doneResize
	}()

	for {
		var input string
		for {
			line, err := t.ReadLine()
			if errors.Is(err, io.EOF) {
				return nil
			} else if err != nil {
				return fmt.Errorf("read: %s", err)
			}
			if strings.HasSuffix(line, "\\") {
				input += line[:len(line)-1] + "\n"
			} else {
				input += line
				break
			}
		}
		if input == "" {
			continue
		}
		output, err := eval(input)
		if err != nil {
			output = fmt.Sprintf("%s\neval: %s", output, err)
		}
		if _, err := fmt.Fprintln(t, output); err != nil {
			return fmt.Errorf("print: %s", err)
		}
	}
}
