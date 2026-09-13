// file: internal/database/pebble_store_atpath_index_test.go
// version: 1.1.0
// guid: 9e4b2d7a-1c86-4f35-8a0e-5b3c7d9f1e62
// last-edited: 2026-09-12

package database

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"pgregory.net/rapid"
)

// hostilePaths is the key-format regression alphabet: empty, ':'-bearing
// prefixes of each other, a NUL-bearing (corrupt) path, non-ASCII, trailing
// space.
var hostilePaths = []string{"", "/a", "/a:x", "/a:x:y", "/a/b", "/a\x00b", "/ä", "/a "}

func newAtPathStore(t testing.TB) *PebbleStore {
	t.Helper()
	s, err := NewPebbleStoreInMemory(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	s.WaitForWarmup()
	return s
}

// resetAtPathIndex wipes every index key and the sentinel, simulating a store
// written by a pre-index binary.
func resetAtPathIndex(t testing.TB, s *PebbleStore) {
	t.Helper()
	if err := s.db.DeleteRange([]byte(bookAtPathPrefix), bareRowUpperBound(bookAtPathPrefix), pebble.Sync); err != nil {
		t.Fatalf("delete range: %v", err)
	}
	if err := s.db.Delete([]byte(bookAtPathBackfillKey), pebble.Sync); err != nil {
		t.Fatalf("delete sentinel: %v", err)
	}
	s.bookAtPathBuilt.Store(false)
}

func mustBackfill(t testing.TB, s *PebbleStore) {
	t.Helper()
	if _, err := s.BackfillBookAtPathIndex(context.Background()); err != nil {
		t.Fatalf("backfill: %v", err)
	}
}

// fataler is the slice of testing.TB that *rapid.T also satisfies.
type fataler interface {
	Helper()
	Fatalf(format string, args ...any)
}

// checkAgainstOracle asserts, for every path in paths, that the index answer
// equals the full-scan answer, and that verify finds no missing key.
func checkAgainstOracle(t fataler, s *PebbleStore, paths []string) {
	t.Helper()
	for _, p := range paths {
		want, err := s.liveBookIDsAtPathScan(p)
		if err != nil {
			t.Fatalf("scan(%q): %v", p, err)
		}
		got, err := s.liveBookIDsAtPathIndex(p)
		if err != nil {
			t.Fatalf("index(%q): %v", p, err)
		}
		if len(want) == 0 && len(got) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("path %q: index = %v, scan = %v", p, got, want)
		}
	}
	rep, err := s.VerifyBookAtPathIndex(context.Background())
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if rep.MissingLive != 0 || rep.MissingTrashed != 0 {
		t.Fatalf("verify found missing keys: %+v", rep)
	}
}

func TestBookAtPathKey_BoundsAreExact(t *testing.T) {
	for _, p := range hostilePaths {
		lo, hi := bookAtPathBounds(p)
		for _, q := range hostilePaths {
			k := bookAtPathKey(q, "01ID")
			in := string(k) >= string(lo) && string(k) < string(hi)
			// The only way another path's key lands in P's range is a NUL at
			// position len(P) in Q, i.e. the corrupt "/a\x00b" under "/a".
			// The reader skips that candidate; see TestLiveBookIDsAtPath_NulPath.
			wantIn := q == p || (p == "/a" && q == "/a\x00b")
			if in != wantIn {
				t.Errorf("key for %q in range of %q = %v, want %v", q, p, in, wantIn)
			}
		}
	}
	path, id, ok := splitBookAtPathKey(bookAtPathKey("/a\x00b", "X1"))
	if !ok || path != "/a\x00b" || id != "X1" {
		t.Fatalf("split = %q %q %v", path, id, ok)
	}
}

