// file: internal/plugins/deluge/centralization_test.go
// version: 1.3.0
// guid: d4e5f6a7-b8c9-4d0e-9f1a-2b3c4d5e6f7a
// last-edited: 2026-10-11

package deluge

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	delugeclient "github.com/falkcorp/audiobook-organizer/internal/deluge"
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

// centralize copies with fileops.ReflinkOrCopy, which refuses an existing
// destination, so a file already at RootDir/<basename> (another book's, or
// one a torrent seeds from the library root) is never overwritten: the row is
// counted an error, left pending, and the file keeps its bytes.
func TestRunCentralization_ExistingDestinationNeverOverwritten(t *testing.T) {
	root := t.TempDir()
	srcPath := filepath.Join(t.TempDir(), "book.mp3")
	if err := os.WriteFile(srcPath, []byte("test data"), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, "book.mp3")
	if err := os.WriteFile(dest, []byte("another book's bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	origRoot := config.AppConfig.RootDir
	config.AppConfig.RootDir = root
	defer func() { config.AppConfig.RootDir = origRoot }()

	row := database.BookFile{ID: "f1", BookID: "b1", FilePath: srcPath}
	store := &database.MockStore{
		GetBookFilesNeedingDelugeImportCoreFunc: func() ([]database.BookFileCore, error) {
			return []database.BookFileCore{row.Core()}, nil
		},
		GetBookFileByIDFunc: func(string, string) (*database.BookFile, error) { return &row, nil },
		UpdateBookFileFunc: func(string, *database.BookFile) error {
			t.Error("UpdateBookFile called: the row must stay pending when its destination is taken")
			return nil
		},
	}
	if err := (&Plugin{store: store}).runCentralization(context.Background(), nil, &centralizationTestReporter{}); err != nil {
		t.Fatalf("runCentralization: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || string(got) != "another book's bytes" {
		t.Errorf("destination holds %q (err %v): it was overwritten", got, err)
	}
}

// A pending row that is its own library destination is marked imported in
// place only when its path is not protected. A path under
// config.ProtectedPaths, or under the save_path of a torrent seeding from
// inside the library (the plugin's ProtectedPathCache), is left pending with
// nothing written -- what /discovery/import's ImportToLibraryWith does
// (tagger.ErrProtectedPathWrite). The control case shows the row would be
// marked otherwise.
func TestRunCentralization_InPlaceProtectedLeftPending(t *testing.T) {
	cases := []struct {
		name      string
		protected func(dir string) []string // config.ProtectedPaths
		seeding   bool                      // a torrent seeds from the row's directory
		unloaded  bool                      // Deluge is configured but cannot be reached
		wantMark  bool
	}{
		{name: "unprotected control", wantMark: true},
		{name: "config protected path", protected: func(dir string) []string { return []string{dir} }},
		{name: "torrent seeding from the library", seeding: true},
		// A configured Deluge that has never answered: the cache would call
		// every seeding file unprotected, so nothing is marked.
		{name: "protected list not loaded", unloaded: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "Example Author", "Example Book")
			origRoot, origProt := config.AppConfig.RootDir, config.AppConfig.ProtectedPaths
			config.AppConfig.RootDir, config.AppConfig.ProtectedPaths = root, nil
			if tc.protected != nil {
				config.AppConfig.ProtectedPaths = tc.protected(dir)
			}
			t.Cleanup(func() { config.AppConfig.RootDir, config.AppConfig.ProtectedPaths = origRoot, origProt })

			row := database.BookFile{ID: "bf-1", BookID: "book-1", FilePath: filepath.Join(dir, "01.mp3"), DelugeHash: linkTorrentID(1)}
			modifies := 0
			store := &database.MockStore{
				GetBookFilesNeedingDelugeImportCoreFunc: func() ([]database.BookFileCore, error) {
					return []database.BookFileCore{row.Core()}, nil
				},
				ModifyBookFileFunc: func(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error) {
					modifies++
					cp := row
					if err := fn(&cp); err != nil {
						return &row, nil
					}
					row = cp
					return &row, nil
				},
				UpdateBookFileFunc: func(string, *database.BookFile) error {
					t.Error("UpdateBookFile called: an in-place row is never rewritten whole")
					return nil
				},
			}
			p := &Plugin{store: store}
			if tc.seeding {
				tor := delugeclient.TorrentDetail{TorrentStatus: delugeclient.TorrentStatus{Hash: linkTorrentID(1), SavePath: dir}}
				_, client := newFakeDelugeClient(t, tor)
				p.client = client
				p.cache = delugeclient.NewProtectedPathCache(client, config.AppConfig.ProtectedPaths)
			}
			if tc.unloaded {
				down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					http.Error(w, "down", http.StatusServiceUnavailable)
				}))
				t.Cleanup(down.Close)
				client, err := delugeclient.New(down.URL, "test-password")
				if err != nil {
					t.Fatal(err)
				}
				p.client = client
				p.cache = delugeclient.NewProtectedPathCache(client, config.AppConfig.ProtectedPaths)
			}

			if err := p.runCentralization(context.Background(), nil, &centralizationTestReporter{}); err != nil {
				t.Fatalf("runCentralization: %v", err)
			}
			if marked := row.ImportedFromDelugeAt != nil; marked != tc.wantMark {
				t.Errorf("marked imported = %v, want %v", marked, tc.wantMark)
			}
			if !tc.wantMark && modifies != 0 {
				t.Errorf("ModifyBookFile called %d time(s) for a protected row; want none", modifies)
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Errorf("centralize created the row's directory (stat err %v)", err)
			}
		})
	}
}
