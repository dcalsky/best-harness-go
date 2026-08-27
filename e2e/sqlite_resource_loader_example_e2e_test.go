package e2e_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/dcalsky/best-harness-go"
	. "github.com/dcalsky/best-harness-go/examples/sqlite_resources"
)

type fixedStore struct {
	rules  []Rule
	skills []Skill
	err    error
}

func (s fixedStore) Load(context.Context, string) ([]Rule, []Skill, error) {
	return append([]Rule(nil), s.rules...), append([]Skill(nil), s.skills...), s.err
}

func TestLoaderMergesGlobalAndProjectResources(t *testing.T) {
	project := "billing-api"
	loader := Loader{
		Store: fixedStore{
			rules: []Rule{
				{ID: 2, ProjectKey: &project, Name: "project", Content: "Run billing tests.", Priority: 10},
				{ID: 1, Name: "global", Content: "Run gofmt.", Priority: 20},
			},
			skills: []Skill{
				{ID: 3, Name: "review", Description: "Global review", Content: "global skill", Priority: 10},
				{ID: 4, ProjectKey: &project, Name: "review", Description: "Billing review", Content: "project skill", Priority: 10},
			},
		},
		ProjectKey: project,
		CacheDir:   t.TempDir(),
	}

	snapshot, err := loader.Load(context.Background(), harness.ResourceLoadRequest{Cwd: "/work/billing"})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.ProjectInstructions) != 2 || snapshot.ProjectInstructions[0].Name != "global" {
		t.Fatalf("unexpected rules: %#v", snapshot.ProjectInstructions)
	}
	if snapshot.Skills["review"].Description != "Billing review" {
		t.Fatalf("project skill did not replace global skill: %#v", snapshot.Skills["review"])
	}
	if len(snapshot.Diagnostics) != 1 {
		t.Fatalf("diagnostics=%#v", snapshot.Diagnostics)
	}
	b, err := os.ReadFile(snapshot.Skills["review"].Location)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "project skill" {
		t.Fatalf("unexpected cached skill: %q", b)
	}
	info, err := os.Stat(snapshot.Skills["review"].Location)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("skill mode=%o", info.Mode().Perm())
	}
	prompt := harness.BuildSystemPrompt(harness.ResourcePromptOptions{
		Cwd:      "/work/billing",
		Tools:    []string{"read"},
		Snapshot: snapshot,
	})
	if !strings.Contains(prompt, "Run gofmt.") || !strings.Contains(prompt, snapshot.Skills["review"].Location) {
		t.Fatalf("prompt is missing sqlite resources: %s", prompt)
	}
}

func TestLoaderRejectsMissingDependencies(t *testing.T) {
	if _, err := (Loader{CacheDir: t.TempDir()}).Load(context.Background(), harness.ResourceLoadRequest{}); err == nil {
		t.Fatal("missing store did not fail")
	}
	if _, err := (Loader{Store: fixedStore{}}).Load(context.Background(), harness.ResourceLoadRequest{}); err == nil {
		t.Fatal("missing cache directory did not fail")
	}
}

func TestLoaderReturnsStoreAndContextErrors(t *testing.T) {
	want := errors.New("read failed")
	_, err := (Loader{Store: fixedStore{err: want}, CacheDir: t.TempDir()}).Load(context.Background(), harness.ResourceLoadRequest{})
	if !errors.Is(err, want) {
		t.Fatalf("error=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = (Loader{Store: fixedStore{rules: []Rule{{ID: 1, Name: "r", Content: "x"}}}, CacheDir: t.TempDir()}).Load(ctx, harness.ResourceLoadRequest{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
}

func TestLoaderKeepsCachedSkillVersionsSeparate(t *testing.T) {
	dir := t.TempDir()
	first, err := (Loader{Store: fixedStore{skills: []Skill{{ID: 7, Name: "review", Content: "one"}}}, CacheDir: dir}).Load(context.Background(), harness.ResourceLoadRequest{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := (Loader{Store: fixedStore{skills: []Skill{{ID: 7, Name: "review", Content: "two"}}}, CacheDir: dir}).Load(context.Background(), harness.ResourceLoadRequest{})
	if err != nil {
		t.Fatal(err)
	}
	firstPath := first.Skills["review"].Location
	secondPath := second.Skills["review"].Location
	if firstPath == secondPath {
		t.Fatal("different skill versions use the same path")
	}
	for path, want := range map[string]string{firstPath: "one", secondPath: "two"} {
		b, readErr := os.ReadFile(path)
		if readErr != nil || string(b) != want {
			t.Fatalf("path=%s content=%q error=%v", path, b, readErr)
		}
	}
}
