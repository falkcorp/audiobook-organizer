// file: internal/plugins/acoustid/fingerprint_era_test.go
// version: 1.2.0
// guid: 8c1f3e76-4a2b-4d9e-9f05-b7e2a6c4d830
// last-edited: 2026-09-19

package acoustid

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
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

// TestBackfill_ReFingerprintsLegacyRowsOnly: the nightly backfill must treat a
// legacy-era raw print as not done (so it re-fingerprints it) and a
// current-era print as done (so a resumed or repeated run never redoes it).
// A book whose files are all current but whose signature is legacy still gets
// its signature rebuilt.
func TestBackfill_ReFingerprintsLegacyRowsOnly(t *testing.T) {
	origAvail, origFn := fpcalcAvailable, fingerprintFileFn
	t.Cleanup(func() { fpcalcAvailable, fingerprintFileFn = origAvail, origFn })
	fpcalcAvailable = func() bool { return true }

	dir := t.TempDir()
	mk := func(name string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	legacy := database.BookFile{ID: "legacy", BookID: "b1", FilePath: mk("a.mp3"),
		AcoustIDFingerprint: []byte{1, 2, 3, 4}, AcoustIDFingerprintDurationSec: 120}
	current := database.BookFile{ID: "current", BookID: "b1", FilePath: mk("b.mp3"),
		AcoustIDFingerprint: []byte{1, 2, 3, 4}, AcoustIDFingerprintDurationSec: 120,
		AcoustIDFPVersion: fingerprint.PrintEncodingVersion}

	if !hasUsableFingerprint(current) {
		t.Fatal("current-era print not treated as done: a rerun would redo it")
	}
	if hasUsableFingerprint(legacy) {
		t.Fatal("legacy-era print treated as done: the backfill would never replace it")
	}

	var mu sync.Mutex
	var ran []string
	fingerprintFileFn = func(_ pluginStore, f database.BookFile, _ bool) fingerprintFileOutcome {
		mu.Lock()
		ran = append(ran, f.ID)
		mu.Unlock()
		return fingerprintOutcomeFingerprinted
	}
	for _, tc := range []struct {
		name  string
		files []database.BookFile
		want  []string
	}{
		{"mixed", []database.BookFile{legacy, current}, []string{"legacy"}},
		{"all_current", []database.BookFile{current}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ran = nil
			oldSig := "legacy-sig" // no BookSigVersion: pre-fix signature
			book := database.Book{ID: "b1", BookSigV1: &oldSig}
			var sigTouched int
			m := &database.MockStore{}
			m.GetBookFilesFunc = func(string) ([]database.BookFile, error) { return tc.files, nil }
			m.GetBookByIDFunc = func(string) (*database.Book, error) { cp := book; return &cp, nil }
			m.ClearBookSignatureFunc = func(string) error { sigTouched++; return nil }
			m.ModifyBookFunc = func(string, func(*database.Book) error) (*database.Book, error) {
				sigTouched++
				return &book, nil
			}
			p := &Plugin{store: m}
			var tally backfillTally
			if err := p.backfillBook(context.Background(), book, &tally, make(chan struct{}, 2), slog.New(slog.DiscardHandler)); err != nil {
				t.Fatalf("backfillBook: %v", err)
			}
			if len(ran) != len(tc.want) || (len(ran) == 1 && ran[0] != tc.want[0]) {
				t.Fatalf("fingerprinted %v, want %v", ran, tc.want)
			}
			if sigTouched == 0 {
				t.Fatal("legacy-era book signature was not rebuilt or cleared")
			}
		})
	}
}

// TestSynthesize_NoStoredSignatureMeansNoClearWrite: a book with no usable
// data and no signature on record must not be "cleared" — nightly, that is
// tens of thousands of pointless writes.
func TestSynthesize_NoStoredSignatureMeansNoClearWrite(t *testing.T) {
	var clears int
	m := &database.MockStore{}
	m.GetBookByIDFunc = func(string) (*database.Book, error) { return &database.Book{ID: "b"}, nil }
	m.GetBookFilesFunc = func(string) ([]database.BookFile, error) {
		return []database.BookFile{{ID: "f", BookID: "b", AcoustIDSeg0: makeEraSegment()}}, nil // legacy-era
	}
	m.ClearBookSignatureFunc = func(string) error { clears++; return nil }
	if err := synthesizeBookSignatureForBook(m, "b"); err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	if clears != 0 {
		t.Fatalf("issued %d clear write(s) for a book with no signature", clears)
	}
}

// TestDoFingerprintFile_SegmentFallbackNeverStampsVersion: the ffmpeg segment
// fallback writes no raw print, and the row it gets may be a projection
// without its blob — so it must not stamp the current version (which would
// certify whatever legacy print the stored row holds).
func TestDoFingerprintFile_SegmentFallbackNeverStampsVersion(t *testing.T) {
	origLen, origSegs := fileFingerprintLengthFn, fileSegmentsFn
	t.Cleanup(func() { fileFingerprintLengthFn, fileSegmentsFn = origLen, origSegs })
	fileFingerprintLengthFn = func(string, int) (*fingerprint.WholeFile, error) { return nil, fingerprint.ErrNotAvailable }
	fileSegmentsFn = func(string, int) (*fingerprint.Segments, error) {
		var s fingerprint.Segments
		s[0] = makeEraSegment()
		return &s, nil
	}
	var written *database.BookFile
	m := &database.MockStore{}
	m.UpdateBookFileFunc = func(_ string, f *database.BookFile) error { cp := *f; written = &cp; return nil }
	// Projection-shaped row: no blob, no duration, although the stored row
	// may hold a legacy print.
	row := database.BookFile{ID: "f", BookID: "b", FilePath: "/x.mp3"}
	if out := doFingerprintFile(m, row, false); out != fingerprintOutcomeFingerprinted {
		t.Fatalf("outcome %v", out)
	}
	if written == nil || written.AcoustIDFPVersion != 0 {
		t.Fatalf("segment fallback stamped version %v", written)
	}
}
