// file: internal/database/storage_format_test.go
// version: 2.1.2
// guid: 8a965f29-0efc-4a0a-9fb9-bdc400520fd4
// last-edited: 2026-11-09

package database

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/falkcorp/mediacore/storage"
	"github.com/stretchr/testify/require"
)

// The tests that change buildStorageFormat must not run in parallel: it is a
// package variable every open reads.

func setBuildStorageFormat(t *testing.T, n int) {
	t.Helper()
	prev := buildStorageFormat
	buildStorageFormat = n
	t.Cleanup(func() { buildStorageFormat = prev })
}

// rawOpen opens the store at path with Pebble directly, at the pinned format,
// bypassing every storage-format check. fs nil means the real disk.
func rawOpen(t *testing.T, path string, fs vfs.FS) *pebble.DB {
	t.Helper()
	opts := &pebble.Options{FormatMajorVersion: PebbleFormatMajorVersion}
	if fs != nil {
		opts.FS = fs
	}
	db, err := pebble.Open(path, opts)
	require.NoError(t, err)
	return db
}

func rawSet(t *testing.T, path string, fs vfs.FS, key, value string) {
	t.Helper()
	db := rawOpen(t, path, fs)
	require.NoError(t, db.Set([]byte(key), []byte(value), pebble.Sync))
	require.NoError(t, db.Close())
}

// rawKeys returns every key in the store, in order.
func rawKeys(t *testing.T, db *pebble.DB) []string {
	t.Helper()
	iter, err := db.NewIter(nil)
	require.NoError(t, err)
	var keys []string
	for ok := iter.First(); ok; ok = iter.Next() {
		keys = append(keys, string(iter.Key()))
	}
	require.NoError(t, iter.Error())
	require.NoError(t, iter.Close())
	return keys
}

func rawKeysAt(t *testing.T, path string, fs vfs.FS) []string {
	t.Helper()
	db := rawOpen(t, path, fs)
	defer func() { require.NoError(t, db.Close()) }()
	return rawKeys(t, db)
}

// rawStampAt decodes the stamp row of the closed store at path, the way HEAD
// reads it.
func rawStampAt(t *testing.T, path string, fs vfs.FS) (int, bool) {
	t.Helper()
	row, present := rawStampRowAt(t, path, fs)
	if !present {
		return 0, false
	}
	return decodeStampRow(t, row), true
}

// decodeStampRow decodes a stamp row's format number, the way HEAD reads it.
func decodeStampRow(t *testing.T, row []byte) int {
	t.Helper()
	var pref UserPreference
	require.NoError(t, json.Unmarshal(row, &pref))
	require.NotNil(t, pref.Value)
	var v DatabaseVersion
	require.NoError(t, json.Unmarshal([]byte(*pref.Value), &v))
	return v.Version
}

// rawStampRowAt returns the stamp row's bytes as stored.
func rawStampRowAt(t *testing.T, path string, fs vfs.FS) ([]byte, bool) {
	t.Helper()
	db := rawOpen(t, path, fs)
	defer func() { require.NoError(t, db.Close()) }()
	v, err := rawGet(db, []byte("preference:"+storageFormatPreferenceKey))
	require.NoError(t, err)
	return []byte(v.value), v.present
}

// writeRawStamp writes the stamp row straight into Pebble, in HEAD's format,
// the way HEAD's stamp writer and the recovery command do (the preference
// API refuses it).
func writeRawStamp(t *testing.T, path string, fs vfs.FS, n int) {
	t.Helper()
	payload, err := databaseVersionPayload(n)
	require.NoError(t, err)
	rec, err := json.Marshal(UserPreference{ID: 77, Key: storageFormatPreferenceKey, Value: &payload, UpdatedAt: time.Now()})
	require.NoError(t, err)
	rawSet(t, path, fs, "preference:"+storageFormatPreferenceKey, string(rec))
}

func openAndClose(t *testing.T, path string) {
	t.Helper()
	s, err := NewPebbleStore(path)
	require.NoError(t, err)
	require.NoError(t, s.Close())
}

func readSidecarFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(StorageFormatSidecarPath(path))
	require.NoError(t, err)
	return string(data)
}

