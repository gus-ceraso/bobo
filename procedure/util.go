package procedure

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
)

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

func mediaType(file io.ReadSeeker) (string, error) {
	if _, err := file.Seek(0, 0); err != nil {
		return "", fmt.Errorf("lseek: %s", err)
	}
	head := make([]byte, 512)
	if _, err := io.ReadFull(file, head); err != nil {
		return "", fmt.Errorf("read: %s", err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		return "", fmt.Errorf("lseek: %s", err)
	}
	return http.DetectContentType(head), nil
}
