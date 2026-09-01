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
