// file: internal/plugins/maintenance/dedupe_book_file_rows_parallel_test.go
// version: 1.1.0
// guid: 7f21c6ad-95be-4c30-8d02-5b3a1e6f4c99
// last-edited: 2026-09-10

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// concurrentReporter is deliberately NOT the package's shared fakeReporter: that
// one appends to a plain slice, which would itself race once the op runs books in
// parallel — and a racing test harness reports a race that is not in the code
// under test. This one is mutex-guarded so `-race` failures mean something.
type concurrentReporter struct {
	mu       sync.Mutex
	progress int
	logs     []string
}

func (r *concurrentReporter) UpdateProgress(cur, _ int, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cur > r.progress {
		r.progress = cur
	}
	return nil
}

// Log used to discard. It records now because the op's ONLY report of how much
// work remains is the summary string it logs -- the idempotency assertion
// ("a dry run after a successful apply reports 0 pending") is unreachable
// otherwise. Guarded by the same mutex as progress: RunItems workers log.
func (r *concurrentReporter) Log(_ slog.Level, msg string, _ ...slog.Attr) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = append(r.logs, msg)
	return nil
}

// loggedText joins everything the op logged, for substring assertions.
func (r *concurrentReporter) loggedText() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.logs, "\n")
}
func (r *concurrentReporter) Logger() *slog.Logger   { return slog.Default() }
func (r *concurrentReporter) Checkpoint(_ any) error { return nil }
func (r *concurrentReporter) IsCanceled() bool       { return false }
func (r *concurrentReporter) RunPhase(ctx context.Context, _ string, fn func(context.Context, registry.Reporter) error) error {
	return fn(ctx, r)
}
func (r *concurrentReporter) Trigger(_ context.Context, _ string, _ any) error { return nil }
func (r *concurrentReporter) SetCurrentItem(_ string)                          {}

// seedDupBooks creates `books` books, each holding `copies` rows that all point at
// the SAME file path — the exact shape the op exists to collapse.
func seedDupBooks(t *testing.T, s *database.PebbleStore, books, copies int) []string {
	t.Helper()
	ids := make([]string, 0, books)
	for b := range books {
		bk, err := s.CreateBook(&database.Book{Title: fmt.Sprintf("Dup Book %02d", b)})
		if err != nil {
			t.Fatalf("CreateBook: %v", err)
		}
		path := fmt.Sprintf("/lib/dup-%02d/track.m4b", b)
		for range copies {
			f := &database.BookFile{
				BookID:   bk.ID,
				FilePath: path,
				Duration: 3600,
				FileSize: 58000000, // ~129 kbps at 3600s — a plausible real bitrate
			}
			if err := s.CreateBookFile(f); err != nil {
				t.Fatalf("CreateBookFile: %v", err)
			}
		}
		ids = append(ids, bk.ID)
	}
	return ids
}

// 🔴 THE CONCURRENCY REGRESSION. The book loop was sequential, which CLAUDE.md's
// concurrency rule forbids for a whole-library loop doing per-item DB work — and
// the first full production run proved the cost: ~1.7 min/book meant 176 books
// could not finish inside the op's own 2-hour timeout.
//
// Parallelising is only safe because the partition is disjoint: a book_file row
// belongs to exactly one book, so two workers can never touch the same row or the
// same RecomputeBookAggregates target. This exercises that for real — many books,
// each with many duplicate rows — so `go test -race` has an actual parallel path
// to inspect. The package's other tests are pure functions and prove nothing here.
func TestDedupeBookFileRows_ParallelApplyCollapsesEveryBook(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds a real PebbleStore; skipped in -short")
	}
	const books, copies = 24, 6

	s, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer s.Close()
	// Required: writes made before warmup publishes are dropped from memdb, and
	// PASS 1 of this op reads GetAllBookFilesCore (memdb). Without this the op
	// would see an empty library and "correctly" do nothing.
	s.WaitForWarmup()

	bookIDs := seedDupBooks(t, s, books, copies)

	p := &Plugin{deps: fakeDeps{store: s}}
	raw, _ := json.Marshal(DedupeBookFileRowsParams{Apply: true})
	rep := &concurrentReporter{}

	if err := p.runDedupeBookFileRows(context.Background(), raw, rep); err != nil {
		t.Fatalf("runDedupeBookFileRows: %v", err)
	}

	// Every book must be collapsed to exactly one row, and it must be the same
	// path — nothing invented, nothing cross-contaminated between workers.
	for i, id := range bookIDs {
		files, ferr := s.GetBookFiles(id)
		if ferr != nil {
			t.Fatalf("GetBookFiles(%s): %v", id, ferr)
		}
		if len(files) != 1 {
			t.Fatalf("book %d (%s): %d rows survived, want exactly 1", i, id, len(files))
		}
		wantPath := fmt.Sprintf("/lib/dup-%02d/track.m4b", i)
		if files[0].FilePath != wantPath {
			t.Fatalf("book %d: surviving row path = %q, want %q", i, files[0].FilePath, wantPath)
		}
		// The survivor must still carry its data — a parallel worker must not have
		// blanked it while collapsing a neighbouring book.
		if files[0].Duration != 3600 {
			t.Fatalf("book %d: surviving duration = %d, want 3600", i, files[0].Duration)
		}
	}

	if rep.progress != books {
		t.Fatalf("progress reported %d/%d books; RunItems must count every completion", rep.progress, books)
	}
}