// A fresh store is stamped at 1, as HEAD stamped it: stampStorageFormat, the
// open-time repair HEAD called ensureStorageFormatStamp, writes an absent
// stamp at the value an absent stamp already reads as (R1-23 D7). The
// projection is unchanged and the sidecar says 1.
func TestStorageFormat_FreshStoreStampedAtOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fresh.pebble")
	openAndClose(t, path)

	stamp, present := rawStampAt(t, path, nil)
	require.True(t, present, "the open-time repair stamps a fresh store, as HEAD did")
	require.Equal(t, SupportedStorageFormat, stamp)
	require.Equal(t, fmt.Sprintf("%d\n", SupportedStorageFormat), readSidecarFile(t, path))

	// The in-memory constructor tests use stamps the same way.
	m, err := NewPebbleStoreInMemory("/mem")
	require.NoError(t, err)
	defer func() { require.NoError(t, m.Close()) }()
	row, err := m.GetRaw("preference:" + storageFormatPreferenceKey)
	require.NoError(t, err)
	require.NotNil(t, row)
	require.Equal(t, SupportedStorageFormat, decodeStampRow(t, row))
}

func TestStorageFormat_DataWithoutStampReadsAsLegacy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.pebble")
	rawSet(t, path, nil, "book:x", "{}")

	openAndClose(t, path)

	stamp, present := rawStampAt(t, path, nil)
	require.True(t, present, "the first core boot stamps a store HEAD left unstamped, as HEAD's open did")
	require.Equal(t, legacyStorageFormat, stamp, "at the value an absent stamp already reads as")
	require.Equal(t, fmt.Sprintf("%d\n", legacyStorageFormat), readSidecarFile(t, path))
}

