package tool

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
)

func argString(m map[string]any, k string) (string, bool) {
	v, ok := m[k]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	if !ok {
		return "", false
	}
	return s, true
}

func argInt(m map[string]any, k string) (int, bool) {
	v, ok := m[k]
	if !ok {
		return 0, false
	}
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	i, err := n.Int64()
	if err != nil {
		return 0, false
	}
	return int(i), true
}

func enc(obj any) string {
	b, err := json.Marshal(obj)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func hasHiddenComponent(path string) bool {
	for _, component := range strings.Split(filepath.ToSlash(filepath.Clean(path)), "/") {
		if strings.HasPrefix(component, ".") && component != "." && component != ".." {
			return true
		}
	}
	return false
}

func detectMediaType(file io.ReadSeeker) (string, error) {
	if _, err := file.Seek(0, 0); err != nil {
		return "", fmt.Errorf("lseek: %s", err)
	}
	head := make([]byte, 512)
	n, err := io.ReadFull(file, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read: %s", err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		return "", fmt.Errorf("lseek: %s", err)
	}
	return http.DetectContentType(head[:n]), nil
}

func containsAny(s string, substrs ...string) bool {
	for _, substr := range substrs {
		if strings.Contains(s, substr) {
			return true
		}
	}
	return false
}

func encImage(mediaType string, content []byte) string {
	return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(content)
}

// pathDescription appends the configured path restrictions to desc.
func pathDescription(desc string, localFiles, hiddenFiles bool) string {
	if localFiles {
		desc += " Must be a relative path inside the working directory."
	}
	if !hiddenFiles {
		desc += " Hidden files and directories (names starting with \".\") aren't allowed."
	}
	return desc
}

func argOptionalPositive(m map[string]any, k string) (int, error) {
	if _, ok := m[k]; !ok {
		return 0, nil
	}
	n, ok := argInt(m, k)
	if !ok || n < 1 {
		return 0, fmt.Errorf("invalid arguments: %s must be an integer >= 1", k)
	}
	return n, nil
}
