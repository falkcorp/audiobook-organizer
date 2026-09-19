// file: internal/database/pebble_store_book_lock_test.go
// version: 1.2.0
// guid: 8b1e4d27-5c93-4f0a-a6d2-7e39c1f5b084
// last-edited: 2026-09-19

package database

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// withDeadline fails the test if fn has not returned within d. A hang here is
// a lock-ordering bug (a stripe re-entered or two stripes held in a cycle).
func withDeadline(t *testing.T, d time.Duration, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not finish within %s: deadlock on a book write stripe", what, d)
	}
}

// TestModifyBook_ConcurrentWritersLoseNothing: many goroutines change DIFFERENT
// fields of ONE book through ModifyBook at the same time, and every one of
// them also increments a shared counter field. With the read and the write
// under one stripe hold, no writer can commit a stale copy over another's
// change: every field ends at its writer's last value and the counter equals
// the total number of writes. Without the lock this loses counter increments
// and field values immediately (run with -race).
func TestModifyBook_ConcurrentWritersLoseNothing(t *testing.T) {
	s := newAtPathStore(t)
	defer s.Close()

	zero := 0
	b, err := s.CreateBook(&Book{Title: "t", FilePath: "/lock/one", Duration: &zero})
	if err != nil {
		t.Fatal(err)
	}

	setters := []func(*Book, string){
		func(x *Book, v string) { x.Narrator = strp(v) },
		func(x *Book, v string) { x.Publisher = strp(v) },
		func(x *Book, v string) { x.Language = strp(v) },
		func(x *Book, v string) { x.ISBN10 = strp(v) },
		func(x *Book, v string) { x.ISBN13 = strp(v) },
		func(x *Book, v string) { x.ASIN = strp(v) },
		func(x *Book, v string) { x.Codec = strp(v) },
		func(x *Book, v string) { x.MetadataSource = strp(v) },
	}
	getters := []func(*Book) *string{
		func(x *Book) *string { return x.Narrator },
		func(x *Book) *string { return x.Publisher },
		func(x *Book) *string { return x.Language },
		func(x *Book) *string { return x.ISBN10 },
		func(x *Book) *string { return x.ISBN13 },
		func(x *Book) *string { return x.ASIN },
		func(x *Book) *string { return x.Codec },
		func(x *Book) *string { return x.MetadataSource },
	}
	const iters = 25

	withDeadline(t, 60*time.Second, "concurrent ModifyBook writers", func() {
		var wg sync.WaitGroup
		for g := range setters {
			wg.Add(1)
			go func(g int) {
				defer wg.Done()
				for i := 0; i < iters; i++ {
					v := fmt.Sprintf("g%d-%d", g, i)
					if _, err := s.ModifyBook(b.ID, func(row *Book) error {
						setters[g](row, v)
						n := *row.Duration + 1
						row.Duration = &n
						return nil
					}); err != nil {
						t.Error(err)
						return
					}
				}
			}(g)
		}
		wg.Wait()
	})

	row, err := s.GetBookByID(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := len(setters) * iters
	if row.Duration == nil {
		t.Fatalf("counter is nil, want %d: a concurrent write was lost", want)
	}
	if *row.Duration != want {
		t.Fatalf("counter = %d, want %d: a concurrent write was lost", *row.Duration, want)
	}
	for g, get := range getters {
		want := fmt.Sprintf("g%d-%d", g, iters-1)
		if v := get(row); v == nil || *v != want {
			t.Errorf("field %d = %v, want %q: a concurrent write reverted it", g, v, want)
		}
	}
}

// TestModifyBook_MergeKeepsConcurrentWrite is the apply-vs-book-page-save
// shape end to end: writer A reads the row, spends time (the apply's
// provider/author work), and meanwhile writer B commits a different field.
// A then merges only its own change onto the fresh row. B's field survives;
// before this change A's whole-struct UpdateBook reverted it.
func TestModifyBook_MergeKeepsConcurrentWrite(t *testing.T) {
	s := newAtPathStore(t)
	defer s.Close()

	b, err := s.CreateBook(&Book{Title: "old", FilePath: "/lock/merge"})
	if err != nil {
		t.Fatal(err)
	}

	// Writer A reads and snapshots.
	a, err := s.GetBookByID(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	before, err := SnapshotBook(a)
	if err != nil {
		t.Fatal(err)
	}
	a.Title = "applied title"
	a.Narrator = strp("applied narrator")

	// Writer B commits in A's window.
	if _, err := s.ModifyBook(b.ID, func(row *Book) error {
		row.Publisher = strp("user publisher")
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	var changed []string
	got, err := s.ModifyBook(b.ID, func(fresh *Book) error {
		var mErr error
		changed, mErr = MergeBookChanges(fresh, before, a)
		return mErr
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "applied title" || got.Narrator == nil || *got.Narrator != "applied narrator" {
		t.Fatalf("A's fields not written: title=%q narrator=%v", got.Title, got.Narrator)
	}
	if got.Publisher == nil || *got.Publisher != "user publisher" {
		t.Fatalf("B's publisher was reverted by A's merge: %v", got.Publisher)
	}
	if len(changed) != 2 {
		t.Fatalf("MergeBookChanges copied %v, want exactly title and narrator", changed)
	}

	// The control: the same interleaving through whole-struct UpdateBook
	// still loses B's write. The stripe serializes; it cannot un-stale a copy.
	stale, err := s.GetBookByID(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ModifyBook(b.ID, func(row *Book) error {
		row.Language = strp("de")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	stale.Title = "stale writer"
	if _, err := s.UpdateBook(b.ID, stale); err != nil {
		t.Fatal(err)
	}
	row, err := s.GetBookByID(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Language != nil {
		t.Fatalf("control: expected UpdateBook's stale full replace to drop Language, got %q -- if this now passes, update the UpdateBook doc comment", *row.Language)
	}
}

// TestMergeBookChanges_EmptyCollectionIsNotAChange: the caller's read held a
// non-nil EMPTY Authors slice and MetadataProvenance map, and the caller
// changed only the title. SnapshotBook's JSON round trip turns those empties
// into nil (both fields are omitempty), so a JSON-only comparison sees
// "[]" vs "null" and copies the caller's empties over the authors and
// provenance a concurrent writer committed. nil and empty must compare equal.
func TestMergeBookChanges_EmptyCollectionIsNotAChange(t *testing.T) {
	read := &Book{
		ID:                 "b",
		Title:              "old",
		Authors:            []BookAuthor{},
		MetadataProvenance: map[string]MetadataProvenanceEntry{},
	}
	before, err := SnapshotBook(read)
	if err != nil {
		t.Fatal(err)
	}
	read.Title = "new"

	// The row as a concurrent writer left it.
	fresh := &Book{
		ID:                 "b",
		Title:              "old",
		Authors:            []BookAuthor{{BookID: "b", AuthorID: 9, Role: "author"}},
		MetadataProvenance: map[string]MetadataProvenanceEntry{"title": {}},
	}
	changed, err := MergeBookChanges(fresh, before, read)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Title != "new" {
		t.Fatalf("title = %q, want the caller's change", fresh.Title)
	}
	if len(fresh.Authors) != 1 || fresh.Authors[0].AuthorID != 9 {
		t.Fatalf("concurrent writer's authors were cleared: %+v (copied %v)", fresh.Authors, changed)
	}
	if _, ok := fresh.MetadataProvenance["title"]; !ok || len(fresh.MetadataProvenance) != 1 {
		t.Fatalf("concurrent writer's provenance was cleared: %+v (copied %v)", fresh.MetadataProvenance, changed)
	}
	if len(changed) != 1 {
		t.Fatalf("copied %v, want only title", changed)
	}
}

// TestModifyBook_SkipAndMissing pins the two non-write outcomes.
func TestModifyBook_SkipAndMissing(t *testing.T) {
	s := newAtPathStore(t)
	defer s.Close()

	b, err := s.CreateBook(&Book{Title: "t", FilePath: "/lock/skip"})
	if err != nil {
		t.Fatal(err)
	}
	orig, err := s.GetBookByID(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.ModifyBook(b.ID, func(row *Book) error {
		row.Title = "must not persist"
		return ErrSkipBookWrite
	})
	if err != nil || got == nil {
		t.Fatalf("skip: got %v, %v", got, err)
	}
	row, _ := s.GetBookByID(b.ID)
	if row.Title != "t" || !row.UpdatedAt.Equal(*orig.UpdatedAt) {
		t.Fatalf("ErrSkipBookWrite wrote: title=%q updated_at %v -> %v", row.Title, orig.UpdatedAt, row.UpdatedAt)
	}

	called := false
	got, err = s.ModifyBook("no-such-book", func(*Book) error { called = true; return nil })
	if got != nil || err != nil || called {
		t.Fatalf("missing book: got %v, %v, called=%v; want nil, nil, false", got, err, called)
	}
}

// sameStripePair creates books until two land on one stripe and returns them.
// With 256 stripes that takes about 20 books on average.
func sameStripePair(t *testing.T, s *PebbleStore) (string, string) {
	t.Helper()
	seen := map[int]string{}
	for i := 0; i < 2000; i++ {
		b, err := s.CreateBook(&Book{Title: fmt.Sprintf("s%d", i), FilePath: fmt.Sprintf("/lock/stripe/%d", i)})
		if err != nil {
			t.Fatal(err)
		}
		k := stripeFor(b.ID)
		if other, ok := seen[k]; ok {
			return other, b.ID
		}
		seen[k] = b.ID
	}
	t.Fatal("no stripe collision in 2000 books")
	return "", ""
}

// TestBookLock_SameStripeDifferentBooksDoNotDeadlock: two different books on
// ONE stripe, written concurrently through every locked entry point
// (UpdateBook, ModifyBook, FillBookMediaInfo, which nests ModifyBook ->
// updateBookLocked, and UpdateBookRating). They must queue, never hang.
func TestBookLock_SameStripeDifferentBooksDoNotDeadlock(t *testing.T) {
	s := newAtPathStore(t)
	defer s.Close()
	id1, id2 := sameStripePair(t, s)
	if stripeFor(id1) != stripeFor(id2) {
		t.Fatalf("precondition: %s and %s are on different stripes", id1, id2)
	}

	withDeadline(t, 60*time.Second, "same-stripe writers", func() {
		var wg sync.WaitGroup
		for _, id := range []string{id1, id2} {
			for w := 0; w < 3; w++ {
				wg.Add(1)
				go func(id string, w int) {
					defer wg.Done()
					for i := 0; i < 20; i++ {
						var err error
						switch w {
						case 0:
							var row *Book
							if row, err = s.GetBookByID(id); err == nil {
								row.Title = fmt.Sprintf("u%d", i)
								_, err = s.UpdateBook(id, row)
							}
						case 1:
							d := i + 1
							_, err = s.FillBookMediaInfo(id, BookMediaInfoPatch{Duration: &d})
						case 2:
							notes := fmt.Sprintf("n%d", i)
							err = s.UpdateBookRating(id, UpdateBookRatingRequest{Notes: &notes})
						}
						if err != nil {
							t.Error(err)
							return
						}
					}
				}(id, w)
			}
		}
		wg.Wait()
	})
}

// TestBookLock_MoveBookFilesSameStripeDoesNotDeadlock: a chapter merge moves a
// source's files onto the primary (MoveBookFilesToBook recomputes BOTH books'
// aggregates) and then flags the source. With both books on ONE stripe each
// write must release its hold before the next takes it, or it re-enters the
// non-re-entrant mutex and hangs. Concurrent writers on both books run
// alongside to catch an ordering cycle as well. (This pinned
// MergeChapterBooks until that method was removed 2026-09-19.)
func TestBookLock_MoveBookFilesSameStripeDoesNotDeadlock(t *testing.T) {
	s := newAtPathStore(t)
	defer s.Close()
	primary, src := sameStripePair(t, s)
	if stripeFor(primary) != stripeFor(src) {
		t.Fatalf("precondition: %s and %s are on different stripes", primary, src)
	}
	f := &BookFile{BookID: src, FilePath: "/lib/stripe/02 - Book.mp3", Format: "mp3"}
	if err := s.CreateBookFile(f); err != nil {
		t.Fatal(err)
	}

	withDeadline(t, 60*time.Second, "chapter re-parent with primary and source on one stripe", func() {
		var wg sync.WaitGroup
		for _, id := range []string{primary, src} {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				for i := 0; i < 20; i++ {
					notes := fmt.Sprintf("n%d", i)
					if err := s.UpdateBookRating(id, UpdateBookRatingRequest{Notes: &notes}); err != nil {
						t.Error(err)
						return
					}
				}
			}(id)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.MoveBookFilesToBook([]string{f.ID}, src, primary); err != nil {
				t.Error(err)
				return
			}
			if err := s.FlagMetadataHashDuplicate(primary, src); err != nil {
				t.Error(err)
			}
		}()
		wg.Wait()
	})

	files, err := s.GetBookFiles(primary)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].ID != f.ID {
		t.Fatalf("file not re-parented onto the primary: %+v", files)
	}
}

// TestBookLock_AuthorDeleteRepointSameStripeDoesNotDeadlock: two books on ONE
// stripe, each with a different primary author, both authors deleted at the
// same time while other writers hit both books. The repoint writes one book
// per ModifyBook and resolves the successor before taking the stripe, so the
// deletes must queue on the shared stripe, never hang, and each book must end
// on its surviving co-author.
func TestBookLock_AuthorDeleteRepointSameStripeDoesNotDeadlock(t *testing.T) {
	s := newAtPathStore(t)
	defer s.Close()
	id1, id2 := sameStripePair(t, s)
	if stripeFor(id1) != stripeFor(id2) {
		t.Fatalf("precondition: %s and %s are on different stripes", id1, id2)
	}

	mkAuthor := func(name string) *Author {
		a, err := s.CreateAuthor(name)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	gone1, gone2, successor := mkAuthor("Lock Gone One"), mkAuthor("Lock Gone Two"), mkAuthor("Lock Successor")
	for _, pair := range []struct {
		book    string
		primary *Author
	}{{id1, gone1}, {id2, gone2}} {
		pid := pair.primary.ID
		if _, err := s.ModifyBook(pair.book, func(row *Book) error {
			row.AuthorID = &pid
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := s.SetBookAuthors(pair.book, []BookAuthor{
			{BookID: pair.book, AuthorID: pid, Role: "author", Position: 0},
			{BookID: pair.book, AuthorID: successor.ID, Role: "author", Position: 1},
		}); err != nil {
			t.Fatal(err)
		}
	}

	withDeadline(t, 60*time.Second, "concurrent author deletes repointing two books on one stripe", func() {
		var wg sync.WaitGroup
		for _, a := range []*Author{gone1, gone2} {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				if err := s.DeleteAuthor(id); err != nil {
					t.Error(err)
				}
			}(a.ID)
		}
		for _, id := range []string{id1, id2} {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				for i := 0; i < 20; i++ {
					notes := fmt.Sprintf("n%d", i)
					if err := s.UpdateBookRating(id, UpdateBookRatingRequest{Notes: &notes}); err != nil {
						t.Error(err)
						return
					}
				}
			}(id)
		}
		wg.Wait()
	})

	for _, id := range []string{id1, id2} {
		row, err := s.GetBookByID(id)
		if err != nil {
			t.Fatal(err)
		}
		if row.AuthorID == nil || *row.AuthorID != successor.ID {
			t.Fatalf("book %s primary author = %v, want successor %d", id, row.AuthorID, successor.ID)
		}
	}
}

// TestBookLock_DifferentStripesWriteInParallel: while one book's stripe is
// held (a ModifyBook callback parked on a channel), a write to a book on a
// DIFFERENT stripe must go through. If UpdateBook took a store-wide lock this
// would hang until the deadline.
func TestBookLock_DifferentStripesWriteInParallel(t *testing.T) {
	s := newAtPathStore(t)
	defer s.Close()

	a, err := s.CreateBook(&Book{Title: "a", FilePath: "/lock/par/a"})
	if err != nil {
		t.Fatal(err)
	}
	var other *Book
	for i := 0; other == nil; i++ {
		b, err := s.CreateBook(&Book{Title: "b", FilePath: fmt.Sprintf("/lock/par/b%d", i)})
		if err != nil {
			t.Fatal(err)
		}
		if stripeFor(b.ID) != stripeFor(a.ID) {
			other = b
		}
	}

	held := make(chan struct{})
	release := make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		_, err := s.ModifyBook(a.ID, func(row *Book) error {
			close(held)
			<-release
			row.Title = "a2"
			return nil
		})
		holderDone <- err
	}()
	<-held

	withDeadline(t, 10*time.Second, "write to a book on another stripe while one stripe is held", func() {
		if _, err := s.ModifyBook(other.ID, func(row *Book) error {
			row.Title = "b2"
			return nil
		}); err != nil {
			t.Error(err)
		}
	})
	close(release)
	if err := <-holderDone; err != nil {
		t.Fatal(err)
	}
}
