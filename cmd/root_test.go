// file: cmd/root_test.go
// version: 1.2.0
// guid: 7eae8d0c-7fda-4f45-8f73-5d1e0c7c9f1a
// last-edited: 2026-09-12

package cmd

import (
	"bytes"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
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

func TestSetupFileLogging(t *testing.T) {
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

	prevWriter := log.Writer()
	prevFlags := log.Flags()
	defer func() {
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
	}()

	logFile, err := setupFileLogging()
	if err != nil {
		t.Fatalf("setupFileLogging failed: %v", err)
	}
	defer logFile.Close()

	if _, err := os.Stat(logFile.Name()); err != nil {
		t.Fatalf("expected log file to exist: %v", err)
	}
}

func TestSetupFileLoggingErrorHandling(t *testing.T) {
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

	// Create logs dir as a file to force mkdir error
	if err := os.WriteFile("logs", []byte("not a dir"), 0o644); err != nil {
		t.Fatalf("failed to create file: %v", err)
	}

	prevWriter := log.Writer()
	prevFlags := log.Flags()
	defer func() {
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
	}()

	_, err = setupFileLogging()
	// Should handle error gracefully
	if err == nil {
		t.Fatal("expected error when logs dir is a file")
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
