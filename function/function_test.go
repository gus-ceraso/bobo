package function

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"ceraso.dev/bobo/provider"
)

func TestSchemas(t *testing.T) {
	r, v, w, e := NewFileFunctions()
	for _, tc := range []struct {
		fn       provider.Function
		name     string
		required []string
	}{
		{r, "read", []string{"path", "show_line_numbers", "offset", "limit"}},
		{v, "view", []string{"path", "crop", "horizontal_flip", "vertical_flip", "rotate", "scale", "grayscale", "invert", "brightness", "contrast", "saturation", "gamma", "blur", "sharpness"}},
		{w, "write", []string{"path", "text"}},
		{e, "edit", []string{"path", "old_text", "new_text"}},
		{&Bash{}, "bash", []string{"command"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema := tc.fn.Schema()
			if schema["name"] != tc.name || schema["type"] != "function" || schema["strict"] != true || schema["description"] == "" {
				t.Fatalf("invalid function schema: %#v", schema)
			}
			params := schema["parameters"].(map[string]any)
			if params["type"] != "object" || params["additionalProperties"] != false || !reflect.DeepEqual(params["required"], tc.required) {
				t.Fatalf("invalid parameters: %#v", params)
			}
			properties := params["properties"].(map[string]any)
			if len(properties) != len(tc.required) {
				t.Fatalf("unexpected properties: %#v", properties)
			}
			for _, name := range tc.required {
				property, ok := properties[name].(map[string]any)
				if !ok || property["type"] == nil || property["description"] == "" {
					t.Fatalf("invalid property %s: %#v", name, property)
				}
			}
			if _, err := json.Marshal(schema); err != nil {
				t.Fatal(err)
			}
			schema["name"] = "mutated"
			properties[tc.required[0]] = nil
			if tc.fn.Schema()["name"] != tc.name || tc.fn.Schema()["parameters"].(map[string]any)["properties"].(map[string]any)[tc.required[0]] == nil {
				t.Fatal("Schema exposes shared mutable maps")
			}
		})
	}
}

func TestFileFunctionValidation(t *testing.T) {
	r, v, w, e := NewFileFunctions()
	for _, fn := range []provider.Function{r, v, w, e} {
		t.Run(functionName(fn), func(t *testing.T) {
			for _, args := range []map[string]any{nil, {"path": nil}, {"path": 1}, {"path": ""}, {"path": "\xff"}} {
				requireError(t, invoke(t, fn, context.Background(), args), "invalid arguments")
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			requireError(t, invoke(t, fn, ctx, functionArgs("missing")), context.Canceled.Error())
		})
	}
	for _, tc := range []struct {
		fn    provider.Function
		key   string
		value any
	}{
		{r, "show_line_numbers", "true"}, {r, "offset", 1.0}, {r, "offset", json.Number("0")}, {r, "limit", json.Number("-1")},
		{w, "text", nil}, {w, "text", false}, {w, "text", "\xff"},
		{e, "old_text", ""}, {e, "old_text", nil}, {e, "old_text", "\xff"},
		{e, "new_text", nil}, {e, "new_text", "\xff"},
		{e, "old_text", strings.Repeat("x", (1<<15)+1)}, {e, "new_text", strings.Repeat("x", (1<<15)+1)},
	} {
		t.Run(fmt.Sprintf("%s/%s/%T", functionName(tc.fn), tc.key, tc.value), func(t *testing.T) {
			args := functionArgs("missing")
			args[tc.key] = tc.value
			requireError(t, invoke(t, tc.fn, context.Background(), args), "invalid arguments")
		})
	}
	for _, fn := range []provider.Function{w, e} {
		args := functionArgs("missing")
		key := "text"
		if fn == e {
			key = "new_text"
		}
		delete(args, key)
		requireError(t, invoke(t, fn, context.Background(), args), "missing "+key)
	}
}

func TestNonregularFilesRejected(t *testing.T) {
	dir := t.TempDir()
	regular := filepath.Join(dir, "regular")
	writeFixture(t, regular, []byte("old"))
	link := filepath.Join(dir, "link")
	dangling := filepath.Join(dir, "dangling")
	fifo := filepath.Join(dir, "fifo")
	for name, target := range map[string]string{link: regular, dangling: filepath.Join(dir, "absent")} {
		if err := os.Symlink(target, name); err != nil {
			t.Fatal(err)
		}
	}
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "socket")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	paths := []string{dir, link, dangling, fifo, socket, "/dev/null", filepath.Join(dir, "absent"), dir + "/", dir + "/.", dir + "/.."}
	r, v, w, e := NewFileFunctions()
	for _, fn := range []provider.Function{r, v, w, e} {
		for _, path := range paths {
			if fn == w && filepath.Base(path) == "absent" {
				continue
			}
			t.Run(functionName(fn)+"/"+filepath.Base(path), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
				defer cancel()
				result := invoke(t, fn, ctx, functionArgs(path))
				requireError(t, result, "")
				if strings.Contains(result.attrs["error"], "deadline") {
					t.Fatal("opening a nonregular file blocked")
				}
				requireEmptyRegistry(t, r.locks)
			})
		}
	}
	if string(readFixture(t, regular)) != "old" {
		t.Fatal("rejected symlink write modified its target")
	}
	requireCleanDirectory(t, dir)
}

