// file: internal/repairs/standdown_beats_test.go
// version: 1.2.0
// guid: 9d3f6b21-7a4e-4c58-b1d0-2e8f5c7a3b96
// last-edited: 2026-10-01

package repairs

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// memFiles is a book_file store: rows by id, each on one book.
type memFiles struct {
	mu         sync.Mutex
	rows       map[string]*database.BookFile
	recomputes int
}

func (m *memFiles) GetBookFileByID(bookID, fileID string) (*database.BookFile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rows[fileID]
	if !ok || r.BookID != bookID {
		return nil, nil
	}
	c := *r
	return &c, nil
}

func (m *memFiles) ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rows[fileID]
	if !ok || r.BookID != bookID {
		return nil, nil
	}
	c := *r
	if err := fn(&c); err != nil {
		return nil, err
	}
	*r = c
	return &c, nil
}

// GetBookFileByPath names the row holding a path's single-owner key (the
// first row naming it here; the tests never put two rows on one path).
func (m *memFiles) GetBookFileByPath(filePath string) (*database.BookFile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.rows {
		if r.FilePath == filePath {
			c := *r
			return &c, nil
		}
	}
	return nil, nil
}

func (m *memFiles) MoveBookFilesToBook(fileIDs []string, source, target string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range fileIDs {
		r, ok := m.rows[id]
		if !ok || r.BookID != source {
			return fmt.Errorf("book_file %s is not on %s", id, source)
		}
		r.BookID = target
	}
	return nil
}

func (m *memFiles) RecomputeBookAggregates(string) error {
	m.mu.Lock()
	m.recomputes++
	m.mu.Unlock()
	return nil
}

// fileGroupFixer consolidates fragments onto a survivor with ONLY book_file
// writes and aggregate recomputes, never a Modify: per fragment it moves the
// file (MoveBookFiles: journal beats), renumbers its track (SetTrackNumber:
// Step, journal beats) and recomputes the survivor (Recompute: its own
// beat), the shape of a fragment group row. between runs before each write
// so the test can move the lease clock.
type fileGroupFixer struct {
	survivor string
	frags    []string
	between  func()
}

func (f *fileGroupFixer) ID() string          { return "file-group" }
func (f *fileGroupFixer) Title() string       { return "File group" }
func (f *fileGroupFixer) Description() string { return "test fixer" }

func (f *fileGroupFixer) row() Row {
	return Row{RowID: "g", BookIDs: append([]string{f.survivor}, f.frags...), Title: "g", Reason: "fragments",
		Risk: RiskLow, Fingerprint: "fp:g"}
}

func (f *fileGroupFixer) Plan(context.Context, json.RawMessage, registry.Reporter) ([]Row, error) {
	return []Row{f.row()}, nil
}

func (f *fileGroupFixer) Replan(context.Context, json.RawMessage, Row, registry.Reporter) (Row, error) {
	return f.row(), nil
}

func (f *fileGroupFixer) Apply(_ context.Context, w *Writer, _ Row) error {
	steps := 0
	partial := func(err error) error {
		if steps == 0 {
			return err
		}
		return fmt.Errorf("%w: after %d step(s): %w", ErrPartiallyApplied, steps, err)
	}
	for i, frag := range f.frags {
		f.between()
		if err := w.MoveBookFiles([]string{"bf-" + frag}, frag, f.survivor); err != nil {
			return partial(err)
		}
		steps++
		f.between()
		if err := w.SetTrackNumber(f.survivor, "bf-"+frag, 0, i+1); err != nil {
			return partial(err)
		}
		steps++
		f.between()
		if err := w.Recompute(f.survivor); err != nil {
			return partial(err)
		}
		steps++
	}
	return nil
}

