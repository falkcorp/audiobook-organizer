// file: internal/database/pebble_store_book_media_test.go
// version: 1.0.0
// guid: 2bd37725-b63d-4529-8450-c4e8858d385b
// last-edited: 2026-09-13

package database

import (
	"testing"
)

func intp(v int) *int       { return &v }
func strp(v string) *string { return &v }

// TestFillBookMediaInfo_KeepsConcurrentApply is the GET-reverts-apply bug at
// the store: the read path read the row, an apply rewrote it, then the read
// path saved. Before the fix that save was UpdateBook(staleRow) and put the
// old title back; FillBookMediaInfo re-reads, so the apply's title survives.
func TestFillBookMediaInfo_KeepsConcurrentApply(t *testing.T) {
	s := newAtPathStore(t)
	defer s.Close()

	b, err := s.CreateBook(&Book{Title: "Old Title", FilePath: "/lib/a.m4b"})
	if err != nil {
		t.Fatal(err)
	}
	stale, err := s.GetBookByID(b.ID) // the GET's read
	if err != nil {
		t.Fatal(err)
	}

	applied, err := s.GetBookByID(b.ID) // a metadata apply lands meanwhile
	if err != nil {
		t.Fatal(err)
	}
	applied.Title = "Applied Title"
	if _, err := s.UpdateBook(b.ID, applied); err != nil {
		t.Fatal(err)
	}

	if stale.Duration != nil {
		t.Fatalf("precondition: stale row has a duration")
	}
	got, err := s.FillBookMediaInfo(b.ID, BookMediaInfoPatch{Duration: intp(3600), Codec: strp("aac")})
	if err != nil {
		t.Fatal(err)
	}
	row, err := s.GetBookByID(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	for name, r := range map[string]*Book{"returned": got, "stored": row} {
		if r.Title != "Applied Title" {
			t.Errorf("%s title = %q, want the apply's %q (reverted)", name, r.Title, "Applied Title")
		}
		if r.Duration == nil || *r.Duration != 3600 {
			t.Errorf("%s duration = %v, want 3600", name, r.Duration)
		}
		if r.Codec == nil || *r.Codec != "aac" {
			t.Errorf("%s codec = %v, want aac", name, r.Codec)
		}
	}
}

// TestFillBookMediaInfo_NoWriteWhenNothingEmpty: every patched field is
// already set, so nothing is written -- no snapshot, no UpdatedAt bump -- and
// the stored values win over the patch.
func TestFillBookMediaInfo_NoWriteWhenNothingEmpty(t *testing.T) {
	s := newAtPathStore(t)
	defer s.Close()

	b, err := s.CreateBook(&Book{Title: "t", FilePath: "/lib/b.m4b", Duration: intp(100), Codec: strp("mp3")})
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.GetBookByID(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	snaps, err := s.CountBookSnapshots(b.ID)
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.FillBookMediaInfo(b.ID, BookMediaInfoPatch{Duration: intp(999), Codec: strp("aac")})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || *got.Duration != 100 || *got.Codec != "mp3" {
		t.Fatalf("returned row = %+v, want stored duration 100 / codec mp3 untouched", got)
	}
	after, err := s.GetBookByID(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before.UpdatedAt == nil || after.UpdatedAt == nil || !before.UpdatedAt.Equal(*after.UpdatedAt) {
		t.Errorf("UpdatedAt changed %v -> %v: a no-op fill wrote the row", before.UpdatedAt, after.UpdatedAt)
	}
	snapsAfter, err := s.CountBookSnapshots(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if snapsAfter != snaps {
		t.Errorf("snapshots %d -> %d: a no-op fill wrote a version", snaps, snapsAfter)
	}
}

// TestFillBookMediaInfo_FillsOnlyEmptyFields: a field set by another writer
// is kept; only the empty one is filled.
func TestFillBookMediaInfo_FillsOnlyEmptyFields(t *testing.T) {
	s := newAtPathStore(t)
	defer s.Close()

	b, err := s.CreateBook(&Book{Title: "t", FilePath: "/lib/c.m4b", Bitrate: intp(64)})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.FillBookMediaInfo(b.ID, BookMediaInfoPatch{Bitrate: intp(128), SampleRate: intp(44100)})
	if err != nil {
		t.Fatal(err)
	}
	if *got.Bitrate != 64 {
		t.Errorf("bitrate = %d, want the stored 64 kept", *got.Bitrate)
	}
	if got.SampleRate == nil || *got.SampleRate != 44100 {
		t.Errorf("sample rate = %v, want 44100 filled", got.SampleRate)
	}
}

// TestFillBookMediaInfo_MissingBook returns nil, nil rather than creating.
func TestFillBookMediaInfo_MissingBook(t *testing.T) {
	s := newAtPathStore(t)
	defer s.Close()
	got, err := s.FillBookMediaInfo("no-such-id", BookMediaInfoPatch{Duration: intp(1)})
	if err != nil || got != nil {
		t.Fatalf("got %v, %v; want nil, nil", got, err)
	}
}
