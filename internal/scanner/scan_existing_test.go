// file: internal/scanner/scan_existing_test.go
// version: 1.0.0
// guid: f5390ae9-0910-4c20-811f-36ab977aec8e
// last-edited: 2026-10-06

package scanner

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// holdFixture is a pebble-backed scanner with one existing book imported
// under stored values, and helpers to count rows and read identity.
type holdFixture struct {
	t     *testing.T
	store *database.PebbleStore
	root  string
}

func newHoldFixture(t *testing.T) *holdFixture {
	t.Helper()
	store, cleanup := setupPebbleStore(t)
	t.Cleanup(cleanup)
	prevStore := database.GetGlobalStore()
	database.SetGlobalStore(store)
	SetStore(store)
	t.Cleanup(func() {
		database.SetGlobalStore(prevStore)
		SetStore(nil)
	})
	prevConfig := config.AppConfig
	t.Cleanup(func() { config.AppConfig = prevConfig })
	root := t.TempDir()
	config.AppConfig.RootDir = root
	return &holdFixture{t: t, store: store, root: root}
}

func (f *holdFixture) write(rel, content string) string {
	f.t.Helper()
	p := filepath.Join(f.root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
	return p
}

type rowCounts struct{ books, authors, series, works int }

func (f *holdFixture) counts() rowCounts {
	f.t.Helper()
	b, err := f.store.GetAllBooksCore(0, 0)
	if err != nil {
		f.t.Fatal(err)
	}
	a, err := f.store.GetAllAuthors()
	if err != nil {
		f.t.Fatal(err)
	}
	s, err := f.store.GetAllSeries()
	if err != nil {
		f.t.Fatal(err)
	}
	w, err := f.store.GetAllWorks()
	if err != nil {
		f.t.Fatal(err)
	}
	return rowCounts{len(b), len(a), len(s), len(w)}
}

type identity struct {
	title, author, series, narrator, work string
	position                              int
}

func (f *holdFixture) identityOf(b *database.Book) identity {
	f.t.Helper()
	id := identity{title: b.Title}
	if b.AuthorID != nil {
		if a, _ := f.store.GetAuthorByID(*b.AuthorID); a != nil {
			id.author = a.Name
		}
	}
	if b.SeriesID != nil {
		if s, _ := f.store.GetSeriesByID(*b.SeriesID); s != nil {
			id.series = s.Name
		}
	}
	if b.Narrator != nil {
		id.narrator = *b.Narrator
	}
	if b.WorkID != nil {
		id.work = *b.WorkID
	}
	if b.SeriesSequence != nil {
		id.position = *b.SeriesSequence
	}
	return id
}

func (f *holdFixture) byID(id string) *database.Book {
	f.t.Helper()
	b, err := f.store.GetBookByID(id)
	if err != nil || b == nil {
		f.t.Fatalf("GetBookByID(%s): %v", id, err)
	}
	return b
}

// importStored saves book as an earlier import (no folder-parse sources).
func (f *holdFixture) importStored(book *Book) *database.Book {
	f.t.Helper()
	if err := saveBookToDatabase(context.Background(), book); err != nil {
		f.t.Fatal(err)
	}
	b, err := f.store.GetBookByFilePath(book.FilePath)
	if err != nil || b == nil {
		f.t.Fatalf("stored import of %s: %v", book.FilePath, err)
	}
	return b
}

// reparsed is a scan of path whose every identity field the new folder
// parse reads differently from the stored row.
func reparsed(path string) *Book {
	return &Book{FilePath: path, Title: "Reparsed Title", Author: "Dorian Vex", Series: "Reparsed Series", Position: 5,
		Narrator: "Reparsed Narrator", Format: ".m4b", Duration: 100,
		folderParse: folderParsed{Title: "Reparsed Title", Author: "Dorian Vex", Series: "Reparsed Series",
			Narrator: "Reparsed Narrator"}}
}

var storedBook = identity{title: "Stored Title", author: "Mara Quill", series: "Stored Series",
	narrator: "Stored Narrator", position: 2}

func storedScan(path string) *Book {
	return &Book{FilePath: path, Title: "Stored Title", Author: "Mara Quill", Series: "Stored Series", Position: 2,
		Narrator: "Stored Narrator", Format: ".m4b", Duration: 100}
}

func sameIdentity(got, want identity) bool {
	got.work, want.work = "", ""
	return got == want
}

// A tagged move: the file carries the book's AUDIOBOOK_ORGANIZER_ID and sits
// at a new path. The relink repoints the row; the folder parse of the new
// path must not mint author, series or work rows first (the hold used to
// look at the path alone, find nothing, and resolve the new parse's values
// before the relink returned).
func TestSaveBookToDatabase_MovedBookHoldsAgainstOrganizerIDRow(t *testing.T) {
	f := newHoldFixture(t)
	old := f.write("old/book.m4b", "moved book bytes")
	stored := f.importStored(storedScan(old))
	want := f.identityOf(stored)
	before := f.counts()

	moved := f.write("new place/book.m4b", "moved book bytes")
	if err := os.Remove(old); err != nil {
		t.Fatal(err)
	}
	scan := reparsed(moved)
	scan.BookOrganizerID = stored.ID
	if err := saveBookToDatabase(context.Background(), scan); err != nil {
		t.Fatal(err)
	}
	if after := f.counts(); after != before {
		t.Fatalf("a moved book created rows: %+v -> %+v", before, after)
	}
	got := f.byID(stored.ID)
	if got.FilePath != moved {
		t.Fatalf("relink: path %q, want %q", got.FilePath, moved)
	}
	if id := f.identityOf(got); id != want {
		t.Fatalf("moved book rewritten: %+v, want %+v", id, want)
	}
}

// An untagged rename: same content, new path, no organizer-ID tag. The hash
// lookup finds the stored row and the scan version-links a new row to it
// (main's untagged-move handling, unchanged here). The new row takes the
// stored identity, the stored row is not rewritten, and no author, series or
// work row comes from the new parse.
func TestSaveBookToDatabase_RenamedBookHoldsAgainstHashMatch(t *testing.T) {
	f := newHoldFixture(t)
	old := f.write("book/Stored Title.m4b", "renamed book bytes")
	stored := f.importStored(storedScan(old))
	want := f.identityOf(stored)
	before := f.counts()

	renamed := f.write("book/Reparsed Title.m4b", "renamed book bytes")
	if err := saveBookToDatabase(context.Background(), reparsed(renamed)); err != nil {
		t.Fatal(err)
	}
	after := f.counts()
	if after.authors != before.authors || after.series != before.series || after.works != before.works {
		t.Fatalf("a renamed book created author/series/work rows: %+v -> %+v", before, after)
	}
	if id := f.identityOf(f.byID(stored.ID)); !sameIdentity(id, want) || id.work != want.work {
		t.Fatalf("stored row rewritten: %+v, want %+v", id, want)
	}
	nb, err := f.store.GetBookByFilePath(renamed)
	if err != nil {
		t.Fatal(err)
	}
	if nb != nil {
		if id := f.identityOf(nb); !sameIdentity(id, storedBook) || id.work != want.work {
			t.Fatalf("the renamed copy took the new parse: %+v, want the stored identity %+v", id, want)
		}
	}
}

// A multi-file book whose first file changed (a re-sorted or renamed chapter
// order: the group's path is only its first file). The ownership check sees
// one live book owning every scanned file, all of its files among them --
// the same book. The rescan merges into it: no new book (the segment vote
// used to match the book to itself and mint a second row over the same
// files), no author, series or work row, nothing rewritten.
func TestSaveBookToDatabase_MultiFileOrderChangeIsTheSameBook(t *testing.T) {
	f := newHoldFixture(t)
	a := f.write("multi/01.mp3", "chapter one bytes")
	b := f.write("multi/02.mp3", "chapter two bytes")
	c := f.write("multi/03.mp3", "chapter three bytes")
	first := storedScan(a)
	first.Format = ".mp3"
	first.SegmentFiles = []string{a, b, c}
	stored := f.importStored(first)
	want := f.identityOf(stored)
	var rows []*database.BookFile
	for i, p := range []string{a, b, c} {
		h, err := ComputeFileHash(p)
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, &database.BookFile{BookID: stored.ID, FilePath: p, FileHash: h, TrackNumber: i + 1})
	}
	if err := f.store.BatchCreateBookFiles(rows); err != nil {
		t.Fatal(err)
	}
	before := f.counts()

	scan := reparsed(b)
	scan.Format = ".mp3"
	scan.SegmentFiles = []string{b, a, c}
	if err := saveBookToDatabase(context.Background(), scan); err != nil {
		t.Fatal(err)
	}
	if after := f.counts(); after != before {
		t.Fatalf("a multi-file order change created rows: %+v -> %+v", before, after)
	}
	if id := f.identityOf(f.byID(stored.ID)); id != want {
		t.Fatalf("multi-file book rewritten: %+v, want %+v", id, want)
	}
}

