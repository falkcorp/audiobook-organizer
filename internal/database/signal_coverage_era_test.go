// file: internal/database/signal_coverage_era_test.go
// version: 1.0.0
// guid: 5f19da07-0c20-4ad3-ad8c-47c9b90bac57
// last-edited: 2026-09-19

package database

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The head-print era split is the progress meter for the nightly
// re-fingerprint: present rows with a print, split by AcoustIDFPVersion.
// Missing rows and rows without a print are not in the denominator.
func TestCountBookFileSignals_HeadPrintEra(t *testing.T) {
	cur := fingerprint.PrintEncodingVersion
	rows := []BookFile{
		{ID: "a", AcoustIDFingerprint: []byte{1}, AcoustIDFingerprintDurationSec: 60, AcoustIDFPVersion: cur},
		{ID: "b", AcoustIDFingerprint: []byte{1}, AcoustIDFingerprintDurationSec: 60, AcoustIDFPVersion: cur + 1},
		{ID: "c", AcoustIDFingerprint: []byte{1}, AcoustIDFingerprintDurationSec: 60},                // legacy
		{ID: "d", AcoustIDFingerprint: []byte{1}, AcoustIDFingerprintDurationSec: 60, Missing: true}, // missing: excluded
		{ID: "e", AcoustIDFingerprint: []byte{1}, AcoustIDFPVersion: cur, Missing: true},             // missing: excluded
		{ID: "f"}, // no print
		{ID: "g", AcoustIDFingerprint: []byte{1}}, // raw, no duration: legacy (exact only)
	}
	at := func(i int) *BookFile { return &rows[i] }

	exact, err := CountBookFileSignals(context.Background(), len(rows), at, true, 3, "test")
	require.NoError(t, err)
	require.NotNil(t, exact.HeadPrintEra)
	assert.Equal(t, HeadPrintEraCoverage{
		Basis: SignalRawFingerprint, EncodingVersion: cur,
		PresentWithPrint: 4, CurrentEra: 2, LegacyEra: 2,
	}, *exact.HeadPrintEra)

	proxy, err := CountBookFileSignals(context.Background(), len(rows), at, false, 3, "test")
	require.NoError(t, err)
	require.NotNil(t, proxy.HeadPrintEra)
	assert.Equal(t, HeadPrintEraCoverage{
		Basis: SignalFingerprintDuration, EncodingVersion: cur,
		PresentWithPrint: 3, CurrentEra: 2, LegacyEra: 1,
	}, *proxy.HeadPrintEra, "the memdb path detects a print by the duration proxy")
	assert.NotContains(t, proxy.Unavailable, "legacy_print_era")
}

// The deep scan reads acoustid_fp_version off the Pebble row, and the memdb
// row keeps it (it is not stripped), so both paths split the era.
func TestGetBookFileSignalCoverage_HeadPrintEraBothPaths(t *testing.T) {
	s := setupTestPebbleStore(t)
	book, err := s.CreateBook(&Book{Title: "Era", FilePath: "/test/era"})
	require.NoError(t, err)
	for i, v := range []int{fingerprint.PrintEncodingVersion, 0, 0} {
		f := &BookFile{BookID: book.ID, FilePath: "/test/era/" + string(rune('a'+i)) + ".m4b",
			AcoustIDFingerprint: []byte{1, 2, 3, 4}, AcoustIDFingerprintDurationSec: 60, AcoustIDFPVersion: v}
		require.NoError(t, s.CreateBookFile(f))
	}
	s.WaitForWarmup()
	require.True(t, s.IsMemReady())
	for _, deep := range []bool{true, false} {
		cov, err := s.GetBookFileSignalCoverage(context.Background(), deep, 2)
		require.NoError(t, err)
		require.NotNil(t, cov.HeadPrintEra, "deep=%v", deep)
		assert.EqualValues(t, 3, cov.HeadPrintEra.PresentWithPrint, "deep=%v", deep)
		assert.EqualValues(t, 1, cov.HeadPrintEra.CurrentEra, "deep=%v", deep)
		assert.EqualValues(t, 2, cov.HeadPrintEra.LegacyEra, "deep=%v", deep)
	}
}

// Book signatures live in the book_sig: sidecar, but a book the sidecar
// migration has not reached still carries its signature inline on the book:
// row (hydrateBookSig is fallback-first). The census reads both, sidecar
// winning, and skips soft-deleted books.
func TestCountBookSignatureEras(t *testing.T) {
	s := setupTestPebbleStore(t)
	sig := "AAAA"
	cur, old := fingerprint.BookSignatureVersion, fingerprint.BookSignatureVersion-1
	yes := true

	mk := func(title string, v *int, withSig bool) *Book {
		b := &Book{Title: title, FilePath: "/test/sig/" + title}
		if withSig {
			b.BookSigV1, b.BookSigVersion = &sig, v
		}
		created, err := s.CreateBook(b)
		require.NoError(t, err)
		return created
	}
	mk("current", &cur, true)
	mk("legacy-version", &old, true)
	mk("legacy-unversioned", nil, true)
	mk("none", nil, false)
	del := mk("deleted-current", &cur, true)
	del.MarkedForDeletion = &yes
	_, err := s.UpdateBook(del.ID, del)
	require.NoError(t, err)

	// An un-migrated book: the signature inline on the row, no sidecar.
	inline := Book{ID: "inline-legacy", Title: "inline", FilePath: "/test/sig/inline", BookSigV1: &sig}
	raw, err := json.Marshal(inline)
	require.NoError(t, err)
	require.NoError(t, s.db.Set([]byte(bookRowPrefix+inline.ID), raw, nil))
	_, closer, gerr := s.db.Get(bookSigKey(inline.ID))
	if gerr == nil {
		closer.Close()
	}
	require.Error(t, gerr, "fixture: the inline book must have no sidecar")

	got, err := s.CountBookSignatureEras(context.Background(), 3)
	require.NoError(t, err)
	assert.Equal(t, BookSignatureEraCoverage{
		SignatureVersion: cur,
		LiveBooks:        5,
		WithSignature:    4,
		CurrentEra:       1,
		LegacyEra:        3,
		NoSignature:      1,
		InlineUnmigrated: 1,
	}, *got)
}
