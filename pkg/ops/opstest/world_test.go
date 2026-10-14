// file: pkg/ops/opstest/world_test.go
// version: 1.0.0
// guid: 4b7e1c93-6a28-4d50-9f3e-c1a85d02e7b4
// last-edited: 2026-10-14

package opstest

import (
	"errors"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// TestLeakedUseAfterCleanupIsRefused: a goroutine a def left behind that
// uses the World after its test finished (cleanup closed the store) gets an
// error, for a write and for a read. Before the World retired its last
// generation in cleanup, the write passed the gate of a generation still
// alive and then called a store that was nil, which panics the whole test
// binary.
func TestLeakedUseAfterCleanupIsRefused(t *testing.T) {
	var books *FakeBooks
	t.Run("world", func(t *testing.T) { books = NewWorld(t).Books() }) // its cleanup runs when the subtest ends
	sub := ops.Subject{Type: ops.SubjectBook, ID: "book-001"}
	uses := map[string]func() error{
		"PutValue":   func() error { return books.PutValue(sub, "k", "v") },
		"AppendLog":  func() error { return books.AppendLog("seen", "e") },
		"InsertBook": func() error { return books.InsertBook(database.Book{ID: "book-new"}) },
		"ModifyBook": func() error { _, err := books.ModifyBook("book-001", upperTitle); return err },
		"ModifyBookFile": func() error {
			_, err := books.ModifyBookFile("book-001", "file-001", func(*database.BookFile) error { return nil })
			return err
		},
		"ModifyBookAuthors": func() error {
			_, err := books.ModifyBookAuthors("book-001",
				func(cur []database.BookAuthor) ([]database.BookAuthor, error) { return cur, nil })
			return err
		},
		"GetBookByID":     func() error { _, err := books.GetBookByID("book-001"); return err },
		"AllBooks":        func() error { _, err := books.AllBooks(); return err },
		"Page":            func() error { _, _, err := books.Page("", 5); return err },
		"GetBookFiles":    func() error { _, err := books.GetBookFiles("book-001"); return err },
		"AllBookFiles":    func() error { _, err := books.AllBookFiles(); return err },
		"GetBookFileByID": func() error { _, err := books.GetBookFileByID("book-001", "file-001"); return err },
		"GetBookAuthors":  func() error { _, err := books.GetBookAuthors("book-001"); return err },
	}
	var mu sync.Mutex
	got := map[string]error{}
	var wg sync.WaitGroup
	release := make(chan struct{})
	for name, use := range uses {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-release
			err := use()
			mu.Lock()
			got[name] = err
			mu.Unlock()
		}()
	}
	close(release)
	wg.Wait()
	for name := range uses {
		if !errors.Is(got[name], errWorldClosed) {
			t.Errorf("%s after the World's test finished returned %v, want errWorldClosed", name, got[name])
		}
	}
}

// TestResetRefusesAnOldGenerationsWrites: a write made through a generation
// the World has replaced (a run's registry or Writer that outlived it) is
// refused, not committed to the next case's fresh store or to the closed one.
func TestResetRefusesAnOldGenerationsWrites(t *testing.T) {
	t.Parallel()
	w := NewWorld(t)
	old := w.cur.Load()
	w.Reset()
	if _, err := w.modifyBook(old, "book-001", upperTitle); !errors.Is(err, errWorldReset) {
		t.Fatalf("a write through the generation Reset replaced returned %v, want errWorldReset", err)
	}
	b, err := w.Books().GetBookByID("book-001")
	if err != nil || b == nil || b.Title != "Synthetic Title 001" {
		t.Fatalf("book-001 in the reset World = %+v (%v), want its seeded title", b, err)
	}
	release, err := w.cur.Load().admit()
	if err != nil {
		t.Fatalf("the World's current generation refuses writes: %v", err)
	}
	release()
}
