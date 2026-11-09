package function

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func FuzzEditStreamingLiteral(f *testing.F) {
	if testing.Short() {
		f.Skip("streaming edits scan files larger than 32 MiB")
	}
	for _, seed := range []struct {
		content, old, replacement string
		split                     uint16
	}{
		{"before old after", "old", "long replacement", 3},
		{"aaa", "aa", "x", 1},
		{"🙂tail", "🙂", "世界", 2},
		{"old old", "old", "new", 0},
		{"old\xff", "old", "new", 1},
		{"世界🙂", "世界🙂", "", 3},
		{"\x00old\x00", "old", "old", 0},
	} {
		f.Add(seed.content, seed.old, seed.replacement, seed.split)
	}
	f.Fuzz(func(t *testing.T, content, old, replacement string, split uint16) {
		if len(content) > 4096 || len(old) > 4096 || len(replacement) > 4096 ||
			old == "" || !utf8.ValidString(old) || !utf8.ValidString(replacement) || strings.ContainsRune(old, 0) {
			t.Skip()
		}
		// A sparse NUL envelope forces the streaming route without writing a
		// large fixture. A NUL-free needle can only match inside the payload.
		// Vary its position around a scan boundary to exercise retained bytes.
		const size = (32 << 20) + 1
		at := (1 << 15) - int(split%(1<<15))
		path := filepath.Join(t.TempDir(), "file")
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(size); err != nil {
			file.Close()
			t.Fatal(err)
		}
		if _, err := file.WriteAt([]byte(content), int64(at)); err != nil {
			file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		before, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}

		// Deliberately count overlapping matches with a simple byte-by-byte
		// oracle, independent of the streaming search and its overlap buffer.
		matches, match := 0, -1
		for i := 0; i+len(old) <= len(content); i++ {
			if content[i:i+len(old)] == old {
				matches++
				match = i
			}
		}
		valid := utf8.ValidString(content) && matches == 1
		want := content
		wantSize := size
		if valid {
			want = content[:match] + replacement + content[match+len(old):]
			wantSize += len(replacement) - len(old)
		}
		_, _, _, e := NewFileFunctions()
		result := invoke(t, e, context.Background(), map[string]any{"path": path, "old_text": old, "new_text": replacement})
		if valid {
			requireSuccess(t, result)
		} else if !utf8.ValidString(content) {
			// Duplicate matches may be detected before a later invalid byte.
			requireError(t, result, "")
		} else {
			requireError(t, result, "exactly once")
		}
		got := readFixture(t, path)
		if len(got) != wantSize {
			t.Fatalf("size = %d, want %d", len(got), wantSize)
		}
		if string(got[at:at+len(want)]) != want ||
			bytes.Count(got[:at], []byte{0}) != at ||
			bytes.Count(got[at+len(want):], []byte{0}) != len(got)-at-len(want) {
			t.Fatal("streaming edit disagrees with model or corrupted the NUL envelope")
		}
		after, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if (!valid || old == replacement) && !os.SameFile(before, after) {
			t.Fatal("failed or no-op streaming edit replaced the inode")
		}
		requireCleanDirectory(t, filepath.Dir(path))
		requireEmptyRegistry(t, e.locks)
	})
}
