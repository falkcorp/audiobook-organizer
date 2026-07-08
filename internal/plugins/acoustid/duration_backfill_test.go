// file: internal/plugins/acoustid/duration_backfill_test.go
// version: 1.0.0
// guid: f6a7b8c9-d0e1-4f2a-9b3c-4d5e6f7a8b9c
// last-edited: 2026-07-07

package acoustid

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestDurationBackfill_DryRunDoesNotWrite verifies that omitting "live" (or
// explicitly passing live=false) never calls UpdateBookFile — the op only
// reports the affected count and a sample of paths.
func TestDurationBackfill_DryRunDoesNotWrite(t *testing.T) {
	files := []database.BookFile{
		{ID: "f1", BookID: "b1", FilePath: "/lib/a.m4b", AcoustIDFingerprint: []byte{1, 2}},
		{ID: "f2", BookID: "b2", FilePath: "/lib/b.m4b", AcoustIDFingerprint: []byte{3, 4}},
	}

	scanCalled := false
	updateCalled := false
	store := &database.MockStore{
		GetFilesWithZeroDurationFingerprintFunc: func(limit, offset int) ([]database.BookFile, int64, error) {
			scanCalled = true
			return files, int64(len(files)), nil
		},
		UpdateBookFileFunc: func(id string, _ *database.BookFile) error {
			updateCalled = true
			return nil
		},
	}

	p := &Plugin{store: store}
	r := &lshTestReporter{}

	if err := p.runDurationBackfill(context.Background(), nil, r); err != nil {
		t.Fatalf("runDurationBackfill returned error: %v", err)
	}
	if !scanCalled {
		t.Error("expected GetFilesWithZeroDurationFingerprint to be called")
	}
	if updateCalled {
		t.Error("dry run must never call UpdateBookFile")
	}
}

// TestDurationBackfill_NoAffectedFiles verifies the zero-rows path is a
// clean no-op (matches the "no work to do" shape of the sibling ops).
func TestDurationBackfill_NoAffectedFiles(t *testing.T) {
	updateCalled := false
	store := &database.MockStore{
		GetFilesWithZeroDurationFingerprintFunc: func(limit, offset int) ([]database.BookFile, int64, error) {
			return nil, 0, nil
		},
		UpdateBookFileFunc: func(id string, _ *database.BookFile) error {
			updateCalled = true
			return nil
		},
	}

	p := &Plugin{store: store}
	r := &lshTestReporter{}

	if err := p.runDurationBackfill(context.Background(), nil, r); err != nil {
		t.Fatalf("runDurationBackfill returned error: %v", err)
	}
	if updateCalled {
		t.Error("no affected files means UpdateBookFile must never be called")
	}
}

// TestDurationBackfill_LiveRunSkipsMissingFiles verifies live=true actually
// attempts a fix, and that files whose path no longer exists on disk are
// counted as ineligible (via fingerprintEligibility's file_not_found guard)
// rather than erroring the whole op out.
func TestDurationBackfill_LiveRunSkipsMissingFiles(t *testing.T) {
	files := []database.BookFile{
		{ID: "f1", BookID: "b1", FilePath: "/does/not/exist/a.m4b", AcoustIDFingerprint: []byte{1, 2}},
		{ID: "f2", BookID: "b2", FilePath: "/does/not/exist/b.m4b", AcoustIDFingerprint: []byte{3, 4}},
	}

	store := &database.MockStore{
		GetFilesWithZeroDurationFingerprintFunc: func(limit, offset int) ([]database.BookFile, int64, error) {
			return files, int64(len(files)), nil
		},
	}

	p := &Plugin{store: store}
	r := &lshTestReporter{}

	params, err := json.Marshal(DurationBackfillParams{Live: true})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}

	if err := p.runDurationBackfill(context.Background(), params, r); err != nil {
		t.Fatalf("runDurationBackfill returned error: %v", err)
	}

	if len(r.frames) < 2 {
		t.Fatalf("expected at least 2 progress frames, got %d", len(r.frames))
	}
	last := r.frames[len(r.frames)-1]
	if last.total != 2 || last.current != 2 {
		t.Errorf("expected final frame 2/2, got %d/%d", last.current, last.total)
	}
}
