// file: cmd/diagnostics_test.go
// version: 2.3.0
// guid: 5480d7f7-4a6a-4b7f-9d16-6b589c8a3c0b
// last-edited: 2026-10-04

// NOTE(fable5 T022): Tests that used NewSQLiteStore have been ported to
// PebbleStore. Tests that set DatabaseType="sqlite" now verify that
// InitializeStore rejects SQLite with an error (it was removed in T022).

package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func TestTruncateString(t *testing.T) {
	if got := truncateString("short", 10); got != "short" {
		t.Fatalf("expected no truncation, got %q", got)
	}
	if got := truncateString("this is long", 4); got != "this..." {
		t.Fatalf("expected truncation, got %q", got)
	}
}

func TestHasPlaceholder(t *testing.T) {
	tokens := []string{"{author}", "{title}"}
	if !hasPlaceholder("/books/{Author}/file.mp3", tokens) {
		t.Fatal("expected placeholder match")
	}
	if hasPlaceholder("/books/clean/file.mp3", tokens) {
		t.Fatal("did not expect placeholder match")
	}
}

func TestPromptYesNo(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	_, _ = w.Write([]byte("yes\n"))
	_ = w.Close()

	origStdin := os.Stdin
	os.Stdin = r
	defer func() {
		os.Stdin = origStdin
	}()

	confirmed, err := promptYesNo("confirm")
	if err != nil {
		t.Fatalf("promptYesNo failed: %v", err)
	}
	if !confirmed {
		t.Fatal("expected confirmation")
	}
}

func TestRunDiagnosticsQueryErrors(t *testing.T) {
	origConfig := config.AppConfig
	defer func() {
		config.AppConfig = origConfig
	}()

	if err := runDiagnosticsQuery(0, "", false); err == nil {
		t.Fatal("expected error for invalid limit")
	}

	// SQLite is no longer supported; raw query with sqlite type should error.
	config.AppConfig.DatabaseType = "sqlite"
	if err := runDiagnosticsQuery(1, "book:", true); err == nil {
		t.Fatal("expected error for raw query with non-pebble db")
	}
}

func TestRunDiagnosticsQuerySuccess(t *testing.T) {
	origConfig := config.AppConfig
	defer func() {
		config.AppConfig = origConfig
	}()

	tempDir := t.TempDir()
	store, err := database.NewPebbleStore(tempDir)
	if err != nil {
		t.Fatalf("failed to create pebble store: %v", err)
	}
	_, err = store.CreateBook(&database.Book{
		Title:    "Diag Book",
		FilePath: "/tmp/diag.mp3",
	})
	if err != nil {
		t.Fatalf("failed to create book: %v", err)
	}
	_ = store.Close()

	config.AppConfig.DatabaseType = "pebble"
	config.AppConfig.DatabasePath = tempDir

	if err := runDiagnosticsQuery(5, "book:", false); err != nil {
		t.Fatalf("runDiagnosticsQuery failed: %v", err)
	}
}

func TestRunDiagnosticsQueryNoBooks(t *testing.T) {
	origConfig := config.AppConfig
	defer func() {
		config.AppConfig = origConfig
	}()

	tempDir := t.TempDir()
	store, err := database.NewPebbleStore(tempDir)
	if err != nil {
		t.Fatalf("failed to create pebble store: %v", err)
	}
	_ = store.Close()

	config.AppConfig.DatabaseType = "pebble"
	config.AppConfig.DatabasePath = tempDir

	if err := runDiagnosticsQuery(5, "book:", false); err != nil {
		t.Fatalf("runDiagnosticsQuery failed: %v", err)
	}
}

