// file: internal/plugins/acoustid/backfill_test.go
// version: 1.2.1
// guid: f7a8b9c0-d1e2-4f3a-4b5c-6d7e8f9a0123
// last-edited: 2026-09-02

package acoustid

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// makeBookFile builds a BookFile with sensible defaults plus the overrides
// in mods. Defaults reflect a "freshly scanned, not yet fingerprinted" row.
func makeBookFile(mods func(*database.BookFile)) database.BookFile {
	f := database.BookFile{
		ID:       "bf-test",
		BookID:   "book-test",
		FilePath: "/tmp/does-not-exist.m4b",
	}
	if mods != nil {
		mods(&f)
	}
	return f
}

func TestFingerprintEligibility_SkipsWhenWholeFilePresent(t *testing.T) {
	f := makeBookFile(func(bf *database.BookFile) {
		bf.AcoustIDFingerprint = []byte("not really a fingerprint but non-empty")
	})
	got, _, stop := fingerprintEligibility(f, false)
	if !stop {
		t.Fatal("expected stop=true when whole-file fp already present")
	}
	if got != fingerprintOutcomeSkipped {
		t.Errorf("expected skipped, got %v", got)
	}
}

func TestFingerprintEligibility_SkipsWhenSeg0Present(t *testing.T) {
	f := makeBookFile(func(bf *database.BookFile) {
		bf.AcoustIDSeg0 = "AQADtAcSRY"
	})
	got, _, stop := fingerprintEligibility(f, false)
	if !stop {
		t.Fatal("expected stop=true when seg0 already present")
	}
	if got != fingerprintOutcomeSkipped {
		t.Errorf("expected skipped, got %v", got)
	}
}

func TestFingerprintEligibility_ForceOverridesPresentSeg0(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "test.m4b")
	if err := os.WriteFile(path, []byte("not real audio but the path resolves"), 0o644); err != nil {
		t.Fatal(err)
	}

	f := makeBookFile(func(bf *database.BookFile) {
		bf.FilePath = path
		bf.AcoustIDSeg0 = "AQADtAcSRY"
	})
	got, _, stop := fingerprintEligibility(f, true) // force
	if stop {
		t.Fatalf("expected stop=false with force=true, got stop=true outcome=%v", got)
	}
}

func TestFingerprintEligibility_ForceOverridesWholeFile(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "test.flac")
	if err := os.WriteFile(path, []byte("not real audio"), 0o644); err != nil {
		t.Fatal(err)
	}

	f := makeBookFile(func(bf *database.BookFile) {
		bf.FilePath = path
		bf.AcoustIDFingerprint = []byte("existing fp bytes")
	})
	got, _, stop := fingerprintEligibility(f, true)
	if stop {
		t.Fatalf("expected stop=false with force=true, got stop=true outcome=%v", got)
	}
}

func TestFingerprintEligibility_IneligibleWhenMissing(t *testing.T) {
	f := makeBookFile(func(bf *database.BookFile) { bf.Missing = true })
	got, _, stop := fingerprintEligibility(f, false)
	if !stop {
		t.Fatal("expected stop=true when Missing=true")
	}
	if got != fingerprintOutcomeIneligible {
		t.Errorf("expected ineligible, got %v", got)
	}
}

func TestFingerprintEligibility_IneligibleWhenEmptyPath(t *testing.T) {
	f := makeBookFile(func(bf *database.BookFile) { bf.FilePath = "" })
	got, _, stop := fingerprintEligibility(f, false)
	if !stop {
		t.Fatal("expected stop=true with empty file path")
	}
	if got != fingerprintOutcomeIneligible {
		t.Errorf("expected ineligible, got %v", got)
	}
}

func TestFingerprintEligibility_IneligibleWhenBadExtension(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "test.pdf")
	if err := os.WriteFile(path, []byte("not audio"), 0o644); err != nil {
		t.Fatal(err)
	}

	f := makeBookFile(func(bf *database.BookFile) { bf.FilePath = path })
	got, _, stop := fingerprintEligibility(f, false)
	if !stop {
		t.Fatal("expected stop=true for non-audio extension")
	}
	if got != fingerprintOutcomeIneligible {
		t.Errorf("expected ineligible, got %v", got)
	}
}

