// file: internal/database/book_delete_owns_files_test.go
// version: 1.3.0
// guid: ea8c9be0-11d7-4035-b1ea-a50f919e06cd
// last-edited: 2026-09-19

package database

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// DeleteBook never deletes book_file rows, so deleting a book that owns any
// would orphan them. It must refuse (ErrBookOwnsFiles) and leave the book and
// its rows exactly as they were; once the rows have been moved to another
// book the now-empty shell deletes normally.
func TestDeleteBook_RefusesBookOwningFiles(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	src, err := store.CreateBook(&Book{Title: "Owner", FilePath: "/lib/own/src"})
	if err != nil {
		t.Fatal(err)
	}
	dst, err := store.CreateBook(&Book{Title: "Target", FilePath: "/lib/own/dst"})
	if err != nil {
		t.Fatal(err)
	}
	f := &BookFile{BookID: src.ID, FilePath: "/lib/own/src/01.m4b", FileSize: 1}
	if err := store.CreateBookFile(f); err != nil {
		t.Fatal(err)
	}

	err = store.DeleteBook(src.ID)
	if !errors.Is(err, ErrBookOwnsFiles) {
		t.Fatalf("DeleteBook = %v, want ErrBookOwnsFiles", err)
	}
	if b, _ := store.GetBookByID(src.ID); b == nil {
		t.Fatal("book was deleted despite the refusal")
	}
	if rows, _ := store.GetBookFiles(src.ID); len(rows) != 1 {
		t.Fatalf("rows after refusal = %d, want 1", len(rows))
	}

	if err := store.MoveBookFilesToBook([]string{f.ID}, src.ID, dst.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteBook(src.ID); err != nil {
		t.Fatalf("DeleteBook of the emptied shell: %v", err)
	}
	if b, _ := store.GetBookByID(src.ID); b != nil {
		t.Error("emptied shell survived DeleteBook")
	}
	if rows, _ := store.GetBookFiles(dst.ID); len(rows) != 1 {
		t.Errorf("target rows = %d, want the 1 moved row", len(rows))
	}
}

// assertRowsHaveOwner fails for any book_file row of bookID when bookID has no
// book row. Reads Pebble (GetBookFiles), not memdb.
func assertRowsHaveOwner(t *testing.T, store Store, bookID string) {
	t.Helper()
	rows, err := store.GetBookFiles(bookID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		return
	}
	if b, _ := store.GetBookByID(bookID); b == nil {
		t.Fatalf("orphan: %d book_file row(s) name deleted book %s", len(rows), bookID)
	}
}

// A move INTO a book racing that book's DeleteBook must never leave the moved
// rows under a deleted book. Before the owner stripe, DeleteBook's count and
// the move's commit did not serialize: the count saw zero rows, the move
// committed, and the delete committed over it. Run with -race.
func TestDeleteBook_RacingMoveIntoBookNeverOrphans(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	const rounds = 60
	for i := range rounds {
		src, err := store.CreateBook(&Book{Title: "src", FilePath: fmt.Sprintf("/lib/race/src%d", i)})
		if err != nil {
			t.Fatal(err)
		}
		dst, err := store.CreateBook(&Book{Title: "dst", FilePath: fmt.Sprintf("/lib/race/dst%d", i)})
		if err != nil {
			t.Fatal(err)
		}
		f := &BookFile{BookID: src.ID, FilePath: fmt.Sprintf("/lib/race/src%d/01.m4b", i), FileSize: 1}
		if err := store.CreateBookFile(f); err != nil {
			t.Fatal(err)
		}

		var wg sync.WaitGroup
		var moveErr, delErr error
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			moveErr = store.MoveBookFilesToBook([]string{f.ID}, src.ID, dst.ID)
		}()
		go func() {
			defer wg.Done()
			<-start
			delErr = store.DeleteBook(dst.ID)
		}()
		close(start)
		wg.Wait()

		// Exactly one of the two orders happened, and each is fine:
		//   move first  -> delete refused (ErrBookOwnsFiles), dst keeps the row
		//   delete first -> move refused (ErrBookFileOwnerMissing), src keeps it
		switch {
		case moveErr == nil && delErr == nil:
			t.Fatalf("round %d: both the move and the delete succeeded", i)
		case moveErr != nil && !errors.Is(moveErr, ErrBookFileOwnerMissing):
			t.Fatalf("round %d: move: %v", i, moveErr)
		case delErr != nil && !errors.Is(delErr, ErrBookOwnsFiles):
			t.Fatalf("round %d: delete: %v", i, delErr)
		}
		assertRowsHaveOwner(t, store, dst.ID)
		assertRowsHaveOwner(t, store, src.ID)
	}
}

