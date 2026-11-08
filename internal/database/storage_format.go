// file: internal/database/storage_format.go
// version: 2.1.0
// guid: 9a2abfc6-3471-4cb1-8ed7-2efe634af689
// last-edited: 2026-11-08

package database

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
)

// Storage format: books' legacy projection (05-storage §5S.3.7).
//
// From release R1 core storage opens the main store and its guard decides
// whether this build may open it: the per-owner level stamps and the
// .storage-levels sidecar, and for books the legacy projection's sidecar
// (<db>.storage-format, the guard's only input for it). The projection is
// what HEAD (R0) reads: the preference:storage_format stamp row, the
// preference:storage_migration marker and that sidecar. A core build changes
// the rows only through books' LegacyGuard.Stage (booksspec.go,
// stageProjection) when a level step moves the projection, which no step
// does at R1, and rewrites the sidecar from its supported vector at every
// boot. The one other writer is stampStorageFormat, HEAD's
// ensureStorageFormatStamp: it materializes an ABSENT stamp at format 1 and
// never rewrites a present one, so it changes no reader's decision (an
// absent stamp on a store with data already reads as 1 to HEAD). It runs in
// the open-time repairs, after the boot's pre-migration checkpoint (HEAD ran
// it inside its open, before any checkpoint existed), so a rollback's
// checkpoint is the store HEAD left. So an R1 store reads, to R0, as format
// 1: the stamp says 1 and the sidecar says 1. `make rollback` reads the
// sidecar and the previous binary's --print-storage-format output.
//
// What is left here: the format constants, the reserved preference keys, the
// stamp's absent-only writer, the sidecar's name and its atomic writer
// (storage_format_repair.go's recovery command uses both).
//
// Exempt by design: the raw mode of cmd/diagnostics (read-only) and
// cmd/pebble-inject-skip. Both open Pebble directly and never reach the
// guard, so they keep working on a store the guard refuses.

// SupportedStorageFormat is the storage format this build reads and writes.
//
// 1 = the format before the storage-efficiency program (book_ver full copies,
// inline file signals). Raise it only in the release whose converter changes
// the format. The converter raises the stamp, and the sidecar, to the target
// value before its first legacy delete, not at the end (design 9, step 2b), so
// a half-converted store is already too new for the previous build.
const SupportedStorageFormat = 1

// buildStorageFormat is what the recovery command compares against. It is a
// variable only so a test can pretend the build supports a newer format.
var buildStorageFormat = SupportedStorageFormat

// legacyStorageFormat is the format of a store that has data and no stamp:
// every store written before the stamp existed.
const legacyStorageFormat = 1

// PebbleFormatMajorVersion is the Pebble on-disk format the main store is
// ratcheted to. Core storage pins the same format (core/internal/pebble) and
// opens with no format version, so a refused open never ratchets anything,
// and ratchets only after the boot's checkpoint (05-storage §5S.4). The read-only recovery opens (diagnostics query --raw,
// diagnostics reserved-prefs, cmd/pebble-inject-skip) pass no format version
// at all, so they never ratchet. The AI-scan and
// OpenLibrary stores (ai_scan_store.go, internal/openlibrary/store.go) still
// pass pebble.FormatNewest until TASK-A7 moves them onto this constant.
//
// Never pass pebble.FormatNewest in new code: pebble.Open ratchets the on-disk
// format up to the requested version and never back, so a dependency bump of
// pebble would silently raise the format and make a pre-migration checkpoint
// unopenable by the previous build. Raising this constant is a release of its
// own, listed as a format change in its rollback notes, and it must also raise
// SupportedStorageFormat: the previous binary cannot open a store at the new
// Pebble format, so `make rollback` has to refuse it exactly as it refuses a
// storage format change. On pebble v2.1.7 FormatValueSeparation equals
// FormatNewest, so pinning it changed nothing on disk.
const PebbleFormatMajorVersion = pebble.FormatValueSeparation

const (
	// storageFormatPreferenceKey holds the stamp, as a UserPreference whose
	// value is a DatabaseVersion JSON document (the same shape as db_version).
	storageFormatPreferenceKey = "storage_format"
	// storageMigrationPreferenceKey is the projection's migration marker,
	// set by a books Breaking level raise (none at R1) so that HEAD's serve
	// mode refuses a half-converted store.
	storageMigrationPreferenceKey = "storage_migration"
)

// ErrReservedPreferenceKey is returned by PebbleStore.SetUserPreference and
// DeleteUserPreference for storage_format and storage_migration. Those two
// rows decide whether HEAD opens the store at all, so only books' projection
// writer (LegacyGuard.Stage) and the `diagnostics reserved-prefs` recovery
// command may change them. A bad value written through the generic
// preference API would make a rollback to HEAD refuse.
var ErrReservedPreferenceKey = errors.New("reserved preference key")

// isStoreGuardedPreferenceKey reports the keys the store itself refuses to
// set or delete through the generic preference API.
func isStoreGuardedPreferenceKey(key string) bool {
	return key == storageFormatPreferenceKey || key == storageMigrationPreferenceKey
}

