// file: internal/plugins/maintenance/lost_update_test.go
// version: 1.0.0
// guid: 6c1f0b7e-2d94-4a58-9e3b-7f2a5c8d1b04
// last-edited: 2026-09-15

package maintenance

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/transcribe"
)

// lostUpdateRows is the stored table behind newLostUpdateStore. It models the
// OTHER writer the way internal/maintenance/jobs/lost_update_test.go does: the
// land function (Duration=4242 by default) runs on the stored row right after
// every raw GetBookByID (which returns a copy, as the real store does) and
// right before every ModifyBook takes the row. An op that reads the row and
// writes the WHOLE row back reverts it; one that writes through ModifyBook
// keeps it.
type lostUpdateRows struct {
	mu   sync.Mutex
	rows map[string]*database.Book
	land func(*database.Book)
}

func landDuration4242(b *database.Book) {
	d := 4242
	b.Duration = &d
}

func (r *lostUpdateRows) landOn(id string) {
	if b := r.rows[id]; b != nil {
		r.land(b)
	}
}

func (r *lostUpdateRows) get(id string) *database.Book {
	r.mu.Lock()
	defer r.mu.Unlock()
	if b := r.rows[id]; b != nil {
		cp := *b
		return &cp
	}
	return nil
}

func newLostUpdateStore(land func(*database.Book), books ...database.Book) (*database.MockStore, *lostUpdateRows) {
	if land == nil {
		land = landDuration4242
	}
	r := &lostUpdateRows{rows: map[string]*database.Book{}, land: land}
	for i := range books {
		b := books[i]
		r.rows[b.ID] = &b
	}
	m := &database.MockStore{}
	m.GetBookByIDFunc = func(id string) (*database.Book, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		b := r.rows[id]
		if b == nil {
			return nil, nil
		}
		cp := *b
		r.landOn(id)
		return &cp, nil
	}
	m.UpdateBookFunc = func(id string, b *database.Book) (*database.Book, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		cp := *b
		r.rows[id] = &cp
		return &cp, nil
	}
	m.ModifyBookFunc = func(id string, fn func(*database.Book) error) (*database.Book, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.landOn(id)
		b := r.rows[id]
		if b == nil {
			return nil, nil
		}
		cp := *b
		if err := fn(&cp); err != nil {
			if errors.Is(err, database.ErrSkipBookWrite) {
				return b, nil
			}
			return nil, err
		}
		r.rows[id] = &cp
		out := cp
		return &out, nil
	}
	return m, r
}

func assertDurationKept(t *testing.T, site string, got *database.Book) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s: book vanished from the store", site)
	}
	if got.Duration == nil || *got.Duration != 4242 {
		t.Fatalf("Duration reverted by the %s write: got %v, want 4242 (the concurrent writer's value)", site, got.Duration)
	}
}

// TestApplyOutcome_DoesNotRevertConcurrentColumns pins the lost-update fix
// (audit A1#15) on intro-transcribe's per-book outcome write: the book was
// copied before ffmpeg and whisper ran, so the write must set only the
// transcribe columns and a Duration another writer committed meanwhile
// survives.
func TestApplyOutcome_DoesNotRevertConcurrentColumns(t *testing.T) {
	store, rows := newLostUpdateStore(nil, database.Book{ID: "t1", Title: "Transcribed"})
	p := New(fakeDeps{store: store})
	accum := newTranscribeStatsAccum(nil, "op", 0, time.Now())
	book := database.Book{ID: "t1", Title: "Transcribed"}
	ok := p.applyOutcome(store, slog.Default(), &book, statusOK, "", "This is Audible. Dune by Frank Herbert.",
		transcribe.IntroFields{Title: "Dune", Author: "Frank Herbert"}, time.Now(), accum)
	if !ok {
		t.Fatal("applyOutcome: an OK transcript must count as processed")
	}
	got := rows.get("t1")
	if got == nil || got.TranscribeStatus == nil || *got.TranscribeStatus != statusOK {
		t.Fatalf("TranscribeStatus not written as ok: %+v", got)
	}
	if got.TranscribedTitle == nil || *got.TranscribedTitle != "Dune" {
		t.Fatalf("TranscribedTitle not written: %+v", got.TranscribedTitle)
	}
	assertDurationKept(t, "intro-transcribe outcome", got)
}

// TestStampVerifiedAt_DoesNotRevertConcurrentColumns pins the same fix on
// duration-reextract's DurationVerifiedAt stamp, which lands once per examined
// book: a Narrator another writer committed meanwhile survives.
func TestStampVerifiedAt_DoesNotRevertConcurrentColumns(t *testing.T) {
	store, rows := newLostUpdateStore(func(b *database.Book) {
		n := "Concurrent Narrator"
		b.Narrator = &n
	}, database.Book{ID: "d1", Title: "Timed"})
	stampVerifiedAt(store, &fakeReporter{}, "d1")
	got := rows.get("d1")
	if got == nil || got.DurationVerifiedAt == nil {
		t.Fatalf("DurationVerifiedAt not stamped: %+v", got)
	}
	if got.Narrator == nil || *got.Narrator != "Concurrent Narrator" {
		t.Fatalf("Narrator reverted by the duration-reextract stamp write: got %v, want %q (the concurrent writer's value)", got.Narrator, "Concurrent Narrator")
	}
}