// Same race for a row CREATED under a book being deleted.
func TestDeleteBook_RacingCreateBookFileNeverOrphans(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	for i := range 60 {
		b, err := store.CreateBook(&Book{Title: "b", FilePath: fmt.Sprintf("/lib/racec/b%d", i)})
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		start := make(chan struct{})
		var createErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			createErr = store.CreateBookFile(&BookFile{BookID: b.ID, FilePath: fmt.Sprintf("/lib/racec/b%d/01.m4b", i)})
		}()
		go func() {
			defer wg.Done()
			<-start
			_ = store.DeleteBook(b.ID)
		}()
		close(start)
		wg.Wait()
		if createErr != nil && !errors.Is(createErr, ErrBookFileOwnerMissing) {
			t.Fatalf("round %d: create: %v", i, createErr)
		}
		assertRowsHaveOwner(t, store, b.ID)
	}
}

// A book_file row cannot be created for a book that has no row.
func TestCreateBookFile_RefusesMissingOwner(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	err := store.CreateBookFile(&BookFile{BookID: "no-such-book", FilePath: "/lib/x.m4b"})
	if !errors.Is(err, ErrBookFileOwnerMissing) {
		t.Fatalf("CreateBookFile = %v, want ErrBookFileOwnerMissing", err)
	}
	if rows, _ := store.GetBookFiles("no-such-book"); len(rows) != 0 {
		t.Fatalf("%d orphan row(s) written", len(rows))
	}
}

// BookFilesAtPath returns every row at a path, including one the
// single-valued book_file_path index no longer names.
func TestBookFilesAtPath_FindsRowsTheSingleIndexLost(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	ps := store.(*PebbleStore)
	ps.WaitForWarmup()

	a, _ := store.CreateBook(&Book{Title: "a", FilePath: "/lib/at/a"})
	b, _ := store.CreateBook(&Book{Title: "b", FilePath: "/lib/at/b"})
	keep := &BookFile{BookID: a.ID, FilePath: "/lib/at/x.m4b"}
	gone := &BookFile{BookID: b.ID, FilePath: "/lib/at/x.m4b"}
	if err := store.CreateBookFile(keep); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBookFile(gone); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteBookFile(gone.ID); err != nil {
		t.Fatal(err)
	}
	if hit, _ := store.GetBookFileByPath("/lib/at/x.m4b"); hit != nil {
		t.Fatalf("fixture is vacuous: single index still names %s", hit.ID)
	}
	rows, err := ps.BookFilesAtPath("/lib/at/x.m4b")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != keep.ID {
		t.Fatalf("rows = %+v, want exactly %s", rows, keep.ID)
	}
}

