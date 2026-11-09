// file: cmd/root_test.go
// version: 1.7.1
// guid: 7eae8d0c-7fda-4f45-8f73-5d1e0c7c9f1a
// last-edited: 2026-11-09

package cmd

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/scanner"
	"github.com/spf13/viper"
)

// TestRemovedSQLiteFlag_AcceptedWithDeprecationWarning pins the #3268
// follow-up: a command line that still carries the removed
// --enable-sqlite3-i-know-the-risks flag must parse through the real Execute
// path (before this, cobra failed startup with "unknown flag"), print the
// deprecation warning, stay out of --help, and not leak into viper, where
// config.warnRemovedViperKeys would log the removal a second time.
func TestRemovedSQLiteFlag_AcceptedWithDeprecationWarning(t *testing.T) {
	flag := rootCmd.PersistentFlags().Lookup(removedSQLiteFlag)
	if flag == nil {
		t.Fatalf("--%s is not registered; a command line that passes it fails with unknown flag", removedSQLiteFlag)
	}
	if !flag.Hidden {
		t.Errorf("--%s is not hidden", removedSQLiteFlag)
	}
	if flag.Deprecated != removedSQLiteFlagMessage {
		t.Errorf("--%s Deprecated = %q, want %q", removedSQLiteFlag, flag.Deprecated, removedSQLiteFlagMessage)
	}

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	// --help stops cobra before initConfig and the command body, so this
	// exercises flag parsing and help rendering without starting a server.
	rootCmd.SetArgs([]string{"serve", "--" + removedSQLiteFlag, "--help"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		_ = flag.Value.Set("false")
		flag.Changed = false
		if help := serveCmd.Flags().Lookup("help"); help != nil {
			_ = help.Value.Set("false")
			help.Changed = false
		}
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("Execute with --%s returned %v; the removed flag must never break startup", removedSQLiteFlag, err)
	}

	got := out.String()
	wantWarning := "Flag --" + removedSQLiteFlag + " has been deprecated, " + removedSQLiteFlagMessage
	if !strings.Contains(got, wantWarning) {
		t.Errorf("output does not contain the deprecation warning %q:\n%s", wantWarning, got)
	}
	if !strings.Contains(got, "--db-type") {
		t.Fatalf("serve --help output did not list the global flags; the hidden-flag check below would be vacuous:\n%s", got)
	}
	// The warning line is the only mention: the help listing omits the flag.
	if n := strings.Count(got, removedSQLiteFlag); n != 1 {
		t.Errorf("--%s appears %d times in the output, want 1 (the warning only, not the help listing):\n%s", removedSQLiteFlag, n, got)
	}
	if strings.Contains(rootCmd.UsageString(), removedSQLiteFlag) {
		t.Errorf("root usage lists --%s", removedSQLiteFlag)
	}
	for _, key := range []string{"enable_sqlite3_i_know_the_risks", "enable_sqlite"} {
		if viper.IsSet(key) {
			t.Errorf("viper.IsSet(%q) = true after parsing the flag; it must stay unbound", key)
		}
	}
}

func TestFormatMetadataValue(t *testing.T) {
	if got := formatMetadataValue("  "); got != "(empty)" {
		t.Fatalf("expected empty placeholder, got %q", got)
	}
	if got := formatMetadataValue("Title"); got != "Title" {
		t.Fatalf("expected value passthrough, got %q", got)
	}
}

func TestInitConfigWithViper(t *testing.T) {
	tempDir := t.TempDir()
	configFile := filepath.Join(tempDir, "config.yaml")

	// Create a config file with some settings
	configContent := `root_dir: /tmp/audiobooks
database_path: /tmp/test.db
`
	if err := os.WriteFile(configFile, []byte(configContent), 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	origCfgFile := cfgFile
	origConfig := config.AppConfig
	defer func() {
		cfgFile = origCfgFile
		config.AppConfig = origConfig
		viper.Reset()
	}()

	cfgFile = configFile

	initConfig()

	// Verify config was loaded
	if config.AppConfig.RootDir != "/tmp/audiobooks" {
		t.Fatalf("expected root_dir to be set from config file")
	}
}

// enabled_sort_indexes is boot-only, so what GET /api/v1/config reports must
// be the set the store builds: initConfig writes back the accepted names, with
// an unknown entry dropped rather than reported as an enabled index.
func TestInitConfigReportsTheBuiltSortIndexSet(t *testing.T) {
	tempDir := t.TempDir()
	configFile := filepath.Join(tempDir, "config.yaml")
	configContent := "enabled_sort_indexes: [Year, bogus, created_at, year]\n"
	if err := os.WriteFile(configFile, []byte(configContent), 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	origCfgFile := cfgFile
	origConfig := config.Snapshot()
	t.Cleanup(func() {
		cfgFile = origCfgFile
		config.Mutate(func(c *config.Config) { *c = origConfig })
		viper.Reset()
		database.SetEnabledSortIndexes(nil)
	})
	cfgFile = configFile

	initConfig()

	got := config.Snapshot().EnabledSortIndexes
	if strings.Join(got, ",") != "year,created_at" {
		t.Fatalf("AppConfig.EnabledSortIndexes = %#v, want [year created_at] (the built set)", got)
	}
	if !database.CanPushDownSort("created_at") || !database.CanPushDownSort("year") {
		t.Fatal("year and created_at should be push-downable after initConfig")
	}
}

func TestInitConfigDefaults(t *testing.T) {
	tempDir := t.TempDir()

	origCfgFile := cfgFile
	origDBPath := databasePath
	origPlaylistDir := playlistDir
	origConfig := config.AppConfig
	defer func() {
		cfgFile = origCfgFile
		databasePath = origDBPath
		playlistDir = origPlaylistDir
		config.AppConfig = origConfig
		viper.Reset()
	}()

	cfgFile = ""
	databasePath = filepath.Join(tempDir, "test.db")
	playlistDir = filepath.Join(tempDir, "playlists")

	initConfig()

	// Verify directories were created
	if _, err := os.Stat(filepath.Dir(databasePath)); err != nil {
		t.Fatal("database directory should be created")
	}
	if _, err := os.Stat(playlistDir); err != nil {
		t.Fatal("playlist directory should be created")
	}
}

func TestInitConfigCreatesDirectories(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "db", "test.db")
	playlistsPath := filepath.Join(tempDir, "playlists")

	origCfgFile := cfgFile
	origDBPath := databasePath
	origPlaylistDir := playlistDir
	origConfig := config.AppConfig
	defer func() {
		cfgFile = origCfgFile
		databasePath = origDBPath
		playlistDir = origPlaylistDir
		config.AppConfig = origConfig
	}()

	cfgFile = filepath.Join(tempDir, "config.yaml")
	databasePath = dbPath
	playlistDir = playlistsPath

	initConfig()

	if _, err := os.Stat(filepath.Dir(dbPath)); err != nil {
		t.Fatalf("expected database directory to exist: %v", err)
	}
	if _, err := os.Stat(playlistsPath); err != nil {
		t.Fatalf("expected playlist directory to exist: %v", err)
	}
}

func TestInitConfigUsesHomeConfig(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, ".audiobook-organizer.yaml")
	if err := os.WriteFile(configPath, []byte("root_dir: /tmp\n"), 0o644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	origCfgFile := cfgFile
	origDBPath := databasePath
	origPlaylistDir := playlistDir
	origConfig := config.AppConfig
	defer func() {
		cfgFile = origCfgFile
		databasePath = origDBPath
		playlistDir = origPlaylistDir
		config.AppConfig = origConfig
	}()

	t.Setenv("HOME", tempDir)
	cfgFile = ""
	databasePath = ""
	playlistDir = ""

	viper.Reset()
	initConfig()
}

func TestPrintMetadataField(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	origStdout := os.Stdout
	os.Stdout = w
	defer func() {
		os.Stdout = origStdout
	}()

	printMetadataField("Title", "")
	_ = w.Close()

	output, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		t.Fatalf("failed to read output: %v", err)
	}
	if got := string(output); got == "" {
		t.Fatal("expected output to be written")
	}
}

func TestScanCommandMissingRootDir(t *testing.T) {
	tempDir := t.TempDir()
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get cwd: %v", err)
	}
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	defer func() {
		_ = os.Chdir(origDir)
	}()

	origConfig := config.AppConfig
	defer func() {
		config.AppConfig = origConfig
	}()

	config.AppConfig.RootDir = ""

	if err := scanCmd.RunE(scanCmd, nil); err == nil {
		t.Fatal("expected error when root directory is missing")
	}
}

