// file: internal/database/memdb_summaries_topk_test.go
// version: 1.0.0
// guid: 3e175204-dc65-4425-b1c7-449ca72070af
// last-edited: 2026-10-30

package database

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The unindexed sort path in MemStore.GetBookSummaries used to copy every match
// into a []Book, sort the copies and cut the page. Sort keys that need no
// hydration now rank the memdb's own *Book pointers through a bounded top-k
// heap. These tests pin that the page is the SAME page -- same rows, same
// order, ties included -- for every sort key, both directions, and a spread of
// offset/limit windows.

// Batman characters and storylines only (fixture policy).
var tkTitles = []string{
	"Year One", "The Long Halloween", "Hush", "The Killing Joke", "Knightfall",
	"Dark Victory", "Court of Owls", "Zero Year", "Death of the Family",
	"The Black Mirror", "Batman and Son", "Under the Red Hood",
}

var tkNames = []string{
	"Bruce Wayne", "Selina Kyle", "Harvey Dent", "Barbara Gordon", "Dick Grayson",
	"Alfred Pennyworth", "Jim Gordon", "Lucius Fox", "Oswald Cobblepot",
	"Edward Nygma", "Pamela Isley", "Jason Todd",
}

// tkSeed builds n books whose sortable fields are drawn from deliberately small
// pools, so ties are the rule rather than the exception, with a share of nil
// values so the missing-sorts-last rule is exercised too.
func tkSeed(t testing.TB, n int, seed int64) (*MemStore, int) {
	t.Helper()
	rng := rand.New(rand.NewSource(seed))
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	authors := make([]Author, len(tkNames))
	for i, name := range tkNames {
		authors[i] = Author{ID: i + 1, Name: name}
	}
	series := make([]Series, len(tkTitles))
	for i, name := range tkTitles {
		series[i] = Series{ID: i + 1, Name: name}
	}

	str := func(pool []string) *string {
		if rng.Intn(6) == 0 {
			return nil
		}
		s := pool[rng.Intn(len(pool))]
		return &s
	}
	num := func(max int) *int {
		if rng.Intn(6) == 0 {
			return nil
		}
		v := rng.Intn(max)
		return &v
	}

	books := make([]Book, n)
	for i := range books {
		b := &books[i]
		b.ID = fmt.Sprintf("book-%06d", i)
		b.Title = tkTitles[rng.Intn(len(tkTitles))]
		primary := rng.Intn(4) == 0 // ~1 in 4 rows is a visible primary version
		b.IsPrimaryVersion = &primary
		b.Format = []string{"m4b", "mp3", "flac"}[rng.Intn(3)]
		b.Narrator = str(tkNames)
		b.Genre = str(tkTitles)
		b.Language = str([]string{"en", "fr", "de"})
		b.Publisher = str(tkNames)
		b.Codec = str([]string{"aac", "mp3"})
		b.Quality = str([]string{"low", "high"})
		b.Edition = str(tkTitles)
		b.LibraryState = str([]string{"organized", "imported"})
		b.Duration = num(40)
		b.Bitrate = num(8)
		b.SampleRate = num(3)
		b.AudiobookReleaseYear = num(15)
		b.PrintYear = num(15)
		if rng.Intn(6) != 0 {
			fs := int64(rng.Intn(30))
			b.FileSize = &fs
		}
		if rng.Intn(6) != 0 {
			c := base.Add(time.Duration(rng.Intn(25)) * time.Hour)
			b.CreatedAt = &c
		}
		if rng.Intn(6) != 0 {
			u := base.Add(time.Duration(rng.Intn(25)) * time.Hour)
			b.UpdatedAt = &u
		}
		if rng.Intn(5) != 0 {
			id := rng.Intn(len(authors)) + 1
			b.AuthorID = &id
		}
		if rng.Intn(5) != 0 {
			id := rng.Intn(len(series)) + 1
			b.SeriesID = &id
		}
	}
	// Insert in shuffled order so neither insertion order nor ID order can
	// stand in for the sort.
	rng.Shuffle(len(books), func(i, j int) { books[i], books[j] = books[j], books[i] })

	m, err := NewMemStore()
	require.NoError(t, err)
	txn := m.db.Txn(true)
	for i := range books {
		require.NoError(t, txn.Insert(memTableBooks, &books[i]))
	}
	for i := range authors {
		require.NoError(t, txn.Insert(memTableAuthors, &authors[i]))
	}
	for i := range series {
		require.NoError(t, txn.Insert(memTableSeries, &series[i]))
	}
	txn.Commit()
	return m, n
}

