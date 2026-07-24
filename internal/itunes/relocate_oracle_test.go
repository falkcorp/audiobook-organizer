// file: internal/itunes/relocate_oracle_test.go
// version: 1.0.0
// guid: 2b8f1c93-7a64-4d50-9e18-3f6c5a0e2d71
// last-edited: 2026-07-24

package itunes

import (
	"os"
	"strings"
	"testing"
)

// anyPID returns one lower-hex track PID from a decompressed payload.
func anyPID(t *testing.T, payload []byte) string {
	t.Helper()
	blocks, err := splitMithBlocksByPID(payload)
	if err != nil {
		t.Fatalf("splitMithBlocksByPID: %v", err)
	}
	for pid := range blocks {
		return pid
	}
	t.Fatal("no tracks in payload")
	return ""
}

func TestVerifyRelocateWrite_HappyRelocate(t *testing.T) {
	before := buildCleanPayload()
	pid := anyPID(t, before)

	after, n := UpdateMetadataLE(before, []ITLMetadataUpdate{{PersistentID: pid, Location: `W:\PROOF\moved.mp3`}})
	if n != 1 {
		t.Fatalf("relocate applied %d, want 1", n)
	}

	v, err := VerifyRelocateWrite(before, after, map[string]bool{pid: true})
	if err != nil {
		t.Fatalf("VerifyRelocateWrite: %v", err)
	}
	if !v.OK {
		t.Fatalf("expected OK, got violations: %+v", v.Violations)
	}
	if v.RelocatedVerified != 1 || v.LocationChanged != 1 {
		t.Errorf("RelocatedVerified=%d LocationChanged=%d, want 1/1", v.RelocatedVerified, v.LocationChanged)
	}
	if v.TracksBefore != v.TracksAfter {
		t.Errorf("track count changed %d->%d on a relocate", v.TracksBefore, v.TracksAfter)
	}
	if v.UnchangedVerified != v.TracksBefore-1 {
		t.Errorf("UnchangedVerified=%d, want %d", v.UnchangedVerified, v.TracksBefore-1)
	}
}

func TestVerifyRelocateWrite_UnexpectedChange(t *testing.T) {
	before := buildCleanPayload()
	pid := anyPID(t, before)

	// Relocate a track but DON'T declare it in the plan — the oracle must flag it.
	after, n := UpdateMetadataLE(before, []ITLMetadataUpdate{{PersistentID: pid, Location: `W:\PROOF\x.mp3`}})
	if n != 1 {
		t.Fatalf("relocate applied %d, want 1", n)
	}
	v, err := VerifyRelocateWrite(before, after, map[string]bool{}) // empty plan
	if err != nil {
		t.Fatalf("VerifyRelocateWrite: %v", err)
	}
	if v.OK {
		t.Fatal("expected a violation for an undeclared change")
	}
	found := false
	for _, viol := range v.Violations {
		if viol.PID == pid && viol.Kind == ViolationUnexpected {
			found = true
		}
	}
	if !found {
		t.Errorf("expected unexpected-change on %s, got %+v", pid, v.Violations)
	}
}

func TestVerifyRelocateWrite_NonLocationMutation(t *testing.T) {
	before := buildCleanPayload()
	pid := anyPID(t, before)

	// Change the track NAME (a non-location field) but declare it as a relocate —
	// the oracle must reject because a relocate may touch ONLY the location pair.
	after, n := UpdateMetadataLE(before, []ITLMetadataUpdate{{PersistentID: pid, Name: "Tampered Title"}})
	if n != 1 {
		t.Fatalf("metadata update applied %d, want 1", n)
	}
	v, err := VerifyRelocateWrite(before, after, map[string]bool{pid: true})
	if err != nil {
		t.Fatalf("VerifyRelocateWrite: %v", err)
	}
	if v.OK {
		t.Fatal("expected non-location-mutated violation")
	}
	found := false
	for _, viol := range v.Violations {
		if viol.PID == pid && viol.Kind == ViolationNonLocationMut {
			found = true
		}
	}
	if !found {
		t.Errorf("expected non-location-mutated on %s, got %+v", pid, v.Violations)
	}
}