// The stamp row is not the guard's input on a core build (the sidecar is),
// and no open rewrites it: whatever HEAD left there, decodable or not, is
// byte-for-byte what a rollback to HEAD finds.
func TestStorageFormat_StampRowNeverRewrittenAtOpen(t *testing.T) {
	for name, row := range map[string]string{
		"undecodable": "not json",
		"empty value": `{"id":5,"key":"storage_format"}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "stamp.pebble")
			openAndClose(t, path)
			rawSet(t, path, nil, "preference:"+storageFormatPreferenceKey, row)
			openAndClose(t, path)
			got, present := rawStampRowAt(t, path, nil)
			require.True(t, present)
			require.Equal(t, row, string(got))
		})
	}
	t.Run("current", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "stamp.pebble")
		openAndClose(t, path)
		writeRawStamp(t, path, nil, SupportedStorageFormat)
		before, _ := rawStampRowAt(t, path, nil)
		openAndClose(t, path)
		got, _ := rawStampRowAt(t, path, nil)
		require.Equal(t, before, got)
	})
}

func TestStorageFormat_RefusesNewerSidecar(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sidecar-newer.pebble")
	openAndClose(t, path)
	require.NoError(t, os.WriteFile(StorageFormatSidecarPath(path), []byte("2\n"), 0o644))
	before := rawKeysAt(t, path, nil)

	_, err := NewPebbleStore(path)
	var tooNew *storage.LevelTooNewError
	require.True(t, errors.As(err, &tooNew), "got %T: %v", err, err)
	require.Equal(t, BooksOwner, tooNew.Owner)
	require.Equal(t, "legacy-stamp", tooNew.Source)
	require.Equal(t, 2, tooNew.Store)
	require.Equal(t, SupportedStorageFormat, tooNew.Supported)
	require.Equal(t, before, rawKeysAt(t, path, nil), "the refused open wrote something")
	require.Equal(t, "2\n", readSidecarFile(t, path), "the refused open rewrote the sidecar")
}

// A sidecar that exists and does not parse refuses the boot: it is the
// guard's only input for the projection, so a store too new for this build
// could not be ruled out (05-storage §5S.3.7, review MINOR-3).
func TestStorageFormat_CorruptSidecarRefusesBoot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sidecar-garbage.pebble")
	openAndClose(t, path)
	require.NoError(t, os.WriteFile(StorageFormatSidecarPath(path), []byte("garbage"), 0o644))
	before := rawKeysAt(t, path, nil)

	_, err := NewPebbleStore(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), StorageFormatSidecarPath(path))
	require.Equal(t, before, rawKeysAt(t, path, nil), "the refused open wrote something")
	require.Equal(t, "garbage", readSidecarFile(t, path), "the refused open rewrote the sidecar")
}

func TestStorageFormat_SidecarWrittenAtOpen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	openAndClose(t, dir)

	data, err := os.ReadFile(dir + ".storage-format")
	require.NoError(t, err)
	require.Equal(t, "1\n", string(data))
	_, err = os.Stat(dir + ".storage-format.tmp")
	require.True(t, errors.Is(err, os.ErrNotExist), "the temp file must be renamed away: %v", err)
}

func TestStorageFormat_CurrentStoreOpensAndServes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "current.pebble")
	s, err := NewPebbleStore(path)
	require.NoError(t, err)
	s.WaitForWarmup()
	created, err := s.CreateBook(&Book{Title: "Guard Check", FilePath: "/books/guard-check.m4b"})
	require.NoError(t, err)
	require.NoError(t, s.Close())

	require.Equal(t, "1\n", readSidecarFile(t, path))

	s2, err := NewPebbleStore(path)
	require.NoError(t, err, "a current store must open")
	defer func() { require.NoError(t, s2.Close()) }()
	got, err := s2.GetBookByID(created.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "Guard Check", got.Title)
}

// The brief names this ListUserPreferences; the function that iterates
// preference: and silently skips rows that do not decode is
// GetAllUserPreferences. The stamp row HEAD wrote still decodes as one.
func TestStorageFormat_ListUserPreferencesStillDecodes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prefs.pebble")
	openAndClose(t, path)
	writeRawStamp(t, path, nil, SupportedStorageFormat)
	s, err := NewPebbleStore(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, s.Close()) }()

	prefs, err := s.GetAllUserPreferences()
	require.NoError(t, err)
	found := false
	for _, p := range prefs {
		if p.Key == storageFormatPreferenceKey {
			found = true
			require.NotNil(t, p.Value)
			require.Contains(t, *p.Value, `"version":1`)
		}
	}
	require.True(t, found, "storage_format must decode as a UserPreference")
}

func TestStorageFormat_PinnedFormatLeavesOnDiskVersionUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pinned.pebble")
	db := rawOpen(t, path, nil)
	require.Equal(t, PebbleFormatMajorVersion, db.FormatMajorVersion())
	require.NoError(t, db.Close())

	openAndClose(t, path)

	db, err := pebble.Open(path, &pebble.Options{FormatMajorVersion: pebble.FormatMinSupported})
	require.NoError(t, err)
	require.Equal(t, PebbleFormatMajorVersion, db.FormatMajorVersion())
	require.NoError(t, db.Close())
}

func TestStorageFormat_PreferenceAPIRefusesGuardedKeys(t *testing.T) {
	s, err := NewPebbleStoreInMemory("/guarded")
	require.NoError(t, err)
	defer func() { require.NoError(t, s.Close()) }()

	for _, key := range []string{storageFormatPreferenceKey, storageMigrationPreferenceKey} {
		require.ErrorIs(t, s.SetUserPreference(key, "garbage"), ErrReservedPreferenceKey, key)
		require.ErrorIs(t, s.DeleteUserPreference(key), ErrReservedPreferenceKey, key)
	}
	row, err := s.GetRaw("preference:" + storageFormatPreferenceKey)
	require.NoError(t, err)
	require.NotNil(t, row, "the refused delete left the open-time stamp in place")
	require.Equal(t, SupportedStorageFormat, decodeStampRow(t, row), "the refused set wrote nothing")

	// db_version and migration_<n> stay writable at the store (books'
	// VersionSource and the tests write them). The HTTP handler rejects them.
	require.NoError(t, s.SetUserPreference("migration_9999", "{}"))
	require.True(t, IsReservedPreferenceKey("migration_9999"))
	require.True(t, IsReservedPreferenceKey(dbVersionPreferenceKey))
	require.False(t, IsReservedPreferenceKey("theme"))
}

// S2 probe: a refused open must not ratchet the Pebble on-disk format.
func TestStorageFormat_RefusedOpenDoesNotRatchetPebbleFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "min-format.pebble")
	db, err := pebble.Open(path, &pebble.Options{FormatMajorVersion: pebble.FormatMinSupported})
	require.NoError(t, err)
	require.NoError(t, db.Set([]byte("book:x"), []byte("{}"), pebble.Sync))
	require.NoError(t, db.Close())
	require.NoError(t, os.WriteFile(StorageFormatSidecarPath(path), []byte("2\n"), 0o644))

	_, err = NewPebbleStore(path)
	var tooNew *storage.LevelTooNewError
	require.True(t, errors.As(err, &tooNew), "got %T: %v", err, err)

	db, err = pebble.Open(path, &pebble.Options{FormatMajorVersion: pebble.FormatMinSupported})
	require.NoError(t, err)
	require.Equal(t, pebble.FormatMinSupported, db.FormatMajorVersion(), "the refused open ratcheted the on-disk format")
	require.NoError(t, db.Close())
}

func TestStorageFormat_AcceptedOpenRatchetsToPinnedFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "min-format-ok.pebble")
	db, err := pebble.Open(path, &pebble.Options{FormatMajorVersion: pebble.FormatMinSupported})
	require.NoError(t, err)
	require.NoError(t, db.Set([]byte("book:x"), []byte("{}"), pebble.Sync))
	require.NoError(t, db.Close())

	openAndClose(t, path)

	db, err = pebble.Open(path, &pebble.Options{FormatMajorVersion: pebble.FormatMinSupported})
	require.NoError(t, err)
	require.Equal(t, PebbleFormatMajorVersion, db.FormatMajorVersion())
	require.NoError(t, db.Close())
}

func TestStorageFormat_SidecarWriteFailureRemovesTemp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sidecar-fail.pebble")
	// A non-empty directory where the sidecar belongs: the rename fails.
	require.NoError(t, os.MkdirAll(filepath.Join(StorageFormatSidecarPath(path), "block"), 0o755))

	err := writeStorageFormatSidecar(vfs.Default, path, 1)
	require.Error(t, err)
	_, statErr := os.Stat(StorageFormatSidecarPath(path) + ".tmp")
	require.True(t, errors.Is(statErr, os.ErrNotExist), "temp file left behind: %v", statErr)
}

// --- recovery: RepairReservedPreference ---

func TestStorageFormat_RepairDeletesBadStampSoStoreOpens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repair-delete.pebble")
	openAndClose(t, path)
	rawSet(t, path, nil, "preference:"+storageFormatPreferenceKey, "garbage")
	require.NoError(t, os.WriteFile(StorageFormatSidecarPath(path), []byte("2\n"), 0o644))
	_, err := NewPebbleStore(path)
	require.Error(t, err, "premise: the bad stamp refuses the open")

	rows, sidecar, sidecarPresent, err := ListReservedPreferences(path)
	require.NoError(t, err)
	require.True(t, sidecarPresent)
	require.Equal(t, "2\n", sidecar)
	require.Contains(t, rows, ReservedPreferenceRow{Key: storageFormatPreferenceKey, Raw: "garbage"})

	change, err := RepairReservedPreference(path, storageFormatPreferenceKey, 0)
	require.NoError(t, err)
	require.Equal(t, "garbage", change.Before)
	require.False(t, change.AfterPresent)
	require.True(t, change.SidecarTouched)
	require.Equal(t, "2\n", change.SidecarBefore)
	require.False(t, change.SidecarAfterPresent)

	openAndClose(t, path)
	stamp, present := rawStampAt(t, path, nil)
	require.True(t, present)
	require.Equal(t, legacyStorageFormat, stamp, "a store with data and no stamp restamps as legacy")
	require.Equal(t, "1\n", readSidecarFile(t, path))
}

// TestStorageFormat_ListOpensReadOnly (R1-25 follow-up N-3): the listing
// opens the store read-only, so it adds no file to the store directory (a
// read-write open writes a new WAL, manifest and OPTIONS file, which, run as
// root, the service could not replace) and it reads a store directory this
// process may not write. The repair still opens read-write.
func TestStorageFormat_ListOpensReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "list-readonly.pebble")
	openAndClose(t, path)
	names := func() []string {
		entries, err := os.ReadDir(path)
		require.NoError(t, err)
		out := make([]string, 0, len(entries))
		for _, e := range entries {
			out = append(out, e.Name())
		}
		return out
	}
	before := names()
	_, _, _, err := ListReservedPreferences(path)
	require.NoError(t, err)
	require.Equal(t, before, names(), "a listing added or removed a file in the store directory")
	if os.Geteuid() != 0 {
		require.NoError(t, os.Chmod(path, 0o500))
		t.Cleanup(func() { _ = os.Chmod(path, 0o700) })
		_, _, _, err := ListReservedPreferences(path)
		require.NoError(t, err, "a listing of a store directory this process cannot write")
	}
}

func TestStorageFormat_RepairRewritesStampAndSidecar(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repair-set.pebble")
	openAndClose(t, path)
	writeRawStamp(t, path, nil, SupportedStorageFormat+1)
	before := rawKeysAt(t, path, nil)

	change, err := RepairReservedPreference(path, storageFormatPreferenceKey, SupportedStorageFormat)
	require.NoError(t, err)
	require.True(t, change.AfterPresent)
	require.Equal(t, fmt.Sprintf("%d\n", SupportedStorageFormat), change.SidecarAfter)
	require.Equal(t, before, rawKeysAt(t, path, nil), "the decodable row kept its ID: no counter bump, no new key")

	openAndClose(t, path)
	stamp, _ := rawStampAt(t, path, nil)
	require.Equal(t, SupportedStorageFormat, stamp)
}

func TestStorageFormat_RepairUndecodableRowAllocatesID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repair-id.pebble")
	openAndClose(t, path)
	rawSet(t, path, nil, "preference:"+dbVersionPreferenceKey, "not json")
	db := rawOpen(t, path, nil)
	counterBefore, err := rawGet(db, []byte("counter:preference"))
	require.NoError(t, err)
	require.NoError(t, db.Close())

	_, err = RepairReservedPreference(path, dbVersionPreferenceKey, 7)
	require.NoError(t, err)

	s, err := NewPebbleStore(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, s.Close()) }()
	pref, err := s.GetUserPreference(dbVersionPreferenceKey)
	require.NoError(t, err)
	require.Equal(t, counterBefore.value, fmt.Sprint(pref.ID), "the new row takes the next preference ID")
	require.Contains(t, *pref.Value, `"version":7`)
}

func TestStorageFormat_RepairReportsCounterBump(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repair-counter.pebble")
	openAndClose(t, path)
	rawSet(t, path, nil, "preference:"+dbVersionPreferenceKey, "not json")
	db := rawOpen(t, path, nil)
	counterBefore, err := rawGet(db, []byte("counter:preference"))
	require.NoError(t, err)
	require.NoError(t, db.Close())

	change, err := RepairReservedPreference(path, dbVersionPreferenceKey, 7)
	require.NoError(t, err)
	require.True(t, change.CounterTouched, "the counter bump must be reported, not silent")
	require.Equal(t, counterBefore.value, change.CounterBefore)
	n, err := strconv.Atoi(counterBefore.value)
	require.NoError(t, err)
	require.Equal(t, strconv.Itoa(n+1), change.CounterAfter)
}

func TestStorageFormat_RepairSidecarUnreadableChangesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repair-sidecar-dir.pebble")
	openAndClose(t, path)
	require.NoError(t, os.Remove(StorageFormatSidecarPath(path)))
	require.NoError(t, os.MkdirAll(StorageFormatSidecarPath(path), 0o755))
	before := rawKeysAt(t, path, nil)

	change, err := RepairReservedPreference(path, storageFormatPreferenceKey, 0)
	require.Error(t, err)
	require.Contains(t, err.Error(), "nothing changed")
	require.False(t, change.Committed)
	require.Equal(t, before, rawKeysAt(t, path, nil), "a repair that fails before the commit must not change the store")

	rows, sidecar, present, err := ListReservedPreferences(path)
	require.NoError(t, err, "an unreadable sidecar must not hide the rows")
	require.True(t, present)
	require.Contains(t, sidecar, "<unreadable:")
	require.NotEmpty(t, rows)
}

func TestStorageFormat_RepairCloseFailureStillReportsCommitted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repair-close.pebble")
	openAndClose(t, path)
	rawSet(t, path, nil, "preference:"+storageMigrationPreferenceKey, "x")
	prev := closeRepairDB
	closeRepairDB = func(db *pebble.DB) error {
		require.NoError(t, db.Close())
		return errors.New("injected close failure")
	}
	t.Cleanup(func() { closeRepairDB = prev })

	change, err := RepairReservedPreference(path, storageMigrationPreferenceKey, 0)
	require.ErrorContains(t, err, "injected close failure")
	require.True(t, change.Committed, "the batch committed before the close failed")
	require.Equal(t, "x", change.Before)
	closeRepairDB = prev
	_, present, err := func() (string, bool, error) {
		db := rawOpen(t, path, nil)
		defer func() { require.NoError(t, db.Close()) }()
		v, err := rawGet(db, []byte("preference:"+storageMigrationPreferenceKey))
		return v.value, v.present, err
	}()
	require.NoError(t, err)
	require.False(t, present, "the marker was deleted")
}

func TestStorageFormat_RepairDBVersionCappedAtLatestMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repair-dbversion.pebble")
	openAndClose(t, path)
	latest := LatestMigrationVersion()
	require.Greater(t, latest, 1)

	_, err := RepairReservedPreference(path, dbVersionPreferenceKey, latest+1)
	require.ErrorContains(t, err, "above the highest registered migration")
	_, err = RepairReservedPreference(path, dbVersionPreferenceKey, 100000)
	require.Error(t, err)
	_, err = RepairReservedPreference(path, dbVersionPreferenceKey, latest)
	require.NoError(t, err)
}

func TestStorageFormat_RepairRefusesStampDeleteWhenBuildIsNewer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repair-delete-newer.pebble")
	openAndClose(t, path)
	writeRawStamp(t, path, nil, 1) // as HEAD left it
	setBuildStorageFormat(t, 2)

	_, err := RepairReservedPreference(path, storageFormatPreferenceKey, 0)
	require.ErrorContains(t, err, "Use --set storage_format=N")
	stamp, present := rawStampAt(t, path, nil)
	require.True(t, present)
	require.Equal(t, 1, stamp)
}

func TestStorageFormat_RepairMissingPathCreatesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "typo.pebble")
	_, err := RepairReservedPreference(path, storageMigrationPreferenceKey, 0)
	require.ErrorContains(t, err, "no store at")
	_, statErr := os.Stat(path)
	require.True(t, errors.Is(statErr, os.ErrNotExist), "the repair created %s: %v", path, statErr)
	_, _, _, err = ListReservedPreferences(path)
	require.ErrorContains(t, err, "no store at")
	_, statErr = os.Stat(path)
	require.True(t, errors.Is(statErr, os.ErrNotExist))
}

func TestStorageFormat_RepairLockedStoreNamesTheService(t *testing.T) {
	path := filepath.Join(t.TempDir(), "locked.pebble")
	s, err := NewPebbleStore(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, s.Close()) }()

	_, err = RepairReservedPreference(path, storageMigrationPreferenceKey, 0)
	require.ErrorContains(t, err, "is the audiobook-organizer service still running?")
}

func TestStorageFormat_RepairDeletesMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repair-marker.pebble")
	openAndClose(t, path)
	rawSet(t, path, nil, "preference:"+storageMigrationPreferenceKey, "x")

	change, err := RepairReservedPreference(path, storageMigrationPreferenceKey, 0)
	require.NoError(t, err)
	require.False(t, change.SidecarTouched)
	openAndClose(t, path)
}

func TestStorageFormat_RepairRefusesBadRequests(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repair-bad.pebble")
	openAndClose(t, path)
	before := rawKeysAt(t, path, nil)

	_, err := RepairReservedPreference(path, "theme", 0)
	require.ErrorContains(t, err, "not a reserved preference key")
	_, err = RepairReservedPreference(path, storageMigrationPreferenceKey, 2)
	require.ErrorContains(t, err, "only storage_format and db_version")
	_, err = RepairReservedPreference(path, storageFormatPreferenceKey, SupportedStorageFormat+1)
	require.ErrorContains(t, err, "newer than this build supports")
	_, err = RepairReservedPreference(path, storageMigrationPreferenceKey, 0)
	require.ErrorContains(t, err, "not present")
	_, err = RepairReservedPreference(filepath.Join(t.TempDir(), "missing.pebble"), storageMigrationPreferenceKey, 0)
	require.Error(t, err, "the repair must not create a store")
	require.Equal(t, before, rawKeysAt(t, path, nil))
}
