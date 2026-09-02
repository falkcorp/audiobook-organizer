// file: internal/server/server_helpers.go
// version: 1.4.1
// guid: 8a40b808-2bf2-4a35-893c-ad5e3351dbae
// last-edited: 2026-09-02

package server

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/appdirs"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
	"github.com/falkcorp/audiobook-organizer/internal/security/pathvalidation"
)

func SetVersion(v string) {
	appVersion = v
}

// validateAbsolutePath rejects non-absolute paths and paths containing traversal
// sequences. Delegates to pathvalidation.CleanAbsolutePath; kept for callers
// that only need an error signal and don't use the cleaned return value.
func validateAbsolutePath(path string) error {
	_, err := pathvalidation.CleanAbsolutePath(path)
	return err
}

// resetLibrarySizeCache resets the library size cache (for testing)
func resetLibrarySizeCache() {
	cacheLock.Lock()
	defer cacheLock.Unlock()
	cachedLibrarySize = 0
	cachedImportSize = 0
	cachedSizeComputedAt = time.Time{}
}

func stringVal(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

func intVal(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

// nonEmpty is metafetch.NonEmpty. This package held a byte-identical copy until
// 2026-09-01; the implementation is canonical there because
// BuildMetadataProvenance -- which used to live in this file too -- depends on
// it. An alias rather than a rename keeps all 9 call sites unchanged.
var nonEmpty = metafetch.NonEmpty

// warmLibrarySizes runs calculateLibrarySizes once at startup so the
// filesystem-walk path (Sonarr/Radarr-style refresh of physical-on-disk
// sizes) is primed for any caller that asks for it later. The hot path
// of /system/status itself reads from DB stats (PR #1137), so this is
// purely an offline refresh — never blocks any request.
//
// Runs in its own goroutine; safe to start before memdb warmup completes.
func (s *Server) warmLibrarySizes() {
	if s.Ops() == nil {
		return
	}
	folders, err := s.Ops().GetAllImportPaths()
	if err != nil {
		slog.Info("library size warm-up skipped", "err", err)
		return
	}
	rootDir := strings.TrimSpace(config.AppConfig.RootDir)
	started := time.Now()
	slog.Info("library size warm-up starting",
		"root_dir", rootDir,
		"import_folders", len(folders))
	lib, imp := calculateLibrarySizes(rootDir, folders)
	slog.Info("library size warm-up complete",
		"library_bytes", lib,
		"import_bytes", imp,
		"duration_ms", time.Since(started).Milliseconds(),
	)
}

func calculateLibrarySizes(rootDir string, importFolders []database.ImportPath) (librarySize, importSize int64) {
	cacheLock.RLock()
	if time.Since(cachedSizeComputedAt) < librarySizeCacheTTL {
		librarySize = cachedLibrarySize
		importSize = cachedImportSize
		cacheLock.RUnlock()
		// cached sizes used
		return
	}
	cacheLock.RUnlock()

	// Cache expired, recalculate
	cacheLock.Lock()
	defer cacheLock.Unlock()

	// Double-check in case another goroutine just updated
	if time.Since(cachedSizeComputedAt) < librarySizeCacheTTL {
		return cachedLibrarySize, cachedImportSize
	}

	// Recalculating library sizes (cache expired)

	// appdirs.Current() is resolved here, in the function body, rather than
	// threaded as a parameter: this function is passed as a VALUE to
	// sysinfo.NewSystemService (registry_wire.go), so widening its signature
	// would ripple into a file this change must not touch.
	app := appdirs.Current()

	// Calculate library size
	librarySize = 0
	if rootDir != "" {
		if info, err := os.Stat(rootDir); err == nil && info.IsDir() {
			filepath.Walk(rootDir, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return nil
				}
				if info.IsDir() {
					// Not a deletion hazard -- a WRONG ANSWER. The reported
					// "library size" currently includes the application's own
					// backup archives and OpenLibrary dumps, which on a real
					// install is tens of GB of storage attributed to books
					// that do not exist. It is also the slowest walk on the
					// startup path, and it re-walks that data every TTL.
					if pathutil.ShouldSkipDir(rootDir, path, app) {
						return filepath.SkipDir
					}
					return nil
				}
				librarySize += filePhysicalSize(info)
				return nil
			})
		}
	}

	// Calculate import path sizes independently (not by subtraction)
	importSize = 0
	for _, folder := range importFolders {
		if !folder.Enabled {
			continue
		}
		if info, err := os.Stat(folder.Path); err == nil && info.IsDir() {
			filepath.Walk(folder.Path, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return nil
				}
				if info.IsDir() {
					// Each import path is its own walk root, so an app dir
					// configured inside one is reached the same way it is
					// under the library root.
					if pathutil.ShouldSkipDir(folder.Path, path, app) {
						return filepath.SkipDir
					}
					return nil
				}
				// Skip files that are under rootDir to avoid double counting
				if rootDir != "" && strings.HasPrefix(path, rootDir) {
					return nil
				}
				importSize += filePhysicalSize(info)
				return nil
			})
		}
	}

	// Update cache
	cachedLibrarySize = librarySize
	cachedImportSize = importSize
	cachedSizeComputedAt = time.Now()

	// sizes recalculated
	return
}
