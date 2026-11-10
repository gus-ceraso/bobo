//go:build linux

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf8"

	"ceraso.dev/bobo/internal/function"
	"ceraso.dev/bobo/internal/provider"
)

const defaultInstructions = "You are a helpful assistant. Always answer the user in UTF-8 plain text (NO MARKDOWN). Give concise answers in the active voice."

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGHUP)
	err := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stop()
	if err == nil || errors.Is(err, flag.ErrHelp) {
		return
	}
	if errors.Is(err, context.Canceled) {
		// Cancellation must not block again reporting to stderr, which may
		// share the same undrained pipe as stdout (2>&1).
		os.Exit(130)
	}
	printError(os.Stderr, err)
	os.Exit(1)
}

type options struct {
	signin                       bool
	model, effort, tier, logPath string
	compactThreshold             int
}

func parseOptions(args []string, stderr io.Writer) (options, error) {
	var o options
	fs := flag.NewFlagSet("bobo", flag.ContinueOnError)
	// Report parse errors once, through the same error path as other failures.
	fs.SetOutput(io.Discard)
	fs.BoolVar(&o.signin, "signin", false, "Sign in with ChatGPT before starting")
	fs.StringVar(&o.model, "model", "gpt-6.1-sol", "Model name")
	fs.StringVar(&o.effort, "effort", "xhigh", "Reasoning effort")
	fs.IntVar(&o.compactThreshold, "compact-threshold", 128000, "Native compaction token threshold")
	fs.StringVar(&o.tier, "service-tier", "default", "API service tier")
	fs.StringVar(&o.logPath, "log", "", "Append events to this file (disabled by default)")
	fs.Usage = func() {
		fs.SetOutput(stderr)
		fmt.Fprintln(stderr, "Usage: bobo [options]\n\nChat interactively, or read all piped stdin as one prompt.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if fs.NArg() != 0 {
		return o, errors.New("positional prompts are not supported; use interactive or piped input")
	}
	if strings.TrimSpace(o.model) == "" || strings.TrimSpace(o.effort) == "" || strings.TrimSpace(o.tier) == "" {
		return o, errors.New("model, effort, and service tier must not be empty")
	}
	if o.compactThreshold <= 0 {
		return o, errors.New("compact-threshold must be positive")
	}
	return o, nil
}

func run(ctx context.Context, args []string, stdin *os.File, stdout, stderr io.Writer) error {
	o, err := parseOptions(args, stderr)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var sink *logSink
	if o.logPath != "" {
		file, err := os.OpenFile(o.logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
		if err != nil {
			return fmt.Errorf("open log: %w", err)
		}
		defer file.Close()
		sink = &logSink{file: file, cancel: cancel}
	}
	config, err := os.UserConfigDir()
	if err != nil {
		return fmt.Errorf("locate configuration directory: %w", err)
	}
	config = filepath.Join(config, "bobo")
	store := &credentialStore{path: filepath.Join(config, "credentials.json"), fallback: os.Getenv("OPENAI_API_KEY")}
	client := provider.NewClient(store)
	if sink != nil {
		client.Logger = log.New(sink, "", log.LstdFlags|log.Lmicroseconds)
	}
	if o.signin {
		if err := signIn(ctx, client, stderr); err != nil {
			return loggedError(client, sink, err)
		}
	}
	// Validate the source before presenting an interactive prompt. Only an absent
	// file permits environment fallback; invalid/unreadable files are errors.
	credential, err := store.Load(ctx)
	if err == nil && credential.AccessToken == "" && credential.RefreshToken == "" && credential.FallbackToken == "" {
		err = provider.ErrNoCredentials
	}
	if err != nil {
		if errors.Is(err, provider.ErrNoCredentials) {
			err = fmt.Errorf("%w; run bobo -signin or set OPENAI_API_KEY", err)
		}
		return loggedError(client, sink, err)
	}
	text, err := instructions(config, func(err error) { printError(stderr, err) })
	if err != nil {
		return loggedError(client, sink, err)
	}
	r, v, w, e := function.NewFileFunctions()
	session := provider.NewSession(o.model, o.effort, text, []provider.Function{&function.Bash{}, r, v, w, e})
	session.CompactThreshold, session.ServiceTier = o.compactThreshold, o.tier
	if isTerminal(stdin) {
		err = console(ctx, client, session, stdout, stderr)
	} else {
		err = pipe(ctx, client, session, stdin, stdout)
	}
	return loggedError(client, sink, err)
}

func signIn(ctx context.Context, client *provider.Client, stderr io.Writer) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()
	url, done := client.SignIn(ctx)
	if url != "" {
		if _, err := fmt.Fprintln(stderr, url); err != nil {
			stop()
			<-done
			return err
		}
	}
	if err := <-done; err != nil {
		return fmt.Errorf("sign in: %w", err)
	}
	return nil
}

func instructions(config string, warn func(error)) (string, error) {
	text := defaultInstructions
	body, err := os.ReadFile(filepath.Join(config, "instructions.txt"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read instructions: %w", err)
	}
	if err == nil {
		if !utf8.Valid(body) {
			return "", errors.New("instructions.txt must contain valid UTF-8")
		}
		text += "\n\n" + string(body)
	}
	skills := loadSkills(filepath.Join(config, "skills"), warn)
	return text + formatSkills(skills), nil
}

func pipe(ctx context.Context, client *provider.Client, session *provider.Session, stdin io.Reader, stdout io.Writer) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()
	body, err := readAllContext(ctx, stdin)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}
	answer, err := client.Prompt(ctx, session, string(body))
	if err != nil {
		return err
	}
	return writeAnswer(ctx, stdout, answer, false)
}
