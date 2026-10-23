// file: internal/dedup/collectors_acoustid_retired_test.go
// version: 1.0.0
// guid: 6d2b8f14-3c7a-4e91-a5d0-8b1f47e2c963
// last-edited: 2026-10-23

package dedup

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
)

// A retired row is a version that left the library, and its BookID is the live
// holder. An exact AcoustID hit through one must not pair the holder with the
// content already merged into it, even on a store that returns the retired row
// (the stub does not filter, as a store whose reader forgot to would not).
func TestCollectExactAcoustID_RetiredHitIsNotACandidate(t *testing.T) {
	queryFile := &database.BookFile{ID: "qfile1", BookID: "bookA", AcoustIDSeg0: validFP80}
	retired := &database.BookFile{ID: "cfile1", BookID: "holderH", Retired: true}
	store := &stubExactStore{m: map[string]*database.BookFile{validFP80: retired}}

	sigs, err := CollectExactAcoustID(store, queryFile, "bookA")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sigs) != 0 {
		t.Fatalf("a retired row must not produce a signal, got %+v", sigs)
	}

	// The same row, live, is the true positive: the test is not blind.
	live := *retired
	live.Retired = false
	store = &stubExactStore{m: map[string]*database.BookFile{validFP80: &live}}
	sigs, err = CollectExactAcoustID(store, queryFile, "bookA")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sigs) != 1 {
		t.Fatalf("control: a live row must produce one signal, got %d", len(sigs))
	}
}

// LSHProbe resolves an fpidx entry by file id and returns retired rows too, so
// the collector has to drop them itself.
func TestCollectLSHAcoustID_RetiredCandidateIsSkipped(t *testing.T) {
	queryFile := &database.BookFile{
		ID: "qfile1", BookID: "bookA",
		AcoustIDFingerprint: makeFP(1000), AcoustIDFPVersion: fingerprint.PrintEncodingVersion,
	}
	cand := &database.BookFile{
		ID: "cfile1", BookID: "holderH", Retired: true,
		AcoustIDFingerprint: makeFP(1000), AcoustIDFPVersion: fingerprint.PrintEncodingVersion,
	}
	store := &stubLSHStore{
		indexBuilt:  true,
		probeResult: map[string]int{"cfile1": 3},
		filesByID:   map[string]*database.BookFile{"cfile1": cand},
	}
	sigs, err := CollectLSHAcoustID(store, queryFile, "bookA", DefaultLSHAcoustIDConfig())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sigs) != 0 {
		t.Fatalf("a retired candidate must not produce a signal, got %+v", sigs)
	}

	cand.Retired = false
	sigs, err = CollectLSHAcoustID(store, queryFile, "bookA", DefaultLSHAcoustIDConfig())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sigs) != 1 {
		t.Fatalf("control: a live candidate must produce one signal, got %d", len(sigs))
	}
}
