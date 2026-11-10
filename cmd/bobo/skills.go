package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

type skill struct {
	name, description, path string
}

// loadSkills inspects only skills/*/SKILL.md. Supporting files and nested skill
// directories are deliberately not searched. Symlinked immediate directories
// are supported; there is no recursive walk and therefore no symlink cycle walk.
func loadSkills(root string, warn func(error)) []skill {
	var skills []skill
	entries, err := os.ReadDir(root) // Sorted by filename: collision winners are stable.
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			warn(fmt.Errorf("read skills: %w", err))
		}
		return nil
	}
	seenNames, seenPaths := map[string]bool{}, map[string]bool{}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		info, err := os.Stat(dir)
		if err != nil {
			warn(fmt.Errorf("inspect skill directory %s: %w", dir, err))
			continue
		}
		if !info.IsDir() {
			continue
		}
		path := filepath.Join(dir, "SKILL.md")
		canonical, err := filepath.EvalSymlinks(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			warn(fmt.Errorf("resolve skill %s: %w", path, err))
			continue
		}
		canonical, err = filepath.Abs(canonical)
		if err != nil {
			warn(err)
			continue
		}
		if seenPaths[canonical] {
			continue
		}
		seenPaths[canonical] = true
		// Use the canonical location in the prompt, so supporting relative paths
		// resolve beside the actual SKILL.md, even through a directory symlink.
		s, err := readSkill(canonical, entry.Name())
		if err != nil {
			warn(fmt.Errorf("skill %s: %w", path, err))
			continue
		}
		if seenNames[s.name] {
			warn(fmt.Errorf("skill %s: duplicate name %q; keeping the first skill", path, s.name))
			continue
		}
		seenNames[s.name] = true
		skills = append(skills, s)
	}
	return skills
}

func readSkill(path, directoryName string) (skill, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return skill{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return skill{}, err
	}
	if !info.Mode().IsRegular() {
		return skill{}, errors.New("SKILL.md must be a regular file")
	}
	// Only metadata is needed at startup. Read through the closing delimiter,
	// not the full skill body, which may contain substantial reference material.
	var frontmatter bytes.Buffer
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	if !scanner.Scan() || strings.TrimPrefix(scanner.Text(), "\ufeff") != "---" {
		return skill{}, errors.New("missing YAML frontmatter")
	}
	closed := false
	for scanner.Scan() {
		if scanner.Text() == "---" {
			closed = true
			break
		}
		if frontmatter.Len()+len(scanner.Bytes())+1 > 1<<20 {
			return skill{}, errors.New("skill frontmatter exceeds 1 MiB")
		}
		frontmatter.Write(scanner.Bytes())
		frontmatter.WriteByte('\n')
	}
	if err := scanner.Err(); err != nil {
		return skill{}, fmt.Errorf("read frontmatter: %w", err)
	}
	if !closed || !utf8.Valid(frontmatter.Bytes()) {
		return skill{}, errors.New("invalid or unterminated YAML frontmatter")
	}
	var meta map[string]any
	if err := yaml.Unmarshal(frontmatter.Bytes(), &meta); err != nil {
		return skill{}, fmt.Errorf("invalid YAML frontmatter: %w", err)
	}
	name, _ := meta["name"].(string)
	description, _ := meta["description"].(string)
	name, description = strings.TrimSpace(name), strings.TrimSpace(description)
	if name == "" {
		name = directoryName
	}
	if description == "" {
		return skill{}, errors.New("a nonempty string description is required")
	}
	return skill{name: name, description: description, path: path}, nil
}

func formatSkills(skills []skill) string {
	if len(skills) == 0 {
		return ""
	}
	var out strings.Builder
	out.WriteString("\n\nThe following skills provide specialized instructions for specific tasks.\nUse the read tool to load a skill's file when the task matches its description.\nResolve relative paths in a skill against the directory containing its SKILL.md, and use absolute paths in tool commands.\n\n<available_skills>\n")
	for _, s := range skills {
		fmt.Fprintf(&out, "  <skill>\n    <name>%s</name>\n    <description>%s</description>\n    <location>%s</location>\n  </skill>\n", escapeXML(s.name), escapeXML(s.description), escapeXML(s.path))
	}
	out.WriteString("</available_skills>")
	return out.String()
}

func escapeXML(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;", "'", "&apos;").Replace(s)
}
