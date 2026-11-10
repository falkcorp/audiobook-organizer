// file: internal/books/store/raw_snapshot_test.go
// version: 1.0.2
// guid: 8b3f6d20-7a15-4c9e-b4e8-1d0a52c7f693
// last-edited: 2026-11-09

package store

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A snapshot answers as of the instant it opened: later sets, deletes and new
// keys are invisible to both GetRaw and every page of a scan, while the live
// store sees them at once.
func TestRawSnapshot_IsPointInTime(t *testing.T) {
	p := newOpChangeTestStore(t)
	require.NoError(t, p.ApplyRawBatch([]RawWrite{
		{Key: "quick_query_cache:snap:a", Value: []byte("1")},
		{Key: "quick_query_cache:snap:b", Value: []byte("2")},
		{Key: "quick_query_cache:snap:c", Value: []byte("3")},
	}))

	snap, err := p.NewRawSnapshot()
	require.NoError(t, err)
	defer func() { require.NoError(t, snap.Close()) }()

	require.NoError(t, p.ApplyRawBatch([]RawWrite{
		{Key: "quick_query_cache:snap:a", Value: []byte("changed")},
		{Key: "quick_query_cache:snap:b", Delete: true},
		{Key: "quick_query_cache:snap:d", Value: []byte("4")},
	}))

	got, err := snap.GetRaw("quick_query_cache:snap:a")
	require.NoError(t, err)
	require.Equal(t, []byte("1"), got)
	got, err = snap.GetRaw("quick_query_cache:snap:b")
	require.NoError(t, err)
	require.Equal(t, []byte("2"), got)
	got, err = snap.GetRaw("quick_query_cache:snap:d")
	require.NoError(t, err)
	require.Nil(t, got, "a key written after the snapshot opened must not exist in it")

	// Paged scan: two pages of two, then the rest, all from the snapshot.
	var keys []string
	after := ""
	for {
		pairs, next, err := snap.ScanPrefixPage("quick_query_cache:snap:", after, 2)
		require.NoError(t, err)
		for _, kv := range pairs {
			keys = append(keys, kv.Key)
		}
		if next == "" {
			break
		}
		after = next
	}
	require.Equal(t, []string{"quick_query_cache:snap:a", "quick_query_cache:snap:b", "quick_query_cache:snap:c"}, keys)

	live, err := p.GetRaw("quick_query_cache:snap:a")
	require.NoError(t, err)
	require.Equal(t, []byte("changed"), live)
}

func TestRawSnapshot_RejectsNonPositiveLimit(t *testing.T) {
	p := newOpChangeTestStore(t)
	snap, err := p.NewRawSnapshot()
	require.NoError(t, err)
	defer func() { _ = snap.Close() }()
	_, _, err = snap.ScanPrefixPage("x:", "", 0)
	require.Error(t, err)
}
