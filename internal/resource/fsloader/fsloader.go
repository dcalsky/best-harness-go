// Package fsloader reads resources from an explicitly selected project tree.
package fsloader

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dcalsky/best-harness-go/internal/resource"
)

type Loader struct{ Root string }

func New(root string) *Loader { return &Loader{Root: root} }
func (l *Loader) Load(ctx context.Context, req resource.LoadRequest) (resource.Snapshot, error) {
	root := l.Root
	if root == "" {
		return resource.Snapshot{}, errors.New("fsloader root is required")
	}
	cwd := req.Cwd
	if cwd == "" {
		cwd = root
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return resource.Snapshot{}, err
	}
	cwd, err = filepath.Abs(cwd)
	if err != nil {
		return resource.Snapshot{}, err
	}
	rel, err := filepath.Rel(root, cwd)
	if err != nil || strings.HasPrefix(rel, "..") {
		return resource.Snapshot{}, errors.New("working directory is outside fsloader root")
	}
	out := resource.Snapshot{Skills: map[string]resource.Skill{}, PromptTemplates: map[string]resource.PromptTemplate{}}
	dirs := []string{root}
	if rel != "." {
		cur := root
		for _, part := range strings.Split(rel, string(filepath.Separator)) {
			cur = filepath.Join(cur, part)
			dirs = append(dirs, cur)
		}
	}
	for _, dir := range dirs {
		for _, name := range []string{"AGENTS.override.md", "AGENTS.md", "AGENTS.MD", "CLAUDE.md", "CLAUDE.MD"} {
			if err := ctx.Err(); err != nil {
				return out, err
			}
			path := filepath.Join(dir, name)
			if b, err := os.ReadFile(path); err == nil {
				src := resource.Source{Name: name, Path: path, Content: string(b)}
				out.ProjectInstructions = append(out.ProjectInstructions, src)
				out.Sources = append(out.Sources, src)
				break
			}
		}
	}
	piDir := filepath.Join(root, ".pi")
	if b, err := os.ReadFile(filepath.Join(piDir, "SYSTEM.md")); err == nil {
		out.SystemPrompt = string(b)
	}
	if b, err := os.ReadFile(filepath.Join(piDir, "APPEND_SYSTEM.md")); err == nil {
		out.AppendSystemPrompt = append(out.AppendSystemPrompt, resource.Source{Name: "APPEND_SYSTEM.md", Path: filepath.Join(piDir, "APPEND_SYSTEM.md"), Content: string(b)})
	}
	loadMarkdownDir(ctx, filepath.Join(piDir, "prompts"), func(path string, b []byte) {
		name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		frontmatter, body := parseFrontmatter(string(b))
		description := frontmatter["description"]
		if description == "" {
			for _, line := range strings.Split(body, "\n") {
				if strings.TrimSpace(line) != "" {
					description = line
					if len(description) > 60 {
						description = description[:60] + "..."
					}
					break
				}
			}
		}
		out.PromptTemplates[name] = resource.PromptTemplate{Name: name, Description: description, ArgumentHint: frontmatter["argument-hint"], Template: body}
	})
	entries, _ := os.ReadDir(filepath.Join(piDir, "skills"))
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		path := filepath.Join(piDir, "skills", entry.Name(), "SKILL.md")
		if !entry.IsDir() {
			path = filepath.Join(piDir, "skills", entry.Name())
		}
		if b, err := os.ReadFile(path); err == nil {
			name := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
			frontmatter, _ := parseFrontmatter(string(b))
			if frontmatter["name"] != "" {
				name = frontmatter["name"]
			}
			out.Skills[name] = resource.Skill{Name: name, Description: frontmatter["description"], Location: path, Content: string(b)}
		}
	}
	return out, nil
}

func parseFrontmatter(content string) (map[string]string, string) {
	values := make(map[string]string)
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return values, content
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
		key, value, ok := strings.Cut(lines[i], ":")
		if ok {
			value = strings.TrimSpace(value)
			value = strings.Trim(value, `"'`)
			values[strings.TrimSpace(key)] = value
		}
	}
	if end < 0 {
		return map[string]string{}, content
	}
	return values, strings.Join(lines[end+1:], "\n")
}
func loadMarkdownDir(ctx context.Context, dir string, fn func(string, []byte)) {
	entries, _ := os.ReadDir(dir)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, e := range entries {
		if ctx.Err() != nil {
			return
		}
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if b, err := os.ReadFile(path); err == nil {
			fn(path, b)
		}
	}
}
