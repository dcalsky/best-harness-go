package builtin_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dcalsky/best-harness-go/internal/builtin"
	"github.com/dcalsky/best-harness-go/internal/fff"
)

type recordingSearch struct {
	findRoot   string
	find       fff.FindOptions
	grepRoot   string
	grep       fff.GrepOptions
	findResult fff.FindResult
	grepResult fff.GrepResult
}

func (s *recordingSearch) Find(_ context.Context, root string, options fff.FindOptions) (fff.FindResult, error) {
	s.findRoot, s.find = root, options
	return s.findResult, nil
}

func (s *recordingSearch) Grep(_ context.Context, root string, options fff.GrepOptions) (fff.GrepResult, error) {
	s.grepRoot, s.grep = root, options
	return s.grepResult, nil
}

func TestFindUsesFFFGlobAndGrepUsesFFFRegex(t *testing.T) {
	dir := t.TempDir()
	search := &recordingSearch{}
	c := builtin.Config{Cwd: dir, Search: search}

	_ = execute(t, builtin.Find(c), `{"pattern":"*.go","path":"src","maxResults":7}`)
	if search.findRoot != filepath.Join(dir, "src") || search.find.Pattern != "*.go" || search.find.Limit != 7 {
		t.Fatalf("find root=%q options=%#v", search.findRoot, search.find)
	}

	_ = execute(t, builtin.Grep(c), `{"pattern":"Foo.+Bar","path":"src","glob":"*.go","maxResults":9}`)
	if search.grepRoot != filepath.Join(dir, "src") || search.grep.Pattern != "Foo.+Bar" || search.grep.Constraints != "*.go" || search.grep.Mode != fff.GrepRegex || search.grep.SmartCase || search.grep.Limit != 9 {
		t.Fatalf("grep root=%q options=%#v", search.grepRoot, search.grep)
	}
}

func TestGrepLiteralIgnoreCaseUsesEscapedRegex(t *testing.T) {
	search := &recordingSearch{}
	_ = execute(t, builtin.Grep(builtin.Config{Cwd: t.TempDir(), Search: search}), `{"pattern":"a.b","literal":true,"ignoreCase":true}`)
	if search.grep.Mode != fff.GrepRegex || search.grep.Pattern != `(?i)a\.b` {
		t.Fatalf("grep options=%#v", search.grep)
	}
}

func TestGrepUsesBudgetPerFileCapContextAndCursor(t *testing.T) {
	search := &recordingSearch{grepResult: fff.GrepResult{NextFilePage: 17, RegexFallbackError: "unsupported escape"}}
	c := builtin.Config{Cwd: t.TempDir(), Search: search}
	first := execute(t, builtin.Grep(c), `{"pattern":"TODO","context":99,"maxResults":500}`)
	details := first.Details.(builtin.GrepDetails)
	if search.grep.Limit != 50 || search.grep.MaxPerFile != 200 || search.grep.BeforeContext != 20 || search.grep.AfterContext != 20 {
		t.Fatalf("grep options=%#v", search.grep)
	}
	if search.grep.TimeBudget <= 0 || search.grep.TimeBudget > 10*time.Second {
		t.Fatalf("time budget=%s", search.grep.TimeBudget)
	}
	if details.Cursor == "" || details.RegexFallbackError != "unsupported escape" {
		t.Fatalf("details=%#v", details)
	}
	_ = execute(t, builtin.Grep(c), fmt.Sprintf(`{"pattern":"TODO","context":99,"maxResults":500,"cursor":%q}`, details.Cursor))
	if search.grep.FileOffset != 17 {
		t.Fatalf("file offset=%d", search.grep.FileOffset)
	}
}

func TestFindCursorResumesNextPage(t *testing.T) {
	search := &recordingSearch{findResult: fff.FindResult{Files: []fff.File{{Path: "a.go"}}, TotalMatched: 3, NextPage: 1}}
	c := builtin.Config{Cwd: t.TempDir(), Search: search}
	first := execute(t, builtin.Find(c), `{"pattern":"go","maxResults":1}`)
	details := first.Details.(builtin.FindDetails)
	if details.Cursor == "" {
		t.Fatalf("details=%#v", details)
	}
	_ = execute(t, builtin.Find(c), fmt.Sprintf(`{"pattern":"go","maxResults":1,"cursor":%q}`, details.Cursor))
	if search.find.Page != 1 {
		t.Fatalf("page=%d", search.find.Page)
	}
}

func TestBuiltinsUseNativeFFF(t *testing.T) {
	library := os.Getenv("BEST_HARNESS_FFF_LIBRARY")
	if library == "" && os.Getenv("BEST_HARNESS_FFF_INTEGRATION") == "" {
		t.Skip("set BEST_HARNESS_FFF_INTEGRATION=1 to test the pinned release asset")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("alpha\ngamma\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pool := fff.NewPool(fff.Options{LibraryPath: library})
	defer pool.Close()
	c := builtin.Config{Cwd: dir, Search: pool}

	find := execute(t, builtin.Find(c), `{"pattern":"*.txt"}`)
	if find.Content[0].Text != "notes.txt" {
		t.Fatalf("find=%#v", find)
	}
	grep := execute(t, builtin.Grep(c), `{"pattern":"gamma","literal":true}`)
	if grep.Content[0].Text != "notes.txt:2:gamma" {
		t.Fatalf("grep=%#v", grep)
	}
}
