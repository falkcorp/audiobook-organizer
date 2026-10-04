// file: internal/database/storage_format_repair.go
// version: 1.1.0
// guid: 5c23ebb3-ad3d-4ea8-ae2e-8b1876ac7ef8
// last-edited: 2026-10-04

package database

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
)

// Recovery for the reserved preference rows (`diagnostics reserved-prefs`).
//
// storage_format and storage_migration decide whether the store opens at all,
// and db_version / migration_<n> decide what RunMigrations does. A bad value in
// any of them makes the next boot refuse or misbehave, and the store's own
// preference API refuses to touch the first two (ErrReservedPreferenceKey). These
// functions open Pebble directly, bypassing the storage-format guard, so they
// work on exactly the store the guard refuses. They pass no Pebble format
// version, so they never ratchet the on-disk format.

// ReservedPreferenceRow is one reserved row as stored, undecoded.
type ReservedPreferenceRow struct {
	Key string
	Raw string
}

// ReservedPreferenceChange records what RepairReservedPreference changed.
// Before/After are the raw stored values; "" with the matching *Present false
// means the row (or sidecar, or counter) was absent. Committed reports whether
// the Pebble batch was durably committed: when RepairReservedPreference
// returns an error with Committed true, the row change HAS happened and the
// caller must still report it.
type ReservedPreferenceChange struct {
	Key                  string
	Before, After        string
	BeforePresent        bool
	AfterPresent         bool
	Committed            bool
	CounterTouched       bool // counter:preference was bumped to allocate the row's ID
	CounterBefore        string
	CounterAfter         string
	SidecarPath          string
	SidecarBefore        string
	SidecarAfter         string
	SidecarBeforePresent bool
	SidecarAfterPresent  bool
	SidecarTouched       bool
}

// LatestMigrationVersion is the highest version in the migration registry.
// `diagnostics reserved-prefs --set db_version=N` refuses anything above it:
// a db_version past the last migration makes RunMigrations skip every
// migration up to it, silently.
func LatestMigrationVersion() int {
	latest := 0
	for _, m := range migrations {
		if m.Version > latest {
			latest = m.Version
		}
	}
	return latest
}

// closeRepairDB closes the store a repair opened. A variable only so a test
// can make the close fail after the batch committed.
var closeRepairDB = func(db *pebble.DB) error { return db.Close() }

// ListReservedPreferences returns every reserved row in the store at dbPath,
// plus the sidecar's raw content (sidecarPresent false when the file is absent).
func ListReservedPreferences(dbPath string) (rows []ReservedPreferenceRow, sidecar string, sidecarPresent bool, err error) {
	return listReservedPreferences(dbPath, vfs.Default)
}

func listReservedPreferences(dbPath string, fs vfs.FS) (rows []ReservedPreferenceRow, sidecar string, sidecarPresent bool, err error) {
	db, err := openRawForRepair(dbPath, fs)
	if err != nil {
		return nil, "", false, err
	}
	defer func() {
		if closeErr := db.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close %s: %w", dbPath, closeErr)
		}
	}()
	iter, err := db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("preference:"),
		UpperBound: []byte("preference;"),
	})
	if err != nil {
		return nil, "", false, fmt.Errorf("iterate preferences: %w", err)
	}
	for ok := iter.First(); ok; ok = iter.Next() {
		key := strings.TrimPrefix(string(iter.Key()), "preference:")
		if IsReservedPreferenceKey(key) {
			rows = append(rows, ReservedPreferenceRow{Key: key, Raw: string(iter.Value())})
		}
	}
	iterErr := iter.Error()
	if closeErr := iter.Close(); iterErr == nil {
		iterErr = closeErr
	}
	if iterErr != nil {
		return nil, "", false, fmt.Errorf("iterate preferences: %w", iterErr)
	}
	// An unreadable sidecar must not hide the rows: listing is how an
	// operator diagnoses exactly this kind of breakage.
	sidecar, sidecarPresent, scErr := readSidecarRaw(fs, dbPath)
	if scErr != nil {
		return rows, fmt.Sprintf("<unreadable: %v>", scErr), true, nil
	}
	return rows, sidecar, sidecarPresent, nil
}

