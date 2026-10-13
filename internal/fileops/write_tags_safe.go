// file: internal/fileops/write_tags_safe.go
// version: 1.15.0
// guid: b4c5d6e7-f8a9-0b1c-2d3e-4f5a6b7c8d9e
// last-edited: 2026-10-13

package fileops

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/filehash"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// WriteTagsSafeOptions configures WriteTagsSafe behavior.
type WriteTagsSafeOptions struct {
	// BookFileID is the database row ID for hash tracking. Empty = skip DB update.
	BookFileID string
	// Store receives the pre- and post-write hashes. Nil = skip DB update.
	Store database.BookFileHashUpdater
	// Provenance receives an append-only record of this write. Nil = skip.
	//
	// Store overwrites two hash columns and so keeps only the most recent
	// pair; Provenance keeps every pair ever recorded. They are separate
	// fields because the columns are what existing queries read, and the
	// ledger is what makes a hash from before this write still resolvable
	// after it.
	Provenance database.FileProvenanceRecorder
	// Actor names what is performing the write (an op, plugin, or user). It is
	// recorded on the provenance events and is otherwise unused.
	Actor string
	// Detail is a human-readable note about what this write changes, e.g.
	// `author: "" -> "Brandon Sanderson"`. Recorded on the tags_written event.
	Detail string
	// TorrentHash is the Deluge infohash of the release the file came from,
	// normally BookFile.DelugeHash. It identifies the source rather than the
	// bytes, so unlike either SHA it is unchanged by this write — which makes
	// it the most durable link back to a pristine original.
	TorrentHash string
	// KeepBackup leaves the pre-write bytes beside the file as
	// <name>.bak-<unix seconds> (a -N suffix when that name is taken) when the
	// tagged copy is renamed in: the create_backups setting.
	//
	// How: after the tag write on the temp copy has succeeded, the original is
	// HARDLINKED to the backup name, so taking the backup copies no data: the
	// rename then makes the backup the only library name of the old inode.
	// From that moment the backup holds a full file's blocks (the old bytes;
	// the path now names the new ones), so each kept backup costs one file's
	// size on disk until the sweep removes it. When the file is shared with
	// another name (a hardlink-organized download), the old inode lives on
	// under that name anyway and the backup costs nothing more. path exists at
	// every instant (nothing renames the original away). Where a hardlink is
	// refused for any reason, or path is not a regular file (a symlink-strategy
	// library entry, which a link would only duplicate as a symlink), the
	// backup is a full fsynced copy instead (CopyFileExclusive).
	//
	// Nothing here changes the original inode's metadata: a library file can
	// share its inode with a seeding download, and that file's mtime must not
	// move (libtorrent's fast-resume checks it).
	//
	// Failure: a failed tag write leaves no backup; a failed backup fails the
	// write and leaves the original untouched; a failed rename removes the
	// backup again (the original is intact, so the backup adds nothing).
	//
	// AGE CONTRACT. The backup-cleanup sweep (maintenance.cleanup-old-backups,
	// which scheduler.cleanup-old-backups resolves to) ages a .bak-* file by the
	// time in its NAME (BackupNameTime), not by its mtime: a hardlinked backup keeps
	// the original's mtime, which can be years old. Changing the name format here
	// means changing BackupNameTime with it. The sweep walks RootDir only, so a
	// backup beside a file outside the library root is never collected.
	KeepBackup bool
}

// maxBackupNameAttempts bounds the suffix search in keepBackup: a second write
// of the same file within one second must not overwrite the first backup (that
// one holds the older, more original bytes), so the name gets a -1, -2, ...
// suffix instead.
const maxBackupNameAttempts = 100

// linkFile is os.Link and renameFile os.Rename: seams so a test can force the
// copy fallback and a failed rename.
var (
	linkFile   = os.Link
	renameFile = os.Rename
)

// backupInfix separates a file's name from its backup timestamp:
// <name>.bak-<unix seconds>[-N]. Legacy backups (the retired
// write_backup_before switch) used <name>.bak-YYYYMMDD-HHMMSS in local time.
const backupInfix = ".bak-"

// legacyBackupLayout is the time layout of a legacy backup name's suffix.
const legacyBackupLayout = "20060102-150405"

var (
	// legacyBackupSuffix is exactly 8 digits, a dash and 6 digits. It is
	// checked before backupSuffix, which it would otherwise also match (a
	// current name is 10 unix-second digits and a -N of at most 3).
	legacyBackupSuffix = regexp.MustCompile(`^[0-9]{8}-[0-9]{6}$`)
	backupSuffix       = regexp.MustCompile(`^([0-9]+)(?:-[0-9]+)?$`)
	// earliestBackupTime rejects a parsed time no backup can carry, so an
	// unrelated ".bak-1" name is not a backup at all instead of reading as
	// 1970 and being deleted at once.
	earliestBackupTime = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
)

