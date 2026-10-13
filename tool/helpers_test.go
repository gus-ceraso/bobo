package tool

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// parseResult splits a tool's Call result into its metadata, decoded into M,
// and the content parts that follow the metadata part. A string result is
// metadata only and has no content parts.
func parseResult[M any](t *testing.T, v any) (M, []map[string]any) {
	t.Helper()
	var m M
	var metaText string
	var parts []map[string]any
	switch v := v.(type) {
	case string:
		metaText = v
	case []map[string]any:
		if len(v) == 0 {
			t.Fatal("result has no parts")
		}
		if v[0]["type"] != "text" {
			t.Fatalf("first part type = %v, want text", v[0]["type"])
		}
		text, ok := v[0]["text"].(string)
		if !ok {
			t.Fatalf("first part text = %#v, want string", v[0]["text"])
		}
		metaText = text
		parts = v[1:]
	default:
		t.Fatalf("result type = %T, want string or []map[string]any", v)
	}
	if err := json.Unmarshal([]byte(metaText), &m); err != nil {
		t.Fatalf("decoding metadata %q: %v", metaText, err)
	}
	return m, parts
}

// partText returns the text of content part i, which must be a text part.
func partText(t *testing.T, parts []map[string]any, i int) string {
	t.Helper()
	if i >= len(parts) {
		t.Fatalf("got %d content parts, want index %d", len(parts), i)
	}
	if parts[i]["type"] != "text" {
		t.Fatalf("part %d type = %v, want text", i, parts[i]["type"])
	}
	text, ok := parts[i]["text"].(string)
	if !ok {
		t.Fatalf("part %d text = %#v, want string", i, parts[i]["text"])
	}
	return text
}

// checkError fails the test unless got is an error containing want.
func checkError(t *testing.T, got, want string) {
	t.Helper()
	if got == "" {
		t.Fatalf("error = none, want one containing %q", want)
	}
	if !strings.Contains(got, want) {
		t.Errorf("error = %q, want one containing %q", got, want)
	}
}

// checkNoError fails the test if got is an error.
func checkNoError(t *testing.T, got string) {
	t.Helper()
	if got != "" {
		t.Fatalf("error = %q, want none", got)
	}
}

// num returns n as the json.Number that integer arguments carry after
// openrouter decodes a tool call.
func num(n int) json.Number {
	return json.Number(strconv.Itoa(n))
}

// writeTestFile writes content to name relative to the current directory,
// creating parent directories as needed.
func writeTestFile(t *testing.T, name string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, content, 0o644); err != nil {
		t.Fatal(err)
	}
}

// minimalPDF returns a valid PDF with the given number of blank 200x200 pages.
func minimalPDF(pages int) []byte {
	kids := ""
	for i := 0; i < pages; i++ {
		kids += fmt.Sprintf("%d 0 R ", 3+i)
	}
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", kids, pages),
	}
	for i := 0; i < pages; i++ {
		objs = append(objs, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] >>")
	}
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs))
	for i, obj := range objs {
		offsets[i] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, off := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	return buf.Bytes()
}

// decodeImagePart checks that part is an image_url content part holding a
// base64 data URL of mediaType, and returns the decoded image bytes.
func decodeImagePart(t *testing.T, part map[string]any, mediaType string) []byte {
	t.Helper()
	if part["type"] != "image_url" {
		t.Fatalf("part type = %v, want image_url", part["type"])
	}
	img, ok := part["image_url"].(map[string]any)
	if !ok {
		t.Fatalf("image_url = %#v, want object", part["image_url"])
	}
	url, _ := img["url"].(string)
	prefix := "data:" + mediaType + ";base64,"
	if !strings.HasPrefix(url, prefix) {
		t.Fatalf("image URL prefix = %.40q, want %q", url, prefix)
	}
	b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(url, prefix))
	if err != nil {
		t.Fatalf("decoding image data: %v", err)
	}
	return b
}
