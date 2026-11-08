// file: core/app/binaryid_test.go
// version: 1.0.0
// guid: b2ff12fd-e6a2-4bd0-8748-29d938047130
// last-edited: 2026-11-08

package app

import (
	"regexp"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/buildid"
)

// TestBinaryIDDistinguishesBuilds (06 §6.10), the part R1 can prove in one
// process: BinaryID is buildid.Binary(opts.Version) and nothing more, so the
// version and the executable's hash are both in it. Two binaries of different
// code differ because buildid hashes the executable file itself, which
// buildid's own tests pin against the file's SHA-256; building a second
// binary here would only re-test the toolchain.
func TestBinaryIDDistinguishesBuilds(t *testing.T) {
	opts := Options{Version: "v1.2.3"}
	got, err := BinaryID(opts)
	if err != nil {
		t.Fatalf("BinaryID: %v", err)
	}
	want, err := buildid.Binary(opts.Version)
	if err != nil {
		t.Fatalf("buildid.Binary: %v", err)
	}
	if got != want {
		t.Fatalf("BinaryID = %q, want buildid.Binary(%q) = %q", got, opts.Version, want)
	}
	version, sum, ok := strings.Cut(got, "+")
	if !ok || version != "v1.2.3" || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(sum) {
		t.Fatalf("BinaryID = %q, want the version, a '+', then 64 lowercase hex", got)
	}

	// A different version is a different build identity over the same file.
	other, err := BinaryID(Options{Version: "v1.2.4"})
	if err != nil {
		t.Fatalf("BinaryID: %v", err)
	}
	if other == got {
		t.Fatalf("two versions share the binary ID %q", got)
	}
	if _, otherSum, _ := strings.Cut(other, "+"); otherSum != sum {
		t.Fatalf("the executable hash changed between calls: %q vs %q", sum, otherSum)
	}
}