// RepairReservedPreference deletes (setVersion == 0) or rewrites (setVersion >=
// 1) one reserved row in the store at dbPath. Only storage_format and
// db_version can be rewritten, as a valid DatabaseVersion row; storage_format
// can only be set to a format this build supports. Changing storage_format
// also brings the sidecar in line: a delete removes it (the next open rewrites
// it from the restamped store), a rewrite writes the new value.
func RepairReservedPreference(dbPath, key string, setVersion int) (ReservedPreferenceChange, error) {
	return repairReservedPreference(dbPath, vfs.Default, key, setVersion)
}

func repairReservedPreference(dbPath string, fs vfs.FS, key string, setVersion int) (change ReservedPreferenceChange, err error) {
	change = ReservedPreferenceChange{Key: key}
	if !IsReservedPreferenceKey(key) {
		return change, fmt.Errorf("%q is not a reserved preference key (storage_format, storage_migration, db_version, migration_<n>)", key)
	}
	switch {
	case setVersion < 0:
		return change, fmt.Errorf("version must be >= 1, got %d", setVersion)
	case setVersion > 0 && key != storageFormatPreferenceKey && key != dbVersionPreferenceKey:
		return change, fmt.Errorf("only storage_format and db_version can be rewritten; delete %q instead", key)
	case setVersion > 0 && key == storageFormatPreferenceKey && setVersion > buildStorageFormat:
		return change, fmt.Errorf("storage_format %d is newer than this build supports (%d)", setVersion, buildStorageFormat)
	case setVersion > 0 && key == dbVersionPreferenceKey && setVersion > LatestMigrationVersion():
		return change, fmt.Errorf("db_version %d is above the highest registered migration (%d); RunMigrations would silently skip every migration up to it",
			setVersion, LatestMigrationVersion())
	case setVersion == 0 && key == storageFormatPreferenceKey && buildStorageFormat > legacyStorageFormat:
		// Without a stamp, a store with data reads as the legacy format, so
		// deleting a converted store's stamp would make it look unconverted.
		return change, fmt.Errorf("refusing to delete storage_format: this build supports format %d, and a store without a stamp reads as legacy format %d. "+
			"Use --set storage_format=N with the store's real format instead", buildStorageFormat, legacyStorageFormat)
	}

	db, err := openRawForRepair(dbPath, fs)
	if err != nil {
		return change, err
	}
	defer func() {
		if closeErr := closeRepairDB(db); closeErr != nil && err == nil {
			err = fmt.Errorf("close %s: %w", dbPath, closeErr)
		}
	}()

	// Everything that can fail is read and staged BEFORE the commit, so an
	// error before the commit means nothing changed.
	dbKey := []byte("preference:" + key)
	before, err := rawGet(db, dbKey)
	if err != nil {
		return change, err
	}
	change.Before, change.BeforePresent = before.value, before.present
	if setVersion == 0 && !before.present {
		return change, fmt.Errorf("preference %q is not present; nothing changed", key)
	}
	if key == storageFormatPreferenceKey {
		change.SidecarPath = StorageFormatSidecarPath(dbPath)
		change.SidecarBefore, change.SidecarBeforePresent, err = readSidecarRaw(fs, dbPath)
		if err != nil {
			return change, fmt.Errorf("nothing changed: %w", err)
		}
	}

	batch := db.NewBatch()
	defer func() { _ = batch.Close() }()
	if setVersion == 0 {
		if err := batch.Delete(dbKey, nil); err != nil {
			return change, fmt.Errorf("stage delete of %q: %w", key, err)
		}
	} else {
		id, err := repairPreferenceID(db, batch, before, &change)
		if err != nil {
			return change, err
		}
		payload, err := databaseVersionPayload(setVersion)
		if err != nil {
			return change, err
		}
		row, err := json.Marshal(UserPreference{ID: id, Key: key, Value: &payload, UpdatedAt: time.Now()})
		if err != nil {
			return change, fmt.Errorf("encode %q: %w", key, err)
		}
		if err := batch.Set(dbKey, row, nil); err != nil {
			return change, fmt.Errorf("stage %q: %w", key, err)
		}
		change.After, change.AfterPresent = string(row), true
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return change, fmt.Errorf("commit repair of %q (nothing changed): %w", key, err)
	}
	change.Committed = true

	if key != storageFormatPreferenceKey {
		return change, nil
	}
	change.SidecarTouched = true
	if setVersion == 0 {
		if rmErr := fs.Remove(change.SidecarPath); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			// The sidecar still holds its old value.
			change.SidecarAfter, change.SidecarAfterPresent = change.SidecarBefore, change.SidecarBeforePresent
			return change, fmt.Errorf("stamp deleted, but removing the sidecar %s failed: %w", change.SidecarPath, rmErr)
		}
		return change, nil
	}
	if err := writeStorageFormatSidecar(fs, dbPath, setVersion); err != nil {
		change.SidecarAfter, change.SidecarAfterPresent = change.SidecarBefore, change.SidecarBeforePresent
		return change, fmt.Errorf("stamp rewritten, but the sidecar was not: %w", err)
	}
	change.SidecarAfter, change.SidecarAfterPresent = fmt.Sprintf("%d\n", setVersion), true
	return change, nil
}

