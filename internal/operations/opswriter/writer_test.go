// file: internal/operations/opswriter/writer_test.go
// version: 1.8.0
// guid: 1ec011de-862f-44e4-b916-1cdd06721d91
// last-edited: 2026-10-26

package opswriter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// recStore is every store surface the Writer uses, recording the order of
// calls in log.
type recStore struct {
	// The nil reader only makes the fake resolve as a RetiredBookFileReader;
	// GetBookFileByPathIncludingRetired below is the one method the writer reads.
	database.RetiredBookFileReader

	mu      sync.Mutex
	log     []string
	books   map[string]*database.Book
	files   map[string]*database.BookFile
	authors map[string][]database.BookAuthor
	intents map[uint64]database.Intent
	changes []database.OperationChange
	history []database.MetadataChangeRecord
	// mutate runs inside ModifyBook's critical section, after the read and
	// before fn: a concurrent writer that got there first.
	mutate      func(*database.Book)
	failHistory bool
	failJournal bool
	putFails    bool
	onPut       func(database.Intent)
}

func newRecStore() *recStore {
	return &recStore{books: map[string]*database.Book{}, files: map[string]*database.BookFile{},
		authors: map[string][]database.BookAuthor{}, intents: map[uint64]database.Intent{}}
}

func (s *recStore) note(format string, a ...any) { s.log = append(s.log, fmt.Sprintf(format, a...)) }

func (s *recStore) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.books[id]
	if !ok {
		return nil, nil
	}
	cp := *cur
	if s.mutate != nil {
		s.mutate(&cp)
	}
	if err := fn(&cp); err != nil {
		return nil, err
	}
	s.books[id] = &cp
	s.note("write book:%s", id)
	return &cp, nil
}

func (s *recStore) GetBookFileByID(bookID, fileID string) (*database.BookFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.files[fileID]
	if !ok || f.BookID != bookID {
		return nil, nil
	}
	cp := *f
	return &cp, nil
}

// GetBookFileByPathIncludingRetired: this fake never holds a retired row unless a
// test wraps it to say otherwise (retiredPathStore), so its plain path getter is
// the complete answer.
func (s *recStore) GetBookFileByPathIncludingRetired(path string) (*database.BookFile, error) {
	return s.GetBookFileByPath(path)
}

func (s *recStore) GetBookFileByPath(path string) (*database.BookFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range s.files {
		if f.FilePath == path {
			cp := *f
			return &cp, nil
		}
	}
	return nil, nil
}

func (s *recStore) ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.files[fileID]
	if !ok || f.BookID != bookID {
		return nil, nil
	}
	cp := *f
	if err := fn(&cp); err != nil {
		return nil, err
	}
	s.files[fileID] = &cp
	s.note("write book_file:%s", fileID)
	return &cp, nil
}

func (s *recStore) ModifyBookAuthors(bookID string, fn func([]database.BookAuthor) ([]database.BookAuthor, error)) ([]database.BookAuthor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next, err := fn(slices.Clone(s.authors[bookID]))
	if err != nil {
		return nil, err
	}
	s.authors[bookID] = next
	s.note("write authors:%s", bookID)
	return next, nil
}

func (s *recStore) RecordMetadataChange(r *database.MetadataChangeRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failHistory {
		return errors.New("history down")
	}
	s.history = append(s.history, *r)
	s.note("history %s:%s", r.BookID, r.Field)
	return nil
}

func (s *recStore) CreateOperationChange(c *database.OperationChange) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failJournal {
		return errors.New("journal down")
	}
	s.changes = append(s.changes, *c)
	s.note("history %s:%s", c.ChangeType, c.FieldName)
	return nil
}

func (s *recStore) PutIntentV3(_ string, in database.Intent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.putFails {
		return errors.New("intent store down")
	}
	s.intents[in.Seq] = in
	if s.onPut != nil {
		s.onPut(in)
	}
	s.note("intent+ %d %s", in.Seq, in.Subject)
	return nil
}

func (s *recStore) ClearIntentV3(_ string, seq uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.intents, seq)
	s.note("intent- %d", seq)
	return nil
}

func (s *recStore) ListIntentsV3(string) ([]database.Intent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []database.Intent
	for _, in := range s.intents {
		out = append(out, in)
	}
	return out, nil
}

func (s *recStore) logCopy() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.log)
}

