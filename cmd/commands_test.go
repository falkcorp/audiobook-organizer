// file: cmd/commands_test.go
// version: 1.7.0
// guid: 6f5b7d78-11d8-4c1a-a150-96d2c4a1a885
// last-edited: 2026-11-08

package cmd

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"

	"github.com/falkcorp/audiobook-organizer/internal/books"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/scanner"
	"github.com/falkcorp/audiobook-organizer/internal/server"
)

func stubCommandDeps(t *testing.T) {
	t.Helper()

	origInit := initializeStore
	origClose := closeStore
	origScan := scanDirectory
	origProcess := processBooks
	origPlaylists := generatePlaylists
	origTags := updateSeriesTags
	origInitEncrypt := initEncryption
	origLoadConfig := loadConfigFromDB
	origSyncEnv := syncConfigFromEnv
	origNewServer := newServer
	origDefaultCfg := getDefaultServerConfig
	origRunApp := runApp

	initializeStore = func(dbType, path string) (database.Store, error) {
		// Command tests only need GlobalStore to be non-nil for the
		// startup sequence to proceed. The only store method the
		// startup path actually touches is GetAllImportPaths (in
		// root.go — it prints the import path count). We register
		// it as .Maybe() so the mock tolerates both "called" and
		// "not called". If new startup code calls additional Store
		// methods, add them here and the test will tell you which.
		ms := mocks.NewMockStore(t)
		ms.EXPECT().GetAllImportPaths().Return(nil, nil).Maybe()
		database.SetGlobalStore(ms)
		return ms, nil
	}
	closeStore = func() error {
		database.SetGlobalStore(nil)
		return nil
	}
	scanDirectory = func(_ context.Context, rootDir string, _ logger.Logger) ([]scanner.Book, error) {
		return []scanner.Book{}, nil
	}
	processBooks = func(books []scanner.Book, _ logger.Logger) error {
		return nil
	}
	generatePlaylists = func() error {
		return nil
	}
	updateSeriesTags = func() error {
		return nil
	}
	initEncryption = func(dir string, legacy ...string) error {
		return nil
	}
	loadConfigFromDB = func(store database.SettingsStore) error {
		return nil
	}
	syncConfigFromEnv = func() {}
	newServer = func(_ database.Store, _ ...server.ServerOption) *server.Server { return &server.Server{} }
	getDefaultServerConfig = func() server.ServerConfig {
		return server.ServerConfig{Host: "localhost", Port: "8484"}
	}
	runApp = func(context.Context, database.Store, books.ServeInputs) error { return nil }

	t.Cleanup(func() {
		initializeStore = origInit
		closeStore = origClose
		scanDirectory = origScan
		processBooks = origProcess
		generatePlaylists = origPlaylists
		updateSeriesTags = origTags
		initEncryption = origInitEncrypt
		loadConfigFromDB = origLoadConfig
		syncConfigFromEnv = origSyncEnv
		newServer = origNewServer
		getDefaultServerConfig = origDefaultCfg
		runApp = origRunApp
		database.SetGlobalStore(nil)
	})
}

func TestCommandsRunWithStubs(t *testing.T) {
	stubCommandDeps(t)

	tempDir := t.TempDir()
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get cwd: %v", err)
	}
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(origDir)
	})

	origConfig := config.AppConfig
	t.Cleanup(func() {
		config.AppConfig = origConfig
	})

	config.AppConfig.DatabaseType = "sqlite"
	config.AppConfig.DatabasePath = filepath.Join(tempDir, "db.sqlite")
	config.AppConfig.RootDir = tempDir
	config.AppConfig.PlaylistDir = filepath.Join(tempDir, "playlists")

	if err := scanCmd.RunE(scanCmd, nil); err != nil {
		t.Fatalf("scanCmd failed: %v", err)
	}
	if err := playlistCmd.RunE(playlistCmd, nil); err != nil {
		t.Fatalf("playlistCmd failed: %v", err)
	}
	if err := tagCmd.RunE(tagCmd, nil); err != nil {
		t.Fatalf("tagCmd failed: %v", err)
	}
	if err := organizeCmd.RunE(organizeCmd, nil); err != nil {
		t.Fatalf("organizeCmd failed: %v", err)
	}
	sandboxStateDir(t)
	if err := serveCmd.RunE(serveCmd, nil); err != nil {
		t.Fatalf("serveCmd failed: %v", err)
	}
}

