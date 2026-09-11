// file: internal/database/pebble_store_published_years_test.go
// version: 1.0.0
// guid: 2c50dbcc-0ac1-4258-b076-516bad59c64b
// last-edited: 2026-09-11

package database

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestGetDistinctPublishedYears_MemDBAndPebbleAgree pins the contract on BOTH
// dispatch arms: AudiobookReleaseYear wins over PrintYear, a zero or absent
// year contributes nothing, soft-deleted rows are excluded, and the result is
// sorted and distinct. The two arms are compared to each other as well, so a
// drift in either shows up as a disagreement rather than as two green subtests.
func TestGetDistinctPublishedYears_MemDBAndPebbleAgree(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	intp := func(i int) *int { return &i }
	boolp := func(b bool) *bool { return &b }
	seed := []*Book{
		{Title: "print only", PrintYear: intp(1995)},
		{Title: "audiobook wins", PrintYear: intp(1880), AudiobookReleaseYear: intp(2012)},
		{Title: "duplicate year", PrintYear: intp(1995)},
		{Title: "zero year is unknown", PrintYear: intp(0)},
		{Title: "no year at all"},
		{Title: "soft-deleted decade must not leak", PrintYear: intp(1760), MarkedForDeletion: boolp(true)},
	}
	for _, b := range seed {
		if _, err := store.CreateBook(b); err != nil {
			t.Fatalf("CreateBook(%q): %v", b.Title, err)
		}
	}

	p, ok := store.(*PebbleStore)
	require.True(t, ok, "expected *PebbleStore from setupPebbleTestDB")
	p.WaitForWarmup()
	require.True(t, p.IsMemReady(),
		"memdb must be published or the UseMemDB=true arm silently runs the Pebble path")

	want := []int{1995, 2012}
	got := map[bool][]int{}
	for _, useMemDB := range []bool{true, false} {
		p.UseMemDB = useMemDB
		label := "PebbleScanPath"
		if useMemDB {
			label = "MemDBPath"
		}
		t.Run(label, func(t *testing.T) {
			years, err := store.GetDistinctPublishedYears()
			require.NoError(t, err)
			require.Equal(t, want, years)
			got[useMemDB] = years
		})
	}
	p.UseMemDB = true

	require.Equal(t, got[true], got[false],
		"memdb and Pebble implementations of GetDistinctPublishedYears disagree")
}
