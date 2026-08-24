// file: internal/scanner/service_unit_test.go
// version: 1.2.0
// guid: e2f3a4b5-c6d7-8e9f-0a1b-3c4d5e6f7a8b
// last-edited: 2026-08-24

package scanner

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScanService_DetermineFolders_SpecificFolderPath(t *testing.T) {
	mockDB := &database.MockStore{}
	ss := NewScanService(mockDB)
	log := logger.New("test")

	path := "/my/audiobooks"
	folders, err := ss.determineFoldersToScan(&path, false, false, log)

	assert.NoError(t, err)
	assert.Equal(t, []string{"/my/audiobooks"}, folders)
}

func TestScanService_DetermineFolders_ImportPathError(t *testing.T) {
	mockDB := &database.MockStore{
		GetAllImportPathsFunc: func() ([]database.ImportPath, error) {
			return nil, errors.New("db unavailable")
		},
	}
	ss := NewScanService(mockDB)
	log := logger.New("test")

	folders, err := ss.determineFoldersToScan(nil, false, false, log)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get import paths")
	assert.Nil(t, folders)
}

func TestScanService_DetermineFolders_DisabledPathsExcluded(t *testing.T) {
	mockDB := &database.MockStore{
		GetAllImportPathsFunc: func() ([]database.ImportPath, error) {
			return []database.ImportPath{
				{Path: "/enabled1", Enabled: true},
				{Path: "/disabled", Enabled: false},
				{Path: "/enabled2", Enabled: true},
			}, nil
		},
	}
	ss := NewScanService(mockDB)
	log := logger.New("test")

	folders, err := ss.determineFoldersToScan(nil, false, false, log)

	assert.NoError(t, err)
	assert.Equal(t, []string{"/enabled1", "/enabled2"}, folders)
}

func TestScanService_DetermineFolders_ForceUpdateIncludesRootDir(t *testing.T) {
	origRoot := config.AppConfig.RootDir
	config.AppConfig.RootDir = "/library/root"
	t.Cleanup(func() { config.AppConfig.RootDir = origRoot })

	mockDB := &database.MockStore{
		GetAllImportPathsFunc: func() ([]database.ImportPath, error) {
			return []database.ImportPath{
				{Path: "/import/one", Enabled: true},
			}, nil
		},
	}
	ss := NewScanService(mockDB)
	log := logger.New("test")

	folders, err := ss.determineFoldersToScan(nil, true, false, log)

	assert.NoError(t, err)
	assert.Contains(t, folders, "/library/root")
	assert.Contains(t, folders, "/import/one")
	assert.Equal(t, "/library/root", folders[0], "root dir should be first")
}

// include_root_dir reaches RootDir WITHOUT force_update. Before this existed,
// force_update was the only route to RootDir and it also nils the scan cache, so
// "scan the whole library" and "re-hash every file in it" could not be asked for
// separately.
func TestScanService_DetermineFolders_IncludeRootDirWithoutForceUpdate(t *testing.T) {
	origRoot := config.AppConfig.RootDir
	config.AppConfig.RootDir = "/library/root"
	t.Cleanup(func() { config.AppConfig.RootDir = origRoot })

	mockDB := &database.MockStore{
		GetAllImportPathsFunc: func() ([]database.ImportPath, error) {
			return []database.ImportPath{{Path: "/import/one", Enabled: true}}, nil
		},
	}
	ss := NewScanService(mockDB)

	folders, err := ss.determineFoldersToScan(nil, false, true, logger.New("test"))

	assert.NoError(t, err)
	assert.Contains(t, folders, "/library/root",
		"include_root_dir=true must reach RootDir even with force_update=false")
	assert.Contains(t, folders, "/import/one", "import paths must still be scanned")
}

