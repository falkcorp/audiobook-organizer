// file: internal/plugins/maintenance/integrity_check_test.go
// version: 1.0.0
// guid: 3a9d1e2f-4b5c-4d6e-8f7a-1b2c3d4e5f6a
// last-edited: 2026-07-01

package maintenance

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestFindIntegrityMismatches_ReportOnly verifies the core scan: given a mix
// of book_files with matching hashes, mismatched hashes with no AO write on
// record, mismatched hashes explained by an AO tag write, and rows with no
// baseline hash, only the true mismatches (no AO write) are flagged.
func TestFindIntegrityMismatches_ReportOnly(t *testing.T) {
	files := []database.BookFile{
		// (a) matching hashes — not flagged.
		{ID: "f1", FilePath: "/lib/a.m4b", FileHash: "hash-a", OriginalFileHash: "hash-a"},
		// (b) mismatch with no AO write on record — flagged.
		{ID: "f2", FilePath: "/lib/b.m4b", FileHash: "hash-b-new", OriginalFileHash: "hash-b-orig"},
		// (c) mismatch explained by an AO tag write — not flagged.
		{ID: "f3", FilePath: "/lib/c.m4b", FileHash: "hash-c-new", OriginalFileHash: "hash-c-orig", PostMetadataHash: "hash-c-new"},
		// (d) no baseline hash — not flagged.
		{ID: "f4", FilePath: "/lib/d.m4b", FileHash: "hash-d-new", OriginalFileHash: ""},
	}

	var writeCalls, deleteCalls []string
	store := &database.MockStore{
		GetAllBookFilesFunc: func() ([]database.BookFile, error) {
			return files, nil
		},
		DeleteBookFileFunc: func(id string) error {
			deleteCalls = append(deleteCalls, id)
			return nil
		},
	}

	flagged, totalFiles, err := findIntegrityMismatches(context.Background(), store)
	if err != nil {
		t.Fatalf("findIntegrityMismatches returned error: %v", err)
	}
	if totalFiles != len(files) {
		t.Errorf("totalFiles = %d, want %d", totalFiles, len(files))
	}
	if got, want := len(flagged), 1; got != want {
		t.Fatalf("len(flagged) = %d, want %d (flagged: %+v)", got, want, flagged)
	}
	if flagged[0].ID != "f2" {
		t.Errorf("flagged[0].ID = %q, want %q", flagged[0].ID, "f2")
	}

	if len(writeCalls) != 0 {
		t.Errorf("report-only scan triggered %d write calls: %v", len(writeCalls), writeCalls)
	}
	if len(deleteCalls) != 0 {
		t.Errorf("report-only scan called DeleteBookFile %d times: %v", len(deleteCalls), deleteCalls)
	}
}

// TestFindIntegrityMismatches_NoMismatches verifies the clean-library case
// returns an empty slice with no error.
func TestFindIntegrityMismatches_NoMismatches(t *testing.T) {
	files := []database.BookFile{
		{ID: "f1", FileHash: "h1", OriginalFileHash: "h1"},
		{ID: "f2", FileHash: "h2", OriginalFileHash: "h2"},
	}
	store := &database.MockStore{
		GetAllBookFilesFunc: func() ([]database.BookFile, error) { return files, nil },
	}
	flagged, _, err := findIntegrityMismatches(context.Background(), store)
	if err != nil {
		t.Fatalf("findIntegrityMismatches returned error: %v", err)
	}
	if len(flagged) != 0 {
		t.Errorf("expected no flagged rows, got %d", len(flagged))
	}
}
