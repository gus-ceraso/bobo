# bobo

A small, Linux-only command-line agent with shell, file, image, and web-search
tools. Conversations stay in memory and end when bobo exits; there are no saved
sessions or slash commands.

Bobo runs tools without approvals or a sandbox. Use it only in an environment
you trust, and set up any sandbox before starting it.

## Build and install

Use Linux with `/proc` mounted, `/bin/bash`, and a Go version compatible with
`go.mod`.

The CLI lives in `cmd/bobo`, with private packages under `internal/function`
and `internal/provider`. Run it with `go run ./cmd/bobo`, or build `./bobo`:

```sh
make          # Build ./bobo
make check    # Run go vet and tests with the race detector
make install  # Install to /usr/local/bin (may require elevated permissions)
```

For a user-local installation, run `make install PREFIX="$HOME/.local"`.
`BINDIR` overrides the executable directory, `DESTDIR` supports staged installs,
and `GO` selects the Go command. `make clean` removes the built executable.

## Authentication

To sign in with ChatGPT:

```sh
./bobo -signin
```

Open the authorization URL printed to stderr. Bobo saves the credentials and
then starts normally. It never opens a browser or signs in without `-signin`.
Ctrl+C cancels sign-in.

Configuration lives in `$XDG_CONFIG_HOME/bobo`, or `~/.config/bobo` when
`XDG_CONFIG_HOME` is unset. Keep `credentials.json` private (`chmod 600`), and do
not delete `credentials.json.lock` while bobo runs.

Alternatively, set `OPENAI_API_KEY` for API-only use. Bobo reads it only when
`credentials.json` is absent and does not save it during ordinary API-only runs.
An invalid or unreadable credential file is an error. Bobo does not load `.env`.

ChatGPT credentials refresh automatically. A saved `fallback_token` in
`credentials.json` enables API-token fallback after a refresh failure or a
ChatGPT usage limit. To save an API fallback during your first sign-in, set
`OPENAI_API_KEY` for that run. Once selected, fallback stays active until bobo
exits; a new run prefers ChatGPT again. API quota errors, ordinary rate limits,
and Responses API authentication errors do not select fallback.

## Usage

```sh
./bobo
printf 'Explain UTF-8 in one sentence.\n' | ./bobo
printf 'Your prompt here.\n' | ./bobo > answers.txt
./bobo -log events.log
```

| Option | Default | Purpose |
| --- | --- | --- |
| `-signin` | false | Sign in before starting |
| `-model` | `gpt-6.1-sol` | Model name |
| `-effort` | `xhigh` | Reasoning effort |
| `-compact-threshold` | `128000` | Token threshold for conversation compaction |
| `-service-tier` | `default` | API service tier, sent unchanged |
| `-log` | disabled | Append conversation events and errors to a file |

Use `./bobo -h` to show options. Positional prompt arguments are not supported.

### Interactive mode

Interactive mode requires terminal input and output. Bobo shows a `> ` prompt
and displays only final answers, followed by a terminal bell. It hides reasoning,
commentary, and tool calls/results.

- Enter submits the draft; Ctrl+J inserts a newline.
- Tab completes filesystem paths, including `~/` paths and paths with spaces.
- Alt+Backspace deletes the previous word.
- Ctrl+G or Ctrl+X Ctrl+E opens the draft in `$VISUAL`, then `$EDITOR`, then `vi`.
  Saving and closing returns to the draft without submitting it. Editor settings
  may include arguments.
- Ctrl+C discards the draft or cancels the active turn and buffered input.
- Ctrl+D exits on an empty draft; otherwise it deletes at the cursor. It does not
  cancel an active turn.

Multiline paste is supported. While bobo works, the terminal echoes and buffers
input until the next prompt; Enter submits that buffered input when the prompt
returns. There is no draft editor during a turn.

History stays in memory. Restart bobo to clear it. Failed or canceled turns retain
completed conversation events; the next prompt starts a new turn rather than
retrying the previous one. Canceling answer delivery can truncate the displayed
text without removing the completed answer from history.

### Piped input

With nonterminal input, bobo reads all of stdin as one prompt, prints the final
answer to stdout, and exits without a prompt or bell. Errors and sign-in
information go to stderr. To redirect answers, pipe the input too; terminal input
with redirected output is not supported. Canceling a piped run exits with status
130.

## Instructions and skills

Bobo requests concise UTF-8 plain-text answers without Markdown. Add personal
instructions to `instructions.txt` in the configuration directory. Bobo appends
them to its built-in instructions at startup; they do not replace those defaults.

Place each skill in `skills/<directory>/SKILL.md` under the configuration
directory, for example:

```markdown
---
name: example
description: Describe what this skill does and when to use it.
---

Instructions and references to supporting files go here.
```

The name defaults to the directory name; a nonempty string description is
required. Bobo skips invalid skills with a diagnostic. For duplicate names, the
first valid skill in directory-name order wins. Other metadata, including
`disable-model-invocation`, is ignored.

Discovery is not recursive and skips hidden directories. Immediate skill
directories may be symlinks; project-local skills and standalone Markdown files
are not loaded. Bobo loads instructions and skill metadata once at startup. The
assistant reads a skill's full contents when relevant and resolves supporting
paths relative to its `SKILL.md`.

## Logging and safety

Nothing is logged by default. `-log filename` appends events and errors to a file;
bobo creates new logs with private permissions and stops if logging fails. Logs
include conversation content and tool outputs, including images. Protect them as
sensitive data; they cannot resume a session.

The assistant can run `bash`, `read`, `view`, `write`, and `edit` with your user
permissions. Review personal instructions and skills before trusting them. Bash
commands have no interactive input. Cancellation stops only the immediate shell,
so background jobs and other descendants may keep running after a canceled turn.

## Tests

Run `make check`, or `go test ./...` for tests without the race detector. Tests
use mock HTTP responses and Linux terminal emulation; they need no credentials
and make no live API calls.