type rawValue struct {
	value   string
	present bool
}

func rawGet(db *pebble.DB, key []byte) (rawValue, error) {
	v, closer, err := db.Get(key)
	if errors.Is(err, pebble.ErrNotFound) {
		return rawValue{}, nil
	}
	if err != nil {
		return rawValue{}, fmt.Errorf("read %s: %w", key, err)
	}
	out := rawValue{value: string(v), present: true}
	if err := closer.Close(); err != nil {
		return rawValue{}, fmt.Errorf("close read of %s: %w", key, err)
	}
	return out, nil
}

// repairPreferenceID keeps the existing row's ID when it decodes, and
// otherwise allocates the next preference ID, staging the counter bump in
// the same batch as the row.
func repairPreferenceID(db *pebble.DB, batch *pebble.Batch, before rawValue, change *ReservedPreferenceChange) (int, error) {
	if before.present {
		var pref UserPreference
		if json.Unmarshal([]byte(before.value), &pref) == nil && pref.ID > 0 {
			return pref.ID, nil
		}
	}
	counter, err := rawGet(db, []byte("counter:preference"))
	if err != nil {
		return 0, err
	}
	if !counter.present {
		return 0, errors.New("counter:preference is missing; cannot allocate a preference ID")
	}
	id, err := strconv.Atoi(strings.TrimSpace(counter.value))
	if err != nil || id < 1 {
		return 0, fmt.Errorf("counter:preference holds %q; cannot allocate a preference ID", counter.value)
	}
	if err := batch.Set([]byte("counter:preference"), []byte(strconv.Itoa(id+1)), nil); err != nil {
		return 0, fmt.Errorf("stage preference counter: %w", err)
	}
	change.CounterTouched = true
	change.CounterBefore = counter.value
	change.CounterAfter = strconv.Itoa(id + 1)
	return id, nil
}

// openRawForRepair opens an EXISTING store with no format version (so nothing
// ratchets) and without the storage-format guard. The directory is checked
// first: pebble creates the directory and its LOCK file before it honours
// ErrorIfNotExists, so a typo'd path would otherwise leave an empty store
// directory behind.
func openRawForRepair(dbPath string, fs vfs.FS) (*pebble.DB, error) {
	info, err := fs.Stat(dbPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("no store at %s (check --db / DATABASE_PATH)", dbPath)
	}
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", dbPath, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a Pebble store directory", dbPath)
	}
	db, err := pebble.Open(dbPath, &pebble.Options{FS: fs, ErrorIfNotExists: true})
	if err != nil {
		if strings.Contains(err.Error(), "lock") || strings.Contains(err.Error(), "resource temporarily unavailable") {
			return nil, fmt.Errorf("open %s: %w (is the audiobook-organizer service still running? stop it first)", dbPath, err)
		}
		return nil, fmt.Errorf("open %s: %w", dbPath, err)
	}
	return db, nil
}

func readSidecarRaw(fs vfs.FS, dbPath string) (string, bool, error) {
	name := StorageFormatSidecarPath(dbPath)
	f, err := fs.Open(name)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("open %s: %w", name, err)
	}
	data, readErr := io.ReadAll(io.LimitReader(f, 64))
	closeErr := f.Close()
	if readErr != nil {
		return "", false, fmt.Errorf("read sidecar: %w", readErr)
	}
	if closeErr != nil {
		return "", false, fmt.Errorf("close %s: %w", name, closeErr)
	}
	return string(data), true, nil
}