func TestRunDiagnosticsQueryPrintsHashes(t *testing.T) {
	origConfig := config.AppConfig
	defer func() {
		config.AppConfig = origConfig
	}()

	tempDir := t.TempDir()
	store, err := database.NewPebbleStore(tempDir)
	if err != nil {
		t.Fatalf("failed to create pebble store: %v", err)
	}
	hash := "hash"
	origHash := "orig"
	orgHash := "organized"
	_, err = store.CreateBook(&database.Book{
		Title:             "Diag Book",
		FilePath:          "/tmp/diag.mp3",
		FileHash:          &hash,
		OriginalFileHash:  &origHash,
		OrganizedFileHash: &orgHash,
	})
	if err != nil {
		t.Fatalf("failed to create book: %v", err)
	}
	_ = store.Close()

	config.AppConfig.DatabaseType = "pebble"
	config.AppConfig.DatabasePath = tempDir

	if err := runDiagnosticsQuery(5, "book:", false); err != nil {
		t.Fatalf("runDiagnosticsQuery failed: %v", err)
	}
}

func TestRunCleanupInvalidBooksDryRun(t *testing.T) {
	origConfig := config.AppConfig
	defer func() {
		config.AppConfig = origConfig
	}()

	tempDir := t.TempDir()
	store, err := database.NewPebbleStore(tempDir)
	if err != nil {
		t.Fatalf("failed to create pebble store: %v", err)
	}
	_, err = store.CreateBook(&database.Book{
		Title:    "Bad Book",
		FilePath: "/tmp/{author}/bad.mp3",
	})
	if err != nil {
		t.Fatalf("failed to create book: %v", err)
	}
	_ = store.Close()

	config.AppConfig.DatabaseType = "pebble"
	config.AppConfig.DatabasePath = tempDir

	if err := runCleanupInvalidBooks(false, true); err != nil {
		t.Fatalf("runCleanupInvalidBooks failed: %v", err)
	}
}

func TestRunCleanupInvalidBooksPromptAbort(t *testing.T) {
	origConfig := config.AppConfig
	defer func() {
		config.AppConfig = origConfig
	}()

	tempDir := t.TempDir()
	store, err := database.NewPebbleStore(tempDir)
	if err != nil {
		t.Fatalf("failed to create pebble store: %v", err)
	}
	_, err = store.CreateBook(&database.Book{
		Title:    "Bad Book",
		FilePath: "/tmp/{author}/bad.mp3",
	})
	if err != nil {
		t.Fatalf("failed to create book: %v", err)
	}
	_ = store.Close()

	config.AppConfig.DatabaseType = "pebble"
	config.AppConfig.DatabasePath = tempDir

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	_, _ = w.Write([]byte("no\n"))
	_ = w.Close()

	origStdin := os.Stdin
	os.Stdin = r
	defer func() {
		os.Stdin = origStdin
	}()

	if err := runCleanupInvalidBooks(false, false); err != nil {
		t.Fatalf("runCleanupInvalidBooks failed: %v", err)
	}
}

func TestRunCleanupInvalidBooksDelete(t *testing.T) {
	origConfig := config.AppConfig
	defer func() {
		config.AppConfig = origConfig
	}()

	tempDir := t.TempDir()
	store, err := database.NewPebbleStore(tempDir)
	if err != nil {
		t.Fatalf("failed to create pebble store: %v", err)
	}
	_, err = store.CreateBook(&database.Book{
		Title:    "Bad Book",
		FilePath: "/tmp/{author}/bad.mp3",
	})
	if err != nil {
		t.Fatalf("failed to create book: %v", err)
	}
	_ = store.Close()

	config.AppConfig.DatabaseType = "pebble"
	config.AppConfig.DatabasePath = tempDir

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	_, _ = w.Write([]byte("yes\n"))
	_ = w.Close()

	origStdin := os.Stdin
	os.Stdin = r
	defer func() {
		os.Stdin = origStdin
	}()

	if err := runCleanupInvalidBooks(false, false); err != nil {
		t.Fatalf("runCleanupInvalidBooks failed: %v", err)
	}
}

