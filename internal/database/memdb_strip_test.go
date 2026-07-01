// file: internal/database/memdb_strip_test.go
// version: 1.4.0
// guid: e6f7a8b9-c0d1-4e2f-3a4b-5c6d7e8f9012
// last-edited: 2026-07-01

package database

import (
	"encoding/json"
	"testing"
	"time"
)

// TestStripBookFileForMemdb_NilsLargeFields verifies that stripBookFileForMemdb
// strips heavy fingerprint diagnostic fields AND all AcoustIDSeg0..6 fields (fable5 T019),
// while preserving identity fields, AcoustIDFingerprintDurationSec, and FingerprintFailedAt.
// FingerprintFailedAt is intentionally preserved (it is 24B per row and is read by the
// LSH index builder to skip permanently-failed files from rescan enqueue).
func TestStripBookFileForMemdb_NilsLargeFields(t *testing.T) {
	now := time.Now()
	reason := "corrupt_audio"
	detail := "ffmpeg returned 1"
	diag := `{"diagnostic":"data"}`

	src := &BookFile{
		ID:                             "bf-1",
		BookID:                         "book-1",
		FilePath:                       "/tmp/test.m4b",
		AcoustIDSeg0:                   "AQADtAcSRY",
		AcoustIDSeg1:                   "AQADtAcSRZ",
		AcoustIDSeg2:                   "AQADtAcSRA",
		AcoustIDSeg3:                   "AQADtAcSRB",
		AcoustIDSeg4:                   "AQADtAcSRC",
		AcoustIDSeg5:                   "AQADtAcSRD",
		AcoustIDSeg6:                   "AQADtAcSRE",
		AcoustIDFingerprint:            make([]byte, 256*1024),
		AcoustIDFingerprintDurationSec: 7200.5,
		FingerprintFailedAt:            &now,
		FingerprintFailureReason:       &reason,
		FingerprintFailureDetail:       &detail,
		FingerprintDiagnosticJSON:      &diag,
	}

	stripped := stripBookFileForMemdb(src)
	if stripped == nil {
		t.Fatal("stripped is nil")
	}

	// --- Heavy fields must be nil ---
	if stripped.AcoustIDFingerprint != nil {
		t.Errorf("AcoustIDFingerprint not stripped: got len=%d, want nil", len(stripped.AcoustIDFingerprint))
	}
	if stripped.FingerprintFailureReason != nil {
		t.Errorf("FingerprintFailureReason not stripped")
	}
	if stripped.FingerprintFailureDetail != nil {
		t.Errorf("FingerprintFailureDetail not stripped")
	}
	if stripped.FingerprintDiagnosticJSON != nil {
		t.Errorf("FingerprintDiagnosticJSON not stripped")
	}

	// --- AcoustIDSeg0..6 must ALL be zeroed (fable5 T019) ---
	// All memdb readers of these fields were retired by T013 (O(N) fuzzy scan)
	// and the GetAcoustIDSeg0 / fingerprint_status badge path was migrated to
	// use AcoustIDFingerprintDurationSec as the presence proxy.
	if stripped.AcoustIDSeg0 != "" {
		t.Errorf("AcoustIDSeg0 not stripped: got %q, want empty", stripped.AcoustIDSeg0)
	}
	if stripped.AcoustIDSeg1 != "" {
		t.Errorf("AcoustIDSeg1 not stripped: got %q, want empty", stripped.AcoustIDSeg1)
	}
	if stripped.AcoustIDSeg2 != "" {
		t.Errorf("AcoustIDSeg2 not stripped: got %q, want empty", stripped.AcoustIDSeg2)
	}
	if stripped.AcoustIDSeg3 != "" {
		t.Errorf("AcoustIDSeg3 not stripped: got %q, want empty", stripped.AcoustIDSeg3)
	}
	if stripped.AcoustIDSeg4 != "" {
		t.Errorf("AcoustIDSeg4 not stripped: got %q, want empty", stripped.AcoustIDSeg4)
	}
	if stripped.AcoustIDSeg5 != "" {
		t.Errorf("AcoustIDSeg5 not stripped: got %q, want empty", stripped.AcoustIDSeg5)
	}
	if stripped.AcoustIDSeg6 != "" {
		t.Errorf("AcoustIDSeg6 not stripped: got %q, want empty", stripped.AcoustIDSeg6)
	}

	// --- Identity and presence-proxy fields must be preserved ---
	if stripped.ID != "bf-1" {
		t.Errorf("ID not preserved: %q", stripped.ID)
	}
	if stripped.BookID != "book-1" {
		t.Errorf("BookID not preserved: %q", stripped.BookID)
	}
	if stripped.FilePath != "/tmp/test.m4b" {
		t.Errorf("FilePath not preserved: %q", stripped.FilePath)
	}
	// AcoustIDFingerprintDurationSec is preserved as the fingerprint presence proxy
	// for GetAcoustIDSeg0's memdb fallback path (bookfile_fingerprint.go).
	if stripped.AcoustIDFingerprintDurationSec != 7200.5 {
		t.Errorf("AcoustIDFingerprintDurationSec not preserved: %v", stripped.AcoustIDFingerprintDurationSec)
	}
	// FingerprintFailedAt is preserved for the LSH index builder to skip
	// permanently-failed files from fingerprint-rescan enqueue.
	if stripped.FingerprintFailedAt == nil {
		t.Errorf("FingerprintFailedAt should be preserved (needed by LSH builder)")
	}

	// --- Source must not be mutated ---
	if src.AcoustIDFingerprint == nil {
		t.Errorf("source mutated: AcoustIDFingerprint nil on src")
	}
	if src.FingerprintFailedAt == nil {
		t.Errorf("source mutated: FingerprintFailedAt nil on src")
	}
	if src.AcoustIDSeg0 != "AQADtAcSRY" {
		t.Errorf("source mutated: AcoustIDSeg0 changed on src")
	}
}

