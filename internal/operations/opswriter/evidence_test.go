// file: internal/operations/opswriter/evidence_test.go
// version: 2.3.0
// guid: 4af29ea6-4101-42cd-b6c6-1e86908383ef
// last-edited: 2026-10-14

package opswriter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// changeTypeUndo is metafetch.ChangeTypeApplyUndo, the change type of the
// rows undo-last-apply, a single-field undo and an operation revert record
// (opswriter cannot import metafetch). RowApplied does not read it: the
// tests use it so the undo rows they plant look like the real ones.
const changeTypeUndo = "undo"

// evidenceStore adds the apply markers and the reads RowApplied makes to
// recStore, with injectable failures and read counters.
type evidenceStore struct {
	*recStore
	marks          map[string]database.RowMarkV3 // op + "\x00" + row
	putErr         error
	markErr        error
	changeErr      error
	bookChangesErr error
	historyErr     error
	undecodable    map[string]int // book -> history rows that "did not decode"
	markReads      atomic.Int32
	changeReads    atomic.Int32
	// The ChangeScanner half: whether the per-book index is "usable", the
	// scans and per-book reads made, and a hook run on each scanned row.
	indexUsable atomic.Bool
	scans       atomic.Int32
	bookReads   atomic.Int32
	scanErr     error
	onScanRow   func()
}

func (s *evidenceStore) OpChangeByBookIndexUsable() bool { return s.indexUsable.Load() }

func (s *evidenceStore) ScanOperationChanges(fn func(*database.OperationChange) error) error {
	s.scans.Add(1)
	if s.scanErr != nil {
		return s.scanErr
	}
	s.mu.Lock()
	rows := slices.Clone(s.changes)
	s.mu.Unlock()
	for i := range rows {
		if s.onScanRow != nil {
			s.onScanRow()
		}
		if err := fn(&rows[i]); err != nil {
			return err
		}
	}
	return nil
}

func newEvidenceStore() *evidenceStore {
	s := &evidenceStore{recStore: newRecStore(), marks: map[string]database.RowMarkV3{}}
	for _, id := range []string{"book-001", "book-002", "book-003", "book-004", "book-shared"} {
		s.books[id] = &database.Book{ID: id, Title: "t " + id}
	}
	s.files["file-001"] = &database.BookFile{ID: "file-001", BookID: "book-001", FilePath: "/lib/001.m4b", Missing: true}
	s.files["file-002"] = &database.BookFile{ID: "file-002", BookID: "book-002", FilePath: "/lib/002.m4b"}
	return s
}

