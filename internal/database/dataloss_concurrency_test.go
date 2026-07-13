// file: internal/database/dataloss_concurrency_test.go
// version: 1.0.0
// guid: a6b7c8d9-0e1f-2a3b-4c5d-concurrency001
// last-edited: 2026-07-13

package database

import (
	"fmt"
	"runtime"
	"sync"
	"testing"
)

// T4 — concurrency invariant harness (run under -race).
//
// N workers run interleaved store mutations (UpdateBook metadata edits, soft
// delete via MarkedForDeletion, and hard DeleteBook) over a SHARED store. Work
// is PARTITIONED by book ID: each worker owns a disjoint set of books and never
// touches another worker's row. This is deliberate (CLAUDE.md concurrency rule):
// it keeps the test deterministic — no two workers can produce an
// order-dependent lost update on the same row, so any invariant violation after
// the join is a genuine store-level corruption, not a benign race between two
// legitimate writers. Meanwhile the workers still hammer the store's SHARED
// internals concurrently (memdb, secondary indexes, aggregate caches), which is
// exactly what -race is here to vet.
//
// Randomness is seeded from a fixed arithmetic table (worker, step), never from
// time or math/rand, so runs are reproducible.
//
// NOTE: merge.SoftDeleteBook / MergeBooks live in package merge and cannot be
// called from a white-box database test without an import cycle. Merge/combine
// concurrency is covered separately by internal/merge/service_concurrent_test.go
// and by the dbtest.AssertStoreInvariants calls wired into the merge tests.
func TestConcurrency_StoreInvariantsHold(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	workers := runtime.NumCPU()
	if workers < 4 {
		workers = 4
	}
	if workers > 16 {
		workers = 16
	}
	const booksPerWorker = 3
	const steps = 24

	// Shared work IDs so the book:work index is contended across workers even
	// though each row belongs to exactly one worker.
	sharedWorks := []string{"WORKC0000000000000000000A", "WORKC0000000000000000000B"}

	type owned struct{ ids []string }
	all := make([]owned, workers)
	for w := 0; w < workers; w++ {
		for k := 0; k < booksPerWorker; k++ {
			b, err := store.CreateBook(&Book{
				Title:    fmt.Sprintf("w%d-b%d", w, k),
				FilePath: fmt.Sprintf("/lib/conc/w%d-b%d.m4b", w, k),
				WorkID:   strPtr(sharedWorks[(w+k)%len(sharedWorks)]),
			})
			if err != nil {
				t.Fatalf("CreateBook w%d k%d: %v", w, k, err)
			}
			all[w].ids = append(all[w].ids, b.ID)
		}
	}

	var wg sync.WaitGroup
	errs := make([][]error, workers)
	start := make(chan struct{})
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			<-start
			deleted := map[string]bool{}
			for s := 0; s < steps; s++ {
				id := all[w].ids[(w+s)%booksPerWorker]
				if deleted[id] {
					continue
				}
				// Deterministic op selection from a fixed table.
				switch (w*7 + s*3) % 5 {
				case 0, 1: // metadata edit (title)
					if err := updateTitleConc(store, id, fmt.Sprintf("w%d-s%d", w, s)); err != nil {
						errs[w] = append(errs[w], err)
					}
				case 2: // toggle work id (index churn)
					if err := updateWorkConc(store, id, sharedWorks[s%len(sharedWorks)]); err != nil {
						errs[w] = append(errs[w], err)
					}
				case 3: // soft delete via MarkedForDeletion
					if err := softDeleteConc(store, id); err != nil {
						errs[w] = append(errs[w], err)
					}
				case 4: // hard delete on the LAST touch of this book
					if s >= steps-booksPerWorker {
						if err := store.DeleteBook(id); err != nil {
							errs[w] = append(errs[w], err)
						}
						deleted[id] = true
					}
				}
			}
		}(w)
	}
	close(start)
	wg.Wait()

	for w := range errs {
		for _, e := range errs[w] {
			t.Errorf("worker %d op error: %v", w, e)
		}
	}

	assertStoreInvariants(t, store)
}

func updateTitleConc(store Store, id, title string) error {
	cur, err := store.GetBookByID(id)
	if err != nil || cur == nil {
		return err
	}
	cur.Title = title
	_, err = store.UpdateBook(id, cur)
	return err
}

func updateWorkConc(store Store, id, work string) error {
	cur, err := store.GetBookByID(id)
	if err != nil || cur == nil {
		return err
	}
	cur.WorkID = &work
	_, err = store.UpdateBook(id, cur)
	return err
}

func softDeleteConc(store Store, id string) error {
	cur, err := store.GetBookByID(id)
	if err != nil || cur == nil {
		return err
	}
	tru := true
	cur.MarkedForDeletion = &tru
	_, err = store.UpdateBook(id, cur)
	return err
}