// TestUpdateBook_StaleRevertKeepsOwnEntry is the Q2 counterexample, landed
// deterministically in UpdateBook's race window. W2 starts a title-only write
// at /A and reads oldBook (/A). Before W2 commits, W1 moves the book to /B and
// commits. W2 then commits its row at /A, seeing "no path change" because its
// own oldBook read is stale. The row ends at /A, so the index must list the
// book at /A. A Set gated on the path changing stages nothing here and leaves
// only the /B key: a false "path is free". (A sequential stale write does NOT
// reproduce this: UpdateBook re-reads oldBook, sees /B, and takes the
// path-changed branch. Checked by mutation on 2026-09-12.)
func TestUpdateBook_StaleRevertKeepsOwnEntry(t *testing.T) {
	s := newAtPathStore(t)
	defer s.Close()
	mustBackfill(t, s)

	b, err := s.CreateBook(&Book{Title: "t", FilePath: "/A"})
	if err != nil {
		t.Fatal(err)
	}
	w2, err := s.GetBookByID(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	w2.Title = "t2"

	defer func() { updateBookAfterOldReadHook = nil }()
	updateBookAfterOldReadHook = func(id string) {
		updateBookAfterOldReadHook = nil // W1's own UpdateBook must not re-enter
		w1, err := s.GetBookByID(id)
		if err != nil {
			t.Error(err)
			return
		}
		w1.FilePath = "/B"
		if _, err := s.UpdateBook(id, w1); err != nil {
			t.Error(err)
		}
	}
	if _, err := s.UpdateBook(b.ID, w2); err != nil {
		t.Fatal(err)
	}
	row, err := s.GetBookByID(b.ID)
	if err != nil || row.FilePath != "/A" {
		t.Fatalf("precondition: W2's lost-update revert should leave the row at /A, got %v %v", row, err)
	}
	ids, err := s.LiveBookIDsAtPath("/A")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != b.ID {
		t.Fatalf("LiveBookIDsAtPath(/A) = %v, want [%s] (false free)", ids, b.ID)
	}
	checkAgainstOracle(t, s, []string{"/A", "/B"})
}

// TestBookAtPathIndex_RandomOps is the primary test: random writes over the
// hostile alphabet, index == scan oracle and no missing key after every step.
func TestBookAtPathIndex_RandomOps(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		s := newAtPathStore(t)
		defer s.Close()
		mustBackfill(t, s)

		var ids []string
		stale := map[string]Book{}
		pathGen := rapid.SampledFrom(hostilePaths)

		pickID := func(label string) (string, bool) {
			if len(ids) == 0 {
				return "", false
			}
			return rapid.SampledFrom(ids).Draw(rt, label), true
		}
		fresh := func(id string) *Book {
			b, err := s.GetBookByID(id)
			if err != nil || b == nil {
				rt.Fatalf("GetBookByID(%s) = %v, %v", id, b, err)
			}
			return b
		}

		steps := rapid.IntRange(1, 25).Draw(rt, "steps")
		for i := 0; i < steps; i++ {
			switch rapid.IntRange(0, 7).Draw(rt, "op") {
			case 0: // create
				b, err := s.CreateBook(&Book{Title: "b", FilePath: pathGen.Draw(rt, "path")})
				if err != nil {
					rt.Fatal(err)
				}
				ids = append(ids, b.ID)
			case 1: // move
				if id, ok := pickID("id"); ok {
					b := fresh(id)
					b.FilePath = pathGen.Draw(rt, "path")
					if _, err := s.UpdateBook(id, b); err != nil {
						rt.Fatal(err)
					}
				}
			case 2: // snapshot for a later stale write
				if id, ok := pickID("id"); ok {
					stale[id] = *fresh(id)
				}
			case 3: // stale title-only write (lost-update revert)
				if id, ok := pickID("id"); ok {
					if b, has := stale[id]; has {
						b.Title = "stale"
						if _, err := s.UpdateBook(id, &b); err != nil {
							rt.Fatal(err)
						}
					}
				}
			case 4: // soft-delete / restore toggle
				if id, ok := pickID("id"); ok {
					b := fresh(id)
					flag := !markedForDeletionFlag(b.MarkedForDeletion)
					b.MarkedForDeletion = &flag
					if _, err := s.UpdateBook(id, b); err != nil {
						rt.Fatal(err)
					}
				}
			case 5: // hard delete
				if id, ok := pickID("id"); ok {
					if err := s.DeleteBook(id); err != nil {
						rt.Fatal(err)
					}
					for j, x := range ids {
						if x == id {
							ids = append(ids[:j], ids[j+1:]...)
							break
						}
					}
					delete(stale, id)
				}
			case 6: // merge-shaped: survivor moves onto loser's path, loser trashed
				if len(ids) >= 2 {
					a := rapid.SampledFrom(ids).Draw(rt, "survivor")
					l := rapid.SampledFrom(ids).Draw(rt, "loser")
					if a != l {
						loser := fresh(l)
						surv := fresh(a)
						surv.FilePath = loser.FilePath
						if _, err := s.UpdateBook(a, surv); err != nil {
							rt.Fatal(err)
						}
						tr := true
						loser.MarkedForDeletion = &tr
						if _, err := s.UpdateBook(l, loser); err != nil {
							rt.Fatal(err)
						}
					}
				}
			case 7: // booksig sidecar migration (rewrites rows, no path change)
				if id, ok := pickID("id"); ok {
					if _, err := s.MigrateBookSigToSidecar(id, false); err != nil {
						rt.Fatal(err)
					}
				}
			}
			checkAgainstOracle(rt, s, hostilePaths)
		}
	})
}

