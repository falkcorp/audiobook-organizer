// file: internal/database/title_dir_index_test.go
// version: 1.0.0
// guid: 4b7abbe2-8ac2-4468-bd1e-9147e50d5280
// last-edited: 2026-10-30

package database

import (
	"fmt"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

// These tests pin GetBooksByTitleInDir's two backends to one answer and pin the
// reason the memdb index exists: the scanner calls it once per newly imported
// book, and the Pebble walk behind it decodes every book row.

type titleDirBook struct {
	title string
	path  string
}

// titleDirFixture is a small Gotham library. It covers the shapes the predicate
// distinguishes: same title in two dirs, same dir with two titles, case and
// padding differences, a dir that is a string-prefix of another, an empty
// title, and a book with no path at all.
var titleDirFixture = []titleDirBook{
	{"The Long Halloween", "/lib/Batman/Long Halloween/part1.m4b"},
	{"The Long Halloween", "/lib/Batman/Long Halloween/part1 (1).mp3"},
	{"  the long HALLOWEEN ", "/lib/Batman/Long Halloween/part1 (2).flac"},
	{"The Long Halloween", "/lib/Batman/Dark Victory/part1.m4b"},
	{"Dark Victory", "/lib/Batman/Dark Victory/dv.m4b"},
	{"Year One", "/lib/Robin/Batman"},
	{"Year One", "/lib/Robin/Batman Beyond/yo.m4b"},
	{"Year One", "/lib/Robin/Batman/yo.m4b"},
	{"", "/lib/Unsorted/untitled.m4b"},
	{"Court of Owls", ""},
}

func createTitleDirFixture(t *testing.T, p *PebbleStore) map[string]string {
	t.Helper()
	idByPath := map[string]string{}
	for _, f := range titleDirFixture {
		created, err := p.CreateBook(&Book{Title: f.title, FilePath: f.path})
		require.NoError(t, err)
		idByPath[f.path] = created.ID
	}
	return idByPath
}

func titleDirIDs(books []Book) []string {
	ids := make([]string, len(books))
	for i := range books {
		ids[i] = books[i].ID
	}
	sort.Strings(ids)
	return ids
}

type titleDirQuery struct{ title, dir string }

// titleDirQueries is every (title, dir) pair the fixture can answer, plus
// pairs that must come back empty and the raw-vs-normalized spellings.
func titleDirQueries() []titleDirQuery {
	seen := map[titleDirQuery]bool{}
	var qs []titleDirQuery
	add := func(q titleDirQuery) {
		if !seen[q] {
			seen[q] = true
			qs = append(qs, q)
		}
	}
	for _, f := range titleDirFixture {
		add(titleDirQuery{f.title, filepath.Dir(f.path)})
		add(titleDirQuery{"the long halloween", filepath.Dir(f.path)})
		add(titleDirQuery{"  THE LONG HALLOWEEN", filepath.Dir(f.path)})
	}
	add(titleDirQuery{"year one", "/lib/Robin"})
	add(titleDirQuery{"year one", "/lib/Robin/Batman/"})
	add(titleDirQuery{"year one", "/lib/robin/batman"})
	add(titleDirQuery{"no such book", "/lib/Batman/Long Halloween"})
	add(titleDirQuery{"", "."})
	add(titleDirQuery{"", "/lib/Unsorted"})
	return qs
}

// TestGetBooksByTitleInDir_MemDBMatchesPebbleScan runs every query through the
// memdb index, the public method with memdb off, and the scan itself. They must
// return the same book IDs, in the same order.
func TestGetBooksByTitleInDir_MemDBMatchesPebbleScan(t *testing.T) {
	p := setupTestPebbleStore(t)
	p.WaitForWarmup()
	createTitleDirFixture(t, p)

	nonEmpty := 0
	for _, q := range titleDirQueries() {
		p.UseMemDB = true
		viaMem, err := p.GetBooksByTitleInDir(q.title, q.dir)
		require.NoError(t, err)

		p.UseMemDB = false
		viaFallback, err := p.GetBooksByTitleInDir(q.title, q.dir)
		require.NoError(t, err)

		scan, err := p.getBooksByTitleInDirPebbleScan(q.title, q.dir)
		require.NoError(t, err)

		require.Equal(t, titleDirIDs(scan), titleDirIDs(viaMem), "memdb vs scan for %+v", q)
		require.Equal(t, titleDirIDs(scan), titleDirIDs(viaFallback), "fallback vs scan for %+v", q)
		// Order, not just membership: the scan is ID order and so is memdb.
		memOrder := make([]string, len(viaMem))
		for i := range viaMem {
			memOrder[i] = viaMem[i].ID
		}
		require.True(t, sort.StringsAreSorted(memOrder), "memdb result must be ID ascending for %+v", q)
		if len(scan) > 0 {
			nonEmpty++
		}
	}
	require.Greater(t, nonEmpty, 5, "the fixture must exercise real matches, not only empty answers")
}

// TestGetBooksByTitleInDir_ExactPredicate pins the cases a careless index key
// would get wrong, against fixed expectations rather than only the other
// backend.
func TestGetBooksByTitleInDir_ExactPredicate(t *testing.T) {
	p := setupTestPebbleStore(t)
	p.WaitForWarmup()
	ids := createTitleDirFixture(t, p)

	get := func(title, dir string) []string {
		books, err := p.GetBooksByTitleInDir(title, dir)
		require.NoError(t, err)
		return titleDirIDs(books)
	}
	sorted := func(paths ...string) []string {
		out := make([]string, len(paths))
		for i, path := range paths {
			out[i] = ids[path]
		}
		sort.Strings(out)
		return out
	}

	// Case and padding differences on either side are one title.
	require.Equal(t, sorted(
		"/lib/Batman/Long Halloween/part1.m4b",
		"/lib/Batman/Long Halloween/part1 (1).mp3",
		"/lib/Batman/Long Halloween/part1 (2).flac",
	), get("THE long halloween  ", "/lib/Batman/Long Halloween"))

	// A dir that is a string prefix of another does not bleed into it.
	require.Equal(t, sorted("/lib/Robin/Batman/yo.m4b"), get("year one", "/lib/Robin/Batman"))
	require.Equal(t, sorted("/lib/Robin/Batman Beyond/yo.m4b"), get("year one", "/lib/Robin/Batman Beyond"))
	// "/lib/Robin/Batman" is itself a file path whose parent is /lib/Robin.
	require.Equal(t, sorted("/lib/Robin/Batman"), get("year one", "/lib/Robin"))

	// The dir is compared byte for byte: no cleaning, no case folding.
	require.Empty(t, get("year one", "/lib/Robin/Batman/"))
	require.Empty(t, get("year one", "/lib/robin/batman"))

	// An empty title and an empty path are ordinary rows, exactly as in the scan.
	require.Equal(t, sorted("/lib/Unsorted/untitled.m4b"), get("", "/lib/Unsorted"))
	require.Equal(t, sorted(""), get("court of owls", "."))
}

// TestGetBooksByTitleInDir_IndexFollowsWrites proves the index is maintained on
// every write that changes the key: a retitle, a move to another folder, a
// soft delete (which must NOT drop the row -- the scanner filters liveness), and
// a hard delete.
func TestGetBooksByTitleInDir_IndexFollowsWrites(t *testing.T) {
	p := setupTestPebbleStore(t)
	p.WaitForWarmup()

	const dirA = "/lib/Alfred/Hush"
	const dirB = "/lib/Alfred/Knightfall"
	mk := func(title, path string) *Book {
		b, err := p.CreateBook(&Book{Title: title, FilePath: path})
		require.NoError(t, err)
		return b
	}
	idsIn := func(title, dir string) []string {
		// Both backends, every time: a write that only one of them sees is the bug.
		p.UseMemDB = true
		viaMem, err := p.GetBooksByTitleInDir(title, dir)
		require.NoError(t, err)
		p.UseMemDB = false
		viaScan, err := p.GetBooksByTitleInDir(title, dir)
		require.NoError(t, err)
		p.UseMemDB = true
		require.Equal(t, titleDirIDs(viaScan), titleDirIDs(viaMem), "backends disagree for (%q, %q)", title, dir)
		return titleDirIDs(viaMem)
	}

	a := mk("Hush", dirA+"/a.m4b")
	b := mk("Hush", dirA+"/b.mp3")
	require.ElementsMatch(t, []string{a.ID, b.ID}, idsIn("hush", dirA))

	// Retitle: leaves the old key, joins the new one.
	b.Title = "Knightfall"
	_, err := p.UpdateBook(b.ID, b)
	require.NoError(t, err)
	require.Equal(t, []string{a.ID}, idsIn("hush", dirA))
	require.Equal(t, []string{b.ID}, idsIn("knightfall", dirA))

	// Move: same title, new folder.
	b.FilePath = dirB + "/b.mp3"
	_, err = p.UpdateBook(b.ID, b)
	require.NoError(t, err)
	require.Empty(t, idsIn("knightfall", dirA))
	require.Equal(t, []string{b.ID}, idsIn("knightfall", dirB))

	// Retitle back into the first folder in the same call as a move.
	b.Title = "Hush"
	b.FilePath = dirA + "/b2.mp3"
	_, err = p.UpdateBook(b.ID, b)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{a.ID, b.ID}, idsIn("hush", dirA))
	require.Empty(t, idsIn("knightfall", dirB))

	// Soft delete keeps the row visible to this getter, on both backends.
	trashed := true
	a.MarkedForDeletion = &trashed
	_, err = p.UpdateBook(a.ID, a)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{a.ID, b.ID}, idsIn("hush", dirA),
		"a soft-deleted row is still a same-title sibling; the scanner decides liveness")

	// Hard delete removes it.
	require.NoError(t, p.DeleteBook(a.ID))
	require.Equal(t, []string{b.ID}, idsIn("hush", dirA))
	require.NoError(t, p.DeleteBook(b.ID))
	require.Empty(t, idsIn("hush", dirA))
}

