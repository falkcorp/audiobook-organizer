// file: internal/database/memdb_sync_race_test.go
// version: 1.0.0
// guid: 4e7b9d21-8f3a-4c56-9e02-7a1d5b8c3f60
// last-edited: 2026-08-07

package database

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestUpsertBookToMemDB_SnapshotsCallerBookAtEnqueue is a regression test for
// the memdb-warmup caller-pointer data race (CI, PR #2170): UpsertBookToMemDB
// used to capture the caller's *Book in the memSync closure, and while warmup
// buffering was active that closure ran much later on the warmup goroutine
// (publishWarmMemStore -> applyMemSync -> stripBookForMemdb's `cp := *src`),
// dereferencing the caller's live struct while the caller was still mutating
// it (UpdateBook writes book.ID after the call returns).
//
// The race is timing-dependent in production: a full -race run of this package
// passed 0-races locally while CI caught it. So this test does NOT hope to
// catch it — it forces the exact interleaving deterministically:
//
//  1. arm the warmup pending buffer (as if warmup were still in flight),
//  2. enqueue an upsert for a caller-owned *Book (the closure is buffered,
//     not run inline),
//  3. start a goroutine that mutates that same *Book in a tight loop with no
//     synchronization,
//  4. replay the buffered op via publishWarmMemStore while the mutator is
//     provably running.
//
// On the unfixed code the replay's read of the caller's struct overlaps the
// unsynchronized writes and the race detector flags it on every run. With the
// fix (snapshot copied at enqueue time) the closure never touches the caller's
// struct, so `go test -race` stays green. Run with -race or it proves nothing.
func TestUpsertBookToMemDB_SnapshotsCallerBookAtEnqueue(t *testing.T) {
	store := setupTestPebbleStore(t)
	store.WaitForWarmup()
	require.True(t, store.UseMemDB, "test must exercise the memdb path")

	// The caller-owned Book, exactly like CreateBook/UpdateBook callers hold.
	book := &Book{
		ID:       "race-book-0",
		Title:    "original title",
		FilePath: "/tmp/race-book.m4b",
	}

	// Re-arm the pending buffer, putting the store back into the async-warmup
	// window: memSync now buffers the closure instead of running it inline.
	store.beginMemWarmupBuffering()
	store.UpsertBookToMemDB(context.Background(), book)

	// Fresh MemStore to publish into, same as the warmup goroutine builds.
	m, err := NewMemStore()
	require.NoError(t, err)

	// Mutate the caller's struct in a tight unsynchronized loop, exactly what
	// UpdateBook's `book.ID = id` write-back does after the call returns.
	started := make(chan struct{})
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; ; i++ {
			if i == 1 {
				close(started) // loop is provably live before the replay runs
			}
			select {
			case <-stop:
				return
			default:
			}
			book.Title = fmt.Sprintf("mutated title %d", i)
			book.ID = fmt.Sprintf("race-book-%d", i)
		}
	}()
	<-started

	// Replay the buffered op — this is the warmup-goroutine side of the race.
	// On unfixed code this dereferences the caller's *Book right here, while
	// the goroutine above is writing it.
	require.True(t, store.publishWarmMemStore(m), "publish must succeed")

	close(stop)
	<-done
}
