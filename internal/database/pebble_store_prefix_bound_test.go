// file: internal/database/pebble_store_prefix_bound_test.go
// version: 1.0.0
// guid: 2d236c92-42b8-40fc-b9f1-fb12e92c5b9c
// last-edited: 2026-10-04

package database

import (
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/require"
)

func putPrefixTestKeys(t *testing.T, p *PebbleStore, keys ...string) {
	t.Helper()
	for _, k := range keys {
		require.NoError(t, p.db.Set([]byte(k), []byte("v"), pebble.Sync))
	}
}

func scanKeys(t *testing.T, p *PebbleStore, prefix string) []string {
	t.Helper()
	pairs, err := p.ScanPrefix(prefix)
	require.NoError(t, err)
	var out []string
	for _, kv := range pairs {
		out = append(out, kv.Key)
	}
	return out
}

// A prefix ending in 0xff used to wrap its upper bound to 0x00 (a range that
// excludes every key it should include).
func TestScanPrefix_TrailingFFPrefix(t *testing.T) {
	p := newCensusRawStore(t)
	putPrefixTestKeys(t, p, "a\xff1", "a\xff2", "b")

	require.Equal(t, []string{"a\xff1", "a\xff2"}, scanKeys(t, p, "a\xff"))
	n, err := p.CountPrefix("a\xff")
	require.NoError(t, err)
	require.EqualValues(t, 2, n)
}

// A prefix made only of 0xff bytes has no finite upper bound.
func TestScanPrefix_AllFFPrefix(t *testing.T) {
	p := newCensusRawStore(t)
	putPrefixTestKeys(t, p, "a", "\xff\xff1", "\xff\xff\xff")

	require.Equal(t, []string{"\xff\xff1", "\xff\xff\xff"}, scanKeys(t, p, "\xff\xff"))
	n, err := p.CountPrefix("\xff\xff")
	require.NoError(t, err)
	require.EqualValues(t, 2, n)
}

func TestCountPrefix_EmptyPrefixCountsAll(t *testing.T) {
	p := newCensusRawStore(t)
	putPrefixTestKeys(t, p, "a", "b\xff", "\xffz")

	n, err := p.CountPrefix("")
	require.NoError(t, err)
	require.EqualValues(t, 3, n)
	require.Len(t, scanKeys(t, p, ""), 3)
}

// Keys with no 0xff byte must still match an ordinary prefix through the new
// bound: the fix must not have narrowed the common case.
func TestScanPrefix_NormalPrefixStillMatches(t *testing.T) {
	p := newCensusRawStore(t)
	putPrefixTestKeys(t, p, "ab1", "ab2", "ac1")

	require.Equal(t, []string{"ab1", "ab2"}, scanKeys(t, p, "ab"))
	n, err := p.CountPrefix("ab")
	require.NoError(t, err)
	require.EqualValues(t, 2, n)
}