func TestVerifyRelocateWrite_TrackRemoved(t *testing.T) {
	before := buildCleanPayload()
	pid := anyPID(t, before)

	after, removed := RemoveTracksByPIDLE(before, map[string]bool{pid: true})
	if removed != 1 {
		t.Fatalf("removed %d, want 1", removed)
	}
	v, err := VerifyRelocateWrite(before, after, map[string]bool{})
	if err != nil {
		t.Fatalf("VerifyRelocateWrite: %v", err)
	}
	if v.OK {
		t.Fatal("expected track-removed violation (relocate removes nothing)")
	}
	found := false
	for _, viol := range v.Violations {
		if viol.PID == pid && viol.Kind == ViolationRemoved {
			found = true
		}
	}
	if !found {
		t.Errorf("expected track-removed on %s, got %+v", pid, v.Violations)
	}
}

func TestVerifyRelocateWrite_Idempotent(t *testing.T) {
	before := buildCleanPayload()
	v, err := VerifyRelocateWrite(before, before, map[string]bool{})
	if err != nil {
		t.Fatalf("VerifyRelocateWrite: %v", err)
	}
	if !v.OK || v.UnchangedVerified != v.TracksBefore {
		t.Errorf("identical payloads must verify clean: OK=%v unchanged=%d/%d %+v",
			v.OK, v.UnchangedVerified, v.TracksBefore, v.Violations)
	}
}

// TestVerifyRelocateWrite_RealLibrary runs the oracle on a real relocate of a copy
// of the real .itl (ITL_PRESERVE_PROOF_PATH; skips in CI): a genuine 300-track
// relocate must verify clean, and a single tampered byte in an untouched track must
// be caught.
func TestVerifyRelocateWrite_RealLibrary(t *testing.T) {
	path := os.Getenv("ITL_PRESERVE_PROOF_PATH")
	if path == "" {
		t.Skip("set ITL_PRESERVE_PROOF_PATH to a COPY of a real .itl to run the real-library oracle test")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	before, err := DecryptAndInflateITL(raw)
	if err != nil {
		t.Fatalf("decrypt/inflate: %v", err)
	}
	lib, err := ParseITL(path)
	if err != nil {
		t.Fatalf("ParseITL: %v", err)
	}

	var updates []ITLMetadataUpdate
	plan := map[string]bool{}
	var victim string // an untouched PID, for the tamper check
	for i := range lib.Tracks {
		tr := &lib.Tracks[i]
		p := strings.ToLower(pidToHex(tr.PersistentID))
		if len(updates) < 300 {
			if !strings.HasPrefix(tr.Location, `W:\`) {
				continue
			}
			updates = append(updates, ITLMetadataUpdate{PersistentID: p, Location: `W:\PROOF\` + tr.Location[3:]})
			plan[p] = true
		} else if victim == "" && !plan[p] {
			victim = p
		}
	}
	after, n := UpdateMetadataLE(before, updates)
	if n != len(updates) {
		t.Fatalf("relocate applied %d, want %d", n, len(updates))
	}

	v, err := VerifyRelocateWrite(before, after, plan)
	if err != nil {
		t.Fatalf("VerifyRelocateWrite: %v", err)
	}
	if !v.OK {
		t.Fatalf("real relocate should verify clean, got %d violations e.g. %+v", len(v.Violations), firstN(v.Violations, 3))
	}
	t.Logf("REAL-LIBRARY ORACLE: relocated_verified=%d location_changed=%d unchanged_verified=%d (tracks %d)",
		v.RelocatedVerified, v.LocationChanged, v.UnchangedVerified, v.TracksBefore)

	// Tamper an UNTOUCHED track (change its Name) and confirm the oracle catches it
	// as an unexpected change — the auto-rollback trigger.
	if victim == "" {
		t.Skip("no untouched track available to tamper")
	}
	tamperedAfter, tn := UpdateMetadataLE(after, []ITLMetadataUpdate{{PersistentID: victim, Name: "TAMPERED"}})
	if tn != 1 {
		t.Fatalf("tamper update applied %d, want 1", tn)
	}
	vt, err := VerifyRelocateWrite(before, tamperedAfter, plan)
	if err != nil {
		t.Fatalf("VerifyRelocateWrite(tampered): %v", err)
	}
	if vt.OK {
		t.Fatal("oracle failed to catch a tampered untouched track")
	}
	caught := false
	for _, viol := range vt.Violations {
		if viol.PID == victim && viol.Kind == ViolationUnexpected {
			caught = true
		}
	}
	if !caught {
		t.Errorf("expected unexpected-change on tampered %s, got %+v", victim, firstN(vt.Violations, 3))
	}
	t.Logf("oracle correctly caught tamper on untouched track %s", victim)
}

func firstN(v []OracleViolation, n int) []OracleViolation {
	if len(v) < n {
		return v
	}
	return v[:n]
}
