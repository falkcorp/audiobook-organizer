// file: internal/foldernames/evidence_test.go
// version: 1.1.0
// guid: 6cbae67b-604d-4b23-be02-29ef9669f74f
// last-edited: 2026-10-06

package foldernames

import (
	"fmt"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

type library struct {
	t     *testing.T
	store *database.PebbleStore
	n     int
}

func newLibrary(t *testing.T) *library {
	t.Helper()
	store, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := database.RunMigrations(store); err != nil {
		t.Fatal(err)
	}
	return &library{t: t, store: store}
}

func (l *library) author(name string) *int {
	l.t.Helper()
	a, err := l.store.CreateAuthor(name)
	if err != nil {
		l.t.Fatal(err)
	}
	return &a.ID
}

func (l *library) series(name string, authorID *int) *int {
	l.t.Helper()
	s, err := l.store.CreateSeries(name, authorID)
	if err != nil {
		l.t.Fatal(err)
	}
	return &s.ID
}

func (l *library) book(title string, authorID, seriesID *int) {
	l.t.Helper()
	l.n++
	if _, err := l.store.CreateBook(&database.Book{Title: title, AuthorID: authorID, SeriesID: seriesID,
		FilePath: fmt.Sprintf("/srv/library/book-%d.m4b", l.n)}); err != nil {
		l.t.Fatal(err)
	}
}

func (l *library) snapshot() *Snapshot {
	l.t.Helper()
	s, err := Load(l.store)
	if err != nil {
		l.t.Fatal(err)
	}
	return s
}

// A series row named after its own author ("Brandon Sanderson", a series of
// Brandon Sanderson's -- the "Author - Title" split read as "Series - Title")
// whose author has books elsewhere is author junk, not series evidence: the
// folder lead is the author.
func TestSnapshot_JunkSeriesNamedForItsOwnAuthorIsNoSeries(t *testing.T) {
	l := newLibrary(t)
	sanderson := l.author("Brandon Sanderson")
	junk := l.series("Brandon Sanderson", sanderson)
	l.book("Elantris", sanderson, junk)
	l.book("The Final Empire", sanderson, l.series("Mistborn", sanderson))
	snap := l.snapshot()
	if snap.IsKnownSeries("Brandon Sanderson") {
		t.Fatal("a series row naming its own author must not count as a series")
	}
	fm, err := metadata.ExtractMetadataFromFolderWith("/srv/library/Brandon Sanderson/Brandon Sanderson - Elantris", snap.Evidence())
	if err != nil {
		t.Fatal(err)
	}
	if len(fm.Authors) != 1 || fm.Authors[0] != "Brandon Sanderson" || fm.SeriesName != "" || fm.Title != "Elantris" {
		t.Fatalf("Sanderson with a junk series row: authors=%v series=%q title=%q, want the author", fm.Authors, fm.SeriesName, fm.Title)
	}
}

// A junk author row of a real series' name ("Star Wars" filed as an author)
// does not turn the series into an author: the series' books are credited to
// other people, so the series row is real evidence.
func TestSnapshot_RealSeriesBesideAJunkAuthorRowStaysASeries(t *testing.T) {
	l := newLibrary(t)
	l.author("Star Wars")
	zahn := l.author("Timothy Zahn")
	luceno := l.author("James Luceno")
	sw := l.series("Star Wars", nil)
	l.book("Thrawn", zahn, sw)
	l.book("Darth Plagueis", luceno, sw)
	snap := l.snapshot()
	if !snap.IsKnownSeries("Star Wars") {
		t.Fatal("a series whose books are by other authors is a real series")
	}
	fm, err := metadata.ExtractMetadataFromFolderWith("/srv/library/Star Wars/Star Wars - Thrawn", snap.Evidence())
	if err != nil {
		t.Fatal(err)
	}
	if fm.SeriesName != "Star Wars" || len(fm.Authors) != 0 || fm.Title != "Thrawn" {
		t.Fatalf("Star Wars: authors=%v series=%q title=%q, want the series", fm.Authors, fm.SeriesName, fm.Title)
	}
	// The point lookup a search uses decides the same way.
	if !IsRealSeries(l.store, "Star Wars") {
		t.Fatal("IsRealSeries(Star Wars) = false")
	}
}

// The rules one by one.
func TestSnapshot_IsKnownSeriesRules(t *testing.T) {
	l := newLibrary(t)
	// No author row of the name: any series row counts.
	l.series("Stormlight Archive", nil)
	// Authorless series holding only the same-named author's books, and
	// that author has books outside it: junk.
	jordan := l.author("Robert Jordan")
	rj := l.series("Robert Jordan", nil)
	l.book("The Eye of the World", jordan, rj)
	l.book("Warrior of the Altaii", jordan, nil)
	// A series filed as its own author, whose author row has no book
	// outside it: the AUTHOR row is the junk side, and the series stands.
	rogue := l.author("Rogue Merchant")
	rm := l.series("Rogue Merchant", rogue)
	l.book("Battle for the North", rogue, rm)
	l.book("The Devil Archetype", rogue, rm)
	// A junk author row of a real character series: the books are by
	// someone else, so the series stands.
	l.author("Honor Harrington")
	weber := l.author("David Weber")
	hh := l.series("Honor Harrington", weber)
	l.book("On Basilisk Station", weber, hh)
	// A same-named series with no books: no evidence.
	l.author("Mara Quill")
	l.series("Mara Quill", nil)

	snap := l.snapshot()
	for name, want := range map[string]bool{
		"Stormlight Archive": true,
		"Robert Jordan":      false,
		"Rogue Merchant":     true,
		"Honor Harrington":   true,
		"Mara Quill":         false,
		"No Such Series":     false,
	} {
		if got := snap.IsKnownSeries(name); got != want {
			t.Errorf("IsKnownSeries(%q) = %v, want %v", name, got, want)
		}
		if got := IsRealSeries(l.store, name); name != "Honor Harrington" && name != "Rogue Merchant" && got != want {
			// These rows carry an author, not authorless, so the point
			// lookup (authorless rows only) does not see them.
			t.Errorf("IsRealSeries(%q) = %v, want %v", name, got, want)
		}
	}
}

// Concurrent callers share one snapshot: the memo is the only mutable state.
func TestSnapshot_ConcurrentCallers(t *testing.T) {
	l := newLibrary(t)
	sanderson := l.author("Brandon Sanderson")
	l.book("Elantris", sanderson, l.series("Brandon Sanderson", sanderson))
	l.book("Warbreaker", sanderson, nil)
	l.author("Star Wars")
	l.book("Thrawn", l.author("Timothy Zahn"), l.series("Star Wars", nil))
	snap := l.snapshot()
	ev := snap.Evidence()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				if ev.IsKnownSeries("Brandon Sanderson") || !ev.IsKnownSeries("Star Wars") {
					t.Error("concurrent verdict changed")
					return
				}
				_ = ev.IsAuthorRow("Star Wars")
				_ = ev.IsKnownAuthor("Brandon Sanderson")
			}
		}()
	}
	wg.Wait()
}
