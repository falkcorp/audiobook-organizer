// file: internal/operations/opswriter/evidence_test.go
// version: 2.0.0
// guid: 4af29ea6-4101-42cd-b6c6-1e86908383ef
// last-edited: 2026-10-14

package opswriter

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// evidenceStore adds the apply markers and the two reads RowApplied makes to
// recStore, with injectable failures and read counters.
type evidenceStore struct {
	*recStore
	marks       map[string]database.RowMarkV3 // op + "\x00" + row
	putErr      error
	markErr     error
	changeErr   error
	historyErr  error
	markReads   atomic.Int32
	changeReads atomic.Int32
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

func (s *evidenceStore) GetBookChangeHistory(bookID string, limit int) ([]database.MetadataChangeRecord, error) {
	if s.historyErr != nil {
		return nil, s.historyErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []database.MetadataChangeRecord
	for _, r := range s.history {
		if r.BookID == bookID && len(out) < limit {
			out = append(out, r)
		}
	}
	return out, nil
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
	applyItem(t, first, "effect-004", func(ctx context.Context) error {
		return first.Effect(ctx, ops.EffectSpec{Name: "touch", Subject: ops.Subject{Type: ops.SubjectBook, ID: "book-004"},
			Resource: ops.ResBooks}, func() ([]ops.HistoryRow, error) {
			return []ops.HistoryRow{{BookID: "book-004", Field: "touched", Old: "0", New: "1"}}, nil
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
	if m, ok := s.mark("op1", "title-shared"); !ok || len(m.Batches) != 1 || m.Batches[0].BookID != "book-shared" ||
		!strings.HasPrefix(m.Batches[0].BatchID, "op-op1-") || len(m.Changes) != 0 {
		t.Fatalf("title-shared marker = %+v, %v; want its one book-shared batch", m, ok)
	}
	if m, ok := s.mark("op1", "repoint-002"); !ok || len(m.Changes) != 1 || len(m.Batches) != 0 {
		t.Fatalf("repoint-002 marker = %+v, %v; want its one journal row", m, ok)
	}
	if m, ok := s.mark("op1", "noop-004"); !ok || len(m.Changes)+len(m.Batches) != 0 {
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
		{"effect-004", true},
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
// voided, its repoint row reverted (even with its book write standing), its
// journal row gone. An undo of another field, or one older than the batch,
// leaves it standing; so does an undo of another row's batch on another
// book. A marker that names history the Writer cannot read is no evidence.
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
	for _, row := range []string{"batch-undone", "field-undone", "other-field-undone", "credits-voided",
		"repoint-reverted", "journal-gone", "standing"} {
		mustRowApplied(t, w, row, true)
	}

	m, _ := s.mark("op1", "batch-undone")
	later := time.Now().Add(time.Second)
	s.history = append(s.history,
		database.MetadataChangeRecord{BookID: "book-001", Field: "title", ChangeType: ChangeTypeUndo,
			BatchID: m.Batches[0].BatchID, ChangedAt: later},
		database.MetadataChangeRecord{BookID: "book-002", Field: "title", ChangeType: ChangeTypeUndo, ChangedAt: later},
		database.MetadataChangeRecord{BookID: "book-003", Field: "publisher", ChangeType: ChangeTypeUndo, ChangedAt: later},
		database.MetadataChangeRecord{BookID: "book-003", Field: "title", ChangeType: ChangeTypeUndo,
			ChangedAt: time.Now().Add(-time.Hour)})
	reverted := time.Now()
	credits, _ := s.mark("op1", "credits-voided")
	s.editChange(credits.Changes[0], func(c *database.OperationChange) { c.Voided, c.RevertedAt = true, &reverted })
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
	} {
		mustRowApplied(t, w, c.row, c.want)
	}

	// The same markers through a Writer that cannot read what they name.
	noChanges := New(Config{OpID: "op1", Mode: ops.Live, RowMarks: s, HistoryReader: s})
	mustRowApplied(t, noChanges, "standing", true) // a batch only
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
	mustRowApplied(t, noHistory, "credits-standing", true)
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
