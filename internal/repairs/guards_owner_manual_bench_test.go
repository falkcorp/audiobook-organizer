// file: internal/repairs/guards_owner_manual_bench_test.go
// version: 1.0.0
// guid: 6a2f9d83-1c75-4e08-b4a6-3d91e7f05c28
// last-edited: 2026-10-22

package repairs

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// benchGuardLibrary is a library-sized fake for the owner-manual guard: books
// with ordinary (non-franchise) titles, one or two authors, a narrator, a few
// tags, one or two files in a few hundred real folders. Nothing matches, so
// the guard runs every check on every book (no early return): the worst case.
// Titles and names are synthetic.
type benchGuardLibrary struct {
	books    map[string]*database.Book
	files    map[string][]database.BookFile
	authors  map[string][]database.BookAuthor
	narrs    map[string][]database.BookNarrator
	tags     map[string][]database.BookTag
	authorBy map[int]*database.Author
	narrBy   map[int]*database.Narrator
}

func (l *benchGuardLibrary) GetBookByID(id string) (*database.Book, error) { return l.books[id], nil }
func (l *benchGuardLibrary) GetBookFiles(id string) ([]database.BookFile, error) {
	return l.files[id], nil
}
func (l *benchGuardLibrary) GetBookAuthors(id string) ([]database.BookAuthor, error) {
	return l.authors[id], nil
}
func (l *benchGuardLibrary) GetAuthorByID(id int) (*database.Author, error) {
	return l.authorBy[id], nil
}
func (l *benchGuardLibrary) GetBookNarrators(id string) ([]database.BookNarrator, error) {
	return l.narrs[id], nil
}
func (l *benchGuardLibrary) GetNarratorByID(id int) (*database.Narrator, error) {
	return l.narrBy[id], nil
}
func (l *benchGuardLibrary) GetBookTagsDetailed(id string) ([]database.BookTag, error) {
	return l.tags[id], nil
}

func newBenchGuardLibrary(tb testing.TB, root string, n int) (*benchGuardLibrary, []string) {
	tb.Helper()
	words := []string{"Silent", "River", "Garden", "Iron", "Harbor", "Winter", "Lantern", "Orchard", "Compass", "Tide", "Ember", "Meridian"}
	l := &benchGuardLibrary{
		books: map[string]*database.Book{}, files: map[string][]database.BookFile{},
		authors: map[string][]database.BookAuthor{}, narrs: map[string][]database.BookNarrator{},
		tags: map[string][]database.BookTag{}, authorBy: map[int]*database.Author{}, narrBy: map[int]*database.Narrator{},
	}
	for i := 0; i < 400; i++ {
		l.authorBy[i] = &database.Author{ID: i, Name: fmt.Sprintf("Author%03d Sample", i)}
		l.narrBy[i] = &database.Narrator{ID: i, Name: fmt.Sprintf("Narrator%03d Sample", i)}
	}
	const folders = 600
	for f := 0; f < folders; f++ {
		if err := os.MkdirAll(filepath.Join(root, fmt.Sprintf("a%03d", f%400), fmt.Sprintf("book-%03d", f)), 0o755); err != nil {
			tb.Fatal(err)
		}
	}
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("bench-%06d", i)
		ids = append(ids, id)
		dir := filepath.Join(root, fmt.Sprintf("a%03d", (i%folders)%400), fmt.Sprintf("book-%03d", i%folders))
		title := fmt.Sprintf("The %s %s, Book %d", words[i%len(words)], words[(i/len(words))%len(words)], i%9+1)
		aid, pub, nar := i%400, "Sample Audio", fmt.Sprintf("Narrator%03d Sample", i%400)
		l.books[id] = &database.Book{ID: id, Title: title, FilePath: filepath.Join(dir, "part1.m4b"), AuthorID: &aid, Publisher: &pub, Narrator: &nar}
		l.files[id] = []database.BookFile{{BookID: id, FilePath: filepath.Join(dir, "part1.m4b")}, {BookID: id, FilePath: filepath.Join(dir, "part2.m4b")}}
		l.authors[id] = []database.BookAuthor{{BookID: id, AuthorID: aid}, {BookID: id, AuthorID: (aid + 7) % 400, Position: 1}}
		l.narrs[id] = []database.BookNarrator{{BookID: id, NarratorID: i % 400}}
		l.tags[id] = []database.BookTag{{BookID: id, Tag: "genre:" + words[i%len(words)]}, {BookID: id, Tag: "language:english"}, {BookID: id, Tag: "year:2019"}}
	}
	return l, ids
}

// BenchmarkGuardOwnerManualBooks51k times the retired-versions owner-manual
// check (GuardOwnerManualBooks, one book id per call as the fixer makes it)
// over 51,000 books: first on one goroutine, then fanned over NumCPU workers
// as the fixer's worker pool does. The reads are in-memory, so this is the
// guard's own CPU (franchise regexes over paths, titles, credits, transcribed
// fields and tags, plus path resolution against a few hundred real folders);
// a production run adds the store reads on top. Run it with
//
//	go test ./internal/repairs -run '^$' -bench GuardOwnerManualBooks51k -benchtime 1x
func BenchmarkGuardOwnerManualBooks51k(b *testing.B) {
	const n = 51000
	lib, ids := newBenchGuardLibrary(b, b.TempDir(), n)
	run := func(workers int) {
		res := NewPathResolver()
		var wg sync.WaitGroup
		var mu sync.Mutex
		var flagged int
		next := make(chan string)
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for id := range next {
					kind, _, err := GuardOwnerManualBooks(lib, lib, func(int) string { return "" }, res, []string{id})
					if err != nil {
						b.Error(err)
						return
					}
					if kind != "" {
						mu.Lock()
						flagged++
						mu.Unlock()
					}
				}
			}()
		}
		for _, id := range ids {
			next <- id
		}
		close(next)
		wg.Wait()
		if flagged != 0 {
			b.Fatalf("%d synthetic books were flagged; the fixture must not match any franchise", flagged)
		}
	}
	b.Run("serial", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			run(1)
		}
	})
	b.Run(fmt.Sprintf("workers=%d", runtime.NumCPU()), func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			run(runtime.NumCPU())
		}
	})
}

// TestWithResolved_ReturnsEachSpellingOnce: a path repeated across a book's
// file_path and its book_file rows, or equal to another path's resolved
// spelling, is returned once and in first-occurrence order, so the guard
// matches it once; a symlink's target is still returned after its link.
func TestWithResolved_ReturnsEachSpellingOnce(t *testing.T) {
	root := t.TempDir()
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(real, "shows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(real, "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	a := filepath.Join(dir, "a.m4b")
	b := filepath.Join(link, "b.m4b") // resolves to dir/b.m4b

	got, doubt := NewPathResolver().withResolved([]string{a, "", a, b, "", filepath.Join(dir, "b.m4b"), a})
	if doubt != nil {
		t.Fatalf("unexpected doubt: %v", doubt)
	}
	want := []string{a, "", b, filepath.Join(dir, "b.m4b")}
	if len(got) != len(want) {
		t.Fatalf("withResolved = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("withResolved = %q, want %q", got, want)
		}
	}
}
