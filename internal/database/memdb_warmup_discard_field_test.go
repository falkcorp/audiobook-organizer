// file: internal/database/memdb_warmup_discard_field_test.go
// version: 1.1.0
// guid: c4e08b52-71a9-4d36-9f2b-6a30df518e7c
// last-edited: 2026-08-13

package database

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The aggregate shipped first said 76% of the book_files phase is read only to
// be discarded (production 2026-08-13: 1,853 MB of 2,436 MB). It does not say
// WHICH field, and the candidates are not interchangeable:
//
//   - AcoustIDFingerprint sits behind write-back preserve-guards that exist
//     because a bare memdb round-trip once wiped fingerprints in production.
//     Moving it out of the row retires those guards and needs an atomic batch.
//   - IntroTranscription and the diagnostic strings have neither.
//
// So the per-field split has to be trustworthy in a specific way: each group's
// bytes must land under ITS OWN key. A version that charged everything to the
// fingerprint would produce a confident, wrong recommendation to do the
// expensive and dangerous thing. These tests seed one group at a time so
// cross-attribution cannot hide.

func discardStrPtr(s string) *string { return &s }

// seedOneBookFile writes a single book_file row, letting the caller populate
// exactly one discarded field group.
func seedOneBookFile(t *testing.T, store *PebbleStore, tag string, mutate func(*BookFile)) {
	t.Helper()
	hash := "discardfield-" + tag
	book, err := store.CreateBook(&Book{
		Title:    "Discard Field " + tag,
		FilePath: "/tmp/discardfield_" + tag + ".m4b",
		FileHash: &hash,
	})
	require.NoError(t, err)

	fh := "discardfield-file-" + tag
	bf := &BookFile{
		BookID:   book.ID,
		FilePath: "/tmp/discardfield_" + tag + ".mp3",
		FileHash: fh,
		Format:   "mp3",
	}
	if mutate != nil {
		mutate(bf)
	}
	require.NoError(t, store.CreateBookFile(bf))
}

// TestDiscardByField_ChargesEachGroupToItsOwnKey is the load-bearing case. Each
// subtest populates exactly ONE group, so any cross-attribution shows up as a
// nonzero total under a key that should be untouched.
func TestDiscardByField_ChargesEachGroupToItsOwnKey(t *testing.T) {
	const blob = 8192

	cases := []struct {
		name     string
		key      string
		mutate   func(*BookFile)
		wantSize int64
	}{
		{
			name: "fingerprint",
			key:  DiscardFieldAcoustIDFingerprint,
			mutate: func(bf *BookFile) {
				bf.AcoustIDFingerprint = warmBytesFingerprint(blob)
			},
			// A []byte is base64 in the stored row, so the charge is the
			// ENCODED length. Charging the decoded length here would understate
			// the fingerprint's share by 4/3 — and understating precisely the
			// field whose removal is expensive is the way to reach the wrong
			// recommendation while looking rigorous.
			wantSize: int64(base64.StdEncoding.EncodedLen(blob)),
		},
		// AcoustIDSeg0..6 is deliberately absent from this table — it cannot be
		// seeded through CreateBookFile at all. See
		// TestDiscardByField_SegmentsAreChargedOnlyFromLegacyRows.
		{
			name: "intro transcription",
			key:  DiscardFieldIntroTranscription,
			mutate: func(bf *BookFile) {
				bf.IntroTranscription = discardStrPtr(strings.Repeat("t", blob))
			},
			wantSize: blob,
		},
		{
			name: "fingerprint diagnostics",
			key:  DiscardFieldFingerprintDiagnostics,
			mutate: func(bf *BookFile) {
				bf.FingerprintDiagnosticJSON = discardStrPtr(strings.Repeat("d", blob))
			},
			wantSize: blob,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := setupTestPebbleStore(t)
			store.WaitForWarmup()
			seedOneBookFile(t, store, tc.name, tc.mutate)

			mem := warmBytesFresh(t, store)
			byField := mem.LastWarmupDiscardedByField()

			require.GreaterOrEqual(t, byField[tc.key], tc.wantSize,
				"%q must be charged at least the %d bytes seeded into it", tc.key, tc.wantSize)

			// Every OTHER book_file group must stay at zero. This is the
			// assertion that catches an implementation which sums correctly in
			// total but attributes to the wrong key.
			for _, other := range []string{
				DiscardFieldAcoustIDFingerprint,
				DiscardFieldAcoustIDSegments,
				DiscardFieldIntroTranscription,
				DiscardFieldFingerprintDiagnostics,
			} {
				if other == tc.key {
					continue
				}
				require.Zero(t, byField[other],
					"seeded only %q, but %q was charged %d bytes", tc.key, other, byField[other])
			}
		})
	}
}