// A dry run must mutate nothing, and that has to hold under concurrency too —
// a missing Apply check inside a worker would delete rows the operator never
// approved, which is the one failure this op must never have.
func TestDedupeBookFileRows_ParallelDryRunDeletesNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds a real PebbleStore; skipped in -short")
	}
	const books, copies = 12, 4

	s, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer s.Close()
	s.WaitForWarmup()

	bookIDs := seedDupBooks(t, s, books, copies)

	p := &Plugin{deps: fakeDeps{store: s}}
	raw, _ := json.Marshal(DedupeBookFileRowsParams{Apply: false})
	if err := p.runDedupeBookFileRows(context.Background(), raw, &concurrentReporter{}); err != nil {
		t.Fatalf("runDedupeBookFileRows (dry run): %v", err)
	}

	for i, id := range bookIDs {
		files, ferr := s.GetBookFiles(id)
		if ferr != nil {
			t.Fatalf("GetBookFiles: %v", ferr)
		}
		if len(files) != copies {
			t.Fatalf("book %d: dry run changed the row count to %d, want %d untouched",
				i, len(files), copies)
		}
	}
}

// dedupeOpIDReporter is a concurrentReporter that CAN name its operation.
//
// registry.ReporterOpID resolves through an optional `OpID() string`, and the
// package's fakes deliberately do not implement it, so journal rows written
// under a plain fake carry OperationID "". That is a supported degradation
// (GetBookChanges scans every opchange: key and filters by BookID, so replay by
// book still works), but it would leave the correlated path -- the one
// production uses, and the one GetOperationChanges reads -- untested.
//
// It is NOT the package's existing opIDReporter (scan_standdown_apply_test.go):
// that one embeds fakeReporter, whose Log appends to an unguarded slice. This op
// logs from inside RunItems workers, so reusing it would make `-race` report a
// race in the harness rather than in the code under test — the same reason
// concurrentReporter exists at all.
type dedupeOpIDReporter struct {
	*concurrentReporter
	opID string
}

func (r *dedupeOpIDReporter) OpID() string { return r.opID }

// journalFailStore is a PebbleStore whose undo-ledger write always fails.
//
// Embedding rather than reimplementing keeps every other one of the ~496 store
// methods real, so the op runs its true path right up to the journal write.
type journalFailStore struct {
	*database.PebbleStore
}

func (s *journalFailStore) CreateOperationChange(_ *database.OperationChange) error {
	return errors.New("journal is down")
}

// seedActiveOp puts an op row into the ACTIVE set the way the store actually
// maintains it.
//
// 🔴 DO NOT "SIMPLIFY" THIS TO A SINGLE InsertOperationV2 WITH Status:"running".
// InsertOperationV2 writes the opv2:act: index ONLY for a queued row
// (pebble_store_ops_v2.go), and ListActiveOperationsV2 iterates exactly that
// index -- so a row inserted directly as "running" is INVISIBLE to the guard,
// and a refusal test written that way passes only when the guard is broken.
// The running state is reached the way production reaches it: queued, then a
// status transition.
func seedActiveOp(t *testing.T, s *database.PebbleStore, id, defID, status string) {
	t.Helper()
	if err := s.InsertOperationV2(database.OperationV2Row{
		ID:       id,
		DefID:    defID,
		Plugin:   "library",
		Status:   "queued",
		QueuedAt: time.Now(),
	}); err != nil {
		t.Fatalf("InsertOperationV2(%s): %v", id, err)
	}
	if status != "queued" {
		now := time.Now()
		if err := s.UpdateOperationV2Status(id, status, &now, nil, nil); err != nil {
			t.Fatalf("UpdateOperationV2Status(%s, %s): %v", id, status, err)
		}
	}
	// Prove the round-trip rather than assuming it: if the fixture does not
	// actually appear in the active list, every guard assertion below is vacuous.
	active, err := s.ListActiveOperationsV2()
	if err != nil {
		t.Fatalf("ListActiveOperationsV2: %v", err)
	}
	for _, row := range active {
		if row.ID == id && row.Status == status && row.DefID == defID {
			return
		}
	}
	t.Fatalf("fixture op %s (%s/%s) is not in ListActiveOperationsV2 (%d rows) — the guard tests would be vacuous",
		id, defID, status, len(active))
}

