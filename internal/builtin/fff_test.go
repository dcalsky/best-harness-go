package builtin_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	grepCalls  []fff.GrepOptions
	grepQueue  []fff.GrepResult
}

func (s *recordingSearch) Find(_ context.Context, root string, options fff.FindOptions) (fff.FindResult, error) {
	s.findRoot, s.find = root, options
	return s.findResult, nil
}

func (s *recordingSearch) Grep(_ context.Context, root string, options fff.GrepOptions) (fff.GrepResult, error) {
	s.grepRoot, s.grep = root, options
	s.grepCalls = append(s.grepCalls, options)
	if len(s.grepQueue) > 0 {
		result := s.grepQueue[0]
		s.grepQueue = s.grepQueue[1:]
		return result, nil
	}
	return s.grepResult, nil
}

func TestFindUsesFFFGlobAndGrepUsesFFFRegex(t *testing.T) {
	dir := t.TempDir()
	search := &recordingSearch{}
	c := builtin.Config{Cwd: dir, Search: search}

	_ = execute(t, builtin.Find(c), `{"pattern":"*.go","path":"src","limit":7}`)
	if search.findRoot != dir || search.find.Pattern != "src/ *.go" || search.find.Limit != 7 || !search.find.UseQueryParser {
		t.Fatalf("find root=%q options=%#v", search.findRoot, search.find)
	}

	_ = execute(t, builtin.Grep(c), `{"pattern":"Foo.+Bar","path":"src/**/*.go","limit":9}`)
	if search.grepRoot != dir || search.grep.Pattern != "Foo.+Bar" || search.grep.Constraints != "src/**/*.go" || search.grep.Mode != fff.GrepRegex || !search.grep.SmartCase || search.grep.Limit != 9 {
		t.Fatalf("grep root=%q options=%#v", search.grepRoot, search.grep)
	}
}

func TestGrepUsesSmartCaseUnlessForcedCaseSensitive(t *testing.T) {
	search := &recordingSearch{grepResult: fff.GrepResult{Matches: []fff.Match{{Path: "a.go", Line: 1, Text: "needle"}}}}
	tool := builtin.Grep(builtin.Config{Cwd: t.TempDir(), Search: search})
	_ = execute(t, tool, `{"pattern":"needle"}`)
	if !search.grep.SmartCase || search.grep.Mode != fff.GrepPlain {
		t.Fatalf("smart-case grep options=%#v", search.grep)
	}
	_ = execute(t, tool, `{"pattern":"needle","caseSensitive":true}`)
	if search.grep.SmartCase {
		t.Fatalf("grep options=%#v", search.grep)
	}
}

func TestGrepUsesBudgetPerFileCapContextAndCursor(t *testing.T) {
	search := &recordingSearch{grepResult: fff.GrepResult{NextFilePage: 17, RegexFallbackError: "unsupported escape"}}
	c := builtin.Config{Cwd: t.TempDir(), Search: search}
	grepTool := builtin.Grep(c)
	first := execute(t, grepTool, `{"pattern":"TODO","context":99,"limit":500}`)
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
	_ = execute(t, grepTool, fmt.Sprintf(`{"pattern":"TODO","context":99,"limit":500,"cursor":%q}`, details.Cursor))
	if search.grep.FileOffset != 17 {
		t.Fatalf("file offset=%d", search.grep.FileOffset)
	}
}

func TestGrepCursorUsesNativeFileOffset(t *testing.T) {
	search := &recordingSearch{grepResult: fff.GrepResult{NextFilePage: 3}}
	c := builtin.Config{Cwd: t.TempDir(), Search: search}
	grepTool := builtin.Grep(c)
	first := execute(t, grepTool, `{"pattern":"main","limit":3}`)
	details := first.Details.(builtin.GrepDetails)
	if details.Cursor == "" {
		t.Fatalf("details=%#v", details)
	}
	_ = execute(t, grepTool, fmt.Sprintf(`{"pattern":"main","limit":3,"cursor":%q}`, details.Cursor))
	if search.grep.FileOffset != 3 {
		t.Fatalf("grep options=%#v", search.grep)
	}
}

