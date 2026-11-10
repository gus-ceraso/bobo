//go:build linux

package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"

	"ceraso.dev/bobo/internal/provider"
	"github.com/reeflective/readline"
	"github.com/reeflective/readline/inputrc"
	"golang.org/x/sys/unix"
)

var errEditPrompt = errors.New("edit prompt")

func newShell() *readline.Shell {
	rl := readline.NewShell(inputrc.WithApp("bobo"))
	for name, value := range map[string]any{
		"editing-mode": "emacs", "autocomplete": false, "history-autosuggest": false,
		"convert-meta": false, "output-meta": true,
		"transient-prompt": false, "usage-hint-always": false, "multiline-column": false,
		"multiline-column-numbered": false, "multiline-column-custom": "> ",
		"enable-bracketed-paste": true, "interrupt-bracketed-paste": true,
		"bell-style": "none", "autopairs": false,
		"blink-matching-paren": false, "comment-begin": "", "preserve-cursor": true,
		"cache-cursor-position": true,
		// Readline needs the absolute cursor row to reserve space at the bottom
		// of the terminal. Disabling this makes redraw overwrite prior output.
		"cursor-position-probe": true,
	} {
		_ = rl.Config.Set(name, value)
	}
	rl.Keymap.SetMain("emacs")
	rl.Prompt.Primary(func() string { return "> " })
	rl.Prompt.Secondary(func() string { return "> " })
	rl.Keymap.Register(map[string]func(){
		"bobo-newline": func() { rl.Cursor().InsertAt('\n') },
		"bobo-interrupt": func() {
			rl.Display.AcceptLine()
			rl.History.Accept(false, false, readline.ErrInterrupt)
		},
		"bobo-editor": func() {
			rl.Display.AcceptLine()
			rl.History.Accept(false, false, errEditPrompt)
		},
		"bobo-complete": func() { completePrompt(rl) },
	})
	// Bind after inputrc loading so application semantics do not depend on a
	// user's shell bindings. Ctrl+X Ctrl+E uses the same safe editor path.
	for sequence, action := range map[string]string{
		"\r": "accept-line", "\n": "bobo-newline", "\x03": "bobo-interrupt",
		"\x07": "bobo-editor", "\x18\x05": "bobo-editor", "\t": "bobo-complete",
		"\x04":     "end-of-file",
		"\x1b\x7f": "backward-kill-word", "\x1b\x08": "backward-kill-word",
	} {
		_ = rl.Config.Bind("emacs", sequence, action, false)
	}
	return rl
}