func TestScanCommandErrorPaths(t *testing.T) {
	stubCommandDeps(t)

	tempDir := t.TempDir()
	origConfig := config.AppConfig
	t.Cleanup(func() {
		config.AppConfig = origConfig
	})

	config.AppConfig.DatabaseType = "sqlite"
	config.AppConfig.DatabasePath = filepath.Join(tempDir, "db.sqlite")
	config.AppConfig.RootDir = tempDir

	scanDirectory = func(_ context.Context, rootDir string, _ logger.Logger) ([]scanner.Book, error) {
		return nil, fmt.Errorf("scan failed")
	}
	if err := scanCmd.RunE(scanCmd, nil); err == nil {
		t.Fatal("expected scan command error")
	}

	scanDirectory = func(_ context.Context, rootDir string, _ logger.Logger) ([]scanner.Book, error) {
		return []scanner.Book{{FilePath: filepath.Join(tempDir, "book.m4b")}}, nil
	}
	processBooks = func(books []scanner.Book, _ logger.Logger) error {
		return fmt.Errorf("process failed")
	}
	if err := scanCmd.RunE(scanCmd, nil); err == nil {
		t.Fatal("expected processBooks error")
	}
}

func TestServeCommandErrorPaths(t *testing.T) {
	stubCommandDeps(t)

	tempDir := t.TempDir()
	origConfig := config.AppConfig
	t.Cleanup(func() {
		config.AppConfig = origConfig
	})

	config.AppConfig.DatabaseType = "sqlite"
	config.AppConfig.DatabasePath = filepath.Join(tempDir, "db.sqlite")

	sandboxStateDir(t)
	initEncryption = func(dir string, legacy ...string) error { return fmt.Errorf("encrypt fail") }
	if err := serveCmd.RunE(serveCmd, nil); err == nil {
		t.Fatal("expected serve command to fail on encryption error")
	}

	initEncryption = func(dir string, legacy ...string) error { return nil }
	runApp = func(context.Context, database.Store, books.ServeInputs) error {
		return fmt.Errorf("start failed")
	}
	if err := serveCmd.RunE(serveCmd, nil); err == nil {
		t.Fatal("expected serve command to fail on start error")
	}
}

func TestPlaylistCommandError(t *testing.T) {
	stubCommandDeps(t)

	tempDir := t.TempDir()
	origConfig := config.AppConfig
	t.Cleanup(func() {
		config.AppConfig = origConfig
	})

	config.AppConfig.DatabaseType = "sqlite"
	config.AppConfig.DatabasePath = filepath.Join(tempDir, "db.sqlite")
	config.AppConfig.PlaylistDir = filepath.Join(tempDir, "playlists")

	generatePlaylists = func() error {
		return fmt.Errorf("playlist generation failed")
	}
	if err := playlistCmd.RunE(playlistCmd, nil); err == nil {
		t.Fatal("expected playlist command error")
	}
}

func TestTagCommandError(t *testing.T) {
	stubCommandDeps(t)

	tempDir := t.TempDir()
	origConfig := config.AppConfig
	t.Cleanup(func() {
		config.AppConfig = origConfig
	})

	config.AppConfig.DatabaseType = "sqlite"
	config.AppConfig.DatabasePath = filepath.Join(tempDir, "db.sqlite")

	updateSeriesTags = func() error {
		return fmt.Errorf("tag update failed")
	}
	if err := tagCmd.RunE(tagCmd, nil); err == nil {
		t.Fatal("expected tag command error")
	}
}

func TestOrganizeCommandError(t *testing.T) {
	stubCommandDeps(t)

	tempDir := t.TempDir()
	origConfig := config.AppConfig
	t.Cleanup(func() {
		config.AppConfig = origConfig
	})

	config.AppConfig.DatabaseType = "sqlite"
	config.AppConfig.DatabasePath = filepath.Join(tempDir, "db.sqlite")

	scanDirectory = func(_ context.Context, rootDir string, _ logger.Logger) ([]scanner.Book, error) {
		return nil, fmt.Errorf("scan failed in organize")
	}
	if err := organizeCmd.RunE(organizeCmd, nil); err == nil {
		t.Fatal("expected organize command error")
	}
}

