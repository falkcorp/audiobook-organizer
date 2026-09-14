// file: internal/remux/transcode.go
// version: 1.6.0
// guid: b2c3d4e5-f6a7-8b9c-0d1e-2f3a4b5c6d7e
// last-edited: 2026-09-14

package remux

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/appdirs"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/diagnosis"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
	taglib "go.senan.xyz/taglib"
)

const TranscodeKey = "malformed_m4b_transcode_v1_done"

// Transcoder provides malformed M4B transcode operations.
type Transcoder struct {
	store     Store
	files     BookFileStore
	protected ProtectedChecker
}

// SetBookFileStore installs the store transcoded files' hashes and audio
// properties are recorded through. Nil disables recording.
func (t *Transcoder) SetBookFileStore(files BookFileStore) { t.files = files }

// SetProtectedChecker installs the protected-path predicate. A protected file
// is never transcoded (a re-encode replaces the file) and is counted as
// skipped_protected. Nil disables the check.
func (t *Transcoder) SetProtectedChecker(c ProtectedChecker) { t.protected = c }

// transcodeAndRecord re-encodes path with transcode, records the new bytes'
// hashes on the file's book_file row, and refreshes the row's codec, bitrate
// and sample rate from probe: a transcode can change all three, and a row
// left with the old values reports the wrong format. A probe value that comes
// back empty (no ffprobe, an unreadable stream) leaves the stored one. The
// fields are set with PatchBookFileFields, under the row's stripe, so a
// concurrent writer's columns are not reverted.
func (t *Transcoder) transcodeAndRecord(ctx context.Context, path string, transcode func(string) error, probe func(context.Context, string) diagnosis.FileDiagnostic) error {
	var rec fileops.BookFileHashRecorder
	if t.files != nil {
		rec = t.files
	}
	if err := fileops.RecordRewrite(rec, path, func() error { return transcode(path) }); err != nil {
		return err
	}
	if t.files == nil {
		return nil
	}
	log := logger.New("remux")
	row, err := t.files.GetBookFileByPath(path)
	if err != nil {
		log.Warn("book_file lookup after transcode failed; codec, bitrate and sample rate left as stored: path=%s error=%v",
			logger.SanitizeLogValue(path), err)
		return nil
	}
	if row == nil {
		return nil
	}
	d := probe(ctx, path)
	var patch database.BookFileFieldPatch
	if d.Codec != "" {
		codec := d.Codec
		patch.Codec = &codec
	}
	if d.BitrateKbps > 0 {
		kbps := d.BitrateKbps
		patch.BitrateKbps = &kbps
	}
	if d.SampleRateHz > 0 {
		hz := d.SampleRateHz
		patch.SampleRateHz = &hz
	}
	if patch.Codec == nil && patch.BitrateKbps == nil && patch.SampleRateHz == nil {
		log.Warn("probe after transcode gave no audio properties; codec, bitrate and sample rate left as stored: path=%s",
			logger.SanitizeLogValue(path))
		return nil
	}
	if _, _, err := t.files.PatchBookFileFields(row.BookID, row.ID, patch); err != nil {
		log.Warn("audio properties not updated after transcode; the file was written: book_file_id=%s path=%s error=%v",
			logger.SanitizeLogValue(row.ID), logger.SanitizeLogValue(path), err)
	}
	return nil
}

// NewTranscoder creates a new Transcoder instance.
func NewTranscoder(store Store) *Transcoder {
	return &Transcoder{store: store}
}

// TranscodeSkipKey returns a settings key that marks a specific file as
// permanently unfixable by transcode, so restarts don't re-attempt it.
func TranscodeSkipKey(path string) string {
	h := sha256.Sum256([]byte(path))
	return fmt.Sprintf("transcode_skip_%x", h[:8])
}

// isTranscodeCandidate reports whether path is an M4B/M4A file this pass
// should consider (used identically by the pre-count pass and the work pass
// so the reported "total" never drifts from what actually gets processed).
func isTranscodeCandidate(path string, d fs.DirEntry) bool {
	if d.IsDir() {
		return false
	}
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".m4b" && ext != ".m4a" {
		return false
	}
	return !strings.Contains(filepath.Base(path), ".tmp.")
}

