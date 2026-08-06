// file: internal/database/memdb_warmup_writeloss_test.go
// version: 1.0.0
// guid: 5e1c9f27-3a64-4b18-9d02-c7f5a8e3b410
// last-edited: 2026-08-06

package database

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// Acceptance tests for the memdb warmup lost-update window.
//
// THE BUG (pre-fix): NewPebbleStore returns immediately and warms memdb in a
// goroutine. memSync no-ops while memPtr is nil, and WarmFromPebble scans the
// live DB through an iterator whose view is fixed when it is created. A write
// that lands after that iterator exists but before memPtr.Store is therefore
// dropped from memdb *permanently* — it is in Pebble, but the published
// snapshot predates it and nothing ever re-warms. Every memdb-backed read is
// wrong for the rest of the process lifetime.
//
// THE INVARIANT these tests pin: no write that succeeds in Pebble may be
// absent from the published memdb. That holds for deletes too — a dropped
// delete leaves a phantom row in memdb, which is worse than a dropped create
// because callers act on rows that no longer exist.

// countIDs turns ListBookIDs output into a set for membership assertions.
func countIDs(t *testing.T, store *PebbleStore) map[string]bool {
	t.Helper()
	ids, err := store.ListBookIDs()
	if err != nil {
		t.Fatalf("ListBookIDs: %v", err)
	}
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set
}

// seedBooksStore creates a store at dir, writes n real books through the
// normal write path, and closes it. Reopening dir then forces a warmup that
// has to rescan all n books, which is what widens the write-loss window
// enough to observe deterministically.
func seedBooksStore(t *testing.T, dir string, n int) {
	t.Helper()
	seed, err := NewPebbleStore(dir)
	if err != nil {
		t.Fatalf("seed NewPebbleStore: %v", err)
	}
	seed.WaitForWarmup()
	for i := 0; i < n; i++ {
		if _, err := seed.CreateBook(&Book{
			Title:    fmt.Sprintf("Seed %d", i),
			FilePath: fmt.Sprintf("/seed/%d", i),
		}); err != nil {
			t.Fatalf("seed CreateBook %d: %v", i, err)
		}
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("seed Close: %v", err)
	}
}

// TestWarmupWriteLoss_CreateDuringWarmupIsVisible is the primary acceptance
// test, ported from the reproduction artifact. Pre-fix it reports books that
// are readable from Pebble but missing from the published memdb.
func TestWarmupWriteLoss_CreateDuringWarmupIsVisible(t *testing.T) {
	dir := t.TempDir()
	seedBooksStore(t, dir, 3000)

	store, err := NewPebbleStore(dir)
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer store.Close()

	// Write continuously for the whole warmup window. Writes made before the
	// books iterator exists are picked up by the scan; writes made after it
	// are the ones the bug drops.
	var written []string
	for !store.IsMemReady() {
		b, err := store.CreateBook(&Book{
			Title:    fmt.Sprintf("DuringWarmup %d", len(written)),
			FilePath: fmt.Sprintf("/during/%d", len(written)),
		})
		if err != nil {
			t.Fatalf("CreateBook during warmup: %v", err)
		}
		written = append(written, b.ID)
	}
	store.WaitForWarmup()

	if !store.IsMemReady() {
		t.Fatal("memdb never published; this test only means something on the memdb read path")
	}
	// Guard against a vacuous pass: if warmup finished before a single write
	// landed there was no window to exercise, and "0 lost" proves nothing.
	if len(written) == 0 {
		t.Skip("warmup completed before any write landed; window too narrow on this run")
	}

	inMem := countIDs(t, store)
	var lost []string
	for _, id := range written {
		if !inMem[id] {
			lost = append(lost, id)
		}
	}

	t.Logf("wrote %d books during the warmup window", len(written))
	t.Logf("ListBookIDs (memdb path) returned %d ids; expected %d", len(inMem), 3000+len(written))
	t.Logf("writes LOST from memdb: %d", len(lost))

	// Every lost book is still readable from Pebble — proving this is a memdb
	// visibility loss, not a failed write.
	for _, id := range lost {
		b, err := store.GetBookByID(id)
		if err != nil {
			t.Fatalf("GetBookByID %s: %v", id, err)
		}
		if b == nil {
			t.Fatalf("book %s missing from Pebble too", id)
		}
	}

	if len(lost) > 0 {
		t.Errorf("INVARIANT VIOLATED: %d/%d books written during warmup are in Pebble but absent from the published memdb (e.g. %s)",
			len(lost), len(written), lost[0])
	}
}