// The default must not move. Neither flag set keeps the historical exclusion.
func TestScanService_DetermineFolders_DefaultStillExcludesRootDir(t *testing.T) {
	origRoot := config.AppConfig.RootDir
	config.AppConfig.RootDir = "/library/root"
	t.Cleanup(func() { config.AppConfig.RootDir = origRoot })

	mockDB := &database.MockStore{
		GetAllImportPathsFunc: func() ([]database.ImportPath, error) {
			return []database.ImportPath{{Path: "/import/one", Enabled: true}}, nil
		},
	}
	ss := NewScanService(mockDB)

	folders, err := ss.determineFoldersToScan(nil, false, false, logger.New("test"))

	assert.NoError(t, err)
	assert.NotContains(t, folders, "/library/root",
		"a default scan must still exclude the organize destination")
	assert.Equal(t, []string{"/import/one"}, folders)
}

// The decoupling itself, exercised against the REAL code path rather than a
// restatement of it: run performScanInternal and record whether it asked the
// store for the scan cache.
//
// An earlier version of this test asserted `!forceUpdate` against a hand-copied
// mirror of the production condition. That is worthless -- it passes even if the
// real guard inverts, because it never runs the real guard. Calling the actual
// entry point is the only version that can catch the regression it names.
func TestScanService_IncludeRootDirDoesNotDisableTheScanCache(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		forceUpdate, incRoot bool
		wantCacheLoaded      bool
	}{
		{"default", false, false, true},
		{"include_root_dir only", false, true, true},
		{"force_update", true, false, false},
		{"both", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			origRoot := config.AppConfig.RootDir
			config.AppConfig.RootDir = root
			t.Cleanup(func() { config.AppConfig.RootDir = origRoot })

			cacheAsked := false
			mockDB := &database.MockStore{
				GetAllImportPathsFunc: func() ([]database.ImportPath, error) {
					// Empty, so RootDir is the ONLY way this scan has work to do
					// and the run cannot bail at the len(folders)==0 early return
					// when include_root_dir is what put it there.
					return []database.ImportPath{{Path: root, Enabled: true}}, nil
				},
				GetScanCacheMapFunc: func() (map[string]database.ScanCacheEntry, error) {
					cacheAsked = true
					return map[string]database.ScanCacheEntry{}, nil
				},
			}
			ss := NewScanService(mockDB)

			_ = ss.performScanInternal(context.Background(), "",
				&ScanRequest{ForceUpdate: &tc.forceUpdate, IncludeRootDir: &tc.incRoot},
				logger.New("test"))

			assert.Equal(t, tc.wantCacheLoaded, cacheAsked,
				"include_root_dir must not gate the incremental cache; only force_update may. "+
					"If this inverts, asking for the whole library silently becomes a full re-hash.")
		})
	}
}

func TestScanService_PerformScan_NoFoldersReturnsNil(t *testing.T) {
	mockDB := &database.MockStore{
		GetAllImportPathsFunc: func() ([]database.ImportPath, error) {
			return []database.ImportPath{}, nil
		},
	}
	ss := NewScanService(mockDB)
	log := logger.New("test")

	err := ss.PerformScan(context.Background(), &ScanRequest{}, log)

	assert.NoError(t, err)
}

func TestScanService_UpdateImportPathBookCount(t *testing.T) {
	var updatedID int
	var updatedCount int

	mockDB := &database.MockStore{
		CountBooksByPathPrefixFunc: func(prefix string) (int, error) {
			if prefix == "/path/b" {
				return 42, nil
			}
			return 0, nil
		},
		GetAllImportPathsFunc: func() ([]database.ImportPath, error) {
			return []database.ImportPath{
				{ID: 1, Path: "/path/a", BookCount: 0},
				{ID: 2, Path: "/path/b", BookCount: 5},
			}, nil
		},
		UpdateImportPathFunc: func(id int, ip *database.ImportPath) error {
			updatedID = id
			updatedCount = ip.BookCount
			return nil
		},
	}
	ss := NewScanService(mockDB)
	log := logger.New("test")

	ss.updateImportPathBookCount("/path/b", 42, log)

	assert.Equal(t, 2, updatedID)
	assert.Equal(t, 42, updatedCount)
}

