// file: internal/database/storage_format.go
// version: 1.0.0
// guid: 9a2abfc6-3471-4cb1-8ed7-2efe634af689
// last-edited: 2026-10-04

package database

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// storageFormatLog is the subsystem logger for the storage format guard
// (used by the open and init phases in pebble_store.go).
var storageFormatLog = logger.New("database.storage-format")

// Storage format guard (storage-efficiency design, section 9).
//
// The main Pebble store carries an integer storage_format stamp, mirrored in a
// sidecar file beside the store directory. Every open compares both against
// the format this build supports and refuses a store that is newer
// (StorageFormatTooNewError) or older / mid-migration
// (StorageMigrationRequiredError). `make rollback` reads the sidecar and the
// previous binary's --print-storage-format output, and refuses a binary swap
// that would go back past a format change.
//
// Exempt by design: the raw mode of cmd/diagnostics (read-only) and
// cmd/pebble-inject-skip. Both open Pebble directly and never reach this
// guard, so they keep working on a store the guard refuses.

// SupportedStorageFormat is the storage format this build reads and writes.
//
// 1 = the format before the storage-efficiency program (book_ver full copies,
// inline file signals). Raise it only in the release whose converter changes
// the format. The converter raises the stamp, and the sidecar, to the target
// value before its first legacy delete, not at the end (design 9, step 2b), so
// a half-converted store is already too new for the previous build.
const SupportedStorageFormat = 1

// buildStorageFormat is what every comparison in this file uses. It is a
// variable only so a test can pretend the build supports a newer format.
var buildStorageFormat = SupportedStorageFormat

// legacyStorageFormat is the format of a store that has data and no stamp:
// every store written before the stamp existed.
const legacyStorageFormat = 1

// PebbleFormatMajorVersion is the Pebble on-disk format the main store is
// ratcheted to. The main store's open phase opens with no format version (so a
// refused open never ratchets anything) and the init phase then ratchets to
// this value. The read-only recovery opens (diagnostics query --raw,
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
	// storageMigrationPreferenceKey is the in-progress marker the release B
	// cut-over writes. This build only checks whether it exists.
	storageMigrationPreferenceKey = "storage_migration"
)

// ErrReservedPreferenceKey is returned by PebbleStore.SetUserPreference and
// DeleteUserPreference for storage_format and storage_migration. Those two
// rows decide whether the store opens at all, so only the stamp writer
// (setPreferencesAtomic's raw batch), the release B cut-over and the
// `diagnostics reserved-prefs` recovery command may change them. A bad value
// written through the generic preference API would make the next boot refuse.
var ErrReservedPreferenceKey = errors.New("reserved preference key")

// isStoreGuardedPreferenceKey reports the keys the store itself refuses to
// set or delete through the generic preference API.
func isStoreGuardedPreferenceKey(key string) bool {
	return key == storageFormatPreferenceKey || key == storageMigrationPreferenceKey
}

// IsReservedPreferenceKey reports whether key is bookkeeping the app owns and
// a client must never set or delete: the storage format stamp and migration
// marker, db_version, and the migration_<n> records. The store blocks only the
// first two (migrations write the others through SetUserPreference); the
// /preferences/:key handler rejects all of them.
func IsReservedPreferenceKey(key string) bool {
	return isStoreGuardedPreferenceKey(key) ||
		key == dbVersionPreferenceKey ||
		strings.HasPrefix(key, "migration_")
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
// file in which a storage cut-over records its checkpoint_dir (design 9, step
// 2b). Release A only defines the path; `make rollback` reads it.
const StorageMigrationCheckpointSuffix = ".migration-checkpoint"

// StorageMigrationCheckpointPath returns the checkpoint-record path for a
// store at dbPath.
func StorageMigrationCheckpointPath(dbPath string) string {
	return filepath.Clean(dbPath) + StorageMigrationCheckpointSuffix
}

// StorageFormatTooNewError is returned when the store's stamp, or its sidecar,
// names a format above the one this build supports.
type StorageFormatTooNewError struct {
	Path      string
	Source    string // "stamp" or "sidecar"
	Stamp     int
	Supported int
}

func (e *StorageFormatTooNewError) Error() string {
	return fmt.Sprintf("storage format %d (%s) in %s is newer than this build supports (%d); refusing to open. "+
		"Restore the pre-migration checkpoint and the binary that match this store: "+
		"docs/system/runbooks.md#storage-format-restore",
		e.Stamp, e.Source, e.Path, e.Supported)
}

// StorageMigrationRequiredError is returned by a serve-mode open when the
// store is older than this build, or a storage migration marker is present.
type StorageMigrationRequiredError struct {
	Path          string
	Stamp         int
	Supported     int
	MarkerPresent bool
}

func (e *StorageMigrationRequiredError) Error() string {
	if e.MarkerPresent {
		return fmt.Sprintf("storage format %d in %s carries the storage_migration marker: a storage cut-over "+
			"to format %d did not finish. Do not start serve on it; either restore the pre-migration checkpoint "+
			"named in the marker (docs/system/runbooks.md#storage-format-restore) or re-run the cut-over mode "+
			"that resumes it", e.Stamp, e.Path, e.Supported)
	}
	return fmt.Sprintf("storage format %d in %s needs migration to %d; start serve to migrate",
		e.Stamp, e.Path, e.Supported)
}

// storageFormatState is what the open phase learned about the store, before
// anything was written. The init phase uses it to decide which stamp to write.
type storageFormatState struct {
	stamp          int  // the stamp read from the store; 0 when absent
	stampPresent   bool // preference:storage_format exists
	effective      int  // stamp, else legacy (data) or build (empty)
	emptyAtOpen    bool // the store held no keys at all when opened
	markerPresent  bool // preference:storage_migration exists
	sidecar        int  // the parsed sidecar value; 0 when absent or unreadable
	sidecarPresent bool // the sidecar parsed as one integer
}

// readStorageFormatStamp reads preference:storage_format straight from the
// raw DB. A value that does not decode is an error, never "absent": treating a
// corrupt stamp as missing would let the open restamp a store it cannot read.
func readStorageFormatStamp(db *pebble.DB) (stamp int, present bool, err error) {
	key := []byte("preference:" + storageFormatPreferenceKey)
	value, closer, err := db.Get(key)
	if errors.Is(err, pebble.ErrNotFound) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("read storage format stamp: %w", err)
	}
	defer func() { _ = closer.Close() }()

	var pref UserPreference
	if err := json.Unmarshal(value, &pref); err != nil {
		return 0, false, fmt.Errorf("decode storage format stamp %q: %w", key, err)
	}
	if pref.Value == nil || *pref.Value == "" {
		return 0, false, fmt.Errorf("decode storage format stamp %q: empty value", key)
	}
	var v DatabaseVersion
	if err := json.Unmarshal([]byte(*pref.Value), &v); err != nil {
		return 0, false, fmt.Errorf("decode storage format stamp %q value: %w", key, err)
	}
	if v.Version < 1 {
		return 0, false, fmt.Errorf("decode storage format stamp %q: invalid version %d", key, v.Version)
	}
	return v.Version, true, nil
}

