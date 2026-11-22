// file: cmd/root_test.go
// version: 1.7.1
// guid: 7eae8d0c-7fda-4f45-8f73-5d1e0c7c9f1a
// last-edited: 2026-11-09

package cmd

import (
	"bytes"
	"context"
	"errors"
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
)

// TestRemovedSQLiteFlag_AcceptedWithDeprecationWarning pins the #3268
// follow-up: a command line that still carries the removed
// --enable-sqlite3-i-know-the-risks flag must parse through the real Execute
// path (before this, cobra failed startup with "unknown flag"), print the
// deprecation warning, and stay out of --help.
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
}

func TestFormatMetadataValue(t *testing.T) {
	if got := formatMetadataValue("  "); got != "(empty)" {
		t.Fatalf("expected empty placeholder, got %q", got)
	}
	if got := formatMetadataValue("Title"); got != "Title" {
		t.Fatalf("expected value passthrough, got %q", got)
	}
}

// isolateInitConfig gives initConfig a process with no operator settings: a
// temporary HOME (no HEAD home file) and state directory, the root flags
// unset, and AppConfig and the store tunables restored afterwards.
func isolateInitConfig(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv(config.SecureStateDirEnv, filepath.Join(t.TempDir(), "state"))
	t.Chdir(t.TempDir())
	origCfgFile, origDBPath, origPlaylistDir, origRootDir := cfgFile, databasePath, playlistDir, rootDir
	orig := config.Snapshot()
	t.Cleanup(func() {
		cfgFile, databasePath, playlistDir, rootDir = origCfgFile, origDBPath, origPlaylistDir, origRootDir
		for _, name := range []string{"config", "db", "dir", "playlists", "log-level"} {
			if fl := rootCmd.PersistentFlags().Lookup(name); fl != nil {
				_ = fl.Value.Set(fl.DefValue)
				fl.Changed = false
			}
		}
		config.Project(orig)
		config.SetOperatorRootDir("")
		database.SetEnabledSortIndexes(nil)
	})
	playlistDir, databasePath = "", ""
}

// setRootFlag sets a root persistent flag as the command line would.
func setRootFlag(t *testing.T, name, value string) {
	t.Helper()
	if err := rootCmd.PersistentFlags().Set(name, value); err != nil {
		t.Fatal(err)
	}
}

// TestInitConfig_ProjectsTheOperatorLayers (F3-07 slice 3): AppConfig is the
// projection of the operator layers: the canonical environment, the flags set
// (--log-level as core.log.level, --dir as the library root), an operator
// secret through its kept ecosystem name, and the defaults for the rest.
func TestInitConfig_ProjectsTheOperatorLayers(t *testing.T) {
	isolateInitConfig(t)
	t.Setenv("ABO_BOOKS_PLAYLIST_DIR", "/srv/gotham/playlists")
	t.Setenv("OPENAI_API_KEY", "sk-joker-test")
	setRootFlag(t, "log-level", "debug")
	setRootFlag(t, "dir", "/srv/gotham/library")

	if err := initConfig(); err != nil {
		t.Fatal(err)
	}
	got := config.Snapshot()
	if got.PlaylistDir != "/srv/gotham/playlists" {
		t.Errorf("PlaylistDir = %q, want the environment's", got.PlaylistDir)
	}
	if got.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want --log-level's debug", got.LogLevel)
	}
	if got.OpenAIAPIKey != "sk-joker-test" {
		t.Errorf("OpenAIAPIKey not projected from OPENAI_API_KEY (len %d)", len(got.OpenAIAPIKey))
	}
	if got.RootDir != "/srv/gotham/library" {
		t.Errorf("RootDir = %q, want --dir", got.RootDir)
	}
	if v, ok := config.RootDirOverride(); !ok || v != "/srv/gotham/library" {
		t.Errorf("RootDirOverride = %q, %v; want --dir", v, ok)
	}
	if got.DatabaseType != "pebble" {
		t.Errorf("DatabaseType = %q, want the base's pebble", got.DatabaseType)
	}
}

// TestInitConfig_NoOperatorRootIsNoOverride: with no layer naming a root, the
// offline reset plan keeps the stored one.
func TestInitConfig_NoOperatorRootIsNoOverride(t *testing.T) {
	isolateInitConfig(t)
	config.SetOperatorRootDir("/stale")
	if err := initConfig(); err != nil {
		t.Fatal(err)
	}
	if v, ok := config.RootDirOverride(); ok {
		t.Errorf("RootDirOverride = %q with no operator root", v)
	}
}

// TestInitConfig_RefusesARetiredName: a retired name that refuses the boot
// refuses initConfig, naming it, before any directory is created.
func TestInitConfig_RefusesARetiredName(t *testing.T) {
	isolateInitConfig(t)
	t.Setenv("PLAYLIST_DIR", "/srv/gotham/playlists")
	playlistDir = "made-by-init"
	err := initConfig()
	if err == nil || !strings.Contains(err.Error(), "PLAYLIST_DIR") {
		t.Fatalf("initConfig = %v, want a refusal naming PLAYLIST_DIR", err)
	}
	if _, serr := os.Stat("made-by-init"); !errors.Is(serr, os.ErrNotExist) {
		t.Errorf("a refused initConfig created the playlist directory (%v)", serr)
	}
}

