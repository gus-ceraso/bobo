package tool

import (
	"sort"
	"strings"
	"testing"
)

// schemaTool is implemented by every tool.
type schemaTool interface {
	Schema() map[string]any
}

// schemaProperties returns the properties of a schema, keyed by name.
func schemaProperties(t *testing.T, s map[string]any) map[string]map[string]any {
	t.Helper()
	params, ok := s["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("parameters = %T, want map[string]any", s["parameters"])
	}
	raw, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %T, want map[string]any", params["properties"])
	}
	props := make(map[string]map[string]any, len(raw))
	for name, v := range raw {
		p, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("property %s = %T, want map[string]any", name, v)
		}
		props[name] = p
	}
	return props
}

func TestToolSchemas(t *testing.T) {
	tests := []struct {
		tool       schemaTool
		name       string
		properties []string // sorted
		required   []string
	}{
		{&Copy{}, "copy", []string{"new_path", "old_path"}, []string{"old_path", "new_path"}},
		{&Edit{}, "edit", []string{"new_text", "old_text", "path"}, []string{"path", "old_text", "new_text"}},
		{&Exec{}, "exec", []string{"arguments", "limit", "path", "timeout"}, []string{"path"}},
		{&Fetch{}, "fetch", []string{"limit", "offset", "url"}, []string{"url"}},
		{&Find{}, "find", []string{"limit", "offset", "path", "pattern"}, []string{"pattern"}},
		{&Grep{}, "grep", []string{"limit", "offset", "path", "path_pattern", "pattern"}, []string{"pattern"}},
		{&Move{}, "move", []string{"new_path", "old_path"}, []string{"old_path", "new_path"}},
		{&Read{}, "read", []string{"limit", "offset", "path"}, []string{"path"}},
		{&Remove{}, "remove", []string{"path"}, []string{"path"}},
		{&Search{}, "search", []string{"query"}, []string{"query"}},
		{&Write{}, "write", []string{"path", "text"}, []string{"path", "text"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := tt.tool.Schema()
			if got := s["name"]; got != tt.name {
				t.Errorf("name = %v, want %q", got, tt.name)
			}
			if desc, _ := s["description"].(string); desc == "" {
				t.Error("description is empty")
			}
			params, _ := s["parameters"].(map[string]any)
			if params["type"] != "object" {
				t.Errorf("parameters type = %v, want object", params["type"])
			}
			props := schemaProperties(t, s)
			var names []string
			for name, p := range props {
				names = append(names, name)
				if _, ok := p["type"].(string); !ok {
					t.Errorf("property %s has no type", name)
				}
				if desc, _ := p["description"].(string); desc == "" {
					t.Errorf("property %s has no description", name)
				}
			}
			sort.Strings(names)
			if !equalStrings(names, tt.properties) {
				t.Errorf("properties = %q, want %q", names, tt.properties)
			}
			required, ok := params["required"].([]string)
			if !ok {
				t.Fatalf("required = %T, want []string", params["required"])
			}
			if !equalStrings(required, tt.required) {
				t.Errorf("required = %q, want %q", required, tt.required)
			}
			for _, r := range required {
				if _, ok := props[r]; !ok {
					t.Errorf("required property %s is not in properties", r)
				}
			}
		})
	}
}

// pathTool is a tool that takes paths, and the names of its path properties.
type pathTool struct {
	tool  schemaTool
	props []string
}

// pathTools returns the tools that take paths, with the path restrictions set.
func pathTools(localFiles, hiddenFiles bool) []pathTool {
	return []pathTool{
		{&Copy{LocalFiles: localFiles, HiddenFiles: hiddenFiles}, []string{"old_path", "new_path"}},
		{&Edit{LocalFiles: localFiles, HiddenFiles: hiddenFiles}, []string{"path"}},
		{&Find{LocalFiles: localFiles, HiddenFiles: hiddenFiles}, []string{"path"}},
		{&Grep{LocalFiles: localFiles, HiddenFiles: hiddenFiles}, []string{"path"}},
		{&Move{LocalFiles: localFiles, HiddenFiles: hiddenFiles}, []string{"old_path", "new_path"}},
		{&Read{LocalFiles: localFiles, HiddenFiles: hiddenFiles}, []string{"path"}},
		{&Remove{LocalFiles: localFiles, HiddenFiles: hiddenFiles}, []string{"path"}},
		{&Write{LocalFiles: localFiles, HiddenFiles: hiddenFiles}, []string{"path"}},
	}
}

// The path descriptions must state exactly the restrictions that are set.
func TestSchemaPathDescriptionsReflectRestrictions(t *testing.T) {
	const local = "Must be a relative path inside the working directory."
	const hidden = `Hidden files and directories (names starting with ".") aren't allowed.`
	tests := []struct {
		name                    string
		localFiles, hiddenFiles bool
		wantLocal, wantHidden   bool
	}{
		{"restricted", true, false, true, true},
		{"unrestricted", false, true, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, pt := range pathTools(tt.localFiles, tt.hiddenFiles) {
				s := pt.tool.Schema()
				props := schemaProperties(t, s)
				for _, name := range pt.props {
					desc, _ := props[name]["description"].(string)
					if got := strings.Contains(desc, local); got != tt.wantLocal {
						t.Errorf("%v %s: description %q has local sentence = %v, want %v", s["name"], name, desc, got, tt.wantLocal)
					}
					if got := strings.Contains(desc, hidden); got != tt.wantHidden {
						t.Errorf("%v %s: description %q has hidden sentence = %v, want %v", s["name"], name, desc, got, tt.wantHidden)
					}
				}
			}
		})
	}
}
