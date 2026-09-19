// file: internal/plugins/acoustid/fingerprint_era_test.go
// version: 1.0.0
// guid: 8c1f3e76-4a2b-4d9e-9f05-b7e2a6c4d830
// last-edited: 2026-09-19

package acoustid

import (
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
)

// TestOnlineLookup_LegacyPrintNotSent: a legacy-era print is not a valid
// AcoustID submission; sending it returns "no match" and the LookedUpAt stamp
// would bury the file. It must be ineligible (counted as needs-refingerprint).
func TestOnlineLookup_LegacyPrintNotSent(t *testing.T) {
	legacy := &database.BookFileCore{AcoustIDFingerprintDurationSec: 600}
	current := &database.BookFileCore{AcoustIDFingerprintDurationSec: 600, AcoustIDFPVersion: fingerprint.PrintEncodingVersion}
	if onlineLookupPrintUsable(legacy) {
		t.Fatal("legacy-era print would be sent to AcoustID")
	}
	if !onlineLookupPrintUsable(current) {
		t.Fatal("current-era print not eligible")
	}
}

// TestStampFreshPrint_ClearsOnlineLookupVerdict: re-fingerprinting stamps the
// current version and clears the old online-lookup verdict so the next
// online lookup retries the file with the new print.
func TestStampFreshPrint_ClearsOnlineLookupVerdict(t *testing.T) {
	then := time.Now()
	f := database.BookFile{AcoustIDOnlineLookedUpAt: &then, AcoustIDOnlineRecordingID: "mb-x", AcoustIDOnlineScore: 0.4}
	stampFreshPrint(&f)
	if f.AcoustIDFPVersion != fingerprint.PrintEncodingVersion {
		t.Fatalf("version %d, want %d", f.AcoustIDFPVersion, fingerprint.PrintEncodingVersion)
	}
	if f.AcoustIDOnlineLookedUpAt != nil || f.AcoustIDOnlineRecordingID != "" || f.AcoustIDOnlineScore != 0 {
		t.Fatalf("online-lookup verdict not cleared: %+v", f)
	}
}

// TestSynthesizeBookSignature_LegacyOnlyClearsOldSignature: a book whose
// files are all legacy-era has no usable data; its existing (garbage)
// signature must be deleted, not left in place. A book with current-era
// files gets a signature stamped with the current version.
func TestSynthesizeBookSignature_LegacyOnlyClearsOldSignature(t *testing.T) {
	seg := makeEraSegment()
	for _, tc := range []struct {
		name      string
		version   int
		wantClear bool
	}{
		{"legacy_files", 0, true},
		{"current_files", fingerprint.PrintEncodingVersion, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := "garbage"
			book := &database.Book{ID: "b1", BookSigV1: &old}
			var cleared int
			var written *database.Book
			m := &database.MockStore{}
			m.GetBookByIDFunc = func(string) (*database.Book, error) { return book, nil }
			m.GetBookFilesFunc = func(string) ([]database.BookFile, error) {
				return []database.BookFile{{ID: "f1", BookID: "b1", AcoustIDSeg0: seg, AcoustIDFPVersion: tc.version}}, nil
			}
			m.ClearBookSignatureFunc = func(string) error { cleared++; return nil }
			m.ModifyBookFunc = func(id string, fn func(*database.Book) error) (*database.Book, error) {
				cp := *book
				if err := fn(&cp); err != nil {
					return nil, err
				}
				written = &cp
				return &cp, nil
			}
			if err := synthesizeBookSignatureForBook(m, "b1"); err != nil {
				t.Fatalf("synthesize: %v", err)
			}
			if tc.wantClear {
				if cleared != 1 || written != nil {
					t.Fatalf("cleared=%d written=%v: legacy-only book must delete its old signature and write none", cleared, written != nil)
				}
				return
			}
			if cleared != 0 || written == nil || written.BookSigVersion == nil || *written.BookSigVersion != fingerprint.BookSignatureVersion {
				t.Fatalf("current-era book: cleared=%d written=%+v, want a version-stamped signature", cleared, written)
			}
		})
	}
}

func makeEraSegment() string {
	raw := make([]byte, 500*4)
	for i := range raw {
		raw[i] = byte(i * 13)
	}
	return fingerprint.EncodeWholeFingerprint(raw)
}
