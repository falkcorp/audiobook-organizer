// file: internal/config/removed_keys_test.go
// version: 1.2.0
// guid: 9e2b6d41-7c3a-4f58-b0d9-2a61c8e4f735
// last-edited: 2026-09-12

package config

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/mock"
)

// captureRemovedKeyLogs routes slog to a buffer for the duration of the test.
func captureRemovedKeyLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// TestUpdateConfig_RejectsRemovedEnableSQLite is the owner decision on
// TASK-020: a PUT carrying enable_sqlite gets 400 for ANY value — it got 400
// while the field was immutable, and dropping it silently would answer 200 for
// a setting that does not exist. Nothing may be persisted.
//
// Mutation check: delete the removedKeyInUpdate call in UpdateConfig and every
// case fails with 200 (the JSON round-trip drops the unknown key).
func TestUpdateConfig_RejectsRemovedEnableSQLite(t *testing.T) {
	for _, val := range []any{true, false, "true", nil} {
		t.Run("", func(t *testing.T) {
			restoreAppConfig(t)
			ms, blobs := ladderTestStore(t)
			svc := NewUpdateService(ms)

			status, resp := svc.UpdateConfig(context.Background(), map[string]any{"enable_sqlite": val, "root_dir": "/lib"})
			if status != http.StatusBadRequest {
				t.Fatalf("enable_sqlite=%v: status = %d, want 400; resp = %v", val, status, resp)
			}
			msg, _ := resp["error"].(string)
			for _, want := range []string{"enable_sqlite", "--enable-sqlite3-i-know-the-risks", "was removed", "SQLite is no longer selectable"} {
				if !strings.Contains(msg, want) {
					t.Errorf("enable_sqlite=%v: error %q does not mention %q", val, msg, want)
				}
			}
			if len(*blobs) != 0 {
				t.Errorf("enable_sqlite=%v: rejected PUT persisted %d blob(s)", val, len(*blobs))
			}
			if got := Snapshot().RootDir; got == "/lib" {
				t.Errorf("enable_sqlite=%v: rejected PUT still applied root_dir", val)
			}
		})
	}
}

// TestUpdateConfig_WithoutRemovedKeyUnaffected guards the other side: a PUT
// that does not carry a removed key behaves exactly as before.
func TestUpdateConfig_WithoutRemovedKeyUnaffected(t *testing.T) {
	restoreAppConfig(t)
	ms, blobs := ladderTestStore(t)
	svc := NewUpdateService(ms)

	if status, resp := svc.UpdateConfig(context.Background(), map[string]any{"root_dir": "/lib"}); status != http.StatusOK {
		t.Fatalf("status = %d; resp = %v", status, resp)
	}
	if got := Snapshot().RootDir; got != "/lib" {
		t.Errorf("root_dir = %q, want /lib", got)
	}
	if len(*blobs) == 0 {
		t.Error("accepted PUT persisted no blob")
	}
}

// TestUpdateConfig_ViperOnlyRemovedKeyRejected: enable_sqlite3_i_know_the_risks
// was only ever the flag's viper/env key, never a config-API field, so a PUT
// carrying it used to be dropped silently and answered 200. Since unknown keys
// are refused, it gets a 400 — and the registry's removal message, which says
// what to do, rather than the generic unknown-key text.
func TestUpdateConfig_ViperOnlyRemovedKeyRejected(t *testing.T) {
	restoreAppConfig(t)
	ms, blobs := ladderTestStore(t)
	svc := NewUpdateService(ms)

	status, resp := svc.UpdateConfig(context.Background(), map[string]any{"enable_sqlite3_i_know_the_risks": true})
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; resp = %v", status, resp)
	}
	if msg, _ := resp["error"].(string); !strings.Contains(msg, "was removed") {
		t.Errorf("error %q is not the removal message", msg)
	}
	if len(*blobs) != 0 {
		t.Error("rejected PUT persisted a blob")
	}
}

// TestInitConfig_RemovedKeysInConfigFileWarnAndLoad covers the viper path
// cmd's initConfig uses: a config file still carrying either spelling of the
// removed setting must load (other keys applied) and log a WARN per key.
func TestInitConfig_RemovedKeysInConfigFileWarnAndLoad(t *testing.T) {
	restoreAppConfig(t)
	viper.Reset()
	t.Cleanup(viper.Reset)
	logs := captureRemovedKeyLogs(t)

	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	body := "root_dir: /srv/books\nenable_sqlite: true\nenable_sqlite3_i_know_the_risks: true\n"
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	viper.SetConfigFile(cfgPath)
	if err := viper.ReadInConfig(); err != nil {
		t.Fatalf("ReadInConfig: %v", err)
	}

	InitConfig()

	if got := AppConfig.RootDir; got != "/srv/books" {
		t.Errorf("root_dir = %q, want /srv/books — the file did not load", got)
	}
	out := logs.String()
	for _, key := range []string{"key=enable_sqlite ", "key=enable_sqlite3_i_know_the_risks "} {
		if !strings.Contains(out, key) {
			t.Errorf("no WARN for %s in logs:\n%s", strings.TrimSpace(key), out)
		}
	}
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "removed setting is ignored") {
		t.Errorf("expected a removed-setting WARN, got:\n%s", out)
	}
}

