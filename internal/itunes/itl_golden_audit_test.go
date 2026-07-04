// file: internal/itunes/itl_golden_audit_test.go
// version: 1.0.0
// guid: 5a7e9c1d-3b2f-4e6a-8d0c-9f1b2a3c4d5f
// last-edited: 2026-07-03
//
// Golden-corpus audit regression (SPEC 3 §6 / K16 acceptance).
//
// WHY: a guard that fires on known-good data is worse than no guard — it
// trains operators to ignore red. Before the K16 fix, location-form reported
// ~2× tracks-with-location violations on EVERY iTunes-authored library
// (including the golden master) because the mhoh string decoder had the
// at24 1/3 semantics swapped, so the guard was auditing mojibake. This test
// pins the acceptance criterion: the checked-in iTunes-authored testdata
// library must pass every guard with zero violations. Any future guard or
// decoder change that regresses on real iTunes output fails the build here.

package itunes

import (
	"os"
	"testing"
)

const goldenTestdataITL = "../../testdata/itunes/iTunes Library.itl"

func TestGoldenCorpusAuditClean(t *testing.T) {
	raw, err := os.ReadFile(goldenTestdataITL)
	if err != nil {
		t.Skipf("golden testdata library unavailable: %v", err)
	}

	verdict := AuditITL(raw)
	for _, r := range verdict.Results {
		if r.Pass() {
			continue
		}
		max := len(r.Violations)
		if max > 5 {
			max = 5
		}
		for _, v := range r.Violations[:max] {
			t.Errorf("guard %s: [%d/%s] %s", r.Guard, v.Offset, v.Chunk, v.Message)
		}
		if len(r.Violations) > max {
			t.Errorf("guard %s: ...and %d more violations", r.Guard, len(r.Violations)-max)
		}
	}
	if !verdict.Pass {
		t.Fatalf("iTunes-authored golden library must pass every guard; failed: %v", verdict.FailedGuards())
	}

	// The decoder must also produce sane strings: every 0x0D Location in an
	// iTunes-authored library is a Windows absolute path, so a decode that
	// yields mojibake (the K16 signature) fails the drive-letter shape check.
	_, payload, err := decodeITLForContract(raw)
	if err != nil {
		t.Fatal(err)
	}
	checked, bad := 0, 0
	forEachTrackLocations(payload, func(_ int, loc0D, _ string, has0D, _ bool) {
		if !has0D {
			return
		}
		checked++
		if len(loc0D) < 3 || loc0D[1] != ':' || loc0D[2] != '\\' {
			bad++
		}
	})
	if checked == 0 {
		t.Fatal("no 0x0D locations decoded — walker regression")
	}
	if bad > 0 {
		t.Fatalf("%d of %d decoded Locations are not drive-letter Windows paths (K16 decode regression)", bad, checked)
	}
}