// The title lock holds the work and the series position the scan derived
// from the title it was not allowed to write.
func TestApplyScannerFields_TitleLockHoldsWorkAndPosition(t *testing.T) {
	work, other := "work-stored", "work-scanned"
	seq, scannedSeq := 2, 7
	dst := &database.Book{Title: "Stored", WorkID: &work, SeriesSequence: &seq}
	scanned := &database.Book{Title: "Scanned", WorkID: &other, SeriesSequence: &scannedSeq}
	book := &Book{Title: "Scanned", folderParse: folderParsed{Title: "Scanned"}}
	applyScannerFields(dst, scanned, folderDerivedLocks(nil, book))
	if dst.Title != "Stored" || *dst.WorkID != work || *dst.SeriesSequence != seq {
		t.Fatalf("held title leaked: title=%q work=%q seq=%d", dst.Title, *dst.WorkID, *dst.SeriesSequence)
	}
	applyScannerFields(dst, scanned, map[string]bool{database.FieldKeyAuthorName: true})
	if *dst.WorkID != work {
		t.Fatalf("an author lock must hold the work too, got %q", *dst.WorkID)
	}
	applyScannerFields(dst, scanned, nil)
	if *dst.WorkID != other || *dst.SeriesSequence != scannedSeq {
		t.Fatalf("unlocked: work=%q seq=%d, want the scanned values", *dst.WorkID, *dst.SeriesSequence)
	}
}
