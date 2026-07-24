// file: internal/itunes/itl_identity_refresh_test.go
// version: 1.0.0
// guid: 9d3f1a82-4c60-4b95-8e17-2f6c5b0e1d74
// last-edited: 2026-07-24

package itunes

import (
	"path/filepath"
	"testing"
)

// writeRefreshFixtureITL writes a small encrypted .itl with the given LibraryPID and
// returns its path + its true identity (for assertions).
func writeRefreshFixtureITL(t *testing.T, pid [8]byte) (string, *LibraryIdentity) {
	t.Helper()
	payload := buildCleanPayload()
	hdr := headerWithLibraryPID(payload, pid)
	path := filepath.Join(t.TempDir(), "iTunes Library.itl")
	if err := writeITLFileRaw(path, hdr, payload, false); err != nil {
		t.Fatalf("writeITLFileRaw: %v", err)
	}
	id, err := ComputeLibraryIdentity(payload, hdr)
	if err != nil {
		t.Fatalf("ComputeLibraryIdentity: %v", err)
	}
	return path, id
}

func TestRefreshLibraryIdentity_Happy(t *testing.T) {
	path, trueID := writeRefreshFixtureITL(t, fxLibraryPID)
	// Bootstrap a matching sidecar (as AdoptLibraryIdentity would), but with a
	// deliberately stale sample so we can prove the refresh re-derives it.
	stale := *trueID
	stale.PIDSample = []string{"deadbeefdeadbeef"} // wrong sample
	stale.FileSHA256 = "priorwritehash"
	if err := SaveLibraryIdentity(path, &stale); err != nil {
		t.Fatalf("SaveLibraryIdentity: %v", err)
	}

	fresh, res, err := RefreshLibraryIdentity(path, RefreshOptions{})
	if err != nil {
		t.Fatalf("RefreshLibraryIdentity: %v", err)
	}
	if fresh.LibraryPID != fxLibraryPIDHex {
		t.Errorf("LibraryPID = %q, want pinned %q", fresh.LibraryPID, fxLibraryPIDHex)
	}
	if fresh.TrackCount != trueID.TrackCount {
		t.Errorf("TrackCount = %d, want %d", fresh.TrackCount, trueID.TrackCount)
	}
	if len(fresh.PIDSample) != len(trueID.PIDSample) || (len(fresh.PIDSample) > 0 && fresh.PIDSample[0] == "deadbeefdeadbeef") {
		t.Errorf("PIDSample not re-derived: got %v", fresh.PIDSample)
	}
	if fresh.FileSHA256 != "priorwritehash" {
		t.Errorf("FileSHA256 anchor should carry forward, got %q", fresh.FileSHA256)
	}
	// Persisted to disk.
	reloaded, err := LoadLibraryIdentity(path)
	if err != nil || reloaded == nil {
		t.Fatalf("reload sidecar: %v", err)
	}
	if reloaded.TrackCount != trueID.TrackCount {
		t.Errorf("persisted TrackCount = %d, want %d", reloaded.TrackCount, trueID.TrackCount)
	}
	if res.DriftPct != 0 {
		t.Errorf("DriftPct = %d, want 0 (same library)", res.DriftPct)
	}
}

func TestRefreshLibraryIdentity_NoSidecar(t *testing.T) {
	path, _ := writeRefreshFixtureITL(t, fxLibraryPID)
	if _, _, err := RefreshLibraryIdentity(path, RefreshOptions{}); err == nil {
		t.Fatal("expected error when no sidecar exists (must Adopt first)")
	}
}

func TestRefreshLibraryIdentity_PIDChanged(t *testing.T) {
	path, trueID := writeRefreshFixtureITL(t, fxLibraryPID)
	// Sidecar claims a DIFFERENT LibraryPID than the file on disk → reseed.
	swapped := *trueID
	swapped.LibraryPID = "0000000000000000"
	if err := SaveLibraryIdentity(path, &swapped); err != nil {
		t.Fatalf("SaveLibraryIdentity: %v", err)
	}
	if _, _, err := RefreshLibraryIdentity(path, RefreshOptions{}); err == nil {
		t.Fatal("expected error when LibraryPID changed (reseed → must Adopt)")
	}
}

func TestRefreshLibraryIdentity_DriftOverCeiling(t *testing.T) {
	path, trueID := writeRefreshFixtureITL(t, fxLibraryPID)
	// Sidecar claims a huge track count; the real library is tiny → drift ceiling.
	inflated := *trueID
	inflated.TrackCount = trueID.TrackCount + 10_000
	if err := SaveLibraryIdentity(path, &inflated); err != nil {
		t.Fatalf("SaveLibraryIdentity: %v", err)
	}
	if _, _, err := RefreshLibraryIdentity(path, RefreshOptions{MaxDriftPct: 25}); err == nil {
		t.Fatal("expected drift-ceiling error (mass deletion / reseed)")
	}
}

func TestPartitionedTrackCount(t *testing.T) {
	path, trueID := writeRefreshFixtureITL(t, fxLibraryPID)
	ab, nonAB, err := PartitionedTrackCount(path)
	if err != nil {
		t.Fatalf("PartitionedTrackCount: %v", err)
	}
	if ab+nonAB != trueID.TrackCount {
		t.Errorf("partition %d+%d != total %d", ab, nonAB, trueID.TrackCount)
	}
}

func TestDriftPct(t *testing.T) {
	cases := []struct{ prev, cur, want int }{
		{100, 100, 0},
		{100, 125, 25},
		{100, 75, 25},
		{0, 5, 100},
		{0, 0, 0},
		{200, 300, 50},
	}
	for _, c := range cases {
		if got := driftPct(c.prev, c.cur); got != c.want {
			t.Errorf("driftPct(%d,%d) = %d, want %d", c.prev, c.cur, got, c.want)
		}
	}
}
