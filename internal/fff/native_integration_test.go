package fff

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNativeFindAndGrep(t *testing.T) {
	library := os.Getenv("BEST_HARNESS_FFF_LIBRARY")
	if library == "" && os.Getenv("BEST_HARNESS_FFF_INTEGRATION") == "" {
		t.Skip("set BEST_HARNESS_FFF_INTEGRATION=1 to test the pinned release asset")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("alpha\ngamma\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"other.txt", "third.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("nothing\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	pool := NewPool(Options{LibraryPath: library})
	defer pool.Close()
	files, err := pool.Find(context.Background(), dir, FindOptions{Pattern: "*.txt", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(files.Files) != 3 || files.TotalMatched != 3 {
		t.Fatalf("find=%#v", files)
	}
	firstPage, err := pool.Find(context.Background(), dir, FindOptions{Pattern: "*.txt", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	secondPage, err := pool.Find(context.Background(), dir, FindOptions{Pattern: "*.txt", Limit: 1, Page: firstPage.NextPage})
	if err != nil {
		t.Fatal(err)
	}
	if len(firstPage.Files) != 1 || firstPage.NextPage != 1 || len(secondPage.Files) != 1 || firstPage.Files[0].Path == secondPage.Files[0].Path {
		t.Fatalf("pages first=%#v second=%#v", firstPage, secondPage)
	}

	hits, err := pool.Grep(context.Background(), dir, GrepOptions{Pattern: "gamma", Mode: GrepPlain, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits.Matches) != 1 || hits.Matches[0].Path != "notes.txt" || hits.Matches[0].Line != 2 || hits.Matches[0].Text != "gamma" {
		t.Fatalf("grep=%#v", hits)
	}
	contextHits, err := pool.Grep(context.Background(), dir, GrepOptions{Pattern: "gamma", Mode: GrepPlain, Limit: 10, BeforeContext: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(contextHits.Matches) != 1 || len(contextHits.Matches[0].ContextBefore) != 1 || contextHits.Matches[0].ContextBefore[0] != "alpha" {
		t.Fatalf("context grep=%#v", contextHits)
	}
	grepPage, err := pool.Grep(context.Background(), dir, GrepOptions{Pattern: "nothing", Mode: GrepPlain, Limit: 1, MaxPerFile: 200, TimeBudget: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	grepNext, err := pool.Grep(context.Background(), dir, GrepOptions{Pattern: "nothing", Mode: GrepPlain, Limit: 1, MaxPerFile: 200, FileOffset: grepPage.NextFilePage, TimeBudget: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if len(grepPage.Matches) != 1 || grepPage.NextFilePage == 0 || len(grepNext.Matches) != 1 || grepPage.Matches[0].Path == grepNext.Matches[0].Path {
		t.Fatalf("grep pages first=%#v second=%#v", grepPage, grepNext)
	}

	fallback, err := pool.Grep(context.Background(), dir, GrepOptions{Pattern: "(", Mode: GrepRegex, Limit: 10, TimeBudget: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if fallback.RegexFallbackError == "" {
		t.Fatalf("expected regex fallback metadata: %#v", fallback)
	}
}
