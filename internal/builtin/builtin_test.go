package builtin_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dcalsky/best-harness-go/internal/builtin"
	"github.com/dcalsky/best-harness-go/internal/fff"
	"github.com/dcalsky/best-harness-go/internal/tool"
)

type fakeShell struct{}

func (fakeShell) Execute(_ context.Context, _ string, _ string, update func(string, []byte)) (builtin.ShellResult, error) {
	update("stdout", []byte("partial"))
	return builtin.ShellResult{Stdout: []byte("out"), Stderr: []byte("err"), ExitCode: 2}, nil
}

type fakeSearch struct{}

func (fakeSearch) Find(_ context.Context, _ string, _ fff.FindOptions) (fff.FindResult, error) {
	return fff.FindResult{Files: []fff.File{{Path: "a.txt"}}, TotalMatched: 1}, nil
}

func (fakeSearch) Grep(_ context.Context, _ string, _ fff.GrepOptions) (fff.GrepResult, error) {
	return fff.GrepResult{Matches: []fff.Match{{Path: "a.txt", Line: 2, Text: "gamma"}}}, nil
}

func execute[P, D any](t *testing.T, def tool.Tool[P, D], raw string) tool.Result {
	t.Helper()
	r := tool.NewRegistry()
	if err := r.Register(def); err != nil {
		t.Fatal(err)
	}
	res, err := r.Execute(context.Background(), tool.ToolCall{Name: def.Name, Arguments: json.RawMessage(raw)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return res
}
func TestWriteEditReadGrepFindLS(t *testing.T) {
	dir := t.TempDir()
	c := builtin.Config{Cwd: dir, MaxOutputBytes: 80, Search: fakeSearch{}}
	execute(t, builtin.Write(c), `{"path":"a.txt","content":"alpha\nbeta\n"}`)
	execute(t, builtin.Edit(c), `{"path":"a.txt","oldText":"beta","newText":"gamma"}`)
	read := execute(t, builtin.Read(c), `{"path":"a.txt"}`)
	if !strings.Contains(read.Content[0].Text, "gamma") {
		t.Fatalf("read=%#v", read)
	}
	grep := execute(t, builtin.Grep(c), `{"pattern":"gamma","path":"."}`)
	if !strings.Contains(grep.Content[0].Text, "a.txt:2") {
		t.Fatalf("grep=%#v", grep)
	}
	find := execute(t, builtin.Find(c), `{"pattern":"*.txt","path":"."}`)
	if !strings.Contains(find.Content[0].Text, "a.txt") {
		t.Fatalf("find=%#v", find)
	}
	ls := execute(t, builtin.LS(c), `{"path":"."}`)
	if !strings.Contains(ls.Content[0].Text, "a.txt") {
		t.Fatalf("ls=%#v", ls)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.txt")); err != nil {
		t.Fatal(err)
	}
}
func TestEditRejectsAmbiguousMatch(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a"), []byte("x x"), 0644)
	r := tool.NewRegistry()
	r.Register(builtin.Edit(builtin.Config{Cwd: dir}))
	_, err := r.Execute(context.Background(), tool.ToolCall{Name: "edit", Arguments: json.RawMessage(`{"path":"a","oldText":"x","newText":"y"}`)}, nil)
	if err == nil {
		t.Fatal("expected ambiguous match error")
	}
}
func TestBashUpdatesAndExitCode(t *testing.T) {
	r := tool.NewRegistry()
	if err := r.Register(builtin.Bash(builtin.Config{Cwd: t.TempDir(), Shell: fakeShell{}})); err != nil {
		t.Fatal(err)
	}
	var updates int
	res, err := r.Execute(context.Background(), tool.ToolCall{Name: "bash", Arguments: json.RawMessage(`{"command":"ignored"}`)}, func(any) { updates++ })
	if err != nil {
		t.Fatal(err)
	}
	d := res.Details.(builtin.BashDetails)
	if updates != 1 || d.ExitCode != 2 || !res.IsError || res.Content[0].Text != "outerr" {
		t.Fatalf("result=%#v updates=%d", res, updates)
	}
}

func TestReadOffsetIsOneIndexed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "lines.txt"), []byte("one\ntwo\nthree"), 0644); err != nil {
		t.Fatal(err)
	}
	result := execute(t, builtin.Read(builtin.Config{Cwd: dir}), `{"path":"lines.txt","offset":2,"limit":1}`)
	if result.Content[0].Text != "two" {
		t.Fatalf("content=%q", result.Content[0].Text)
	}
	r := tool.NewRegistry()
	_ = r.Register(builtin.Read(builtin.Config{Cwd: dir}))
	if _, err := r.Execute(context.Background(), tool.ToolCall{Name: "read", Arguments: json.RawMessage(`{"path":"lines.txt","offset":5}`)}, nil); err == nil {
		t.Fatal("expected out-of-range offset error")
	}
}