// TestBookAtPathIndex_ConcurrentWriters is the executable form of the
// completeness proof under UpdateBook's self-race. Run with -race.
func TestBookAtPathIndex_ConcurrentWriters(t *testing.T) {
	s := newAtPathStore(t)
	defer s.Close()
	mustBackfill(t, s)

	var shared []string
	for i := 0; i < 4; i++ {
		b, err := s.CreateBook(&Book{Title: "c", FilePath: hostilePaths[i]})
		if err != nil {
			t.Fatal(err)
		}
		shared = append(shared, b.ID)
	}

	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(seed int64) {
			defer wg.Done()
			r := rand.New(rand.NewSource(seed))
			for i := 0; i < 40; i++ {
				switch r.Intn(4) {
				case 0, 1:
					id := shared[r.Intn(len(shared))]
					b, err := s.GetBookByID(id)
					if err != nil || b == nil {
						continue
					}
					if r.Intn(2) == 0 {
						b.FilePath = hostilePaths[r.Intn(len(hostilePaths))]
					} else {
						f := r.Intn(2) == 0
						b.MarkedForDeletion = &f
					}
					_, _ = s.UpdateBook(id, b)
				case 2:
					nb, err := s.CreateBook(&Book{Title: "n", FilePath: hostilePaths[r.Intn(len(hostilePaths))]})
					if err == nil && r.Intn(2) == 0 {
						_ = s.DeleteBook(nb.ID)
					}
				case 3:
					_, _ = s.LiveBookIDsAtPath(hostilePaths[r.Intn(len(hostilePaths))])
				}
			}
		}(int64(w))
	}
	wg.Wait()
	checkAgainstOracle(t, s, hostilePaths)
}

