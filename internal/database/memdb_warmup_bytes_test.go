// file: internal/database/memdb_warmup_bytes_test.go
// version: 1.0.0
// guid: 3b91d70e-52c4-4f18-a6d3-9e7f04c1b8a5
// last-edited: 2026-08-13

package database

import (
	"context"
	"encoding/base64"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// Per-phase TIMING (shipped earlier) says book_files is 82% of a ~109 s
// production warmup. It does not say why, and the two candidate explanations
// take opposite fixes:
//
//	"the scan is serial"  → parallelize it (divides the work)
//	"the rows are huge"   → stop storing the blob in the row (shrinks the work)
//
// Byte accounting separates them. BookFile.AcoustIDFingerprint is a []byte held
// inline in the Pebble row — ~230 KB raw, ~307 KB once encoding/json
// base64-encodes it — and stripBookFileForMemdb nils it the instant it has been
// decoded. These tests pin that the warmup reports how many bytes it read and
// how many of those it threw away, so the choice between the two fixes rests on
// a measurement rather than on reading a call graph.

// warmBytesFingerprint returns a deterministic blob standing in for a real
// chromaprint stream. Size matters: the point of the fixture is a row whose
// bytes are almost entirely the discarded field, which is the production shape.
func warmBytesFingerprint(n int) []byte {
	fp := make([]byte, n)
	for i := range fp {
		fp[i] = byte(i % 251)
	}
	return fp
}

// seedWarmBytesBook creates one book and n book_files under it, optionally
// giving each file a fingerprint blob.
func seedWarmBytesBook(t *testing.T, store *PebbleStore, tag string, n int, fp []byte) {
	t.Helper()
	hash := "warmbytes-" + tag
	book, err := store.CreateBook(&Book{
		Title:    "Warm Bytes " + tag,
		FilePath: "/tmp/warmbytes_" + tag + ".m4b",
		FileHash: &hash,
	})
	require.NoError(t, err)

	for i := 0; i < n; i++ {
		fh := fmt.Sprintf("warmbytes-%s-%03d", tag, i)
		bf := &BookFile{
			BookID:   book.ID,
			FilePath: fmt.Sprintf("/tmp/warmbytes_%s_%03d.mp3", tag, i),
			FileHash: fh,
			Format:   "mp3",
		}
		if len(fp) > 0 {
			bf.AcoustIDFingerprint = fp
		}
		require.NoError(t, store.CreateBookFile(bf))
	}
}

func warmBytesFresh(t *testing.T, store *PebbleStore) *MemStore {
	t.Helper()
	mem, err := NewMemStore()
	require.NoError(t, err)
	require.NoError(t, mem.WarmFromPebble(context.Background(), store))
	return mem
}

// TestWarmupBytes_AttributesTheDiscardedBlob is the load-bearing case: a
// fixture whose book_file rows are almost entirely fingerprint must report a
// discarded share that reflects that.
//
// "Nonzero" would not be enough. An implementation that counted only the small
// scalar fields, or that charged the DECODED fingerprint length against an
// ENCODED byte total, would report a nonzero number that is wrong by 4/3 or
// worse — and a wrong ratio here would send the fix at the wrong target.
func TestWarmupBytes_AttributesTheDiscardedBlob(t *testing.T) {
	const (
		files  = 12
		fpSize = 64 * 1024
	)
	store := setupTestPebbleStore(t)
	store.WaitForWarmup()

	fp := warmBytesFingerprint(fpSize)
	seedWarmBytesBook(t, store, "fat", files, fp)

	mem := warmBytesFresh(t, store)
	scanned, discarded := mem.LastWarmupBytes()

	// The fingerprint is base64 in the stored row, so that is the length the
	// accounting must charge — this is the assertion that catches an
	// encoded/decoded mixup, which is the easy way to get a plausible wrong
	// number here.
	perRow := int64(base64.StdEncoding.EncodedLen(fpSize))
	wantDiscarded := perRow * files

	require.GreaterOrEqual(t, discarded[memTableBookFiles], wantDiscarded,
		"discarded bytes must charge the base64 length of the fingerprint (%d/row × %d rows)",
		perRow, files)

	// Discarded bytes are a subset of bytes read; they cannot exceed them.
	require.LessOrEqual(t, discarded[memTableBookFiles], scanned[memTableBookFiles],
		"discarded bytes exceed the bytes actually read — the two are measured against different things")

	// The whole point: on this fixture the blob IS the row. If the ratio came
	// back low, the premise behind removing the field from the row would be
	// wrong, and the test should say so rather than pass on a nonzero number.
	ratio := float64(discarded[memTableBookFiles]) / float64(scanned[memTableBookFiles])
	require.Greater(t, ratio, 0.9,
		"fixture rows are ~99%% fingerprint but only %.1f%% of scanned bytes were attributed to discarded fields",
		ratio*100)
}

// TestWarmupBytes_NoFingerprintMeansNothingDiscarded is the control. Without
// it, an implementation that simply returned the scanned total as the discarded
// total would pass every assertion above.
func TestWarmupBytes_NoFingerprintMeansNothingDiscarded(t *testing.T) {
	store := setupTestPebbleStore(t)
	store.WaitForWarmup()

	seedWarmBytesBook(t, store, "lean", 12, nil)

	mem := warmBytesFresh(t, store)
	scanned, discarded := mem.LastWarmupBytes()

	require.Greater(t, scanned[memTableBookFiles], int64(0),
		"rows were written, so the scan must report reading bytes")

	// Rows with no fingerprint, no segments and no transcript have nothing the
	// projection discards. A nonzero number here means the accounting is
	// charging fields it should not.
	require.Zero(t, discarded[memTableBookFiles],
		"nothing in these rows is discarded, but %d bytes were charged as discarded",
		discarded[memTableBookFiles])
}

// TestWarmupBytes_EveryWarmedTableIsAttributed mirrors the timing test's
// coverage assertion: a phase that scans but reports no byte total is the same
// blind spot in a different column.
func TestWarmupBytes_EveryWarmedTableIsAttributed(t *testing.T) {
	store := setupTestPebbleStore(t)
	store.WaitForWarmup()

	seedWarmBytesBook(t, store, "cover", 3, warmBytesFingerprint(2048))

	mem := warmBytesFresh(t, store)
	scanned, _ := mem.LastWarmupBytes()
	require.NotEmpty(t, scanned, "warmup must record per-phase byte totals")

	rows, _ := mem.LastWarmupCounts()
	for table, n := range rows {
		got, ok := scanned[table]
		require.True(t, ok,
			"table %q has a warmup row count but no recorded byte total", table)
		if n > 0 {
			require.Greater(t, got, int64(0),
				"table %q admitted %d rows but reports reading 0 bytes", table, n)
		}
	}
}

// TestWarmupBytes_AccessorHandsOutACopy guards against a caller scribbling on
// the returned maps and corrupting the next reader's view — the same contract
// LastWarmupDurations has.
func TestWarmupBytes_AccessorHandsOutACopy(t *testing.T) {
	store := setupTestPebbleStore(t)
	store.WaitForWarmup()

	seedWarmBytesBook(t, store, "copy", 2, warmBytesFingerprint(1024))

	mem := warmBytesFresh(t, store)
	scanned, discarded := mem.LastWarmupBytes()
	require.NotEmpty(t, scanned)

	for k := range scanned {
		scanned[k] = -1
	}
	for k := range discarded {
		discarded[k] = -1
	}

	scanned2, discarded2 := mem.LastWarmupBytes()
	for table, v := range scanned2 {
		require.GreaterOrEqual(t, v, int64(0),
			"table %q leaked a mutation from a previously returned scanned map", table)
	}
	for table, v := range discarded2 {
		require.GreaterOrEqual(t, v, int64(0),
			"table %q leaked a mutation from a previously returned discarded map", table)
	}
}

// TestBytesMegabytes_NonzeroNeverRendersAsZero pins the rounding contract of
// the log rendering. Plain integer division prints 0 for anything under a
// megabyte, which makes "read nothing" and "read a little" the same value in
// the log — and a zero that can mean two things is not a measurement.
func TestBytesMegabytes_NonzeroNeverRendersAsZero(t *testing.T) {
	const mb = 1 << 20
	cases := map[string]struct {
		in   int64
		want int64
	}{
		"exactly zero":      {0, 0},
		"one byte":          {1, 1},
		"just under 1 MB":   {mb - 1, 1},
		"exactly 1 MB":      {mb, 1},
		"just over 1 MB":    {mb + 1, 2},
		"a few gigabytes":   {3 * 1024 * mb, 3 * 1024},
		"forty gigabytes":   {40 * 1024 * mb, 40 * 1024},
		"exactly 2 MB flat": {2 * mb, 2},
	}

	in := make(map[string]int64, len(cases))
	want := make(map[string]int64, len(cases))
	for name, c := range cases {
		in[name] = c.in
		want[name] = c.want
	}

	got := bytesMegabytes(in)
	require.Equal(t, want, got)
}