func console(ctx context.Context, client *provider.Client, session *provider.Session, stdout, stderr io.Writer) error {
	if file, ok := stdout.(*os.File); !ok || !isTerminal(file) {
		return errors.New("interactive mode requires terminal stdout; pipe stdin when redirecting output")
	}
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)
	fd := int(os.Stdin.Fd())
	original, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return err
	}
	// Readline owns the terminal while editing. Between Readline calls, keep
	// echo and CR translation off so fast typeahead remains intact.
	between := *original
	between.Iflag &^= unix.ICRNL
	between.Lflag &^= unix.ICANON | unix.ECHO
	between.Lflag |= unix.ISIG
	between.Cc[unix.VMIN], between.Cc[unix.VTIME] = 1, 0
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, &between); err != nil {
		return err
	}
	defer unix.IoctlSetTermios(fd, unix.TCSETS, original)
	rl := newShell()
	// While a turn runs, use the terminal's ordinary cooked input/echo rather
	// than a second readline prompt. No goroutine reads or edits that input.
	busy := *original
	busy.Iflag &^= unix.IGNCR | unix.INLCR
	busy.Iflag |= unix.ICRNL
	busy.Lflag |= unix.ICANON | unix.ECHO | unix.ISIG
	busy.Lflag &^= unix.NOFLSH
	var turnMu sync.Mutex
	var cancelTurn, cancelRead context.CancelFunc
	var interrupted atomic.Bool
	watchDone := make(chan struct{})
	watchStopped := make(chan struct{})
	go func() {
		defer close(watchDone)
		for {
			select {
			case <-ctx.Done():
				rl.Keys.RequestRefresh()
				return
			case <-interrupts:
				turnMu.Lock()
				if cancelTurn != nil {
					cancelTurn()
				} else {
					interrupted.Store(true)
					if cancelRead != nil {
						cancelRead()
					}
				}
				turnMu.Unlock()
				rl.Keys.RequestRefresh()
			case <-watchStopped:
				return
			}
		}
	}()
	defer func() { close(watchStopped); <-watchDone }()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		readCtx, stopRead := context.WithCancel(ctx)
		turnMu.Lock()
		cancelRead = stopRead
		if interrupted.Load() {
			stopRead()
		}
		turnMu.Unlock()
		text, err := rl.ReadlineContext(readCtx)
		turnMu.Lock()
		cancelRead = nil
		turnMu.Unlock()
		stopRead()
		switch {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			rl.Display.AcceptLine()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if !interrupted.Swap(false) {
				return err
			}
			if err := resumeInput(fd, &between, rl, true); err != nil {
				return err
			}
			continue
		case errors.Is(err, io.EOF):
			return nil
		case errors.Is(err, readline.ErrInterrupt):
			if err := resumeInput(fd, &between, rl, true); err != nil {
				return err
			}
			continue
		case errors.Is(err, errEditPrompt):
			if err := unix.IoctlSetTermios(fd, unix.TCSETS, original); err != nil {
				return err
			}
			edited, editErr := editPrompt(ctx, text)
			if err := unix.IoctlSetTermios(fd, unix.TCSETS, &between); err != nil {
				return err
			}
			if editErr != nil {
				printError(stderr, editErr)
				edited = text
			}
			rl.Line().Set([]rune(edited)...)
			rl.History.Accept(true, false, errEditPrompt)
			continue
		case err != nil:
			return err
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		turnCtx, cancel := context.WithCancel(ctx)
		turnMu.Lock()
		cancelTurn = cancel
		if interrupted.Swap(false) {
			cancel()
		}
		turnMu.Unlock()
		if err := unix.IoctlSetTermios(fd, unix.TCSETS, &busy); err != nil {
			cancel()
			return err
		}
		answer, turnErr := client.Prompt(turnCtx, session, text)
		// Delivery is part of the active turn: keep Ctrl+C and cooked terminal
		// signals working until even a blocked answer write has finished.
		var outputErr error
		if turnErr == nil {
			outputErr = writeAnswer(turnCtx, stdout, answer, true)
		}
		turnMu.Lock()
		cancelTurn = nil
		canceled := turnCtx.Err() != nil
		turnMu.Unlock()
		cancel()
		if err := resumeInput(fd, &between, rl, canceled); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if outputErr != nil {
			if canceled && (errors.Is(outputErr, context.Canceled) || errors.Is(outputErr, context.DeadlineExceeded)) {
				// Do not block again printing a diagnostic to the stalled tty.
				// The completed response remains in conversation history.
				continue
			}
			return outputErr
		}
		if turnErr != nil {
			if client.Logger != nil {
				client.Logger.Printf("event=application.turn_failed error=%q", turnErr)
			}
			printError(stderr, turnErr)
			continue
		}
	}
}

// resumeInput transfers cooked typeahead after the turn, including delivery.
// In cooked mode Enter becomes LF; convert only these buffered bytes back to
// Return, leaving Ctrl+J's normal draft-editing binding unchanged.
func resumeInput(fd int, between *unix.Termios, rl *readline.Shell, discard bool) error {
	drain := *between
	drain.Cc[unix.VMIN], drain.Cc[unix.VTIME] = 0, 0
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, &drain); err != nil {
		return err
	}
	var input []byte
	buf := make([]byte, 4096)
	var readErr error
	for {
		n, err := unix.Read(fd, buf)
		if n > 0 {
			input = append(input, buf[:n]...)
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			readErr = err
			break
		}
		if n == 0 {
			break
		}
	}
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, between); err != nil {
		return err
	}
	if readErr != nil {
		return readErr
	}
	if discard {
		for {
			if _, empty := rl.Keys.Pop(); empty {
				break
			}
		}
		return nil
	}
	for i := range input {
		if input[i] == '\n' {
			input[i] = '\r'
		}
	}
	rl.Keys.QueueInput(input)
	return nil
}
