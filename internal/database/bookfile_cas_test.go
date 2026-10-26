// file: internal/database/bookfile_cas_test.go
// version: 1.6.0
// guid: 0f3c52d8-7e19-4b64-a2d0-91c8b5e46a17
// last-edited: 2026-10-26

package database

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// The compare-and-swap in commitBookFileBatch. Every test here injects a
// concurrent writer through bookFileBeforeCommitHook, which runs after the
// victim has READ its row and before it takes the owner stripes: the exact
// window in which a writer that takes no file stripe used to be reverted.
//
// The injected writer must never take the victim's file stripe: it runs on the
// victim's goroutine while the victim holds that stripe, so it would deadlock.
// That rules out the single-row writers (ModifyBookFile, UpdateBookFile,
// SetBookFileHash, PatchBookFileFields, the intro writers, UpsertBookFile) and,
// since the batch upsert began taking file stripes, BatchUpsertBookFiles and the
// retire writers as well. The writers that still hold no file stripe are the PID
// transfer in CreateBookFile/BatchCreateBookFiles, the bulk moves, the deletes,
// the sweeps and the scan-cache stamp. strayWriteRow stands in for them in the
// tests that need an arbitrary field change: it writes through
// updateBookFileOver, the same commit path they use, and takes no file stripe.
// The hook is guarded against re-entry because the injected writer commits
// through the same hook. The delete paths run the hook while holding the row's
// fingerprint window stripes too (commitDeleteWithWindowCascade), so a writer
// injected into a delete must not take those either: a window write
// (ReplaceFingerprintWindows) does.

type casFixture struct {
	s    *PebbleStore
	book *Book
	file BookFile
}

const (
	casPath = "/lib/Gotham/Batman-Year-One/01.m4b"
	casPID  = "BATMAN-YEAR-ONE-01"
)

