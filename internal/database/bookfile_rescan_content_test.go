// file: internal/database/bookfile_rescan_content_test.go
// version: 1.2.0
// guid: 3b8e1f52-6c4d-4a97-9e20-7d5b1c8a4f63
// last-edited: 2026-09-13

package database

import (
	"reflect"
	"testing"
)

// A derivation only makes sense on a field the upsert would otherwise restore;
// on an owned, identity, not-stored or write-once field it would be dead.
func TestBookFileFieldClasses_DerivationOnlyOnRestoredFields(t *testing.T) {
	for name, rule := range bookFileFieldClasses {
		if rule.derived != derivedNone && rule.class != bfPreserveOnUpsert && rule.class != bfPreserveAlways {
			t.Errorf("%s has derivation %d but its class (%d) is never restored", name, rule.derived, rule.class)
		}
	}
}

func openRescanStore(t *testing.T) *PebbleStore {
	t.Helper()
	s, err := NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func onlyBookFile(t *testing.T, s *PebbleStore, bookID string) BookFile {
	t.Helper()
	files, err := s.GetBookFiles(bookID)
	if err != nil || len(files) != 1 {
		t.Fatalf("GetBookFiles: err=%v len=%d", err, len(files))
	}
	return files[0]
}

// changedFields lists every compared field that differs, skipping
// rescanNotCompared plus extra.
func changedFields(before, after *BookFile, extra ...string) []string {
	skip := map[string]bool{}
	for _, n := range extra {
		skip[n] = true
	}
	bv, av := reflect.ValueOf(*before), reflect.ValueOf(*after)
	var out []string
	for i := range bv.NumField() {
		name := bv.Type().Field(i).Name
		if _, s := rescanNotCompared[name]; s || skip[name] {
			continue
		}
		if !reflect.DeepEqual(bv.Field(i).Interface(), av.Field(i).Interface()) {
			out = append(out, name)
		}
	}
	return out
}

// seedWithNeedsRescanFalse seeds a fully populated row, then stores
// NeedsRescan=false so a test can see whether anything raises it.
func seedWithNeedsRescanFalse(t *testing.T, s *PebbleStore) (*BookFile, string) {
	t.Helper()
	before, bookID := seedFullyPopulatedBookFile(t, s)
	f := false
	row := *before
	row.NeedsRescan = &f
	if err := s.UpdateBookFile(row.ID, &row); err != nil {
		t.Fatalf("UpdateBookFile: %v", err)
	}
	got := onlyBookFile(t, s, bookID)
	return &got, bookID
}

// M1: the scanner stat'd the file, so the upsert clears Missing, and nothing
// else it does not own changes.
func TestBatchUpsertScannedBookFiles_StatSucceededClearsMissing(t *testing.T) {
	s := openRescanStore(t)
	before, bookID := seedFullyPopulatedBookFile(t, s)
	if !before.Missing {
		t.Fatal("seed must be Missing=true")
	}
	if err := s.BatchUpsertScannedBookFiles([]ScannedBookFile{{File: scannerShapedRow(bookID, before.FilePath), Present: true}}); err != nil {
		t.Fatalf("BatchUpsertScannedBookFiles: %v", err)
	}
	after := onlyBookFile(t, s, bookID)
	if after.Missing {
		t.Error("a successful scanner stat must clear Missing")
	}
	if ch := changedFields(before, &after, "Missing"); len(ch) > 0 {
		t.Errorf("scanner upsert changed fields it does not own: %v", ch)
	}
}

// A scanned row whose stat failed must not clear Missing.
func TestBatchUpsertScannedBookFiles_StatFailedKeepsMissing(t *testing.T) {
	s := openRescanStore(t)
	before, bookID := seedFullyPopulatedBookFile(t, s)
	if err := s.BatchUpsertScannedBookFiles([]ScannedBookFile{{File: scannerShapedRow(bookID, before.FilePath), Present: false}}); err != nil {
		t.Fatalf("BatchUpsertScannedBookFiles: %v", err)
	}
	if after := onlyBookFile(t, s, bookID); !after.Missing {
		t.Error("a scanned row without a successful stat cleared Missing")
	}
}

// The iTunes sync upserts bare rows without looking at the disk; a stored
// Missing=true must survive it.
func TestBatchUpsertBookFiles_ITunesShapedRowKeepsMissing(t *testing.T) {
	s := openRescanStore(t)
	before, bookID := seedFullyPopulatedBookFile(t, s)
	row := &BookFile{ // shape of the importer.go syncLibrary literal
		BookID:             bookID,
		FilePath:           before.FilePath,
		ITunesPath:         "file://localhost/itunes/track.m4b",
		ITunesPersistentID: before.ITunesPersistentID,
		TrackNumber:        3,
		Title:              "iTunes Track Name",
		Format:             "m4b",
		Duration:           1234,
		FileSize:           4242,
	}
	if err := s.BatchUpsertBookFiles([]*BookFile{row}); err != nil {
		t.Fatalf("BatchUpsertBookFiles: %v", err)
	}
	after := onlyBookFile(t, s, bookID)
	if !after.Missing {
		t.Error("an iTunes-shaped upsert cleared Missing without any disk evidence")
	}
	if after.TranscribeStatus == nil || after.DelugeHash != before.DelugeHash {
		t.Error("an iTunes-shaped upsert wiped fields it does not own")
	}
}

// Same bytes: a rescan whose FileHash equals the stored one preserves
// everything it does not own.
func TestBatchUpsertScannedBookFiles_SameHashPreservesEverything(t *testing.T) {
	s := openRescanStore(t)
	before, bookID := seedFullyPopulatedBookFile(t, s)
	row := scannerShapedRow(bookID, before.FilePath)
	row.FileHash = before.FileHash
	row.OriginalFileHash = before.FileHash
	row.OriginalFileHashKind = FileHashKindSampled
	if err := s.BatchUpsertScannedBookFiles([]ScannedBookFile{{File: row, Present: true}}); err != nil {
		t.Fatalf("BatchUpsertScannedBookFiles: %v", err)
	}
	after := onlyBookFile(t, s, bookID)
	if ch := changedFields(before, &after, "Missing"); len(ch) > 0 {
		t.Errorf("same-hash rescan changed fields: %v", ch)
	}
}

// M-A: a known-kind OriginalFileHash is write-once. The scanner sets it to the
// CURRENT hash on every row; the stored first-seen value must win, on the
// upsert paths and on the full-row update.
func TestOriginalFileHash_KnownKindIsWriteOnce(t *testing.T) {
	s := openRescanStore(t)
	before, bookID := seedFullyPopulatedBookFile(t, s)
	row := scannerShapedRow(bookID, before.FilePath)
	row.FileHash = before.FileHash
	row.OriginalFileHash = "a-later-hash"
	row.OriginalFileHashKind = FileHashKindSampled
	if err := s.BatchUpsertScannedBookFiles([]ScannedBookFile{{File: row, Present: true}}); err != nil {
		t.Fatalf("BatchUpsertScannedBookFiles: %v", err)
	}
	after := onlyBookFile(t, s, bookID)
	if after.OriginalFileHash != before.OriginalFileHash || after.OriginalFileHashKind != FileHashKindSampled {
		t.Errorf("upsert: OriginalFileHash = %q/%q, want the stored first-seen %q/%q",
			after.OriginalFileHash, after.OriginalFileHashKind, before.OriginalFileHash, FileHashKindSampled)
	}
	full := after
	full.OriginalFileHash = "overwrite-attempt"
	if err := s.UpdateBookFile(full.ID, &full); err != nil {
		t.Fatalf("UpdateBookFile: %v", err)
	}
	if got := onlyBookFile(t, s, bookID); got.OriginalFileHash != before.OriginalFileHash {
		t.Errorf("UpdateBookFile: OriginalFileHash = %q, want the stored first-seen %q", got.OriginalFileHash, before.OriginalFileHash)
	}
}

// Review round 3, item 2: a legacy OriginalFileHash of unknown kind (for
// example a whole-file SHA-256 an old tag write stored) is not frozen. The next
// scan replaces it with the sampled digest, and from then on it is frozen.
func TestOriginalFileHash_LegacyKindIsReplacedThenFrozen(t *testing.T) {
	s := openRescanStore(t)
	book, err := s.CreateBook(&Book{Title: "Legacy Original"})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	path := "/lib/Legacy Original/01.m4b"
	if err := s.CreateBookFile(&BookFile{BookID: book.ID, FilePath: path, FileHash: "sampled-1", OriginalFileHash: "whole-file-sha"}); err != nil {
		t.Fatalf("CreateBookFile: %v", err)
	}
	scan := func(original string) BookFile {
		row := scannerShapedRow(book.ID, path)
		row.FileHash = "sampled-1"
		row.OriginalFileHash = original
		row.OriginalFileHashKind = FileHashKindSampled
		if err := s.BatchUpsertScannedBookFiles([]ScannedBookFile{{File: row, Present: true}}); err != nil {
			t.Fatalf("BatchUpsertScannedBookFiles: %v", err)
		}
		return onlyBookFile(t, s, book.ID)
	}
	if got := scan("sampled-1"); got.OriginalFileHash != "sampled-1" || got.OriginalFileHashKind != FileHashKindSampled {
		t.Fatalf("legacy value not replaced: %q/%q", got.OriginalFileHash, got.OriginalFileHashKind)
	}
	if got := scan("sampled-2"); got.OriginalFileHash != "sampled-1" {
		t.Errorf("once of known kind, OriginalFileHash must be frozen: got %q", got.OriginalFileHash)
	}
}

// UpdateBookFileHashes stores the pre-write digest as a known-kind original
// over a legacy value, keeps a frozen one, and never recomputes the book's
// aggregates (a hash cannot change them).
func TestUpdateBookFileHashes_KindAndNoAggregateRecompute(t *testing.T) {
	s := openRescanStore(t)
	book, err := s.CreateBook(&Book{Title: "Hash Updates"})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	if err := s.CreateBookFile(&BookFile{BookID: book.ID, FilePath: "/lib/Hash Updates/01.m4b", FileHash: "pre", OriginalFileHash: "legacy-whole", Duration: 600}); err != nil {
		t.Fatalf("CreateBookFile: %v", err)
	}
	f := onlyBookFile(t, s, book.ID)

	// Make the book's aggregate deliberately stale; a recompute would fix it.
	b, err := s.GetBookByID(book.ID)
	if err != nil || b == nil {
		t.Fatalf("GetBookByID: %v", err)
	}
	stale := 1
	b.Duration = &stale
	if _, err := s.UpdateBook(b.ID, b); err != nil {
		t.Fatalf("UpdateBook: %v", err)
	}

	if err := s.UpdateBookFileHashes(f.ID, "pre", "post-whole-sha", "post-sampled"); err != nil {
		t.Fatalf("UpdateBookFileHashes: %v", err)
	}
	got := onlyBookFile(t, s, book.ID)
	if got.OriginalFileHash != "pre" || got.OriginalFileHashKind != FileHashKindSampled {
		t.Errorf("legacy original not replaced by the pre-write sampled digest: %q/%q", got.OriginalFileHash, got.OriginalFileHashKind)
	}
	if got.FileHash != "post-sampled" || got.PostMetadataHash != "post-whole-sha" {
		t.Errorf("hashes = %q/%q, want post-sampled/post-whole-sha", got.FileHash, got.PostMetadataHash)
	}
	if b2, _ := s.GetBookByID(book.ID); b2 == nil || b2.Duration == nil || *b2.Duration != stale {
		t.Errorf("a hash-only update recomputed the book aggregates (Duration = %v, want the untouched %d)", b2.Duration, stale)
	}

	if err := s.UpdateBookFileHashes(f.ID, "second-pre", "post-2", "post-sampled-2"); err != nil {
		t.Fatalf("UpdateBookFileHashes: %v", err)
	}
	if got := onlyBookFile(t, s, book.ID); got.OriginalFileHash != "pre" {
		t.Errorf("a frozen original was replaced: %q", got.OriginalFileHash)
	}

	// Control: a full-row update of the same file does recompute.
	full := onlyBookFile(t, s, book.ID)
	if err := s.UpdateBookFile(full.ID, &full); err != nil {
		t.Fatalf("UpdateBookFile: %v", err)
	}
	if b3, _ := s.GetBookByID(book.ID); b3 == nil || b3.Duration == nil || *b3.Duration == stale {
		t.Error("control failed: UpdateBookFile did not recompute the aggregate, so the skip assertion above proves nothing")
	}
}

// Review round 3, item 5: two identical files share a content hash, and the
// book_file_hash: index points at whichever row wrote it last. A hash update
// on the OTHER row must not delete that entry.
func TestHashIndex_UpdateOnOneOfTwoIdenticalFilesKeepsTheOwnersEntry(t *testing.T) {
	s := openRescanStore(t)
	bookA, err := s.CreateBook(&Book{Title: "Copy A"})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	bookB, err := s.CreateBook(&Book{Title: "Copy B"})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	if err := s.CreateBookFile(&BookFile{BookID: bookA.ID, FilePath: "/lib/A/01.m4b", FileHash: "dup"}); err != nil {
		t.Fatalf("CreateBookFile A: %v", err)
	}
	if err := s.CreateBookFile(&BookFile{BookID: bookB.ID, FilePath: "/lib/B/01.m4b", FileHash: "dup"}); err != nil {
		t.Fatalf("CreateBookFile B: %v", err)
	}
	if got, _ := s.GetBookBySegmentFileHash("dup"); got == nil || got.ID != bookB.ID {
		t.Fatalf("precondition: the dup entry should point at the last writer (book B), got %v", got)
	}

	a := onlyBookFile(t, s, bookA.ID)
	if err := s.UpdateBookFileHashes(a.ID, "", "post", "a-rewritten"); err != nil {
		t.Fatalf("UpdateBookFileHashes: %v", err)
	}
	if got, _ := s.GetBookBySegmentFileHash("dup"); got == nil || got.ID != bookB.ID {
		t.Errorf("book B's hash entry was dropped by an update to book A's file: got %v", got)
	}
	if got, _ := s.GetBookBySegmentFileHash("a-rewritten"); got == nil || got.ID != bookA.ID {
		t.Errorf("book A's new hash was not indexed: got %v", got)
	}
}

// expectAfterChange checks the changed-hash contract against the table:
// derivedBytes fields take the incoming row's value (zero unless supplied),
// derivedAudio fields do too when audioDropped, and every other field the row
// does not own keeps the stored value.
func expectAfterChange(t *testing.T, before, row, after *BookFile, audioDropped bool, extraSkip ...string) {
	t.Helper()
	skip := map[string]bool{"FileHash": true}
	for _, n := range extraSkip {
		skip[n] = true
	}
	bv, rv, av := reflect.ValueOf(*before), reflect.ValueOf(*row), reflect.ValueOf(*after)
	for i := range av.NumField() {
		name := av.Type().Field(i).Name
		if _, s := rescanNotCompared[name]; s || skip[name] {
			continue
		}
		want := bv.Field(i).Interface()
		switch d := bookFileFieldClasses[name].derived; {
		case d == derivedBytes, d == derivedAudio && audioDropped:
			want = rv.Field(i).Interface()
		}
		if !reflect.DeepEqual(want, av.Field(i).Interface()) {
			t.Errorf("%s after a changed-hash rescan: got %v, want %v", name, av.Field(i).Interface(), want)
		}
	}
	if after.DelugeHash != before.DelugeHash || after.ITunesPersistentID != before.ITunesPersistentID ||
		after.VersionID != before.VersionID || after.OrganizeMethod != before.OrganizeMethod ||
		after.SkipScan != before.SkipScan || after.OriginalFileHash != before.OriginalFileHash {
		t.Error("provenance/state fields were not kept across a changed-hash rescan")
	}
	if after.FileHash != row.FileHash {
		t.Errorf("FileHash = %q, want the new %q", after.FileHash, row.FileHash)
	}
}

// Review round 3, item 1: a changed hash with no duration to compare is a file
// that changed in a way nothing verified. Its audio-derived data belongs to
// the old bytes and is dropped; nothing claims a later re-check.
func TestBatchUpsertScannedBookFiles_ChangedHashNoDurationDropsAudio(t *testing.T) {
	s := openRescanStore(t)
	before, bookID := seedWithNeedsRescanFalse(t, s)
	row := scannerShapedRow(bookID, before.FilePath)
	row.FileHash = "new-bytes-hash"
	row.OriginalFileHash = "new-bytes-hash"
	row.OriginalFileHashKind = FileHashKindSampled
	rowCopy := *row
	if err := s.BatchUpsertScannedBookFiles([]ScannedBookFile{{File: row, Present: true}}); err != nil {
		t.Fatalf("BatchUpsertScannedBookFiles: %v", err)
	}
	after := onlyBookFile(t, s, bookID)
	expectAfterChange(t, before, &rowCopy, &after, true, "Missing")
	if len(after.AcoustIDFingerprint) != 0 || after.IntroTranscription != nil || after.Duration != 0 || after.TranscribeStatus != nil {
		t.Error("an unverified content change kept the old file's fingerprint, transcript or duration")
	}
	if after.NeedsRescan == nil || *after.NeedsRescan {
		t.Error("the merge must not touch NeedsRescan (it is the scan cache's flag)")
	}
}

// The audio really changed (duration differs by more than a second): the
// audio-derived fields drop. Single-row path, which must not restore the
// memdb-stripped fingerprint or transcript through UpdateBookFile.
func TestUpsertBookFile_ChangedHashAndDurationDropsAudio(t *testing.T) {
	s := openRescanStore(t)
	before, bookID := seedFullyPopulatedBookFile(t, s)
	row := scannerShapedRow(bookID, before.FilePath)
	row.FileHash = "new-bytes-hash"
	row.Duration = before.Duration + 600
	rowCopy := *row
	if err := s.UpsertBookFile(row); err != nil {
		t.Fatalf("UpsertBookFile: %v", err)
	}
	after := onlyBookFile(t, s, bookID)
	expectAfterChange(t, before, &rowCopy, &after, true)
	if len(after.AcoustIDFingerprint) != 0 || after.IntroTranscription != nil || after.TranscribeStatus != nil {
		t.Error("audio-derived data of the replaced file survived")
	}
	if !after.Missing {
		t.Error("UpsertBookFile has not stat'd the file and must keep Missing=true")
	}
}

// A codec change drops the audio too, even at the same duration.
func TestBatchUpsertBookFiles_ChangedHashSameDurationOtherCodecDropsAudio(t *testing.T) {
	s := openRescanStore(t)
	before, bookID := seedFullyPopulatedBookFile(t, s)
	row := scannerShapedRow(bookID, before.FilePath)
	row.FileHash = "new-bytes-hash"
	row.Duration = before.Duration
	row.Codec = before.Codec + "-other"
	rowCopy := *row
	if err := s.BatchUpsertBookFiles([]*BookFile{row}); err != nil {
		t.Fatalf("BatchUpsertBookFiles: %v", err)
	}
	after := onlyBookFile(t, s, bookID)
	expectAfterChange(t, before, &rowCopy, &after, true)
}

// Same audio, re-tagged: the hash changed but the duration is within a second
// (and the codec matches, case-insensitively), so the audio-derived fields
// stay.
func TestBatchUpsertBookFiles_ChangedHashSameDurationKeepsAudio(t *testing.T) {
	s := openRescanStore(t)
	before, bookID := seedWithNeedsRescanFalse(t, s)
	row := scannerShapedRow(bookID, before.FilePath)
	row.FileHash = "new-bytes-hash"
	row.Duration = before.Duration + 1
	row.Codec = before.Codec
	rowCopy := *row
	if err := s.BatchUpsertBookFiles([]*BookFile{row}); err != nil {
		t.Fatalf("BatchUpsertBookFiles: %v", err)
	}
	after := onlyBookFile(t, s, bookID)
	expectAfterChange(t, before, &rowCopy, &after, false, "Duration")
	if len(after.AcoustIDFingerprint) == 0 || after.IntroTranscription == nil {
		t.Error("a verified same-audio change lost the fingerprint or transcript")
	}
}

// m1: a scanned row whose stat failed is never judged changed, so nothing
// content-derived drops.
func TestBatchUpsertScannedBookFiles_ChangedHashButStatFailedDropsNothing(t *testing.T) {
	s := openRescanStore(t)
	before, bookID := seedWithNeedsRescanFalse(t, s)
	row := scannerShapedRow(bookID, before.FilePath)
	row.FileHash = "new-bytes-hash"
	if err := s.BatchUpsertScannedBookFiles([]ScannedBookFile{{File: row, Present: false}}); err != nil {
		t.Fatalf("BatchUpsertScannedBookFiles: %v", err)
	}
	after := onlyBookFile(t, s, bookID)
	if ch := changedFields(before, &after, "FileHash"); len(ch) > 0 {
		t.Errorf("a changed hash on a row whose stat failed changed: %v", ch)
	}
}