// newDupStore builds a warmed PebbleStore holding `books` books of `copies`
// identical rows each.
func newDupStore(t *testing.T, books, copies int) (*database.PebbleStore, []string) {
	t.Helper()
	s, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	s.WaitForWarmup()
	return s, seedDupBooks(t, s, books, copies)
}

// assertRowCounts fails unless every seeded book still holds exactly `want` rows.
func assertRowCounts(t *testing.T, s *database.PebbleStore, bookIDs []string, want int) {
	t.Helper()
	for i, id := range bookIDs {
		files, ferr := s.GetBookFiles(id)
		if ferr != nil {
			t.Fatalf("GetBookFiles(%s): %v", id, ferr)
		}
		if len(files) != want {
			t.Fatalf("book %d (%s): %d rows, want %d", i, id, len(files), want)
		}
	}
}

// 🔴 THE UNDO-LEDGER ASSERTION. Every row this op deletes must leave a replayable
// record behind BEFORE it goes: an OperationChange whose OldValue is the ENTIRE
// row as JSON. Without it a deletion is unrecoverable by any means the codebase
// has, which is what made this op unrunnable on production data.
func TestDedupeBookFileRows_ApplyJournalsEveryDeletedRow(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds a real PebbleStore; skipped in -short")
	}
	const books, copies = 3, 4

	s, bookIDs := newDupStore(t, books, copies)

	// Capture what exists before the delete, so the journal can be checked
	// against the real rows rather than against itself.
	pre := map[string][]database.BookFile{}
	for _, id := range bookIDs {
		files, ferr := s.GetBookFiles(id)
		if ferr != nil {
			t.Fatalf("GetBookFiles: %v", ferr)
		}
		pre[id] = files
	}

	p := &Plugin{deps: fakeDeps{store: s}}
	raw, _ := json.Marshal(DedupeBookFileRowsParams{Apply: true})
	rep := &dedupeOpIDReporter{concurrentReporter: &concurrentReporter{}, opID: "op-dedupe-journal"}

	if err := p.runDedupeBookFileRows(context.Background(), raw, rep); err != nil {
		t.Fatalf("runDedupeBookFileRows: %v", err)
	}

	for i, id := range bookIDs {
		changes, cerr := s.GetBookChanges(id)
		if cerr != nil {
			t.Fatalf("GetBookChanges(%s): %v", id, cerr)
		}
		var deletes []*database.OperationChange
		for _, c := range changes {
			if c.ChangeType == "book_file_delete" {
				deletes = append(deletes, c)
			}
		}
		if len(deletes) != copies-1 {
			t.Fatalf("book %d (%s): %d book_file_delete journal rows, want %d",
				i, id, len(deletes), copies-1)
		}

		survivors, ferr := s.GetBookFiles(id)
		if ferr != nil {
			t.Fatalf("GetBookFiles: %v", ferr)
		}
		if len(survivors) != 1 {
			t.Fatalf("book %d: %d rows survived, want 1", i, len(survivors))
		}
		wantPath := fmt.Sprintf("/lib/dup-%02d/track.m4b", i)

		seen := map[string]bool{}
		for _, c := range deletes {
			if c.OperationID != "op-dedupe-journal" {
				t.Fatalf("book %d: journal row %s carries OperationID %q, want %q",
					i, c.ID, c.OperationID, "op-dedupe-journal")
			}
			if c.BookID != id {
				t.Fatalf("journal row %s carries BookID %q, want %q", c.ID, c.BookID, id)
			}
			if c.NewValue != "" {
				t.Fatalf("journal row %s carries NewValue %q; a delete has no new value", c.ID, c.NewValue)
			}
			var row database.BookFile
			if uerr := json.Unmarshal([]byte(c.OldValue), &row); uerr != nil {
				t.Fatalf("journal row %s: OldValue does not unmarshal into a BookFile: %v", c.ID, uerr)
			}
			// The ledger has to carry enough to REPLAY the row back, not just to
			// name it: id, path and the aggregate-bearing fields.
			if row.FilePath != wantPath {
				t.Fatalf("journal row %s: FilePath %q, want %q", c.ID, row.FilePath, wantPath)
			}
			if row.Duration != 3600 || row.FileSize != 58000000 {
				t.Fatalf("journal row %s: Duration/FileSize = %d/%d, want 3600/58000000",
					c.ID, row.Duration, row.FileSize)
			}
			if c.FieldName != row.ID {
				t.Fatalf("journal row %s: FieldName %q must name the deleted row id %q",
					c.ID, c.FieldName, row.ID)
			}
			if row.ID == survivors[0].ID {
				t.Fatalf("book %d: journal names the SURVIVING row %s as deleted", i, row.ID)
			}
			known := false
			for _, f := range pre[id] {
				if f.ID == row.ID {
					known = true
					break
				}
			}
			if !known {
				t.Fatalf("book %d: journal names row %s, which was never seeded", i, row.ID)
			}
			if seen[row.ID] {
				t.Fatalf("book %d: row %s journaled twice", i, row.ID)
			}
			seen[row.ID] = true
		}
	}

	// The correlated read path production would use for a replay.
	opChanges, oerr := s.GetOperationChanges("op-dedupe-journal")
	if oerr != nil {
		t.Fatalf("GetOperationChanges: %v", oerr)
	}
	if len(opChanges) != books*(copies-1) {
		t.Fatalf("GetOperationChanges returned %d rows, want %d", len(opChanges), books*(copies-1))
	}
}

