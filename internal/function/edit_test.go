package function

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

func TestEdit(t *testing.T) {
	for _, tc := range []struct {
		name, content, old, replacement, want, err string
	}{
		{"grow", "before OLD after", "OLD", "long replacement", "before long replacement after", ""},
		{"shrink", "before long replacement after", "long replacement", "x", "before x after", ""},
		{"delete", "before OLD after", "OLD", "", "before  after", ""},
		{"whole file", "old", "old", "new", "new", ""},
		{"first", "old suffix", "old", "new", "new suffix", ""},
		{"last", "prefix old", "old", "new", "prefix new", ""},
		{"multiline Unicode", "世界\n🙂\nend", "世界\n🙂", "hello\n\x00", "hello\n\x00\nend", ""},
		{"literal", "before a.*[x] after", "a.*[x]", "$1", "before $1 after", ""},
		{"unchanged", "old", "old", "old", "old", ""},
		{"empty file", "", "old", "new", "", "exactly once"},
		{"missing", "text", "absent", "new", "text", "exactly once"},
		{"case sensitive", "OLD", "old", "new", "OLD", "exactly once"},
		{"duplicate", "old old", "old", "new", "old old", "exactly once"},
		{"overlap", "aaa", "aa", "new", "aaa", "exactly once"},
		{"duplicate no-op", "old old", "old", "old", "old old", "exactly once"},
		{"invalid UTF8", "old\xff", "old", "new", "old\xff", "valid utf-8"},
		{"invalid no-op", "old\xff", "old", "old", "old\xff", "valid utf-8"},
		{"maximum old", strings.Repeat("Q", 1<<15), strings.Repeat("Q", 1<<15), "new", "new", ""},
		{"maximum new", "old", "old", strings.Repeat("Q", 1<<15), strings.Repeat("Q", 1<<15), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, e := NewFileFunctions()
			path := filepath.Join(t.TempDir(), "file")
			writeFixture(t, path, []byte(tc.content))
			before, _ := os.Stat(path)
			result := invoke(t, e, context.Background(), map[string]any{"path": path, "old_text": tc.old, "new_text": tc.replacement})
			if tc.err != "" {
				requireError(t, result, tc.err)
			} else {
				requireSuccess(t, result)
			}
			if got := string(readFixture(t, path)); got != tc.want {
				t.Fatalf("contents = %.200q, want %.200q", got, tc.want)
			}
			after, _ := os.Stat(path)
			if (tc.err != "" || tc.old == tc.replacement) && !os.SameFile(before, after) {
				t.Fatal("failed or no-op edit replaced the inode")
			}
			requireCleanDirectory(t, filepath.Dir(path))
			requireEmptyRegistry(t, e.locks)
		})
	}
}

func TestEditAtomicityAndPermissions(t *testing.T) {
	_, _, _, e := NewFileFunctions()
	dir := t.TempDir()
	path := filepath.Join(dir, "file")
	writeFixture(t, path, []byte("before old after"))
	if err := os.Chmod(path, 0o751); err != nil {
		t.Fatal(err)
	}
	original, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	before, _ := original.Stat()
	requireSuccess(t, invoke(t, e, context.Background(), functionArgs(path)))
	after, err := os.Stat(path)
	if err != nil || os.SameFile(before, after) || after.Mode().Perm() != 0o751 {
		t.Fatalf("edit was not atomic or lost permissions: %v, %v", after, err)
	}
	oldData, err := io.ReadAll(original)
	if err != nil || string(oldData) != "before old after" {
		t.Fatal("edit changed the original descriptor's contents")
	}
}

func TestEditStreamingBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, old, replacement string
		size, at               int
	}{
		{"one-byte file", "Q", "new", 1, 0},
		{"one byte", "Q", "new", (2 << 15) + 17, 0},
		{"two bytes crossing chunk", "QR", "", (2 << 15) + 17, (1 << 15) - 1},
		{"UTF8 crossing chunk", "🙂", "世界", (2 << 15) + 17, (1 << 15) - 2},
		{"three-byte rune", "世", "🙂replacement", (2 << 15) + 17, (1 << 15) - 1},
		{"maximum needle", strings.Repeat("Q", 1<<15), "x", (2 << 15) + 17, (1 << 15) - 7},
		{"exact full final chunk", "needle", "replacement", 2 << 15, 2<<15 - 6},
		{"EOF match", "needle", "replacement", (2 << 15) + 17, (2 << 15) + 11},
		{"no-op", "🙂", "🙂", (2 << 15) + 17, (1 << 15) - 1},
		{"large file", "needle", "replacement", (32 << 20) + 17, (32 << 20) + 11},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if testing.Short() && tc.size > 32<<20 {
				t.Skip("large-file edit")
			}
			_, _, _, e := NewFileFunctions()
			path := filepath.Join(t.TempDir(), "file")
			source := bytes.Repeat([]byte("x"), tc.size)
			copy(source[tc.at:], tc.old)
			writeFixture(t, path, source)
			result := invoke(t, e, context.Background(), map[string]any{"path": path, "old_text": tc.old, "new_text": tc.replacement})
			requireSuccess(t, result)
			got := readFixture(t, path)
			if len(got) != len(source)-len(tc.old)+len(tc.replacement) || !bytes.Equal(got[:tc.at], source[:tc.at]) || string(got[tc.at:tc.at+len(tc.replacement)]) != tc.replacement || !bytes.Equal(got[tc.at+len(tc.replacement):], source[tc.at+len(tc.old):]) {
				t.Fatal("streamed replacement corrupted contents")
			}
			requireCleanDirectory(t, filepath.Dir(path))
			requireEmptyRegistry(t, e.locks)
		})
	}
}

