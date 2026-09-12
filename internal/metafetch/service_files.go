// file: internal/metafetch/service_files.go
// version: 1.10.0
// guid: 969b284a-5657-442b-beba-275e325e000b
// last-edited: 2026-09-12

package metafetch

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
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

// tagWriteResult reports what runApplyPipeline did about audio tags.
type tagWriteResult struct {
	// handled: the pipeline owned the tag write for this run (wrote them, found
	// them already written by an interrupted earlier attempt, or failed).
	handled bool
	// err is the pipeline's tag-write failure, if any.
	err error
}

// writeTags is the one tag write every apply path goes through. tagWriter is
// a test seam that counts it; nil in production.
func (mfs *Service) writeTags(id string, policy copyPolicy) (int, error) {
	if mfs.tagWriter != nil {
		return mfs.tagWriter(id)
	}
	return mfs.writeBackForBook(id, nil, policy)
}

// copyPolicy says whether file work may create a library copy for a book that
// lives under a protected (iTunes/import) path.
type copyPolicy bool

const (
	// createLibraryCopy: user-initiated applies. A protected book gets a
	// library copy made if it has none, and the file work runs on the copy.
	createLibraryCopy copyPolicy = true
	// existingCopyOnly: auto-fetch. File work runs on a library copy that
	// already exists, and is skipped when there is none.
	existingCopyOnly copyPolicy = false
)

// fileWorkTarget is the book whose audio files the file work writes: book
// itself, or -- for a book under a protected (iTunes/import) path -- its
// library copy, created under createLibraryCopy and only looked up under
// existingCopyOnly. Nil means a protected book with no copy: nothing may be
// written.
//
// It is the ONE resolution both the writers (embed, rename pipeline) and the
// path-lock key use. Until 2026-09-12 they disagreed: auto-fetch locked the
// original book's path while runApplyPipeline wrote the library copy's, so the
// lock guarded a path nothing wrote and a manual apply of the copy ran
// alongside it.
func (mfs *Service) fileWorkTarget(book *database.Book, policy copyPolicy) *database.Book {
	if book == nil || !mfs.isProtectedPath(book.FilePath) {
		return book
	}
	return mfs.libraryCopyFor(book, policy)
}

// lockPath takes the server's per-path write lock (SetPathLocker) and returns
// its release. With no locker wired, or no path, it is a no-op.
func (mfs *Service) lockPath(path string) func() {
	if mfs.pathLock == nil || path == "" {
		return func() {}
	}
	return mfs.pathLock(path)
}

// lockWriteTarget re-reads book id, resolves the files the next write touches
// (fileWorkTarget) and locks THAT path. The re-read matters: a rename earlier
// in the same file work moves the files, and a lock on the pre-rename path
// guards nothing.
func (mfs *Service) lockWriteTarget(id string, policy copyPolicy) func() {
	book, err := mfs.db.GetBookByID(id)
	if err != nil || book == nil {
		return func() {}
	}
	target := mfs.fileWorkTarget(book, policy)
	if target == nil {
		return func() {}
	}
	return mfs.lockPath(target.FilePath)
}

// bookLockKey is the lock-table key for one book's whole file-work sequence.
// Path keys are absolute paths, so the "book:" prefix cannot collide with one.
func bookLockKey(id string) string { return "book:" + id }

// lockBookFileWork serializes file-work jobs for the same book. It locks the
// book, then -- for a protected book -- its existing library copy, whose files
// the work writes. Callers hold it across the whole sequence (cover download,
// embed, rename, tags) and take path locks only inside it: book locks first,
// path locks second, never the other way round. A library copy is never itself
// protected, so "original, then copy" is a fixed order and two jobs cannot
// wait on each other.
//
// Without it the pool ran two jobs for one book at once, and the path lock was
// keyed from a read made before blocking: job X renamed P to Q under P's lock
// and then tagged Q, while job Y, which had read P earlier, took P's lock once
// X released it and embedded or renamed files that were already at Q.
//
// The copy is looked up, never created (existingCopyOnly): a batch apply with
// write-back off must not make a library copy just to pick a lock.
func (mfs *Service) lockBookFileWork(id string) func() {
	if mfs.pathLock == nil || id == "" {
		return func() {}
	}
	release := mfs.pathLock(bookLockKey(id))
	book, err := mfs.db.GetBookByID(id)
	if err != nil || book == nil {
		return release
	}
	target := mfs.fileWorkTarget(book, existingCopyOnly)
	if target == nil || target.ID == id {
		return release
	}
	releaseCopy := mfs.pathLock(bookLockKey(target.ID))
	return func() {
		releaseCopy()
		release()
	}
}

