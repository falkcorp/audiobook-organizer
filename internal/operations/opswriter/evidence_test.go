// file: internal/operations/opswriter/evidence_test.go
// version: 1.0.0
// guid: 4af29ea6-4101-42cd-b6c6-1e86908383ef
// last-edited: 2026-10-14

package opswriter

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// evidenceStore adds the two history reads to recStore. Its journal read
// returns EVERY op's rows, more than the real prefix scan would: Wrote must
// keep only its own op id's rows itself. It counts journal reads.
type evidenceStore struct {
	*recStore
	journalReads atomic.Int32
	journalErr   error
	historyErr   error
}

func (s *evidenceStore) GetOperationChanges(string) ([]*database.OperationChange, error) {
	s.journalReads.Add(1)
	if s.journalErr != nil {
		return nil, s.journalErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*database.OperationChange, len(s.changes))
	for i := range s.changes {
		c := s.changes[i]
		out[i] = &c
	}
	return out, nil
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

// evidenceWriter builds a Live Writer for op over s, the way the v3 registry
// does: batch prefix "op-<op>-", both readers wired.
func evidenceWriter(s *evidenceStore, op string, mode ops.Mode) *Writer {
	return New(Config{OpID: op, Mode: mode, Intents: s, Books: s, Files: s, Authors: s, Journal: s,
		History:       NewBookHistory(s, "test.def", "ops_v3", "op-"+op+"-", nil),
		JournalReader: s, HistoryReader: s})
}

func newEvidenceStore() *evidenceStore {
	s := &evidenceStore{recStore: newRecStore()}
	for _, id := range []string{"book-001", "book-002", "book-003", "book-004", "book-005", "book-010", "book-011"} {
		s.books[id] = &database.Book{ID: id, Title: "t " + id}
	}
	s.files["file-001"] = &database.BookFile{ID: "file-001", BookID: "book-001", FilePath: "/lib/001.m4b", Missing: true}
	s.files["file-002"] = &database.BookFile{ID: "file-002", BookID: "book-002", FilePath: "/lib/002.m4b"}
	s.files["file-003"] = &database.BookFile{ID: "file-003", BookID: "book-002", FilePath: "/lib/003.m4b"}
	return s
}

func mustWrote(t *testing.T, w *Writer, want bool, subjects ...ops.Subject) {
	t.Helper()
	got, err := w.Wrote(context.Background(), subjects)
	if err != nil || got != want {
		t.Fatalf("Wrote(%v) = %v, %v; want %v", subjects, got, err, want)
	}
}

// TestWroteEvidence: the history an earlier attempt of the same op recorded
// through the Writer is evidence for its subjects -- a book's metadata
// history batch, a credits journal row, a repointed or modified book_file --
// and nothing else is: not another op id's journal rows or batches, not a
// batch of a longer op id that starts with this one, not rows this attempt
// recorded, not a subject only some of whose siblings are evidenced. The
// journal is read once however many rows ask.
func TestWroteEvidence(t *testing.T) {
	s := newEvidenceStore()
	ctx := context.Background()
	first := evidenceWriter(s, "op1", ops.Live)
	if _, err := first.ModifyBook(ctx, "book-001", func(b *database.Book) error { b.Title = "new 1"; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := first.RepointBookFile(ctx, "book-001", "file-001", database.BookFileLocation{Path: "/lib/001.m4b", Missing: true},
		database.BookFileLocation{Path: "/lib/001b.m4b"}); err != nil {
		t.Fatal(err)
	}
	if _, err := first.ModifyBookFile(ctx, "book-002", "file-002", func(f *database.BookFile) error { f.Format = "m4b"; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := first.ModifyBookAuthors(ctx, "book-003", func([]database.BookAuthor) ([]database.BookAuthor, error) {
		return []database.BookAuthor{{BookID: "book-003", AuthorID: 7}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	other := evidenceWriter(s, "op2", ops.Live)
	if _, err := other.ModifyBook(ctx, "book-010", func(b *database.Book) error { b.Title = "new 10"; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := other.ModifyBookFile(ctx, "book-002", "file-003", func(f *database.BookFile) error { f.Format = "mp3"; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := other.ModifyBookAuthors(ctx, "book-011", func([]database.BookAuthor) ([]database.BookAuthor, error) {
		return []database.BookAuthor{{BookID: "book-011", AuthorID: 8}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	// op "op1-x" records batches "op-op1-x-<ulid>", which start with op1's
	// prefix "op-op1-".
	s.history = append(s.history, database.MetadataChangeRecord{BookID: "book-004", Field: "title",
		BatchID: "op-op1-x-" + ulid.Make().String(), ChangedAt: time.Now().Add(-time.Hour)})
	// The resumed attempt starts after every row above.
	resumed := New(Config{OpID: "op1", Mode: ops.Live, Intents: s, Books: s, Files: s, Authors: s, Journal: s,
		History:       NewBookHistory(s, "test.def", "ops_v3", "op-op1-", nil),
		JournalReader: s, HistoryReader: s, AttemptStart: time.Now().Add(time.Millisecond)})
	time.Sleep(2 * time.Millisecond) // so this attempt's own rows come after AttemptStart
	// This attempt's own writes are never evidence of an earlier one.
	if _, err := resumed.ModifyBook(ctx, "book-005", func(b *database.Book) error { b.Title = "new 5"; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := resumed.RepointBookFile(ctx, "book-002", "file-003", database.BookFileLocationOf(s.files["file-003"]),
		database.BookFileLocation{Path: "/lib/003b.m4b"}); err != nil {
		t.Fatal(err)
	}

	mustWrote(t, resumed, true, ops.Books("book-001")...)      // metadata history batch
	mustWrote(t, resumed, true, ops.BookFiles("file-001")...)  // repoint journal row
	mustWrote(t, resumed, true, ops.BookFiles("file-002")...)  // file.<field>:<id> journal row
	mustWrote(t, resumed, true, ops.Books("book-003")...)      // credits journal row
	mustWrote(t, resumed, true, ops.Books("book-002")...)      // the book_file row's BookID
	mustWrote(t, resumed, false, ops.Books("book-010")...)     // op2's batch
	mustWrote(t, resumed, false, ops.Books("book-011")...)     // op2's journal row
	mustWrote(t, resumed, false, ops.Books("book-004")...)     // a longer op id's batch
	mustWrote(t, resumed, false, ops.Books("book-005")...)     // this attempt's batch
	mustWrote(t, resumed, false, ops.BookFiles("file-003")...) // op2's and this attempt's rows
	mustWrote(t, resumed, true, append(ops.Books("book-001"), ops.BookFiles("file-001")...)...)
	mustWrote(t, resumed, false, append(ops.Books("book-001"), ops.Books("book-010")...)...)
	mustWrote(t, resumed, false)
	mustWrote(t, resumed, false, ops.Subject{Type: "series", ID: "book-001"})
	if n := s.journalReads.Load(); n != 1 {
		t.Fatalf("the journal was read %d times, want once", n)
	}

	// A Preview Writer never reports evidence.
	mustWrote(t, evidenceWriter(s, "op1", ops.Preview), false, ops.Books("book-001")...)
	// A Writer without the readers finds none, and does not fail.
	bare := New(Config{OpID: "op1", Mode: ops.Live, Intents: s, Books: s, Journal: s,
		History: NewBookHistory(s, "test.def", "ops_v3", "op-op1-", nil)})
	mustWrote(t, bare, false, ops.Books("book-001")...)
	// An empty batch prefix owns no batch, even a bare ULID one.
	s.history = append(s.history, database.MetadataChangeRecord{BookID: "book-010", Field: "title",
		BatchID: ulid.Make().String(), ChangedAt: time.Now().Add(-time.Hour)})
	noPrefix := New(Config{OpID: "op9", Mode: ops.Live, Journal: s, History: NewBookHistory(s, "x", "y", "", nil),
		HistoryReader: s})
	mustWrote(t, noPrefix, false, ops.Books("book-010")...)
}

// A failed read is the caller's error, never "no evidence" or "written", and
// a failed journal read is not cached: the next row reads again. A fenced
// run reads nothing.
func TestWroteReadFailures(t *testing.T) {
	s := newEvidenceStore()
	s.changes = append(s.changes, database.OperationChange{OperationID: "op1", BookID: "book-001",
		FieldName: "authors", CreatedAt: time.Now().Add(-time.Hour)})
	down := errors.New("store down")
	s.journalErr = down
	w := evidenceWriter(s, "op1", ops.Live)
	if _, err := w.Wrote(context.Background(), ops.Books("book-001")); !errors.Is(err, down) {
		t.Fatalf("journal read failure = %v", err)
	}
	s.journalErr = nil
	mustWrote(t, w, true, ops.Books("book-001")...)
	if n := s.journalReads.Load(); n != 2 {
		t.Fatalf("journal reads = %d, want 2 (the failure is not cached)", n)
	}
	s.historyErr = down
	if _, err := w.Wrote(context.Background(), ops.Books("book-002")); !errors.Is(err, down) {
		t.Fatalf("history read failure = %v", err)
	}
	s.historyErr = nil

	f, revoke := NewFence(1, nil)
	fenced := evidenceWriter(s, "op1", ops.Live)
	fenced.SetFence(f.Check)
	revoke()
	before := s.journalReads.Load()
	if ok, err := fenced.Wrote(context.Background(), ops.Books("book-001")); ok || !errors.Is(err, ops.ErrFenced) {
		t.Fatalf("fenced Wrote = %v, %v; want ErrFenced", ok, err)
	}
	if s.journalReads.Load() != before {
		t.Fatal("a fenced Wrote read the store")
	}
}