func TestEditStreamingRejectsInvalidFiles(t *testing.T) {
	for _, kind := range []string{"missing", "duplicate", "overlap", "invalid byte", "incomplete EOF", "overlong UTF8", "surrogate"} {
		t.Run(kind, func(t *testing.T) {
			_, _, _, e := NewFileFunctions()
			path := filepath.Join(t.TempDir(), "file")
			data := bytes.Repeat([]byte("x"), 2<<15)
			old := "needle"
			wantErr := "exactly once"
			switch kind {
			case "duplicate":
				copy(data[100:], old)
				copy(data[len(data)-len(old):], old)
			case "overlap":
				copy(data[(1<<15)-1:], "QQQ")
				old = "QQ"
			case "invalid byte":
				data[(1<<15)-1] = 0xff
				wantErr = "valid utf-8"
			case "incomplete EOF":
				data[len(data)-1] = 0xf0
				wantErr = "valid utf-8"
			case "overlong UTF8":
				copy(data[(1<<15)-1:], "\xc0\xaf")
				wantErr = "valid utf-8"
			case "surrogate":
				copy(data[(1<<15)-1:], "\xed\xa0\x80")
				wantErr = "valid utf-8"
			}
			writeFixture(t, path, data)
			before, _ := os.Stat(path)
			requireError(t, invoke(t, e, context.Background(), map[string]any{"path": path, "old_text": old, "new_text": "new"}), wantErr)
			after, _ := os.Stat(path)
			if !os.SameFile(before, after) || !bytes.Equal(data, readFixture(t, path)) {
				t.Fatal("failed streaming edit changed the file")
			}
			requireCleanDirectory(t, filepath.Dir(path))
		})
	}
}

func TestConcurrentEditsAreNotLost(t *testing.T) {
	r, _, _, e := NewFileFunctions()
	dir := t.TempDir()
	path := filepath.Join(dir, "file")
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, path, []byte("anchor"))
	start := make(chan struct{})
	outputs := make(chan []byte, 32)
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Go(func() {
			<-start
			p := path
			if i%2 == 0 {
				p = filepath.Join(alias, "file")
			}
			parts := e.Call(context.Background(), map[string]any{"path": p, "old_text": "anchor", "new_text": "anchor!"})
			outputs <- parts[0].Content
		})
	}
	close(start)
	wg.Wait()
	close(outputs)
	for out := range outputs {
		if !strings.HasSuffix(string(out), " error=\"\"/>") {
			t.Errorf("concurrent edit failed: %s", out)
		}
	}
	if string(readFixture(t, path)) != "anchor"+strings.Repeat("!", 32) {
		t.Fatal("concurrent edits lost an update")
	}
	requireEmptyRegistry(t, r.locks)
	requireCleanDirectory(t, dir)
}

func TestEditRenameFailureCleansUp(t *testing.T) {
	_, _, _, e := NewFileFunctions()
	dir := t.TempDir()
	path := filepath.Join(dir, "file")
	writeFixture(t, path, []byte("old"))
	original, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	var setupErr error
	changed := false
	ctx := &observingContext{Context: context.Background(), onErr: func() {
		matches, err := filepath.Glob(filepath.Join(dir, ".bobo-*"))
		if err != nil {
			setupErr = err
		}
		if !changed && len(matches) != 0 {
			changed = true
			setupErr = os.Remove(path)
			if setupErr == nil {
				setupErr = os.Mkdir(path, 0o755)
			}
		}
	}}
	result := invoke(t, e, ctx, functionArgs(path))
	if setupErr != nil || !changed {
		t.Fatalf("failure setup did not run: %v", setupErr)
	}
	requireError(t, result, "")
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		t.Fatalf("conflicting directory was changed: %v", err)
	}
	data, err := io.ReadAll(original)
	if err != nil || string(data) != "old" {
		t.Fatalf("old inode was changed: %q, %v", data, err)
	}
	requireCleanDirectory(t, dir)
	requireEmptyRegistry(t, e.locks)
}

func FuzzEditLiteral(f *testing.F) {
	for _, seed := range [][3]string{{"aaa", "aa", "x"}, {"before old after", "old", "new"}, {"世界🙂", "🙂", ""}, {"old old", "old", "new"}, {"old\xff", "old", "new"}} {
		f.Add(seed[0], seed[1], seed[2])
	}
	f.Fuzz(func(t *testing.T, content, old, replacement string) {
		if len(content) > 4096 || len(old) > 4096 || len(replacement) > 4096 || old == "" || !utf8.ValidString(old) || !utf8.ValidString(replacement) {
			t.Skip()
		}
		_, _, _, e := NewFileFunctions()
		path := filepath.Join(t.TempDir(), "file")
		writeFixture(t, path, []byte(content))
		result := invoke(t, e, context.Background(), map[string]any{"path": path, "old_text": old, "new_text": replacement})
		i := strings.Index(content, old)
		valid := utf8.ValidString(content) && i >= 0 && !strings.Contains(content[i+1:], old)
		want := content
		if valid {
			requireSuccess(t, result)
			want = content[:i] + replacement + content[i+len(old):]
		} else {
			requireError(t, result, "")
		}
		if string(readFixture(t, path)) != want {
			t.Fatal("literal replacement disagrees with reference model")
		}
		requireCleanDirectory(t, filepath.Dir(path))
		requireEmptyRegistry(t, e.locks)
	})
}