// TranscodeMalformedFiles walks the library and re-encodes any M4B/M4A
// file that taglib cannot parse even after the remux pass. Full AAC transcode
// at 64 kbps rebuilds the file from scratch, which fixes corruption that a
// stream copy cannot repair. Runs once at startup. The walk checks ctx per
// file and stops early via fs.SkipAll when canceled (SYS-1); a canceled run
// does not write the done flag, so the next startup resumes.
//
// progress is called every 25 processed files (and once more at the end)
// with the running counts, so a caller wired to an op reporter's
// UpdateProgress can surface a live "X/Y" during a multi-hour ffmpeg walk
// instead of a single log line at completion (C2). progress may be nil.
//
// A non-nil error is returned only for fatal setup problems (RootDir not
// configured, ffmpeg missing) — those used to be swallowed as a Warn log,
// letting the op report success while doing nothing. Per-file transcode
// failures are expected (files get marked permanently-unfixable and are
// counted in the progress message) rather than failing the whole run.
func (t *Transcoder) TranscodeMalformedFiles(ctx context.Context, progress func(processed, total int, msg string)) error {
	if t.store == nil {
		return nil
	}

	if setting, err := t.store.GetSetting(TranscodeKey); err == nil && setting != nil && setting.Value == "true" {
		slog.Info("Malformed M4B transcode already completed, skipping")
		return nil
	}

	root := config.AppConfig.RootDir
	if root == "" {
		return fmt.Errorf("TranscodeMalformedFiles: RootDir not configured")
	}

	// Both walks below MUST agree on what they skip: the first is the progress
	// denominator for the second. A subtree counted but not processed makes
	// the bar unable to reach 100%; one processed but not counted overshoots.
	app := appdirs.Current()

	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return fmt.Errorf("TranscodeMalformedFiles: ffmpeg not found: %w", err)
	}
	if !protectedListLoaded(t.protected) {
		return fmt.Errorf("TranscodeMalformedFiles: %w", errProtectedListNotLoaded)
	}

	// Pre-mark files confirmed permanently unfixable by full transcode.
	// These produce valid ffmpeg output but taglib still cannot parse them.
	permanentlyUnfixable := []string{
		"/mnt/bigdata/books/audiobook-organizer/David Petrie/Necrotic Apocalypse (7 book series)/Necrotic Apocalypse (7 book series)/Necrotic Apocalypse (7 book series) - David Petrie - read by narrator.m4b",
		"/mnt/bigdata/books/audiobook-organizer/Eric Ugland/One More Last Time_ A LitRPG/GameLit Novel (The Good Guys/One More Last Time_ A LitRPG/GameLit Novel (The Good Guys, Book 1)/One More Last Time_ A LitRPG/GameLit Novel (The Good Guys, Book 1) - Eric Ugland - read by narrator.m4b",
	}
	for _, p := range permanentlyUnfixable {
		k := TranscodeSkipKey(p)
		if skip, _ := t.store.GetSetting(k); skip == nil {
			_ = t.store.SetSetting(k, "true", "bool", false)
			slog.Info("malformed M4B transcode pre-marked permanently unfixable", "path", p)
		}
	}

	// Pre-count candidates so progress can report an accurate "X/Y" instead
	// of an unbounded counter. Cheap relative to the ffmpeg work itself —
	// this pass does no file content reads, just a directory walk.
	total := 0
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		select {
		case <-ctx.Done():
			return fs.SkipAll
		default:
		}
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			// The library root holds the application's own state as well
			// as books -- a backup directory of multi-GB archives and an
			// OpenLibrary dump directory containing an embedded database
			// of ~1,200 files. Neither has a leading dot by default, so
			// nothing excluded them before. This pass opens and REWRITES
			// files it accepts, so descending is not merely wasted I/O.
			if pathutil.ShouldSkipDir(root, path, app) {
				return fs.SkipDir
			}
			return nil
		}
		if isTranscodeCandidate(path, d) {
			total++
		}
		return nil
	})

	slog.Info("Starting malformed M4B transcode scan under", "root", root, "candidates", total)
	// skipped counts files marked permanently unfixable; skippedProtected
	// counts protected files, which are never rewritten. Two meanings, two
	// counters.
	transcoded, clean, failed, skipped, skippedProtected, processed := 0, 0, 0, 0, 0, 0
	log := logger.New("remux")

	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		// Stop the walk cleanly on shutdown (SYS-1). fs.SkipAll ends WalkDir
		// without an error; WalkDir's return is discarded above by design.
		select {
		case <-ctx.Done():
			return fs.SkipAll
		default:
		}
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			// The library root holds the application's own state as well
			// as books -- a backup directory of multi-GB archives and an
			// OpenLibrary dump directory containing an embedded database
			// of ~1,200 files. Neither has a leading dot by default, so
			// nothing excluded them before. This pass opens and REWRITES
			// files it accepts, so descending is not merely wasted I/O.
			if pathutil.ShouldSkipDir(root, path, app) {
				return fs.SkipDir
			}
			return nil
		}
		if !isTranscodeCandidate(path, d) {
			return nil
		}

		processed++
		if progress != nil && processed%25 == 0 {
			progress(processed, total, fmt.Sprintf("Transcoding M4B: %d/%d (transcoded=%d failed=%d skipped=%d skipped_protected=%d)", processed, total, transcoded, failed, skipped, skippedProtected))
		}

		// Counted by both walks, skipped here after processed++ so they agree.
		// A protected file is never probed or re-encoded.
		if isProtected(t.protected, path) {
			log.Info("malformed M4B transcode: skipping protected file %s", logger.SanitizeLogValue(path))
			skippedProtected++
			return nil
		}

		if _, err := taglib.ReadTags(path); err == nil {
			clean++
			return nil
		}

		// Skip files that have already been confirmed permanently unfixable.
		if skip, err := t.store.GetSetting(TranscodeSkipKey(path)); err == nil && skip != nil && skip.Value == "true" {
			slog.Info("malformed M4B transcode skipping known-unfixable", "path", path)
			skipped++
			failed++
			return nil
		}

		// taglib failed — attempt full AAC transcode.
		if err := t.transcodeAndRecord(ctx, path, TranscodeFile, diagnosis.ProbeFile); err != nil {
			slog.Warn("malformed M4B transcode failed for", "path", path, "err", err)
			_ = t.store.SetSetting(TranscodeSkipKey(path), "true", "bool", false)
			failed++
			return nil
		}

		// Verify the output is now readable.
		if _, err := taglib.ReadTags(path); err != nil {
			slog.Warn("malformed M4B transcode produced unreadable file for", "path", path, "err", err)
			_ = t.store.SetSetting(TranscodeSkipKey(path), "true", "bool", false)
			failed++
			return nil
		}

		slog.Info("malformed M4B transcoded", "path", path)
		transcoded++
		return nil
	})

	if progress != nil {
		progress(processed, total, fmt.Sprintf("Transcoding M4B: %d/%d (transcoded=%d failed=%d skipped=%d skipped_protected=%d)", processed, total, transcoded, failed, skipped, skippedProtected))
	}

	log.Info("Malformed M4B transcode complete: transcoded=%d clean=%d failed=%d skipped=%d skipped_protected=%d", transcoded, clean, failed, skipped, skippedProtected)
	// Same rule as the remux pass: not done after a cancel or a protected skip.
	if why := notDoneReason(ctx, skippedProtected); why != "" {
		log.Info("Malformed M4B transcode not marked done (%s); it runs again next time", why)
		return nil
	}
	_ = t.store.SetSetting(TranscodeKey, "true", "bool", false)
	return nil
}

// TranscodeFile re-encodes an M4B/M4A file to 64 kbps AAC in-place.
// Writes to a temp file first, then atomically renames over the original.
func TranscodeFile(path string) error {
	tmp := path + ".remux.tmp"
	defer os.Remove(tmp)

	cmd := exec.Command("ffmpeg",
		"-nostdin", "-loglevel", "error", "-y",
		"-i", path,
		"-vn",
		"-c:a", "aac", "-b:a", "64k",
		"-map_metadata", "0",
		"-map_chapters", "0",
		"-f", "mp4",
		tmp,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg: %w — %s", err, strings.TrimSpace(string(out)))
	}

	return os.Rename(tmp, path)
}
