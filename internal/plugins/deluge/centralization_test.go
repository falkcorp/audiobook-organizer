// file: internal/plugins/deluge/centralization_test.go
// version: 1.0.0
// guid: d4e5f6a7-b8c9-4d0e-9f1a-2b3c4d5e6f7a
// last-edited: 2026-07-07

package deluge

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// --- test reporter (mirrors internal/plugins/acoustid/lsh_backfill_test.go) --

type centralizationTestReporter struct{}

func (r *centralizationTestReporter) UpdateProgress(int, int, string) error { return nil }
func (r *centralizationTestReporter) Log(slog.Level, string, ...slog.Attr) error {
	return nil
}
func (r *centralizationTestReporter) Logger() *slog.Logger { return slog.Default() }
func (r *centralizationTestReporter) Checkpoint(any) error { return nil }
func (r *centralizationTestReporter) IsCanceled() bool     { return false }
func (r *centralizationTestReporter) RunPhase(ctx context.Context, _ string, fn func(context.Context, sdk.Reporter) error) error {
	return fn(ctx, r)
}
func (r *centralizationTestReporter) Trigger(context.Context, string, any) error { return nil }
func (r *centralizationTestReporter) SetCurrentItem(string)                      {}

// TestRunCentralization_HydratesBeforeWriteback is a regression test for
// STOREFID PR-D: GetBookFilesNeedingDelugeImportCore returns a memdb-slim
// BookFileCore, and centralization used to write that struct straight back
// via UpdateBookFile, silently wiping the fingerprint diagnostic fields.
// This asserts a pre-set FingerprintDiagnosticJSON survives the centralize
// pass — it fails on the pre-fix code (bare Core-to-Book write) because the
// value would arrive at UpdateBookFile as nil.
func TestRunCentralization_HydratesBeforeWriteback(t *testing.T) {
	root := t.TempDir()
	// srcPath lives outside root so the centralize pass computes a distinct
	// destDir (== root) instead of hitting the srcPath == dest skip path.
	srcDir := t.TempDir()
	srcPath := filepath.Join(srcDir, "book.mp3")
	if err := os.WriteFile(srcPath, []byte("test data"), 0o644); err != nil {
		t.Fatalf("write src file: %v", err)
	}

	origRoot := config.AppConfig.RootDir
	config.AppConfig.RootDir = root
	defer func() { config.AppConfig.RootDir = origRoot }()

	diagJSON := `{"reason":"low_confidence"}`

	fullFile := &database.BookFile{
		ID:                        "f1",
		BookID:                    "b1",
		FilePath:                  srcPath,
		FingerprintDiagnosticJSON: &diagJSON,
	}

	var updated *database.BookFile
	store := &database.MockStore{
		GetBookFilesNeedingDelugeImportCoreFunc: func() ([]database.BookFileCore, error) {
			return []database.BookFileCore{fullFile.Core()}, nil
		},
		GetBookFileByIDFunc: func(bookID, fileID string) (*database.BookFile, error) {
			if bookID == fullFile.BookID && fileID == fullFile.ID {
				return fullFile, nil
			}
			return nil, nil
		},
		UpdateBookFileFunc: func(id string, file *database.BookFile) error {
			updated = file
			return nil
		},
	}

	p := &Plugin{store: store}
	r := &centralizationTestReporter{}

	if err := p.runCentralization(context.Background(), nil, r); err != nil {
		t.Fatalf("runCentralization returned error: %v", err)
	}

	if updated == nil {
		t.Fatal("UpdateBookFile was never called")
	}
	if updated.FingerprintDiagnosticJSON == nil {
		t.Fatal("FingerprintDiagnosticJSON was wiped on writeback (PR-D regression)")
	}
	if *updated.FingerprintDiagnosticJSON != diagJSON {
		t.Errorf("FingerprintDiagnosticJSON = %q, want %q", *updated.FingerprintDiagnosticJSON, diagJSON)
	}
}