// storageMigrationMarkerPresent reports whether preference:storage_migration
// exists. Its content is release B's business; only presence matters here.
func storageMigrationMarkerPresent(db *pebble.DB) (bool, error) {
	_, closer, err := db.Get([]byte("preference:" + storageMigrationPreferenceKey))
	if errors.Is(err, pebble.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read storage migration marker: %w", err)
	}
	if err := closer.Close(); err != nil {
		return false, fmt.Errorf("close storage migration marker read: %w", err)
	}
	return true, nil
}

// storeIsEmpty reports whether the store holds no keys at all.
func storeIsEmpty(db *pebble.DB) (bool, error) {
	iter, err := db.NewIter(nil)
	if err != nil {
		return false, fmt.Errorf("open emptiness iterator: %w", err)
	}
	hasKey := iter.First()
	iterErr := iter.Error()
	closeErr := iter.Close()
	if iterErr != nil {
		return false, fmt.Errorf("emptiness check: %w", iterErr)
	}
	if closeErr != nil {
		return false, fmt.Errorf("close emptiness iterator: %w", closeErr)
	}
	return !hasKey, nil
}

// readStorageFormatSidecar reads the sidecar beside the store. A missing file
// is (0, false, nil). Content that is not one integer is returned as an error;
// the caller logs it and carries on, and the init phase rewrites the file. A
// sidecar that parses is authoritative for the "too new" check.
func readStorageFormatSidecar(fs vfs.FS, dbPath string) (stamp int, present bool, err error) {
	name := StorageFormatSidecarPath(dbPath)
	f, err := fs.Open(name)
	if errors.Is(err, os.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("open storage format sidecar %s: %w", name, err)
	}
	data, readErr := io.ReadAll(io.LimitReader(f, 64))
	closeErr := f.Close()
	if readErr != nil {
		return 0, false, fmt.Errorf("read storage format sidecar %s: %w", name, readErr)
	}
	if closeErr != nil {
		return 0, false, fmt.Errorf("close storage format sidecar %s: %w", name, closeErr)
	}
	text := strings.TrimSpace(string(data))
	n, err := strconv.Atoi(text)
	if err != nil || n < 1 {
		return 0, false, fmt.Errorf("storage format sidecar %s holds %q, not a positive integer", name, text)
	}
	return n, true, nil
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

// ensureStorageFormatStamp makes sure the store carries a stamp and returns
// it. A present stamp is returned as is. An absent one is written as the
// build's format when the store was empty at open, else as the legacy format
// (the open phase already refused that case unless legacy equals the build's
// format).
func (p *PebbleStore) ensureStorageFormatStamp(emptyAtOpen bool) (int, error) {
	stamp, present, err := readStorageFormatStamp(p.db)
	if err != nil {
		return 0, err
	}
	if present {
		return stamp, nil
	}
	n := legacyStorageFormat
	if emptyAtOpen {
		n = buildStorageFormat
	}
	payload, err := databaseVersionPayload(n)
	if err != nil {
		return 0, fmt.Errorf("encode storage format stamp: %w", err)
	}
	if err := p.setPreferencesAtomic([]preferenceWrite{{Key: storageFormatPreferenceKey, Value: payload}}); err != nil {
		return 0, fmt.Errorf("write storage format stamp: %w", err)
	}
	return n, nil
}

// storageFormatStampRecord encodes the stamp row exactly as the preference
// layer stores it: a UserPreference whose value is a DatabaseVersion. Used
// where the row is staged in a raw batch (Reset, the recovery command).
func storageFormatStampRecord(id, n int) ([]byte, error) {
	payload, err := databaseVersionPayload(n)
	if err != nil {
		return nil, fmt.Errorf("encode storage format stamp: %w", err)
	}
	data, err := json.Marshal(UserPreference{
		ID:        id,
		Key:       storageFormatPreferenceKey,
		Value:     &payload,
		UpdatedAt: time.Now(),
	})
	if err != nil {
		return nil, fmt.Errorf("encode storage format stamp row: %w", err)
	}
	return data, nil
}