func newTestWriter(s *recStore, mode ops.Mode) *Writer {
	return New(Config{OpID: "op1", Mode: mode, Intents: s, Books: s, Files: s, Authors: s, Journal: s,
		History: NewBookHistory(s, "test", "bulk_update", "rp-", nil)})
}

func TestOrderIntentWriteHistory(t *testing.T) {
	s := newRecStore()
	s.books["b1"] = &database.Book{ID: "b1", Title: "Old"}
	s.files["f1"] = &database.BookFile{ID: "f1", BookID: "b1", FilePath: "/lib/a.m4b", TrackNumber: 1}
	w := newTestWriter(s, ops.Live)
	ctx := context.Background()
	ch, err := w.ModifyBook(ctx, "b1", func(b *database.Book) error { b.Title = "New"; return nil })
	if err != nil || !reflect.DeepEqual(ch.Fields, []string{"title"}) {
		t.Fatalf("ModifyBook = %v, %v", ch, err)
	}
	if _, err := w.ModifyBookFile(ctx, "b1", "f1", func(f *database.BookFile) error { f.TrackNumber = 2; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := w.ModifyBookAuthors(ctx, "b1", func([]database.BookAuthor) ([]database.BookAuthor, error) {
		return []database.BookAuthor{{BookID: "b1", AuthorID: 7, Role: "author"}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"intent+ 1 book:b1", "write book:b1", "history b1:title", "intent- 1",
		"intent+ 2 book_file:f1", "write book_file:f1", "history book_file_modify:file.track_number:f1", "intent- 2",
		"intent+ 3 book:b1", "write authors:b1", "history book_authors_modify:authors", "intent- 3",
	}
	if got := s.logCopy(); !reflect.DeepEqual(got, want) {
		t.Fatalf("call order:\n got  %q\n want %q", got, want)
	}
	if len(s.intents) != 0 || w.Writes() != 3 || w.History().Rows() != 3 {
		t.Fatalf("intents left %d, writes %d, history rows %d", len(s.intents), w.Writes(), w.History().Rows())
	}
}

func TestNoHistoryRowWhenNothingChanged(t *testing.T) {
	s := newRecStore()
	s.files["f1"] = &database.BookFile{ID: "f1", BookID: "b1", FilePath: "/lib/a.m4b"}
	w := newTestWriter(s, ops.Live)
	ch, err := w.ModifyBookFile(context.Background(), "b1", "f1", func(*database.BookFile) error { return nil })
	if err != nil || ch.Any() {
		t.Fatalf("no-op re-save = %v, %v", ch, err)
	}
	want := []string{"intent+ 1 book_file:f1", "write book_file:f1", "intent- 1"}
	if got := s.logCopy(); !reflect.DeepEqual(got, want) {
		t.Fatalf("a no-op re-save writes the row and the intent but no history: %q", got)
	}
}

func TestPreviewRefusesAndRecords(t *testing.T) {
	s := newRecStore()
	s.books["b1"] = &database.Book{ID: "b1", Title: "Old"}
	s.files["f1"] = &database.BookFile{ID: "f1", BookID: "b1", FilePath: "/lib/a.m4b"}
	w := newTestWriter(s, ops.Preview)
	ctx := context.Background()
	called := false
	if _, err := w.ModifyBook(ctx, "b1", func(*database.Book) error { called = true; return nil }); !errors.Is(err, ops.ErrPreview) {
		t.Fatalf("ModifyBook in Preview = %v, want ErrPreview", err)
	}
	err := w.RepointBookFile(ctx, "b1", "f1", database.BookFileLocation{Path: "/lib/a.m4b"}, database.BookFileLocation{Path: "/lib/b.m4b"})
	if !errors.Is(err, ops.ErrPreview) {
		t.Fatalf("RepointBookFile in Preview = %v", err)
	}
	if err := w.Effect(ctx, ops.EffectSpec{Name: "reflink", Subject: ops.Subject{Type: ops.SubjectBook, ID: "b1"}},
		func() ([]ops.HistoryRow, error) { called = true; return nil, nil }); !errors.Is(err, ops.ErrPreview) {
		t.Fatalf("Effect in Preview = %v", err)
	}
	if called || len(s.logCopy()) != 0 || s.books["b1"].Title != "Old" {
		t.Fatalf("Preview touched the store: called=%v log=%q", called, s.logCopy())
	}
	planned, dropped := w.Planned()
	if len(planned) != 3 || dropped != 0 || planned[0].Subject != "book:b1" || planned[1].Kind != "repoint" {
		t.Fatalf("plan lines = %+v (dropped %d)", planned, dropped)
	}
}

// The previous value must come from inside the critical section: a write
// that landed between the caller's read and this one is reported against the
// value it actually replaced.
func TestPreviousCapturedInCallback(t *testing.T) {
	s := newRecStore()
	s.books["b1"] = &database.Book{ID: "b1", Title: "Caller saw this"}
	s.mutate = func(b *database.Book) { b.Title = "Another writer's title" }
	w := newTestWriter(s, ops.Live)
	if _, err := w.ModifyBook(context.Background(), "b1", func(b *database.Book) error { b.Title = "Mine"; return nil }); err != nil {
		t.Fatal(err)
	}
	if len(s.history) != 1 || *s.history[0].PreviousValue != `"Another writer's title"` || *s.history[0].NewValue != `"Mine"` {
		t.Fatalf("history = %+v", s.history)
	}
}

func TestFencedRefuses(t *testing.T) {
	s := newRecStore()
	s.books["b1"] = &database.Book{ID: "b1", Title: "Old"}
	f, revoke := NewFence(1, nil)
	w := newTestWriter(s, ops.Live)
	w.SetFence(f.Check)
	revoke()
	_, err := w.ModifyBook(context.Background(), "b1", func(b *database.Book) error { b.Title = "x"; return nil })
	if !errors.Is(err, ErrFenced) || !errors.Is(err, ops.ErrFenced) {
		t.Fatalf("err = %v, want ErrFenced", err)
	}
	if len(s.logCopy()) != 0 {
		t.Fatalf("a fenced write touched the store: %q", s.logCopy())
	}
	// A lost lease behind the fence is reported as the loss, without renewing.
	renewed := 0
	w.SetLease(func() bool { renewed++; return true })
	w.SetLeaseValid(func() bool { return false })
	if _, err := w.ModifyBook(context.Background(), "b1", func(*database.Book) error { return nil }); !errors.Is(err, ErrStandDownLost) || !errors.Is(err, ErrFenced) {
		t.Fatalf("lost lease behind the fence = %v", err)
	}
	if renewed != 0 {
		t.Fatal("a fenced write renewed the lease")
	}
	// A canceled item context refuses too.
	w2 := newTestWriter(s, ops.Live)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := w2.ModifyBook(ctx, "b1", func(*database.Book) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled ctx = %v", err)
	}
}

// Under -race: writers hammer the Writer while the fence is revoked mid-run.
// Every write that passes the gate after the revoke would be a zombie write.
func TestFenceRevokedWhileWritingRefusesEveryLaterWrite(t *testing.T) {
	s := newRecStore()
	for i := range 64 {
		s.books[fmt.Sprint(i)] = &database.Book{ID: fmt.Sprint(i)}
	}
	f, revoke := NewFence(1, nil)
	w := newTestWriter(s, ops.Live)
	w.SetFence(f.Check)
	var revoked atomic.Bool
	var lateWrites atomic.Int64
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Go(func() {
			for i := range 200 {
				id := fmt.Sprint((g*200 + i) % 64)
				_, err := w.ModifyBook(context.Background(), id, func(b *database.Book) error {
					if revoked.Load() {
						lateWrites.Add(1)
					}
					b.Title = "t"
					return nil
				})
				if err != nil && !errors.Is(err, ErrFenced) {
					t.Errorf("unexpected error: %v", err)
				}
				if g == 0 && i == 50 {
					revoked.Store(true)
					revoke()
				}
			}
		})
	}
	wg.Wait()
	// A write whose gate passed before the revoke may still commit; one that
	// reached the gate after it may not. The flag is set just before the
	// revoke, and each goroutine writes sequentially, so a goroutine can have
	// at most one write that passed the gate in time and runs its callback
	// after the flag: 8 in all. Without the fence check every later write of
	// every goroutine would count, well over a thousand.
	if n := lateWrites.Load(); n > 8 {
		t.Fatalf("%d writes committed after the revoke (more than the 8 in flight)", n)
	}
	before := w.Writes()
	if _, err := w.ModifyBook(context.Background(), "0", func(*database.Book) error { return nil }); !errors.Is(err, ErrFenced) {
		t.Fatalf("after revoke: %v", err)
	}
	if w.Writes() != before {
		t.Fatal("a write committed after the revoke")
	}
}

func TestNoDeleteMethod(t *testing.T) {
	for _, typ := range []reflect.Type{reflect.TypeFor[ops.Writer](), reflect.TypeFor[*Writer]()} {
		for method := range typ.Methods() {
			n := strings.ToLower(method.Name)
			if strings.Contains(n, "delete") || strings.Contains(n, "remove") || strings.Contains(n, "purge") {
				t.Errorf("%s exposes %s", typ, method.Name)
			}
		}
	}
}

// A crash between the intent and its clear leaves the intent; the next
// attempt numbers its intents after it, so it never overwrites (and then
// clears) the evidence.
func TestCrashLeavesIntent(t *testing.T) {
	s := newRecStore()
	s.books["b1"] = &database.Book{ID: "b1"}
	// The crashed attempt wrote intent 5 and died before the write or the
	// clear: the intent is all that is left of it.
	if err := s.PutIntentV3("op1", database.Intent{Seq: 5, Subject: "book:b1", Kind: "modify"}); err != nil {
		t.Fatal(err)
	}
	resumed := newTestWriter(s, ops.Live)
	if _, err := resumed.ModifyBook(context.Background(), "b1", func(b *database.Book) error { b.Title = "x"; return nil }); err != nil {
		t.Fatal(err)
	}
	left, _ := s.ListIntentsV3("op1")
	if len(left) != 1 || left[0].Seq != 5 {
		t.Fatalf("the crashed attempt's intent must survive the resume: %+v", left)
	}
	if !slices.Contains(s.logCopy(), "intent+ 6 book:b1") {
		t.Fatalf("the resumed attempt must number after seq 5: %q", s.logCopy())
	}
}

func TestEffectFailureKeepsIntentAndRecordsRows(t *testing.T) {
	s := newRecStore()
	w := newTestWriter(s, ops.Live)
	spec := ops.EffectSpec{Name: "reflink", Subject: ops.Subject{Type: ops.SubjectBook, ID: "b1"}, Resource: ops.ResFiles}
	err := w.Effect(context.Background(), spec, func() ([]ops.HistoryRow, error) {
		return []ops.HistoryRow{{BookID: "b1", Field: "copy:1", Old: "", New: "/lib/x"}}, errors.New("disk full")
	})
	if err == nil || len(s.intents) != 1 {
		t.Fatalf("err %v, intents %d: a failed effect may be partially applied", err, len(s.intents))
	}
	if len(s.changes) != 1 || s.changes[0].ChangeType != "effect:reflink" {
		t.Fatalf("changes = %+v", s.changes)
	}
	if err := w.Effect(context.Background(), spec, func() ([]ops.HistoryRow, error) { return nil, nil }); err != nil || len(s.intents) != 1 {
		t.Fatalf("a successful effect clears its own intent only: %v %d", err, len(s.intents))
	}
}

func TestRepointBookFile(t *testing.T) {
	s := newRecStore()
	s.files["f1"] = &database.BookFile{ID: "f1", BookID: "b1", FilePath: "/lib/old.m4b", Missing: true}
	s.files["f2"] = &database.BookFile{ID: "f2", BookID: "b2", FilePath: "/lib/new.m4b"}
	w := newTestWriter(s, ops.Live)
	ctx := context.Background()
	stale := database.BookFileLocation{Path: "/lib/elsewhere.m4b"}
	if err := w.RepointBookFile(ctx, "b1", "f1", stale, database.BookFileLocation{Path: "/lib/new.m4b"}); !errors.Is(err, ops.ErrChangedSincePlan) {
		t.Fatalf("stale expect = %v", err)
	}
	expect := database.BookFileLocation{Path: "/lib/old.m4b", Missing: true}
	if err := w.RepointBookFile(ctx, "b1", "f1", expect, database.BookFileLocation{Path: "/lib/new.m4b"}); err != nil {
		t.Fatal(err)
	}
	if s.files["f1"].FilePath != "/lib/new.m4b" || len(s.files) != 2 {
		t.Fatalf("files = %+v", s.files)
	}
	if len(s.changes) != 1 || s.changes[0].ChangeType != database.ChangeTypeBookFileRepoint || s.changes[0].FieldName != "book_file:f1" {
		t.Fatalf("history = %+v", s.changes)
	}
	to, err := database.DecodeBookFileLocation(s.changes[0].NewValue)
	if err != nil || to.KeyOwnerRow != "f2" || to.KeyOwnerBook != "b2" {
		t.Fatalf("the repoint must name the key owner it took the path from: %+v %v", to, err)
	}
}

// retiredPathStore answers like the production store: the plain path getter
// skips a retired row, the include-retired twin does not. Only the two path
// getters are implemented; the embedded reader is nil and never called.
type retiredPathStore struct {
	*recStore
	database.RetiredBookFileReader
}

func (r retiredPathStore) GetBookFileByPath(path string) (*database.BookFile, error) {
	f, err := r.recStore.GetBookFileByPath(path)
	if f != nil && f.Retired {
		return nil, err
	}
	return f, err
}

func (r retiredPathStore) GetBookFileByPathIncludingRetired(path string) (*database.BookFile, error) {
	return r.recStore.GetBookFileByPath(path)
}

// A retired row holding the target path's single-owner key is journaled as the
// key's owner: the plain path getter cannot see it, so without the
// include-retired read the repoint's history named no owner and a revert had
// nobody to hand the key back to. The revert that reads it back is
// TestRevertRepoint_KeyGoesBackToARetiredOwner (plugins/maintenance).
func TestRepointBookFile_JournalsRetiredKeyOwner(t *testing.T) {
	s := newRecStore()
	s.files["f1"] = &database.BookFile{ID: "f1", BookID: "b1", FilePath: "/lib/old.m4b"}
	s.files["r1"] = &database.BookFile{ID: "r1", BookID: "holder", FilePath: "/lib/new.m4b", Retired: true, VersionID: "v1"}
	w := New(Config{OpID: "op1", Mode: ops.Live, Intents: s, Books: s, Files: retiredPathStore{recStore: s}, Authors: s, Journal: s,
		History: NewBookHistory(s, "test", "bulk_update", "rp-", nil)})
	expect := database.BookFileLocation{Path: "/lib/old.m4b"}
	if err := w.RepointBookFile(context.Background(), "b1", "f1", expect, database.BookFileLocation{Path: "/lib/new.m4b"}); err != nil {
		t.Fatal(err)
	}
	if len(s.changes) != 1 {
		t.Fatalf("history = %+v", s.changes)
	}
	to, err := database.DecodeBookFileLocation(s.changes[0].NewValue)
	if err != nil || to.KeyOwnerRow != "r1" || to.KeyOwnerBook != "holder" {
		t.Fatalf("the retired key owner must be journaled: %+v %v", to, err)
	}
}

func TestHistoryFailureMarksBatchIncomplete(t *testing.T) {
	s := newRecStore()
	s.books["b1"] = &database.Book{ID: "b1", Title: "Old"}
	s.failHistory = true
	w := newTestWriter(s, ops.Live)
	if _, err := w.ModifyBook(context.Background(), "b1", func(b *database.Book) error { b.Title = "New"; return nil }); err != nil {
		t.Fatalf("a history failure does not undo a committed write: %v", err)
	}
	if w.History().Failed() != 1 || w.History().Rows() != 0 {
		t.Fatalf("rows %d failed %d", w.History().Rows(), w.History().Failed())
	}
}

func TestIntentWriteFailureRefusesTheWrite(t *testing.T) {
	s := newRecStore()
	s.books["b1"] = &database.Book{ID: "b1", Title: "Old"}
	s.putFails = true
	w := newTestWriter(s, ops.Live)
	if _, err := w.ModifyBook(context.Background(), "b1", func(b *database.Book) error { b.Title = "New"; return nil }); err == nil {
		t.Fatal("a write without its intent was made")
	}
	if s.books["b1"].Title != "Old" {
		t.Fatal("the store was written although the intent was not")
	}
}

// TestIntentClearedOnlyAfterRecordedWrite: a write's intent is cleared only
// once the write committed and its history was recorded. A failed write, a
// panic, or a history row that could not be recorded leaves the intent, so
// recovery reports the subject as "may be partially applied".
func TestIntentClearedOnlyAfterRecordedWrite(t *testing.T) {
	setup := func() (*recStore, *Writer) {
		s := newRecStore()
		s.books["b1"] = &database.Book{ID: "b1", Title: "Old"}
		s.files["f1"] = &database.BookFile{ID: "f1", BookID: "b1", FilePath: "/a/1.m4b"}
		return s, newTestWriter(s, ops.Live)
	}
	ctx := context.Background()
	retitle := func(b *database.Book) error { b.Title = "New"; return nil }
	cases := []struct {
		name string
		run  func(*recStore, *Writer)
		left int
	}{
		{"book written and recorded", func(_ *recStore, w *Writer) { _, _ = w.ModifyBook(ctx, "b1", retitle) }, 0},
		{"book history not recorded", func(s *recStore, w *Writer) {
			s.failHistory = true
			_, _ = w.ModifyBook(ctx, "b1", retitle)
		}, 1},
		{"book callback panics", func(_ *recStore, w *Writer) {
			defer func() { _ = recover() }()
			_, _ = w.ModifyBook(ctx, "b1", func(*database.Book) error { panic("boom") })
		}, 1},
		{"book callback fails", func(_ *recStore, w *Writer) {
			_, _ = w.ModifyBook(ctx, "b1", func(*database.Book) error { return errors.New("no") })
		}, 1},
		{"file written and journaled", func(_ *recStore, w *Writer) {
			_, _ = w.ModifyBookFile(ctx, "b1", "f1", func(f *database.BookFile) error { f.Format = "m4b"; return nil })
		}, 0},
		{"file journal row not recorded", func(s *recStore, w *Writer) {
			s.failJournal = true
			_, _ = w.ModifyBookFile(ctx, "b1", "f1", func(f *database.BookFile) error { f.Format = "m4b"; return nil })
		}, 1},
		{"repoint journal row not recorded", func(s *recStore, w *Writer) {
			s.failJournal = true
			_ = w.RepointBookFile(ctx, "b1", "f1", database.BookFileLocation{Path: "/a/1.m4b"}, database.BookFileLocation{Path: "/b/1.m4b"})
		}, 1},
		{"authors journal row not recorded", func(s *recStore, w *Writer) {
			s.failJournal = true
			_, _ = w.ModifyBookAuthors(ctx, "b1", func(cur []database.BookAuthor) ([]database.BookAuthor, error) {
				return append(cur, database.BookAuthor{BookID: "b1", AuthorID: 7}), nil
			})
		}, 1},
		{"authors unchanged", func(_ *recStore, w *Writer) {
			_, _ = w.ModifyBookAuthors(ctx, "b1", func(cur []database.BookAuthor) ([]database.BookAuthor, error) { return cur, nil })
		}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, w := setup()
			c.run(s, w)
			if left, _ := s.ListIntentsV3("op1"); len(left) != c.left {
				t.Fatalf("intents left = %d, want %d (log %q)", len(left), c.left, s.logCopy())
			}
		})
	}
}

// S4 (05-PR5 review): the Writer fails closed.

// A repoint with no operation journal would commit a write nothing records,
// which the operation revert could never undo: refused before writing.
func TestRepointWithoutJournalRefused(t *testing.T) {
	s := newRecStore()
	s.files["f1"] = &database.BookFile{ID: "f1", BookID: "b1", FilePath: "/lib/old.m4b"}
	w := New(Config{OpID: "op1", Mode: ops.Live, Intents: s, Files: s, Authors: s, History: NewBookHistory(s, "t", "x", "p-", nil)})
	err := w.RepointBookFile(context.Background(), "b1", "f1", database.BookFileLocation{Path: "/lib/old.m4b"}, database.BookFileLocation{Path: "/lib/new.m4b"})
	if !errors.Is(err, ErrNotJournaled) {
		t.Fatalf("err = %v, want ErrNotJournaled", err)
	}
	if s.files["f1"].FilePath != "/lib/old.m4b" || len(s.intents) != 0 {
		t.Fatalf("a refused repoint wrote: %+v intents %d", s.files["f1"], len(s.intents))
	}
	for _, write := range []func() error{
		func() error {
			_, err := w.ModifyBookFile(context.Background(), "b1", "f1", func(*database.BookFile) error { return nil })
			return err
		},
		func() error {
			_, err := w.ModifyBookAuthors(context.Background(), "b1", func(a []database.BookAuthor) ([]database.BookAuthor, error) { return a, nil })
			return err
		},
		func() error {
			return w.Effect(context.Background(), ops.EffectSpec{Name: "x"}, func() ([]ops.HistoryRow, error) {
				t.Error("an unjournaled effect ran")
				return nil, nil
			})
		},
	} {
		if err := write(); !errors.Is(err, ErrNotJournaled) {
			t.Errorf("unjournaled write: %v", err)
		}
	}
}

// The fence is checked before the repoint reads anything: a fenced run gets
// ErrFenced, never ErrChangedSincePlan from a read it should not have made.
func TestRepointChecksFenceFirst(t *testing.T) {
	s := newRecStore()
	s.files["f1"] = &database.BookFile{ID: "f1", BookID: "b1", FilePath: "/lib/moved.m4b"}
	f, revoke := NewFence(1, nil)
	w := newTestWriter(s, ops.Live)
	w.SetFence(f.Check)
	revoke()
	err := w.RepointBookFile(context.Background(), "b1", "f1", database.BookFileLocation{Path: "/lib/old.m4b"}, database.BookFileLocation{Path: "/lib/new.m4b"})
	if !errors.Is(err, ErrFenced) || errors.Is(err, ops.ErrChangedSincePlan) {
		t.Fatalf("err = %v, want ErrFenced only", err)
	}
}

// A fence revoked while RepointBookFile reads (after its first check, before
// its intent) still refuses the write: the fence is checked again right
// before the intent.
func TestRepointRechecksFenceBeforeIntent(t *testing.T) {
	s := newRecStore()
	s.files["f1"] = &database.BookFile{ID: "f1", BookID: "b1", FilePath: "/lib/old.m4b"}
	w := newTestWriter(s, ops.Live)
	checks := 0
	w.SetFence(func() error {
		checks++
		if checks > 1 {
			return ErrFenced
		}
		return nil
	})
	err := w.RepointBookFile(context.Background(), "b1", "f1", database.BookFileLocation{Path: "/lib/old.m4b"}, database.BookFileLocation{Path: "/lib/new.m4b"})
	if !errors.Is(err, ErrFenced) {
		t.Fatalf("err = %v, want ErrFenced", err)
	}
	if len(s.intents) != 0 || s.files["f1"].FilePath != "/lib/old.m4b" {
		t.Fatalf("a fenced repoint wrote: intents %d path %q", len(s.intents), s.files["f1"].FilePath)
	}
}

// The repoint intent carries both locations, so a crash between the write
// and the history row leaves what a revert needs.
func TestRepointIntentCarriesLocations(t *testing.T) {
	s := newRecStore()
	s.files["f1"] = &database.BookFile{ID: "f1", BookID: "b1", FilePath: "/lib/old.m4b"}
	w := newTestWriter(s, ops.Live)
	var planned json.RawMessage
	s.onPut = func(in database.Intent) { planned = in.Planned }
	if err := w.RepointBookFile(context.Background(), "b1", "f1", database.BookFileLocation{Path: "/lib/old.m4b"},
		database.BookFileLocation{Path: "/lib/new.m4b"}); err != nil {
		t.Fatal(err)
	}
	var plan struct{ Op, Detail string }
	if err := json.Unmarshal(planned, &plan); err != nil {
		t.Fatalf("intent %s: %v", planned, err)
	}
	var d repointDetail
	if err := json.Unmarshal([]byte(plan.Detail), &d); err != nil {
		t.Fatalf("detail %q: %v", plan.Detail, err)
	}
	from, ferr := database.DecodeBookFileLocation(d.From)
	to, terr := database.DecodeBookFileLocation(d.To)
	if ferr != nil || terr != nil || from.Path != "/lib/old.m4b" || to.Path != "/lib/new.m4b" || d.BookID != "b1" {
		t.Fatalf("intent detail = %+v (%v %v)", d, ferr, terr)
	}
}

// A Live ModifyBook with no metadata history recorder is refused before the
// intent and the write; Preview still records its plan line.
func TestModifyBookWithoutHistoryRefused(t *testing.T) {
	s := newRecStore()
	s.books["b1"] = &database.Book{ID: "b1", Title: "Old"}
	w := New(Config{OpID: "op1", Mode: ops.Live, Intents: s, Books: s, Journal: s})
	_, err := w.ModifyBook(context.Background(), "b1", func(b *database.Book) error { b.Title = "New"; return nil })
	if !errors.Is(err, ErrNoHistory) {
		t.Fatalf("err = %v, want ErrNoHistory", err)
	}
	if s.books["b1"].Title != "Old" || len(s.intents) != 0 {
		t.Fatalf("a refused write wrote: title %q intents %d", s.books["b1"].Title, len(s.intents))
	}
	p := New(Config{OpID: "op1", Mode: ops.Preview, Books: s})
	if _, err := p.ModifyBook(context.Background(), "b1", func(b *database.Book) error { b.Title = "New"; return nil }); !errors.Is(err, ops.ErrPreview) {
		t.Fatalf("preview: err = %v, want ErrPreview", err)
	}
}

// A write refused because the run is fenced (or is being stopped) is flagged
// on the item's outcome (ops.Tally.Refused) through the item's context, so
// the batch runner hears of it even when the op swallows the error; a write
// that went through, or one refused only by the item's own deadline, is not.
func TestRefusalFlaggedOnTheItem(t *testing.T) {
	write := func(w *Writer, ctx context.Context) (error, error) {
		tally := &ops.Tally{}
		irc := ops.NewItemRC(ops.NewRC(ops.RunEnv{Context: ctx}), ctx, tally, nil)
		_, err := w.ModifyBook(irc.Context(), "b1", func(b *database.Book) error { b.Title = "x"; return nil })
		return err, tally.Refused()
	}
	setup := func() (*Writer, func()) {
		s := newRecStore()
		s.books["b1"] = &database.Book{ID: "b1"}
		f, revoke := NewFence(1, nil)
		w := newTestWriter(s, ops.Live)
		w.SetFence(f.Check)
		return w, revoke
	}
	t.Run("clean write", func(t *testing.T) {
		w, _ := setup()
		if err, refused := write(w, context.Background()); err != nil || refused != nil {
			t.Fatalf("err %v refused %v", err, refused)
		}
	})
	t.Run("fenced", func(t *testing.T) {
		w, revoke := setup()
		revoke()
		if err, refused := write(w, context.Background()); !errors.Is(err, ErrFenced) || !errors.Is(refused, ErrFenced) {
			t.Fatalf("err %v refused %v", err, refused)
		}
	})
	t.Run("revoked then canceled reports the fence", func(t *testing.T) {
		w, revoke := setup()
		revoke()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err, refused := write(w, ctx); !errors.Is(err, ErrFenced) || !errors.Is(refused, ErrFenced) {
			t.Fatalf("err %v refused %v", err, refused)
		}
	})
	t.Run("item timeout is not a refusal", func(t *testing.T) {
		w, _ := setup()
		ctx, cancel := context.WithTimeoutCause(context.Background(), time.Nanosecond, ops.ErrItemTimedOut)
		defer cancel()
		<-ctx.Done()
		if err, refused := write(w, ctx); err == nil || refused != nil {
			t.Fatalf("err %v refused %v", err, refused)
		}
	})
	t.Run("item returned is not a refusal", func(t *testing.T) {
		w, _ := setup()
		ctx, cancel := context.WithCancelCause(context.Background())
		cancel(ops.ErrItemReturned)
		if err, refused := write(w, ctx); !errors.Is(err, ops.ErrItemReturned) || refused != nil {
			t.Fatalf("err %v refused %v", err, refused)
		}
	})
	t.Run("a bare deadline is a refusal", func(t *testing.T) {
		// N-1: only the runner's item-scoped causes are exempt.
		w, _ := setup()
		ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
		defer cancel()
		<-ctx.Done()
		if err, refused := write(w, ctx); err == nil || refused == nil {
			t.Fatalf("err %v refused %v", err, refused)
		}
	})
	t.Run("an unrelated context goes to the run latch", func(t *testing.T) {
		s := newRecStore()
		s.books["b1"] = &database.Book{ID: "b1"}
		f, revoke := NewFence(1, nil)
		latch := &ops.RefusalLatch{}
		w := New(Config{OpID: "op1", Mode: ops.Live, Intents: s, Books: s, Journal: s, RunLatch: latch,
			History: NewBookHistory(s, "test", "bulk_update", "rp-", nil)})
		w.SetFence(f.Check)
		revoke()
		_, err := w.ModifyBook(context.Background(), "b1", func(b *database.Book) error { return nil })
		if !errors.Is(err, ErrFenced) || !errors.Is(latch.Err(), ErrFenced) {
			t.Fatalf("err %v latch %v", err, latch.Err())
		}
	})
}
