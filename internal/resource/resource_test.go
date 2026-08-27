package resource_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dcalsky/best-harness-go/internal/resource"
	"github.com/dcalsky/best-harness-go/internal/resource/fsloader"
)

func TestMergeOrderAndPrompt(t *testing.T) {
	r := resource.NewRegistry()
	r.Register(resource.ProgramLoader{Snapshot: resource.Snapshot{SystemPrompt: "first", Skills: map[string]resource.Skill{"s": {Name: "s", Content: "1"}}}})
	r.Register(resource.ProgramLoader{Snapshot: resource.Snapshot{SystemPrompt: "second", Skills: map[string]resource.Skill{"s": {Name: "s", Content: "2"}}, AppendSystemPrompt: []resource.Source{{Content: "tail"}}}})
	s, err := r.Load(context.Background(), resource.LoadRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if s.SystemPrompt != "second" || s.Skills["s"].Content != "2" || len(s.Diagnostics) != 2 {
		t.Fatalf("snapshot=%#v", s)
	}
	prompt := resource.BuildSystemPrompt(resource.PromptOptions{Snapshot: s})
	if prompt != "second\n\ntail" {
		t.Fatalf("prompt=%q", prompt)
	}
}
func TestExplicitFSLoader(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "a")
	os.MkdirAll(filepath.Join(root, ".pi", "skills", "review"), 0755)
	os.MkdirAll(sub, 0755)
	os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("root rules"), 0644)
	os.WriteFile(filepath.Join(sub, "CLAUDE.md"), []byte("sub rules"), 0644)
	os.WriteFile(filepath.Join(root, ".pi", "skills", "review", "SKILL.md"), []byte("review files"), 0644)
	s, err := fsloader.New(root).Load(context.Background(), resource.LoadRequest{Cwd: sub})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.ProjectInstructions) != 2 || !strings.Contains(s.Skills["review"].Content, "review") {
		t.Fatalf("snapshot=%#v", s)
	}
}

func TestContextFilePrecedenceAndSkillsPrompt(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("regular"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.override.md"), []byte("override"), 0644); err != nil {
		t.Fatal(err)
	}
	snapshot, err := fsloader.New(root).Load(context.Background(), resource.LoadRequest{Cwd: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.ProjectInstructions) != 1 || snapshot.ProjectInstructions[0].Content != "override" {
		t.Fatalf("instructions=%#v", snapshot.ProjectInstructions)
	}
	snapshot.Skills["review"] = resource.Skill{Name: "review", Description: "Review changes", Location: "/skills/review/SKILL.md"}
	withoutRead := resource.BuildSystemPrompt(resource.PromptOptions{Snapshot: snapshot, Tools: []string{"bash"}})
	withRead := resource.BuildSystemPrompt(resource.PromptOptions{Snapshot: snapshot, Tools: []string{"read"}})
	if strings.Contains(withoutRead, "<available_skills>") || !strings.Contains(withRead, "/skills/review/SKILL.md") {
		t.Fatalf("without read=%q\nwith read=%q", withoutRead, withRead)
	}
}

func TestPiStylePromptArguments(t *testing.T) {
	args := resource.ParseCommandArgs(`one "two words" 'three words'`)
	if len(args) != 3 || args[1] != "two words" || args[2] != "three words" {
		t.Fatalf("args=%#v", args)
	}
	template := resource.PromptTemplate{Template: `$1|$@|${2:-fallback}|${4:-fallback}|${@:2:2}`}
	if got := resource.ExpandArgs(template, args); got != "one|one two words three words|two words|fallback|two words three words" {
		t.Fatalf("expanded=%q", got)
	}
}

func TestDefaultSystemPromptMatchesPiCoreStructure(t *testing.T) {
	prompt := resource.BuildSystemPrompt(resource.PromptOptions{
		Cwd:   "/work/project",
		Tools: []string{"read", "custom"},
	})
	for _, text := range []string{
		"You are an expert coding assistant operating inside pi, a coding agent harness.",
		"- Be concise in your responses",
		"- Show file paths clearly when working with files",
		"Current working directory: /work/project",
	} {
		if !strings.Contains(prompt, text) {
			t.Fatalf("prompt missing %q:\n%s", text, prompt)
		}
	}
	if strings.Contains(prompt, "Available tools:") || strings.Contains(prompt, "Use read to examine files") {
		t.Fatalf("tool prompt content should not be injected:\n%s", prompt)
	}
}