// TestAuthorSplit_DoesNotRevertConcurrentColumns pins the same fix on the
// author split's primary-author write (the "keep in sync" twin of
// internal/scheduler/extra_ops.go): it must set only AuthorID and Author.
func TestAuthorSplit_DoesNotRevertConcurrentColumns(t *testing.T) {
	const (
		compositeID = 1
		firstNewID  = 101
		secondNewID = 102
	)
	store, rows := newLostUpdateStore(nil, database.Book{
		ID:       "bk1",
		Title:    "The Left Hand of Darkness",
		AuthorID: new(compositeID),
		Author:   &database.Author{ID: compositeID, Name: "Alice Smith / Bob Jones"},
	})
	createCalls := 0
	store.GetAllAuthorsFunc = func() ([]database.Author, error) {
		return []database.Author{{ID: compositeID, Name: "Alice Smith / Bob Jones"}}, nil
	}
	store.GetAuthorByNameFunc = func(_ string) (*database.Author, error) { return nil, nil }
	store.CreateAuthorFunc = func(name string) (*database.Author, error) {
		createCalls++
		id := firstNewID
		if createCalls == 2 {
			id = secondNewID
		}
		return &database.Author{ID: id, Name: name}, nil
	}
	store.GetBooksByAuthorIDWithRoleFunc = func(authorID int) ([]database.BookCore, error) {
		if authorID != compositeID {
			return nil, nil
		}
		return []database.BookCore{{ID: "bk1", Title: "The Left Hand of Darkness", AuthorID: new(compositeID)}}, nil
	}
	store.DeleteAuthorFunc = func(_ int) error { return nil }
	junction := map[string][]database.BookAuthor{}
	store.SetBookAuthorsFunc = func(bookID string, authors []database.BookAuthor) error {
		junction[bookID] = authors
		return nil
	}
	store.GetBooksByAuthorIDForRelinkFunc = relinkAwareBooks(store.GetBooksByAuthorIDWithRoleFunc, junction,
		func(bookID string) (*int, bool) {
			b := rows.get(bookID)
			if b == nil {
				return nil, false
			}
			return b.AuthorID, true
		})

	p := New(fakeDeps{store: store})
	if err := p.runAuthorSplitScan(context.Background(), nil, &fakeReporter{}); err != nil {
		t.Fatalf("runAuthorSplitScan: %v", err)
	}
	got := rows.get("bk1")
	if got == nil || got.AuthorID == nil || *got.AuthorID != firstNewID {
		t.Fatalf("AuthorID not advanced to the first split author: %+v", got)
	}
	if got.Author == nil || got.Author.ID != firstNewID {
		t.Fatalf("denormalized Author not refreshed: %+v", got.Author)
	}
	assertDurationKept(t, "author split", got)
}

// TestRetitleBook_DoesNotRevertConcurrentColumns pins the same fix on the
// title write shared by title-repair and title-backfill: it must set only
// Title, and only while the stored title is still the one the new title was
// derived from.
func TestRetitleBook_DoesNotRevertConcurrentColumns(t *testing.T) {
	store, rows := newLostUpdateStore(nil, database.Book{ID: "r1", Title: "01 - Chapter One"})
	if err := retitleBook(store, "r1", "01 - Chapter One", "Dune"); err != nil {
		t.Fatalf("retitleBook: %v", err)
	}
	got := rows.get("r1")
	if got == nil || got.Title != "Dune" {
		t.Fatalf("Title not written: %+v", got)
	}
	assertDurationKept(t, "title repair", got)
}

// TestRetitleBook_SkipsWhenTitleChangedUnderneath pins the fresh-row
// precondition: a title another writer set meanwhile is kept, not overwritten
// with a value derived from the old one.
func TestRetitleBook_SkipsWhenTitleChangedUnderneath(t *testing.T) {
	store, rows := newLostUpdateStore(func(b *database.Book) { b.Title = "Set By Someone Else" },
		database.Book{ID: "r2", Title: "01 - Chapter One"})
	err := retitleBook(store, "r2", "01 - Chapter One", "Dune")
	if !errors.Is(err, errRetitleChangedUnderneath) {
		t.Fatalf("retitleBook: err = %v, want errRetitleChangedUnderneath", err)
	}
	got := rows.get("r2")
	if got == nil || got.Title != "Set By Someone Else" {
		t.Fatalf("concurrent title overwritten: %+v", got)
	}
}
