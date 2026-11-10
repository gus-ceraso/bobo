package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSkillsShallowDiscovery(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "a", "SKILL.md"), "---\nname: alpha\ndescription: |\n  First line\n  Second line\ndisable-model-invocation: true\n---\nBODY MUST NOT ENTER PROMPT")
	writeTestFile(t, filepath.Join(root, "a", "nested", "SKILL.md"), "---\nname: nested\ndescription: Hidden\n---")
	writeTestFile(t, filepath.Join(root, "group", "nested", "SKILL.md"), "---\ndescription: Also hidden\n---")
	writeTestFile(t, filepath.Join(root, "standalone.md"), "---\ndescription: Hidden\n---")
	writeTestFile(t, filepath.Join(root, "b", "SKILL.md"), "---\ndescription: Name falls back\n---")
	writeTestFile(t, filepath.Join(root, "duplicate", "SKILL.md"), "---\nname: alpha\ndescription: Duplicate\n---")
	writeTestFile(t, filepath.Join(root, "missing", "SKILL.md"), "---\nname: missing\n---")
	writeTestFile(t, filepath.Join(root, "broken", "SKILL.md"), "---\ndescription: [\n---")
	var warnings []error
	skills := loadSkills(root, func(err error) { warnings = append(warnings, err) })
	if len(skills) != 2 || skills[0].name != "alpha" || skills[1].name != "b" || len(warnings) != 3 {
		t.Fatalf("skills=%+v warnings=%v", skills, warnings)
	}
	text := formatSkills(skills)
	if !strings.Contains(text, "First line\nSecond line") || strings.Contains(text, "BODY MUST") || !strings.Contains(text, skills[0].path) {
		t.Fatal(text)
	}
}

func TestSkillSymlinksAndMissingDirectory(t *testing.T) {
	root, target := t.TempDir(), t.TempDir()
	writeTestFile(t, filepath.Join(target, "SKILL.md"), "\ufeff---\r\ndescription: External\r\n---\r\nBody")
	for _, name := range []string{"a", "b"} {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(root, filepath.Join(root, "cycle")); err != nil {
		t.Fatal(err)
	}
	skills := loadSkills(root, func(err error) { t.Error(err) })
	if len(skills) != 1 || skills[0].path != filepath.Join(target, "SKILL.md") {
		t.Fatalf("%+v", skills)
	}
	if got := loadSkills(filepath.Join(root, "absent"), func(err error) { t.Error(err) }); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestSkillMetadataErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "SKILL.md")
	for _, body := range []string{
		"No frontmatter", "---\ndescription: unterminated", "---\ndescription: 123\n---", "---\ndescription: false\n---", "---\ndescription: ''\n---", "---\ndescription: [one, two]\n---", "---\ndescription: bad\xff\n---",
	} {
		writeTestFile(t, path, body)
		if _, err := readSkill(path, "test"); err == nil {
			t.Fatalf("accepted %q", body)
		}
	}
}

func TestInstructions(t *testing.T) {
	root := t.TempDir()
	got, err := instructions(root, func(err error) { t.Error(err) })
	if err != nil || got != defaultInstructions {
		t.Fatalf("%q %v", got, err)
	}
	writeTestFile(t, filepath.Join(root, "instructions.txt"), "Personal instructions\n")
	writeTestFile(t, filepath.Join(root, "skills", "example", "SKILL.md"), "---\nname: 'x<&'\ndescription: 'Use <this> & that'\n---\nSECRET BODY")
	got, err = instructions(root, func(err error) { t.Error(err) })
	if err != nil || !strings.HasPrefix(got, defaultInstructions+"\n\nPersonal instructions\n\n\n") || !strings.Contains(got, "x&lt;&amp;") || strings.Contains(got, "SECRET BODY") {
		t.Fatalf("%q %v", got, err)
	}
	writeTestFile(t, filepath.Join(root, "instructions.txt"), "\xff")
	if _, err := instructions(root, func(err error) {}); err == nil {
		t.Fatal("invalid UTF-8 accepted")
	}
}
