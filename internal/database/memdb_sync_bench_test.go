// file: internal/database/memdb_sync_bench_test.go
// version: 1.0.0
// guid: 0e7c4a92-6d15-4b83-a9f0-2c58e1d76b34
// last-edited: 2026-08-14

package database

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

// BenchmarkParallelUpdateBook measures UpdateBook throughput under concurrent
// writers — the C816 shape. Before the fix, every UpsertBookToMemDB performed
// three Pebble reads INSIDE go-memdb's single global writer mutex, so parallel
// writers serialized behind each other's I/O.
func BenchmarkParallelUpdateBook(b *testing.B) {
	tmpdir := b.TempDir()
	store, err := NewPebbleStore(tmpdir)
	if err != nil {
		b.Fatalf("NewPebbleStore: %v", err)
	}
	defer store.Close()
	store.WaitForWarmup()
	store.UseMemDB = true

	const nBooks = 32
	books := make([]*Book, nBooks)
	for i := 0; i < nBooks; i++ {
		bk, err := store.CreateBook(&Book{Title: fmt.Sprintf("bench-%02d", i), FilePath: fmt.Sprintf("/b/%02d", i)})
		if err != nil {
			b.Fatalf("CreateBook: %v", err)
		}
		// Realistic fingerprint payloads: loadBookFilesForBookID prefix-scans
		// and unmarshals every fingerprint-bearing row, and that unmarshal cost
		// is exactly what C816 moved out of the global writer lock. Production
		// AcoustID fingerprints run tens of KB per file.
		fp := []byte(strings.Repeat("AQADtEmSJEmSJEcOHzh8", 1500)) // ~30KB
		for j := 0; j < 8; j++ {
			if err := store.CreateBookFile(&BookFile{BookID: bk.ID, FilePath: fmt.Sprintf("/b/%02d/f%d.mp3", i, j), Duration: 60, AcoustIDFingerprint: fp}); err != nil {
				b.Fatalf("CreateBookFile: %v", err)
			}
		}
		books[i] = bk
	}

	b.ResetTimer()
	var idx int64
	var mu sync.Mutex
	next := func() *Book { mu.Lock(); defer mu.Unlock(); idx++; return books[idx%nBooks] }
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			bk := next()
			if _, err := store.UpdateBook(bk.ID, bk); err != nil {
				b.Fatalf("UpdateBook: %v", err)
			}
		}
	})
}
