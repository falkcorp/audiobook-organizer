// file: internal/authorcredit/authorcredit_titlecache_test.go
// version: 1.0.1
// guid: c1d7e4a2-5f83-4b19-8a60-3e92b7d0f154
// last-edited: 2026-11-19

package authorcredit

import (
	"testing"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// keyedStore is a decorator over a store that names the identity its title
// listing is cached under, as the gated metafetch wrapper does (round 3, M1):
// a fresh decorator per run must not be a fresh cache entry per run.
type keyedStore struct {
	*database.PebbleStore
	key any
}

func (k keyedStore) TitleCacheKey() any { return k.key }

func titleCacheStore(t *testing.T) *database.PebbleStore {
	t.Helper()
	ResetTitleCache()
	t.Cleanup(ResetTitleCache)
	st := newStore(t)
	_, err := st.CreateBook(&database.Book{Title: "Year One", FilePath: "/l/y1.m4b", Format: "m4b"})
	require.NoError(t, err)
	return st
}

// A store with no key method is cached under its own value, as before.
func TestTitlesOf_CachesUnderTheStoreValueByDefault(t *testing.T) {
	st := titleCacheStore(t)
	a, err := titlesOf(st)
	require.NoError(t, err)
	b, err := titlesOf(st)
	require.NoError(t, err)
	require.Same(t, a, b)
	require.Equal(t, 1, TitleCacheLen())
}

// Two decorator values over one store share ONE entry.
func TestTitlesOf_DecoratorsNamingOneKeyShareOneEntry(t *testing.T) {
	st := titleCacheStore(t)
	a, err := titlesOf(keyedStore{PebbleStore: st, key: st})
	require.NoError(t, err)
	b, err := titlesOf(&keyedStore{PebbleStore: st, key: st})
	require.NoError(t, err)
	require.Same(t, a, b, "the second decorator rebuilt the index")
	require.Equal(t, 1, TitleCacheLen())
	require.True(t, a.keys[LettersKey("Year One")])
}

// The cache does not grow with the number of decorators (one per gated run).
func TestTitlesOf_CacheDoesNotGrowAcrossDecorators(t *testing.T) {
	st := titleCacheStore(t)
	for range 25 {
		_, err := titlesOf(&keyedStore{PebbleStore: st, key: st})
		require.NoError(t, err)
	}
	require.Equal(t, 1, TitleCacheLen())
}

// A key that is not a pointer is not cached, as a non-pointer store never was
// (a value key could be unhashable or never repeat).
func TestTitlesOf_NonPointerKeyIsNotCached(t *testing.T) {
	st := titleCacheStore(t)
	_, err := titlesOf(keyedStore{PebbleStore: st, key: "not-a-pointer"})
	require.NoError(t, err)
	require.Equal(t, 0, TitleCacheLen())
}