func TestStoreInitializationError(t *testing.T) {
	stubCommandDeps(t)

	tempDir := t.TempDir()
	origConfig := config.AppConfig
	t.Cleanup(func() {
		config.AppConfig = origConfig
	})

	config.AppConfig.DatabaseType = "sqlite"
	config.AppConfig.DatabasePath = filepath.Join(tempDir, "db.sqlite")

	initializeStore = func(dbType, path string) (database.Store, error) {
		return nil, fmt.Errorf("store init failed")
	}

	if err := scanCmd.RunE(scanCmd, nil); err == nil {
		t.Fatal("expected scan command to fail on store init error")
	}
}

// sandboxStateDir points the credential directory at a temp dir and seeds it
// with an encryption key.
//
// Both halves matter. Without the redirect these tests target the real
// /var/lib/audiobook-organizer -- EACCES on a dev machine, and the PRODUCTION
// directory on the server. Without the seeded key, guardAgainstKeyRegeneration
// sees no key anywhere and calls GetAllSettings to find out whether generating
// one would destroy secrets, which these stores are not set up to answer.
//
// A pre-existing key is also the realistic state for what these tests cover: an
// install that has run before. The guard's own behaviour is tested directly in
// encryption_key_guard_test.go.
func sandboxStateDir(t *testing.T) {
	t.Helper()
	config.ResetStateDirForTest()
	t.Cleanup(config.ResetStateDirForTest)
	dir := t.TempDir()
	t.Setenv(config.SecureStateDirEnv, dir)
	if err := os.WriteFile(database.EncryptionKeyPath(dir), bytes.Repeat([]byte{9}, 32), 0o600); err != nil {
		t.Fatal(err)
	}
}

// serveWithStubs runs serveCmd against stubbed deps and reports whether
// runApp (the composition root, where the listeners are bound) was reached,
// and with what inputs.
func serveWithStubs(t *testing.T) (started bool, in books.ServeInputs, err error) {
	t.Helper()
	tempDir := t.TempDir()
	origConfig := config.AppConfig
	t.Cleanup(func() { config.AppConfig = origConfig })
	config.AppConfig.DatabaseType = "pebble"
	config.AppConfig.DatabasePath = filepath.Join(tempDir, "db.pebble")
	config.AppConfig.RootDir = tempDir
	sandboxStateDir(t)
	runApp = func(_ context.Context, _ database.Store, got books.ServeInputs) error {
		started, in = true, got
		return nil
	}
	err = serveCmd.RunE(serveCmd, nil)
	return started, in, err
}

// serve hands the composition root its startup runner, its listener flags
// and the telemetry settings as HEAD's InitOTEL read them: before the stored
// settings apply. A collector endpoint that only the stored settings carry
// does not reach the App's Provider, as it did not reach HEAD's (R1-15).
func TestServe_TelemetryReadBeforeStoredSettings(t *testing.T) {
	stubCommandDeps(t)
	loadConfigFromDB = func(database.SettingsStore) error {
		config.AppConfig.OTelExporterOTLPEndpoint = "http://192.0.2.9:4317"
		return nil
	}

	started, in, err := serveWithStubs(t)
	if err != nil {
		t.Fatalf("serve failed: %v", err)
	}
	if !started {
		t.Fatal("serve never reached the composition root")
	}
	if in.Server.StartupSteps == nil {
		t.Fatal("serve did not hand its startup runner to the composition root")
	}
	if in.Server.Port == "" {
		t.Fatal("serve did not hand its listener flags to the composition root")
	}
	if got := in.Telemetry.OTelExporterOTLPEndpoint; got != "" {
		t.Fatalf("telemetry endpoint = %q, want the value from before the stored settings (empty)", got)
	}
	if got := config.AppConfig.OTelExporterOTLPEndpoint; got != "http://192.0.2.9:4317" {
		t.Fatalf("the stored settings did not apply to AppConfig: %q", got)
	}
}

// serve hands the composition root the state directory it resolved, where the
// App opens its log file (core.log.file.path, 05-logging section 11 item 1).
func TestServe_PassesTheStateDirForTheLogFile(t *testing.T) {
	stubCommandDeps(t)

	started, in, err := serveWithStubs(t)
	if err != nil {
		t.Fatalf("serve failed: %v", err)
	}
	if !started {
		t.Fatal("serve never reached the composition root")
	}
	want := os.Getenv(config.SecureStateDirEnv)
	if want == "" || in.StateDir != want {
		t.Fatalf("ServeInputs.StateDir = %q, want the resolved state directory %q", in.StateDir, want)
	}
}