func TestFingerprintEligibility_IneligibleWhenFileDoesNotExist(t *testing.T) {
	f := makeBookFile(func(bf *database.BookFile) {
		bf.FilePath = "/this/path/definitely/does/not/exist.m4b"
	})
	got, _, stop := fingerprintEligibility(f, false)
	if !stop {
		t.Fatal("expected stop=true when file missing on disk")
	}
	if got != fingerprintOutcomeIneligible {
		t.Errorf("expected ineligible, got %v", got)
	}
}

func TestFingerprintEligibility_ProceedsWhenAllChecksPass(t *testing.T) {
	tmp := t.TempDir()
	for _, ext := range []string{".m4b", ".mp3", ".flac", ".m4a", ".ogg", ".opus", ".aac", ".wav"} {
		t.Run(ext, func(t *testing.T) {
			path := filepath.Join(tmp, "ok"+ext)
			if err := os.WriteFile(path, []byte("placeholder"), 0o644); err != nil {
				t.Fatal(err)
			}
			f := makeBookFile(func(bf *database.BookFile) { bf.FilePath = path })
			outcome, _, stop := fingerprintEligibility(f, false)
			if stop {
				t.Fatalf("expected stop=false for %s, got outcome=%v", ext, outcome)
			}
		})
	}
}

func TestAudioExtensions_KnownFormats(t *testing.T) {
	want := []string{".aac", ".aiff", ".alac", ".ape", ".flac", ".m4a", ".m4b", ".mp3", ".ogg", ".opus", ".wav", ".wma", ".wv"}
	for _, ext := range want {
		if !fpcalcDecodableExtensions[ext] {
			t.Errorf("expected %q in fpcalcDecodableExtensions", ext)
		}
	}
}

func TestAudioExtensions_RejectsNonAudio(t *testing.T) {
	for _, ext := range []string{".txt", ".pdf", ".jpg", ".mkv", ".mp4", ".avi", ".epub"} {
		if fpcalcDecodableExtensions[ext] {
			t.Errorf("did not expect %q in fpcalcDecodableExtensions", ext)
		}
	}
}

func TestFingerprintEligibility_IneligibleWhenPermanentlyFailed(t *testing.T) {
	now := time.Now()
	f := makeBookFile(func(bf *database.BookFile) {
		bf.FingerprintFailedAt = &now
	})
	got, reason, stop := fingerprintEligibility(f, false)
	if !stop {
		t.Fatal("expected stop=true for permanently-failed file")
	}
	if got != fingerprintOutcomeIneligible {
		t.Errorf("expected fingerprintOutcomeIneligible, got %v", got)
	}
	if reason != "permanent_failure" {
		t.Errorf("expected reason=permanent_failure, got %q", reason)
	}
}

func TestFingerprintEligibility_ForceOverridesPermanentFailure(t *testing.T) {
	now := time.Now()
	f := makeBookFile(func(bf *database.BookFile) {
		bf.FingerprintFailedAt = &now
		// File doesn't exist on disk — expect file_not_found ineligible,
		// not permanent_failure (force bypasses the tombstone check).
	})
	got, reason, stop := fingerprintEligibility(f, true)
	if !stop {
		t.Fatal("expected stop=true because file does not exist on disk")
	}
	if got != fingerprintOutcomeIneligible {
		t.Errorf("expected fingerprintOutcomeIneligible, got %v", got)
	}
	// With force=true, permanent_failure is bypassed; we get file_not_found.
	if reason == "permanent_failure" {
		t.Errorf("force=true should bypass permanent_failure tombstone, got %q", reason)
	}
}

func TestFingerprintEligibility_SkipsWhenDurationProxySet(t *testing.T) {
	// AcoustIDFingerprintDurationSec > 0 means the file has a whole-file fp
	// in Pebble even if AcoustIDFingerprint is nil (stripped from memdb rows).
	f := makeBookFile(func(bf *database.BookFile) {
		bf.AcoustIDFingerprintDurationSec = 3600.0 // 1 hour, fingerprint present
	})
	got, _, stop := fingerprintEligibility(f, false)
	if !stop {
		t.Fatal("expected stop=true when AcoustIDFingerprintDurationSec > 0")
	}
	if got != fingerprintOutcomeSkipped {
		t.Errorf("expected fingerprintOutcomeSkipped for duration proxy, got %v", got)
	}
}
