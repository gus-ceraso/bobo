package tool

import (
	"os"
	"path/filepath"
	"testing"
)

type editMeta struct {
	Error string `json:"error"`
	Path  string `json:"path"`
}

// The schema requires new_text. Omitting it must not delete old_text.
func TestEditMissingNewTextLeavesFileUnchanged(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "f.txt", []byte("keep this\n"))
	meta, _ := parseResult[editMeta](t, (&Edit{LocalFiles: true}).Call(map[string]any{"path": "f.txt", "old_text": "keep "}))
	checkError(t, meta.Error, "missing new_text string")
	got, err := os.ReadFile("f.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "keep this\n" {
		t.Errorf("file = %q, want unchanged %q", got, "keep this\n")
	}
}

func TestEditNonStringNewTextRejected(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "f.txt", []byte("keep this\n"))
	meta, _ := parseResult[editMeta](t, (&Edit{LocalFiles: true}).Call(map[string]any{"path": "f.txt", "old_text": "keep ", "new_text": 5}))
	checkError(t, meta.Error, "missing new_text string")
	got, err := os.ReadFile("f.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "keep this\n" {
		t.Errorf("file = %q, want unchanged %q", got, "keep this\n")
	}
}

// An explicit empty new_text is valid and deletes the matched text.
func TestEditEmptyNewTextDeletesMatch(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "f.txt", []byte("keep this\n"))
	meta, _ := parseResult[editMeta](t, (&Edit{LocalFiles: true}).Call(map[string]any{"path": "f.txt", "old_text": "keep ", "new_text": ""}))
	checkNoError(t, meta.Error)
	got, err := os.ReadFile("f.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "this\n" {
		t.Errorf("file = %q, want %q", got, "this\n")
	}
}

func TestEditReplacesSingleMatch(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "f.txt", []byte("hello world\nbye\n"))
	meta, parts := parseResult[editMeta](t, (&Edit{LocalFiles: true}).Call(map[string]any{"path": "f.txt", "old_text": "world", "new_text": "there"}))
	checkNoError(t, meta.Error)
	if meta.Path != "f.txt" {
		t.Errorf("path = %q, want %q", meta.Path, "f.txt")
	}
	if len(parts) != 0 {
		t.Errorf("got %d content parts, want none", len(parts))
	}
	got, err := os.ReadFile("f.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello there\nbye\n" {
		t.Errorf("file = %q, want %q", got, "hello there\nbye\n")
	}
}

func TestEditMultiLineMatch(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "f.txt", []byte("one\ntwo\nthree\n"))
	meta, _ := parseResult[editMeta](t, (&Edit{LocalFiles: true}).Call(map[string]any{"path": "f.txt", "old_text": "one\ntwo", "new_text": "1\n2"}))
	checkNoError(t, meta.Error)
	got, err := os.ReadFile("f.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "1\n2\nthree\n" {
		t.Errorf("file = %q, want %q", got, "1\n2\nthree\n")
	}
}

// old_text matching is literal, case-sensitive, and includes whitespace. It
// must occur exactly once, and the file is left unchanged otherwise.
func TestEditOldTextMatchRules(t *testing.T) {
	t.Chdir(t.TempDir())
	tests := []struct {
		name    string
		content string
		oldText string
		wantErr string
	}{
		{"not found", "foo one\n", "bar", "does not contain old_text"},
		{"case differs", "Hello\n", "hello", "does not contain old_text"},
		{"whitespace differs", "foo one\n", "foo  one", "does not contain old_text"},
		{"regexp syntax is literal", "abc\n", "a.c", "does not contain old_text"},
		{"more than once", "foo one\nfoo two\n", "foo", "does not contain old_text exactly once"},
		{"overlapping matches", "aaa\n", "aa", "does not contain old_text exactly once"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writeTestFile(t, "f.txt", []byte(tt.content))
			meta, _ := parseResult[editMeta](t, (&Edit{LocalFiles: true}).Call(map[string]any{"path": "f.txt", "old_text": tt.oldText, "new_text": "X"}))
			checkError(t, meta.Error, tt.wantErr)
			got, err := os.ReadFile("f.txt")
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.content {
				t.Errorf("file = %q, want unchanged %q", got, tt.content)
			}
		})
	}
}