func TestMetadataInspectRequiresFile(t *testing.T) {
	if err := metadataInspectCmd.RunE(metadataInspectCmd, nil); err == nil {
		t.Fatal("expected error when file is missing")
	}
}

// TestScanAndOrganizeLogToStdoutAtLogLevel: scan and organize have no App, so
// slog's stock default (standard error, info) would take their lines. They
// install a text handler on standard output at log_level for the length of the
// command, and put the previous default back.
func TestScanAndOrganizeLogToStdoutAtLogLevel(t *testing.T) {
	stubCommandDeps(t)
	processBooks = func([]scanner.Book, logger.Logger) error {
		slog.Debug("joker debug line")
		slog.Info("penguin info line")
		return nil
	}
	origConfig := config.AppConfig
	t.Cleanup(func() { config.AppConfig = origConfig })
	config.AppConfig.RootDir = t.TempDir()
	config.AppConfig.PlaylistDir = t.TempDir()
	config.AppConfig.DatabasePath = filepath.Join(t.TempDir(), "db")

	for _, tc := range []struct {
		name      string
		run       func() error
		level     string
		wantDebug bool
	}{
		{"scan at debug", func() error { return scanCmd.RunE(scanCmd, nil) }, "debug", true},
		{"scan at info", func() error { return scanCmd.RunE(scanCmd, nil) }, "info", false},
		{"scan with a level slog does not know", func() error { return scanCmd.RunE(scanCmd, nil) }, "loud", false},
		{"organize at debug", func() error { return organizeCmd.RunE(organizeCmd, nil) }, "debug", true},
		{"organize at info", func() error { return organizeCmd.RunE(organizeCmd, nil) }, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config.AppConfig.LogLevel = tc.level
			prev := slog.Default()

			origStdout := os.Stdout
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			os.Stdout = w
			runErr := tc.run()
			os.Stdout = origStdout
			_ = w.Close()
			out, _ := io.ReadAll(r)

			if runErr != nil {
				t.Fatalf("command failed: %v", runErr)
			}
			if !strings.Contains(string(out), "penguin info line") {
				t.Errorf("the command's info line is not on standard output:\n%s", out)
			}
			if got := strings.Contains(string(out), "joker debug line"); got != tc.wantDebug {
				t.Errorf("debug line on stdout = %v, want %v at log_level %q", got, tc.wantDebug, tc.level)
			}
			if slog.Default() != prev {
				t.Error("the command did not restore the previous default logger")
			}
		})
	}
}

