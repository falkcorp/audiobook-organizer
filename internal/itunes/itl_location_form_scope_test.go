// file: internal/itunes/itl_location_form_scope_test.go
// version: 1.1.0
// guid: 1f8c3a72-5b94-4d60-8e17-2c6b5a0e9d43
// last-edited: 2026-07-24

package itunes

import (
	"os"
	"strings"
	"testing"
)

// TestStagingMarkerIsLeak covers the F7 decision: the ".itunes-writeback/" staging
// marker is a leak by default (strict), NOT a leak when the location sits under the
// configured AllowedWritebackRoot (the AO library's own media root), and STILL a leak
// when it does not match the configured root (fail-closed). The location value is
// tested directly (0x0B URL and 0x0D WinPath forms) because the canonicalizing write
// path scrubs ".itunes-writeback/" and so cannot construct a synthetic marker — the
// real markers exist only in libraries written before the pathnorm fix (see the
// env-gated real-library test below).
func TestStagingMarkerIsLeak(t *testing.T) {
	const aoURL = `file://localhost/W:/audiobook-organizer/.itunes-writeback/iTunes%20Media/Audiobooks/x.m4b`
	const aoWin = `W:\audiobook-organizer\.itunes-writeback\iTunes Media\Audiobooks\x.m4b`
	const leakURL = `file://localhost/W:/itunes/.itunes-writeback/iTunes%20Media/leak.m4b`

	aoRoot := normalizeStagingPath(`audiobook-organizer/.itunes-writeback/`)
	winRoot := normalizeStagingPath(`audiobook-organizer\.itunes-writeback\`) // backslash form normalizes the same

	cases := []struct {
		name string
		loc  string
		root string
		want bool // want leak?
	}{
		{"strict rejects AO url", aoURL, "", true},
		{"strict rejects AO winpath", aoWin, "", true},
		{"scoped allows AO url", aoURL, aoRoot, false},
		{"scoped allows AO winpath (backslash form)", aoWin, aoRoot, false},
		{"scoped allows AO url via backslash root", aoURL, winRoot, false},
		{"scoped still rejects a leak under a different root", leakURL, aoRoot, true},
		{"wrong root rejects the AO path (misconfig → fail-closed)", aoURL, normalizeStagingPath(`some/other/lib/.itunes-writeback/`), true},
	}
	for _, c := range cases {
		if got := stagingMarkerIsLeak(c.loc, c.root); got != c.want {
			t.Errorf("%s: stagingMarkerIsLeak=%v, want %v", c.name, got, c.want)
		}
	}
}

// TestLocationFormScope_RealLibrary confirms on a copy of the real .itl (env-gated)
// that the strict guard flags the whole library's .itunes-writeback/ paths (the F7
// repro) while the AO-scoped guard accepts them.
func TestLocationFormScope_RealLibrary(t *testing.T) {
	path := os.Getenv("ITL_PRESERVE_PROOF_PATH")
	if path == "" {
		t.Skip("set ITL_PRESERVE_PROOF_PATH to a COPY of a real .itl")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	payload, err := DecryptAndInflateITL(raw)
	if err != nil {
		t.Fatalf("decrypt/inflate: %v", err)
	}

	countStaging := func(res GuardResult) int {
		n := 0
		for _, v := range res.Violations {
			if strings.Contains(v.Message, ".itunes-writeback/") {
				n++
			}
		}
		return n
	}

	strict := countStaging(guardLocationForm(nil, payload, nil, ContractConfig{}))
	if strict == 0 {
		t.Fatal("expected the strict guard to flag the real library's .itunes-writeback/ paths (F7 repro)")
	}
	scoped := countStaging(guardLocationForm(nil, payload, nil, ContractConfig{AllowedWritebackRoot: `audiobook-organizer/.itunes-writeback/`}))
	t.Logf("F7 FIX on real library: strict staging violations=%d, AO-scoped staging violations=%d", strict, scoped)
	if scoped != 0 {
		t.Errorf("AO-scoped guard must accept the library's own .itunes-writeback/ media root, still got %d violations", scoped)
	}
}