// tkOracle is the page the OLD code produced: copy every match, hydrate, stable
// sort with the ID tiebreak (descending negates the whole comparison), cut.
// It is written out here, not built from the production helpers, so a mistake
// in bookSortLess cannot hide on both sides of the comparison.
func tkOracle(t testing.TB, m *MemStore, sortBy string, asc bool, limit, offset int) []BookSummary {
	t.Helper()
	if limit <= 0 {
		limit = 1_000_000
	}
	txn := m.db.Txn(false)
	defer txn.Abort()
	it, err := txn.Get(memTableBooks, memIdxID)
	require.NoError(t, err)
	var matches []Book
	for obj := it.Next(); obj != nil; obj = it.Next() {
		b := obj.(*Book)
		if !includeByDeletionState(bookIsSoftDeleted(b), nil) {
			continue
		}
		matches = append(matches, *b)
	}
	require.NoError(t, hydrateSortNames(txn, matches, sortBy))
	cmp := bookSortComparators[sortBy]
	sort.SliceStable(matches, func(i, j int) bool {
		r := cmp(&matches[i], &matches[j])
		if r == 0 {
			r = strings.Compare(matches[i].ID, matches[j].ID)
		}
		if !asc {
			return r > 0
		}
		return r < 0
	})
	if offset >= len(matches) {
		return []BookSummary{}
	}
	end := len(matches)
	if offset+limit < end {
		end = offset + limit
	}
	out := make([]BookSummary, 0, end-offset)
	for i := offset; i < end; i++ {
		out = append(out, bookToSummary(&matches[i]))
	}
	return out
}

var tkWindows = []struct{ limit, offset int }{
	{40, 0}, {40, 25}, {1, 0}, {1, 2999}, {7, 2993}, {100, 2950}, {100, 2990},
	{500, 0}, {5000, 0}, {0, 0}, {0, 100}, {10, 5000}, {3, 1},
}

func TestMemdbSummariesTopKMatchesCopyPath(t *testing.T) {
	SetEnabledSortIndexes(nil) // nothing but title streams: every other key is unindexed
	m, n := tkSeed(t, 3000, 20261030)

	for _, key := range SortableBookFields() {
		for _, asc := range []bool{true, false} {
			// One full oracle ordering per (key, direction); each window is a
			// cut of it, which is exactly what the old path's cut was.
			full := tkOracle(t, m, key, asc, 0, 0)
			require.Len(t, full, n)
			for _, w := range tkWindows {
				name := fmt.Sprintf("%s/asc=%v/limit=%d/offset=%d", key, asc, w.limit, w.offset)
				t.Run(name, func(t *testing.T) {
					want := []BookSummary{}
					if w.offset < len(full) {
						end := len(full)
						if w.limit > 0 && w.offset+w.limit < end {
							end = w.offset + w.limit
						}
						want = full[w.offset:end]
					}
					got, err := m.GetBookSummaries(w.limit, w.offset, BookSummaryFilter{SortBy: key, SortAscending: asc})
					require.NoError(t, err)
					require.Equal(t, want, got)
				})
			}
		}
	}

	// Guard the fixture: a property test over a tie-free set proves nothing
	// about tie order, and a window that never reaches the data proves nothing
	// about the cut.
	t.Run("FixtureHasTiesAndWindowsReachData", func(t *testing.T) {
		all, err := m.GetBookSummaries(0, 0, BookSummaryFilter{SortBy: "created_at"})
		require.NoError(t, err)
		require.Len(t, all, n)
		seen := map[int64]int{}
		for _, s := range all {
			if s.CreatedAt != nil {
				seen[s.CreatedAt.UnixNano()]++
			}
		}
		require.Less(t, len(seen), n/20, "created_at must be heavily tied")
		page, err := m.GetBookSummaries(100, 2950, BookSummaryFilter{SortBy: "created_at"})
		require.NoError(t, err)
		require.Len(t, page, 50)
	})
}

// With a filter, the page must come from the ordered FILTERED set.
func TestMemdbSummariesTopKRespectsFilters(t *testing.T) {
	SetEnabledSortIndexes(nil)
	m, _ := tkSeed(t, 1500, 7)
	state := "organized"
	f := BookSummaryFilter{
		SortBy: "created_at", LibraryState: state,
		Predicate: func(b *Book) bool { return b.Duration != nil },
	}
	all, err := m.GetBookSummaries(0, 0, f)
	require.NoError(t, err)
	require.NotEmpty(t, all)
	for _, s := range all {
		require.NotNil(t, s.LibraryState)
		require.Equal(t, state, *s.LibraryState)
		require.NotNil(t, s.Duration)
	}
	f.SortAscending = false
	desc, err := m.GetBookSummaries(0, 0, f)
	require.NoError(t, err)
	page, err := m.GetBookSummaries(10, 5, f)
	require.NoError(t, err)
	require.Equal(t, desc[5:15], page)
}

