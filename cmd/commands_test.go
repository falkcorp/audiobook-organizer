// file: cmd/commands_test.go
// version: 1.8.1
// guid: 6f5b7d78-11d8-4c1a-a150-96d2c4a1a885
// last-edited: 2026-11-09

package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

	origOpen := openCLIStore
	origScan := scanDirectory
	origProcess := processBooks
	origPlaylists := generatePlaylists
	origTags := updateSeriesTags
	origLoadConfig := loadConfigFromDB
	origNewServer := newServer
	origDefaultCfg := getDefaultServerConfig
	origRunApp := runApp

	openCLIStore = func(context.Context, bool) (cliStore, func(), error) {
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
		return ms, func() { database.SetGlobalStore(nil) }, nil
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
	loadConfigFromDB = func(store database.SettingsStore) error {
		return nil
	}
	newServer = func(_ database.Store, _ ...server.ServerOption) *server.Server { return &server.Server{} }
	getDefaultServerConfig = func() server.ServerConfig {
		return server.ServerConfig{Host: "localhost", Port: "8484"}
	}
	runApp = func(context.Context, books.ServeInputs) error { return nil }

	t.Cleanup(func() {
		openCLIStore = origOpen
		scanDirectory = origScan
		processBooks = origProcess
		generatePlaylists = origPlaylists
		updateSeriesTags = origTags
		loadConfigFromDB = origLoadConfig
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

	// The encryption init's failure is books' now (internal/books
	// TestSettingsBoot_StepFailuresAreFatal); serve returns the composition
	// root's error.
	runApp = func(context.Context, books.ServeInputs) error {
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

	openCLIStore = func(context.Context, bool) (cliStore, func(), error) {
		return nil, nil, fmt.Errorf("store init failed")
	}

	if err := scanCmd.RunE(scanCmd, nil); err == nil {
		t.Fatal("expected scan command to fail on store init error")
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
	runApp = func(_ context.Context, got books.ServeInputs) error {
		started, in = true, got
		return nil
	}
	err = serveCmd.RunE(serveCmd, nil)
	return started, in, err
}

// serve runs no startup step of its own (06 §6.4.3, R1-23): the kernel opens
// the store at P2, and books runs the key guard, the encryption init, the
// settings load and config.Validate in Register. serve hands the composition
// root its listener flags and the telemetry settings as HEAD's InitOTEL read
// them, before any stored setting applies (the settings boot runs inside
// app.New, after this snapshot), as it did for HEAD (R1-15).
func TestServe_RunsNoStartupStepOfItsOwn(t *testing.T) {
	stubCommandDeps(t)
	openCLIStore = func(context.Context, bool) (cliStore, func(), error) {
		t.Error("serve opened the store itself; the kernel opens it at P2")
		return nil, nil, fmt.Errorf("serve must not open the store")
	}
	loadConfigFromDB = func(database.SettingsStore) error {
		t.Error("serve loaded the stored settings itself; books' Register does")
		return nil
	}
	origConfig := config.AppConfig
	t.Cleanup(func() { config.AppConfig = origConfig })
	config.AppConfig.OTelExporterOTLPEndpoint = "http://192.0.2.9:4317"

	started, in, err := serveWithStubs(t)
	if err != nil {
		t.Fatalf("serve failed: %v", err)
	}
	if !started {
		t.Fatal("serve never reached the composition root")
	}
	if in.Server.StartupSteps != nil {
		t.Fatal("serve made a startup runner; books.Options makes it")
	}
	if in.Server.Port == "" {
		t.Fatal("serve did not hand its listener flags to the composition root")
	}
	if got := in.Telemetry.OTelExporterOTLPEndpoint; got != "http://192.0.2.9:4317" {
		t.Fatalf("telemetry endpoint = %q, want the pre-settings snapshot's", got)
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
