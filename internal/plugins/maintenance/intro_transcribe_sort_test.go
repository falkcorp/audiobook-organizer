// file: internal/plugins/maintenance/intro_transcribe_sort_test.go
// version: 1.0.0
// guid: 4f2b9c81-7d36-4a5e-9b02-3c8e1a6d5f47
// last-edited: 2026-08-06

package maintenance

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// newSortStore returns a MockStore serving a fixed BookFile set for any bookID.
// Deliberately named for this test file only — a generic helper name in this
// package would collide with sibling tests added by parallel work.
func newSortStore(files []database.BookFile) *database.MockStore {
	return &database.MockStore{
		GetBookFilesFunc: func(_ string) ([]database.BookFile, error) {
			return files, nil
		},
	}
}

// TestNthAudioFile_MultiDiscPicksDisc1Track1 pins the first-file selection for a
// genuine multi-disc set — the case the old (track, path) comparator got wrong.
//
// The old sort ignored DiscNumber entirely, so disc-1-track-1 and disc-2-track-1
// TIED on track number and FilePath broke the tie. Here disc 2's paths sort
// lexically BEFORE disc 1's ("/lib/a-disc2/..." < "/lib/b-disc1/..."), so the old
// comparator returned disc 2's opening as "the first file". The correct answer is
// disc 1, track 1.
//
// This matters beyond picking a wrong sample: the per-file intro signal rests on
// "track 1 carries the spoken opening, tracks 2..N do not", with position as the
// discriminator. A sort that disagrees about which row is track 1 makes the
// discriminator read the wrong row.
//
// Input order is deliberately scrambled so the test exercises nthAudioFile's own
// comparator rather than any ordering a store happened to return.
func TestNthAudioFile_MultiDiscPicksDisc1Track1(t *testing.T) {
	files := []database.BookFile{
		{ID: "d2t2", BookID: "b1", FilePath: "/lib/a-disc2/track02.mp3", DiscNumber: 2, TrackNumber: 2},
		{ID: "d1t2", BookID: "b1", FilePath: "/lib/b-disc1/track02.mp3", DiscNumber: 1, TrackNumber: 2},
		{ID: "d2t1", BookID: "b1", FilePath: "/lib/a-disc2/track01.mp3", DiscNumber: 2, TrackNumber: 1},
		{ID: "d1t1", BookID: "b1", FilePath: "/lib/b-disc1/track01.mp3", DiscNumber: 1, TrackNumber: 1},
	}
	store := newSortStore(files)
	book := database.Book{ID: "b1"}

	_, _, gotID, err := firstAudioFile(store, book)
	if err != nil {
		t.Fatalf("firstAudioFile: %v", err)
	}
	if gotID != "d1t1" {
		t.Errorf("first file = %q, want %q (disc 1 track 1); "+
			"a disc-blind sort returns %q because disc 2's path sorts first",
			gotID, "d1t1", "d2t1")
	}

	// Full (disc, track, path) order — matches PebbleStore.GetBookFiles.
	want := []string{"d1t1", "d1t2", "d2t1", "d2t2"}
	for n, wantID := range want {
		_, _, gotID, err := nthAudioFile(store, book, n)
		if err != nil {
			t.Fatalf("nthAudioFile(%d): %v", n, err)
		}
		if gotID != wantID {
			t.Errorf("nthAudioFile(%d) = %q, want %q", n, gotID, wantID)
		}
	}
}

// TestNthAudioFile_FlattenedDiscsUnaffected covers the shape the iTunes regroup
// path produces: assignDiscTrack (internal/itunes/service/fs_regroup_shape.go)
// stamps DiscNumber=0 and TrackNumber=1..N contiguously over play order, per the
// owner decision that discs are flattened. With disc constant the disc key is
// inert and ordering falls through to track — so the fix must be a no-op here.
func TestNthAudioFile_FlattenedDiscsUnaffected(t *testing.T) {
	files := []database.BookFile{
		{ID: "t3", BookID: "b1", FilePath: "/lib/bk/c.mp3", DiscNumber: 0, TrackNumber: 3},
		{ID: "t1", BookID: "b1", FilePath: "/lib/bk/a.mp3", DiscNumber: 0, TrackNumber: 1},
		{ID: "t2", BookID: "b1", FilePath: "/lib/bk/b.mp3", DiscNumber: 0, TrackNumber: 2},
	}
	store := newSortStore(files)
	book := database.Book{ID: "b1"}

	for n, wantID := range []string{"t1", "t2", "t3"} {
		_, _, gotID, err := nthAudioFile(store, book, n)
		if err != nil {
			t.Fatalf("nthAudioFile(%d): %v", n, err)
		}
		if gotID != wantID {
			t.Errorf("nthAudioFile(%d) = %q, want %q", n, gotID, wantID)
		}
	}
}

// TestNthAudioFile_UntaggedFallsBackToPath covers rows with no disc/track tags at
// all (both zero): ordering must fall through to FilePath, unchanged by the fix.
func TestNthAudioFile_UntaggedFallsBackToPath(t *testing.T) {
	files := []database.BookFile{
		{ID: "z", BookID: "b1", FilePath: "/lib/bk/z.mp3"},
		{ID: "a", BookID: "b1", FilePath: "/lib/bk/a.mp3"},
	}
	store := newSortStore(files)

	_, _, gotID, err := firstAudioFile(store, database.Book{ID: "b1"})
	if err != nil {
		t.Fatalf("firstAudioFile: %v", err)
	}
	if gotID != "a" {
		t.Errorf("first file = %q, want %q (lowest path when disc+track are absent)", gotID, "a")
	}
}