// The collector on its own, for every key including the ones the end-to-end
// test cannot route to it (title streams from its index; author and series
// need hydrated rows). Rows are hydrated copies here, so the comparator -- not
// the dispatch -- is what is under test.
func TestBookTopKMatchesSortBooks(t *testing.T) {
	SetEnabledSortIndexes(nil)
	m, _ := tkSeed(t, 2000, 99)
	txn := m.db.Txn(false)
	defer txn.Abort()
	it, err := txn.Get(memTableBooks, memIdxID)
	require.NoError(t, err)
	var rows []Book
	for obj := it.Next(); obj != nil; obj = it.Next() {
		rows = append(rows, *obj.(*Book))
	}

	for _, key := range SortableBookFields() {
		hydrated := append([]Book(nil), rows...)
		require.NoError(t, hydrateSortNames(txn, hydrated, key))
		for _, asc := range []bool{true, false} {
			sorted := append([]Book(nil), hydrated...)
			SortBooks(sorted, key, asc)
			for _, w := range []struct{ limit, offset int }{{40, 0}, {40, 31}, {1, 0}, {3000, 0}, {5, 1995}} {
				c, ok := newBookTopK(key, asc, topKWindow(w.offset, w.limit))
				require.True(t, ok)
				for i := range hydrated {
					c.add(&hydrated[i])
				}
				got := c.page(w.offset, w.limit)
				end := min(w.offset+w.limit, len(sorted))
				want := sorted[min(w.offset, len(sorted)):end]
				require.Lenf(t, got, len(want), "%s asc=%v %+v", key, asc, w)
				for i := range got {
					require.Equalf(t, want[i].ID, got[i].ID, "%s asc=%v %+v index %d", key, asc, w, i)
				}
			}
		}
	}
}

// The dispatch is only safe if sortHydratesRows names exactly the keys
// hydrateSortNames writes for: a key it misses would be ranked in place on
// shared memdb rows and written through.
func TestSortHydratesRowsMatchesHydrateSortNames(t *testing.T) {
	id := 1
	stub := stubLookup{}
	for _, key := range SortableBookFields() {
		rows := []Book{{ID: "b", AuthorID: &id, SeriesID: &id}}
		require.NoError(t, hydrateSortNames(stub, rows, key))
		mutated := rows[0].Author != nil || rows[0].Series != nil
		require.Equalf(t, mutated, sortHydratesRows(key),
			"sort %q: hydrateSortNames mutated=%v but sortHydratesRows says %v", key, mutated, sortHydratesRows(key))
	}
}

type stubLookup struct{}

func (stubLookup) First(table, _ string, _ ...any) (any, error) {
	switch table {
	case memTableAuthors:
		return &Author{ID: 1, Name: "Bruce Wayne"}, nil
	case memTableSeries:
		return &Series{ID: 1, Name: "Year One"}, nil
	}
	return nil, nil
}

// The pointer path must not write to the memdb rows it ranks.
func TestMemdbSummariesTopKLeavesRowsUntouched(t *testing.T) {
	SetEnabledSortIndexes(nil)
	m, _ := tkSeed(t, 500, 3)
	snapshot := func() map[string]Book {
		txn := m.db.Txn(false)
		defer txn.Abort()
		it, err := txn.Get(memTableBooks, memIdxID)
		require.NoError(t, err)
		out := map[string]Book{}
		for obj := it.Next(); obj != nil; obj = it.Next() {
			b := obj.(*Book)
			out[b.ID] = *b
		}
		return out
	}
	before := snapshot()
	for _, key := range SortableBookFields() {
		_, err := m.GetBookSummaries(40, 0, BookSummaryFilter{SortBy: key})
		require.NoError(t, err)
	}
	require.Equal(t, before, snapshot())
}

func tkBenchStore(b *testing.B, n int) *MemStore {
	b.Helper()
	SetEnabledSortIndexes(nil)
	m, _ := tkSeed(b, n, 42)
	return m
}

// BenchmarkSortRecentlyAddedDesc is the ABS Recently Added shelf / Library
// sort=addedAt&desc=1 shape on a library the size of production's: the 40
// newest of ~15k visible rows out of 60k, with no created_at index.
func BenchmarkSortRecentlyAddedDesc(b *testing.B) {
	m := tkBenchStore(b, 60_000)
	primary := true
	f := BookSummaryFilter{SortBy: "created_at", IsPrimaryVersion: &primary}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out, err := m.GetBookSummaries(40, 0, f)
		if err != nil || len(out) != 40 {
			b.Fatalf("got %d rows, err %v", len(out), err)
		}
	}
}

// BenchmarkSortAuthorAscCopyPath is the control: author mutates rows, stays on
// the copying path, and should be unchanged by this work.
func BenchmarkSortAuthorAscCopyPath(b *testing.B) {
	m := tkBenchStore(b, 60_000)
	primary := true
	f := BookSummaryFilter{SortBy: "author", SortAscending: true, IsPrimaryVersion: &primary}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out, err := m.GetBookSummaries(40, 0, f)
		if err != nil || len(out) != 40 {
			b.Fatalf("got %d rows, err %v", len(out), err)
		}
	}
}
