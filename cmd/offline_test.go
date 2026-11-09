// file: cmd/offline_test.go
// version: 1.0.0
// guid: 6dbaed6c-e250-472b-85cd-333d35cfe245
// last-edited: 2026-11-08

package cmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/viper"

	"github.com/falkcorp/audiobook-organizer/internal/books"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/mediacore/app"
)

// TestCLIExposesCoreOfflineCommands (06 §6.4.5, R9-9, COR9-6; ABO from R1):
// every entry of app.OfflineCommands this build has is a command of ABO's
// CLI at its path, with core's help; and storage prune-checkpoints runs
// through the real Execute path against the store --db names, deleting a
// completed checkpoint beside it, with its own flags handed to core.
func TestCLIExposesCoreOfflineCommands(t *testing.T) {
	cmds := app.OfflineCommands(app.Options{AppID: books.AppID}, books.Module())
	if !slices.ContainsFunc(cmds, func(c app.Command) bool { return c.Name == "storage prune-checkpoints" }) {
		t.Fatalf("app.OfflineCommands lacks storage prune-checkpoints: %+v", cmds)
	}
	for _, c := range cmds {
		got, rest, err := rootCmd.Find(strings.Fields(c.Name))
		if err != nil || len(rest) != 0 || got.CommandPath() != "audiobook-organizer "+c.Name || got.Short != c.Short {
			t.Errorf("%s: the CLI has %q (rest %v, %v), want it with core's help", c.Name, got.CommandPath(), rest, err)
		}
	}

	root := t.TempDir()
	stateDir, dbPath := filepath.Join(root, "state"), filepath.Join(root, "db", "books.pebble")
	const name = "20260301T000000Z-e9b1e001"
	cps := filepath.Join(filepath.Dir(dbPath), ".mediacore-checkpoints")
	if err := os.MkdirAll(filepath.Join(cps, name, "db"), 0o700); err != nil {
		t.Fatal(err)
	}
	rec := `{"install_id":"wayne-install","epoch":1,"taken_at":"2026-03-01T00:00:00Z","completed_at":"2026-03-01T00:00:00Z","delete_after":"2026-03-08T00:00:00Z","status":"completed"}`
	if err := os.WriteFile(filepath.Join(cps, name+".json"), []byte(rec), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(cfg, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.SecureStateDirEnv, stateDir)
	origCfgFile, origDB, origConfig := cfgFile, databasePath, config.AppConfig
	var out bytes.Buffer
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		cfgFile, databasePath, config.AppConfig = origCfgFile, origDB, origConfig
		for _, f := range []string{"config", "db"} {
			if fl := rootCmd.PersistentFlags().Lookup(f); fl != nil {
				fl.Changed = false
			}
		}
		viper.Reset()
	})
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	// Another test's viper.Reset drops init's flag bindings; restore them,
	// as a fresh process has them.
	for flagName, key := range persistentFlagConfigKeys {
		if err := viper.BindPFlag(key, rootCmd.PersistentFlags().Lookup(flagName)); err != nil {
			t.Fatal(err)
		}
	}

	rootCmd.SetArgs([]string{"storage", "prune-checkpoints", "--include-open", "--config", cfg, "--db", dbPath})
	if err := rootCmd.Execute(); err == nil || !strings.Contains(err.Error(), "--confirm") {
		t.Fatalf("prune --include-open = %v, want core's refusal (the flag reached core)", err)
	}
	rootCmd.SetArgs([]string{"storage", "prune-checkpoints", "--config", cfg, "--db", dbPath})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("prune = %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "deleted checkpoint "+name) {
		t.Fatalf("prune output lacks the deletion:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(cps, name+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the record survived the prune (%v)", err)
	}
}

// TestSplitFlags: the root's persistent flags are taken out of an offline
// command's arguments with their values, in every spelling; the rest,
// and everything after "--", stay the command's.
func TestSplitFlags(t *testing.T) {
	fs := rootCmd.PersistentFlags()
	own, inherited := splitFlags(fs, []string{"--include-open", "--db", "/batcave/db", "--confirm", "--db-type=pebble", "--", "--db", "x"})
	if want := []string{"--include-open", "--confirm", "--", "--db", "x"}; !slices.Equal(own, want) {
		t.Errorf("own = %v, want %v", own, want)
	}
	if want := []string{"--db", "/batcave/db", "--db-type=pebble"}; !slices.Equal(inherited, want) {
		t.Errorf("inherited = %v, want %v", inherited, want)
	}
}
