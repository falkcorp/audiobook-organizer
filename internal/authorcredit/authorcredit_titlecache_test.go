// file: internal/authorcredit/authorcredit_titlecache_test.go
// version: 1.1.0
// guid: c1d7e4a2-5f83-4b19-8a60-3e92b7d0f154
// last-edited: 2026-11-19

package authorcredit

import (
	"reflect"
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

func (k keyedStore) TitleCacheKey() TitleKey { return TitleKeyOf(k.key) }

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

// A TitleKey is opaque (round 4, m1): a decorator returns one from TitleCacheKey,
// and a key that exposed the store it names would be an Unwrap under another
// name. It has no methods and no exported field, so it satisfies no store
// interface, and TitleKeyOf(v) values for one pointer are equal.
func TestTitleKey_IsOpaque(t *testing.T) {
	ty := reflect.TypeFor[TitleKey]()
	require.Equal(t, reflect.Struct, ty.Kind())
	require.Zero(t, ty.NumMethod(), "a TitleKey method could hand back the store it names")
	require.Zero(t, reflect.PointerTo(ty).NumMethod())
	for i := range ty.NumField() {
		require.False(t, ty.Field(i).IsExported(), "exported field %s reads the store back", ty.Field(i).Name)
	}
	var k any = TitleKeyOf(&database.PebbleStore{})
	_, isStore := k.(database.Store)
	require.False(t, isStore, "a TitleKey must not be a database.Store")
	_, unwraps := k.(database.StoreUnwrapper)
	require.False(t, unwraps, "a TitleKey must not be a StoreUnwrapper")

	a, b := &struct{ n int }{}, &struct{ n int }{}
	require.True(t, TitleKeyOf(a) == TitleKeyOf(a))
	require.False(t, TitleKeyOf(a) == TitleKeyOf(b), "distinct pointers are distinct keys, whatever they point at")
}