func TestRunRawPebbleQuery(t *testing.T) {
	tempDir := t.TempDir()
	store, err := database.NewPebbleStore(tempDir)
	if err != nil {
		t.Fatalf("failed to create pebble store: %v", err)
	}
	_, err = store.CreateBook(&database.Book{
		Title:    "Pebble Book",
		FilePath: "/tmp/pebble.mp3",
	})
	if err != nil {
		t.Fatalf("failed to create book: %v", err)
	}
	_ = store.Close()

	config.AppConfig.DatabasePath = tempDir
	if err := runRawPebbleQuery(1, "book:"); err != nil {
		t.Fatalf("runRawPebbleQuery failed: %v", err)
	}
}

// TestRawPebbleQuery_OpensStoreTheGuardRefuses: raw diagnostics mode is
// exempt from the storage-format guard by design, so it must still read a
// store whose stamp is newer than this build supports.
func TestRawPebbleQuery_OpensStoreTheGuardRefuses(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "too-new.pebble")
	versionJSON, err := json.Marshal(database.DatabaseVersion{Version: database.SupportedStorageFormat + 1, UpdatedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	value := string(versionJSON)
	prefJSON, err := json.Marshal(database.UserPreference{ID: 1, Key: "storage_format", Value: &value, UpdatedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	db, err := pebble.Open(dbPath, &pebble.Options{FormatMajorVersion: database.PebbleFormatMajorVersion})
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	if err := db.Set([]byte("preference:storage_format"), prefJSON, pebble.Sync); err != nil {
		t.Fatalf("raw set: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("raw close: %v", err)
	}

	// Premise: the guard refuses this store.
	if s, err := database.NewPebbleStore(dbPath); err == nil {
		_ = s.Close()
		t.Fatal("NewPebbleStore opened a store stamped newer than this build")
	} else {
		var tooNew *database.StorageFormatTooNewError
		if !errors.As(err, &tooNew) {
			t.Fatalf("NewPebbleStore: want *StorageFormatTooNewError, got %T: %v", err, err)
		}
	}

	origPath := config.AppConfig.DatabasePath
	t.Cleanup(func() { config.AppConfig.DatabasePath = origPath })
	config.AppConfig.DatabasePath = dbPath
	if err := runRawPebbleQuery(1, "preference:"); err != nil {
		t.Fatalf("runRawPebbleQuery on a store the guard refuses: %v", err)
	}
}

// TestRawPebbleQuery_DoesNotRatchetPebbleFormat: raw inspection must leave
// the on-disk format of a store below the pin exactly where it was.
func TestRawPebbleQuery_DoesNotRatchetPebbleFormat(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "min-format.pebble")
	db, err := pebble.Open(dbPath, &pebble.Options{FormatMajorVersion: pebble.FormatMinSupported})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Set([]byte("book:x"), []byte("{}"), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	origPath := config.AppConfig.DatabasePath
	t.Cleanup(func() { config.AppConfig.DatabasePath = origPath })
	config.AppConfig.DatabasePath = dbPath
	if err := runRawPebbleQuery(1, "book:"); err != nil {
		t.Fatalf("runRawPebbleQuery: %v", err)
	}

	db, err = pebble.Open(dbPath, &pebble.Options{FormatMajorVersion: pebble.FormatMinSupported})
	if err != nil {
		t.Fatal(err)
	}
	got := db.FormatMajorVersion()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if got != pebble.FormatMinSupported {
		t.Fatalf("raw query ratcheted the on-disk format to %d", got)
	}
}

// TestRunReservedPrefs_RecoversStoreWithBadStamp: a store whose stamp holds
// garbage is refused by every open; `diagnostics reserved-prefs --delete`
// is the recovery path. Declining the prompt changes nothing; confirming
// deletes the row and the store opens again.
func TestRunReservedPrefs_RecoversStoreWithBadStamp(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "bad-stamp.pebble")
	s, err := database.NewPebbleStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := pebble.Open(dbPath, &pebble.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Set([]byte("preference:storage_format"), []byte("garbage"), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if s, err := database.NewPebbleStore(dbPath); err == nil {
		_ = s.Close()
		t.Fatal("premise: a garbage stamp must refuse the open")
	}

	// Listing is read-only and works on the refused store.
	if err := runReservedPrefs(dbPath, "", "", false, false, nil); err != nil {
		t.Fatalf("list: %v", err)
	}

	asked := ""
	decline := func(q string) (bool, error) { asked = q; return false, nil }
	if err := runReservedPrefs(dbPath, "storage_format", "", false, false, decline); err != nil {
		t.Fatalf("declined delete: %v", err)
	}
	if !strings.Contains(asked, "storage_format") {
		t.Fatalf("confirmation prompt did not name the key: %q", asked)
	}
	if s, err := database.NewPebbleStore(dbPath); err == nil {
		_ = s.Close()
		t.Fatal("a declined confirmation changed the store")
	}

	if err := runReservedPrefs(dbPath, "storage_format", "", true, false, nil); err != nil {
		t.Fatalf("delete --yes: %v", err)
	}
	s, err = database.NewPebbleStore(dbPath)
	if err != nil {
		t.Fatalf("store still refused after the repair: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	if err := runReservedPrefs(dbPath, "theme", "", true, false, nil); err == nil {
		t.Fatal("a non-reserved key must be rejected")
	}
	if err := runReservedPrefs(dbPath, "", "storage_format=abc", true, false, nil); err == nil {
		t.Fatal("--set with a non-integer must be rejected")
	}
}

// captureStdout runs fn with os.Stdout redirected and returns what it wrote.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	type result struct {
		b   []byte
		err error
	}
	done := make(chan result)
	go func() {
		b, err := io.ReadAll(r)
		done <- result{b, err}
	}()
	fn()
	os.Stdout = orig
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	res := <-done
	if res.err != nil {
		t.Fatal(res.err)
	}
	return string(res.b)
}

// A repair that committed and THEN failed (sidecar removal, store close) must
// still print every key it changed before returning the error.
func TestRunReservedPrefs_ReportsCommittedChangeOnError(t *testing.T) {
	prev := repairReservedPreference
	t.Cleanup(func() { repairReservedPreference = prev })
	repairReservedPreference = func(dbPath, key string, v int) (database.ReservedPreferenceChange, error) {
		return database.ReservedPreferenceChange{
			Key: key, Before: "old", BeforePresent: true, Committed: true,
			SidecarTouched: true, SidecarPath: dbPath + ".storage-format",
			SidecarBefore: "2\n", SidecarBeforePresent: true,
			SidecarAfter: "2\n", SidecarAfterPresent: true,
		}, errors.New("removing the sidecar failed: is a directory")
	}

	var err error
	out := captureStdout(t, func() {
		err = runReservedPrefs("/srv/x.pebble", "storage_format", "", true, false, nil)
	})
	if err == nil || !strings.Contains(err.Error(), "WAS committed") {
		t.Fatalf("want an error saying the change was committed, got %v", err)
	}
	for _, want := range []string{"Changed preference storage_format", `before="old"`, "after=<absent>", "Changed sidecar"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout missing %q:\n%s", want, out)
		}
	}
}

func TestRunReservedPrefs_ClosedStdinAsksForYes(t *testing.T) {
	eof := func(string) (bool, error) { return false, io.EOF }
	err := runReservedPrefs("/srv/x.pebble", "storage_migration", "", false, false, eof)
	if err == nil || err.Error() != "confirmation needed: pass --yes or run interactively" {
		t.Fatalf("got %v", err)
	}
}

func TestExecuteHelp(t *testing.T) {
	tempDir := t.TempDir()

	origCfg := cfgFile
	origDBPath := databasePath
	origPlaylist := playlistDir
	defer func() {
		cfgFile = origCfg
		databasePath = origDBPath
		playlistDir = origPlaylist
	}()

	cfgFile = filepath.Join(tempDir, "config.yaml")
	databasePath = filepath.Join(tempDir, "db.pebble")
	playlistDir = filepath.Join(tempDir, "playlists")

	rootCmd.SetArgs([]string{"--db", databasePath, "--playlists", playlistDir, "--help"})
	defer rootCmd.SetArgs(nil)

	if err := Execute(); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
}

func TestPromptYesNoNo(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	_, _ = w.Write([]byte("no\n"))
	_ = w.Close()

	origStdin := os.Stdin
	os.Stdin = r
	defer func() {
		os.Stdin = origStdin
	}()

	confirmed, err := promptYesNo("confirm")
	if err != nil {
		t.Fatalf("promptYesNo failed: %v", err)
	}
	if confirmed {
		t.Fatal("expected rejection")
	}
}

func TestTruncateStringWithBuffer(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteString("1234567890")
	got := truncateString(buf.String(), 6)
	if got != "123456..." {
		t.Fatalf("expected truncated buffer, got %q", got)
	}
}

func TestEnsureDiagnosticsStoreError(t *testing.T) {
	origConfig := config.AppConfig
	defer func() {
		config.AppConfig = origConfig
	}()

	// Set invalid database type
	config.AppConfig.DatabaseType = "invalid"

	_, _, err := ensureDiagnosticsStore()
	if err == nil {
		t.Fatal("expected error for invalid database type")
	}
}

func TestEnsureDiagnosticsStorePebble(t *testing.T) {
	origConfig := config.AppConfig
	defer func() {
		config.AppConfig = origConfig
	}()

	tempDir := t.TempDir()
	config.AppConfig.DatabaseType = "pebble"
	config.AppConfig.DatabasePath = tempDir

	_, cleanup, err := ensureDiagnosticsStore()
	if err != nil {
		t.Fatalf("expected pebble store creation to succeed: %v", err)
	}
	if cleanup != nil {
		cleanup()
	}
}

func TestRunCleanupInvalidBooksForceMode(t *testing.T) {
	origConfig := config.AppConfig
	defer func() {
		config.AppConfig = origConfig
	}()

	tempDir := t.TempDir()
	store, err := database.NewPebbleStore(tempDir)
	if err != nil {
		t.Fatalf("failed to create pebble store: %v", err)
	}
	_, err = store.CreateBook(&database.Book{
		Title:    "Bad Book",
		FilePath: "/tmp/{author}/bad.mp3",
	})
	if err != nil {
		t.Fatalf("failed to create book: %v", err)
	}
	_ = store.Close()

	config.AppConfig.DatabaseType = "pebble"
	config.AppConfig.DatabasePath = tempDir

	// Test force mode (no prompt)
	if err := runCleanupInvalidBooks(true, true); err != nil {
		t.Fatalf("runCleanupInvalidBooks with force failed: %v", err)
	}
}

func TestRunDiagnosticsQueryPebble(t *testing.T) {
	origConfig := config.AppConfig
	defer func() {
		config.AppConfig = origConfig
	}()

	tempDir := t.TempDir()
	store, err := database.NewPebbleStore(tempDir)
	if err != nil {
		t.Fatalf("failed to create pebble store: %v", err)
	}
	_, err = store.CreateBook(&database.Book{
		Title:    "Pebble Test",
		FilePath: "/tmp/test.mp3",
	})
	if err != nil {
		t.Fatalf("failed to create book: %v", err)
	}
	_ = store.Close()

	config.AppConfig.DatabaseType = "pebble"
	config.AppConfig.DatabasePath = tempDir

	if err := runDiagnosticsQuery(5, "book:", false); err != nil {
		t.Fatalf("runDiagnosticsQuery with pebble failed: %v", err)
	}
}
