// file: internal/dedup/engine_acoustid_parallel_test.go
// version: 1.0.0
// guid: 9d3b6f1a-2c47-4e8b-9a0d-5f61c8e0a3b2
// last-edited: 2026-07-05

// Regression tests for CONC-3: AcoustIDScan's per-book loop is now sharded
// across a bounded worker pool (registry.RunItems) instead of running
// single-threaded. AcoustIDScan mutates FOUR shared maps + a counter inside
// emit()/the loop (booksByID, boilerplateBookCache, parentDirCache, emitted,
// identifierGateDrops) — this test proves the parallel pass emits the EXACT
// same candidate set as an independent ground truth (no lost/duplicated
// updates through the guarded state) and exercises every one of the four
// gates (boilerplate book, conflicting identifiers, same-parent-dir
// suppression, and a genuine match) concurrently under -race.
package dedup

import (
	"context"
	"fmt"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestParallelAcoustIDScan_SameCandidatesAsSerial builds a "hub" fixture: a
// single HUB book and many satellite books that all share one AcoustID
// segment fingerprint. The mock's exact-match lookup always resolves that
// shared segment to HUB's file, so every satellite book's scan should emit
// exactly one candidate pairing it with HUB (HUB's own lookup of its own
// segment resolves to itself and is skipped — never a self-pair).
//
// The fixture is sized well above runtime.NumCPU() so the outer RunItems
// shard genuinely parallelizes and many emit() calls contend on the
// mutex-guarded maps/counter. Three additional satellites each test one of
// the other three gates so all four guarded maps + the counter are exercised
// under concurrency:
//   - book-boilerplate: boilerplate title -> isBoilerplateBook excludes it
//     (boilerplateBookCache).
//   - book-samedir: files live in the same parent directory as HUB -> the
//     same-directory suppression excludes it (parentDirCache).
//   - book-conflict: conflicting ISBN13 vs HUB -> identifiersConflict drops it
//     (booksByID, identifierGateDrops), while still marking the pair emitted
//     (emitted) so it can never double-fire.
func TestParallelAcoustIDScan_SameCandidatesAsSerial(t *testing.T) {
	engine, mock, es := setupTestEngine(t)

	const numSatellites = 60
	hubISBN := "9780000000001"
	conflictISBN := "9780000000002"

	var books []database.Book
	filesByBook := make(map[string][]database.BookFile)

	books = append(books, database.Book{ID: "HUB", Title: "The Hub Book", ISBN13: &hubISBN})
	filesByBook["HUB"] = []database.BookFile{{
		ID: "F_HUB", BookID: "HUB", FilePath: "/lib/hub/book.mp3", AcoustIDSeg0: validFP80,
	}}

	books = append(books, database.Book{ID: "book-boilerplate", Title: "  THIS   IS\tAUDIBLE  "})
	filesByBook["book-boilerplate"] = []database.BookFile{{
		ID: "F_boilerplate", BookID: "book-boilerplate", FilePath: "/lib/dboilerplate/book.mp3", AcoustIDSeg0: validFP80,
	}}

	books = append(books, database.Book{ID: "book-samedir", Title: "Same Dir Book"})
	filesByBook["book-samedir"] = []database.BookFile{{
		ID: "F_samedir", BookID: "book-samedir", FilePath: "/lib/hub/other.mp3", AcoustIDSeg0: validFP80,
	}}

	books = append(books, database.Book{ID: "book-conflict", Title: "Conflicting ISBN Book", ISBN13: &conflictISBN})
	filesByBook["book-conflict"] = []database.BookFile{{
		ID: "F_conflict", BookID: "book-conflict", FilePath: "/lib/dconflict/book.mp3", AcoustIDSeg0: validFP80,
	}}

	var wantMatches []string // satellite book IDs expected to pair with HUB
	for i := 0; i < numSatellites; i++ {
		id := fmt.Sprintf("book-%02d", i)
		books = append(books, database.Book{ID: id, Title: fmt.Sprintf("Real Book %02d", i)})
		filesByBook[id] = []database.BookFile{{
			ID: "F_" + id, BookID: id, FilePath: fmt.Sprintf("/lib/d%02d/book.mp3", i), AcoustIDSeg0: validFP80,
		}}
		wantMatches = append(wantMatches, id)
	}

	mock.GetAllBooksFunc = func(limit, offset int) ([]database.Book, error) {
		return books, nil
	}
	mock.GetBookFilesFunc = func(bookID string) ([]database.BookFile, error) {
		return filesByBook[bookID], nil
	}
	// The real store's exact-match index maps one fingerprint value to a
	// single file. Every book here shares validFP80, so wire the lookup to
	// always resolve to HUB's file — this deterministically creates a
	// "star" topology (every satellite matches HUB, never each other) that
	// is easy to verify independently of the scan under test.
	mock.GetBookFileByAcoustIDFunc = func(fp string) (*database.BookFile, error) {
		if fp != validFP80 {
			return nil, nil
		}
		hubFile := filesByBook["HUB"][0]
		return &hubFile, nil
	}

	// Ground truth computed independently of AcoustIDScan: exactly one
	// candidate per wantMatches entry, pairing it with HUB. The three special
	// satellites (boilerplate/samedir/conflict) must NOT appear.
	want := make(map[string]struct{}, len(wantMatches))
	for _, id := range wantMatches {
		want[pairKeyFor(id, "HUB")] = struct{}{}
	}

	type prog struct{ done, total int }
	var progs []prog

	if err := engine.AcoustIDScan(context.Background(), func(done, total int) {
		progs = append(progs, prog{done, total})
	}); err != nil {
		t.Fatalf("AcoustIDScan: %v", err)
	}

	cands, _, err := es.ListCandidates(database.CandidateFilter{
		EntityType: "book", Layer: "acoustid", Status: "pending", Limit: 1_000_000,
	})
	if err != nil {
		t.Fatalf("ListCandidates: %v", err)
	}
	got := make(map[string]struct{}, len(cands))
	for _, c := range cands {
		key := pairKeyFor(c.EntityAID, c.EntityBID)
		if _, dup := got[key]; dup {
			t.Fatalf("duplicate candidate emitted for pair %s", key)
		}
		got[key] = struct{}{}
	}

	if len(got) != len(want) {
		t.Fatalf("candidate count mismatch: got %d, want %d (got=%v)", len(got), len(want), got)
	}
	for key := range want {
		if _, ok := got[key]; !ok {
			t.Fatalf("missing expected candidate pair %s (lost update in parallel scan)", key)
		}
	}
	for key := range got {
		if _, ok := want[key]; !ok {
			t.Fatalf("unexpected candidate pair %s (spurious emit, e.g. a suppressed gate leaked through)", key)
		}
	}

	// Explicitly confirm the three gated satellites never produced a
	// candidate with HUB (redundant with the set comparison above, but
	// pins down which gate each one exercises for future readers).
	for _, gated := range []string{"book-boilerplate", "book-samedir", "book-conflict"} {
		if _, ok := got[pairKeyFor(gated, "HUB")]; ok {
			t.Fatalf("gated satellite %s unexpectedly produced a candidate with HUB", gated)
		}
	}

	if len(progs) == 0 {
		t.Fatal("progress callback never invoked")
	}
	total := len(books)
	last := 0
	for i, p := range progs {
		if p.total != total {
			t.Fatalf("progress[%d].total = %d, want %d", i, p.total, total)
		}
		if p.done < last {
			t.Fatalf("progress not monotonic: progress[%d].done=%d < previous %d", i, p.done, last)
		}
		last = p.done
	}
	if last != total {
		t.Fatalf("final progress done = %d, want %d", last, total)
	}
}