// 🔴 THE DATA-LOSS ASSERTION. If the ledger cannot be written, the rows must
// STAY. An unjournaled deletion is unrecoverable; a duplicate row that survives
// costs one more run of an idempotent op.
func TestDedupeBookFileRows_JournalFailureLeavesRowsIntact(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds a real PebbleStore; skipped in -short")
	}
	const books, copies = 6, 3

	s, bookIDs := newDupStore(t, books, copies)

	p := &Plugin{deps: fakeDeps{store: &journalFailStore{PebbleStore: s}}}
	raw, _ := json.Marshal(DedupeBookFileRowsParams{Apply: true})

	// It DEGRADES, it does not abort: one unwritable ledger row must not turn
	// into an op-level failure that hides the other books' (also skipped) state.
	rep := &concurrentReporter{}
	if err := p.runDedupeBookFileRows(context.Background(), raw, rep); err != nil {
		t.Fatalf("runDedupeBookFileRows must degrade, not abort: %v", err)
	}

	assertRowCounts(t, s, bookIDs, copies)

	// 🔑 THE POSITIVE SIGNAL. "All rows survived" is also what an op that found
	// nothing to do would produce, so surviving rows alone cannot distinguish
	// "the journal refused and the delete was skipped" from "the sweep never got
	// there". The summary must show the deletes were ATTEMPTED and counted as
	// failures — one per book — and that nothing was deleted.
	summary := rep.loggedText()
	if !strings.Contains(summary, fmt.Sprintf("failed %d", books)) {
		t.Fatalf("summary must count one failure per book (failed %d); logged:\n%s", books, summary)
	}
	if !strings.Contains(summary, "deleted 0 ") {
		t.Fatalf("summary must report 0 deletions; logged:\n%s", summary)
	}
}

// The in-run refusal, running case. A scan rewrites the very rows this op
// deletes, so an apply during one can resurrect a collapsed row or mutate a
// keeper mid-flight.
func TestDedupeBookFileRows_ApplyRefusesWhileLibraryScanRunning(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds a real PebbleStore; skipped in -short")
	}
	const books, copies = 4, 3

	s, bookIDs := newDupStore(t, books, copies)
	seedActiveOp(t, s, "scan-1", "library.scan", "running")

	p := &Plugin{deps: fakeDeps{store: s}}
	raw, _ := json.Marshal(DedupeBookFileRowsParams{Apply: true})

	err := p.runDedupeBookFileRows(context.Background(), raw, &concurrentReporter{})
	if err == nil {
		t.Fatal("apply proceeded while library.scan was running; it must refuse")
	}
	if !strings.Contains(err.Error(), "library.scan") || !strings.Contains(err.Error(), "refusing to apply") {
		t.Fatalf("refusal message %q must name library.scan and say it is refusing to apply", err.Error())
	}
	assertRowCounts(t, s, bookIDs, copies)
}