// TestLoadConfigFromFile_RemovedKeyWarnsAndLoads covers the config.yaml that
// lives next to the database.
func TestLoadConfigFromFile_RemovedKeyWarnsAndLoads(t *testing.T) {
	restoreAppConfig(t)
	logs := captureRemovedKeyLogs(t)

	dir := t.TempDir()
	Mutate(func(c *Config) {
		c.DatabasePath = filepath.Join(dir, "audiobooks.pebble")
		c.Language = ""
	})
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("language: de\nenable_sqlite: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := LoadConfigFromFile(); err != nil {
		t.Fatalf("LoadConfigFromFile: %v", err)
	}
	if got := Snapshot().Language; got != "de" {
		t.Errorf("language = %q, want de — the file did not load", got)
	}
	if out := logs.String(); !strings.Contains(out, "level=WARN") || !strings.Contains(out, "key=enable_sqlite ") {
		t.Errorf("expected a removed-setting WARN for enable_sqlite, got:\n%s", out)
	}
}

// TestLoadConfigFromDatabase_LegacyRemovedKeyRowWarns covers installs still on
// per-key settings rows: a row named enable_sqlite logs the removed-setting
// WARN (not the generic "unknown setting key") and loading still succeeds.
func TestLoadConfigFromDatabase_LegacyRemovedKeyRowWarns(t *testing.T) {
	restoreAppConfig(t)
	logs := captureRemovedKeyLogs(t)

	store := mocks.NewMockStore(t)
	setupMigrationExpectations(store)
	store.On("GetSetting", mock.Anything).Return((*database.Setting)(nil), nil).Maybe()
	store.On("SetSetting", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	store.EXPECT().GetAllSettings().Return([]database.Setting{
		{Key: "enable_sqlite", Value: "true", Type: "bool"},
	}, nil).Once()

	if err := LoadConfigFromDatabase(store); err != nil {
		t.Fatalf("LoadConfigFromDatabase: %v", err)
	}
	out := logs.String()
	if !strings.Contains(out, "removed setting is ignored") || !strings.Contains(out, "key=enable_sqlite ") {
		t.Errorf("expected a removed-setting WARN, got:\n%s", out)
	}
	if strings.Contains(out, "unknown setting key: enable_sqlite") {
		t.Errorf("legacy row fell through to the generic unknown-key path:\n%s", out)
	}
}

// TestInitConfig_RemovedKeyInEnvironmentWarns verifies the "or environment"
// half of warnRemovedViperKeys: cmd's initConfig calls viper.AutomaticEnv()
// before config.InitConfig, and under AutomaticEnv viper.IsSet sees an
// unbound env var, so ENABLE_SQLITE3_I_KNOW_THE_RISKS left in a systemd unit
// logs the removed-setting WARN instead of vanishing silently.
func TestInitConfig_RemovedKeyInEnvironmentWarns(t *testing.T) {
	restoreAppConfig(t)
	viper.Reset()
	t.Cleanup(viper.Reset)
	logs := captureRemovedKeyLogs(t)

	t.Setenv("ENABLE_SQLITE3_I_KNOW_THE_RISKS", "true")
	viper.AutomaticEnv() // same order as cmd/root.go initConfig

	InitConfig()

	if out := logs.String(); !strings.Contains(out, "key=enable_sqlite3_i_know_the_risks ") || !strings.Contains(out, "removed setting is ignored") {
		t.Errorf("expected a removed-setting WARN for the env var, got:\n%s", out)
	}
}

// TestInitConfig_NoRemovedKeyNoWarning guards against a viper default for a
// removed key creeping back: IsSet would then be true on every boot and the
// WARN would fire for a value nobody set.
func TestInitConfig_NoRemovedKeyNoWarning(t *testing.T) {
	restoreAppConfig(t)
	viper.Reset()
	t.Cleanup(viper.Reset)
	logs := captureRemovedKeyLogs(t)

	InitConfig()

	if out := logs.String(); strings.Contains(out, "removed setting is ignored") {
		t.Errorf("removed-setting WARN fired with no removed key set:\n%s", out)
	}
}