// BackupNameTime reports whether name is a backup this package (or its retired
// predecessor) wrote, and when it was taken. The two recognised names are
// <file>.bak-<unix>[-N] (keepBackup) and the legacy <file>.bak-YYYYMMDD-HHMMSS
// in local time (the retired write_backup_before switch). Every other name,
// including one that merely contains ".bak-", a name with nothing before the
// infix, a time before 2000 and a time in the future, reports false, and the
// backup sweep must then leave the file alone: the match is on the whole name,
// so a book called "x.bak-notes.m4b" is never mistaken for a backup however
// old its mtime. There is no mtime fallback: both recognised names carry
// their time, and a hardlinked backup's mtime is the original audio's (see
// KeepBackup, AGE CONTRACT), so ageing by mtime would delete a fresh backup of
// an old file.
func BackupNameTime(name string) (time.Time, bool) {
	return backupNameTime(name, time.Now())
}

// backupNameTime parses the timestamp after the last ".bak-" in name.
func backupNameTime(name string, now time.Time) (time.Time, bool) {
	i := strings.LastIndex(name, backupInfix)
	if i <= 0 {
		return time.Time{}, false
	}
	suffix := name[i+len(backupInfix):]
	var t time.Time
	switch {
	case legacyBackupSuffix.MatchString(suffix):
		lt, err := time.ParseInLocation(legacyBackupLayout, suffix, time.Local)
		if err != nil {
			return time.Time{}, false
		}
		t = lt
	default:
		m := backupSuffix.FindStringSubmatch(suffix)
		if m == nil {
			return time.Time{}, false
		}
		secs, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			return time.Time{}, false
		}
		t = time.Unix(secs, 0)
	}
	// A minute of slack for clock skew between the writer and the sweep.
	if t.Before(earliestBackupTime) || t.After(now.Add(time.Minute)) {
		return time.Time{}, false
	}
	return t, true
}

// KeepBackupCopy makes a fresh sibling <path>.bak-<unix>[-N] that is a full,
// fsynced COPY of path, and returns its name. It is for writers that rewrite
// path in place (the AtomicParsley/eyeD3/metaflac CLI fallbacks): a hardlink
// there would share the inode the writer is about to change, and the backup
// would end up holding the new bytes.
func KeepBackupCopy(path string) (string, error) {
	name, _, err := keepBackup(path, time.Now(), false)
	return name, err
}

// keepBackup makes a fresh sibling named <path>.bak-<unix> (adding -N when
// that name is taken) holding path's current bytes, and returns its name and
// whether it is a hardlink. With allowLink it hardlinks a regular file and
// copies when the link fails for any reason but a taken name, or when path is
// not a regular file; without it, it always copies (CopyFileExclusive:
// O_EXCL, fsynced file and directory). Both steps are exclusive, so an
// existing backup is never overwritten.
func keepBackup(path string, now time.Time, allowLink bool) (name string, linked bool, err error) {
	base := path + backupInfix + strconv.FormatInt(now.Unix(), 10)
	useLink := allowLink
	if useLink {
		// Lstat, not Stat: os.Link does not follow a symlink on Linux, so a
		// link of a symlink-strategy entry would back up the symlink, not the
		// bytes it points at.
		fi, lerr := os.Lstat(path)
		if lerr != nil {
			return "", false, lerr
		}
		useLink = fi.Mode().IsRegular()
	}
	name = base
	for i := 1; i <= maxBackupNameAttempts; {
		if useLink {
			err = linkFile(path, name)
			if err != nil && !errors.Is(err, fs.ErrExist) {
				// Any refusal (EXDEV, EPERM, EMLINK, EACCES, ENOSYS, an odd
				// network-mount errno) is a reason to copy, not to fail the
				// tag write: the copy is exclusive and safe for every case,
				// and only its own failure fails the write.
				logger.New("fileops").Debug("WriteTagsSafe: hardlink backup refused, copying instead: path=%s error=%v",
					logger.SanitizeLogValue(path), err)
				useLink = false
				continue // same name, by copy
			}
		} else {
			err = CopyFileExclusive(path, name)
		}
		if err == nil {
			return name, useLink, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", false, err
		}
		name = base + "-" + strconv.Itoa(i)
		i++
	}
	return "", false, fmt.Errorf("no free backup name for %s after %d attempts", path, maxBackupNameAttempts)
}