// TestGetBooksByTitleInDir_MemDBPathDoesNotScanPebble is the count test: with a
// read fault armed on the first book row, the Pebble walk MUST fail and the
// memdb path MUST NOT notice, because it never opens the keyspace. Visiting
// zero rows is the whole point -- the scan visited every row.
func TestGetBooksByTitleInDir_MemDBPathDoesNotScanPebble(t *testing.T) {
	p := setupTestPebbleStore(t)
	p.WaitForWarmup()
	createTitleDirFixture(t, p)
	const dir = "/lib/Batman/Long Halloween"

	clear := setRowScanFault(p.db, "", 1, errInjectedScanFault)
	defer clear()

	books, err := p.GetBooksByTitleInDir("the long halloween", dir)
	require.NoError(t, err, "memdb path must answer without touching the book keyspace")
	require.Len(t, books, 3)

	_, err = p.getBooksByTitleInDirPebbleScan("the long halloween", dir)
	require.ErrorIs(t, err, errInjectedScanFault, "the fallback does walk the keyspace; the fault proves the probe works")

	p.UseMemDB = false
	_, err = p.GetBooksByTitleInDir("the long halloween", dir)
	require.ErrorIs(t, err, errInjectedScanFault, "pre-warmup / memdb-off still takes the scan")
}