// In a file with CRLF line endings, old_text and new_text are converted to
// CRLF, so LF-only input still matches and the replacement keeps CRLF.
func TestEditCRLFFile(t *testing.T) {
	t.Chdir(t.TempDir())
	tests := []struct {
		name    string
		oldText string
		newText string
		want    string
	}{
		{"single line", "b", "B", "a\r\nB\r\nc\r\n"},
		{"multi line with LF", "a\nb", "x\ny", "x\r\ny\r\nc\r\n"},
		{"multi line with CRLF", "a\r\nb", "x\r\ny", "x\r\ny\r\nc\r\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writeTestFile(t, "f.txt", []byte("a\r\nb\r\nc\r\n"))
			meta, _ := parseResult[editMeta](t, (&Edit{LocalFiles: true}).Call(map[string]any{"path": "f.txt", "old_text": tt.oldText, "new_text": tt.newText}))
			checkNoError(t, meta.Error)
			got, err := os.ReadFile("f.txt")
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Errorf("file = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEditRejectsDirectoryBinaryAndMissingFiles(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "dir/inner.txt", []byte("keep\n"))
	writeTestFile(t, "data.bin", []byte("keep\x00\x01\x02\x03"))
	tests := []struct {
		name    string
		path    string
		wantErr string
	}{
		{"directory", "dir", "open:"},
		{"binary file", "data.bin", "is not a text file"},
		{"missing file", "nope.txt", "open:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta, _ := parseResult[editMeta](t, (&Edit{LocalFiles: true}).Call(map[string]any{"path": tt.path, "old_text": "keep", "new_text": "x"}))
			checkError(t, meta.Error, tt.wantErr)
		})
	}
	if got, _ := os.ReadFile("dir/inner.txt"); string(got) != "keep\n" {
		t.Errorf("dir/inner.txt = %q, want unchanged", got)
	}
	if got, _ := os.ReadFile("data.bin"); string(got) != "keep\x00\x01\x02\x03" {
		t.Errorf("data.bin = %q, want unchanged", got)
	}
}

func TestEditPathRestrictions(t *testing.T) {
	t.Chdir(t.TempDir())
	outside := filepath.Join(t.TempDir(), "outside.txt")
	args := func(path string) map[string]any {
		return map[string]any{"path": path, "old_text": "A=1", "new_text": "A=2"}
	}
	t.Run("hidden path rejected", func(t *testing.T) {
		writeTestFile(t, ".env", []byte("A=1\n"))
		meta, _ := parseResult[editMeta](t, (&Edit{}).Call(args(".env")))
		checkError(t, meta.Error, "hidden path not allowed")
		if got, _ := os.ReadFile(".env"); string(got) != "A=1\n" {
			t.Errorf(".env = %q, want unchanged", got)
		}
	})
	t.Run("hidden path allowed", func(t *testing.T) {
		writeTestFile(t, ".env", []byte("A=1\n"))
		meta, _ := parseResult[editMeta](t, (&Edit{HiddenFiles: true}).Call(args(".env")))
		checkNoError(t, meta.Error)
		if got, _ := os.ReadFile(".env"); string(got) != "A=2\n" {
			t.Errorf(".env = %q, want %q", got, "A=2\n")
		}
	})
	t.Run("non-local path rejected", func(t *testing.T) {
		writeTestFile(t, outside, []byte("A=1\n"))
		for _, p := range []string{"../f.txt", outside} {
			meta, _ := parseResult[editMeta](t, (&Edit{LocalFiles: true}).Call(args(p)))
			checkError(t, meta.Error, "non-local path not allowed")
		}
		if got, _ := os.ReadFile(outside); string(got) != "A=1\n" {
			t.Errorf("outside file = %q, want unchanged", got)
		}
	})
	t.Run("outside path allowed without LocalFiles", func(t *testing.T) {
		writeTestFile(t, outside, []byte("A=1\n"))
		meta, _ := parseResult[editMeta](t, (&Edit{}).Call(args(outside)))
		checkNoError(t, meta.Error)
		if got, _ := os.ReadFile(outside); string(got) != "A=2\n" {
			t.Errorf("outside file = %q, want %q", got, "A=2\n")
		}
	})
}

func TestEditMissingOrEmptyOldTextRejected(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "f.txt", []byte("keep this\n"))
	tests := []struct {
		name    string
		args    map[string]any
		wantErr string
	}{
		{"missing path", map[string]any{"old_text": "keep", "new_text": "x"}, "missing path string"},
		{"missing old_text", map[string]any{"path": "f.txt", "new_text": "x"}, "missing old_text string"},
		{"empty old_text", map[string]any{"path": "f.txt", "old_text": "", "new_text": "x"}, "missing old_text string"},
		{"non-string old_text", map[string]any{"path": "f.txt", "old_text": 5, "new_text": "x"}, "missing old_text string"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta, _ := parseResult[editMeta](t, (&Edit{LocalFiles: true}).Call(tt.args))
			checkError(t, meta.Error, tt.wantErr)
			got, err := os.ReadFile("f.txt")
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != "keep this\n" {
				t.Errorf("file = %q, want unchanged %q", got, "keep this\n")
			}
		})
	}
}

// A replacement shorter than old_text must not leave bytes from the old
// content behind.
func TestEditShorterReplacementTruncatesFile(t *testing.T) {
	t.Chdir(t.TempDir())
	writeTestFile(t, "f.txt", []byte("keep XXXXXXXXXX tail\nend\n"))
	meta, _ := parseResult[editMeta](t, (&Edit{LocalFiles: true}).Call(map[string]any{"path": "f.txt", "old_text": "XXXXXXXXXX", "new_text": "y"}))
	checkNoError(t, meta.Error)
	got, err := os.ReadFile("f.txt")
	if err != nil {
		t.Fatal(err)
	}
	if want := "keep y tail\nend\n"; string(got) != want {
		t.Errorf("file = %q, want %q", got, want)
	}
}

// Only regular files can be edited. os.DevNull is a character device that
// opens for writing, so the check after open is what rejects it.
func TestEditRejectsNonRegularFile(t *testing.T) {
	meta, _ := parseResult[editMeta](t, (&Edit{}).Call(map[string]any{"path": os.DevNull, "old_text": "x", "new_text": "y"}))
	checkError(t, meta.Error, "is not a text file")
}