// TestDiscardByField_SegmentsAreChargedOnlyFromLegacyRows records a fact that
// changes how the production number must be read.
//
// AcoustIDSeg0..6 CANNOT be seeded through CreateBookFile: the write path
// deliberately omits them from the stored row and keeps them only in the
// book_file_acoustid: secondary index — pinned by
// TestCreateBookFile_StoredValueLacksSegs. A row written by the current code
// therefore contributes exactly zero to this group.
//
// So a nonzero acoustid_seg0_6 in a production discarded_field_mb is NOT a
// measure of ongoing cost that a schema change would remove. It is a measure of
// how much un-migrated legacy data is still sitting in the keyspace, and the
// remedy is a backfill, not a redesign. Reading it as the former would aim an
// expensive fix at bytes that new writes already stopped producing.
//
// The row here is written as raw JSON precisely because the supported API
// refuses to produce one.
func TestDiscardByField_SegmentsAreChargedOnlyFromLegacyRows(t *testing.T) {
	const segLen = 4096

	store := setupTestPebbleStore(t)
	store.WaitForWarmup()

	hash := "discardfield-legacy"
	book, err := store.CreateBook(&Book{
		Title:    "Discard Field Legacy",
		FilePath: "/tmp/discardfield_legacy.m4b",
		FileHash: &hash,
	})
	require.NoError(t, err)

	// Control: the supported write path really does drop the segs, so the
	// legacy charge below cannot be coming from this row.
	seedOneBookFile(t, store, "legacy-control", func(bf *BookFile) {
		bf.AcoustIDSeg0 = strings.Repeat("a", segLen)
	})
	mem := warmBytesFresh(t, store)
	require.Zero(t, mem.LastWarmupDiscardedByField()[DiscardFieldAcoustIDSegments],
		"CreateBookFile omits segs from the stored row, so nothing should be charged yet")

	// Now a legacy-format row, written straight to Pebble.
	legacy := fmt.Sprintf(
		`{"id":"01LEGACYSEG","book_id":%q,"file_path":"/tmp/legacy_seg.mp3","acoustid_seg0":%q}`,
		book.ID, strings.Repeat("a", segLen))
	require.NoError(t,
		store.db.Set([]byte("book_file:"+book.ID+":01LEGACYSEG"), []byte(legacy), nil))

	mem = warmBytesFresh(t, store)
	byField := mem.LastWarmupDiscardedByField()
	require.GreaterOrEqual(t, byField[DiscardFieldAcoustIDSegments], int64(segLen),
		"a legacy row carrying acoustid_seg0 must be charged to the segments group")
}