// A store-init failure is Fatal: serve returns the error and never reaches
// the composition root, so no listener is bound.
func TestServe_StoreInitErrorAbortsBeforeAnyListener(t *testing.T) {
	stubCommandDeps(t)
	initializeStore = func(string, string) (database.Store, error) {
		return nil, fmt.Errorf("store init failed")
	}

	started, _, err := serveWithStubs(t)
	if err == nil {
		t.Fatal("serve returned nil after a store-init failure")
	}
	if started {
		t.Fatal("serve reached the composition root after a store-init failure")
	}
}

// loadConfigFromDB is Fatal: a server on defaults would overwrite the stored
// config_blob on its first settings save (whole-struct SaveConfigToDatabase).
// It was a printed warning until 2026-10-10.
func TestServe_ConfigLoadErrorIsFatal(t *testing.T) {
	stubCommandDeps(t)
	loadConfigFromDB = func(database.SettingsStore) error {
		return fmt.Errorf("read settings: boom")
	}

	started, _, err := serveWithStubs(t)
	if err == nil {
		t.Fatal("serve returned nil after a config-load failure")
	}
	if started {
		t.Fatal("serve reached the composition root after a config-load failure")
	}
}

// The review case behind making GetAllSettings strict (PR #3930 SF1): a real
// Pebble store whose config_blob row is not a valid setting record. It used to
// be skipped, so LoadConfigFromDatabase saw "no blob", ran on defaults, and the
// first settings save overwrote the stored config. Now the real loader fails,
// and serve never reaches the composition root.
func TestServe_CorruptConfigBlobRowIsFatal(t *testing.T) {
	stubCommandDeps(t)
	dbPath := filepath.Join(t.TempDir(), "corrupt.pebble")
	raw, err := pebble.Open(dbPath, &pebble.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.Set([]byte("setting:config_blob"), []byte("{truncated"), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	initializeStore = func(string, string) (database.Store, error) {
		st, err := database.NewPebbleStore(dbPath)
		if err != nil {
			return nil, err
		}
		t.Cleanup(func() { _ = st.Close() })
		return st, nil
	}
	loadConfigFromDB = config.LoadConfigFromDatabase

	started, _, err := serveWithStubs(t)
	if err == nil {
		t.Fatal("serve returned nil with a corrupt config_blob row")
	}
	if started {
		t.Fatal("serve reached the composition root with a corrupt config_blob row")
	}
	if !strings.Contains(err.Error(), "config_blob") {
		t.Fatalf("error does not name the corrupt row: %v", err)
	}
}

// serve reads ABO_CORE_SHUTDOWN_BUDGET and hands it to the composition root
// as ServeInputs.ShutdownBudget; unset means 0, core's default (R1-33).
func TestServe_ShutdownBudgetFromEnvReachesApp(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		set         bool
		want        time.Duration
	}{
		{name: "unset"},
		{name: "empty", set: true},
		{name: "set", set: true, value: "40s", want: 40 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubCommandDeps(t)
			if tc.set {
				t.Setenv(shutdownBudgetEnv, tc.value)
			} else {
				os.Unsetenv(shutdownBudgetEnv)
			}
			started, in, err := serveWithStubs(t)
			if err != nil || !started {
				t.Fatalf("started=%v err=%v", started, err)
			}
			if in.ShutdownBudget != tc.want {
				t.Fatalf("ShutdownBudget = %v, want %v", in.ShutdownBudget, tc.want)
			}
		})
	}
}

// An invalid ABO_CORE_SHUTDOWN_BUDGET makes serve refuse to start, with an
// error naming the variable, before anything is opened or bound.
func TestServe_InvalidShutdownBudgetRefusesToStart(t *testing.T) {
	stubCommandDeps(t)
	t.Setenv(shutdownBudgetEnv, "soon")
	started, _, err := serveWithStubs(t)
	if err == nil {
		t.Fatal("serve started with an invalid ABO_CORE_SHUTDOWN_BUDGET")
	}
	if !strings.Contains(err.Error(), shutdownBudgetEnv) {
		t.Fatalf("error %q does not name %s", err, shutdownBudgetEnv)
	}
	if started {
		t.Fatal("composition root reached despite the invalid value")
	}
}