// newCASFixture creates "Batman: Year One" with one file (path casPath, PID
// casPID, hash "gotham-hash-old", 100 s, 1000 bytes).
func newCASFixture(t *testing.T) *casFixture {
	t.Helper()
	s := openRescanStore(t)
	recordRestageSleeps(t)
	book, err := s.CreateBook(&Book{Title: "Batman: Year One", FilePath: "/lib/Gotham/Batman-Year-One"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBookFile(&BookFile{
		BookID: book.ID, FilePath: casPath, ITunesPersistentID: casPID,
		FileHash: "gotham-hash-old", Duration: 100, FileSize: 1000,
	}); err != nil {
		t.Fatal(err)
	}
	return &casFixture{s: s, book: book, file: onlyBookFile(t, s, book.ID)}
}

// injectBeforeCommit runs inject before each commit of the victim write, up
// to times times (0 = every commit), guarded against re-entry. It returns the
// number of times inject ran.
func injectBeforeCommit(t *testing.T, times int, inject func(n int)) *int {
	t.Helper()
	n, inHook := 0, false
	bookFileBeforeCommitHook = func() {
		if inHook || (times > 0 && n >= times) {
			return
		}
		inHook = true
		defer func() { inHook = false }()
		n++
		inject(n)
	}
	t.Cleanup(func() { bookFileBeforeCommitHook = nil })
	return &n
}

// recordRestageSleeps replaces the wait between whole-batch staging rounds with
// a recorder, so tests neither sleep nor lose sight of the intervals chosen.
func recordRestageSleeps(t *testing.T) *[]time.Duration {
	t.Helper()
	var sleeps []time.Duration
	bookFileRestageSleep = func(d time.Duration) { sleeps = append(sleeps, d) }
	t.Cleanup(func() { bookFileRestageSleep = time.Sleep })
	return &sleeps
}

func (f *casFixture) row(t *testing.T) *BookFile {
	t.Helper()
	got, err := f.s.GetBookFileByID(f.book.ID, f.file.ID)
	if err != nil || got == nil {
		t.Fatalf("GetBookFileByID: row=%v err=%v", got, err)
	}
	return got
}

// indexRef is the value stored at an index key ("" when absent).
func (f *casFixture) indexRef(t *testing.T, key string) string {
	t.Helper()
	v, closer, err := f.s.db.Get([]byte(key))
	if err != nil {
		return ""
	}
	defer closer.Close()
	return string(v)
}

func (f *casFixture) ref() string { return f.book.ID + ":" + f.file.ID }

// upsertScan is the scanner's batch upsert of this file's path with a new hash,
// duration and size. It takes the row's file stripe, so it is a VICTIM here (or a
// writer run between rounds), never an injected writer: use strayScan for that.
func (f *casFixture) upsertScan(t *testing.T, hash string, duration int, size int64) {
	t.Helper()
	err := f.s.BatchUpsertBookFiles([]*BookFile{{
		BookID: f.book.ID, FilePath: casPath, FileHash: hash, Duration: duration, FileSize: size,
	}})
	if err != nil {
		t.Fatalf("injected BatchUpsertBookFiles: %v", err)
	}
}

// strayWriteRow is the injected writer: a change to one row made by a path that
// holds no file stripe (see the header comment). It reads the stored row, applies
// mutate and commits through updateBookFileOver, which compares the row it staged
// from like every other writer on commitBookFileBatch. Safe to call from inside
// the hook: it takes no lock the victim holds, and the hook guards re-entry.
func strayWriteRow(t *testing.T, s *PebbleStore, bookID, fileID string, mutate func(*BookFile)) {
	t.Helper()
	old, raw, err := s.loadBookFileRowAt(bookFileKey(bookID, fileID), true)
	if err != nil || old == nil {
		t.Errorf("strayWriteRow: load %s/%s: row=%v err=%v", bookID, fileID, old, err)
		return
	}
	next := cloneBookFile(old)
	mutate(&next)
	if _, err := s.updateBookFileOver(old, raw, &next, bookFileMergeReplace, false); err != nil {
		t.Errorf("strayWriteRow: %v", err)
	}
}

// strayWriteByPath is strayWriteRow for the row currently stored at path.
func strayWriteByPath(t *testing.T, s *PebbleStore, path string, mutate func(*BookFile)) {
	t.Helper()
	row, err := s.GetBookFileByPath(path)
	if err != nil || row == nil {
		t.Errorf("strayWriteByPath: no row at %s: row=%v err=%v", path, row, err)
		return
	}
	strayWriteRow(t, s, row.BookID, row.ID, mutate)
}

// strayWrite changes the fixture's file.
func (f *casFixture) strayWrite(t *testing.T, mutate func(*BookFile)) {
	t.Helper()
	strayWriteRow(t, f.s, f.book.ID, f.file.ID, mutate)
}

// strayScan is what a scan's re-hash looks like to the victim: a new hash,
// duration and size on the fixture's file, written without a file stripe.
func (f *casFixture) strayScan(t *testing.T, hash string, duration int, size int64) {
	t.Helper()
	f.strayWrite(t, func(r *BookFile) { r.FileHash, r.Duration, r.FileSize = hash, duration, size })
}

// Victim SetBookFileHash, concurrent writer: a create that claims the file's
// iTunes ID. The PID transfer clears the PID from the victim's row and points
// book_file_pid at the new row; the hash write must not put the PID back.
func TestCAS_SetBookFileHash_DoesNotRestoreATransferredPID(t *testing.T) {
	f := newCASFixture(t)
	const newPath = "/lib/Gotham/Batman-Year-One/02.m4b"
	injectBeforeCommit(t, 1, func(int) {
		if err := f.s.CreateBookFile(&BookFile{BookID: f.book.ID, FilePath: newPath, ITunesPersistentID: casPID}); err != nil {
			t.Errorf("injected CreateBookFile: %v", err)
		}
	})

	if err := f.s.SetBookFileHash(f.file.ID, "gotham-hash-new"); err != nil {
		t.Fatalf("SetBookFileHash: %v", err)
	}

	got := f.row(t)
	if got.ITunesPersistentID != "" {
		t.Errorf("PID %q was written back onto the old row: two rows now claim it", got.ITunesPersistentID)
	}
	if got.FileHash != "gotham-hash-new" {
		t.Errorf("FileHash = %q, want the hash write to be applied", got.FileHash)
	}
	owner, err := f.s.GetBookFileByPID(casPID)
	if err != nil || owner == nil || owner.FilePath != newPath {
		t.Errorf("book_file_pid:%s resolves to %+v (err %v), want the new row at %s", casPID, owner, err, newPath)
	}
	if ref := f.indexRef(t, "book_file_hash:gotham-hash-new"); ref != f.ref() {
		t.Errorf("hash index = %q, want %q", ref, f.ref())
	}
}

// Victim ModifyBookFile, concurrent writer: a batch upsert that changes the
// file's hash, duration and size. Its fields and its index keys must survive,
// the victim's change must be applied on top, and fn must have been re-run on
// the fresh row.
func TestCAS_ModifyBookFile_KeepsConcurrentBatchUpsert(t *testing.T) {
	f := newCASFixture(t)
	injectBeforeCommit(t, 1, func(int) { f.strayScan(t, "gotham-hash-scan", 250, 2500) })

	var seenHashes []string
	got, err := f.s.ModifyBookFile(f.book.ID, f.file.ID, func(r *BookFile) error {
		seenHashes = append(seenHashes, r.FileHash)
		r.Title = "Batman: Year One, Chapter 1"
		return nil
	})
	if err != nil {
		t.Fatalf("ModifyBookFile: %v", err)
	}

	if len(seenHashes) != 2 || seenHashes[0] != "gotham-hash-old" || seenHashes[1] != "gotham-hash-scan" {
		t.Errorf("fn saw hashes %v, want it re-run once on the fresh row: [gotham-hash-old gotham-hash-scan]", seenHashes)
	}
	row := f.row(t)
	if row.FileHash != "gotham-hash-scan" || row.Duration != 250 || row.FileSize != 2500 {
		t.Errorf("row = hash %q duration %d size %d, want the concurrent upsert's gotham-hash-scan/250/2500 kept",
			row.FileHash, row.Duration, row.FileSize)
	}
	if row.Title != "Batman: Year One, Chapter 1" || got.Title != row.Title {
		t.Errorf("title = %q (returned %q), want the caller's change applied", row.Title, got.Title)
	}
	if ref := f.indexRef(t, "book_file_hash:gotham-hash-scan"); ref != f.ref() {
		t.Errorf("the upsert's hash index key = %q, want %q (it was stranded)", ref, f.ref())
	}
	if ref := f.indexRef(t, "book_file_hash:gotham-hash-old"); ref != "" {
		t.Errorf("the stale hash index key still points at %q", ref)
	}
}

// Victim UpdateBookFile: the caller's whole-row write is merged against the
// row as it is at commit, and the indexes are maintained from that row.
func TestCAS_UpdateBookFile_ReMergesAgainstTheFreshRow(t *testing.T) {
	f := newCASFixture(t)
	fp := []byte{1, 2, 3, 4}
	injectBeforeCommit(t, 1, func(int) {
		// The concurrent writer (one that holds no file stripe) records a
		// fingerprint; a slim whole-row caller must not blank it (bfPreserveAlways
		// restores it from the fresh row).
		f.strayWrite(t, func(r *BookFile) { r.AcoustIDFingerprint = fp })
	})

	slim := f.file
	slim.Title = "Batman: Year One, Chapter 2"
	if err := f.s.UpdateBookFile(f.file.ID, &slim); err != nil {
		t.Fatalf("UpdateBookFile: %v", err)
	}

	row := f.row(t)
	if string(row.AcoustIDFingerprint) != string(fp) {
		t.Errorf("fingerprint = %v, want the concurrent writer's %v kept", row.AcoustIDFingerprint, fp)
	}
	if row.Title != "Batman: Year One, Chapter 2" {
		t.Errorf("title = %q, want the caller's change", row.Title)
	}
}

// Victim UpsertBookFile: the upsert merge runs against the row at commit, so a
// field a concurrent writer set is kept when the upsert row leaves it zero.
func TestCAS_UpsertBookFile_MergesAgainstTheFreshRow(t *testing.T) {
	f := newCASFixture(t)
	injectBeforeCommit(t, 1, func(int) { f.strayScan(t, "gotham-hash-old", 321, 1000) })

	if err := f.s.UpsertBookFile(&BookFile{BookID: f.book.ID, FilePath: casPath, Title: "Batman: Year One, Chapter 3"}); err != nil {
		t.Fatalf("UpsertBookFile: %v", err)
	}
	row := f.row(t)
	if row.Duration != 321 {
		t.Errorf("duration = %d, want 321: the upsert merged against the row it read, not the row it replaced", row.Duration)
	}
	if row.Title != "Batman: Year One, Chapter 3" {
		t.Errorf("title = %q, want the upsert's", row.Title)
	}
}

// Converse: the victim is a batch upsert and the concurrent writer changes the
// title without taking the file stripe the upsert holds (ModifyBookFile cannot be
// injected: it would wait on that stripe). The upsert's staged value came from a
// row that no longer exists; it must be re-staged on the changed row.
func TestCAS_BatchUpsert_KeepsConcurrentModifyBookFile(t *testing.T) {
	f := newCASFixture(t)
	injectBeforeCommit(t, 1, func(int) {
		f.strayWrite(t, func(r *BookFile) { r.Title = "Batman: Year One, Chapter 4" })
	})

	f.upsertScan(t, "gotham-hash-old", 400, 1000)

	row := f.row(t)
	if row.Title != "Batman: Year One, Chapter 4" {
		t.Errorf("title = %q, want the concurrent ModifyBookFile's kept (an upsert row leaves it zero)", row.Title)
	}
	if row.Duration != 400 {
		t.Errorf("duration = %d, want the upsert's 400", row.Duration)
	}
}

// A create claiming the PID inside a batch upsert's window: the batch's stale
// copy of the prior owner must not be written back.
func TestCAS_PIDTransferPriorOwner_NotRevertedByStaleRead(t *testing.T) {
	f := newCASFixture(t)
	// Victim: CreateBookFile of a new row claiming casPID; it reads the prior
	// owner (the fixture row), then a concurrent scan upsert changes it.
	injectBeforeCommit(t, 1, func(int) { f.strayScan(t, "gotham-hash-scan", 250, 2500) })

	newPath := "/lib/Gotham/Batman-Year-One/02.m4b"
	if err := f.s.CreateBookFile(&BookFile{BookID: f.book.ID, FilePath: newPath, ITunesPersistentID: casPID}); err != nil {
		t.Fatalf("CreateBookFile: %v", err)
	}

	row := f.row(t)
	if row.FileHash != "gotham-hash-scan" || row.Duration != 250 {
		t.Errorf("prior owner = hash %q duration %d, want the scan's gotham-hash-scan/250 kept", row.FileHash, row.Duration)
	}
	if row.ITunesPersistentID != "" {
		t.Errorf("prior owner kept PID %q after the transfer", row.ITunesPersistentID)
	}
	if ref := f.indexRef(t, "book_file_hash:gotham-hash-scan"); ref != f.ref() {
		t.Errorf("scan's hash index key = %q, want %q", ref, f.ref())
	}
	owner, _ := f.s.GetBookFileByPID(casPID)
	if owner == nil || owner.FilePath != newPath {
		t.Errorf("PID owner = %+v, want the new row", owner)
	}
}

// Moves read the source row before the stripes: a concurrent in-place write
// must not be lost to a move that carries the stale content to the new key.
func TestCAS_MoveBookFiles_CarriesTheFreshRow(t *testing.T) {
	f := newCASFixture(t)
	dst, err := f.s.CreateBook(&Book{Title: "Batman: The Dark Knight Returns", FilePath: "/lib/Gotham/Dark-Knight"})
	if err != nil {
		t.Fatal(err)
	}
	injectBeforeCommit(t, 1, func(int) { f.strayScan(t, "gotham-hash-scan", 250, 2500) })

	if err := f.s.MoveBookFilesToBook([]string{f.file.ID}, f.book.ID, dst.ID); err != nil {
		t.Fatalf("MoveBookFilesToBook: %v", err)
	}
	moved, err := f.s.GetBookFileByID(dst.ID, f.file.ID)
	if err != nil || moved == nil {
		t.Fatalf("moved row: %v %v", moved, err)
	}
	if moved.FileHash != "gotham-hash-scan" || moved.Duration != 250 {
		t.Errorf("moved row = hash %q duration %d, want the scan's change carried", moved.FileHash, moved.Duration)
	}
	if ref := f.indexRef(t, "book_file_hash:gotham-hash-scan"); ref != dst.ID+":"+f.file.ID {
		t.Errorf("hash index = %q, want it under the new owner", ref)
	}
	if ref := f.indexRef(t, "book_file_hash:gotham-hash-old"); ref != "" {
		t.Errorf("stale hash index key still points at %q", ref)
	}
}

// PatchBookFileFields re-applies the patch, and re-checks its If*
// preconditions, on the fresh row.
func TestCAS_PatchBookFileFields_ReAppliesOnTheFreshRow(t *testing.T) {
	f := newCASFixture(t)
	injectBeforeCommit(t, 1, func(int) { f.strayScan(t, "gotham-hash-scan", 250, 2500) })

	track := 7
	before, after, err := f.s.PatchBookFileFields(f.book.ID, f.file.ID, BookFileFieldPatch{TrackNumber: &track})
	if err != nil {
		t.Fatalf("PatchBookFileFields: %v", err)
	}
	row := f.row(t)
	if row.TrackNumber != 7 || row.Duration != 250 || row.FileHash != "gotham-hash-scan" {
		t.Errorf("row = track %d duration %d hash %q, want 7/250/gotham-hash-scan", row.TrackNumber, row.Duration, row.FileHash)
	}
	if before == nil || before.Duration != 250 || after == nil || after.TrackNumber != 7 {
		t.Errorf("before/after = %+v / %+v, want the fresh read's", before, after)
	}
}

// SetBookFileIntro decides from the row it read. If the file's bytes changed
// (a new FileHash) before the commit, the transcript describes bytes the row no
// longer holds and must be dropped, not stamped onto the new hash.
func TestCAS_SetBookFileIntro_RedecidesOnTheFreshRow(t *testing.T) {
	f := newCASFixture(t)
	injectBeforeCommit(t, 1, func(int) { f.strayScan(t, "gotham-hash-scan", 250, 2500) })

	text := "This is Gotham."
	res, err := f.s.SetBookFileIntro(f.book.ID, f.file.ID, BookFileIntroResult{
		SourceHash: "gotham-hash-old", RunOpID: "op-batman", Status: IntroStatusOK, Transcript: &text, Done: true,
	})
	if err != nil {
		t.Fatalf("SetBookFileIntro: %v", err)
	}
	if res != IntroWriteStale {
		t.Errorf("result = %v, want IntroWriteStale: the file's bytes changed before the commit", res)
	}
	if row := f.row(t); row.IntroTranscription != nil || row.FileHash != "gotham-hash-scan" {
		t.Errorf("row = transcript %v hash %q, want no transcript and the scan's hash", row.IntroTranscription, row.FileHash)
	}
}

// The scan-cache stamp is re-applied to the fresh row.
func TestCAS_StampFileScanCache_KeepsConcurrentWrite(t *testing.T) {
	f := newCASFixture(t)
	injectBeforeCommit(t, 1, func(int) { f.strayScan(t, "gotham-hash-scan", 250, 2500) })

	calls := 0
	if err := f.s.stampFileScanCache(f.book.ID, f.file.ID, func(r *BookFile) {
		calls++
		no := false
		r.NeedsRescan = &no
	}); err != nil {
		t.Fatalf("stampFileScanCache: %v", err)
	}
	row := f.row(t)
	if calls != 2 || row.Duration != 250 || row.NeedsRescan == nil {
		t.Errorf("calls=%d duration=%d needsRescan=%v, want 2 / 250 / set", calls, row.Duration, row.NeedsRescan)
	}
}

// A writer that is refused on every attempt gives up with the typed conflict
// error and writes nothing.
func TestCAS_RetryExhaustion_ReturnsTypedConflict(t *testing.T) {
	cases := map[string]func(f *casFixture) error{
		"SetBookFileHash": func(f *casFixture) error { return f.s.SetBookFileHash(f.file.ID, "gotham-hash-new") },
		"ModifyBookFile": func(f *casFixture) error {
			_, err := f.s.ModifyBookFile(f.book.ID, f.file.ID, func(r *BookFile) error { r.Title = "Batman: Hush"; return nil })
			return err
		},
		"UpdateBookFile": func(f *casFixture) error {
			row := f.file
			row.Title = "Batman: Hush"
			return f.s.UpdateBookFile(f.file.ID, &row)
		},
		"UpdateBookFileHashes": func(f *casFixture) error { return f.s.UpdateBookFileHashes(f.file.ID, "", "", "gotham-hash-new") },
		"PatchBookFileFields": func(f *casFixture) error {
			track := 9
			_, _, err := f.s.PatchBookFileFields(f.book.ID, f.file.ID, BookFileFieldPatch{TrackNumber: &track})
			return err
		},
	}
	for name, victim := range cases {
		t.Run(name, func(t *testing.T) {
			f := newCASFixture(t)
			// Every commit of the victim is preceded by a fresh concurrent change.
			n := injectBeforeCommit(t, 0, func(i int) { f.strayScan(t, fmt.Sprintf("gotham-hash-%d", i), 100+i, 1000) })

			err := victim(f)
			if !errors.Is(err, ErrBookFileConflict) {
				t.Fatalf("err = %v, want ErrBookFileConflict", err)
			}
			conflict, ok := errors.AsType[*BookFileConflictError](err)
			if !ok || conflict.FileID != f.file.ID || conflict.BookID != f.book.ID || conflict.Attempts != maxStageRetries {
				t.Fatalf("conflict = %+v, want file %s book %s after %d attempts", conflict, f.file.ID, f.book.ID, maxStageRetries)
			}
			if *n != maxStageRetries {
				t.Errorf("concurrent writer ran %d times, want one per attempt (%d)", *n, maxStageRetries)
			}
			row := f.row(t)
			if row.Title != "" || row.TrackNumber != 0 || row.FileHash == "gotham-hash-new" {
				t.Errorf("row = title %q track %d hash %q: the refused write must leave nothing behind", row.Title, row.TrackNumber, row.FileHash)
			}
			if want := fmt.Sprintf("gotham-hash-%d", maxStageRetries); row.FileHash != want {
				t.Errorf("hash = %q, want the last concurrent writer's %q", row.FileHash, want)
			}
		})
	}
}

// A conflict on a create (its prior PID owner kept changing) is also countable
// with errors.Is(err, ErrBookFileConflict).
func TestCAS_CreateBookFile_ExhaustionIsCountable(t *testing.T) {
	f := newCASFixture(t)
	injectBeforeCommit(t, 0, func(i int) { f.strayScan(t, fmt.Sprintf("gotham-hash-%d", i), 100+i, 1000) })

	err := f.s.CreateBookFile(&BookFile{BookID: f.book.ID, FilePath: "/lib/Gotham/Batman-Year-One/02.m4b", ITunesPersistentID: casPID})
	if !errors.Is(err, ErrBookFileConflict) {
		t.Fatalf("err = %v, want ErrBookFileConflict", err)
	}
	if row := f.row(t); row.ITunesPersistentID != casPID {
		t.Errorf("a refused create must not strip the prior owner's PID; got %q", row.ITunesPersistentID)
	}
}

// ModifyBookFile honours ErrSkipBookFileWrite on a re-run.
func TestCAS_ModifyBookFile_SkipOnRetry(t *testing.T) {
	f := newCASFixture(t)
	injectBeforeCommit(t, 1, func(int) { f.strayScan(t, "gotham-hash-scan", 250, 2500) })

	calls := 0
	got, err := f.s.ModifyBookFile(f.book.ID, f.file.ID, func(r *BookFile) error {
		calls++
		if r.Duration == 250 { // already what we wanted: nothing to change
			return ErrSkipBookFileWrite
		}
		r.Duration = 250
		return nil
	})
	if err != nil || got == nil || calls != 2 {
		t.Fatalf("got=%v err=%v calls=%d, want a skipped re-run", got, err, calls)
	}
	if row := f.row(t); row.FileHash != "gotham-hash-scan" || row.Duration != 250 {
		t.Errorf("row = %q/%d, want the concurrent upsert left alone", row.FileHash, row.Duration)
	}
}

// The segment sweep read every row up front; a row a scan rewrote since is
// re-read and swept from its CURRENT bytes, not put back as it was.
func TestCAS_SweepBookFileSegDrop_KeepsConcurrentWrite(t *testing.T) {
	s, book, row := newSegSweepFixture(t, "01HUSH0000000000000000000A", "01.m4b", "AQADtAcSRY")
	n := injectBeforeCommit(t, 1, func(int) { setDuration(t, s, row, 250) })

	res, err := s.SweepBookFileSegDrop(context.Background(), false, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	if *n != 1 || res.Rewrite != 1 || res.Conflicts != 0 {
		t.Errorf("injected=%d result=%+v, want 1 rewrite after one refused round", *n, res)
	}
	got, err := s.GetBookFileByID(book.ID, row.ID)
	if err != nil || got == nil {
		t.Fatalf("row: %v %v", got, err)
	}
	if got.Duration != 250 {
		t.Errorf("duration = %d, want the concurrent write's 250 (the sweep put the snapshot back)", got.Duration)
	}
	if got.AcoustIDSeg0 != "" {
		t.Errorf("seg0 = %q, want it dropped", got.AcoustIDSeg0)
	}
}

// Sustained churn: the sweep gives up on the row, counts it, and leaves it as
// stored.
func TestCAS_SweepBookFileSegDrop_CountsRowsThatKeepChanging(t *testing.T) {
	s, book, row := newSegSweepFixture(t, "01HUSH0000000000000000000B", "02.m4b", "AQADtAcSRZ")
	injectBeforeCommit(t, 0, func(i int) { setDuration(t, s, row, 100+i) })

	res, err := s.SweepBookFileSegDrop(context.Background(), false, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Conflicts != 1 || res.Rewrite != 0 {
		t.Errorf("result = %+v, want the row counted as 1 conflict and not rewritten", res)
	}
	got, _ := s.GetBookFileByID(book.ID, row.ID)
	if got == nil || got.AcoustIDSeg0 == "" {
		t.Errorf("a row the sweep gave up on must be left as stored; got %+v", got)
	}
}

func newSegSweepFixture(t *testing.T, id, name, seg string) (*PebbleStore, *Book, *BookFile) {
	t.Helper()
	s := openRescanStore(t)
	book, err := s.CreateBook(&Book{Title: "Batman: Hush", FilePath: "/lib/Gotham/Hush"})
	if err != nil {
		t.Fatal(err)
	}
	row := &BookFile{
		ID: id, BookID: book.ID, FilePath: "/lib/Gotham/Hush/" + name,
		FileHash: "gotham-hash-old", Duration: 100, AcoustIDSeg0: seg,
	}
	writeLegacyBookFileRow(t, s, row)
	return s, book, row
}

// setDuration is a concurrent writer for the sweep tests: it rewrites the row
// in place with a new duration and its legacy segment columns intact (a row
// the sweep still has to rewrite), the way a stale-format row looks after a
// scan touched an unrelated field.
func setDuration(t *testing.T, s *PebbleStore, row *BookFile, d int) {
	t.Helper()
	cp := *row
	cp.Duration = d
	writeLegacyBookFileRow(t, s, &cp)
}

// ClearAllAcoustIDFingerprints keeps a concurrent writer's fields, and refuses
// to wipe the LSH index (returning a countable error) when a row kept changing.
func TestCAS_ClearAllAcoustIDFingerprints_KeepsConcurrentWrite(t *testing.T) {
	f := newCASFixture(t)
	row := f.row(t)
	row.AcoustIDFingerprint = []byte{9, 8, 7, 6}
	if err := f.s.UpdateBookFile(f.file.ID, row); err != nil {
		t.Fatal(err)
	}
	injectBeforeCommit(t, 1, func(int) { f.strayScan(t, "gotham-hash-old", 250, 1000) })

	cleared, total, err := f.s.ClearAllAcoustIDFingerprints(context.Background(), 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cleared != 1 || total != 1 {
		t.Errorf("cleared=%d total=%d, want 1/1", cleared, total)
	}
	got := f.row(t)
	if len(got.AcoustIDFingerprint) != 0 || got.Duration != 250 {
		t.Errorf("row = fp %v duration %d, want fingerprint cleared and the concurrent upsert's 250 kept", got.AcoustIDFingerprint, got.Duration)
	}
}

func TestCAS_ClearAllAcoustIDFingerprints_ChurnIsCountableAndKeepsTheIndex(t *testing.T) {
	store := newPebbleStoreForLSH(t)
	mustInsertBookFile(t, store, "batman-a", synthRaw(1, 57600))
	if !store.HasLSHIndex("batman-a") {
		t.Fatal("fixture: the LSH index must hold the fingerprinted row before the sweep")
	}
	injectBeforeCommit(t, 0, func(i int) { setDuration2(t, store, "book-batman-a", "batman-a", 100+i) })

	cleared, _, err := store.ClearAllAcoustIDFingerprints(context.Background(), 100, nil)
	busy, ok := errors.AsType[*BookFileSweepConflictError](err)
	if !ok || !errors.Is(err, ErrBookFileConflict) || busy.Rows != 1 || cleared != 0 {
		t.Fatalf("cleared=%d err=%v, want 0 and a *BookFileSweepConflictError for 1 row", cleared, err)
	}
	got, _ := store.GetBookFileByID("book-batman-a", "batman-a")
	if got == nil || len(got.AcoustIDFingerprint) == 0 {
		t.Error("a row the sweep gave up on must keep its fingerprint")
	}
	if !store.HasLSHIndex("batman-a") {
		t.Error("the LSH index entries of a row left fingerprinted must survive: the wipe may only run when no row is left unswept")
	}
}

// setDuration2 is ModifyBookFile as the concurrent writer, for a store that
// is not a casFixture.
func setDuration2(t *testing.T, s *PebbleStore, bookID, fileID string, d int) {
	t.Helper()
	if _, err := s.ModifyBookFile(bookID, fileID, func(r *BookFile) error { r.Duration = d; return nil }); err != nil {
		t.Errorf("injected ModifyBookFile: %v", err)
	}
}

// S1: a row that MOVED to another book between the sweep's read and its commit
// is found at its new key and swept there, never dropped as "deleted".
func TestCAS_ClearAllAcoustIDFingerprints_FollowsAMovedRow(t *testing.T) {
	store := newPebbleStoreForLSH(t)
	mustInsertBookFile(t, store, "batman-b", synthRaw(2, 57600))
	dst, err := store.CreateBook(&Book{Title: "Batman: The Long Halloween", FilePath: "/lib/Gotham/Long-Halloween"})
	if err != nil {
		t.Fatal(err)
	}
	injectBeforeCommit(t, 1, func(int) {
		if err := store.MoveBookFilesToBook([]string{"batman-b"}, "book-batman-b", dst.ID); err != nil {
			t.Errorf("injected MoveBookFilesToBook: %v", err)
		}
	})

	cleared, total, err := store.ClearAllAcoustIDFingerprints(context.Background(), 100, nil)
	if err != nil || cleared != 1 || total != 1 {
		t.Fatalf("cleared=%d total=%d err=%v, want 1/1 and no error: the moved row must be swept at its new key", cleared, total, err)
	}
	got, _ := store.GetBookFileByID(dst.ID, "batman-b")
	if got == nil || len(got.AcoustIDFingerprint) != 0 {
		t.Errorf("moved row = %+v, want it present under the new book with its fingerprint cleared", got)
	}
	if old, _ := store.GetBookFileByID("book-batman-b", "batman-b"); old != nil {
		t.Errorf("a ghost row was written back at the old key: %+v", old)
	}
	if store.HasLSHIndex("batman-b") {
		t.Error("every row was swept, so the LSH index must have been wiped")
	}
}

func TestCAS_SweepBookFileSegDrop_FollowsAMovedRow(t *testing.T) {
	s, book, row := newSegSweepFixture(t, "01HUSH0000000000000000000C", "03.m4b", "AQADtAcSRW")
	dst, err := s.CreateBook(&Book{Title: "Batman: The Long Halloween", FilePath: "/lib/Gotham/Long-Halloween"})
	if err != nil {
		t.Fatal(err)
	}
	injectBeforeCommit(t, 1, func(int) {
		if err := s.MoveBookFilesToBook([]string{row.ID}, book.ID, dst.ID); err != nil {
			t.Errorf("injected MoveBookFilesToBook: %v", err)
		}
	})

	res, err := s.SweepBookFileSegDrop(context.Background(), false, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Rewrite != 1 || res.Conflicts != 0 {
		t.Errorf("result = %+v, want the moved row rewritten (1) and nothing left unswept", res)
	}
	if got, _ := s.GetBookFileByID(dst.ID, row.ID); got == nil || got.AcoustIDSeg0 != "" {
		t.Errorf("moved row = %+v, want it under the new book with its segs dropped", got)
	}
	if old, _ := s.GetBookFileByID(book.ID, row.ID); old != nil {
		t.Errorf("a ghost row was written back at the old key: %+v", old)
	}
}

// N5: a row the sweep re-reads and finds no longer needs a rewrite is counted
// as skipped, so the counts add up to Total.
func TestCAS_SweepBookFileSegDrop_CountsReReadCleanRowAsSkipped(t *testing.T) {
	s, book, row := newSegSweepFixture(t, "01HUSH0000000000000000000D", "04.m4b", "AQADtAcSRV")
	injectBeforeCommit(t, 1, func(int) {
		// A scan rewrites the row through the segs-dropping encoder.
		if _, err := s.ModifyBookFile(book.ID, row.ID, func(r *BookFile) error { r.Duration = 250; return nil }); err != nil {
			t.Errorf("injected ModifyBookFile: %v", err)
		}
	})
	res, err := s.SweepBookFileSegDrop(context.Background(), false, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Rewrite + res.Skipped + res.Errors + res.Conflicts; got != res.Total {
		t.Errorf("result = %+v: Rewrite+Skipped+Errors+Conflicts = %d, want Total (%d)", res, got, res.Total)
	}
}

// rewriteRaw rewrites a row in place from the store's own bytes: a concurrent
// writer that bypasses every stripe, usable on retired rows (which the generic
// primitives refuse). Only fields that carry no index may be changed with it.
func rewriteRaw(t *testing.T, s *PebbleStore, bookID, fileID string, mutate func(*BookFile)) {
	t.Helper()
	row, _, err := s.readBookFileRow(bookID, fileID)
	if err != nil || row == nil {
		t.Errorf("rewriteRaw: read %s/%s: row=%v err=%v", bookID, fileID, row, err)
		return
	}
	mutate(row)
	data, err := json.Marshal(row)
	if err != nil {
		t.Errorf("rewriteRaw: marshal: %v", err)
		return
	}
	if err := s.db.Set(bookFileKey(bookID, fileID), data, pebble.NoSync); err != nil {
		t.Errorf("rewriteRaw: set: %v", err)
	}
}

// N7: RetireBookFiles compares each shell row with the bytes it read, so a scan
// that changed the row (its hash, which is indexed) first is kept, and the
// index keys follow the fresh row.
func TestCAS_RetireBookFiles_KeepsConcurrentScanWrite(t *testing.T) {
	fx := newRetiredFixture(t)
	row := fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/Gotham/Retire/01.m4b", FileHash: "gotham-hash-old", Duration: 100})
	injectBeforeCommit(t, 1, func(int) {
		strayWriteByPath(t, fx.ps, row.FilePath, func(r *BookFile) { r.FileHash, r.Duration = "gotham-hash-scan", 250 })
	})

	if err := fx.retire(t, row); err != nil {
		t.Fatalf("RetireBookFiles: %v", err)
	}
	got, err := fx.ps.GetBookFileByID(fx.holder.ID, row.ID)
	if err != nil || got == nil || !got.Retired {
		t.Fatalf("retired row = %+v err %v", got, err)
	}
	if got.FileHash != "gotham-hash-scan" || got.Duration != 250 {
		t.Errorf("retired row = hash %q duration %d, want the scan's gotham-hash-scan/250 kept", got.FileHash, got.Duration)
	}
	if o := indexOwner(t, fx.ps, "book_file_hash:gotham-hash-scan"); o != fx.holder.ID+":"+row.ID {
		t.Errorf("hash index = %q, want it under the holder", o)
	}
	if o := indexOwner(t, fx.ps, "book_file_hash:gotham-hash-old"); o != "" {
		t.Errorf("stale hash index key still points at %q", o)
	}
}

func TestCAS_UnretireBookFiles_KeepsConcurrentRowWrite(t *testing.T) {
	fx := newRetiredFixture(t)
	row := fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/Gotham/Unretire/01.m4b", FileHash: "gotham-hash-old"})
	if err := fx.retire(t, row); err != nil {
		t.Fatal(err)
	}
	injectBeforeCommit(t, 1, func(int) {
		rewriteRaw(t, fx.ps, fx.holder.ID, row.ID, func(r *BookFile) { r.Title = "Batman: Year Two" })
	})

	mv := []BookFileMove{{FileIDs: []string{row.ID}, SourceBookID: fx.holder.ID}}
	if err := fx.ps.UnretireBookFiles(mv, fx.shell.ID, fx.version.ID, nil); err != nil {
		t.Fatalf("UnretireBookFiles: %v", err)
	}
	got, _ := fx.ps.GetBookFileByID(fx.shell.ID, row.ID)
	if got == nil || got.Retired || got.Title != "Batman: Year Two" {
		t.Errorf("un-retired row = %+v, want the concurrent write kept", got)
	}
}

func TestCAS_RehomeRetiredVersion_KeepsConcurrentRowWrite(t *testing.T) {
	fx := newRetiredFixture(t)
	row := fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/Gotham/Rehome/01.m4b", FileHash: "gotham-hash-old"})
	if err := fx.retire(t, row); err != nil {
		t.Fatal(err)
	}
	newHolder, err := fx.ps.CreateBook(&Book{Title: "Batman: Year Three", FilePath: "/lib/Gotham/Year-Three"})
	if err != nil {
		t.Fatal(err)
	}
	injectBeforeCommit(t, 1, func(int) {
		rewriteRaw(t, fx.ps, fx.holder.ID, row.ID, func(r *BookFile) { r.Title = "Batman: Year Two" })
	})

	if err := fx.ps.RehomeRetiredVersion(fx.version.ID, newHolder.ID); err != nil {
		t.Fatalf("RehomeRetiredVersion: %v", err)
	}
	rows, _ := fx.ps.GetRetiredBookFiles(newHolder.ID)
	if len(rows) != 1 || rows[0].Title != "Batman: Year Two" {
		t.Errorf("rehomed rows = %+v, want the concurrent write kept", rows)
	}
	if o := indexOwner(t, fx.ps, "book_file_hash:gotham-hash-old"); o != newHolder.ID+":"+row.ID {
		t.Errorf("hash index = %q, want it under the new holder", o)
	}
}

// S4: ErrBookFileConflict means "busy, try again". An input or holder error is
// not that.
func TestCAS_NonConflictErrorsAreNotConflicts(t *testing.T) {
	fx := newRetiredFixture(t)
	row := fx.create(t, fx.shell.ID, BookFile{FilePath: "/lib/Gotham/Refuse/01.m4b"})
	if err := fx.retire(t, row); err != nil {
		t.Fatal(err)
	}
	twice := []BookFileMove{{FileIDs: []string{row.ID, row.ID}, SourceBookID: fx.holder.ID}}
	if err := fx.ps.UnretireBookFiles(twice, fx.shell.ID, fx.version.ID, nil); err == nil || errors.Is(err, ErrBookFileConflict) {
		t.Errorf("UnretireBookFiles with a file named twice = %v, want an input error that is not ErrBookFileConflict", err)
	}
	if err := fx.ps.RehomeRetiredVersion(fx.version.ID, "no-such-batman-book"); err == nil || errors.Is(err, ErrBookFileConflict) {
		t.Errorf("RehomeRetiredVersion into a missing book = %v, want a holder error that is not ErrBookFileConflict", err)
	}
}

// N7: a create claiming a PID rewrites the prior owner; the batch form compares
// the prior owner's bytes too.
func TestCAS_BatchCreateBookFiles_PriorPIDOwnerKeepsConcurrentWrite(t *testing.T) {
	f := newCASFixture(t)
	const newPath = "/lib/Gotham/Batman-Year-One/02.m4b"
	injectBeforeCommit(t, 1, func(int) { f.strayScan(t, "gotham-hash-scan", 250, 2500) })

	if err := f.s.BatchCreateBookFiles([]*BookFile{{BookID: f.book.ID, FilePath: newPath, ITunesPersistentID: casPID}}); err != nil {
		t.Fatalf("BatchCreateBookFiles: %v", err)
	}
	row := f.row(t)
	if row.FileHash != "gotham-hash-scan" || row.Duration != 250 || row.ITunesPersistentID != "" {
		t.Errorf("prior owner = hash %q duration %d pid %q, want the scan's change kept and the PID released",
			row.FileHash, row.Duration, row.ITunesPersistentID)
	}
	if ref := f.indexRef(t, "book_file_hash:gotham-hash-scan"); ref != f.ref() {
		t.Errorf("scan's hash index key = %q, want %q", ref, f.ref())
	}
	if owner, _ := f.s.GetBookFileByPID(casPID); owner == nil || owner.FilePath != newPath {
		t.Errorf("PID owner = %+v, want the new row", owner)
	}
}

// N7: the upsert's match-by-ID path (a row naming an existing ID, whose path
// matches nothing) compares the matched row too.
func TestCAS_BatchUpsert_MatchByIDKeepsConcurrentWrite(t *testing.T) {
	f := newCASFixture(t)
	injectBeforeCommit(t, 1, func(int) {
		f.strayWrite(t, func(r *BookFile) { r.Title = "Batman: Year One, Chapter 5" })
	})

	const movedPath = "/lib/Gotham/Batman-Year-One/renamed.m4b"
	err := f.s.BatchUpsertBookFiles([]*BookFile{{ID: f.file.ID, BookID: f.book.ID, FilePath: movedPath, Duration: 400}})
	if err != nil {
		t.Fatalf("BatchUpsertBookFiles: %v", err)
	}
	row := f.row(t)
	if row.Title != "Batman: Year One, Chapter 5" || row.FilePath != movedPath || row.Duration != 400 {
		t.Errorf("row = title %q path %q duration %d, want the concurrent title kept and the upsert's path/duration", row.Title, row.FilePath, row.Duration)
	}
}

// N7: ClearBookFileIntro re-decides on the fresh row.
func TestCAS_ClearBookFileIntro_KeepsConcurrentWrite(t *testing.T) {
	f := newCASFixture(t)
	text := "This is Gotham."
	if res, err := f.s.SetBookFileIntro(f.book.ID, f.file.ID, BookFileIntroResult{
		SourceHash: "gotham-hash-old", RunOpID: "op-batman", Status: IntroStatusOK, Transcript: &text, Done: true,
	}); err != nil || res == IntroWriteStale {
		t.Fatalf("seeding the intro: res=%v err=%v", res, err)
	}
	injectBeforeCommit(t, 1, func(int) { f.strayScan(t, "gotham-hash-old", 250, 1000) })

	_, cleared, err := f.s.ClearBookFileIntro(f.book.ID, f.file.ID, "op-batman")
	if err != nil || !cleared {
		t.Fatalf("ClearBookFileIntro: cleared=%v err=%v", cleared, err)
	}
	if row := f.row(t); row.IntroTranscription != nil || row.Duration != 250 {
		t.Errorf("row = transcript %v duration %d, want the intro cleared and the concurrent upsert's 250 kept", row.IntroTranscription, row.Duration)
	}
}

// S7: the whole-batch writers have a documented budget of maxBatchStageRounds
// rounds. Eight lost rounds (more than the old five) still succeed ...
func TestCAS_MoveBookFiles_SurvivesMoreLostRoundsThanASingleRowWrite(t *testing.T) {
	f := newCASFixture(t)
	dst, err := f.s.CreateBook(&Book{Title: "Batman: The Dark Knight Returns", FilePath: "/lib/Gotham/Dark-Knight"})
	if err != nil {
		t.Fatal(err)
	}
	lost := maxBatchStageRounds - 1
	n := injectBeforeCommit(t, lost, func(i int) { f.strayScan(t, fmt.Sprintf("gotham-hash-%d", i), 100+i, 1000) })

	if err := f.s.MoveBookFilesToBook([]string{f.file.ID}, f.book.ID, dst.ID); err != nil {
		t.Fatalf("MoveBookFilesToBook after %d lost rounds: %v", *n, err)
	}
	moved, _ := f.s.GetBookFileByID(dst.ID, f.file.ID)
	if moved == nil || moved.Duration != 100+lost {
		t.Errorf("moved row = %+v, want the last concurrent write (duration %d) carried", moved, 100+lost)
	}
}

// ... and a writer that wins every round exhausts the budget exactly: the move
// gives up with ErrBookFileConflict and writes nothing.
func TestCAS_MoveBookFiles_GivesUpAfterTheBudget(t *testing.T) {
	f := newCASFixture(t)
	dst, err := f.s.CreateBook(&Book{Title: "Batman: The Dark Knight Returns", FilePath: "/lib/Gotham/Dark-Knight"})
	if err != nil {
		t.Fatal(err)
	}
	n := injectBeforeCommit(t, 0, func(i int) { f.strayScan(t, fmt.Sprintf("gotham-hash-%d", i), 100+i, 1000) })

	err = f.s.MoveBookFilesToBook([]string{f.file.ID}, f.book.ID, dst.ID)
	if !errors.Is(err, ErrBookFileConflict) {
		t.Fatalf("err = %v, want ErrBookFileConflict", err)
	}
	if *n != maxBatchStageRounds {
		t.Errorf("staging rounds = %d, want the budget of %d", *n, maxBatchStageRounds)
	}
	if got, _ := f.s.GetBookFileByID(dst.ID, f.file.ID); got != nil {
		t.Errorf("a refused move wrote the row under the target: %+v", got)
	}
	if f.row(t).Duration != 100+maxBatchStageRounds {
		t.Errorf("source row = %+v, want it left as the concurrent writer stored it", f.row(t))
	}
}

// R5: a refused whole-batch round waits before the next one, with a doubling,
// capped, jittered interval, and not before the first.
func TestCAS_RestageBookFileBatch_BacksOffBetweenRounds(t *testing.T) {
	f := newCASFixture(t)
	dst, err := f.s.CreateBook(&Book{Title: "Batman: The Dark Knight Returns", FilePath: "/lib/Gotham/Dark-Knight"})
	if err != nil {
		t.Fatal(err)
	}
	sleeps := recordRestageSleeps(t)
	injectBeforeCommit(t, 0, func(i int) { f.strayScan(t, fmt.Sprintf("gotham-hash-%d", i), 100+i, 1000) })

	if err := f.s.MoveBookFilesToBook([]string{f.file.ID}, f.book.ID, dst.ID); !errors.Is(err, ErrBookFileConflict) {
		t.Fatalf("err = %v, want ErrBookFileConflict", err)
	}
	if len(*sleeps) != maxBatchStageRounds-1 {
		t.Fatalf("waits = %d, want one between each of the %d rounds (%d)", len(*sleeps), maxBatchStageRounds, maxBatchStageRounds-1)
	}
	for i, d := range *sleeps {
		full := min(restageBackoffBase<<min(i, 4), restageBackoffMax)
		if d < full/2 || d > full {
			t.Errorf("wait before round %d = %v, want within [%v, %v]", i+2, d, full/2, full)
		}
	}
}

// R5: only the rows a refused round named are read again. Six rows are moved;
// the row at position 0 changes in two rounds. The first round reads all six,
// each later round reads that one row.
func TestCAS_MoveBookFiles_RereadsOnlyTheStaleRows(t *testing.T) {
	f := newCASFixture(t)
	dst, err := f.s.CreateBook(&Book{Title: "Batman: The Dark Knight Returns", FilePath: "/lib/Gotham/Dark-Knight"})
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{f.file.ID}
	for i := range 5 {
		p := fmt.Sprintf("/lib/Gotham/Batman-Year-One/more-%d.m4b", i)
		if err := f.s.CreateBookFile(&BookFile{BookID: f.book.ID, FilePath: p, FileHash: fmt.Sprintf("gotham-more-%d", i)}); err != nil {
			t.Fatal(err)
		}
		got, _ := f.s.GetBookFileByPath(p)
		ids = append(ids, got.ID)
	}
	var reads []string
	bookFileRowCacheMissHook = func(key string) { reads = append(reads, key) }
	t.Cleanup(func() { bookFileRowCacheMissHook = nil })
	injectBeforeCommit(t, 2, func(i int) { f.strayScan(t, fmt.Sprintf("gotham-hash-%d", i), 100+i, 1000) })

	if err := f.s.MoveBookFilesToBook(ids, f.book.ID, dst.ID); err != nil {
		t.Fatalf("MoveBookFilesToBook: %v", err)
	}
	if want := len(ids) + 2; len(reads) != want {
		t.Errorf("row reads = %d (%v), want %d: six in the first round, then the one stale row twice", len(reads), reads, want)
	}
	for _, key := range reads[len(ids):] {
		if want := "book_file:" + f.book.ID + ":" + f.file.ID; key != want {
			t.Errorf("a later round read %s, want only the stale row %s", key, want)
		}
	}
	moved, _ := f.s.GetBookFileByID(dst.ID, f.file.ID)
	if moved == nil || moved.Duration != 102 || moved.FileHash != "gotham-hash-2" {
		t.Errorf("moved row = %+v, want the last concurrent write carried", moved)
	}
	for _, id := range ids {
		if got, _ := f.s.GetBookFileByID(dst.ID, id); got == nil {
			t.Errorf("row %s did not arrive under the target", id)
		}
		if got, _ := f.s.GetBookFileByID(f.book.ID, id); got != nil {
			t.Errorf("row %s is still under the source", id)
		}
	}
}

// rotatingChurnFixture creates rows rows under the fixture's book and returns a
// batch that upserts each with the title "Batman: Rotation", with their paths.
func rotatingChurnFixture(t *testing.T, f *casFixture, rows int) ([]*BookFile, []string) {
	t.Helper()
	batch := make([]*BookFile, 0, rows)
	paths := make([]string, 0, rows)
	for i := range rows {
		p := fmt.Sprintf("/lib/Gotham/Batman-Year-One/rot-%02d.m4b", i)
		paths = append(paths, p)
		if err := f.s.CreateBookFile(&BookFile{BookID: f.book.ID, FilePath: p, FileHash: fmt.Sprintf("gotham-rot-%02d", i)}); err != nil {
			t.Fatal(err)
		}
		batch = append(batch, &BookFile{BookID: f.book.ID, FilePath: p, Title: "Batman: Rotation"})
	}
	return batch, paths
}

// S8, hard backstop: a writer that changes a DIFFERENT row of a batch each round
// must not make the batch re-stage once per row. Past the budget the stale rows
// are refused round by round, and a writer that keeps going through twice the
// budget abandons the batch with nothing written.
func TestCAS_BatchUpsert_BoundsRestagingUnderRotatingChurn(t *testing.T) {
	f := newCASFixture(t)
	const rows = 4 * maxBatchStageRounds
	batch, paths := rotatingChurnFixture(t, f, rows)
	n := injectBeforeCommit(t, 0, func(i int) {
		strayWriteByPath(t, f.s, paths[i-1], func(r *BookFile) { r.Duration = 100 + i })
	})

	err := f.s.BatchUpsertBookFiles(batch)
	if !errors.Is(err, ErrBookFileConflict) {
		t.Fatalf("err = %v, want ErrBookFileConflict", err)
	}
	var refused *BookFileRowsRefusedError
	if errors.As(err, &refused) {
		t.Errorf("an abandoned batch reported a partial commit: %v", err)
	}
	if *n != 2*maxBatchStageRounds {
		t.Errorf("staging rounds = %d, want the hard backstop of %d, not one per row (%d)", *n, 2*maxBatchStageRounds, rows)
	}
	for _, p := range paths {
		if got, _ := f.s.GetBookFileByPath(p); got == nil || got.Title != "" {
			t.Errorf("the abandoned batch wrote %s: %+v", p, got)
		}
	}
}

// R2: when the churn stops before the backstop, the rows that were stale in a
// round past the budget are refused with the churn reason and EVERY OTHER ROW
// COMMITS, through the same *BookFileRowsRefusedError the batch writers already
// use for a missing owner. The rows refused are the ones that lost a round, and
// their stored value is the concurrent writer's, untouched.
func TestCAS_BatchUpsert_RefusesStaleRowsPastTheBudgetAndCommitsTheRest(t *testing.T) {
	f := newCASFixture(t)
	const rows = 3 * maxBatchStageRounds
	const churnRounds = maxBatchStageRounds + 2
	batch, paths := rotatingChurnFixture(t, f, rows)
	n := injectBeforeCommit(t, churnRounds, func(i int) {
		strayWriteByPath(t, f.s, paths[i-1], func(r *BookFile) { r.Duration = 100 + i })
	})

	err := f.s.BatchUpsertBookFiles(batch)
	var refused *BookFileRowsRefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("err = %v, want a *BookFileRowsRefusedError", err)
	}
	if !errors.Is(err, ErrBookFileConflict) || !refused.churned() {
		t.Errorf("the refusal is not reported as churn: %v", err)
	}
	if *n != churnRounds {
		t.Fatalf("injections = %d, want %d", *n, churnRounds)
	}
	// Rounds 1..budget-1 only re-stage; each round from the budget on refuses
	// the row that lost it.
	wantRefused := churnRounds - maxBatchStageRounds + 1
	if len(refused.RefusedFileIDs) != wantRefused || refused.Committed != rows-wantRefused {
		t.Errorf("refused %d, committed %d; want %d refused and %d committed", len(refused.RefusedFileIDs), refused.Committed, wantRefused, rows-wantRefused)
	}
	for i, p := range paths {
		got, _ := f.s.GetBookFileByPath(p)
		if got == nil {
			t.Fatalf("row %s is gone", p)
		}
		isRefused := i >= maxBatchStageRounds-1 && i < churnRounds
		switch {
		case isRefused && got.Title != "":
			t.Errorf("%s was refused but carries the batch's title %q", p, got.Title)
		case !isRefused && got.Title != "Batman: Rotation":
			t.Errorf("%s was not refused but the batch's write is missing: %+v", p, got)
		}
	}
}

// secondaryKeysOf lists every book_file secondary index key (hash, path, PID,
// AcoustID) whose value names fileID. After a delete there must be none: a key
// left behind points at a row that no longer exists.
func secondaryKeysOf(t *testing.T, s *PebbleStore, fileID string) []string {
	t.Helper()
	var out []string
	for _, prefix := range []string{"book_file_hash:", "book_file_orig_hash:", "book_file_path:", "book_file_pid:", "book_file_acoustid:", "book_file_id:"} {
		iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: []byte(prefix), UpperBound: prefixEnd([]byte(prefix))})
		if err != nil {
			t.Fatal(err)
		}
		for ok := iter.First(); ok; ok = iter.Next() {
			if strings.Contains(string(iter.Value()), fileID) {
				out = append(out, string(iter.Key()))
			}
		}
		if err := iter.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// The deletes derive their index deletes from the row they read. The deletes take
// no file stripe, so a writer that takes none either can replace the row's hash
// and path between that read and the commit; a delete that only checked the row
// still EXISTS would delete the old value's keys and leave the new value's
// pointing at nothing.
// Each case here is one injected rewrite; every delete path must notice, re-read
// and finish with no index key naming the row.
func TestCAS_Deletes_FollowARowChangedBetweenReadAndCommit(t *testing.T) {
	rewrites := map[string]func(f *casFixture, t *testing.T){
		"new hash": func(f *casFixture, t *testing.T) { f.strayScan(t, "gotham-hash-rewritten", 250, 2500) },
		"new path": func(f *casFixture, t *testing.T) {
			f.strayWrite(t, func(r *BookFile) {
				r.FilePath, r.FileHash = "/lib/Gotham/Batman-Year-One/renamed.m4b", "gotham-hash-renamed"
			})
		},
	}
	deletes := map[string]func(f *casFixture) error{
		"DeleteBookFile":         func(f *casFixture) error { return f.s.DeleteBookFile(f.file.ID) },
		"DeleteBookFilesForBook": func(f *casFixture) error { return f.s.DeleteBookFilesForBook(f.book.ID) },
		"DeleteBookFilesByIDs": func(f *casFixture) error {
			_, err := f.s.DeleteBookFilesByIDs(refsOf(f.book.ID, f.file.ID))
			return err
		},
	}
	for rname, rewrite := range rewrites {
		for dname, del := range deletes {
			t.Run(dname+"/"+rname, func(t *testing.T) {
				f := newCASFixture(t)
				n := injectBeforeCommit(t, 1, func(int) { rewrite(f, t) })

				if err := del(f); err != nil {
					t.Fatalf("%s: %v", dname, err)
				}
				if *n != 1 {
					t.Fatalf("the concurrent rewrite ran %d times, want 1", *n)
				}
				if got, err := f.s.GetBookFileByID(f.book.ID, f.file.ID); err != nil || got != nil {
					t.Errorf("row survived the delete: row=%+v err=%v", got, err)
				}
				if keys := secondaryKeysOf(t, f.s, f.file.ID); len(keys) > 0 {
					t.Errorf("index keys left naming the deleted row: %v", keys)
				}
			})
		}
	}
}

// A delete-all whose rows keep changing under it gives up after the whole-batch
// budget, deleting nothing, and says so with a conflict the caller can count.
func TestCAS_DeleteBookFilesForBook_GivesUpUnderRotatingChurn(t *testing.T) {
	f := newCASFixture(t)
	n := injectBeforeCommit(t, 0, func(i int) { f.strayScan(t, fmt.Sprintf("gotham-churn-%02d", i), 100+i, 1000) })

	err := f.s.DeleteBookFilesForBook(f.book.ID)
	if !errors.Is(err, ErrBookFileConflict) {
		t.Fatalf("err = %v, want ErrBookFileConflict", err)
	}
	if *n != maxBatchStageRounds {
		t.Errorf("staging rounds = %d, want the budget of %d", *n, maxBatchStageRounds)
	}
	if got := f.row(t); got.FilePath != casPath {
		t.Errorf("the abandoned delete changed the row: %+v", got)
	}
	if ref := f.indexRef(t, fmt.Sprintf("book_file_hash:gotham-churn-%02d", maxBatchStageRounds)); ref != f.ref() {
		t.Errorf("the surviving row lost its index key: %q", ref)
	}
}

// bytesPerCall is the least TotalAlloc per call of fn over several runs (other
// goroutines only add to it).
func bytesPerCall(t *testing.T, fn func()) uint64 {
	t.Helper()
	const calls = 20
	best := ^uint64(0)
	for range 5 {
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		for range calls {
			fn()
		}
		runtime.ReadMemStats(&after)
		best = min(best, (after.TotalAlloc-before.TotalAlloc)/calls)
	}
	return best
}

// N1: a pure reader decodes straight from the Pebble buffer. The compare-and-swap
// writers need a copy of the stored bytes to compare at commit, but GetBookFileByID,
// GetBookFileByPath and GetBookFileByPID throw it away, and a fingerprinted row
// runs to hundreds of kilobytes: a copy per read is a second allocation of the
// whole row for nothing. Measured against the two read modes of the same row, so
// the check holds under -race, whose instrumentation changes the absolute
// allocations of the decode.
func TestReaders_DoNotCopyTheStoredRow(t *testing.T) {
	f := newCASFixture(t)
	fp := bytes.Repeat([]byte("A"), 200<<10)
	rewriteRaw(t, f.s, f.book.ID, f.file.ID, func(r *BookFile) { r.AcoustIDFingerprint = fp })

	key := bookFileKey(f.book.ID, f.file.ID)
	_, raw, err := f.s.readBookFileRowAt(key)
	if err != nil || len(raw) < len(fp) {
		t.Fatalf("readBookFileRowAt: %d raw bytes, err %v", len(raw), err)
	}
	noCopy := bytesPerCall(t, func() { _, _, _ = f.s.loadBookFileRowAt(key, false) })
	withCopy := bytesPerCall(t, func() { _, _, _ = f.s.loadBookFileRowAt(key, true) })
	if withCopy < noCopy+uint64(len(raw))/2 {
		t.Fatalf("the measurement cannot tell the modes apart: %d bytes per call without the copy, %d with it, row is %d bytes", noCopy, withCopy, len(raw))
	}
	// A reader may allocate a little more than the bare decode (the index
	// lookup), never as much as the extra copy.
	limit := noCopy + uint64(len(raw))/2

	readers := map[string]func() (*BookFile, error){
		"GetBookFileByID":   func() (*BookFile, error) { return f.s.GetBookFileByID(f.book.ID, f.file.ID) },
		"GetBookFileByPath": func() (*BookFile, error) { return f.s.GetBookFileByPath(casPath) },
		"GetBookFileByPID":  func() (*BookFile, error) { return f.s.GetBookFileByPID(casPID) },
	}
	for name, read := range readers {
		t.Run(name, func(t *testing.T) {
			got, err := read()
			if err != nil || got == nil || !bytes.Equal(got.AcoustIDFingerprint, fp) {
				t.Fatalf("read: row=%v err=%v", got != nil, err)
			}
			if used := bytesPerCall(t, func() { _, _ = read() }); used > limit {
				t.Errorf("%s allocates %d bytes per read, want under %d (bare decode %d, decode plus raw copy %d): it is copying the stored row", name, used, limit, noCopy, withCopy)
			}
		})
	}
}

// N8: an upsert that matched row A by iTunes ID must match again when a retry
// finds A no longer holds that ID. Here a concurrent create transfers the ID
// from A to a new row B between the match and the commit: the upsert has to land
// on B, and A must not get the ID back.
func TestCAS_UpsertBookFile_RematchesAfterAPIDTransfer(t *testing.T) {
	f := newCASFixture(t)
	const newPath = "/lib/Gotham/Batman-Year-One/02.m4b"
	n := injectBeforeCommit(t, 1, func(int) {
		if err := f.s.CreateBookFile(&BookFile{BookID: f.book.ID, FilePath: newPath, ITunesPersistentID: casPID}); err != nil {
			t.Errorf("injected CreateBookFile: %v", err)
		}
	})

	if err := f.s.UpsertBookFile(&BookFile{BookID: f.book.ID, FilePath: newPath, ITunesPersistentID: casPID, Title: "Batman: Year One, Chapter 2"}); err != nil {
		t.Fatalf("UpsertBookFile: %v", err)
	}
	if *n != 1 {
		t.Fatalf("the concurrent create ran %d times, want 1", *n)
	}

	a := f.row(t)
	if a.ITunesPersistentID != "" {
		t.Errorf("row A got PID %q back: two rows now claim it", a.ITunesPersistentID)
	}
	if a.Title == "Batman: Year One, Chapter 2" {
		t.Errorf("the upsert was written to row A, which no longer holds the PID")
	}
	owner, err := f.s.GetBookFileByPID(casPID)
	if err != nil || owner == nil {
		t.Fatalf("GetBookFileByPID: row=%v err=%v", owner, err)
	}
	if owner.ID == f.file.ID || owner.FilePath != newPath {
		t.Errorf("book_file_pid:%s resolves to %s at %s, want the new row at %s", casPID, owner.ID, owner.FilePath, newPath)
	}
	if owner.Title != "Batman: Year One, Chapter 2" {
		t.Errorf("owner title = %q, want the upsert applied to the row that holds the PID now", owner.Title)
	}
}

// N8, by path: an upsert that matched row A by path must match again when a
// retry finds A has been renamed. Merging the incoming path back onto the fresh
// A would undo the rename and point book_file_path at A again.
func TestCAS_UpsertBookFile_RematchesAfterARename(t *testing.T) {
	f := newCASFixture(t)
	// Matching by path only: the fixture row's PID is not part of this upsert.
	const renamed = "/lib/Gotham/Batman-Year-One/renamed.m4b"
	n := injectBeforeCommit(t, 1, func(int) {
		f.strayWrite(t, func(r *BookFile) { r.FilePath = renamed })
	})

	if err := f.s.UpsertBookFile(&BookFile{BookID: f.book.ID, FilePath: casPath, Title: "Batman: Year One, Chapter 3"}); err != nil {
		t.Fatalf("UpsertBookFile: %v", err)
	}
	if *n != 1 {
		t.Fatalf("the concurrent rename ran %d times, want 1", *n)
	}

	a := f.row(t)
	if a.FilePath != renamed {
		t.Errorf("row A is at %q, want the concurrent rename to %q kept", a.FilePath, renamed)
	}
	if a.Title == "Batman: Year One, Chapter 3" {
		t.Errorf("the upsert was written to row A, which no longer sits at %s", casPath)
	}
	at, err := f.s.GetBookFileByPath(casPath)
	if err != nil || at == nil {
		t.Fatalf("GetBookFileByPath(%s): row=%v err=%v", casPath, at, err)
	}
	if at.ID == f.file.ID || at.Title != "Batman: Year One, Chapter 3" {
		t.Errorf("the upsert's path resolves to %s (title %q), want a new row carrying the upsert", at.ID, at.Title)
	}
	if byRenamed, _ := f.s.GetBookFileByPath(renamed); byRenamed == nil || byRenamed.ID != f.file.ID {
		t.Errorf("book_file_path for %s does not resolve to row A: %+v", renamed, byRenamed)
	}
}

// A single delete whose row changes on every attempt gives up with the same
// typed conflict every other book_file writer returns, and deletes nothing.
func TestCAS_DeleteBookFile_GivesUpUnderRotatingChurn(t *testing.T) {
	f := newCASFixture(t)
	n := injectBeforeCommit(t, 0, func(i int) { f.strayScan(t, fmt.Sprintf("gotham-churn-%02d", i), 100+i, 1000) })

	err := f.s.DeleteBookFile(f.file.ID)
	if !errors.Is(err, ErrBookFileConflict) {
		t.Fatalf("err = %v, want ErrBookFileConflict", err)
	}
	if *n != maxStageRetries {
		t.Errorf("attempts = %d, want %d", *n, maxStageRetries)
	}
	if got := f.row(t); got.FilePath != casPath {
		t.Errorf("the abandoned delete changed the row: %+v", got)
	}
}

// R3: a bulk delete is a judgement about the book the caller found its rows
// under. A regroup that moves one of them to another book after that judgement
// (here: between the delete's read and its commit) must not have the row deleted
// from its new book by the delete's re-resolution; the whole call is refused,
// with an error that is not "busy", and deletes nothing.
func TestCAS_DeleteBookFilesByIDs_RefusesARowMovedToAnotherBook(t *testing.T) {
	f := newCASFixture(t)
	dst, err := f.s.CreateBook(&Book{Title: "Batman: The Dark Knight Returns", FilePath: "/lib/Gotham/Dark-Knight"})
	if err != nil {
		t.Fatal(err)
	}
	second := &BookFile{BookID: f.book.ID, FilePath: "/lib/Gotham/Batman-Year-One/02.m4b", FileHash: "gotham-hash-second"}
	if err := f.s.CreateBookFile(second); err != nil {
		t.Fatal(err)
	}
	n := injectBeforeCommit(t, 1, func(int) {
		if err := f.s.MoveBookFilesToBook([]string{f.file.ID}, f.book.ID, dst.ID); err != nil {
			t.Errorf("injected MoveBookFilesToBook: %v", err)
		}
	})

	_, err = f.s.DeleteBookFilesByIDs(refsOf(f.book.ID, f.file.ID, second.ID))
	if !errors.Is(err, ErrBookFileOwnerChanged) {
		t.Fatalf("err = %v, want ErrBookFileOwnerChanged", err)
	}
	if errors.Is(err, ErrBookFileConflict) {
		t.Errorf("a moved row is not a busy row: %v", err)
	}
	if *n != 1 {
		t.Errorf("the concurrent move ran %d times, want 1", *n)
	}
	if got, _ := f.s.GetBookFileByID(dst.ID, f.file.ID); got == nil {
		t.Error("the moved row was deleted from its new book")
	}
	if got, _ := f.s.GetBookFileByID(f.book.ID, second.ID); got == nil {
		t.Error("the other row of a refused call was deleted: the call must delete nothing")
	}
}

// ... and a row that has already moved when the call is made is refused the same
// way, on the first resolution.
func TestCAS_DeleteBookFilesByIDs_RefusesARowAlreadyUnderAnotherBook(t *testing.T) {
	f := newCASFixture(t)
	dst, err := f.s.CreateBook(&Book{Title: "Batman: The Dark Knight Returns", FilePath: "/lib/Gotham/Dark-Knight"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.MoveBookFilesToBook([]string{f.file.ID}, f.book.ID, dst.ID); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.DeleteBookFilesByIDs(refsOf(f.book.ID, f.file.ID))
	if !errors.Is(err, ErrBookFileOwnerChanged) {
		t.Fatalf("err = %v, want ErrBookFileOwnerChanged", err)
	}
	// The same row, named under the book it is now under, is deleted, and the
	// rows come back as they were deleted.
	deleted, err := f.s.DeleteBookFilesByIDs(refsOf(dst.ID, f.file.ID))
	if err != nil {
		t.Fatalf("DeleteBookFilesByIDs under the current owner: %v", err)
	}
	if len(deleted) != 1 || deleted[0].ID != f.file.ID || deleted[0].BookID != dst.ID {
		t.Errorf("deleted = %+v, want the one row, under %s", deleted, dst.ID)
	}
}

// R4: the rows a bulk delete returns are the bytes it removed. A writer that
// changed a row after the caller read it is deleted in its changed form, and the
// result says so (a caller journaling the delete records that, not its own read).
func TestCAS_DeleteBookFilesByIDs_ReturnsTheRowsAsDeleted(t *testing.T) {
	f := newCASFixture(t)
	injectBeforeCommit(t, 1, func(int) { f.strayScan(t, "gotham-hash-rewritten", 250, 2500) })

	deleted, err := f.s.DeleteBookFilesByIDs(refsOf(f.book.ID, f.file.ID))
	if err != nil {
		t.Fatalf("DeleteBookFilesByIDs: %v", err)
	}
	if len(deleted) != 1 || deleted[0].FileHash != "gotham-hash-rewritten" || deleted[0].Duration != 250 {
		t.Errorf("deleted = %+v, want the row as the concurrent writer left it", deleted)
	}
}

// N7: real concurrent writers on one row, for -race and for the invariant the
// compare-and-swap exists to keep: after any interleaving, the row's secondary
// index keys are exactly the keys of the row as stored. Every other test injects
// one writer at one point; this one lets ModifyBookFile, SetBookFileHash and the
// scanner's batch upsert (all three take the row's file stripe) race on the
// same row. A write that loses is allowed to give up with
// ErrBookFileConflict; none may fail otherwise, and none may leave a key behind.
//
// What is NOT asserted from the race itself is the final Title. The scanner's
// batch upsert carries a new FileHash every round, which makes the merge treat
// the incoming row as changed content and write its (empty) Title, so whichever
// writer commits last decides the Title. That is correct behaviour, and an
// assertion that ignored it failed about 30% of runs. The Title is asserted only
// where the ordering is known: (a) when the last successful ModifyBookFile call
// began after every other writer had returned, so no other write can have
// followed it, and (b) after the writers are done, with one uncontended
// ModifyBookFile whose write must stick and whose index keys must be exact.
// The sequence below is a single atomic counter: a call takes a number before it
// starts and the other writers take one after each return, so the comparison
// never depends on when a stamp is taken relative to a commit.
func TestCAS_ConcurrentWriters_LeaveExactlyTheStoredRowsIndexKeys(t *testing.T) {
	f := newCASFixture(t)
	const rounds = 300
	var (
		wg                   sync.WaitGroup
		seq                  atomic.Int64
		modifyLastOKBegin    atomic.Int64
		modifyLastOKRound    atomic.Int64
		setLastEnd, scanLast atomic.Int64
	)
	failed := func(who string, i int, err error) {
		if err != nil && !errors.Is(err, ErrBookFileConflict) {
			t.Errorf("%s round %d: %v", who, i, err)
		}
	}
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := range rounds {
			begin := seq.Add(1)
			_, err := f.s.ModifyBookFile(f.book.ID, f.file.ID, func(r *BookFile) error {
				r.Title = fmt.Sprintf("Batman: Year One, Part %02d", i)
				return nil
			})
			if err == nil {
				modifyLastOKBegin.Store(begin)
				modifyLastOKRound.Store(int64(i))
			}
			failed("ModifyBookFile", i, err)
		}
	}()
	go func() {
		defer wg.Done()
		for i := range rounds {
			err := f.s.SetBookFileHash(f.file.ID, fmt.Sprintf("gotham-set-%03d", i))
			setLastEnd.Store(seq.Add(1))
			failed("SetBookFileHash", i, err)
		}
	}()
	go func() {
		defer wg.Done()
		for i := range rounds {
			err := f.s.BatchUpsertBookFiles([]*BookFile{{
				BookID: f.book.ID, FilePath: casPath, FileHash: fmt.Sprintf("gotham-scan-%03d", i), Duration: 100 + i, FileSize: 1000,
			}})
			scanLast.Store(seq.Add(1))
			failed("BatchUpsertBookFiles", i, err)
		}
	}()
	wg.Wait()

	checkKeys := func(stage string) *BookFile {
		t.Helper()
		row := f.row(t)
		want := map[string]bool{
			"book_file_hash:" + row.FileHash:                  true,
			"book_file_path:" + bookFilePathCRC(row.FilePath): true,
			"book_file_pid:" + row.ITunesPersistentID:         true,
			"book_file_id:" + row.ID:                          true,
		}
		if row.OriginalFileHash != "" {
			want["book_file_orig_hash:"+row.OriginalFileHash] = true
		}
		for _, key := range secondaryKeysOf(t, f.s, f.file.ID) {
			if !want[key] {
				t.Errorf("%s: index key %s names the row but the stored row (hash %q, path %q, PID %q) does not own it", stage, key, row.FileHash, row.FilePath, row.ITunesPersistentID)
			}
			delete(want, key)
		}
		for key := range want {
			t.Errorf("%s: the stored row's index key %s is missing", stage, key)
		}
		return row
	}

	row := checkKeys("after the race")
	if begin := modifyLastOKBegin.Load(); begin > setLastEnd.Load() && begin > scanLast.Load() {
		if want := fmt.Sprintf("Batman: Year One, Part %02d", modifyLastOKRound.Load()); row.Title != want {
			t.Errorf("Title = %q: the last ModifyBookFile call began after every other writer returned, so its write %q must be the stored one", row.Title, want)
		}
	}

	if _, err := f.s.ModifyBookFile(f.book.ID, f.file.ID, func(r *BookFile) error {
		r.Title = "Batman: Year One, Final"
		return nil
	}); err != nil {
		t.Fatalf("uncontended ModifyBookFile: %v", err)
	}
	if got := checkKeys("after the uncontended write"); got.Title != "Batman: Year One, Final" {
		t.Errorf("Title = %q: an uncontended ModifyBookFile write was lost", got.Title)
	}
}
