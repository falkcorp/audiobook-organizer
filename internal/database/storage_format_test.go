// file: internal/database/storage_format_test.go
// version: 1.0.0
// guid: 8a965f29-0efc-4a0a-9fb9-bdc400520fd4
// last-edited: 2026-10-04

package database

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
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

func rawStampAt(t *testing.T, path string, fs vfs.FS) (int, bool) {
	t.Helper()
	db := rawOpen(t, path, fs)
	defer func() { require.NoError(t, db.Close()) }()
	stamp, present, err := readStorageFormatStamp(db)
	require.NoError(t, err)
	return stamp, present
}

// writeRawStamp writes the stamp row straight into Pebble, the way only the
// stamp writer and the recovery command can (the preference API refuses it).
func writeRawStamp(t *testing.T, path string, fs vfs.FS, n int) {
	t.Helper()
	rec, err := storageFormatStampRecord(77, n)
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

func TestStorageFormat_FreshStoreStampedAtSupported(t *testing.T) {
	fs := vfs.NewMem()
	s, err := newPebbleStore("/fresh", fs)
	require.NoError(t, err)
	require.NoError(t, s.Close())

	stamp, present := rawStampAt(t, "/fresh", fs)
	require.True(t, present)
	require.Equal(t, SupportedStorageFormat, stamp)

	// The in-memory constructor tests use stamps the same way.
	m, err := NewPebbleStoreInMemory("/mem")
	require.NoError(t, err)
	defer func() { require.NoError(t, m.Close()) }()
	stamp, present, err = readStorageFormatStamp(m.db)
	require.NoError(t, err)
	require.True(t, present)
	require.Equal(t, SupportedStorageFormat, stamp)
}

func TestStorageFormat_EmptyStoreStampedAtBuild(t *testing.T) {
	setBuildStorageFormat(t, 2)
	fs := vfs.NewMem()
	s, err := newPebbleStore("/empty", fs)
	require.NoError(t, err)
	require.NoError(t, s.Close())

	stamp, present := rawStampAt(t, "/empty", fs)
	require.True(t, present)
	require.Equal(t, 2, stamp, "an empty store is created at the build's format, not the legacy one")
}

func TestStorageFormat_DataWithoutStampReadsAsLegacy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.pebble")
	rawSet(t, path, nil, "book:x", "{}")

	openAndClose(t, path)

	stamp, present := rawStampAt(t, path, nil)
	require.True(t, present)
	require.Equal(t, legacyStorageFormat, stamp)
	require.Equal(t, fmt.Sprintf("%d\n", legacyStorageFormat), readSidecarFile(t, path))
}

func TestStorageFormat_OlderStoreRefusedBeforeAnyWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "older.pebble")
	rawSet(t, path, nil, "book:x", "{}")
	setBuildStorageFormat(t, 2)

	s, err := NewPebbleStore(path)
	require.Error(t, err)
	require.Nil(t, s)
	var migErr *StorageMigrationRequiredError
	require.True(t, errors.As(err, &migErr), "got %T: %v", err, err)
	require.Equal(t, legacyStorageFormat, migErr.Stamp)
	require.Equal(t, 2, migErr.Supported)
	require.False(t, migErr.MarkerPresent)
	require.True(t, strings.HasSuffix(err.Error(), "start serve to migrate"), err.Error())

	require.Equal(t, []string{"book:x"}, rawKeysAt(t, path, nil),
		"the refused open must write nothing: no counter: keys, no stamp")
	_, statErr := os.Stat(StorageFormatSidecarPath(path))
	require.True(t, errors.Is(statErr, os.ErrNotExist), "no sidecar after a refused open: %v", statErr)
}

func TestStorageFormat_MarkerPresentRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "marked.pebble")
	openAndClose(t, path)
	rawSet(t, path, nil, "preference:"+storageMigrationPreferenceKey, `{"id":99,"key":"storage_migration"}`)
	before := rawKeysAt(t, path, nil)

	_, err := NewPebbleStore(path)
	var migErr *StorageMigrationRequiredError
	require.True(t, errors.As(err, &migErr), "got %T: %v", err, err)
	require.True(t, migErr.MarkerPresent)
	require.Equal(t, SupportedStorageFormat, migErr.Stamp)
	require.Contains(t, err.Error(), "storage_migration marker")
	require.Contains(t, err.Error(), "restore the pre-migration checkpoint")
	require.NotContains(t, err.Error(), "start serve to migrate",
		"serve itself refuses a marked store, so telling the operator to start serve is circular")
	require.Equal(t, before, rawKeysAt(t, path, nil), "the refused open wrote something")

	db, st, err := openPebbleChecked(path, nil, openForCutover)
	require.NoError(t, err)
	require.True(t, st.markerPresent)
	require.Equal(t, before, rawKeys(t, db), "the cut-over open wrote something")
	require.NoError(t, db.Close())
	require.Equal(t, before, rawKeysAt(t, path, nil))
}

func TestStorageFormat_CutoverModeStillRefusesNewer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cutover-newer.pebble")
	openAndClose(t, path)
	setBuildStorageFormat(t, 0)

	db, _, err := openPebbleChecked(path, nil, openForCutover)
	require.Nil(t, db)
	var tooNew *StorageFormatTooNewError
	require.True(t, errors.As(err, &tooNew), "got %T: %v", err, err)
}