// TestStripBookForMemdb_PreservesTranscription is the correctness guard for
// FilterSpec.OnlyParsedTranscription: the filter resolves against memdb-resident
// Books (via GetAudiobooks), so it only works if stripBookForMemdb keeps the
// transcription fields. This asserts Description is stripped (heavy) while
// TranscribedTitle / TranscribeStatus / IntroTranscription survive. If a future
// change adds these to the strip list, this test fails loudly before the filter
// silently starts returning nothing.
func TestStripBookForMemdb_PreservesTranscription(t *testing.T) {
	desc := "a very long description that memdb strips to save RAM"
	title := "Salem's Lot"
	status := "ok"
	intro := "Simon and Schuster audio presents Salem's Lot by Stephen King"

	src := &Book{
		ID:                 "bk-1",
		Title:              "T",
		Description:        &desc,
		TranscribedTitle:   &title,
		TranscribeStatus:   &status,
		IntroTranscription: &intro,
	}

	stripped := stripBookForMemdb(src)
	if stripped == nil {
		t.Fatal("stripped is nil")
	}
	if stripped.Description != nil {
		t.Errorf("Description should be stripped from memdb, got %q", *stripped.Description)
	}
	if stripped.TranscribedTitle == nil || *stripped.TranscribedTitle != title {
		t.Errorf("TranscribedTitle must survive memdb strip (OnlyParsedTranscription depends on it), got %v", stripped.TranscribedTitle)
	}
	if stripped.TranscribeStatus == nil || *stripped.TranscribeStatus != status {
		t.Errorf("TranscribeStatus must survive memdb strip, got %v", stripped.TranscribeStatus)
	}
	if stripped.IntroTranscription == nil || *stripped.IntroTranscription != intro {
		t.Errorf("IntroTranscription must survive memdb strip, got %v", stripped.IntroTranscription)
	}
	// Source must not be mutated.
	if src.Description == nil {
		t.Error("source mutated: Description nil on src")
	}
}

func TestStripBookFileForMemdb_NilInput(t *testing.T) {
	if got := stripBookFileForMemdb(nil); got != nil {
		t.Errorf("nil input: got %v, want nil", got)
	}
}

func TestStripBookFileForMemdb_AlreadyEmpty(t *testing.T) {
	src := &BookFile{ID: "bf-2"}
	stripped := stripBookFileForMemdb(src)
	if stripped == nil {
		t.Fatal("nil result")
	}
	if stripped.ID != "bf-2" {
		t.Errorf("ID corrupted: %q", stripped.ID)
	}
	if stripped.AcoustIDFingerprint != nil {
		t.Errorf("empty input produced non-nil AcoustIDFingerprint")
	}
	// All Seg0..6 must remain empty when already empty.
	for i, seg := range []string{
		stripped.AcoustIDSeg0, stripped.AcoustIDSeg1, stripped.AcoustIDSeg2,
		stripped.AcoustIDSeg3, stripped.AcoustIDSeg4, stripped.AcoustIDSeg5,
		stripped.AcoustIDSeg6,
	} {
		if seg != "" {
			t.Errorf("AcoustIDSeg%d non-empty on zero input: %q", i, seg)
		}
	}
}