// One missing owner in a 500-row batch must refuse only its own row: the
// other 499 commit, and the refusal names the row and the book.
func TestBatchWriters_MissingOwnerRefusesOnlyItsRows(t *testing.T) {
	for _, tc := range []struct {
		name  string
		write func(Store, []*BookFile) error
	}{
		{"BatchUpsertBookFiles", func(s Store, f []*BookFile) error { return s.BatchUpsertBookFiles(f) }},
		{"BatchCreateBookFiles", func(s Store, f []*BookFile) error { return s.BatchCreateBookFiles(f) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, cleanup := setupTestDB(t)
			defer cleanup()
			var books []string
			for i := range 299 {
				b, err := store.CreateBook(&Book{Title: "b", FilePath: fmt.Sprintf("/lib/part/%d", i)})
				if err != nil {
					t.Fatal(err)
				}
				books = append(books, b.ID)
			}
			var rows []*BookFile
			for i := range 499 {
				rows = append(rows, &BookFile{BookID: books[i%len(books)], FilePath: fmt.Sprintf("/lib/part/f%d.mp3", i)})
			}
			ghostRow := &BookFile{BookID: "ghost-book", FilePath: "/lib/part/ghost.mp3"}
			rows = append(rows[:250], append([]*BookFile{ghostRow}, rows[250:]...)...)

			err := tc.write(store, rows)
			var refused *BookFileRowsRefusedError
			if !errors.As(err, &refused) {
				t.Fatalf("err = %v, want *BookFileRowsRefusedError", err)
			}
			if !errors.Is(err, ErrBookFileOwnerMissing) {
				t.Errorf("refusal does not wrap ErrBookFileOwnerMissing")
			}
			if refused.Committed != 499 || len(refused.RefusedFileIDs) != 1 || refused.RefusedFileIDs[0] != ghostRow.ID || ghostRow.ID == "" {
				t.Fatalf("refusal = %+v (ghost row id %q), want 499 committed and exactly the ghost row refused", refused, ghostRow.ID)
			}
			if len(refused.MissingBookIDs) != 1 || refused.MissingBookIDs[0] != "ghost-book" {
				t.Errorf("MissingBookIDs = %v", refused.MissingBookIDs)
			}
			total := 0
			for _, id := range books {
				fs, err := store.GetBookFiles(id)
				if err != nil {
					t.Fatal(err)
				}
				total += len(fs)
			}
			if total != 499 {
				t.Errorf("committed rows = %d, want 499", total)
			}
			if fs, _ := store.GetBookFiles("ghost-book"); len(fs) != 0 {
				t.Errorf("%d orphan row(s) written for the missing book", len(fs))
			}
			for _, r := range rows {
				if r.ID == "" {
					t.Fatalf("a committed row's struct was not given its ID")
				}
			}
		})
	}
}

// CreateBookFile's PID transfer rewrites the PRIOR owner's row (clearing the
// PID). If that row is deleted and its book deleted meanwhile, the rewrite
// must not resurrect the row under the deleted book — and the create itself
// must still succeed, re-staged without the transfer.
func TestCreateBookFile_PIDTransferRacingDeleteNeverResurrects(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	for i := range 60 {
		x, _ := store.CreateBook(&Book{Title: "x", FilePath: fmt.Sprintf("/lib/pid/x%d", i)})
		y, _ := store.CreateBook(&Book{Title: "y", FilePath: fmt.Sprintf("/lib/pid/y%d", i)})
		pid := fmt.Sprintf("PID%04d", i)
		prior := &BookFile{BookID: x.ID, FilePath: fmt.Sprintf("/lib/pid/x%d/a.mp3", i), ITunesPersistentID: pid}
		if err := store.CreateBookFile(prior); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		start := make(chan struct{})
		var createErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			createErr = store.CreateBookFile(&BookFile{BookID: y.ID, FilePath: fmt.Sprintf("/lib/pid/y%d/a.mp3", i), ITunesPersistentID: pid})
		}()
		go func() {
			defer wg.Done()
			<-start
			if err := store.DeleteBookFile(prior.ID); err == nil {
				_ = store.DeleteBook(x.ID)
			}
		}()
		close(start)
		wg.Wait()
		if createErr != nil {
			t.Fatalf("round %d: create failed: %v", i, createErr)
		}
		assertRowsHaveOwner(t, store, x.ID)
	}
}

// A move racing the deletion of the row it moves must not recreate the row
// under the target: DeleteBookFile wins whichever commits first.
func TestMoveBookFiles_RacingDeleteBookFileNeverResurrects(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	for i := range 60 {
		src, _ := store.CreateBook(&Book{Title: "s", FilePath: fmt.Sprintf("/lib/mvd/s%d", i)})
		dst, _ := store.CreateBook(&Book{Title: "d", FilePath: fmt.Sprintf("/lib/mvd/d%d", i)})
		f := &BookFile{BookID: src.ID, FilePath: fmt.Sprintf("/lib/mvd/s%d/a.mp3", i)}
		if err := store.CreateBookFile(f); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		start := make(chan struct{})
		var delErr error
		wg.Add(2)
		go func() { defer wg.Done(); <-start; _ = store.MoveBookFilesToBook([]string{f.ID}, src.ID, dst.ID) }()
		go func() { defer wg.Done(); <-start; delErr = store.DeleteBookFile(f.ID) }()
		close(start)
		wg.Wait()
		if delErr != nil {
			continue // the delete did not happen; nothing to assert
		}
		for _, b := range []string{src.ID, dst.ID} {
			if rows, _ := store.GetBookFiles(b); len(rows) != 0 {
				t.Fatalf("round %d: deleted row %s resurrected under %s", i, f.ID, b)
			}
		}
	}
}