// TestConfigCheck_ReportsWhatInitConfigRefuses: config check does not run the
// config load, so a boot-refusing retired name is reported, not hidden behind
// the load's refusal.
func TestConfigCheck_ReportsWhatInitConfigRefuses(t *testing.T) {
	isolateInitConfig(t)
	t.Setenv("PLAYLIST_DIR", "/srv/gotham/playlists")
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"config", "check"})
	t.Cleanup(func() { rootCmd.SetArgs(nil); rootCmd.SetOut(nil); rootCmd.SetErr(nil) })
	err := rootCmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "would stop the service from starting") {
		t.Fatalf("config check = %v, want its own refusal count\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "PLAYLIST_DIR") || !strings.Contains(out.String(), "ABO_BOOKS_PLAYLIST_DIR") {
		t.Errorf("config check did not report PLAYLIST_DIR and its successor:\n%s", out.String())
	}
	if _, serr := os.Stat(os.Getenv(config.SecureStateDirEnv)); !errors.Is(serr, os.ErrNotExist) {
		t.Errorf("config check created the state directory (%v)", serr)
	}
	// No filesystem side effect at all: HEAD's initConfig made ./playlists
	// (rehearsal finding 4).
	ents, rerr := os.ReadDir(".")
	if rerr != nil {
		t.Fatal(rerr)
	}
	for _, e := range ents {
		t.Errorf("config check created %q in the working directory", e.Name())
	}
}

// TestConfigCheck_CleanLayersCreateNothing: with nothing retired set, config
// check passes and still creates nothing in the working directory.
func TestConfigCheck_CleanLayersCreateNothing(t *testing.T) {
	isolateInitConfig(t)
	playlistDir = "playlists"
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"config", "check"})
	t.Cleanup(func() { rootCmd.SetArgs(nil); rootCmd.SetOut(nil); rootCmd.SetErr(nil) })
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("config check = %v\n%s", err, out.String())
	}
	ents, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		t.Errorf("config check created %q in the working directory", e.Name())
	}
}

// enabled_sort_indexes is boot-only, so what GET /api/v1/config reports must
// be the set the store builds: initConfig writes back the accepted names, with
// an unknown entry dropped rather than reported as an enabled index.
func TestInitConfigReportsTheBuiltSortIndexSet(t *testing.T) {
	isolateInitConfig(t)
	t.Setenv("ABO_BOOKS_SEARCH_ENABLED_SORT_INDEXES", `["Year","bogus","created_at","year"]`)
	if err := initConfig(); err != nil {
		t.Fatal(err)
	}
	got := config.Snapshot().EnabledSortIndexes
	if strings.Join(got, ",") != "year,created_at" {
		t.Fatalf("AppConfig.EnabledSortIndexes = %#v, want [year created_at] (the built set)", got)
	}
	if !database.CanPushDownSort("created_at") || !database.CanPushDownSort("year") {
		t.Fatal("year and created_at should be push-downable after initConfig")
	}
}

func TestInitConfigCreatesDirectories(t *testing.T) {
	isolateInitConfig(t)
	tempDir := t.TempDir()
	databasePath = filepath.Join(tempDir, "db", "test.db")
	playlistDir = filepath.Join(tempDir, "playlists")

	if err := initConfig(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(databasePath)); err != nil {
		t.Fatalf("expected database directory to exist: %v", err)
	}
	if _, err := os.Stat(playlistDir); err != nil {
		t.Fatalf("expected playlist directory to exist: %v", err)
	}
}

// TestInitConfigRefusesTheHEADHomeFile: HEAD read ~/.audiobook-organizer.yaml
// by default; nothing reads it now, so its settings would be dropped without
// a word. initConfig refuses, naming the file.
func TestInitConfigRefusesTheHEADHomeFile(t *testing.T) {
	isolateInitConfig(t)
	home := os.Getenv("HOME")
	if err := os.WriteFile(filepath.Join(home, ".audiobook-organizer.yaml"), []byte("root_dir: /tmp\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := initConfig(); err == nil || !strings.Contains(err.Error(), ".audiobook-organizer.yaml") {
		t.Fatalf("initConfig = %v, want a refusal naming the home file", err)
	}
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

	origConfig := config.Snapshot()
	defer func() {
		config.ReplaceForTest(origConfig)
	}()

	config.SetForTest(func(c *config.Config) { c.RootDir = "" })

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
	origConfig := config.Snapshot()
	t.Cleanup(func() { config.ReplaceForTest(origConfig) })
	cowV1 := t.TempDir()
	config.SetForTest(func(c *config.Config) { c.RootDir = cowV1 })
	cowV2 := t.TempDir()
	config.SetForTest(func(c *config.Config) { c.PlaylistDir = cowV2 })
	cowV3 := filepath.Join(t.TempDir(), "db")
	config.SetForTest(func(c *config.Config) { c.DatabasePath = cowV3 })

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
			config.SetForTest(func(c *config.Config) { c.LogLevel = tc.level })
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
	origConfig := config.Snapshot()
	t.Cleanup(func() { config.ReplaceForTest(origConfig) })
	tempDir := t.TempDir()
	config.SetForTest(func(c *config.Config) { c.DatabaseType = "sqlite" })
	cowV4 := filepath.Join(tempDir, "db.sqlite")
	config.SetForTest(func(c *config.Config) { c.DatabasePath = cowV4 })
	config.SetForTest(func(c *config.Config) { c.RootDir = tempDir })
	cowV5 := filepath.Join(tempDir, "playlists")
	config.SetForTest(func(c *config.Config) { c.PlaylistDir = cowV5 })
	config.SetForTest(func(c *config.Config) { c.LogLevel = "debug" })

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