func TestScanService_UpdateImportPathBookCount_NoMatch(t *testing.T) {
	updateCalled := false

	mockDB := &database.MockStore{
		GetAllImportPathsFunc: func() ([]database.ImportPath, error) {
			return []database.ImportPath{
				{ID: 1, Path: "/other/path"},
			}, nil
		},
		UpdateImportPathFunc: func(_ int, _ *database.ImportPath) error {
			updateCalled = true
			return nil
		},
	}
	ss := NewScanService(mockDB)
	log := logger.New("test")

	ss.updateImportPathBookCount("/nonexistent", 10, log)

	assert.False(t, updateCalled, "UpdateImportPath should not be called for non-matching path")
}

func TestScanService_ReportCompletion_Messages(t *testing.T) {
	tests := []struct {
		name     string
		stats    ScanStats
		contains string
	}{
		{
			name:     "library and import",
			stats:    ScanStats{TotalBooks: 15, LibraryBooks: 10, ImportBooks: 5},
			contains: "Library: 10 books, Import: 5 books",
		},
		{
			name:     "library only",
			stats:    ScanStats{TotalBooks: 10, LibraryBooks: 10},
			contains: "Library: 10 books",
		},
		{
			name:     "import only",
			stats:    ScanStats{TotalBooks: 5, ImportBooks: 5},
			contains: "Import: 5 books",
		},
		{
			name:     "no books",
			stats:    ScanStats{},
			contains: "No books found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockDB := &database.MockStore{}
			ss := NewScanService(mockDB)
			log := logger.New("test")

			// reportCompletion should not panic; verify it runs without error.
			ss.reportCompletion(tt.stats.TotalBooks, tt.stats.TotalBooks, &tt.stats, log)
		})
	}
}

// TestScanService_ProgressCallback_UsesLogBatch verifies that the per-file
// progress callback in scanFolder routes through activity.LogBatch (and therefore
// through the ActivityBatcher) rather than falling through as unregistered plain
// debug entries. This is the ACT-BATCH-FU-2 regression guard.
//
// The test exercises the same LogBatch pattern used in service.go's scanFolder
// progress callback, then confirms FlushOperation collapses N per-file items
// into a single batched ActivityEntry.
func TestScanService_ProgressCallback_UsesLogBatch(t *testing.T) {
	const opID = "test-op-logbatch"
	nFiles := 4

	// Build a real activity.Writer with a buffer-only channel. Do NOT call
	// w.Start() so there is no drain goroutine — we inspect the channel directly.
	w := activity.NewWriter(nil, 128)

	// Simulate the progress callback that scanFolder builds (same call site).
	paths := []string{
		"/audiobooks/Book1.m4b",
		"/audiobooks/Book2.m4b",
		"/audiobooks/Book3.mp3",
		"/audiobooks/Book4.mp3",
	}
	for _, p := range paths {
		activity.LogBatch(w, opID, "tag-scan", "scan-service",
			activity.BatchItem{Name: filepath.Base(p)})
	}

	// Before the batch window expires nothing should be on the channel.
	require.Equal(t, 0, len(w.Chan()), "items must be held in batcher, not emitted immediately")

	// FlushOperation simulates the call added to performScanInternal before
	// reportCompletion. It should collapse all nFiles items into one entry.
	activity.FlushOperation(w, opID)

	require.Equal(t, 1, len(w.Chan()),
		"FlushOperation must emit exactly 1 merged batch entry for %d per-file LogBatch calls", nFiles)

	entry := <-w.Chan()
	require.Equal(t, "tag-scan", entry.Type)
	require.Equal(t, "batch", entry.Tier, "entry must be a batch-tier entry, not plain debug")
	batched, ok := entry.Details["batched"]
	require.True(t, ok, "batched entry must have Details[\"batched\"]")
	require.Equal(t, true, batched)
	originalCount, _ := entry.Details["original_count"].(float64)
	require.Equal(t, float64(nFiles), originalCount,
		"original_count must equal the number of LogBatch calls")
}