// Two concurrent moves of one row to different books must leave it under
// exactly one of them, never both.
func TestMoveBookFiles_ConcurrentMovesNeverDuplicateARow(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	for i := range 60 {
		src, _ := store.CreateBook(&Book{Title: "s", FilePath: fmt.Sprintf("/lib/mv2/s%d", i)})
		t1, _ := store.CreateBook(&Book{Title: "t1", FilePath: fmt.Sprintf("/lib/mv2/t1-%d", i)})
		t2, _ := store.CreateBook(&Book{Title: "t2", FilePath: fmt.Sprintf("/lib/mv2/t2-%d", i)})
		f := &BookFile{BookID: src.ID, FilePath: fmt.Sprintf("/lib/mv2/s%d/a.mp3", i)}
		if err := store.CreateBookFile(f); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		start := make(chan struct{})
		wg.Add(2)
		for _, tgt := range []string{t1.ID, t2.ID} {
			go func() { defer wg.Done(); <-start; _ = store.MoveBookFilesToBook([]string{f.ID}, src.ID, tgt) }()
		}
		close(start)
		wg.Wait()
		n := 0
		for _, b := range []string{src.ID, t1.ID, t2.ID} {
			rows, _ := store.GetBookFiles(b)
			n += len(rows)
		}
		if n != 1 {
			t.Fatalf("round %d: row %s exists %d times after two concurrent moves", i, f.ID, n)
		}
	}
}

// The owner stripes are released before the WAL fsync. With a 500-row batch
// over 300 books parked in its fsync, a single-book writer on one of those
// books must still complete — it used to wait for the whole fsync.
func TestBatchWrite_DoesNotHoldOwnerStripesAcrossFsync(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	var books []string
	for i := range 300 {
		b, err := store.CreateBook(&Book{Title: "b", FilePath: fmt.Sprintf("/lib/fs/%d", i)})
		if err != nil {
			t.Fatal(err)
		}
		books = append(books, b.ID)
	}
	var rows []*BookFile
	for i := range 500 {
		rows = append(rows, &BookFile{BookID: books[i%300], FilePath: fmt.Sprintf("/lib/fs/f%d.mp3", i)})
	}

	parked := make(chan struct{})
	release := make(chan struct{})
	var first atomic.Bool
	// Parks ONLY the first caller (the batch). Not sync.Once: Once blocks
	// every concurrent caller until the first returns, which would park the
	// single-book writer too and prove nothing.
	bookFileWALSyncHook = func() error {
		if first.CompareAndSwap(false, true) {
			close(parked)
			<-release
		}
		return nil
	}
	t.Cleanup(func() { bookFileWALSyncHook = nil })

	batchDone := make(chan error, 1)
	go func() { batchDone <- store.BatchUpsertBookFiles(rows) }()
	<-parked // the batch has applied and is now in its fsync
	var released sync.Once
	defer func() {
		released.Do(func() { close(release) })
		<-batchDone
	}()

	single := make(chan error, 1)
	go func() {
		single <- store.CreateBookFile(&BookFile{BookID: books[7], FilePath: "/lib/fs/single.mp3"})
	}()
	select {
	case err := <-single:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a single-book writer was blocked by a batch sitting in its fsync")
	}
	released.Do(func() { close(release) })
	if err := <-batchDone; err != nil {
		t.Fatal(err)
	}
	batchDone <- nil // let the deferred drain return
}

// A read error while re-resolving a row during DeleteBookFile's retry must be
// reported, not taken as "someone else deleted it".
func TestDeleteBookFile_ReadErrorDuringRetryIsNotSuccess(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	ps := store.(*PebbleStore)
	b, _ := store.CreateBook(&Book{Title: "b", FilePath: "/lib/rerr/b"})
	f := &BookFile{BookID: b.ID, FilePath: "/lib/rerr/b/a.mp3"}
	if err := store.CreateBookFile(f); err != nil {
		t.Fatal(err)
	}
	// A stale location (as a concurrent move leaves it): the first delete
	// finds nothing at it and must re-resolve the row by id.
	stale := *f
	stale.BookID = "moved-away"
	boom := errors.New("injected read failure")
	bookFileIDGetHook = func([]byte) error { return boom }
	t.Cleanup(func() { bookFileIDGetHook = nil })

	err := ps.deleteBookFileFrom(f.ID, &stale)
	if !errors.Is(err, boom) {
		t.Fatalf("deleteBookFileFrom = %v, want the read error surfaced", err)
	}
	bookFileIDGetHook = nil
	if rows, _ := store.GetBookFiles(b.ID); len(rows) != 1 {
		t.Fatalf("rows = %d, want the row still present", len(rows))
	}
}

