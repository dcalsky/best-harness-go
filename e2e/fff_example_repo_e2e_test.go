package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/dcalsky/best-harness-go"
)

func requireFFFExampleRepo(t *testing.T) string {
	t.Helper()
	if os.Getenv("BEST_HARNESS_FFF_LIBRARY") == "" && os.Getenv("BEST_HARNESS_FFF_INTEGRATION") == "" {
		t.Skip("set BEST_HARNESS_FFF_INTEGRATION=1 to run the native FFF end-to-end tests")
	}
	if !((runtime.GOOS == "darwin" && runtime.GOARCH == "arm64") ||
		(runtime.GOOS == "linux" && (runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64")) ||
		(runtime.GOOS == "windows" && runtime.GOARCH == "amd64")) {
		t.Skipf("FFF release binaries do not support %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	root, err := filepath.Abs(filepath.Join("testdata", "example-repo"))
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(root, "go.mod")); err != nil || info.IsDir() {
		t.Fatalf("example repository is unavailable at %s: %v", root, err)
	}
	return root
}

func newExampleRepoPool(t *testing.T) (*harness.FFFPool, string) {
	t.Helper()
	root := requireFFFExampleRepo(t)
	pool := harness.NewFFFPool(harness.FFFOptions{
		LibraryPath: os.Getenv("BEST_HARNESS_FFF_LIBRARY"),
		ScanTimeout: 30 * time.Second,
	})
	t.Cleanup(func() {
		if err := pool.Close(); err != nil {
			t.Errorf("close FFF pool: %v", err)
		}
	})
	return pool, root
}

func fixturePaths(t *testing.T, root string, accept func(string) bool) []string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if accept(relative) {
			paths = append(paths, relative)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	return paths
}

func findEveryPage(t *testing.T, pool *harness.FFFPool, root, pattern string, limit int) ([]string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var paths []string
	page := 0
	total := -1
	for request := 0; request < 100; request++ {
		result, err := pool.Find(ctx, root, harness.FFFPathSearchOptions{Pattern: pattern, Limit: limit, Page: page})
		if err != nil {
			t.Fatal(err)
		}
		if total < 0 {
			total = result.TotalMatched
		} else if result.TotalMatched != total {
			t.Fatalf("total changed between pages: first=%d page=%d", total, result.TotalMatched)
		}
		for _, file := range result.Files {
			paths = append(paths, filepath.ToSlash(file.Path))
		}
		if result.NextPage == 0 {
			return paths, total
		}
		if result.NextPage <= page {
			t.Fatalf("find cursor did not advance: page=%d next=%d", page, result.NextPage)
		}
		page = result.NextPage
	}
	t.Fatal("find pagination did not terminate after 100 pages")
	return nil, 0
}

func TestFFFExampleRepoFindAllGoFilesAcrossPages(t *testing.T) {
	pool, root := newExampleRepoPool(t)

	got, total := findEveryPage(t, pool, root, "*.go", 7)
	want := fixturePaths(t, root, func(path string) bool { return strings.HasSuffix(path, ".go") })
	sort.Strings(got)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("FFF glob mismatch\n got (%d): %v\nwant (%d): %v", len(got), got, len(want), want)
	}
	if total != len(want) {
		t.Fatalf("FFF total=%d, want %d", total, len(want))
	}
	seen := make(map[string]bool, len(got))
	for _, path := range got {
		if seen[path] {
			t.Fatalf("duplicate path across pages: %s", path)
		}
		seen[path] = true
	}
}

func TestFFFExampleRepoFuzzyFindRanksSpecificFile(t *testing.T) {
	pool, root := newExampleRepoPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := pool.Find(ctx, root, harness.FFFPathSearchOptions{
		Pattern: "indent handler norace",
		Limit:   10,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "slog-handler-guide/indenthandler4/indent_handler_norace_test.go"
	if len(result.Files) == 0 || filepath.ToSlash(result.Files[0].Path) != want {
		t.Fatalf("top fuzzy result=%#v, want %q", result.Files, want)
	}
}

func TestFFFExampleRepoGrepLiteralWithContext(t *testing.T) {
	pool, root := newExampleRepoPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := pool.Grep(ctx, root, harness.FFFContentSearchOptions{
		Pattern:       "Search weaviate to find the most relevant",
		Constraints:   "*.go",
		Mode:          harness.FFFGrepPlain,
		Limit:         20,
		TimeBudget:    10 * time.Second,
		MaxPerFile:    20,
		BeforeContext: 2,
		AfterContext:  2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 1 {
		t.Fatalf("matches=%#v", result.Matches)
	}
	match := result.Matches[0]
	if filepath.ToSlash(match.Path) != "ragserver/ragserver/main.go" || match.Line != 143 {
		t.Fatalf("match=%#v", match)
	}
	if len(match.ContextBefore) != 2 || len(match.ContextAfter) != 2 ||
		match.ContextBefore[0] != "\t}" || match.ContextBefore[1] != "" ||
		!strings.Contains(match.ContextAfter[0], "documents to the query") || !strings.Contains(match.ContextAfter[1], "GraphQL") {
		t.Fatalf("unexpected context: before=%q after=%q", match.ContextBefore, match.ContextAfter)
	}
}

func TestFFFExampleRepoGrepRegexMatchesFilesystemOracle(t *testing.T) {
	pool, root := newExampleRepoPool(t)
	pattern := `func New\(`
	want := grepFixtureOracle(t, root, regexp.MustCompile(pattern), func(path string) bool {
		return strings.HasSuffix(path, ".go")
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := pool.Grep(ctx, root, harness.FFFContentSearchOptions{
		Pattern:     pattern,
		Constraints: "*.go",
		Mode:        harness.FFFGrepRegex,
		Limit:       100,
		TimeBudget:  10 * time.Second,
		MaxPerFile:  20,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(result.Matches))
	for _, match := range result.Matches {
		got = append(got, fmt.Sprintf("%s:%d:%s", filepath.ToSlash(match.Path), match.Line, match.Text))
	}
	sort.Strings(got)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("FFF regex mismatch\n got (%d): %v\nwant (%d): %v", len(got), got, len(want), want)
	}
}

func TestFFFExampleRepoGrepRetainsEmptyLineMatches(t *testing.T) {
	pool, root := newExampleRepoPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := pool.Grep(ctx, root, harness.FFFContentSearchOptions{
		Pattern: `^$`, Constraints: "*.go", Mode: harness.FFFGrepRegex,
		Limit: 50, TimeBudget: 10 * time.Second, MaxPerFile: 200,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) == 0 {
		t.Fatal("blank-line regex returned no matches")
	}
	for _, match := range result.Matches {
		if match.Text == "" && match.Path != "" && match.Line > 0 {
			return
		}
	}
	t.Fatalf("blank-line matches were discarded: %#v", result.Matches)
}

func grepFixtureOracle(t *testing.T, root string, expression *regexp.Regexp, accept func(string) bool) []string {
	t.Helper()
	paths := fixturePaths(t, root, accept)
	var matches []string
	for _, relative := range paths {
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatal(err)
		}
		for index, line := range strings.Split(string(content), "\n") {
			if expression.MatchString(line) {
				matches = append(matches, fmt.Sprintf("%s:%d:%s", relative, index+1, line))
			}
		}
	}
	sort.Strings(matches)
	return matches
}

func executeSearchTool(t *testing.T, registry *harness.ToolRegistry, name, arguments string) harness.Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := registry.Execute(ctx, harness.ToolCall{Name: name, Arguments: json.RawMessage(arguments)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestFFFExampleRepoBuiltinToolsAndCursors(t *testing.T) {
	root := requireFFFExampleRepo(t)
	registry := harness.NewToolRegistry()
	pool, err := harness.RegisterBuiltinToolsManaged(registry, harness.BuiltinConfig{
		Cwd:            root,
		FFFLibraryPath: os.Getenv("BEST_HARNESS_FFF_LIBRARY"),
		FFFScanTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if pool == nil {
		t.Fatal("managed builtin registration did not return its native FFF pool")
	}
	t.Cleanup(func() {
		if err := pool.Close(); err != nil {
			t.Errorf("close FFF pool: %v", err)
		}
	})

	first := executeSearchTool(t, registry, "find", `{"pattern":"","path":"*.go","limit":5}`)
	firstDetails, ok := first.Details.(harness.FindDetails)
	if !ok || firstDetails.Results != 5 || firstDetails.Cursor == "" {
		t.Fatalf("first find details=%#v", first.Details)
	}
	second := executeSearchTool(t, registry, "find", fmt.Sprintf(`{"pattern":"ignored on resume","cursor":%q}`, firstDetails.Cursor))
	if second.Details.(harness.FindDetails).Results != 5 {
		t.Fatalf("second find details=%#v", second.Details)
	}
	firstText, secondText := first.Content[0].Text, second.Content[0].Text
	for _, line := range findOutputPaths(firstText) {
		if strings.Contains(secondText, line) {
			t.Fatalf("find cursor repeated %q across pages", line)
		}
	}

	grep := executeSearchTool(t, registry, "grep", `{"pattern":"TODO:","path":"*.go","context":1,"limit":20}`)
	details, ok := grep.Details.(harness.GrepDetails)
	if !ok || details.Matches != 4 || details.Files != 1 {
		t.Fatalf("grep details=%#v output=%q", grep.Details, grep.Content[0].Text)
	}
	for _, expected := range []string{
		"slog-handler-guide/indenthandler1/indent_handler.go",
		" 18: // TODO:",
		" 51: // TODO:",
		" 56: // TODO:",
		" 74: // TODO:",
	} {
		if !strings.Contains(grep.Content[0].Text, expected) {
			t.Errorf("grep output is missing %q:\n%s", expected, grep.Content[0].Text)
		}
	}

	contextual := executeSearchTool(t, registry, "grep", `{"pattern":"Search weaviate to find the most relevant","path":"*.go","context":2}`)
	for _, expected := range []string{
		"ragserver/ragserver/main.go",
		" 141- }",
		" 142- ",
		" 143: // Search weaviate",
		" 144- // documents to the query.",
		" 145- gql := rs.wvClient.GraphQL()",
	} {
		if !strings.Contains(contextual.Content[0].Text, expected) {
			t.Errorf("context grep output is missing %q:\n%s", expected, contextual.Content[0].Text)
		}
	}

	caseSensitive := executeSearchTool(t, registry, "grep", `{"pattern":"search weaviate to find.*","path":"*.go","caseSensitive":true}`)
	if caseSensitive.Details.(harness.GrepDetails).Matches != 0 {
		t.Fatalf("case-sensitive grep unexpectedly matched: %q", caseSensitive.Content[0].Text)
	}
	smartCase := executeSearchTool(t, registry, "grep", `{"pattern":"search weaviate to find","path":"*.go"}`)
	if smartCase.Details.(harness.GrepDetails).Matches != 1 ||
		!strings.Contains(smartCase.Content[0].Text, " 143: // Search weaviate") {
		t.Fatalf("smart-case grep=%#v output=%q", smartCase.Details, smartCase.Content[0].Text)
	}
}

func findOutputPaths(output string) []string {
	var paths []string
	for _, line := range strings.Split(output, "\n") {
		if line == "" || strings.HasPrefix(line, "[") {
			continue
		}
		if annotation := strings.Index(line, "  ["); annotation >= 0 {
			line = line[:annotation]
		}
		paths = append(paths, line)
	}
	return paths
}

var groupedMatchLine = regexp.MustCompile(`^ ([0-9]+): (.*)$`)

func parseGroupedGrepMatches(t *testing.T, output string) []string {
	t.Helper()
	currentFile := ""
	var matches []string
	for _, line := range strings.Split(output, "\n") {
		if line == "" || strings.HasPrefix(line, "[") {
			continue
		}
		if parsed := groupedMatchLine.FindStringSubmatch(line); len(parsed) == 3 {
			if currentFile == "" {
				t.Fatalf("grep match appeared before a file header: %q", line)
			}
			matches = append(matches, fmt.Sprintf("%s:%s:%s", currentFile, parsed[1], parsed[2]))
			continue
		}
		if strings.HasPrefix(line, " ") {
			continue
		}
		if annotation := strings.Index(line, "  ["); annotation >= 0 {
			line = line[:annotation]
		}
		currentFile = line
	}
	return matches
}

func TestFFFExampleRepoBuiltinGrepCursorMatchesFilesystemOracle(t *testing.T) {
	root := requireFFFExampleRepo(t)
	registry := harness.NewToolRegistry()
	pool, err := harness.RegisterBuiltinToolsManaged(registry, harness.BuiltinConfig{
		Cwd:            root,
		FFFLibraryPath: os.Getenv("BEST_HARNESS_FFF_LIBRARY"),
		FFFScanTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := pool.Close(); err != nil {
			t.Errorf("close FFF pool: %v", err)
		}
	})

	want := grepFixtureOracle(t, root, regexp.MustCompile(regexp.QuoteMeta("package main")), func(path string) bool {
		return strings.HasSuffix(path, ".go")
	})
	var got []string
	cursor := ""
	seenCursors := make(map[string]bool)
	softPageObserved := false
	for request := 0; request < 100; request++ {
		arguments := fmt.Sprintf(`{"pattern":"package main","path":"*.go","limit":3,"cursor":%q}`, cursor)
		result := executeSearchTool(t, registry, "grep", arguments)
		details := result.Details.(harness.GrepDetails)
		if details.Matches > 3 {
			softPageObserved = true
		}
		got = append(got, parseGroupedGrepMatches(t, result.Content[0].Text)...)
		if details.Cursor == "" {
			break
		}
		if seenCursors[details.Cursor] {
			t.Fatalf("grep cursor repeated without terminating: %q", details.Cursor)
		}
		seenCursors[details.Cursor] = true
		cursor = details.Cursor
		if request == 99 {
			t.Fatal("grep pagination did not terminate after 100 requests")
		}
	}
	if !softPageObserved {
		t.Fatal("fixture did not exercise FFF's file-boundary soft page limit")
	}
	sort.Strings(got)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("builtin grep pagination mismatch\n got (%d): %v\nwant (%d): %v", len(got), got, len(want), want)
	}
}

func TestFFFExampleRepoWatcherTracksCreateAndRemove(t *testing.T) {
	source := requireFFFExampleRepo(t)
	root := filepath.Join(t.TempDir(), "example-repo")
	copyFixtureTree(t, source, root)
	pool := harness.NewFFFPool(harness.FFFOptions{
		LibraryPath: os.Getenv("BEST_HARNESS_FFF_LIBRARY"),
		ScanTimeout: 30 * time.Second,
	})
	t.Cleanup(func() {
		if err := pool.Close(); err != nil {
			t.Errorf("close FFF pool: %v", err)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := pool.Find(ctx, root, harness.FFFPathSearchOptions{Pattern: "*.go", Limit: 100}); err != nil {
		t.Fatal(err)
	}
	probePath := filepath.Join(root, "watch_probe.go")
	const marker = "FFF_E2E_WATCH_MARKER"
	if err := os.WriteFile(probePath, []byte("package main\n\n// "+marker+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	waitForFFF(t, 10*time.Second, func() (bool, error) {
		files, err := pool.Find(ctx, root, harness.FFFPathSearchOptions{Pattern: "watch_probe.go", Limit: 10})
		return err == nil && len(files.Files) == 1 && filepath.ToSlash(files.Files[0].Path) == "watch_probe.go", err
	}, "watcher to index a created file")
	waitForFFF(t, 10*time.Second, func() (bool, error) {
		result, err := pool.Grep(ctx, root, harness.FFFContentSearchOptions{
			Pattern: marker, Mode: harness.FFFGrepPlain, Limit: 10, TimeBudget: time.Second,
		})
		return err == nil && len(result.Matches) == 1 && filepath.ToSlash(result.Matches[0].Path) == "watch_probe.go", err
	}, "content index to expose the created file")

	if err := os.Remove(probePath); err != nil {
		t.Fatal(err)
	}
	waitForFFF(t, 10*time.Second, func() (bool, error) {
		files, err := pool.Find(ctx, root, harness.FFFPathSearchOptions{Pattern: "watch_probe.go", Limit: 10})
		return err == nil && len(files.Files) == 0, err
	}, "watcher to remove a deleted file")
}

func copyFixtureTree(t *testing.T, source, destination string) {
	t.Helper()
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, content, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func waitForFFF(t *testing.T, timeout time.Duration, condition func() (bool, error), description string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		ok, err := condition()
		if ok {
			return
		}
		lastErr = err
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s: %v", description, lastErr)
}
