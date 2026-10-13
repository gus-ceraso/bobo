package tool

import (
	"fmt"
	"strings"
	"testing"
)

type execMeta struct {
	Error    string `json:"error"`
	Timeout  int    `json:"timeout"`
	Limit    int    `json:"limit"`
	ExitCode *int   `json:"exit_code"`
}

func TestExecDefaults(t *testing.T) {
	meta, parts := parseResult[execMeta](t, (&Exec{}).Call(map[string]any{"path": "sh", "arguments": []any{"-c", "echo hi"}}))
	checkNoError(t, meta.Error)
	if meta.Timeout != 300 || meta.Limit != 1000 {
		t.Errorf("timeout, limit = %d, %d, want 300, 1000", meta.Timeout, meta.Limit)
	}
	if meta.ExitCode == nil || *meta.ExitCode != 0 {
		t.Errorf("exit code = %v, want 0", meta.ExitCode)
	}
	if got := partText(t, parts, 0); got != "hi" {
		t.Errorf("output = %q, want %q", got, "hi")
	}
}

func TestExecLimitMustBePositive(t *testing.T) {
	for _, v := range []any{num(0), num(-1), "5"} {
		t.Run(fmt.Sprint(v), func(t *testing.T) {
			args := map[string]any{"path": "sh", "arguments": []any{"-c", "echo hi"}, "limit": v}
			meta, parts := parseResult[execMeta](t, (&Exec{}).Call(args))
			checkError(t, meta.Error, "limit must be an integer >= 1")
			if len(parts) != 0 {
				t.Errorf("got %d content parts, want none", len(parts))
			}
		})
	}
}

func TestExecLimitReturnsLastLines(t *testing.T) {
	args := map[string]any{"path": "sh", "arguments": []any{"-c", "printf 'one\\ntwo\\nthree\\nfour\\n'"}, "limit": num(2)}
	meta, parts := parseResult[execMeta](t, (&Exec{}).Call(args))
	checkNoError(t, meta.Error)
	if meta.Limit != 2 {
		t.Errorf("limit = %d, want 2", meta.Limit)
	}
	if got := partText(t, parts, 0); got != "three\nfour" {
		t.Errorf("output = %q, want %q", got, "three\nfour")
	}
}

func TestExecCapturesStdoutAndStderr(t *testing.T) {
	meta, parts := parseResult[execMeta](t, (&Exec{}).Call(map[string]any{"path": "sh", "arguments": []any{"-c", "echo out; echo err >&2"}}))
	checkNoError(t, meta.Error)
	if got := partText(t, parts, 0); got != "out\nerr" {
		t.Errorf("output = %q, want %q", got, "out\nerr")
	}
}

func TestExecNonzeroExitCode(t *testing.T) {
	meta, parts := parseResult[execMeta](t, (&Exec{}).Call(map[string]any{"path": "sh", "arguments": []any{"-c", "echo oops; exit 3"}}))
	checkError(t, meta.Error, "exit status 3")
	if meta.ExitCode == nil || *meta.ExitCode != 3 {
		t.Errorf("exit code = %v, want 3", meta.ExitCode)
	}
	if got := partText(t, parts, 0); got != "oops" {
		t.Errorf("output = %q, want %q", got, "oops")
	}
}

func TestExecNonTextOutputRejected(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "bin.dat", []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"))
	meta, parts := parseResult[execMeta](t, (&Exec{}).Call(map[string]any{"path": "cat", "arguments": []any{"bin.dat"}}))
	checkError(t, meta.Error, "output is not text")
	if len(parts) != 0 {
		t.Errorf("got %d content parts, want none", len(parts))
	}
}

func TestExecProgramNotFound(t *testing.T) {
	tests := []struct {
		path    string
		wantErr string
	}{
		{"bobo-no-such-program", "executable file not found"},
		{"./bobo-no-such-program", "no such file or directory"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			meta, parts := parseResult[execMeta](t, (&Exec{}).Call(map[string]any{"path": tt.path}))
			checkError(t, meta.Error, tt.wantErr)
			if meta.ExitCode != nil {
				t.Errorf("exit code = %d, want none", *meta.ExitCode)
			}
			if len(parts) != 0 {
				t.Errorf("got %d content parts, want none", len(parts))
			}
		})
	}
}

func TestExecTimeoutKillsProcess(t *testing.T) {
	args := map[string]any{"path": "sleep", "arguments": []any{"30"}, "timeout": num(1)}
	meta, parts := parseResult[execMeta](t, (&Exec{}).Call(args))
	checkError(t, meta.Error, "signal: killed")
	if meta.Timeout != 1 {
		t.Errorf("timeout = %d, want 1", meta.Timeout)
	}
	if meta.ExitCode == nil || *meta.ExitCode != -1 {
		t.Errorf("exit code = %v, want -1", meta.ExitCode)
	}
	if len(parts) != 0 {
		t.Errorf("got %d content parts, want none", len(parts))
	}
}