// FinishApplyFileWork is the ONE file-side sequel to a metadata apply, shared by
// the single-book apply, the batch applies and auto-fetch, in this order:
//
//  1. download the candidate's cover (pendingCoverURL, i.e.
//     FetchMetadataResponse.PendingCoverURL, which ApplyMetadataCandidate derives
//     AFTER the field allowlist and the lock guard, so a deselected cover is
//     never downloaded). It must precede step 2: the embed there would
//     otherwise bake the previous image into the files;
//  2. when fileIO: cover embed + rename + (under auto_write_tags_on_apply) tag
//     write, via ApplyMetadataFileIO's pipeline;
//  3. when writeTags: write the tags -- unless step 2 already owned the tag
//     write, so each book's files are tagged exactly once.
//
// Before 2026-09-12 there was no shared sequel. The single-book handler did 1-3
// inline and tagged twice whenever auto_write_tags_on_apply was on (step 2
// wrote them, then its own write-back wrote them again); the batch apply did 2
// and 3 -- tagging twice the same way -- and never 1, so a batch-applied book
// kept its old cover forever; and auto-fetch did its own cover download and
// wrote tags only under write_back_metadata, so its DB and its tags disagreed.
//
// The error is the first file-side failure: a pipeline (rename) failure, else
// the tag-write failure. Tags are still written after a pipeline failure --
// correct tags in a file that did not move are still correct.
//
// LOCKING. The whole sequence runs under the per-book lock (lockBookFileWork),
// so two jobs for the same book never overlap. Inside it, every audio-file
// write holds the server's per-path lock
// (SetPathLocker) on the path of the files it writes, resolved per write by
// fileWorkTarget: the cover embed and the rename lock the files' current path
// (the library copy's, for a protected book); the tag write re-reads the book
// and locks the post-rename path. That mirrors the bulk write-back in
// internal/server/metadata_ops.go, which shares the lock table. The lock is
// not reentrant, so a caller must NOT hold it around this call. The cover
// download writes the per-book covers file, not the audio files, and takes
// no lock.
//
// STAND-DOWN. checkpoint is the caller's scan stand-down check:
// ScanStandDownHold.Checkpoint for the single-book and batch-candidates
// handlers, registry.ScanStandDownCheckpoint for the metadata.batch-apply-cached
// op, and nil for the restart replay, which holds no stand-down. It runs before each
// file-writing step: the cover download, the file I/O and the standalone tag
// write. A failed check stops the sequel there and is returned, wrapped with
// the step it stopped before. Metadata is never written to files during a
// library scan, and the handlers checked the hold between these steps before
// they shared this sequel; a single check before the call would let a scan
// that resumes mid-sequel run alongside the rename and the tag write.
func (mfs *Service) FinishApplyFileWork(id, pendingCoverURL string, fileIO, writeTags bool, checkpoint func() error) error {
	defer mfs.lockBookFileWork(id)()
	if err := standDown(checkpoint, id, "the cover download"); err != nil {
		return err
	}
	mfs.DownloadPendingCover(id, pendingCoverURL)
	return mfs.finishFileWork(id, fileIO, writeTags, createLibraryCopy, checkpoint)
}

// standDown runs a stand-down checkpoint before a file-writing step. A nil
// checkpoint always passes.
func standDown(checkpoint func() error, id, step string) error {
	if checkpoint == nil {
		return nil
	}
	if err := checkpoint(); err != nil {
		return fmt.Errorf("apply file work for book %s: stopped before %s: %w", id, step, err)
	}
	return nil
}

// FinishAutoFetchFileWork is the file side of auto-fetch (the per-book Fetch
// button, iTunes import enrichment; organize's fetch-first pass has no pool
// and reaches no file work). What it does, in order:
//
//  1. downloads the cover, only when the book has no local cover yet
//     (downloadAutoFetchCover);
//  2. stops there unless the book already has a library copy under root_dir
//     (the book itself, or a clean version sibling of a protected book). It
//     never creates one;
//  3. embeds the stored cover into that copy's audio files;
//  4. writes the tags ONLY when writeTags is set, which its callers pass as
//     write_back_metadata (off by default).
//
// It NEVER renames files. It does not run the apply rename pipeline, which
// renames under auto_rename_on_apply and tags under auto_write_tags_on_apply --
// both on by default, and both settings for explicit applies. Routing
// auto-fetch through that pipeline made the Fetch button move a library book's
// files to the naming-pattern path and retag them under default config; before
// this PR auto-fetch only embedded the cover and wrote tags under
// write_back_metadata, and that is what it does again.
//
// Locking matches FinishApplyFileWork: the per-book lock (lockBookFileWork) for
// the whole sequence, then the path lock on the files each write touches, the
// library copy's for a protected book. Callers reach it through the server's
// file-I/O pool (SetFileWorkScheduler); the restart replay
// (recoverAutoFetchFileOp) calls it too.
func (mfs *Service) FinishAutoFetchFileWork(id, pendingCoverURL string, writeTags bool) error {
	defer mfs.lockBookFileWork(id)()
	mfs.downloadAutoFetchCover(id, pendingCoverURL)
	book, err := mfs.db.GetBookByID(id)
	if err != nil || book == nil {
		return fmt.Errorf("auto-fetch file work: load book %s: %v", id, err)
	}
	if !mfs.autoFetchHasLibraryCopy(book) {
		slog.Info("auto-fetch: book has no library copy under root_dir; file work skipped (auto-fetch never creates one)",
			"book_id", logger.SanitizeLogValue(id), "path", logger.SanitizeLogValue(book.FilePath))
		return nil
	}
	mfs.embedCover(id, book, existingCopyOnly)
	if !writeTags {
		return nil
	}
	release := mfs.lockWriteTarget(id, existingCopyOnly)
	defer release()
	if _, err := mfs.writeTags(id, existingCopyOnly); err != nil {
		return fmt.Errorf("auto-fetch file work: write tags for book %s: %w", id, err)
	}
	return nil
}

