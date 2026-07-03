// file: cmd/commands_test.go
// version: 1.2.2
// guid: 6f5b7d78-11d8-4c1a-a150-96d2c4a1a885

package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

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
	origStart := startServer

	initializeStore = func(dbType, path string, enableSQLite bool) (database.Store, error) {
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
	initEncryption = func(dir string) error {
		return nil
	}
	loadConfigFromDB = func(store database.SettingsStore) error {
		return nil
	}
	syncConfigFromEnv = func() {}
	newServer = func(_ database.Store) *server.Server { return &server.Server{} }
	getDefaultServerConfig = func() server.ServerConfig {
		return server.ServerConfig{Host: "localhost", Port: "8484"}
	}
	startServer = func(srv *server.Server, cfg server.ServerConfig) error { return nil }

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
		startServer = origStart
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
	config.AppConfig.EnableSQLite = true
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
	config.AppConfig.EnableSQLite = true

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
	config.AppConfig.EnableSQLite = true

	initEncryption = func(dir string) error { return fmt.Errorf("encrypt fail") }
	if err := serveCmd.RunE(serveCmd, nil); err == nil {
		t.Fatal("expected serve command to fail on encryption error")
	}

	initEncryption = func(dir string) error { return nil }
	startServer = func(srv *server.Server, cfg server.ServerConfig) error {
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
	config.AppConfig.EnableSQLite = true
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
	config.AppConfig.EnableSQLite = true

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
	config.AppConfig.EnableSQLite = true

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
	config.AppConfig.EnableSQLite = true

	initializeStore = func(dbType, path string, enableSQLite bool) (database.Store, error) {
		return nil, fmt.Errorf("store init failed")
	}

	if err := scanCmd.RunE(scanCmd, nil); err == nil {
		t.Fatal("expected scan command to fail on store init error")
	}
}