// TestServeStartupLinesGoToStdoutBeforeTheApp: the lines logged before app.New
// installs the log pipeline at P5 (the kernel's store open, checkpoint and
// migrations at P2 to P4b, R1-23) go to standard output at log_level, not to
// slog's stock default on standard error, and the previous default is back
// when serve returns. The stubbed runApp stands in for app.New, and so for
// those phases: nothing installs a pipeline here, so the handler its lines
// meet is the one serve itself installed.
func TestServeStartupLinesGoToStdoutBeforeTheApp(t *testing.T) {
	stubCommandDeps(t)
	origConfig := config.AppConfig
	t.Cleanup(func() { config.AppConfig = origConfig })
	tempDir := t.TempDir()
	config.AppConfig.DatabaseType = "sqlite"
	config.AppConfig.DatabasePath = filepath.Join(tempDir, "db.sqlite")
	config.AppConfig.RootDir = tempDir
	config.AppConfig.PlaylistDir = filepath.Join(tempDir, "playlists")
	config.AppConfig.LogLevel = "debug"

	runApp = func(context.Context, books.ServeInputs) error {
		slog.Debug("two-face debug line")
		slog.Info("catwoman store line")
		slog.Info("riddler app line")
		return nil
	}
	prev := slog.Default()

	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	runErr := serveCmd.RunE(serveCmd, nil)
	os.Stdout = origStdout
	_ = w.Close()
	out, _ := io.ReadAll(r)

	if runErr != nil {
		t.Fatalf("serve failed: %v", runErr)
	}
	for _, want := range []string{"two-face debug line", "catwoman store line", "riddler app line"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("%q is not on standard output:\n%s", want, out)
		}
	}
	if slog.Default() != prev {
		t.Error("serve did not restore the previous default logger")
	}
}
