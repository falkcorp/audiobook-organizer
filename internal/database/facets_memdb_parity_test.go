// file: internal/database/facets_memdb_parity_test.go
// version: 1.0.0
// guid: 6d3f8a1b-2c9e-4b57-a6d0-9e4c1f7b3a82
// last-edited: 2026-10-06

package database

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

// GetGenreCounts / GetDistinctLanguages take the memdb walk when memdb is
// serving. Switching paths must never change an answer, only its cost: the
// Pebble walk was the cold cost of GET /audiobooks/facets. A soft-deleted row
// is included because the Pebble walk counts it, so the memdb walk must too.
func TestGenreLanguageFacets_MemdbMatchesPebble(t *testing.T) {
	p, err := NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })
	p.WaitForWarmup()

	genres := []string{"fantasy", "scifi", "", "fantasy", "history"}
	langs := []string{"en", "de", "en", "", "fr"}
	for i := range genres {
		g, l := genres[i], langs[i]
		b := &Book{Title: "synthetic-" + string(rune('a'+i)), Genre: &g, Language: &l}
		if i == 4 {
			b.MarkedForDeletion = new(true)
		}
		_, err := p.CreateBook(b)
		require.NoError(t, err)
	}
	require.NotNil(t, p.mem(), "memdb must be serving for the fast path")

	memGenres, err := p.GetGenreCounts()
	require.NoError(t, err)
	memLangs, err := p.GetDistinctLanguages()
	require.NoError(t, err)

	p.UseMemDB = false
	pebGenres, err := p.GetGenreCounts()
	require.NoError(t, err)
	pebLangs, err := p.GetDistinctLanguages()
	require.NoError(t, err)

	require.Equal(t, pebGenres, memGenres)
	require.Equal(t, pebLangs, memLangs)
	require.Equal(t, map[string]int{"fantasy": 2, "scifi": 1, "history": 1}, memGenres)
	require.Equal(t, []string{"de", "en", "fr"}, memLangs)
}

// CountTagsForBookIDs (one tag_idx scan) must agree with counting
// GetBookTagsByBookIDs over the same set, including colon-bearing tags and a
// tag that is a prefix of another.
func TestCountTagsForBookIDs_AgreesWithPerBookRead(t *testing.T) {
	p, err := NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })
	p.WaitForWarmup()

	tagSets := [][]string{
		{"metadata", "metadata:language:en", "fantasy"},
		{"metadata:language:en"},
		{"fantasy", "epic"},
		{"epic"},
	}
	var ids []string
	for i, tags := range tagSets {
		b, err := p.CreateBook(&Book{Title: "tagged-" + string(rune('a'+i))})
		require.NoError(t, err)
		ids = append(ids, b.ID)
		for _, tg := range tags {
			require.NoError(t, p.AddBookTag(b.ID, tg))
		}
	}
	set := map[string]struct{}{ids[0]: {}, ids[1]: {}, ids[2]: {}} // not ids[3]

	got, err := p.CountTagsForBookIDs(set)
	require.NoError(t, err)

	keys := make([]string, 0, len(set))
	for id := range set {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	byBook, err := p.GetBookTagsByBookIDs(keys)
	require.NoError(t, err)
	want := map[string]int{}
	for _, tags := range byBook {
		for _, tg := range tags {
			want[tg]++
		}
	}
	require.Equal(t, want, got)
	require.Equal(t, map[string]int{"metadata": 1, "metadata:language:en": 2, "fantasy": 2, "epic": 1}, got)

	empty, err := p.CountTagsForBookIDs(nil)
	require.NoError(t, err)
	require.Empty(t, empty)
}
