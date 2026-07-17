// file: internal/database/pebble_books_index_keys_test.go
// version: 1.0.0
// guid: 3d9c1f47-8a2e-4b06-9d51-7c4e2a8f60b3
// last-edited: 2026-07-17

package database

import (
	"fmt"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/require"
)

// TestGetAllBooksFullFrom_SkipsSecondaryIndexKeys is a regression test for the
// Pebble-path bug where GetAllBooksFullFrom iterated "book:0".."book:~" and
// skipped only ":path:" keys, then json.Unmarshal'd every other key in range as
// if it were a Book.
//
// The "book:" prefix is shared by ten secondary indexes (asin, author, hash,
// isbn13, organizedhash, originalhash, path, series, versiongroup, work).
// Several of them store an EMPTY value because the data lives entirely in the
// key; json.Unmarshal of zero bytes returns "unexpected end of JSON input", so
// the scan died at the first book:asin: key it reached.
//
// It only reproduces with limit=0 (unbounded): any caller passing a limit under
// the book count stops iterating before reaching the index keys. On production
// this made acoustid.backfill fail on every startup with
// "load books: unexpected end of JSON input", which in turn stalled fingerprint
// coverage. Affected unbounded callers: acoustid/backfill.go,
// acoustid/fingerprint_rescan.go, dedup/engine.go, maintenance/jobs/relink_report.go.
func TestGetAllBooksFullFrom_SkipsSecondaryIndexKeys(t *testing.T) {
	store := setupTestPebbleStore(t)
	store.WaitForWarmup()

	const total = 5
	for i := 0; i < total; i++ {
		_, err := store.CreateBook(&Book{
			Title:    fmt.Sprintf("Book %03d", i),
			FilePath: fmt.Sprintf("/tmp/book_%03d.m4b", i),
		})
		require.NoError(t, err)
	}

	// Write the secondary-index keys exactly as production stores them: the data
	// is encoded in the key and the value is empty (asin/isbn13) or a bare book
	// ID that is not valid JSON (author/hash/series).
	emptyValued := []string{
		"book:asin:0062688650:01KNDB93H0EVZXKMTEMS4T017V",
		"book:isbn13:9780002246828:01KNDB9CP8H7M17DBCS83TV6HR",
	}
	for _, k := range emptyValued {
		require.NoError(t, store.db.Set([]byte(k), []byte{}, pebble.Sync))
	}
	nonJSONValued := map[string]string{
		"book:hash:000063743d8365eb2af105df0589d90357fbdb0d5bdbcdafa13f29a8d0bd8f64": "01KNDBK4FYRWSAZW6PBG9947T8",
		"book:author:38541:01KNDBK4FYRWSAZW6PBG9947T8":                               "01KNDBK4FYRWSAZW6PBG9947T8",
		"book:series:144944:01KNDBK4FYRWSAZW6PBG9947T8":                              "01KNDBK4FYRWSAZW6PBG9947T8",
	}
	for k, v := range nonJSONValued {
		require.NoError(t, store.db.Set([]byte(k), []byte(v), pebble.Sync))
	}

	// Force the raw Pebble path. This is what production hits when an unbounded
	// scan runs as a startup task, before memdb warm-up publishes the index --
	// the memdb path swallows per-record errors with `continue` and so hides this.
	store.UseMemDB = false

	// limit=0 => unbounded: iterate past the last record and into the index keys.
	books, err := store.GetAllBooksFullFrom("", 0)
	require.NoError(t, err, "secondary index keys must not abort the scan")
	require.Len(t, books, total, "must return every book and no index keys")
	for _, b := range books {
		require.NotEmpty(t, b.ID, "an index key was decoded as a Book")
	}
}