func (s *evidenceStore) PutRowMarkV3(id string, m database.RowMarkV3) error {
	if s.putErr != nil {
		return s.putErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.marks[id+"\x00"+m.RowID] = m
	return nil
}

func (s *evidenceStore) GetRowMarkV3(id, rowID string) (*database.RowMarkV3, error) {
	s.markReads.Add(1)
	if s.markErr != nil {
		return nil, s.markErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.marks[id+"\x00"+rowID]
	if !ok {
		return nil, nil
	}
	return &m, nil
}

func (s *evidenceStore) mark(op, rowID string) (database.RowMarkV3, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.marks[op+"\x00"+rowID]
	return m, ok
}

func (s *evidenceStore) GetOperationChange(operationID, changeID string) (*database.OperationChange, error) {
	s.changeReads.Add(1)
	if s.changeErr != nil {
		return nil, s.changeErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.changes {
		if c.OperationID == operationID && c.ID == changeID {
			return &c, nil
		}
	}
	return nil, nil
}

func (s *evidenceStore) GetBookChanges(bookID string) ([]*database.OperationChange, error) {
	s.bookReads.Add(1)
	if s.bookChangesErr != nil {
		return nil, s.bookChangesErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*database.OperationChange
	for i := range s.changes {
		if s.changes[i].BookID == bookID {
			c := s.changes[i]
			out = append(out, &c)
		}
	}
	return out, nil
}

func (s *evidenceStore) ReadBookChangeHistory(bookID string) ([]database.MetadataChangeRecord, int, error) {
	if s.historyErr != nil {
		return nil, 0, s.historyErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []database.MetadataChangeRecord
	for _, r := range s.history {
		if r.BookID == bookID {
			out = append(out, r)
		}
	}
	return out, s.undecodable[bookID], nil
}

// editChange edits the stored journal row id in place.
func (s *evidenceStore) editChange(id string, fn func(*database.OperationChange)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.changes {
		if s.changes[i].ID == id {
			fn(&s.changes[i])
			return
		}
	}
	panic("no journal row " + id)
}

// dropChange deletes the stored journal row id.
func (s *evidenceStore) dropChange(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.changes = slices.DeleteFunc(s.changes, func(c database.OperationChange) bool { return c.ID == id })
}

// evidenceWriter builds a Writer for op over s the way the v3 registry does:
// batch prefix "op-<op>-", markers and both reads wired.
func evidenceWriter(s *evidenceStore, op string, mode ops.Mode) *Writer {
	return New(Config{OpID: op, Mode: mode, Intents: s, Books: s, Files: s, Authors: s, Journal: s,
		History:  NewBookHistory(s, "test.def", "ops_v3", "op-"+op+"-", nil),
		RowMarks: s, Changes: s, HistoryReader: s})
}

// applyItem runs apply as one item would -- a fresh item context, the
// Writer's writes made with it -- and then, when apply returned nil, marks
// the row the way the Fixer's applyRow does.
func applyItem(t *testing.T, w *Writer, rowID string, apply func(ctx context.Context) error) {
	t.Helper()
	irc := ops.NewItemRC(ops.NewRC(ops.RunEnv{Context: context.Background()}), context.Background(), &ops.Tally{}, nil)
	if err := apply(irc.Context()); err != nil {
		return
	}
	w.MarkRowApplied(irc.Context(), rowID)
}

func setTitle(w *Writer, bookID, title string) func(context.Context) error {
	return func(ctx context.Context) error {
		_, err := w.ModifyBook(ctx, bookID, func(b *database.Book) error { b.Title = title; return nil })
		return err
	}
}

func mustRowApplied(t *testing.T, w *Writer, rowID string, want bool) {
	t.Helper()
	got, err := w.RowApplied(context.Background(), rowID)
	if err != nil || got != want {
		t.Fatalf("RowApplied(%s) = %v, %v; want %v", rowID, got, err, want)
	}
}

// TestRowAppliedIsPerRow (05-PR5b B1): a row counts as applied only by its
// OWN marker, recorded when its own Apply returned nil -- whatever the
// row's writes were (a book field, a book_file field, a repoint, credits,
// an Effect, or nothing at all) and whichever attempt recorded it. The run's
// history for a book is no evidence for another row of the same book: not a
// sibling row's metadata batch (the shared-subject case), not a sibling
// row's book_file or repoint journal row (whose BookID names the book). Nor
// is a marker of another op, a row whose Apply failed, a Preview Writer or
// one with no marker store.
func TestRowAppliedIsPerRow(t *testing.T) {
	s := newEvidenceStore()
	first := evidenceWriter(s, "op1", ops.Live)
	applyItem(t, first, "title-shared", setTitle(first, "book-shared", "new shared"))
	// "publisher-shared" is never written by the run; a foreign edit sets
	// the book's publisher between the attempts. The run's batch on
	// book-shared belongs to "title-shared".
	pub := "Foreign Pub"
	s.books["book-shared"].Publisher = &pub
	applyItem(t, first, "file-001", func(ctx context.Context) error {
		_, err := first.ModifyBookFile(ctx, "book-001", "file-001", func(f *database.BookFile) error { f.Format = "m4b"; return nil })
		return err
	})
	applyItem(t, first, "repoint-002", func(ctx context.Context) error {
		return first.RepointBookFile(ctx, "book-002", "file-002", database.BookFileLocationOf(s.files["file-002"]),
			database.BookFileLocation{Path: "/lib/002b.m4b"})
	})
	applyItem(t, first, "credits-003", func(ctx context.Context) error {
		_, err := first.ModifyBookAuthors(ctx, "book-003", func([]database.BookAuthor) ([]database.BookAuthor, error) {
			return []database.BookAuthor{{BookID: "book-003", AuthorID: 7}}, nil
		})
		return err
	})
	// An Effect row's own name ("touched") is unknown to the check, so any
	// later write on its book defeats it (it fails closed): it gets a book
	// no other row writes.
	applyItem(t, first, "effect-005", func(ctx context.Context) error {
		return first.Effect(ctx, ops.EffectSpec{Name: "touch", Subject: ops.Subject{Type: ops.SubjectBook, ID: "book-005"},
			Resource: ops.ResBooks}, func() ([]ops.HistoryRow, error) {
			return []ops.HistoryRow{{BookID: "book-005", Field: "touched", Old: "0", New: "1"}}, nil
		})
	})
	applyItem(t, first, "noop-004", setTitle(first, "book-004", "t book-004"))
	applyItem(t, first, "failed-004", func(ctx context.Context) error {
		if err := setTitle(first, "book-004", "half")(ctx); err != nil {
			return err
		}
		return errors.New("second write failed")
	})
	other := evidenceWriter(s, "op2", ops.Live)
	applyItem(t, other, "other-op", setTitle(other, "book-003", "op2"))

	// Each marker names exactly its own row's history.
	if m, ok := s.mark("op1", "title-shared"); !ok || len(m.BookBatches) != 1 || m.BookBatches[0].BookID != "book-shared" ||
		!strings.HasPrefix(m.BookBatches[0].BatchID, "op-op1-") || len(m.Changes) != 0 {
		t.Fatalf("title-shared marker = %+v, %v; want its one book-shared batch", m, ok)
	}
	if m, ok := s.mark("op1", "repoint-002"); !ok || len(m.Changes) != 1 || len(m.BookBatches) != 0 {
		t.Fatalf("repoint-002 marker = %+v, %v; want its one journal row", m, ok)
	}
	if m, ok := s.mark("op1", "noop-004"); !ok || len(m.Changes)+len(m.BookBatches) != 0 {
		t.Fatalf("noop-004 marker = %+v, %v; want a marker naming nothing", m, ok)
	}

	resumed := evidenceWriter(s, "op1", ops.Live)
	for _, c := range []struct {
		row  string
		want bool
	}{
		{"title-shared", true},
		{"publisher-shared", false}, // the shared-subject foreign edit
		{"file-001", true},
		{"title-001", false}, // book-001 has file-001's journal row, not this row's marker
		{"repoint-002", true},
		{"title-002", false}, // book-002 has repoint-002's journal row
		{"credits-003", true},
		{"effect-005", true},
		{"noop-004", true},
		{"failed-004", false},
		{"other-op", false}, // op2's marker
	} {
		mustRowApplied(t, resumed, c.row, c.want)
	}
	// The attempt that wrote a marker reads it too: identity, not timing.
	mustRowApplied(t, first, "title-shared", true)

	// Preview records nothing and finds nothing.
	preview := evidenceWriter(s, "op1", ops.Preview)
	applyItem(t, preview, "preview-row", func(context.Context) error { return nil })
	if _, ok := s.mark("op1", "preview-row"); ok {
		t.Fatal("a Preview Writer recorded a marker")
	}
	mustRowApplied(t, preview, "title-shared", false)
	// No marker store: nothing recorded, nothing found, no error.
	bare := New(Config{OpID: "op1", Mode: ops.Live, Intents: s, Books: s, Journal: s,
		History: NewBookHistory(s, "test.def", "ops_v3", "op-op1-", nil), Changes: s, HistoryReader: s})
	applyItem(t, bare, "bare-row", func(context.Context) error { return nil })
	if _, ok := s.mark("op1", "bare-row"); ok {
		t.Fatal("a Writer without a marker store recorded a marker")
	}
	mustRowApplied(t, bare, "title-shared", false)
}

// TestRowAppliedUndoneHistory (05-PR5b S1): a marker proves its row applied
// only while the history it names stands. Each way that history is taken
// back makes it no evidence: the batch undone by undo-last-apply (undo rows
// in the batch), a field of it undone on its own (an undo row with no batch
// id, at or after the batch, on a field the batch wrote), its credits row
// voided (Voided alone), its repoint row reverted (even with its book write
// standing), its journal row gone, its batch's rows gone. An undo of
// another field, or one older than the batch, leaves it standing; so does
// an undo of another row's batch on another book, and a batch behind 150
// older rows of its book is still found. A marker that names history the
// Writer cannot read is no evidence.
func TestRowAppliedUndoneHistory(t *testing.T) {
	s := newEvidenceStore()
	w := evidenceWriter(s, "op1", ops.Live)
	applyItem(t, w, "batch-undone", setTitle(w, "book-001", "a"))
	applyItem(t, w, "field-undone", setTitle(w, "book-002", "b"))
	applyItem(t, w, "other-field-undone", setTitle(w, "book-003", "c"))
	applyItem(t, w, "credits-voided", func(ctx context.Context) error {
		_, err := w.ModifyBookAuthors(ctx, "book-004", func([]database.BookAuthor) ([]database.BookAuthor, error) {
			return []database.BookAuthor{{BookID: "book-004", AuthorID: 9}}, nil
		})
		return err
	})
	applyItem(t, w, "repoint-reverted", func(ctx context.Context) error {
		if err := setTitle(w, "book-004", "d")(ctx); err != nil {
			return err
		}
		return w.RepointBookFile(ctx, "book-002", "file-002", database.BookFileLocationOf(s.files["file-002"]),
			database.BookFileLocation{Path: "/lib/002b.m4b"})
	})
	applyItem(t, w, "journal-gone", func(ctx context.Context) error {
		_, err := w.ModifyBookFile(ctx, "book-001", "file-001", func(f *database.BookFile) error { f.Format = "m4b"; return nil })
		return err
	})
	applyItem(t, w, "standing", setTitle(w, "book-shared", "s"))
	// A book with 150 older rows ahead of the batch: a capped read (this
	// fake caps in insertion order, the store by key) would miss it.
	s.books["book-long"] = &database.Book{ID: "book-long", Title: "long"}
	for range 150 {
		s.history = append(s.history, database.MetadataChangeRecord{BookID: "book-long", Field: "publisher",
			BatchID: "manual", ChangedAt: time.Now().Add(-time.Hour)})
	}
	applyItem(t, w, "long-history", setTitle(w, "book-long", "l"))
	for _, row := range []string{"batch-undone", "field-undone", "other-field-undone", "credits-voided",
		"repoint-reverted", "journal-gone", "standing", "long-history"} {
		mustRowApplied(t, w, row, true)
	}

	m, _ := s.mark("op1", "batch-undone")
	later := time.Now().Add(time.Second)
	s.history = append(s.history,
		database.MetadataChangeRecord{BookID: "book-001", Field: "title", ChangeType: changeTypeUndo,
			BatchID: m.BookBatches[0].BatchID, ChangedAt: later},
		database.MetadataChangeRecord{BookID: "book-002", Field: "title", ChangeType: changeTypeUndo, ChangedAt: later},
		database.MetadataChangeRecord{BookID: "book-003", Field: "publisher", ChangeType: changeTypeUndo, ChangedAt: later},
		database.MetadataChangeRecord{BookID: "book-003", Field: "title", ChangeType: changeTypeUndo,
			ChangedAt: time.Now().Add(-time.Hour)})
	reverted := time.Now()
	credits, _ := s.mark("op1", "credits-voided")
	s.editChange(credits.Changes[0], func(c *database.OperationChange) { c.Voided = true })
	repoint, _ := s.mark("op1", "repoint-reverted")
	s.editChange(repoint.Changes[0], func(c *database.OperationChange) { c.RevertedAt = &reverted })
	gone, _ := s.mark("op1", "journal-gone")
	s.dropChange(gone.Changes[0])

	for _, c := range []struct {
		row  string
		want bool
	}{
		{"batch-undone", false},
		{"field-undone", false},
		{"other-field-undone", true},
		{"credits-voided", false},
		{"repoint-reverted", false},
		{"journal-gone", false},
		{"standing", true},
		{"long-history", true},
	} {
		mustRowApplied(t, w, c.row, c.want)
	}

	// The same markers through a Writer that cannot read what they name.
	// A later write to a batch's field can be in either store, so a Writer
	// missing either read cannot show anything on a book stands.
	noChanges := New(Config{OpID: "op1", Mode: ops.Live, RowMarks: s, HistoryReader: s})
	mustRowApplied(t, noChanges, "standing", false) // a batch only
	applyItem(t, w, "credits-standing", func(ctx context.Context) error {
		_, err := w.ModifyBookAuthors(ctx, "book-001", func([]database.BookAuthor) ([]database.BookAuthor, error) {
			return []database.BookAuthor{{BookID: "book-001", AuthorID: 3}}, nil
		})
		return err
	})
	mustRowApplied(t, w, "credits-standing", true)
	mustRowApplied(t, noChanges, "credits-standing", false)
	noHistory := New(Config{OpID: "op1", Mode: ops.Live, RowMarks: s, Changes: s})
	mustRowApplied(t, noHistory, "standing", false)
	mustRowApplied(t, noHistory, "credits-standing", false)
	// A batch whose rows are gone from the book's history cannot be shown
	// to stand.
	s.history = slices.DeleteFunc(s.history, func(r database.MetadataChangeRecord) bool { return r.BookID == "book-shared" })
	mustRowApplied(t, w, "standing", false)
}

// A failed read is the caller's error, never "not applied" or "applied". A
// fenced run reads nothing, and says "not read". A marker is written like
// a write: never once the fence is revoked or the item's context is
// canceled, and a store failure is logged, not returned.
func TestRowMarkerReadAndWriteFailures(t *testing.T) {
	s := newEvidenceStore()
	w := evidenceWriter(s, "op1", ops.Live)
	applyItem(t, w, "r1", func(ctx context.Context) error {
		if err := setTitle(w, "book-001", "x")(ctx); err != nil {
			return err
		}
		_, err := w.ModifyBookAuthors(ctx, "book-001", func([]database.BookAuthor) ([]database.BookAuthor, error) {
			return []database.BookAuthor{{BookID: "book-001", AuthorID: 1}}, nil
		})
		return err
	})
	down := errors.New("store down")
	for _, c := range []struct {
		name string
		set  func(error)
	}{
		{"marker", func(e error) { s.markErr = e }},
		{"journal row", func(e error) { s.changeErr = e }},
		{"history", func(e error) { s.historyErr = e }},
	} {
		c.set(down)
		if ok, err := w.RowApplied(context.Background(), "r1"); ok || !errors.Is(err, down) {
			t.Fatalf("%s read failure: RowApplied = %v, %v", c.name, ok, err)
		}
		c.set(nil)
	}
	mustRowApplied(t, w, "r1", true)

	f, revoke := NewFence(1, nil)
	fenced := evidenceWriter(s, "op1", ops.Live)
	fenced.SetFence(f.Check)
	revoke()
	before := s.markReads.Load()
	ok, err := fenced.RowApplied(context.Background(), "r1")
	if ok || !errors.Is(err, ops.ErrFenced) || !strings.Contains(err.Error(), "not read") {
		t.Fatalf("fenced RowApplied = %v, %v; want ErrFenced, \"not read\"", ok, err)
	}
	if s.markReads.Load() != before {
		t.Fatal("a fenced RowApplied read the store")
	}
	fenced.MarkRowApplied(context.Background(), "fenced-row")
	if _, ok := s.mark("op1", "fenced-row"); ok {
		t.Fatal("a fenced Writer recorded a marker")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	w.MarkRowApplied(canceled, "canceled-row")
	if _, ok := s.mark("op1", "canceled-row"); ok {
		t.Fatal("a canceled context recorded a marker")
	}
	s.putErr = down
	w.MarkRowApplied(context.Background(), "put-fails") // logged; must not panic or block
	s.putErr = nil
	if _, ok := s.mark("op1", "put-fails"); ok {
		t.Fatal("a failed marker write left a marker")
	}
}

// batchRow returns the history row the marker of rowID's only batch wrote
// on field.
func batchRow(t *testing.T, s *evidenceStore, rowID, field string) database.MetadataChangeRecord {
	t.Helper()
	m, ok := s.mark("op1", rowID)
	if !ok || len(m.BookBatches) != 1 {
		t.Fatalf("%s marker = %+v, %v; want one batch", rowID, m, ok)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.history {
		if r.BatchID == m.BookBatches[0].BatchID && r.Field == field {
			return r
		}
	}
	t.Fatalf("no %s row in %s's batch", field, rowID)
	return database.MetadataChangeRecord{}
}

// TestRowAppliedBatchMustBeNewest (05-PR5b re-review S2): a batch stands
// only while it is the newest history on every field it wrote, whatever
// the change type of the later row. A manual edit that is then set back to
// the batch's value is still a later write, so the row is not claimed; so
// is a later row another writer recorded under the field's history name
// (author_name for author_id), and a foreign row stamped at the very same
// instant. Older rows on the field, later rows on another field, and the
// undo markers of a later journaled batch on the same book (field "apply")
// leave it standing, and so does a journaled batch's own marker.
func TestRowAppliedBatchMustBeNewest(t *testing.T) {
	s := newEvidenceStore()
	w := evidenceWriter(s, "op1", ops.Live)
	applyItem(t, w, "set-back", setTitle(w, "book-001", "mine"))
	applyItem(t, w, "alias", func(ctx context.Context) error {
		_, err := w.ModifyBook(ctx, "book-002", func(b *database.Book) error { id := 7; b.AuthorID = &id; return nil })
		return err
	})
	applyItem(t, w, "tie", setTitle(w, "book-003", "tied"))
	applyItem(t, w, "older-and-other", setTitle(w, "book-004", "kept"))
	journaled := New(Config{OpID: "op1", Mode: ops.Live, Intents: s, Books: s, Journal: s,
		History:       NewBookHistory(s, "test.def", "ops_v3", "op-op1-", nil),
		JournaledBook: func(string) bool { return true }, RowMarks: s, Changes: s, HistoryReader: s})
	applyItem(t, journaled, "journaled", setTitle(journaled, "book-shared", "j"))
	for _, row := range []string{"set-back", "alias", "tie", "older-and-other", "journaled"} {
		mustRowApplied(t, w, row, true)
	}

	setBack := batchRow(t, s, "set-back", "title")
	tie := batchRow(t, s, "tie", "title")
	mine, theirs := `"mine"`, `"theirs"`
	s.history = append(s.history,
		database.MetadataChangeRecord{BookID: "book-001", Field: "title", NewValue: &theirs,
			ChangeType: database.ChangeTypeManual, ChangedAt: setBack.ChangedAt.Add(time.Second)},
		database.MetadataChangeRecord{BookID: "book-001", Field: "title", NewValue: &mine,
			ChangeType: database.ChangeTypeManual, ChangedAt: setBack.ChangedAt.Add(2 * time.Second)},
		database.MetadataChangeRecord{BookID: "book-002", Field: database.HistoryFieldAuthor,
			ChangeType: "fetch", BatchID: "fetch-1", ChangedAt: time.Now().Add(time.Second)},
		database.MetadataChangeRecord{BookID: "book-003", Field: "title", ChangeType: "fetch",
			BatchID: "fetch-2", ChangedAt: tie.ChangedAt},
		database.MetadataChangeRecord{BookID: "book-004", Field: "title", ChangeType: database.ChangeTypeManual,
			ChangedAt: time.Now().Add(-time.Hour)},
		database.MetadataChangeRecord{BookID: "book-004", Field: "publisher", ChangeType: database.ChangeTypeManual,
			ChangedAt: time.Now().Add(time.Hour)})
	// A later journaled batch of another row on the same book, on another
	// field: its apply_op_journaled marker is newer than "journaled"'s.
	applyItem(t, journaled, "journaled-later", func(ctx context.Context) error {
		_, err := journaled.ModifyBook(ctx, "book-shared", func(b *database.Book) error {
			p := "later pub"
			b.Publisher = &p
			return nil
		})
		return err
	})

	for _, c := range []struct {
		row  string
		want bool
	}{
		{"set-back", false},
		{"alias", false},
		{"tie", false},
		{"older-and-other", true},
		{"journaled", true},
		{"journaled-later", true},
	} {
		mustRowApplied(t, w, c.row, c.want)
	}
}

// TestRowAppliedUndoIgnoresTheClock (05-PR5b re-review S3): an undo is
// seen whatever its timestamp says. undo-last-apply's row in the batch
// itself and an operation revert's row under this run's op id, each stamped
// a millisecond BEFORE the batch (a clock that stepped back), still make
// the batch stop standing. The same rows under another op's id, or a
// single-field undo with no batch id, stamped before the batch, are older
// history and leave it standing.
func TestRowAppliedUndoIgnoresTheClock(t *testing.T) {
	s := newEvidenceStore()
	w := evidenceWriter(s, "op1", ops.Live)
	applyItem(t, w, "undo-last-apply", setTitle(w, "book-001", "a"))
	applyItem(t, w, "op-revert", setTitle(w, "book-002", "b"))
	applyItem(t, w, "other-op-revert", setTitle(w, "book-003", "c"))
	applyItem(t, w, "older-field-undo", setTitle(w, "book-004", "d"))
	undone := batchRow(t, s, "undo-last-apply", "title")
	reverted := batchRow(t, s, "op-revert", "title")
	other := batchRow(t, s, "other-op-revert", "title")
	field := batchRow(t, s, "older-field-undo", "title")
	skew := -time.Millisecond
	s.history = append(s.history,
		database.MetadataChangeRecord{BookID: "book-001", Field: "title", ChangeType: changeTypeUndo,
			Source: "undo-last-apply", BatchID: undone.BatchID, ChangedAt: undone.ChangedAt.Add(skew)},
		database.MetadataChangeRecord{BookID: "book-002", Field: "title", ChangeType: changeTypeUndo,
			Source: "operation_revert", BatchID: "op1", ChangedAt: reverted.ChangedAt.Add(skew)},
		database.MetadataChangeRecord{BookID: "book-003", Field: "title", ChangeType: changeTypeUndo,
			Source: "operation_revert", BatchID: "op2", ChangedAt: other.ChangedAt.Add(skew)},
		database.MetadataChangeRecord{BookID: "book-004", Field: "title", ChangeType: changeTypeUndo,
			Source: "manual", ChangedAt: field.ChangedAt.Add(skew)})
	for _, c := range []struct {
		row  string
		want bool
	}{
		{"undo-last-apply", false},
		{"op-revert", false},
		{"other-op-revert", true},
		{"older-field-undo", true},
	} {
		mustRowApplied(t, w, c.row, c.want)
	}
}

// TestRowAppliedJournalRowMustBeNewest (05-PR5b re-review S2, journal
// side): a journal row stands only while no journal row of its book, of any
// operation, was recorded at or after it on what it wrote. A book_file
// field is overwritten by a later row on that field, by a later write to
// the file as a whole (book_file:<id>, as a repoint, move or reassign is
// journaled) and by the organizer's book-level file_path row; a repoint by
// a later write to any field of its file; credits by later credits. A later
// row on another file, or on another field of the same file, leaves it
// standing, as does an Effect row with no book id. A foreign row stamped at
// the same instant counts as later. A failed journal read is the caller's
// error.
func TestRowAppliedJournalRowMustBeNewest(t *testing.T) {
	s := newEvidenceStore()
	s.files["file-003"] = &database.BookFile{ID: "file-003", BookID: "book-001", FilePath: "/lib/003.m4b"}
	s.files["file-004"] = &database.BookFile{ID: "file-004", BookID: "book-003", FilePath: "/lib/004.m4b"}
	s.files["file-005"] = &database.BookFile{ID: "file-005", BookID: "book-004", FilePath: "/lib/005.m4b"}
	w := evidenceWriter(s, "op1", ops.Live)
	setFormat := func(bookID, fileID string) func(context.Context) error {
		return func(ctx context.Context) error {
			_, err := w.ModifyBookFile(ctx, bookID, fileID, func(f *database.BookFile) error { f.Format = "m4b"; return nil })
			return err
		}
	}
	applyItem(t, w, "same-field", setFormat("book-001", "file-001"))
	applyItem(t, w, "whole-file", setFormat("book-001", "file-003"))
	applyItem(t, w, "organizer", setFormat("book-003", "file-004"))
	applyItem(t, w, "repoint", func(ctx context.Context) error {
		return w.RepointBookFile(ctx, "book-002", "file-002", database.BookFileLocationOf(s.files["file-002"]),
			database.BookFileLocation{Path: "/lib/002b.m4b"})
	})
	applyItem(t, w, "credits", func(ctx context.Context) error {
		_, err := w.ModifyBookAuthors(ctx, "book-shared", func([]database.BookAuthor) ([]database.BookAuthor, error) {
			return []database.BookAuthor{{BookID: "book-shared", AuthorID: 4}}, nil
		})
		return err
	})
	applyItem(t, w, "untouched", setFormat("book-004", "file-005"))
	s.files["file-006"] = &database.BookFile{ID: "file-006", BookID: "book-002", FilePath: "/lib/006.m4b"}
	applyItem(t, w, "tie", setFormat("book-002", "file-006"))
	applyItem(t, w, "bookless-effect", func(ctx context.Context) error {
		return w.Effect(ctx, ops.EffectSpec{Name: "touch", Subject: ops.Subject{Type: ops.SubjectBook, ID: "x"},
			Resource: ops.ResBooks}, func() ([]ops.HistoryRow, error) {
			return []ops.HistoryRow{{Field: "touched", Old: "0", New: "1"}}, nil
		})
	})
	rows := []string{"same-field", "whole-file", "organizer", "repoint", "credits", "untouched", "tie", "bookless-effect"}
	for _, row := range rows {
		mustRowApplied(t, w, row, true)
	}
	tie, _ := s.mark("op1", "tie")
	var tieAt time.Time
	s.mu.Lock()
	for _, c := range s.changes {
		if c.ID == tie.Changes[0] {
			tieAt = c.CreatedAt
		}
	}
	s.mu.Unlock()

	later := time.Now().Add(time.Second)
	foreign := func(bookID, field string) database.OperationChange {
		return database.OperationChange{ID: "f-" + bookID + "-" + field, OperationID: "op2", BookID: bookID,
			ChangeType: "foreign", FieldName: field, CreatedAt: later}
	}
	s.mu.Lock()
	s.changes = append(s.changes,
		foreign("book-001", "file.format:file-001"),
		foreign("book-001", "book_file:file-003"),
		foreign("book-003", "file_path"),
		foreign("book-002", "file.size:file-002"),
		foreign("book-shared", "authors"),
		foreign("book-004", "file.format:file-999"), // another file
		foreign("book-004", "file.size:file-005"),   // another field of the same file
		foreign("", "touched"),
		database.OperationChange{ID: "f-tie", OperationID: "op2", BookID: "book-002", ChangeType: "foreign",
			FieldName: "file.format:file-006", CreatedAt: tieAt}) // the same instant
	s.mu.Unlock()
	for _, c := range []struct {
		row  string
		want bool
	}{
		{"same-field", false},
		{"whole-file", false},
		{"organizer", false},
		{"repoint", false},
		{"credits", false},
		{"untouched", true},
		{"tie", false},
		{"bookless-effect", true},
	} {
		mustRowApplied(t, w, c.row, c.want)
	}

	down := errors.New("journal index down")
	s.bookChangesErr = down
	if ok, err := w.RowApplied(context.Background(), "untouched"); ok || !errors.Is(err, down) {
		t.Fatalf("book journal read failure: RowApplied = %v, %v", ok, err)
	}
}

// TestSubjectOverlap pins which names, from either store, can write the
// same thing (subjectOf, overlap).
func TestSubjectOverlap(t *testing.T) {
	files := map[string]bool{"f1": true}
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"file.format:f1", "file.format:f1", true},
		{"file.format:f1", "file.size:f1", false},
		{"file.format:f1", "file.format:f2", false},
		{"file.format:f1", "book_file:f1", true},
		{"book_file:f1", "file.size:f1", true},
		{"book_file:f1", "book_file:f2", false},
		{"file.track_number:f1", "book_file:f1:track_number", true}, // a manual file edit in metadata history
		{"file.format:f1", "book_file:f1:track_number", false},
		{"file.format:f1", "file_path", true},
		{"file_path", "book_file:f2", true},
		{"file.track_number:f1", "track_number", true}, // a manual track edit with no file id
		{"book_file:f2", "disc_number", true},
		{"file.format:f1", "f1", true}, // a repoint journaled under the bare file id
		{"file.format:f2", "f9", true}, // an unknown bare name: anything
		{"authors", "authors", true},
		{"authors", "book_authors", true},
		{"authors", "author_id", false},
		{"author_id", "author", true},
		{"author_id", "author_name", true},
		{"series_id", "series", true},
		{"series_id", "series_name", true},
		{"series_id", "series_object", true},
		{"series_sequence", "series_position", true},
		{"title", "title", true},
		{"title", "publisher", false},
		{"title", "file.format:f1", false},
		{"authors", "file_path", false},
		{"touched", "title", true}, // an Effect's own name is unknown: it fails closed
		{"touched", "file.format:f1", true},
	} {
		a, b := subjectOf(c.a, files), subjectOf(c.b, files)
		if got := overlap(a, b); got != c.want {
			t.Errorf("overlap(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
		if got := overlap(b, a); got != c.want {
			t.Errorf("overlap(%q, %q) = %v, want %v", c.b, c.a, got, c.want)
		}
	}
}

// TestNoMarkerForIncompleteHistory (05-PR5b re-review N1): when a write's
// history row cannot be recorded -- a journal row, a metadata history row
// of the batch, or every row of a Writer with no journal -- the item is
// noted incomplete and MarkRowApplied records no marker, so no marker can
// name part of what the row wrote. The next item, whose history records,
// is marked as usual.
func TestNoMarkerForIncompleteHistory(t *testing.T) {
	s := newEvidenceStore()
	w := evidenceWriter(s, "op1", ops.Live)
	s.failJournal = true
	applyItem(t, w, "journal-fails", func(ctx context.Context) error {
		_, err := w.ModifyBookFile(ctx, "book-001", "file-001", func(f *database.BookFile) error { f.Format = "m4b"; return nil })
		return err
	})
	s.failJournal = false
	s.failHistory = true
	applyItem(t, w, "history-fails", setTitle(w, "book-002", "x"))
	s.failHistory = false
	noJournal := New(Config{OpID: "op1", Mode: ops.Live, Intents: s, Books: s, Authors: s,
		History: NewBookHistory(s, "test.def", "ops_v3", "op-op1-", nil), RowMarks: s, Changes: s, HistoryReader: s})
	applyItem(t, noJournal, "no-journal", func(ctx context.Context) error {
		_, err := noJournal.ModifyBookAuthors(ctx, "book-003", func([]database.BookAuthor) ([]database.BookAuthor, error) {
			return []database.BookAuthor{{BookID: "book-003", AuthorID: 2}}, nil
		})
		return err
	})
	// The Writer refuses a journaled write with no journal before it writes
	// (ErrNotJournaled), so journalRows' own no-journal branch is only
	// reachable directly; it notes the item incomplete all the same.
	applyItem(t, noJournal, "no-journal-rows", func(ctx context.Context) error {
		if noJournal.journalRows(ctx, "x", []ops.HistoryRow{{BookID: "book-003", Field: "f"}}) {
			t.Error("journalRows with no journal reported its rows recorded")
		}
		return nil
	})
	applyItem(t, w, "recorded", setTitle(w, "book-004", "y"))
	for _, row := range []string{"journal-fails", "history-fails", "no-journal", "no-journal-rows"} {
		if m, ok := s.mark("op1", row); ok {
			t.Errorf("%s: a marker was recorded with part of its history missing: %+v", row, m)
		}
	}
	if _, ok := s.mark("op1", "recorded"); !ok {
		t.Fatal("a row whose history recorded got no marker")
	}
}

// TestRowAppliedUndecodableHistory (05-PR5b re-review N3): when any
// metadata history row of a batch's book did not decode, the unreadable row
// could be the one that overwrote or undid the batch, so the row is not
// claimed (and no error: it is skipped as changed since plan). Another
// book's unreadable row does not matter.
func TestRowAppliedUndecodableHistory(t *testing.T) {
	s := newEvidenceStore()
	w := evidenceWriter(s, "op1", ops.Live)
	applyItem(t, w, "bad-book", setTitle(w, "book-001", "a"))
	applyItem(t, w, "good-book", setTitle(w, "book-002", "b"))
	s.undecodable = map[string]int{"book-001": 1}
	mustRowApplied(t, w, "bad-book", false)
	mustRowApplied(t, w, "good-book", true)
}

// TestRowAppliedUndecodableJournal (05-PR5b re-review, N3's journal
// side): a named journal row that does not decode, or a book journal read
// that fails on an undecodable row, is not counted as standing -- the row
// is skipped as changed since plan, not failed. Any other read failure is
// still the caller's error (TestRowMarkerReadAndWriteFailures).
func TestRowAppliedUndecodableJournal(t *testing.T) {
	s := newEvidenceStore()
	w := evidenceWriter(s, "op1", ops.Live)
	applyItem(t, w, "credits", func(ctx context.Context) error {
		_, err := w.ModifyBookAuthors(ctx, "book-001", func([]database.BookAuthor) ([]database.BookAuthor, error) {
			return []database.BookAuthor{{BookID: "book-001", AuthorID: 5}}, nil
		})
		return err
	})
	mustRowApplied(t, w, "credits", true)
	s.bookChangesErr = fmt.Errorf("opchange op9:c9: %w", json.Unmarshal([]byte("{not json"), &struct{}{}))
	mustRowApplied(t, w, "credits", false)
	s.bookChangesErr = nil
	var row database.OperationChange
	s.changeErr = json.Unmarshal([]byte(`{"id": 7}`), &row) // a real *json.UnmarshalTypeError
	mustRowApplied(t, w, "credits", false)
	s.changeErr = nil
	mustRowApplied(t, w, "credits", true)
}

// foreignJournal appends a journal row of another op, stamped later.
func (s *evidenceStore) foreignJournal(id, bookID, field string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.changes = append(s.changes, database.OperationChange{ID: id, OperationID: "op-foreign", BookID: bookID,
		ChangeType: "foreign", FieldName: field, CreatedAt: time.Now().Add(time.Second)})
}

// foreignHistory appends a metadata history row, stamped later.
func (s *evidenceStore) foreignHistory(bookID, field string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := `"x"`
	s.history = append(s.history, database.MetadataChangeRecord{BookID: bookID, Field: field, NewValue: &v,
		ChangeType: database.ChangeTypeManual, Source: "user_edit", ChangedAt: time.Now().Add(time.Second)})
}

// TestRowAppliedSeesLaterWritesInEitherStore (05-PR5b round 3 B1): a later
// write defeats a row's marker whichever store recorded it and whatever it
// is named there. Each shape a real writer records is planted after the
// run's write, on another book each:
//
//  1. a book field written only to the journal (fs-regroup's "title");
//  2. a repoint journaled with FieldName the bare file id;
//  3. a manual track edit recorded in METADATA history as
//     book_file:<id>:track_number, and one recorded as bare track_number;
//  4. a credit-list rewrite journaled as book_authors;
//  5. a series override recorded under its lock key series_name (and, as
//     a control, under series);
//  6. a later journal row under a name the check does not know, which
//     fails closed.
//
// A later write to another field, in either store, leaves the row standing.
func TestRowAppliedSeesLaterWritesInEitherStore(t *testing.T) {
	s := newEvidenceStore()
	s.books["book-005"] = &database.Book{ID: "book-005", Title: "t book-005"}
	s.books["book-006"] = &database.Book{ID: "book-006", Title: "t book-006"}
	s.files["file-003"] = &database.BookFile{ID: "file-003", BookID: "book-003", FilePath: "/lib/003.m4b"}
	s.files["file-004"] = &database.BookFile{ID: "file-004", BookID: "book-004", FilePath: "/lib/004.m4b"}
	w := evidenceWriter(s, "op1", ops.Live)
	setTrack := func(bookID, fileID string) func(context.Context) error {
		return func(ctx context.Context) error {
			_, err := w.ModifyBookFile(ctx, bookID, fileID, func(f *database.BookFile) error { f.TrackNumber = 7; return nil })
			return err
		}
	}
	sid := 9
	applyItem(t, w, "journal-title", setTitle(w, "book-001", "mine"))
	applyItem(t, w, "bare-file-id", func(ctx context.Context) error {
		return w.RepointBookFile(ctx, "book-002", "file-002", database.BookFileLocationOf(s.files["file-002"]),
			database.BookFileLocation{Path: "/lib/002b.m4b"})
	})
	applyItem(t, w, "history-file-track", setTrack("book-003", "file-003"))
	applyItem(t, w, "history-bare-track", setTrack("book-004", "file-004"))
	applyItem(t, w, "book-authors", func(ctx context.Context) error {
		_, err := w.ModifyBookAuthors(ctx, "book-shared", func([]database.BookAuthor) ([]database.BookAuthor, error) {
			return []database.BookAuthor{{BookID: "book-shared", AuthorID: 4}}, nil
		})
		return err
	})
	applyItem(t, w, "series-name", func(ctx context.Context) error {
		_, err := w.ModifyBook(ctx, "book-005", func(b *database.Book) error { b.SeriesID = &sid; return nil })
		return err
	})
	applyItem(t, w, "unknown-name", setTitle(w, "book-006", "mine"))
	rows := []string{"journal-title", "bare-file-id", "history-file-track", "history-bare-track", "book-authors",
		"series-name", "unknown-name"}
	for _, r := range rows {
		mustRowApplied(t, w, r, true)
	}

	// Later writes to other fields, in both stores: still standing.
	s.foreignJournal("o1", "book-001", "publisher")
	s.foreignHistory("book-001", "publisher")
	s.foreignJournal("o2", "book-002", "title")
	s.foreignHistory("book-003", "book_file:file-003:disc_count")
	s.foreignJournal("o3", "book-shared", "author_id")
	s.foreignHistory("book-005", "title")
	for _, r := range rows {
		mustRowApplied(t, w, r, true)
	}

	s.foreignJournal("f1", "book-001", "title")
	s.foreignJournal("f2", "book-002", "file-002")
	s.foreignHistory("book-003", "book_file:file-003:track_number")
	s.foreignHistory("book-004", "track_number")
	s.foreignJournal("f4", "book-shared", "book_authors")
	s.foreignHistory("book-005", "series_name")
	s.foreignJournal("f6", "book-006", "orphan_series")
	for _, r := range rows {
		mustRowApplied(t, w, r, false)
	}
	// The control: the same series edit recorded under "series".
	s.mu.Lock()
	s.history[len(s.history)-1].Field = "series"
	s.mu.Unlock()
	mustRowApplied(t, w, "series-name", false)
}

// TestRowAppliedScansTheJournalOncePerRun (05-PR5b round 3 S1): while the
// per-book index is not usable, RowApplied reads the journal with ONE
// whole-journal scan per Writer, however many rows and books it checks;
// once the index is usable it reads each book with GetBookChanges instead.
// A scan the context stops is an error and is not kept (the next call scans
// again); a scan that stops on an undecodable row is kept as "not standing"
// without a rescan. A Writer without a ChangeScan reads per book.
func TestRowAppliedScansTheJournalOncePerRun(t *testing.T) {
	s := newEvidenceStore()
	first := evidenceWriter(s, "op1", ops.Live)
	ids := []string{"book-001", "book-002", "book-003", "book-004"}
	for _, id := range ids {
		applyItem(t, first, "row-"+id, setTitle(first, id, "n "+id))
	}
	resumed := func() *Writer {
		return New(Config{OpID: "op1", Mode: ops.Live, RowMarks: s, Changes: s, HistoryReader: s, ChangeScan: s})
	}
	w := resumed()
	for range 3 {
		for _, id := range ids {
			mustRowApplied(t, w, "row-"+id, true)
		}
	}
	if n := s.scans.Load(); n != 1 {
		t.Fatalf("%d journal scans for 12 checks; want 1", n)
	}
	if n := s.bookReads.Load(); n != 0 {
		t.Fatalf("%d per-book journal reads while the index is not usable; want 0", n)
	}
	s.indexUsable.Store(true)
	mustRowApplied(t, w, "row-book-001", true)
	if s.scans.Load() != 1 || s.bookReads.Load() != 1 {
		t.Fatalf("index usable: %d scans, %d book reads; want 1, 1", s.scans.Load(), s.bookReads.Load())
	}
	s.indexUsable.Store(false)

	// A canceled scan: an error, not kept.
	s.changes = append(s.changes, make([]database.OperationChange, journalScanCtxEvery)...)
	ctx, cancel := context.WithCancel(context.Background())
	s.onScanRow = cancel
	stopped := resumed()
	if ok, err := stopped.RowApplied(ctx, "row-book-001"); ok || err == nil {
		t.Fatalf("canceled scan: RowApplied = %v, %v; want an error", ok, err)
	}
	s.onScanRow = nil
	before := s.scans.Load()
	mustRowApplied(t, stopped, "row-book-001", true)
	if s.scans.Load() != before+1 {
		t.Fatal("a scan the context stopped was kept")
	}

	// An undecodable row: not standing, kept.
	s.scanErr = fmt.Errorf("opchange op9:c9: %w", json.Unmarshal([]byte("{not json"), &struct{}{}))
	bad := resumed()
	mustRowApplied(t, bad, "row-book-001", false)
	mustRowApplied(t, bad, "row-book-002", false)
	if s.scans.Load() != before+2 {
		t.Fatalf("an undecodable scan was not kept: %d scans", s.scans.Load()-before)
	}
	s.scanErr = nil

	// No ChangeScan: per book.
	reads := s.bookReads.Load()
	mustRowApplied(t, evidenceWriter(s, "op1", ops.Live), "row-book-001", true)
	if s.bookReads.Load() != reads+1 {
		t.Fatal("a Writer without ChangeScan did not read the book's journal")
	}
}
