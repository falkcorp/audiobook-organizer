// file: internal/plugins/acoustid/preview_behaviour_test.go
// version: 1.0.0
// guid: 6ed53ee5-4d03-443a-b99b-f92ae49430e7
// last-edited: 2026-10-18

package acoustid

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
)

// TestPreviewBehaviour_FingerprintDurationRepair runs the op on what
// /api/v3/ops would queue (its own Preview declaration's Resolve, handed to
// its registered Run) and counts store writes: a preview makes none, a live
// run makes at least one. It is the live-key representative: a declaration
// whose type lacked live would leave every live run a preview, and the second
// half fails.
func TestPreviewBehaviour_FingerprintDurationRepair(t *testing.T) {
	origLen := fileFingerprintLengthFn
	t.Cleanup(func() { fileFingerprintLengthFn = origLen })
	fileFingerprintLengthFn = func(string, int) (*fingerprint.WholeFile, error) {
		return &fingerprint.WholeFile{Raw: []byte{1, 2, 3, 4}, DurationSec: 42}, nil
	}
	origTools := config.AppConfig.FingerprintWindowReferenceTools
	t.Cleanup(func() { config.AppConfig.FingerprintWindowReferenceTools = origTools })
	config.AppConfig.FingerprintWindowReferenceTools = ""

	path := filepath.Join(t.TempDir(), "a.m4b")
	if err := os.WriteFile(path, []byte("synthetic"), 0o644); err != nil {
		t.Fatal(err)
	}
	files := []database.BookFile{{ID: "f1", BookID: "b1", FilePath: path, AcoustIDFingerprint: []byte{9, 9, 9, 9}}}

	for _, tc := range []struct {
		mode      string
		wantWrite bool
	}{{"preview", false}, {"live", true}} {
		t.Run(tc.mode, func(t *testing.T) {
			var writes atomic.Int64
			store := &database.MockStore{
				GetFilesWithZeroDurationFingerprintFunc: func(int, int) ([]database.BookFile, int64, error) {
					return files, int64(len(files)), nil
				},
				UpdateBookFileFunc: func(string, *database.BookFile) error { writes.Add(1); return nil },
			}
			p := &Plugin{store: store}
			def := p.durationBackfillDef()
			raw, err := def.Preview.Resolve(json.RawMessage(`{}`), tc.mode)
			if err != nil {
				t.Fatal(err)
			}
			if err := def.Run(context.Background(), raw, &lshTestReporter{}); err != nil {
				t.Fatalf("Run(%s): %v", raw, err)
			}
			if got := writes.Load(); tc.wantWrite && got == 0 || !tc.wantWrite && got != 0 {
				t.Fatalf("mode %s (params %s) made %d store writes; want writes=%v", tc.mode, raw, got, tc.wantWrite)
			}
		})
	}
}