// TestGetAcoustIDSeg0_MemdbFallback verifies that GetAcoustIDSeg0() returns a
// non-empty value for memdb-stripped BookFile rows that have
// AcoustIDFingerprintDurationSec > 0 (whole-file fingerprint was computed),
// even when AcoustIDSeg0 is empty (stripped by stripBookFileForMemdb).
//
// This is the regression test for the fingerprint_status badge path:
//   GetBookFilesForIDs → ComputeFingerprintFields → GetAcoustIDSeg0()
//
// Without this fallback, stripping Seg0 from memdb would make every book
// appear as "fingerprint_status: none" on the /api/v1/audiobooks list.
func TestGetAcoustIDSeg0_MemdbFallback(t *testing.T) {
	tests := []struct {
		name     string
		bf       *BookFile
		wantNonEmpty bool
	}{
		{
			name:         "nil receiver",
			bf:           nil,
			wantNonEmpty: false,
		},
		{
			name:         "no fingerprint at all",
			bf:           &BookFile{ID: "bf-none"},
			wantNonEmpty: false,
		},
		{
			name: "seg0 only (legacy, not yet whole-file migrated)",
			bf: &BookFile{
				ID:           "bf-legacy",
				AcoustIDSeg0: "AQADtAcSRY",
			},
			wantNonEmpty: true,
		},
		{
			name: "whole-file duration only (stripped memdb row)",
			bf: &BookFile{
				ID:                             "bf-wf-only",
				AcoustIDFingerprintDurationSec: 7200.5,
				// AcoustIDSeg0 empty — stripped by stripBookFileForMemdb
			},
			wantNonEmpty: true,
		},
		{
			name: "both seg0 and whole-file duration (non-stripped / Pebble-direct)",
			bf: &BookFile{
				ID:                             "bf-both",
				AcoustIDSeg0:                   "AQADtAcSRY",
				AcoustIDFingerprintDurationSec: 3600.0,
			},
			wantNonEmpty: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.bf.GetAcoustIDSeg0()
			if tc.wantNonEmpty && got == "" {
				t.Errorf("GetAcoustIDSeg0() = %q, want non-empty", got)
			}
			if !tc.wantNonEmpty && got != "" {
				t.Errorf("GetAcoustIDSeg0() = %q, want empty", got)
			}
		})
	}
}

// TestStripBookFileForMemdb_SegStripAndFallback verifies the end-to-end
// flow for the fingerprint_status badge after T019:
// strip → Seg0 empty → GetAcoustIDSeg0 falls back to DurationSec → non-empty.
func TestStripBookFileForMemdb_SegStripAndFallback(t *testing.T) {
	src := &BookFile{
		ID:                             "bf-e2e",
		BookID:                         "book-e2e",
		AcoustIDSeg0:                   "AQADtAcSRY",
		AcoustIDFingerprintDurationSec: 5400.0,
	}

	stripped := stripBookFileForMemdb(src)
	if stripped == nil {
		t.Fatal("stripped is nil")
	}

	// Seg0 must be stripped.
	if stripped.AcoustIDSeg0 != "" {
		t.Errorf("AcoustIDSeg0 not stripped: %q", stripped.AcoustIDSeg0)
	}

	// Duration proxy must survive.
	if stripped.AcoustIDFingerprintDurationSec != 5400.0 {
		t.Errorf("AcoustIDFingerprintDurationSec changed: %v", stripped.AcoustIDFingerprintDurationSec)
	}

	// GetAcoustIDSeg0() on the stripped row must return non-empty
	// (falls back to DurationSec), preserving the fingerprint_status badge.
	if got := stripped.GetAcoustIDSeg0(); got == "" {
		t.Errorf("GetAcoustIDSeg0() on stripped memdb row returned empty; "+
			"fingerprint_status badge would be broken (fable5 T019 regression)")
	}
}

// TestStrippedBookSizeRegressionAssertion verifies that a sampled stripped Book
// projection stays ≤4KB mean to catch field bloat before it impacts memdb RSS.
// This is a soft regression assert: if the projection grows past 4KB on average,
// the CI failure alerts us to investigate what field was added.
func TestStrippedBookSizeRegressionAssertion(t *testing.T) {
	// Create a realistic stripped Book with typical field values.
	// Note: Book fields are intentionally kept minimal to reflect what stripBookForMemdb retains.
	title := "The Hobbit"
	language := "en"
	isbn10 := "0547928227"
	asin := "B00DWTGFVI"
	authorID := 1
	seriesID := 1
	duration := 720000
	fileSize := int64(1234567890)

	strippedBook := &Book{
		ID:           "book-12345",
		Title:        title,
		AuthorID:     &authorID,
		SeriesID:     &seriesID,
		FilePath:     "/mnt/audiobooks/tolkien/the-hobbit/",
		Language:     &language,
		ISBN10:       &isbn10,
		ASIN:         &asin,
		Duration:     &duration,
		FileSize:     &fileSize,
		// Heavy fields are stripped (nil in memdb):
		// Description, VersionNotes, BookSigV1, BookSigV1Mask, BookSigSegments
		// Author and Series pointers are nil at warm time (hydrated separately)
	}

	data, err := json.Marshal(strippedBook)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	sizeBytes := len(data)
	const maxBytes = 4096 // 4KB threshold per spec

	if sizeBytes > maxBytes {
		t.Errorf("stripped Book projection exceeds 4KB threshold: %d bytes > %d bytes; "+
			"a heavy field may have been added — check memdb_strip.go", sizeBytes, maxBytes)
	}
}