// TestWarmupWriteLoss_DeleteDuringWarmupLeavesNoPhantom is the mirror-image
// case. The victims are seeded BEFORE the store is reopened, so the warmup scan
// captures them; deleting them during the window must not leave the rows behind
// in the published memdb. A dropped delete is worse than a dropped create:
// orphan-cleanup and dedup then act on rows that no longer exist.
//
// Deletes run in a loop for the whole window rather than as a single call. One
// delete issued immediately after NewPebbleStore usually lands before the
// books iterator is even created, so the scan never sees the row and the test
// passes without exercising anything — the loop guarantees some deletes land
// inside the actual window.
func TestWarmupWriteLoss_DeleteDuringWarmupLeavesNoPhantom(t *testing.T) {
	dir := t.TempDir()
	seedBooksStore(t, dir, 2000)

	// Collect IDs the warmup scan will see.
	pre, err := NewPebbleStore(dir)
	if err != nil {
		t.Fatalf("NewPebbleStore (id lookup): %v", err)
	}
	pre.WaitForWarmup()
	victims, err := pre.ListBookIDs()
	if err != nil {
		t.Fatalf("ListBookIDs: %v", err)
	}
	if len(victims) == 0 {
		t.Fatal("no seeded books found")
	}
	if err := pre.Close(); err != nil {
		t.Fatalf("Close (id lookup): %v", err)
	}

	store, err := NewPebbleStore(dir)
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer store.Close()

	// Delete for the whole warmup window. Pre-fix, DeleteBookFromMemDB's
	// write-through is swallowed by memSync while memPtr is nil, and the scan's
	// snapshot still contains the row, so it is published as a phantom.
	var deleted []string
	for i := 0; i < len(victims) && !store.IsMemReady(); i++ {
		if err := store.DeleteBook(victims[i]); err != nil {
			t.Fatalf("DeleteBook: %v", err)
		}
		deleted = append(deleted, victims[i])
	}
	store.WaitForWarmup()

	if !store.IsMemReady() {
		t.Fatal("memdb never published; this test only means something on the memdb read path")
	}
	// Guard against a vacuous pass — see the create test.
	if len(deleted) == 0 {
		t.Skip("warmup completed before any delete landed; window too narrow on this run")
	}

	inMem := countIDs(t, store)
	var phantoms []string
	for _, id := range deleted {
		// Gone from Pebble...
		b, err := store.GetBookByID(id)
		if err != nil {
			t.Fatalf("GetBookByID %s: %v", id, err)
		}
		if b != nil {
			t.Fatalf("book %s still in Pebble; test setup is wrong", id)
		}
		// ...therefore it must be gone from memdb.
		if inMem[id] {
			phantoms = append(phantoms, id)
		}
	}

	t.Logf("deleted %d books during the warmup window; %d phantoms left in memdb", len(deleted), len(phantoms))
	if len(phantoms) > 0 {
		t.Errorf("INVARIANT VIOLATED: %d/%d books deleted from Pebble during warmup are still present in the published memdb (phantom rows, e.g. %s)",
			len(phantoms), len(deleted), phantoms[0])
	}
}

// TestWarmupWriteLoss_ConcurrentWritesAllVisible drives the window from
// several goroutines at once, which is the production shape (HTTP handlers
// serve immediately while warmup runs). Run with -race.
func TestWarmupWriteLoss_ConcurrentWritesAllVisible(t *testing.T) {
	dir := t.TempDir()
	seedBooksStore(t, dir, 2000)

	store, err := NewPebbleStore(dir)
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer store.Close()

	const writers = 4
	var mu sync.Mutex
	var written []string
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; !store.IsMemReady(); i++ {
				b, err := store.CreateBook(&Book{
					Title:    fmt.Sprintf("Concurrent %d-%d", w, i),
					FilePath: fmt.Sprintf("/conc/%d/%d", w, i),
				})
				if err != nil {
					t.Errorf("CreateBook: %v", err)
					return
				}
				mu.Lock()
				written = append(written, b.ID)
				mu.Unlock()
			}
		}(w)
	}
	wg.Wait()
	store.WaitForWarmup()

	if !store.IsMemReady() {
		t.Fatal("memdb never published; this test only means something on the memdb read path")
	}
	// Guard against a vacuous pass — see the create test.
	if len(written) == 0 {
		t.Skip("warmup completed before any write landed; window too narrow on this run")
	}

	inMem := countIDs(t, store)
	lost := 0
	for _, id := range written {
		if !inMem[id] {
			lost++
		}
	}
	t.Logf("concurrent writers produced %d books during warmup; %d lost", len(written), lost)
	if lost > 0 {
		t.Errorf("INVARIANT VIOLATED: %d/%d concurrently-written books are missing from the published memdb", lost, len(written))
	}
}

