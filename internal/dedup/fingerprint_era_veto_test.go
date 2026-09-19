// file: internal/dedup/fingerprint_era_veto_test.go
// version: 1.0.0
// guid: 2f8d4a61-9c37-4e05-b1a8-6d3e7c0f5b94
// last-edited: 2026-09-19

package dedup

import (
	"encoding/base64"
	"encoding/binary"
	"math/rand"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
)

// eraTestSig builds a full-length signature; mostlyFlipped XORs ~90% of the
// bits of the seed's words so the pair scores ~0.1, well under the veto
// threshold (random pairs sit at ~0.5, too close to it to test with).
// curSigV returns a pointer to the current BookSignatureVersion for fixtures.
func curSigV() *int { v := fingerprint.BookSignatureVersion; return &v }

func eraTestSig(seed int64, mostlyFlipped bool) string {
	rng := rand.New(rand.NewSource(seed))
	flip := rand.New(rand.NewSource(99))
	buf := make([]byte, fingerprint.BookSignatureFixedLength*4)
	for i := 0; i < fingerprint.BookSignatureFixedLength; i++ {
		w := rng.Uint32()
		if mostlyFlipped {
			w ^= ^(flip.Uint32() & flip.Uint32() & flip.Uint32() & flip.Uint32()) // clears ~6% of the mask bits
		}
		binary.LittleEndian.PutUint32(buf[i*4:], w)
	}
	return base64.StdEncoding.EncodeToString(buf)
}

// TestAcoustIDVeto_LegacySignatureIsMissingNotMismatch: after the decoder
// fix, a book whose signature predates it (built from misdecoded prints)
// scores ~0.5 against a re-synthesized signature of the SAME audio — under
// acoustIDVetoMaxSimilarity. The veto must treat the legacy signature as
// missing (no veto, candidate survives), while two current-era signatures of
// different audio must still veto.
func TestAcoustIDVeto_LegacySignatureIsMissingNotMismatch(t *testing.T) {
	engine, mock, _ := setupTestEngine(t)
	cur := fingerprint.BookSignatureVersion
	// The legacy signature scores like a clear mismatch against sigNew —
	// exactly what a misdecoded pre-fix signature of the same audio can do.
	sigNew, sigLegacy, sigOther := eraTestSig(1, false), eraTestSig(1, true), eraTestSig(1, true)
	books := map[string]*database.Book{
		"NEW":    {ID: "NEW", BookSigV1: &sigNew, BookSigVersion: &cur},
		"LEGACY": {ID: "LEGACY", BookSigV1: &sigLegacy}, // no version: pre-fix
		"OTHER":  {ID: "OTHER", BookSigV1: &sigOther, BookSigVersion: &cur},
	}
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) { return books[id], nil }

	if engine.acoustIDSignaturesConflict(books["NEW"], books["LEGACY"]) {
		t.Fatal("legacy-era signature vetoed a candidate: must be treated as missing evidence")
	}
	if !engine.acoustIDSignaturesConflict(books["NEW"], books["OTHER"]) {
		t.Fatal("two current-era signatures of different audio no longer veto")
	}
}