// finishFileWork is steps 2 and 3 of FinishApplyFileWork under a copy policy,
// with the stand-down checkpoint run before each (nil passes).
func (mfs *Service) finishFileWork(id string, fileIO, writeTags bool, policy copyPolicy, checkpoint func() error) error {
	var tags tagWriteResult
	var fileErr error
	if fileIO {
		if err := standDown(checkpoint, id, "the file I/O"); err != nil {
			return err
		}
		tags, fileErr = mfs.applyMetadataFileIO(id, policy)
	}
	if writeTags && !tags.handled {
		if err := standDown(checkpoint, id, "the tag write"); err != nil {
			if fileIO {
				slog.Warn("apply file work stopped after the file I/O: the book's files may be renamed but their tags are stale until it is applied again",
					"book_id", logger.SanitizeLogValue(id), "err", logger.SanitizeLogValue(err.Error()))
			}
			// errors.Join keeps a rename failure from the step above visible.
			return errors.Join(fileErr, err)
		}
		// Resolved and locked after the pipeline returned (and released its
		// own locks), so the key is the post-rename path of the files written.
		release := mfs.lockWriteTarget(id, policy)
		if _, err := mfs.writeTags(id, policy); err != nil {
			tags.err = err
		}
		release()
	}
	if fileErr != nil {
		return fileErr
	}
	if tags.err != nil {
		return fmt.Errorf("apply file work: write tags for book %s: %w", id, tags.err)
	}
	return nil
}

// embedCover embeds book id's stored cover into the audio files the file work
// writes (fileWorkTarget: the library copy's, for a protected book), holding
// the path lock on those files. Slow: ffmpeg.
func (mfs *Service) embedCover(id string, book *database.Book, policy copyPolicy) {
	if config.AppConfig.RootDir == "" {
		return
	}
	target := mfs.fileWorkTarget(book, policy)
	if target == nil {
		slog.Warn("cannot embed cover: protected book has no library copy",
			"book_id", logger.SanitizeLogValue(id), "book_title", logger.SanitizeLogValue(book.Title), "protected_path", logger.SanitizeLogValue(book.FilePath))
		return
	}
	release := mfs.lockPath(target.FilePath)
	defer release()
	mfs.embedCoverInBookFiles(target, metadata.CoverPathForBook(config.AppConfig.RootDir, id), policy)
}

// ApplyMetadataFileIO runs the slow file operations after metadata is applied:
// cover embed, file rename and, under auto_write_tags_on_apply, the tag write.
// The cover DOWNLOAD is not here: it runs first, in FinishApplyFileWork, which
// is what apply paths should call. Designed to run in a background goroutine.
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
	defer mfs.lockBookFileWork(id)()
	_, err := mfs.applyMetadataFileIO(id, createLibraryCopy)
	return err
}

// applyMetadataFileIO is ApplyMetadataFileIO that also reports what the
// pipeline did about tags, for FinishApplyFileWork's write-once guard.
func (mfs *Service) applyMetadataFileIO(id string, policy copyPolicy) (tagWriteResult, error) {
	book, err := mfs.db.GetBookByID(id)
	if err != nil {
		return tagWriteResult{}, fmt.Errorf("apply file I/O: load book %s: %w", id, err)
	}
	if book == nil {
		// Reported rather than ignored: the two recovery handlers replay file
		// ops recorded before a restart, and a book that has since been deleted
		// is the one case where this is expected and benign. Naming it lets the
		// caller log which book vanished instead of silently doing nothing.
		return tagWriteResult{}, fmt.Errorf("apply file I/O: book %s not found", id)
	}

	mfs.embedCover(id, book, policy)

	// Run file rename + tag write pipeline
	if config.AppConfig.AutoRenameOnApply || config.AppConfig.AutoWriteTagsOnApply {
		tags, err := mfs.runApplyPipeline(id, book, policy)
		if err != nil {
			return tags, fmt.Errorf("apply file I/O: pipeline for book %s: %w", id, err)
		}
		return tags, nil
	}
	return tagWriteResult{}, nil
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