// When the WAL fsync fails the batch was still APPLIED: its rows are visible.
// The writer must say so (ErrBookFileDurabilityUnknown), give the caller's
// structs their IDs, and refresh memdb — not report "nothing committed".
func TestBatchUpsert_SyncFailureIsAppliedDurabilityUnknown(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	ps := store.(*PebbleStore)
	ps.WaitForWarmup()
	b, _ := store.CreateBook(&Book{Title: "b", FilePath: "/lib/sync/b"})
	rows := []*BookFile{{BookID: b.ID, FilePath: "/lib/sync/b/1.mp3"}, {BookID: b.ID, FilePath: "/lib/sync/b/2.mp3"}}
	bookFileWALSyncHook = func() error { return errors.New("injected fsync failure") }
	t.Cleanup(func() { bookFileWALSyncHook = nil })

	err := store.BatchUpsertBookFiles(rows)
	bookFileWALSyncHook = nil
	if !errors.Is(err, ErrBookFileDurabilityUnknown) {
		t.Fatalf("err = %v, want ErrBookFileDurabilityUnknown", err)
	}
	for _, r := range rows {
		if r.ID == "" {
			t.Fatal("an applied row's struct was not given its ID")
		}
	}
	got, _ := store.GetBookFiles(b.ID)
	if len(got) != 2 {
		t.Fatalf("visible rows = %d, want 2", len(got))
	}
	core, err := store.GetAllBookFilesCore()
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, c := range core {
		if c.BookID == b.ID {
			seen++
		}
	}
	if seen != 2 {
		t.Fatalf("memdb holds %d of the 2 applied rows", seen)
	}
}

// Sustained churn on ONE matched row (deleted and recreated before every
// commit) must refuse that row alone; the other rows of the batch commit.
func TestBatchUpsert_ChurningRowRefusedOthersCommit(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()
	b, _ := store.CreateBook(&Book{Title: "b", FilePath: "/lib/churn/b"})
	const hot = "/lib/churn/b/hot.mp3"
	if err := store.CreateBookFile(&BookFile{BookID: b.ID, FilePath: hot}); err != nil {
		t.Fatal(err)
	}
	inHook := false
	bookFileBeforeCommitHook = func() {
		if inHook {
			return
		}
		inHook = true
		defer func() { inHook = false }()
		if cur, _ := store.GetBookFileByPath(hot); cur != nil {
			_ = store.DeleteBookFile(cur.ID)
		}
		_ = store.CreateBookFile(&BookFile{BookID: b.ID, FilePath: hot})
	}
	t.Cleanup(func() { bookFileBeforeCommitHook = nil })

	rows := []*BookFile{{BookID: b.ID, FilePath: hot}}
	for i := range 10 {
		rows = append(rows, &BookFile{BookID: b.ID, FilePath: fmt.Sprintf("/lib/churn/b/%02d.mp3", i)})
	}
	err := store.BatchUpsertBookFiles(rows)
	bookFileBeforeCommitHook = nil
	var refused *BookFileRowsRefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("err = %v, want *BookFileRowsRefusedError", err)
	}
	if refused.Committed != 10 || len(refused.RefusedFileIDs) != 1 {
		t.Fatalf("refusal = %+v, want 10 committed and the churning row refused", refused)
	}
	if refused.Reasons[refused.RefusedFileIDs[0]] == "" {
		t.Errorf("refused row has no reason: %+v", refused)
	}
	for i := range 10 {
		if r, _ := store.GetBookFileByPath(fmt.Sprintf("/lib/churn/b/%02d.mp3", i)); r == nil {
			t.Fatalf("row %d was not committed", i)
		}
	}
}