// TestGetBooksByTitleInDir_IncompleteMemDBFallsBackToPebble: a memdb known to be
// missing book rows must not answer, because a short answer is a missed version
// link. The fallback is observable because memdb rows are stripped and Pebble's
// are not.
func TestGetBooksByTitleInDir_IncompleteMemDBFallsBackToPebble(t *testing.T) {
	p := setupTestPebbleStore(t)
	p.WaitForWarmup()

	desc := "Bruce Wayne returns to Gotham."
	created, err := p.CreateBook(&Book{Title: "Batman Begins", FilePath: "/lib/Catwoman/Begins/a.m4b", Description: &desc})
	require.NoError(t, err)

	books, err := p.GetBooksByTitleInDir("batman begins", "/lib/Catwoman/Begins")
	require.NoError(t, err)
	require.Len(t, books, 1)
	require.Nil(t, books[0].Description, "memdb rows are the stripped projection")

	_, err = p.mem().GetBooksByTitleInDir("batman begins", "/lib/Catwoman/Begins")
	require.NoError(t, err)
	p.mem().recordLostRows(memTableBooks, 1)
	_, err = p.mem().GetBooksByTitleInDir("batman begins", "/lib/Catwoman/Begins")
	require.ErrorIs(t, err, ErrMemdbIncomplete)

	books, err = p.GetBooksByTitleInDir("batman begins", "/lib/Catwoman/Begins")
	require.NoError(t, err)
	require.Len(t, books, 1)
	require.Equal(t, created.ID, books[0].ID)
	require.NotNil(t, books[0].Description, "an incomplete memdb must fall through to the full Pebble row")
	require.Equal(t, desc, *books[0].Description)
}

func TestTitleDirIndex_FromArgsRejectsBadArity(t *testing.T) {
	_, err := titleDirIndex{}.FromArgs("only one")
	require.Error(t, err)
	_, err = titleDirIndex{}.FromArgs(1, "dir")
	require.Error(t, err)
	_, err = titleDirIndex{}.FromArgs("title", 2)
	require.Error(t, err)
	_, _, err = titleDirIndex{}.FromObject("not a book")
	require.Error(t, err)
}

// benchTitleDirLibrary builds n books in n/4 folders, four same-title copies
// per folder, so every lookup has a few real siblings.
func benchTitleDirLibrary(b *testing.B, n int) *PebbleStore {
	b.Helper()
	p, err := NewPebbleStoreInMemory("bench_title_dir")
	require.NoError(b, err)
	b.Cleanup(func() { p.Close() })
	p.WaitForWarmup()
	for i := 0; i < n; i++ {
		_, err := p.CreateBook(&Book{
			Title:    fmt.Sprintf("Gotham Chronicle %d", i/4),
			FilePath: fmt.Sprintf("/lib/Gotham/%d/copy%d.m4b", i/4, i%4),
		})
		require.NoError(b, err)
	}
	return p
}

// BenchmarkGetBooksByTitleInDir shows the cost curve. Compare ns/op across
// sizes: the memdb lookup stays flat as the library grows and the scan grows
// linearly with it.
func BenchmarkGetBooksByTitleInDir(b *testing.B) {
	for _, n := range []int{500, 2000} {
		p := benchTitleDirLibrary(b, n)
		dir := fmt.Sprintf("/lib/Gotham/%d", n/8)
		title := fmt.Sprintf("gotham chronicle %d", n/8)
		b.Run(fmt.Sprintf("memdb/books=%d", n), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				got, err := p.GetBooksByTitleInDir(title, dir)
				if err != nil || len(got) != 4 {
					b.Fatalf("got %d, err %v", len(got), err)
				}
			}
		})
		b.Run(fmt.Sprintf("pebble-scan/books=%d", n), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				got, err := p.getBooksByTitleInDirPebbleScan(title, dir)
				if err != nil || len(got) != 4 {
					b.Fatalf("got %d, err %v", len(got), err)
				}
			}
		})
	}
}