// TestWarmupWriteLoss_BufferOverflowDegradesLoudly pins the failure mode. If
// the pending-write buffer cannot hold every write-through that arrived during
// warmup, publishing the memdb anyway would republish the original bug — a
// snapshot that silently lacks rows. The store must instead refuse to publish
// (memPtr stays nil → Pebble-only reads, which are correct just slower) and
// say so at ERROR level.
func TestWarmupWriteLoss_BufferOverflowDegradesLoudly(t *testing.T) {
	var logBuf bytes.Buffer
	var logMu sync.Mutex
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&lockedWriter{mu: &logMu, buf: &logBuf}, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	prevCap := memPendingOpCap
	memPendingOpCap = 2
	defer func() { memPendingOpCap = prevCap }()

	dir := t.TempDir()
	// Seed raw keys: fast to write, slow to warm, so the window is wide.
	seedPebbleDir(t, dir, 20000)

	store, err := NewPebbleStore(dir)
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer store.Close()

	// Comfortably more write-throughs than the cap allows.
	var written []string
	for i := 0; i < 25; i++ {
		b, err := store.CreateBook(&Book{
			Title:    fmt.Sprintf("Overflow %d", i),
			FilePath: fmt.Sprintf("/overflow/%d", i),
		})
		if err != nil {
			t.Fatalf("CreateBook: %v", err)
		}
		written = append(written, b.ID)
	}
	store.WaitForWarmup()

	logMu.Lock()
	logs := logBuf.String()
	logMu.Unlock()

	// If warmup won the race and published before the cap was reached, there was
	// no overflow to observe. Skip rather than fail — the assertions below are
	// about overflow behaviour, not about who won.
	if !strings.Contains(logs, "pending write buffer overflowed") {
		t.Skip("warmup completed before the buffer overflowed; window too narrow on this run")
	}

	if store.IsMemReady() {
		t.Fatal("memdb was published despite a pending-write buffer overflow: the published snapshot cannot be trusted to contain every Pebble write")
	}

	// Degraded reads must still be correct — Pebble is authoritative.
	inMem := countIDs(t, store)
	for _, id := range written {
		if !inMem[id] {
			t.Errorf("book %s missing from the Pebble-only read path", id)
		}
	}

	// ...and the degradation must be LOUD. Match the specific message, not just
	// any ERROR line: other tests in this package run in parallel and could
	// otherwise satisfy a bare "level=ERROR" check.
	if !strings.Contains(logs, "level=ERROR msg=\"memdb warmup: pending write buffer overflowed") {
		t.Errorf("buffer overflow must be logged at ERROR level.\nlogs:\n%s", logs)
	}
}

// TestWarmupWriteLoss_ResetDuringWarmupIsNotUndone covers the mirror of the
// main bug on the Reset path: a warmup that started before Reset wiped the
// keyspace holds a snapshot full of rows that no longer exist, and publishing
// it afterwards resurrects every one of them in memdb.
func TestWarmupWriteLoss_ResetDuringWarmupIsNotUndone(t *testing.T) {
	dir := t.TempDir()
	seedBooksStore(t, dir, 2000)

	store, err := NewPebbleStore(dir)
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer store.Close()

	// Reset while warmup is still scanning the pre-wipe data.
	if err := store.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	store.WaitForWarmup()

	ids, err := store.ListBookIDs()
	if err != nil {
		t.Fatalf("ListBookIDs: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("Reset was undone by the in-flight warmup: %d books resurrected in memdb after the keyspace was wiped", len(ids))
	}
}

// lockedWriter serialises slog output so the -race detector has nothing to
// complain about when warmup logs from its own goroutine.
type lockedWriter struct {
	mu  *sync.Mutex
	buf *bytes.Buffer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}