// A row whose writes are only journal-gated book_file steps and recomputes
// (no Modify at all) keeps the lease across 4-minute gaps between writes,
// 12 writes over ~48 minutes against a 5-minute lease. Each beat matters:
//   - without the Journal beat (mutation M5) nothing renews from the
//     Recompute of one fragment to the Recompute of the next (12 minutes);
//   - without the Recompute beat (mutation M6) nothing renews from a
//     SetTrackNumber to the next MoveBookFiles (8 minutes).
//
// Either way the lease lapses and the row is aborted.
func TestRunApply_FileStepsAndRecomputeRenewTheLease(t *testing.T) {
	s := newMemStore()
	f := &fileGroupFixer{survivor: "sv", frags: []string{"f1", "f2", "f3", "f4"}}
	files := &memFiles{rows: map[string]*database.BookFile{}}
	s.add("sv", "Survivor", "/lib/A/sv/x.m4b", nil)
	for _, id := range f.frags {
		s.add(id, id, "/lib/A/"+id+"/x.m4b", nil)
		files.rows["bf-"+id] = &database.BookFile{ID: "bf-" + id, BookID: id, FilePath: "/lib/A/" + id + "/x.m4b"}
	}
	sd := newTTLStandDown(5 * time.Minute)
	f.between = func() { sd.advance(4 * time.Minute) }
	plan := planFor(t, s, f)
	journal := &creditFake{credits: map[string][]database.BookAuthor{}}
	d := deps(s, sd)
	d.Concurrency = 1
	d.Writer = NewWriter(s, s, "file-group", "bulk_update", "rp-").WithJournal(files, journal, "op-apply")
	res, err := RunApply(context.Background(), f, plan, "op-plan", []string{"g"}, false, d, nopReporter{})
	require.NoError(t, err, "%+v", res)
	require.Empty(t, res.Aborted)
	require.Equal(t, 1, res.Applied, "%+v", res.Rows)
	require.Zero(t, sd.refused)
	for _, id := range f.frags {
		require.Equal(t, "sv", files.rows["bf-"+id].BookID)
	}
	require.Equal(t, 4, files.recomputes)
	require.Equal(t, 8, res.JournalRows, "one reassign and one track row per fragment")
}

// ---- LockWaiting ----

// countingLease renews until fails beats have succeeded (fails < 0: never
// fails) and counts every call.
type countingLease struct {
	mu    sync.Mutex
	calls int
	ok    int
}

func (l *countingLease) renew() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	return l.ok < 0 || l.calls <= l.ok
}

func (l *countingLease) n() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls
}

// While an outside holder keeps the lock, the waiter renews the lease on its
// interval, and once more after acquiring.
func TestWriter_LockWaitingRenewsWhileWaiting(t *testing.T) {
	var mu sync.Mutex
	mu.Lock()
	w := NewWriter(newMemStore(), newMemStore(), "t", "bulk_update", "rp-")
	lease := &countingLease{ok: -1}
	w.setLease(lease.renew)
	w.lockRenewEvery = 5 * time.Millisecond
	done := make(chan error, 1)
	go func() { done <- w.LockWaiting(context.Background(), "test lock", mu.Lock, mu.Unlock) }()
	require.Eventually(t, func() bool { return lease.n() >= 5 }, 5*time.Second, time.Millisecond,
		"renewals while the lock is held elsewhere")
	before := lease.n()
	mu.Unlock()
	require.NoError(t, <-done)
	require.Greater(t, lease.n(), before, "renewed after acquiring")
	require.False(t, mu.TryLock(), "the waiter holds the lock")
	mu.Unlock()
}

// A renewal that fails while waiting returns ErrStandDownLost without the
// lock, and the lock is released once the queued acquire gets it.
func TestWriter_LockWaitingRefusesALostLease(t *testing.T) {
	var mu sync.Mutex
	mu.Lock()
	w := NewWriter(newMemStore(), newMemStore(), "t", "bulk_update", "rp-")
	lease := &countingLease{ok: 3}
	w.setLease(lease.renew)
	w.lockRenewEvery = 5 * time.Millisecond
	err := w.LockWaiting(context.Background(), "test lock", mu.Lock, mu.Unlock)
	require.ErrorIs(t, err, ErrStandDownLost)
	require.Equal(t, 4, lease.n(), "renewed until the first refusal, then stopped")
	mu.Unlock()
	require.Eventually(t, func() bool {
		if mu.TryLock() {
			mu.Unlock()
			return true
		}
		return false
	}, 5*time.Second, time.Millisecond, "the abandoned acquire hands the lock back")
	_, err = w.Modify("x", func(*database.Book) error { return nil })
	require.ErrorIs(t, err, ErrStandDownLost, "the loss is latched for later writes")
}

// A lease already lost is refused before waiting; ctx ending stops the wait.
func TestWriter_LockWaitingStopsOnLostLeaseOrContext(t *testing.T) {
	var mu sync.Mutex
	w := NewWriter(newMemStore(), newMemStore(), "t", "bulk_update", "rp-")
	w.setLease(func() bool { return false })
	require.ErrorIs(t, w.LockWaiting(context.Background(), "test lock", mu.Lock, mu.Unlock), ErrStandDownLost)
	require.True(t, mu.TryLock(), "never acquired")
	// mu stays held: the next waiter blocks until ctx ends.
	w2 := NewWriter(newMemStore(), newMemStore(), "t", "bulk_update", "rp-")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, w2.LockWaiting(ctx, "test lock", mu.Lock, mu.Unlock), context.DeadlineExceeded)
	mu.Unlock()
}