func TestExecNegativeTimeoutRejected(t *testing.T) {
	meta, parts := parseResult[execMeta](t, (&Exec{}).Call(map[string]any{"path": "sh", "arguments": []any{"-c", "echo hi"}, "timeout": num(-1)}))
	checkError(t, meta.Error, "timeout must be >= 0")
	if len(parts) != 0 {
		t.Errorf("got %d content parts, want none", len(parts))
	}
}

func TestExecZeroTimeoutUsesDefault(t *testing.T) {
	meta, parts := parseResult[execMeta](t, (&Exec{}).Call(map[string]any{"path": "sh", "arguments": []any{"-c", "echo hi"}, "timeout": num(0)}))
	checkNoError(t, meta.Error)
	if meta.Timeout != 300 {
		t.Errorf("timeout = %d, want 300", meta.Timeout)
	}
	if got := partText(t, parts, 0); got != "hi" {
		t.Errorf("output = %q, want %q", got, "hi")
	}
}

// A timeout above the 7200-second maximum is rejected.
func TestExecTimeoutTooLargeRejected(t *testing.T) {
	meta, parts := parseResult[execMeta](t, (&Exec{}).Call(map[string]any{"path": "sh", "arguments": []any{"-c", "echo hi"}, "timeout": num(7201)}))
	checkError(t, meta.Error, "timeout must be <= 7200")
	if len(parts) != 0 {
		t.Errorf("got %d content parts, want none", len(parts))
	}
}

// The 7200-second maximum is accepted.
func TestExecLargestTimeoutAccepted(t *testing.T) {
	meta, parts := parseResult[execMeta](t, (&Exec{}).Call(map[string]any{"path": "sh", "arguments": []any{"-c", "echo hi"}, "timeout": num(7200)}))
	checkNoError(t, meta.Error)
	if meta.Timeout != 7200 {
		t.Errorf("timeout = %d, want 7200", meta.Timeout)
	}
	if got := partText(t, parts, 0); got != "hi" {
		t.Errorf("output = %q, want %q", got, "hi")
	}
}

// Arguments that are not all strings must be rejected. They must not be
// silently dropped, which would run the program with no arguments.
func TestExecInvalidArgumentsRejected(t *testing.T) {
	tests := []struct {
		name string
		args any
	}{
		{"non-string element", []any{"-c", num(5)}},
		{"not a list", "-c"},
		{"null", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta, parts := parseResult[execMeta](t, (&Exec{}).Call(map[string]any{"path": "sh", "arguments": tt.args}))
			checkError(t, meta.Error, "arguments")
			if len(parts) != 0 {
				t.Errorf("got %d content parts, want none", len(parts))
			}
		})
	}
}

// A successful run with no output returns a plain string result.
func TestExecEmptyOutputReturnsString(t *testing.T) {
	res := (&Exec{}).Call(map[string]any{"path": "true"})
	if _, ok := res.(string); !ok {
		t.Fatalf("result type = %T, want string", res)
	}
	meta, parts := parseResult[execMeta](t, res)
	checkNoError(t, meta.Error)
	if meta.ExitCode == nil || *meta.ExitCode != 0 {
		t.Errorf("exit code = %v, want 0", meta.ExitCode)
	}
	if len(parts) != 0 {
		t.Errorf("got %d content parts, want none", len(parts))
	}
}

// Stdin is empty, so a program that reads it returns at once with no output.
func TestExecStdinIsEmpty(t *testing.T) {
	meta, parts := parseResult[execMeta](t, (&Exec{}).Call(map[string]any{"path": "cat", "timeout": num(5)}))
	checkNoError(t, meta.Error)
	if len(parts) != 0 {
		t.Errorf("got %d content parts, want none", len(parts))
	}
}

func TestExecMissingPath(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
	}{
		{"missing", map[string]any{}},
		{"empty", map[string]any{"path": ""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta, parts := parseResult[execMeta](t, (&Exec{}).Call(tt.args))
			checkError(t, meta.Error, "missing path string")
			if len(parts) != 0 {
				t.Errorf("got %d content parts, want none", len(parts))
			}
		})
	}
}

// Output lines longer than 64 KiB aren't supported.
func TestExecLineTooLong(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "wide.txt", []byte(strings.Repeat("x", 70*1024)+"\n"))
	meta, parts := parseResult[execMeta](t, (&Exec{}).Call(map[string]any{"path": "cat", "arguments": []any{"wide.txt"}}))
	checkError(t, meta.Error, "scanning text")
	if len(parts) != 0 {
		t.Errorf("got %d content parts, want none", len(parts))
	}
}