func TestStorageFormat_RefusesNewerStamp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "newer.pebble")
	openAndClose(t, path)
	writeRawStamp(t, path, nil, SupportedStorageFormat+1)
	before := rawKeysAt(t, path, nil)

	_, err := NewPebbleStore(path)
	var tooNew *StorageFormatTooNewError
	require.True(t, errors.As(err, &tooNew), "got %T: %v", err, err)
	require.Equal(t, "stamp", tooNew.Source)
	require.Equal(t, SupportedStorageFormat+1, tooNew.Stamp)
	require.Equal(t, SupportedStorageFormat, tooNew.Supported)
	want := fmt.Sprintf("storage format %d (stamp) in %s is newer than this build supports (%d); refusing to open. "+
		"Restore the pre-migration checkpoint and the binary that match this store: "+
		"docs/system/runbooks.md#storage-format-restore", SupportedStorageFormat+1, path, SupportedStorageFormat)
	require.Equal(t, want, err.Error())

	stamp, present := rawStampAt(t, path, nil)
	require.True(t, present)
	require.Equal(t, SupportedStorageFormat+1, stamp, "the refused open changed the stamp")
	require.Equal(t, before, rawKeysAt(t, path, nil), "the refused open wrote something")
}

func TestStorageFormat_RefusesNewerSidecar(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sidecar-newer.pebble")
	openAndClose(t, path)
	require.NoError(t, os.WriteFile(StorageFormatSidecarPath(path), []byte("2\n"), 0o644))
	before := rawKeysAt(t, path, nil)

	_, err := NewPebbleStore(path)
	var tooNew *StorageFormatTooNewError
	require.True(t, errors.As(err, &tooNew), "got %T: %v", err, err)
	require.Equal(t, "sidecar", tooNew.Source)
	require.Equal(t, 2, tooNew.Stamp)
	require.Contains(t, err.Error(), "docs/system/runbooks.md#storage-format-restore")
	require.Equal(t, before, rawKeysAt(t, path, nil), "the refused open wrote something")
	require.Equal(t, "2\n", readSidecarFile(t, path), "the refused open rewrote the sidecar")
}

func TestStorageFormat_CorruptSidecarIsRewritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sidecar-garbage.pebble")
	openAndClose(t, path)
	require.NoError(t, os.WriteFile(StorageFormatSidecarPath(path), []byte("garbage"), 0o644))

	openAndClose(t, path)
	require.Equal(t, "1\n", readSidecarFile(t, path))
}

func TestStorageFormat_UndecodableStampFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "undecodable.pebble")
	openAndClose(t, path)
	rawSet(t, path, nil, "preference:"+storageFormatPreferenceKey, "not json")

	_, err := NewPebbleStore(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "decode storage format stamp")

	db := rawOpen(t, path, nil)
	v, closer, err := db.Get([]byte("preference:" + storageFormatPreferenceKey))
	require.NoError(t, err)
	require.Equal(t, "not json", string(v), "the failed open must not restamp")
	require.NoError(t, closer.Close())
	require.NoError(t, db.Close())
}

func TestStorageFormat_EmptyStampValueFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty-value.pebble")
	openAndClose(t, path)
	rawSet(t, path, nil, "preference:"+storageFormatPreferenceKey, `{"id":5,"key":"storage_format"}`)

	_, err := NewPebbleStore(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "empty value")
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

func TestStorageFormat_ResetRestamps(t *testing.T) {
	s, err := NewPebbleStoreInMemory("/reset")
	require.NoError(t, err)
	defer func() { require.NoError(t, s.Close()) }()
	s.WaitForWarmup()

	require.NoError(t, s.Reset())
	stamp, present, err := readStorageFormatStamp(s.db)
	require.NoError(t, err)
	require.True(t, present)
	require.Equal(t, SupportedStorageFormat, stamp)

	// The stamp went in the wipe batch with preference ID 1, and the counter
	// moved past it, so the next preference does not reuse the ID.
	pref, err := s.GetUserPreference(storageFormatPreferenceKey)
	require.NoError(t, err)
	require.Equal(t, 1, pref.ID)
	require.NoError(t, s.SetUserPreference("theme", "dark"))
	theme, err := s.GetUserPreference("theme")
	require.NoError(t, err)
	require.Equal(t, 2, theme.ID)
}

func TestStorageFormat_CurrentStampStoreOpensAndServes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "current.pebble")
	s, err := NewPebbleStore(path)
	require.NoError(t, err)
	s.WaitForWarmup()
	created, err := s.CreateBook(&Book{Title: "Guard Check", FilePath: "/books/guard-check.m4b"})
	require.NoError(t, err)
	require.NoError(t, s.Close())

	stamp, present := rawStampAt(t, path, nil)
	require.True(t, present)
	require.Equal(t, buildStorageFormat, stamp)
	require.Equal(t, "1\n", readSidecarFile(t, path))

	s2, err := NewPebbleStore(path)
	require.NoError(t, err, "a current, unmarked store must open")
	defer func() { require.NoError(t, s2.Close()) }()
	got, err := s2.GetBookByID(created.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "Guard Check", got.Title)
}

// The brief names this ListUserPreferences; the function that iterates
// preference: and silently skips rows that do not decode is
// GetAllUserPreferences.
func TestStorageFormat_ListUserPreferencesStillDecodes(t *testing.T) {
	s, err := NewPebbleStoreInMemory("/prefs")
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
	stamp, present, err := readStorageFormatStamp(s.db)
	require.NoError(t, err)
	require.True(t, present)
	require.Equal(t, SupportedStorageFormat, stamp)

	// db_version and migration_<n> stay writable at the store: RunMigrations
	// writes them through SetUserPreference. The HTTP handler rejects them.
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
	require.NoError(t, db.Set([]byte("preference:"+storageMigrationPreferenceKey), []byte(`{"id":1}`), pebble.Sync))
	require.NoError(t, db.Close())

	_, err = NewPebbleStore(path)
	var migErr *StorageMigrationRequiredError
	require.True(t, errors.As(err, &migErr), "got %T: %v", err, err)

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
	_, err := NewPebbleStore(path)
	require.Error(t, err)

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