// The in-run refusal, QUEUED case — the one dispatcher Gate 4 structurally
// cannot see, because it only defers dispatch while a named def is RUNNING.
func TestDedupeBookFileRows_ApplyRefusesWhileLibraryScanQueued(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds a real PebbleStore; skipped in -short")
	}
	const books, copies = 4, 3

	s, bookIDs := newDupStore(t, books, copies)
	seedActiveOp(t, s, "scan-q", "library.scan", "queued")

	p := &Plugin{deps: fakeDeps{store: s}}
	raw, _ := json.Marshal(DedupeBookFileRowsParams{Apply: true})

	err := p.runDedupeBookFileRows(context.Background(), raw, &concurrentReporter{})
	if err == nil {
		t.Fatal("apply proceeded while library.scan was queued; it must refuse")
	}
	if !strings.Contains(err.Error(), "library.scan") || !strings.Contains(err.Error(), "refusing to apply") {
		t.Fatalf("refusal message %q must name library.scan and say it is refusing to apply", err.Error())
	}
	assertRowCounts(t, s, bookIDs, copies)
}

// ANTI-OVER-SUPPRESSION. A guard that refused on ANY active op — or simply on
// every apply — would pass both refusal tests above. This is the known-good
// input that must still go through.
func TestDedupeBookFileRows_ApplyProceedsWhenOnlyAnUnrelatedOpIsActive(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds a real PebbleStore; skipped in -short")
	}
	const books, copies = 4, 3

	s, bookIDs := newDupStore(t, books, copies)
	seedActiveOp(t, s, "title-1", "maintenance.title-repair", "running")

	p := &Plugin{deps: fakeDeps{store: s}}
	raw, _ := json.Marshal(DedupeBookFileRowsParams{Apply: true})

	if err := p.runDedupeBookFileRows(context.Background(), raw, &concurrentReporter{}); err != nil {
		t.Fatalf("an unrelated active op must not block the apply: %v", err)
	}
	assertRowCounts(t, s, bookIDs, 1)
}

// The guard is APPLY-ONLY. A dry run is read-only, and blocking it would remove
// the operator's only way to take a census while a long scan runs.
func TestDedupeBookFileRows_DryRunIsAllowedDuringALibraryScan(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds a real PebbleStore; skipped in -short")
	}
	const books, copies = 4, 3

	s, bookIDs := newDupStore(t, books, copies)
	seedActiveOp(t, s, "scan-2", "library.scan", "running")

	p := &Plugin{deps: fakeDeps{store: s}}
	raw, _ := json.Marshal(DedupeBookFileRowsParams{Apply: false})

	if err := p.runDedupeBookFileRows(context.Background(), raw, &concurrentReporter{}); err != nil {
		t.Fatalf("dry run must be allowed during a library.scan: %v", err)
	}
	assertRowCounts(t, s, bookIDs, copies)
}

// IDEMPOTENCY. `git revert` does not restore data, so the op's own re-runnability
// is part of the rollback story: after a successful apply, the same dry run must
// report nothing left to do.
func TestDedupeBookFileRows_DryRunAfterApplyReportsZeroPending(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds a real PebbleStore; skipped in -short")
	}
	const books, copies = 4, 3

	s, bookIDs := newDupStore(t, books, copies)

	p := &Plugin{deps: fakeDeps{store: s}}
	applyRaw, _ := json.Marshal(DedupeBookFileRowsParams{Apply: true})
	if err := p.runDedupeBookFileRows(context.Background(), applyRaw, &concurrentReporter{}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	assertRowCounts(t, s, bookIDs, 1)

	dryRaw, _ := json.Marshal(DedupeBookFileRowsParams{Apply: false})
	rep := &concurrentReporter{}
	if err := p.runDedupeBookFileRows(context.Background(), dryRaw, rep); err != nil {
		t.Fatalf("dry run after apply: %v", err)
	}
	if !strings.Contains(rep.loggedText(), "would delete 0") {
		t.Fatalf("dry run after a successful apply must report nothing pending; logged:\n%s", rep.loggedText())
	}
}
