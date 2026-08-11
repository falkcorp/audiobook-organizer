// file: internal/database/memdb_warmup_counts_test.go
// version: 1.0.0
// guid: 7b1e4c92-5d3a-4f18-9e26-1a0c8b7d4f35
// last-edited: 2026-08-11

package database

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestWarmupCounts_CountRowsNotPebbleKeys is a regression test for the
// mislabeled warmup metric that triggered a false P0.
//
// warmIter walks a Pebble key PREFIX and returns the number of keys it
// visited. WarmFromPebble then published that number under the label
// "books" (and "authors", "book_files", ...). But the "book:" prefix is
// shared by ~7 secondary index families — book:path:, book:hash:,
// book:originalhash:, book:organizedhash:, book:versiongroup:, book:work:,
// book:asin:/book:isbn13: — all of which the row callback deliberately
// SKIPS via `strings.Count(key, ":") != 1`. Skipped keys were still counted.
//
// On production this reported `books=366922` while the library actually held
// ~49,000 books (~7.5 keys per book). Every whole-library iterator was then
// measured against that inflated denominator and looked like it was
// returning 13.3% of the library, which is what this bug was first reported
// as. The iterators were correct; the denominator was not.
//
// The count must be rows INSERTED INTO MEMDB, not keys scanned.
func TestWarmupCounts_CountRowsNotPebbleKeys(t *testing.T) {
	store := setupTestPebbleStore(t)
	store.WaitForWarmup()

	const totalBooks = 4
	for i := 0; i < totalBooks; i++ {
		hash := fmt.Sprintf("hash%03d", i)
		orig := fmt.Sprintf("orighash%03d", i)
		organized := fmt.Sprintf("orghash%03d", i)
		_, err := store.CreateBook(&Book{
			Title:             fmt.Sprintf("Book %03d", i),
			FilePath:          fmt.Sprintf("/tmp/warmcount_%03d.m4b", i),
			FileHash:          &hash,
			OriginalFileHash:  &orig,
			OrganizedFileHash: &organized,
		})
		require.NoError(t, err)
	}

	// Each CreateBook above writes 1 record key plus 4 secondary-index keys
	// (path, hash, originalhash, organizedhash), so the "book:" prefix holds
	// 5*totalBooks keys but only totalBooks book rows.
	mem, err := NewMemStore()
	require.NoError(t, err)
	require.NoError(t, mem.WarmFromPebble(context.Background(), store))

	rows, scanned := mem.LastWarmupCounts()

	require.Equal(t, totalBooks, rows[memTableBooks],
		"warmup must report the number of BOOK ROWS inserted into memdb, "+
			"not the number of Pebble keys under the shared \"book:\" prefix")

	// The scanned-keys number is still useful telemetry, but it must be
	// reported under its own name so it can never be mistaken for a row count.
	require.Greater(t, scanned[memTableBooks], rows[memTableBooks],
		"secondary-index keys should make scanned strictly exceed rows")
}
