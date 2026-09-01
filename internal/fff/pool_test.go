package fff

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestPoolEvictsIdleRoots(t *testing.T) {
	pool := NewPool(Options{MaxRoots: 2})
	for _, root := range []string{"a", "b", "c"} {
		entry, evicted, err := pool.acquire(root)
		if err != nil {
			t.Fatal(err)
		}
		for _, finder := range evicted {
			finder.close()
		}
		pool.release(entry)
	}
	pool.mu.Lock()
	count := len(pool.byRoot)
	absoluteB, _ := filepath.Abs("b")
	absoluteC, _ := filepath.Abs("c")
	_, keptB := pool.byRoot[absoluteB]
	_, keptC := pool.byRoot[absoluteC]
	pool.mu.Unlock()
	if count != 2 || !keptB || !keptC {
		t.Fatalf("roots=%d keptB=%v keptC=%v", count, keptB, keptC)
	}
	_ = pool.Close()
}

func TestPoolCloseWaitsForActiveSearchAndRejectsReuse(t *testing.T) {
	pool := NewPool(Options{MaxRoots: 1})
	entry, _, err := pool.acquire("root")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		_ = pool.Close()
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("Close returned while a search was active")
	case <-time.After(20 * time.Millisecond):
	}
	pool.release(entry)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close did not return")
	}
	_, err = pool.Find(context.Background(), "root", FindOptions{Pattern: "x"})
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("Find error=%v", err)
	}
}

func TestFindPageOffsetUsesPageSize(t *testing.T) {
	tests := []struct {
		page, limit int
		want        uint32
	}{
		{page: 0, limit: 7, want: 0},
		{page: 1, limit: 7, want: 7},
		{page: 5, limit: 30, want: 150},
	}
	for _, test := range tests {
		got, err := findPageOffset(test.page, test.limit)
		if err != nil || got != test.want {
			t.Fatalf("page=%d limit=%d: offset=%d err=%v, want %d", test.page, test.limit, got, err, test.want)
		}
	}
	maxInt := int(^uint(0) >> 1)
	if _, err := findPageOffset(maxInt, 3); err == nil {
		t.Fatal("expected native pagination overflow error")
	}
}