func TestUninitializedFileFunctions(t *testing.T) {
	for _, fn := range []provider.Function{&Read{}, &View{}, &Write{}, &Edit{}} {
		requireError(t, invoke(t, fn, context.Background(), functionArgs("file")), "use NewFileFunctions")
	}
}

func TestPreviewsOnErrors(t *testing.T) {
	_, _, w, e := NewFileFunctions()
	text := strings.Repeat("🙂", 81)
	dir := t.TempDir()
	result := invoke(t, w, context.Background(), map[string]any{"path": dir, "text": text})
	requireError(t, result, "regular file")
	assertAttribute(t, result, "text", wantPreview(text))
	result = invoke(t, e, context.Background(), map[string]any{"path": filepath.Join(dir, "absent"), "old_text": text, "new_text": text + "new"})
	requireError(t, result, "")
	assertAttribute(t, result, "old_text", wantPreview(text))
	assertAttribute(t, result, "new_text", wantPreview(text+"new"))
	command := ": #" + text + "\x00"
	result = invoke(t, &Bash{}, context.Background(), map[string]any{"command": command})
	requireError(t, result, "null byte")
	assertAttribute(t, result, "command", wantPreview(command))
}

func TestPreviewAttributes(t *testing.T) {
	r, _, w, e := NewFileFunctions()
	for _, value := range []string{"", "short", strings.Repeat("x", 79), strings.Repeat("x", 80), strings.Repeat("x", 81), strings.Repeat("🙂", 80), strings.Repeat("🙂", 81), strings.Repeat("\"\\\n<>🙂", 20)} {
		t.Run(fmt.Sprintf("%d/%x", len([]rune(value)), []byte(value[:min(len(value), 4)])), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "quote\" newline\n <file>")
			result := invoke(t, w, context.Background(), map[string]any{"path": path, "text": value})
			requireSuccess(t, result)
			assertAttribute(t, result, "path", path)
			assertAttribute(t, result, "text", wantPreview(value))
			if string(readFixture(t, path)) != value {
				t.Fatal("preview truncated the actual file")
			}
			if value != "" {
				result = invoke(t, e, context.Background(), map[string]any{"path": path, "old_text": value, "new_text": value + "extra"})
				requireSuccess(t, result)
				assertAttribute(t, result, "old_text", wantPreview(value))
				assertAttribute(t, result, "new_text", wantPreview(value+"extra"))
				if string(readFixture(t, path)) != value+"extra" {
					t.Fatal("preview truncated the actual edit")
				}
			}
			result = invoke(t, r, context.Background(), map[string]any{"path": path})
			requireSuccess(t, result)
		})
	}
}
