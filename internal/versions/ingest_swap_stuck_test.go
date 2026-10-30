// file: internal/versions/ingest_swap_stuck_test.go
// version: 1.0.0
// guid: db0d3556-7233-4032-9246-a1b38825670c
// last-edited: 2026-10-30
//
// CreateIngestVersion into a book whose version swap did not finish
// (TODO-INGEST-ACTIVE-WHILE-SWAP-STUCK): the book has no active version, but
// the new download must still be an alt version, or ResumeVersionSwap refuses.

package versions

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// ingestDownload ingests a new file (outside the book's folder, like a Deluge
// download) into the fixture's book: the book_file row first, then the version.
func (f *swapFixture) ingestDownload(store IngestStore, name string) *database.BookVersion {
	f.t.Helper()
	dir := f.t.TempDir()
	allowDir(f.t, f.store, dir)
	path := filepath.Join(dir, name)
	writeTestFile(f.t, path, "download-"+name)
	if err := f.store.CreateBookFile(&database.BookFile{
		BookID: f.book.ID, FilePath: path, Format: "m4b", FileSize: int64(len("download-" + name)),
	}); err != nil {
		f.t.Fatal(err)
	}
	ver, linked, err := CreateIngestVersion(store, IngestVersionParams{
		BookID: f.book.ID, FilePath: path, Format: "m4b", Source: "deluge",
	})
	if err != nil {
		f.t.Fatalf("ingest %s: %v", name, err)
	}
	if !linked {
		f.t.Errorf("ingest %s: the version was not linked to its row", name)
	}
	return ver
}

// A swap left in swapping_out / swapping_in: the new download is alt, and the
// resume then finishes the swap instead of refusing for a second active version.
func TestCreateIngestVersion_StuckSwapGetsAltAndResumeStillWorks(t *testing.T) {
	f := newSwapFixture(t)
	alt := f.add(database.BookVersionStatusAlt, time.Hour, "Hush.m4b")
	fs := f.faults()
	fs.failFileRow = f.rowOf(alt.ID).ID
	f.swapWith(fs, alt)
	if act, _ := f.store.GetActiveVersionForBook(f.book.ID); act != nil {
		t.Fatalf("the stuck book has an active version %s, want none", act.ID)
	}

	late := f.ingestDownload(f.store, "Year One.m4b")

	if late.Status != database.BookVersionStatusAlt {
		t.Fatalf("download ingested into a stuck swap is %q, want alt", late.Status)
	}
	res := f.mustResume(SwapResumeFinish)
	if res.ActiveVersionID != alt.ID {
		t.Errorf("resume made %s active, want the swapped-in version %s", res.ActiveVersionID, alt.ID)
	}
	f.requireSettled(alt.ID)
	if got := f.status(late.ID); got != database.BookVersionStatusAlt {
		t.Errorf("the download is %q after the resume, want it still alt", got)
	}
}

// The other stuck shapes: a lone swapping_out (resume undoes it) and a lone
// swapping_in beside nothing (resume finishes it).
func TestCreateIngestVersion_LoneSwappingVersionGetsAlt(t *testing.T) {
	t.Run("lone swapping_out", func(t *testing.T) {
		f := newSwapFixture(t)
		f.setStatus(f.active.ID, database.BookVersionStatusSwappingOut)
		late := f.ingestDownload(f.store, "Year One.m4b")
		if late.Status != database.BookVersionStatusAlt {
			t.Fatalf("download is %q, want alt", late.Status)
		}
		f.mustResume(SwapResumeUndo)
		f.requireSettled(f.active.ID)
	})
	t.Run("lone swapping_in", func(t *testing.T) {
		f := newSwapFixture(t)
		alt, _, _ := f.enteringMovedToTheFolder()
		f.setStatus(alt.ID, database.BookVersionStatusSwappingIn)
		f.setStatus(f.active.ID, database.BookVersionStatusAlt)
		// Move the row to the folder too, so the swap looks like it stopped
		// at the status writes.
		row := f.rowOf(alt.ID)
		f.modifyRow(row.ID, func(r *database.BookFile) { r.FilePath = filepath.Join(f.bookDir, "Hush.m4b") })
		late := f.ingestDownload(f.store, "Year One.m4b")
		if late.Status != database.BookVersionStatusAlt {
			t.Fatalf("download is %q, want alt", late.Status)
		}
	})
}

// A healthy book with an active version still gets an alt.
func TestCreateIngestVersion_HealthyActiveStillGetsAlt(t *testing.T) {
	f := newSwapFixture(t)
	late := f.ingestDownload(f.store, "Year One.m4b")
	if late.Status != database.BookVersionStatusAlt {
		t.Errorf("download is %q, want alt", late.Status)
	}
	f.requireSettled(f.active.ID)
}

// A book with no versions at all (not a new-book import, so the stuck-swap
// lookup runs) still gets an active one.
func TestCreateIngestVersion_NoVersionsStillGetsActive(t *testing.T) {
	f := newSwapFixture(t)
	book, err := f.store.CreateBook(&database.Book{Title: "Year One", FilePath: filepath.Join(f.bookDir, "Year One.m4b"), Format: "m4b"})
	if err != nil {
		t.Fatal(err)
	}
	f.book = book
	late := f.ingestDownload(f.store, "Year One.m4b")
	if late.Status != database.BookVersionStatusActive {
		t.Errorf("first version is %q, want active", late.Status)
	}
}

// failVersionsReadStore cannot list a book's versions.
type failVersionsReadStore struct{ *database.PebbleStore }

func (failVersionsReadStore) GetBookVersionsByBookID(string) ([]database.BookVersion, error) {
	return nil, errors.New("injected: cannot list versions")
}

// When the check for an unfinished swap cannot be made the download is neither
// refused nor made active: it is added as alt. With a matching book_file row the
// stranded-version lookup reads the same versions first and refuses on its own
// (as it always has), so the check's own fallback is reached by an ingest that
// matches no row.
func TestCreateIngestVersion_UnreadableVersionsFallBackToAlt(t *testing.T) {
	f := newSwapFixture(t)
	// No active version, so only the check itself can keep this one alt.
	f.setStatus(f.active.ID, database.BookVersionStatusSwappingOut)
	dir := t.TempDir()
	allowDir(t, f.store, dir)
	ver, linked, err := CreateIngestVersion(failVersionsReadStore{f.store}, IngestVersionParams{
		BookID: f.book.ID, FilePath: filepath.Join(dir, "Year One.m4b"), Format: "m4b", Source: "deluge",
	})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if linked {
		t.Error("no row matches the path, so the version cannot be linked")
	}
	if ver.Status != database.BookVersionStatusAlt {
		t.Errorf("download is %q, want alt", ver.Status)
	}
}