func TestBackfill_BuildsIndexForPreIndexRows(t *testing.T) {
	s := newAtPathStore(t)
	defer s.Close()
	for i, p := range hostilePaths {
		b, err := s.CreateBook(&Book{Title: fmt.Sprint(i), FilePath: p})
		if err != nil {
			t.Fatal(err)
		}
		if i%3 == 0 {
			tr := true
			b.MarkedForDeletion = &tr
			if _, err := s.UpdateBook(b.ID, b); err != nil {
				t.Fatal(err)
			}
		}
	}
	resetAtPathIndex(t, s)

	rep, err := s.VerifyBookAtPathIndex(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.SentinelSet || rep.MissingLive == 0 {
		t.Fatalf("precondition: expected an unbuilt, empty index, got %+v", rep)
	}

	old := bookAtPathBackfillChunk
	bookAtPathBackfillChunk = 2
	defer func() { bookAtPathBackfillChunk = old }()

	res, err := s.BackfillBookAtPathIndex(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Scanned != len(hostilePaths) || res.Commits < 4 {
		t.Fatalf("result = %+v, want %d scanned over several chunks", res, len(hostilePaths))
	}
	built, err := s.bookAtPathIndexBuilt()
	if err != nil || !built {
		t.Fatalf("sentinel not set: %v %v", built, err)
	}
	checkAgainstOracle(t, s, hostilePaths)

	// A second run is a no-op.
	res, err = s.BackfillBookAtPathIndex(context.Background())
	if err != nil || !res.Skipped {
		t.Fatalf("second run = %+v, %v; want skipped", res, err)
	}
}

func TestBackfill_InterruptedRunLeavesNoSentinelAndReaderFallsBack(t *testing.T) {
	s := newAtPathStore(t)
	defer s.Close()
	for i := 0; i < 7; i++ {
		if _, err := s.CreateBook(&Book{Title: "x", FilePath: "/p"}); err != nil {
			t.Fatal(err)
		}
	}
	resetAtPathIndex(t, s)

	oldChunk, oldHook := bookAtPathBackfillChunk, bookAtPathBackfillAfterChunk
	defer func() { bookAtPathBackfillChunk, bookAtPathBackfillAfterChunk = oldChunk, oldHook }()
	bookAtPathBackfillChunk = 2
	ctx, cancel := context.WithCancel(context.Background())
	bookAtPathBackfillAfterChunk = func(commits int) {
		if commits == 1 {
			cancel()
		}
	}
	if _, err := s.BackfillBookAtPathIndex(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	built, err := s.bookAtPathIndexBuilt()
	if err != nil || built {
		t.Fatalf("an interrupted run must not set the sentinel: built=%v err=%v", built, err)
	}

	var viaIndex []bool
	oldRead := bookAtPathReadHook
	bookAtPathReadHook = func(v bool) { viaIndex = append(viaIndex, v) }
	defer func() { bookAtPathReadHook = oldRead }()

	// The partial index holds only the keys committed before the cancel; the
	// reader must not consult it.
	ids, err := s.LiveBookIDsAtPath("/p")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 7 || len(viaIndex) != 1 || viaIndex[0] {
		t.Fatalf("pre-sentinel read: ids=%d viaIndex=%v; want 7 from the scan", len(ids), viaIndex)
	}

	bookAtPathBackfillAfterChunk = nil
	mustBackfill(t, s)
	ids, err = s.LiveBookIDsAtPath("/p")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 7 || !viaIndex[len(viaIndex)-1] {
		t.Fatalf("post-sentinel read: ids=%d viaIndex=%v; want 7 from the index", len(ids), viaIndex)
	}
}

func TestBackfill_ConcurrentMoveBetweenChunksKeepsCompleteness(t *testing.T) {
	s := newAtPathStore(t)
	defer s.Close()
	var ids []string
	for i := 0; i < 6; i++ {
		b, err := s.CreateBook(&Book{Title: "x", FilePath: "/src"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, b.ID)
	}
	resetAtPathIndex(t, s)

	oldChunk, oldHook := bookAtPathBackfillChunk, bookAtPathBackfillAfterChunk
	defer func() { bookAtPathBackfillChunk, bookAtPathBackfillAfterChunk = oldChunk, oldHook }()
	bookAtPathBackfillChunk = 2
	bookAtPathBackfillAfterChunk = func(commits int) {
		if commits != 1 {
			return
		}
		// Move the last book (not yet visited by the scan) and the first (already
		// visited) while the backfill is mid-run.
		for _, id := range []string{ids[0], ids[len(ids)-1]} {
			b, err := s.GetBookByID(id)
			if err != nil {
				t.Error(err)
				return
			}
			b.FilePath = "/dst"
			if _, err := s.UpdateBook(id, b); err != nil {
				t.Error(err)
			}
		}
	}
	mustBackfill(t, s)
	checkAgainstOracle(t, s, []string{"/src", "/dst"})
}

// TestBackfill_WorkerPoolIndexesEveryRow pins the pool at 4 workers so -race
// sees real concurrent workers (and concurrent hook calls) on any machine,
// including a CI runner whose NumCPU is small.
func TestBackfill_WorkerPoolIndexesEveryRow(t *testing.T) {
	s := newAtPathStore(t)
	defer s.Close()
	const n = 200
	paths := make([]string, 10)
	for i := range paths {
		paths[i] = fmt.Sprintf("/pool/%d", i)
	}
	for i := 0; i < n; i++ {
		if _, err := s.CreateBook(&Book{Title: fmt.Sprint(i), FilePath: paths[i%len(paths)]}); err != nil {
			t.Fatal(err)
		}
	}
	resetAtPathIndex(t, s)

	oldChunk, oldWorkers, oldHook := bookAtPathBackfillChunk, bookAtPathBackfillWorkers, bookAtPathBackfillAfterChunk
	defer func() {
		bookAtPathBackfillChunk, bookAtPathBackfillWorkers, bookAtPathBackfillAfterChunk = oldChunk, oldWorkers, oldHook
	}()
	bookAtPathBackfillChunk = 7
	bookAtPathBackfillWorkers = 4
	var hookCalls atomic.Int64
	bookAtPathBackfillAfterChunk = func(int) { hookCalls.Add(1) }

	res, err := s.BackfillBookAtPathIndex(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Scanned != n {
		t.Fatalf("scanned = %d, want %d", res.Scanned, n)
	}
	// Each chunk commit holds at most 7 keys, plus one sentinel commit.
	if minCommits := (n+6)/7 + 1; res.Commits < minCommits {
		t.Fatalf("commits = %d, want >= %d", res.Commits, minCommits)
	}
	if got := hookCalls.Load(); got != int64(res.Commits-1) {
		t.Fatalf("hook ran %d times, want one per chunk commit (%d)", got, res.Commits-1)
	}
	if built, err := s.bookAtPathIndexBuilt(); err != nil || !built {
		t.Fatalf("sentinel not set: %v %v", built, err)
	}
	checkAgainstOracle(t, s, paths)
}

func TestBackfill_UndecodableRowFailsWithoutSentinel(t *testing.T) {
	s := newAtPathStore(t)
	defer s.Close()
	resetAtPathIndex(t, s)
	if err := s.db.Set([]byte("book:01BADROW"), []byte("{not json"), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BackfillBookAtPathIndex(context.Background()); err == nil {
		t.Fatal("an undecodable row must fail the backfill")
	}
	if built, _ := s.bookAtPathIndexBuilt(); built {
		t.Fatal("sentinel set despite a failed run")
	}
}

func TestLiveBookIDsAtPath_ReaderFailsClosedAndSkipsExtras(t *testing.T) {
	s := newAtPathStore(t)
	defer s.Close()
	mustBackfill(t, s)
	b, err := s.CreateBook(&Book{Title: "x", FilePath: "/p"})
	if err != nil {
		t.Fatal(err)
	}

	// Extra for a hard-deleted book: skipped, not an error.
	if err := s.db.Set(bookAtPathKey("/p", "01GONE"), []byte{}, pebble.Sync); err != nil {
		t.Fatal(err)
	}
	ids, err := s.LiveBookIDsAtPath("/p")
	if err != nil || len(ids) != 1 || ids[0] != b.ID {
		t.Fatalf("with a gone extra: ids=%v err=%v", ids, err)
	}

	// Malformed key (empty id) in the path's range: error.
	if err := s.db.Set([]byte(bookAtPathPrefix+"/m\x00"), []byte{}, pebble.Sync); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LiveBookIDsAtPath("/m"); err == nil {
		t.Fatal("a malformed key must fail the lookup")
	}

	// Undecodable candidate row: error.
	if err := s.db.Set(bookAtPathKey("/u", "01UNDECODABLE"), []byte{}, pebble.Sync); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Set([]byte("book:01UNDECODABLE"), []byte("{"), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LiveBookIDsAtPath("/u"); err == nil {
		t.Fatal("an undecodable candidate row must fail the lookup")
	}
}

func TestLiveBookIDsAtPath_NulPathDoesNotLeakIntoPrefix(t *testing.T) {
	s := newAtPathStore(t)
	defer s.Close()
	mustBackfill(t, s)
	a, _ := s.CreateBook(&Book{Title: "a", FilePath: "/a"})
	n, _ := s.CreateBook(&Book{Title: "n", FilePath: "/a\x00b"})
	ids, err := s.LiveBookIDsAtPath("/a")
	if err != nil || len(ids) != 1 || ids[0] != a.ID {
		t.Fatalf("/a: %v %v", ids, err)
	}
	ids, err = s.LiveBookIDsAtPath("/a\x00b")
	if err != nil || len(ids) != 1 || ids[0] != n.ID {
		t.Fatalf("/a\\x00b: %v %v", ids, err)
	}
}

func TestVerifyBookAtPathIndex_ClassifiesEveryDefect(t *testing.T) {
	s := newAtPathStore(t)
	defer s.Close()
	mustBackfill(t, s)
	live, _ := s.CreateBook(&Book{Title: "l", FilePath: "/live"})
	tr := true
	trashed, _ := s.CreateBook(&Book{Title: "t", FilePath: "/trash", MarkedForDeletion: &tr})
	moved, _ := s.CreateBook(&Book{Title: "m", FilePath: "/now"})

	for _, k := range [][]byte{bookAtPathKey("/live", live.ID), bookAtPathKey("/trash", trashed.ID)} {
		if err := s.db.Delete(k, pebble.Sync); err != nil {
			t.Fatal(err)
		}
	}
	for _, k := range [][]byte{
		bookAtPathKey("/was", moved.ID),     // row moved
		bookAtPathKey("/gone", "01NOBOOK"),  // row gone
		[]byte(bookAtPathPrefix + "no-nul"), // malformed
	} {
		if err := s.db.Set(k, []byte{}, pebble.Sync); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := s.VerifyBookAtPathIndex(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := BookAtPathIndexReport{
		SentinelSet: true, BooksScanned: 3, IndexKeysScanned: 4,
		MissingLive: 1, MissingTrashed: 1, ExtraRowGone: 1, ExtraRowMoved: 1, Malformed: 1,
	}
	got := rep
	got.SampleMissingLive, got.SampleMissingTrashed, got.SampleExtra, got.SampleMalformed = nil, nil, nil, nil
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("report = %+v\nwant     %+v", got, want)
	}
	if len(rep.SampleMissingLive) != 1 || rep.SampleMissingLive[0] != live.ID+" /live" {
		t.Fatalf("missing-live sample = %v", rep.SampleMissingLive)
	}
}