// TestDiscardByField_CoversTheBooksPhaseToo pins that the books phase — 729 MB
// across 67,824 rows on production, the second largest — is accounted at all.
// It was unmeasured in the first version, which meant a fifth of the discarded
// bytes in the library had no owner.
//
// The book_sig_v1_and_mask half is now the same shape as
// TestDiscardByField_SegmentsAreChargedOnlyFromLegacyRows, and for the same
// reason: since the signature moved to the book_sig: sidecar
// (pebble_store_booksig.go), CreateBook/UpdateBook strip it from the book: row,
// so the supported write path can no longer produce an inline one. Nothing
// asserts that the books phase charges a signature for a CURRENTLY-WRITABLE
// book, because that state does not exist any more.
//
// Read a nonzero book_sig_v1_and_mask in production the same way: it measures
// un-migrated legacy rows draining toward zero as the migration op runs, not
// ongoing cost that new writes keep re-creating.
func TestDiscardByField_CoversTheBooksPhaseToo(t *testing.T) {
	const blob = 4096

	store := setupTestPebbleStore(t)
	store.WaitForWarmup()

	hash := "discardfield-book"
	sig := strings.Repeat("S", blob)
	desc := strings.Repeat("D", blob)
	_, err := store.CreateBook(&Book{
		Title:       "Discard Field Book",
		FilePath:    "/tmp/discardfield_book.m4b",
		FileHash:    &hash,
		Description: &desc,
		// BookSigV1 is ALREADY a base64 string in the struct, not a []byte.
		// Running EncodedLen over it would over-charge it by 4/3 and inflate
		// the books phase against book_files.
		BookSigV1: &sig,
	})
	require.NoError(t, err)

	mem := warmBytesFresh(t, store)
	byField := mem.LastWarmupDiscardedByField()

	require.GreaterOrEqual(t, byField[DiscardFieldDescription], int64(blob),
		"the books phase must charge Description/VersionNotes")

	// Control: Description above proves the books phase IS being walked, so a
	// zero here is the sidecar strip working, not the phase going unmeasured.
	require.Zero(t, byField[DiscardFieldBookSignature],
		"CreateBook writes the signature to the book_sig: sidecar, so the book: row must carry none")

	// Now a legacy-format row, written straight to Pebble the way rows looked
	// before the sidecar existed.
	legacy := fmt.Sprintf(
		`{"id":"01LEGACYSIG","title":"Legacy Sig","file_path":"/tmp/legacy_sig.m4b","book_sig_v1":%q}`,
		sig)
	require.NoError(t, store.db.Set([]byte("book:01LEGACYSIG"), []byte(legacy), nil))

	mem = warmBytesFresh(t, store)
	byField = mem.LastWarmupDiscardedByField()

	require.GreaterOrEqual(t, byField[DiscardFieldBookSignature], int64(blob),
		"a legacy row carrying book_sig_v1 inline must be charged to the signature group")
	require.Less(t, byField[DiscardFieldBookSignature], int64(blob*4/3),
		"BookSigV1 is already base64; charging it EncodedLen over-states it by 4/3")
}

// TestDiscardByField_SumsToThePerPhaseTotal is the consistency check between
// the two views. If they disagree, one of them is wrong and neither can be
// trusted to pick a fix — which is the only thing these numbers exist for.
func TestDiscardByField_SumsToThePerPhaseTotal(t *testing.T) {
	store := setupTestPebbleStore(t)
	store.WaitForWarmup()

	for i := 0; i < 4; i++ {
		tag := fmt.Sprintf("sum%02d", i)
		seedOneBookFile(t, store, tag, func(bf *BookFile) {
			bf.AcoustIDFingerprint = warmBytesFingerprint(2048)
			bf.AcoustIDSeg0 = strings.Repeat("a", 128)
			bf.IntroTranscription = discardStrPtr(strings.Repeat("t", 512))
		})
	}

	mem := warmBytesFresh(t, store)
	_, discarded := mem.LastWarmupBytes()
	byField := mem.LastWarmupDiscardedByField()

	var fieldSum int64
	for _, v := range byField {
		fieldSum += v
	}

	var phaseSum int64
	for _, v := range discarded {
		phaseSum += v
	}

	require.Equal(t, phaseSum, fieldSum,
		"per-field totals (%d) and per-phase totals (%d) describe the same bytes and must agree",
		fieldSum, phaseSum)
	require.Greater(t, fieldSum, int64(0), "the fixture seeds discarded bytes, so the sum cannot be zero")
}
