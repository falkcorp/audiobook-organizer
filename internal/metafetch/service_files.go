// file: internal/metafetch/service_files.go
// version: 1.4.0
// guid: 969b284a-5657-442b-beba-275e325e000b
// last-edited: 2026-09-01

package metafetch

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// AudioFilesInDir returns the library audio files directly inside dir, sorted
// by name.
//
// Follows supported_extensions. The previous implementation globbed a private
// 8-pattern list, which was wrong three ways: it skipped the seven configured
// extensions it did not know about; filepath.Glob is case-sensitive on Linux,
// so a "Chapter 01.MP3" was invisible; and a directory whose own name contains
// a glob metacharacter ("[Unabridged]" is a real shape in this library) made
// every pattern match nothing. Reading the directory and testing the extension
// has none of those failure modes.
func AudioFilesInDir(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	exts := config.SupportedExtensionSet()
	var files []string
	for _, e := range entries {
		if e.IsDir() || !exts.MatchPath(e.Name()) {
			continue
		}
		files = append(files, filepath.Join(dir, e.Name()))
	}
	sort.Strings(files)
	return files
}

// backupFileBeforeWrite creates a timestamped .bak copy of a file before
// writing tags — IF the WriteBackupBeforeTagWrite config flag is enabled.
//
// Default is OFF. Historically this function ran unconditionally on every
// tag write and used os.Link (hardlink) for "no disk space cost". Two
// problems with that:
//
//  1. Tens of thousands of stale backup files accumulated across the
//     library (43K+ files, multi-TB apparent size in production) because
//     nothing ever cleaned them up.
//  2. Hardlinks don't actually preserve pre-write content when the
//     writer modifies the inode in place (which TagLib does for some
//     formats). The "backup" could be a hardlink to the same now-modified
//     data, providing false safety.
//
// The flag is opt-in. Users who turn it on should also run the
// cleanup-backups maintenance endpoint periodically to keep the library
// from growing unbounded.
//
// Failures are logged but non-fatal — the write-back proceeds regardless.
func backupFileBeforeWrite(filePath string) {
	if !config.AppConfig.MetadataScoring.WriteBackupBefore {
		return
	}
	if filePath == "" {
		return
	}
	if _, err := os.Stat(filePath); err != nil {
		return
	}
	backupPath := filePath + ".bak-" + time.Now().Format("20060102-150405")
	if err := os.Link(filePath, backupPath); err != nil {
		// Hardlink failed — fall back to copy
		if err := fileops.SafeCopy(filePath, backupPath, fileops.OperationConfig{}); err != nil {
			slog.Warn("backup before tag write failed:", "path", filePath, "error", err)
			return
		}
	}
	slog.Debug("backup before tag write", "path", backupPath)
}

// ApplyMetadataFileIO runs the slow file operations after metadata is applied:
// cover embed, tag write-back, file rename. Cover download is done inline
// in ApplyMetadataCandidate so the response includes the updated cover URL.
// Designed to run in a background goroutine.
//
// It returns an error when the file work did not fully land. Until 2026-08-16
// it returned nothing and swallowed the pipeline failure into a slog.Warn, so
// no caller could tell a completed rename from a failed one — and
// applyCachedCandidateForBook reported Applied:true either way, i.e. the API
// said the apply succeeded while the files had never moved.
//
// WHAT A NON-NIL ERROR MEANS: "the file work did not fully land", NOT "nothing
// happened". runApplyPipeline deliberately persists the book_file rows for
// every rename that DID succeed before returning the failure, so a partial
// rename is durable and already recorded. Callers must therefore keep
// reporting the database apply as successful and flag only the file side --
// see applyOutcome.WriteBackFailed, which exists for exactly this shape.
//
// The cover embed is deliberately NOT part of the returned error:
// embedCoverInBookFiles reports nothing and a missing cover must not mask a
// rename failure or block the pipeline below it.
func (mfs *Service) ApplyMetadataFileIO(id string) error {
	book, err := mfs.db.GetBookByID(id)
	if err != nil {
		return fmt.Errorf("apply file I/O: load book %s: %w", id, err)
	}
	if book == nil {
		// Reported rather than ignored: the two recovery handlers replay file
		// ops recorded before a restart, and a book that has since been deleted
		// is the one case where this is expected and benign. Naming it lets the
		// caller log which book vanished instead of silently doing nothing.
		return fmt.Errorf("apply file I/O: book %s not found", id)
	}

	// Embed cover art into audio files (slow: ffmpeg)
	if config.AppConfig.RootDir != "" {
		mfs.embedCoverInBookFiles(book, metadata.CoverPathForBook(config.AppConfig.RootDir, id))
	}

	// Run file rename + tag write pipeline
	if config.AppConfig.AutoRenameOnApply || config.AppConfig.AutoWriteTagsOnApply {
		if err := mfs.runApplyPipeline(id, book); err != nil {
			return fmt.Errorf("apply file I/O: pipeline for book %s: %w", id, err)
		}
	}
	return nil
}

// computeITunesPath converts a local file path to an iTunes file:// URL
// using the configured path mappings (m.To = Linux prefix, m.From = Windows prefix).
// Returns an empty string if no mapping matches.
func ComputeITunesPath(localPath string) string {
	for _, m := range config.AppConfig.ITunes.PathMappings {
		if m.To != "" && m.From != "" && strings.HasPrefix(localPath, m.To) {
			remainder := localPath[len(m.To):]
			windowsPath := m.From + remainder
			encoded := url.PathEscape(windowsPath)
			encoded = strings.ReplaceAll(encoded, "%2F", "/")
			encoded = strings.ReplaceAll(encoded, "%3A", ":")
			return "file://localhost/" + encoded
		}
	}
	return ""
}

// removeEmptyDirs removes empty directories walking up from dir until reaching stopAt.
func removeEmptyDirs(dir, stopAt string) {
	for dir != stopAt && dir != "/" && dir != "." {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) > 0 {
			break
		}
		if err := os.Remove(dir); err != nil {
			break
		}
		slog.Info("removed empty directory", "value", dir)
		dir = filepath.Dir(dir)
	}
}
