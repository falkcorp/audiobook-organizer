// file: internal/database/bookfile_cas_test.go
// version: 1.0.0
// guid: 0f3c52d8-7e19-4b64-a2d0-91c8b5e46a17
// last-edited: 2026-10-26

package database

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// The compare-and-swap in commitBookFileBatch. Every test here injects a
// concurrent writer through bookFileBeforeCommitHook, which runs after the
// victim has READ its row and before it takes the owner stripes: the exact
// window in which a writer that takes no file stripe used to be reverted.
//
// The injected writer must never take the victim's file stripe (it would
// deadlock): CreateBookFile and the batch upserts take none, and the hook is
// guarded against re-entry because the injected writer commits through the
// same hook.

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

// upsertScan is the injected writer: the scanner's batch upsert of this
// file's path with a new hash, duration and size. It takes no file stripe.
func (f *casFixture) upsertScan(t *testing.T, hash string, duration int, size int64) {
	t.Helper()
	err := f.s.BatchUpsertBookFiles([]*BookFile{{
		BookID: f.book.ID, FilePath: casPath, FileHash: hash, Duration: duration, FileSize: size,
	}})
	if err != nil {
		t.Fatalf("injected BatchUpsertBookFiles: %v", err)
	}
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
	injectBeforeCommit(t, 1, func(int) { f.upsertScan(t, "gotham-hash-scan", 250, 2500) })

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
		// The concurrent writer (a scan upsert, which holds no file stripe)
		// records a fingerprint; a slim whole-row caller must not blank it
		// (bfPreserveAlways restores it from the fresh row).
		err := f.s.BatchUpsertBookFiles([]*BookFile{{
			BookID: f.book.ID, FilePath: casPath, FileHash: "gotham-hash-old", AcoustIDFingerprint: fp,
		}})
		if err != nil {
			t.Errorf("injected BatchUpsertBookFiles: %v", err)
		}
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
	injectBeforeCommit(t, 1, func(int) { f.upsertScan(t, "gotham-hash-old", 321, 1000) })

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

// Converse: the victim is a batch upsert (which holds no file stripe) and the
// concurrent writer is ModifyBookFile. The upsert's staged value came from a
// row that no longer exists; it must be re-staged on the modified row.
func TestCAS_BatchUpsert_KeepsConcurrentModifyBookFile(t *testing.T) {
	f := newCASFixture(t)
	injectBeforeCommit(t, 1, func(int) {
		if _, err := f.s.ModifyBookFile(f.book.ID, f.file.ID, func(r *BookFile) error {
			r.Title = "Batman: Year One, Chapter 4"
			return nil
		}); err != nil {
			t.Errorf("injected ModifyBookFile: %v", err)
		}
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
	injectBeforeCommit(t, 1, func(int) { f.upsertScan(t, "gotham-hash-scan", 250, 2500) })

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
	injectBeforeCommit(t, 1, func(int) { f.upsertScan(t, "gotham-hash-scan", 250, 2500) })

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
	injectBeforeCommit(t, 1, func(int) { f.upsertScan(t, "gotham-hash-scan", 250, 2500) })

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
	injectBeforeCommit(t, 1, func(int) { f.upsertScan(t, "gotham-hash-scan", 250, 2500) })

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
	injectBeforeCommit(t, 1, func(int) { f.upsertScan(t, "gotham-hash-scan", 250, 2500) })

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
			n := injectBeforeCommit(t, 0, func(i int) { f.upsertScan(t, fmt.Sprintf("gotham-hash-%d", i), 100+i, 1000) })

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
	injectBeforeCommit(t, 0, func(i int) { f.upsertScan(t, fmt.Sprintf("gotham-hash-%d", i), 100+i, 1000) })

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
	injectBeforeCommit(t, 1, func(int) { f.upsertScan(t, "gotham-hash-scan", 250, 2500) })

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
	injectBeforeCommit(t, 1, func(int) { f.upsertScan(t, "gotham-hash-old", 250, 1000) })

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
	f := newCASFixture(t)
	row := f.row(t)
	row.AcoustIDFingerprint = []byte{9, 8, 7, 6}
	if err := f.s.UpdateBookFile(f.file.ID, row); err != nil {
		t.Fatal(err)
	}
	injectBeforeCommit(t, 0, func(i int) { f.upsertScan(t, "gotham-hash-old", 100+i, 1000) })

	cleared, _, err := f.s.ClearAllAcoustIDFingerprints(context.Background(), 100, nil)
	if !errors.Is(err, ErrBookFileConflict) || cleared != 0 {
		t.Fatalf("cleared=%d err=%v, want 0 and ErrBookFileConflict", cleared, err)
	}
	if got := f.row(t); len(got.AcoustIDFingerprint) == 0 {
		t.Error("a row the sweep gave up on must keep its fingerprint")
	}
}