// WriteTagsSafe writes audio metadata tags to path safely:
//  1. Copies the file to a sibling temp file in the same directory
//  2. Calls writeFn(tmpPath) to perform the actual tag write on the copy
//  3. With opts.KeepBackup, hardlinks (or copies) the original to <path>.bak-<unix>
//  4. On success: atomically renames the temp file over the original
//
// When BOTH opts.BookFileID and opts.Store are set it additionally computes
// original_file_hash before the write and post_metadata_hash after it, and
// persists the pair. Those two SHA-256 passes each stream the whole audio file,
// so they are skipped entirely when there is no row to persist them against.
//
// Returns (originalHash, postHash, error); the hashes are "" when not computed.
// On writeFn failure the original file is left untouched and the temp file is
// removed.
//
// writeFn receives a temp copy, so a writer that itself wraps its work in
// WriteTagsSafe would double the copy-and-hash cost. Writers meant to be called
// from here have in-place variants — see metadata.WriteMetadataToFileAtomic (which
// wraps this function around the in-place writer) and tagger.WriteTagsInPlace.
func WriteTagsSafe(path string, writeFn func(tmpPath string) error, opts WriteTagsSafeOptions) (originalHash, postHash string, err error) {
	// The hashes exist solely to be persisted against a book_file row. When no
	// row is supplied there is nothing to persist and every caller discards the
	// return values, so computing them is pure waste — and it is expensive waste:
	// ComputeFileHashAndSize streams the ENTIRE audio file through SHA-256 with no size
	// cap and no mtime/size shortcut, twice per call. On NAS-backed audiobooks
	// that dominated the cost of a tag write.
	// Provenance needs the same two digests the columns do, so either
	// destination is reason enough to compute them. Without this the ledger
	// would silently record empty digests whenever Store was nil.
	wantHashes := (opts.BookFileID != "" && opts.Store != nil) || opts.Provenance != nil
	var originalSize int64

	// Step 1: fingerprint the original file before any modification.
	if wantHashes {
		originalHash, originalSize, err = ComputeFileHashAndSize(path)
		if err != nil {
			return "", "", fmt.Errorf("WriteTagsSafe: hash original %s: %w", path, err)
		}
	}
	// Step 1a: the identity digest of the pre-write bytes, for the row's
	// original_file_hash. That column must hold the SAME kind of digest as
	// file_hash (filehash.BookFileHash, sampled), never originalHash above (a
	// whole-file SHA-256): the two differ for any large file, and a mixed pair
	// reads as "changed outside the app" in the integrity check. Cheap: it reads
	// two fixed-size windows, not the whole file.
	var preIdentityHash string
	if opts.BookFileID != "" && opts.Store != nil {
		if h, herr := filehash.BookFileHash(path); herr == nil {
			preIdentityHash = h
		} else {
			logger.New("fileops").Warn("WriteTagsSafe: pre-write identity hash not computed; original_file_hash left as is: book_file_id=%s path=%s error=%v",
				logger.SanitizeLogValue(opts.BookFileID), logger.SanitizeLogValue(path), herr)
		}
	}

	// Step 1b: record the pre-write state BEFORE touching anything. If the
	// process dies during the write, this row is what survives — recording it
	// afterwards would lose precisely the case the ledger exists for.
	recordEvent(opts, database.FileEventObserved, path, originalHash, originalSize, "", "pre-write")

	// Step 2: create temp file in the same directory so os.Rename is atomic
	// (same filesystem mount). Use the same extension so taglib can detect
	// the container format correctly.
	dir := filepath.Dir(path)
	ext := filepath.Ext(path)
	tmpFile, err := os.CreateTemp(dir, ".writetmp-*"+ext)
	if err != nil {
		return originalHash, "", fmt.Errorf("WriteTagsSafe: create temp: %w", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()

	// Always remove the temp file on failure.
	defer func() {
		if err != nil {
			_ = os.Remove(tmpPath)
		}
	}()

	// Step 3: copy original → temp (preserve permissions).
	if err = CopyFileInto(path, tmpPath); err != nil {
		return originalHash, "", fmt.Errorf("WriteTagsSafe: copy to temp: %w", err)
	}

	// Step 4: let the caller write tags into the temp copy.
	if err = writeFn(tmpPath); err != nil {
		return originalHash, "", fmt.Errorf("WriteTagsSafe: writeFn: %w", err)
	}

	// Step 4a: keep the pre-write bytes beside the file (see KeepBackup). This
	// runs only after writeFn succeeded, so a failed tag write leaves no
	// backup, and it links or copies rather than renaming the original away,
	// so path never stops existing.
	var backupPath string
	if opts.KeepBackup {
		if backupPath, _, err = keepBackup(path, time.Now(), true); err != nil {
			return originalHash, "", fmt.Errorf("WriteTagsSafe: backup original: %w", err)
		}
	}

	// Step 5: atomic rename — old file replaced only on success.
	if err = renameFile(tmpPath, path); err != nil {
		if backupPath != "" {
			// The original is intact and identical to the backup: drop it.
			_ = os.Remove(backupPath)
		}
		return originalHash, "", fmt.Errorf("WriteTagsSafe: rename: %w", err)
	}

	// Step 6: fingerprint the result (only when it will be persisted).
	if wantHashes {
		// Assign to the named return, not a new local: `:=` here would shadow
		// postHash and the function would return an empty hash on success.
		var postSize int64
		postHash, postSize, err = ComputeFileHashAndSize(path)
		if err != nil {
			return originalHash, "", fmt.Errorf("WriteTagsSafe: hash result %s: %w", path, err)
		}

		// Step 7: record the completed write in the append-only ledger.
		recordEvent(opts, database.FileEventTagsWritten, path, postHash, postSize, opts.Detail, "")

		// Step 8: update the two hash columns. The bytes are already on disk, so
		// a failure here cannot fail the write — returning an error would invite
		// the caller to retry and write the file twice. It must not be silent
		// either: this used to be `_ =`, which is how the columns could drift
		// from the files without anyone noticing.
		//
		// FileHash is updated too, with the canonical identity digest the
		// scanner computes (filehash.BookFileHash, head+tail+size), NOT postHash
		// (a whole-file SHA-256). Leaving FileHash on the old bytes made the next
		// rescan see a different hash for the same audio and treat the file as
		// replaced. A hashing failure leaves FileHash as it was: the merge then
		// keeps the audio-derived fields and asks for a re-read.
		if opts.BookFileID != "" && opts.Store != nil {
			identityHash, herr := filehash.BookFileHash(path)
			if herr != nil {
				identityHash = ""
				logger.New("fileops").Warn("WriteTagsSafe: identity hash not computed; file_hash left unchanged: book_file_id=%s path=%s error=%v",
					logger.SanitizeLogValue(opts.BookFileID), logger.SanitizeLogValue(path), herr)
			}
			if uerr := opts.Store.UpdateBookFileHashes(opts.BookFileID, preIdentityHash, postHash, identityHash); uerr != nil {
				logger.New("fileops").Warn("WriteTagsSafe: hash columns not updated; file was written and the ledger holds the record: book_file_id=%s path=%s error=%v",
					logger.SanitizeLogValue(opts.BookFileID), logger.SanitizeLogValue(path), uerr)
			}
		}
	}

	return originalHash, postHash, nil
}

// RecordObserved appends a FileEventObserved event carrying detail for the
// file at path, for callers that observe a file outside a write (the tagger's
// read-back after a tag write).
//
// sha256 and size are the file's whole-file digest as of the observation (what
// ComputeFileHashAndSize returns). The event is keyed on bookFileID when there
// is one; with none, a non-empty sha256 still records it as an orphan
// observation, reachable through FindFileEventsByHash. (AdoptOrphanEvents could
// attach it to a row, but nothing in production calls it, so it stays an
// orphan.) With neither, nothing is recorded: an event naming no row and no
// digest could not be found again. A nil recorder records nothing. Like every
// provenance record it is observational and a failure is logged, not returned.
func RecordObserved(rec database.FileProvenanceRecorder, bookFileID, actor, path, sha256 string, size int64, detail string) {
	recordEvent(WriteTagsSafeOptions{Provenance: rec, BookFileID: bookFileID, Actor: actor},
		database.FileEventObserved, path, sha256, size, detail, "")
}

// recordEvent appends one provenance event, if a recorder is configured.
//
// Provenance is observational: a failure to record must never fail or alter the
// file operation being observed. It is logged rather than returned for that
// reason — but it is logged, because a ledger with silent gaps is worse than no
// ledger, since it reads as authoritative.
func recordEvent(opts WriteTagsSafeOptions, kind database.FileEventKind, path, sha string, size int64, detail, note string) {
	if opts.Provenance == nil {
		return
	}
	if sha == "" && opts.BookFileID == "" {
		// Would be rejected as an unadoptable orphan; skip rather than log noise.
		return
	}
	// The size is passed in rather than looked up here: it comes off the same
	// open handle the hash does, so the two provably describe the same bytes.
	digest := database.FileDigest{
		SHA256Full:  sha,
		SizeBytes:   size,
		TorrentHash: opts.TorrentHash,
	}
	if note != "" && detail == "" {
		detail = note
	}
	ev := database.FileEvent{
		BookFileID: opts.BookFileID,
		Path:       path,
		Kind:       kind,
		At:         time.Now(),
		Digest:     digest,
		Detail:     detail,
		Actor:      opts.Actor,
	}
	if err := opts.Provenance.AppendFileEvent(ev); err != nil {
		slog.Warn("WriteTagsSafe: provenance event not recorded",
			"kind", kind, "path", logger.SanitizeLogValue(path), "book_file_id", opts.BookFileID, "error", err)
	}
}
