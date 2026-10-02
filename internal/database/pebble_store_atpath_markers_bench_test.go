// file: internal/database/pebble_store_atpath_markers_bench_test.go
// version: 1.0.0
// guid: c3d81f5a-6e24-4b97-a0f2-1d9e7b45c8a0
// last-edited: 2026-10-02

package database

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
)

// BenchmarkLiveBookPathsUnderDir_After50kUpdates measures one folder lookup
// after 50,000 UpdateBook calls. Before 2026-10-02 every UpdateBook left a
// tombstone in book_atpath_undecodable:, and every lookup range-scanned that
// family, so the lookup's cost grew with the number of book writes since the
// last compaction. Uses only exported store APIs, so the same file runs
// unchanged against an older checkout for the before/after comparison.
//
//	go test ./internal/database/ -run '^$' -bench LiveBookPathsUnderDir_After50k -benchmem
func BenchmarkLiveBookPathsUnderDir_After50kUpdates(b *testing.B) {
	s := after50kUpdatesStore(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		got, err := s.LiveBookPathsUnderDir("/lib/d7")
		if err != nil {
			b.Fatal(err)
		}
		if len(got) != after50kBooks/after50kDirs {
			b.Fatalf("got %d books, want %d", len(got), after50kBooks/after50kDirs)
		}
	}
}

const (
	after50kBooks   = 500
	after50kDirs    = 50
	after50kUpdates = 50_000
)

var (
	after50kOnce  sync.Once
	after50kStore *PebbleStore
	after50kErr   error
)

// after50kUpdatesStore builds the store once per process: the benchmark
// function is re-entered for every b.N probe and the setup dominates.
func after50kUpdatesStore(b *testing.B) *PebbleStore {
	b.Helper()
	after50kOnce.Do(func() {
		dir, err := os.MkdirTemp("", "atpath-marker-bench")
		if err != nil {
			after50kErr = err
			return
		}
		s, err := NewPebbleStore(dir)
		if err != nil {
			after50kErr = err
			return
		}
		s.WaitForWarmup()
		ids := make([]string, 0, after50kBooks)
		for i := 0; i < after50kBooks; i++ {
			bk, err := s.CreateBook(&Book{Title: fmt.Sprintf("t%d", i),
				FilePath: fmt.Sprintf("/lib/d%d/b%d.m4b", i%after50kDirs, i)})
			if err != nil {
				after50kErr = err
				return
			}
			ids = append(ids, bk.ID)
		}
		if _, err := s.BackfillBookAtPathIndex(context.Background()); err != nil {
			after50kErr = err
			return
		}
		for i := 0; i < after50kUpdates; i++ {
			id := ids[i%len(ids)]
			bk, err := s.GetBookByID(id)
			if err != nil {
				after50kErr = err
				return
			}
			bk.Title = fmt.Sprintf("t%d-%d", i%len(ids), i)
			if _, err := s.UpdateBook(id, bk); err != nil {
				after50kErr = err
				return
			}
		}
		after50kStore = s
	})
	if after50kErr != nil {
		b.Fatal(after50kErr)
	}
	return after50kStore
}