// IsReservedPreferenceKey reports whether key is bookkeeping the app owns and
// a client must never set or delete: the storage format stamp and migration
// marker, db_version, and the migration_<n> records. The store blocks only the
// first two (books' VersionSource writes the others, in a migration's
// bookkeeping batch); the /preferences/:key handler rejects all of them.
func IsReservedPreferenceKey(key string) bool {
	return isStoreGuardedPreferenceKey(key) ||
		key == dbVersionPreferenceKey ||
		strings.HasPrefix(key, "migration_")
}

// stampStorageFormat writes preference:storage_format at legacyStorageFormat
// (1, the projection of a store no books level step has raised) when the row
// is absent, in HEAD's row format with an ID from counter:preference; a
// present stamp is left alone. It writes nothing while the storage_migration
// marker is set: a marker means a level step's conversion is unfinished, and
// only Stage may write the projection then. A raised store always carries
// the stamp Stage wrote, and its sidecar (rewritten every boot) says the
// raised value, which is what `make rollback` reads. Kept from HEAD so every
// store stays self-describing in its own rows, as HEAD's stores are: a
// checkpoint or backup archive carries its format even without the sidecar,
// which lives outside it. Callers run it after the boot's checkpoint
// (runOpenRepairs).
func (p *PebbleStore) stampStorageFormat() error {
	for _, k := range []string{storageFormatPreferenceKey, storageMigrationPreferenceKey} {
		_, closer, err := p.db.Get([]byte("preference:" + k))
		switch {
		case err == nil:
			_ = closer.Close()
			return nil
		case !errors.Is(err, pebble.ErrNotFound):
			return fmt.Errorf("read preference %q: %w", k, err)
		}
	}
	payload, err := databaseVersionPayload(legacyStorageFormat)
	if err != nil {
		return fmt.Errorf("encode storage format stamp: %w", err)
	}
	id, err := p.nextID("preference")
	if err != nil {
		return fmt.Errorf("allocate the storage format stamp's preference id: %w", err)
	}
	row, err := json.Marshal(UserPreference{ID: id, Key: storageFormatPreferenceKey, Value: &payload, UpdatedAt: time.Now()})
	if err != nil {
		return fmt.Errorf("encode storage format stamp: %w", err)
	}
	if err := p.db.Set([]byte("preference:"+storageFormatPreferenceKey), row, pebble.Sync); err != nil {
		return fmt.Errorf("write storage format stamp: %w", err)
	}
	return nil
}

// StorageFormatSidecarSuffix is appended to the store path to name the sidecar
// file. The sidecar is a sibling of the store directory (Pebble owns the
// directory itself) and holds one line with one integer.
const StorageFormatSidecarSuffix = ".storage-format"

// StorageFormatSidecarPath returns the sidecar path for a store at dbPath, for
// example /data/audiobooks.pebble.storage-format.
func StorageFormatSidecarPath(dbPath string) string {
	return filepath.Clean(dbPath) + StorageFormatSidecarSuffix
}

// StorageMigrationCheckpointSuffix is appended to the store path to name the
// checkpoint record core storage writes for an upgrade attempt (05-storage
// §5S.3.7, "Checkpoint record"); `make rollback-restore` reads it.
const StorageMigrationCheckpointSuffix = ".migration-checkpoint"

// StorageMigrationCheckpointPath returns the checkpoint-record path for a
// store at dbPath.
func StorageMigrationCheckpointPath(dbPath string) string {
	return filepath.Clean(dbPath) + StorageMigrationCheckpointSuffix
}

// writeStorageFormatSidecar writes the sidecar atomically: a temp file in the
// same directory, synced, renamed over the sidecar, then the directory synced
// so the rename itself is durable. A failure removes the temp file. It uses
// the store's own vfs.FS, so in-memory test stores never touch the real disk.
func writeStorageFormatSidecar(fs vfs.FS, dbPath string, stamp int) (err error) {
	name := StorageFormatSidecarPath(dbPath)
	tmp := name + ".tmp"
	renamed := false
	defer func() {
		if err != nil && !renamed {
			if rmErr := fs.Remove(tmp); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
				err = fmt.Errorf("%w (and removing %s failed: %v)", err, tmp, rmErr)
			}
		}
	}()
	f, err := fs.Create(tmp, vfs.WriteCategoryUnspecified)
	if err != nil {
		return fmt.Errorf("create %s: %w", tmp, err)
	}
	if _, err := f.Write([]byte(fmt.Sprintf("%d\n", stamp))); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmp, err)
	}
	if err := fs.Rename(tmp, name); err != nil {
		return fmt.Errorf("rename %s to %s: %w", tmp, name, err)
	}
	renamed = true
	dir, err := fs.OpenDir(filepath.Dir(name))
	if err != nil {
		return fmt.Errorf("open %s to sync the rename: %w", filepath.Dir(name), err)
	}
	if err := dir.Sync(); err != nil {
		_ = dir.Close()
		return fmt.Errorf("sync %s after the rename: %w", filepath.Dir(name), err)
	}
	if err := dir.Close(); err != nil {
		return fmt.Errorf("close %s: %w", filepath.Dir(name), err)
	}
	return nil
}
