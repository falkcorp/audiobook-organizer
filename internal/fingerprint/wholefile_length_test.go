// file: internal/fingerprint/wholefile_length_test.go
// version: 1.0.0
// guid: c8ba97ba-1386-4b67-a268-646c1f52286d
// last-edited: 2026-09-12

package fingerprint

import (
	"errors"
	"reflect"
	"testing"
)

// TestFpcalcArgs_DefaultIsFpcalcsOwn120 pins the default window to 120 s,
// fpcalc's built-in -length default, which is what every stored fingerprint
// was made with. Changing it would silently make new prints incomparable
// with the ~420k already stored.
func TestFpcalcArgs_DefaultIsFpcalcsOwn120(t *testing.T) {
	if DefaultAnalysisLengthSec != 120 {
		t.Fatalf("DefaultAnalysisLengthSec = %d; must stay 120 (fpcalc's default) unless the owner re-fingerprints the library", DefaultAnalysisLengthSec)
	}
	got := fpcalcArgs("/a/b.mp3", DefaultAnalysisLengthSec)
	want := []string{"-json", "-length", "120", "/a/b.mp3"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("fpcalcArgs default = %v, want %v", got, want)
	}
	whole := fpcalcArgs("/a/b.mp3", WholeFileAnalysisLength)
	if !reflect.DeepEqual(whole, []string{"-json", "-length", "0", "/a/b.mp3"}) {
		t.Errorf("fpcalcArgs whole-file = %v, want -length 0", whole)
	}
}

func TestFileFingerprintLength_RejectsNegative(t *testing.T) {
	_, err := FileFingerprintLength("/nonexistent.mp3", -1)
	if err == nil || errors.Is(err, ErrNotAvailable) {
		t.Fatalf("negative length must be rejected before fpcalc lookup, got %v", err)
	}
}