func TestGrepFuzzyFallbackMatchesPiScopeAndDropsContext(t *testing.T) {
	search := &recordingSearch{grepQueue: []fff.GrepResult{
		{},
		{Matches: []fff.Match{{Path: "renamed.go", Line: 1, Text: "needle"}}},
	}}
	result := execute(t, builtin.Grep(builtin.Config{Cwd: t.TempDir(), Search: search}), `{"pattern":"nedle","path":"missing.go","context":4}`)
	if len(search.grepCalls) != 2 {
		t.Fatalf("grep calls=%#v", search.grepCalls)
	}
	fuzzy := search.grepCalls[1]
	if fuzzy.Mode != fff.GrepFuzzy || fuzzy.Constraints != "" || fuzzy.BeforeContext != 0 || fuzzy.AfterContext != 0 {
		t.Fatalf("fuzzy options=%#v", fuzzy)
	}
	if !strings.HasPrefix(result.Content[0].Text, "[0 exact matches. Maybe you meant this?]") {
		t.Fatalf("output=%q", result.Content[0].Text)
	}
}

func TestGrepCursorSurvivesOutputTruncation(t *testing.T) {
	search := &recordingSearch{grepResult: fff.GrepResult{
		Matches:      []fff.Match{{Path: "very/long/path.go", Line: 1, Text: strings.Repeat("x", 200)}},
		NextFilePage: 2,
	}}
	result := execute(t, builtin.Grep(builtin.Config{Cwd: t.TempDir(), Search: search, MaxOutputBytes: 24}), `{"pattern":"x"}`)
	details := result.Details.(builtin.GrepDetails)
	if details.Cursor == "" || !strings.Contains(result.Content[0].Text, details.Cursor) || !details.Truncated {
		t.Fatalf("details=%#v output=%q", details, result.Content[0].Text)
	}
}

func TestFindCursorResumesNextPage(t *testing.T) {
	search := &recordingSearch{findResult: fff.FindResult{Files: []fff.File{{Path: "a.go", Score: 100}}, TotalMatched: 3, NextPage: 1}}
	c := builtin.Config{Cwd: t.TempDir(), Search: search}
	findTool := builtin.Find(c)
	first := execute(t, findTool, `{"pattern":"go","limit":1}`)
	details := first.Details.(builtin.FindDetails)
	if details.Cursor == "" {
		t.Fatalf("details=%#v", details)
	}
	_ = execute(t, findTool, fmt.Sprintf(`{"pattern":"ignored","cursor":%q}`, details.Cursor))
	if search.find.Page != 1 {
		t.Fatalf("page=%d", search.find.Page)
	}
}

func TestFindCapsWeakFuzzyNoiseLikePi(t *testing.T) {
	files := make([]fff.File, 10)
	for index := range files {
		files[index] = fff.File{Path: fmt.Sprintf("noise-%d.txt", index), Score: 1}
	}
	search := &recordingSearch{findResult: fff.FindResult{Files: files, TotalMatched: 100, TotalFiles: 100, NextPage: 1}}
	result := execute(t, builtin.Find(builtin.Config{Cwd: t.TempDir(), Search: search}), `{"pattern":"specific-long-concept","limit":10}`)
	details := result.Details.(builtin.FindDetails)
	if details.Results != 5 || details.Cursor != "" || !details.HasMore {
		t.Fatalf("details=%#v", details)
	}
	if got := strings.Count(strings.Split(result.Content[0].Text, "\n\n[")[0], "\n") + 1; got != 5 {
		t.Fatalf("shown=%d output=%q", got, result.Content[0].Text)
	}
	if !strings.Contains(result.Content[0].Text, "weak scattered fuzzy matches") {
		t.Fatalf("output=%q", result.Content[0].Text)
	}
}

func TestFindRoutesOutsideWorkspacePathToAuxiliaryRoot(t *testing.T) {
	parent := t.TempDir()
	workspace := filepath.Join(parent, "workspace")
	outside := filepath.Join(parent, "other", "src")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	search := &recordingSearch{}
	path := filepath.ToSlash(filepath.Join(outside, "**", "*.go"))
	_ = execute(t, builtin.Find(builtin.Config{Cwd: workspace, Search: search}), fmt.Sprintf(`{"pattern":"handler","path":%q}`, path))
	if search.findRoot != outside || search.find.Pattern != "**/*.go handler" {
		t.Fatalf("find root=%q options=%#v", search.findRoot, search.find)
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
	if !strings.HasPrefix(find.Content[0].Text, "notes.txt") {
		t.Fatalf("find=%#v", find)
	}
	grep := execute(t, builtin.Grep(c), `{"pattern":"gamma"}`)
	if grep.Content[0].Text != "notes.txt\n 2: gamma" {
		t.Fatalf("grep=%#v", grep)
	}
}
