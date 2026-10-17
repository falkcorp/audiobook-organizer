// file: internal/config/removed_keys_test.go
// version: 1.7.0
// guid: 9e2b6d41-7c3a-4f58-b0d9-2a61c8e4f735
// last-edited: 2026-10-17

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
		c.RootDir = ""
	})
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("root_dir: /de/books\nenable_sqlite: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := LoadConfigFromFile(); err != nil {
		t.Fatalf("LoadConfigFromFile: %v", err)
	}
	if got := Snapshot().RootDir; got != "/de/books" {
		t.Errorf("root_dir = %q, want /de/books — the file did not load", got)
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

// TestRemovedAutoFetchMetadata_WarnsOnLoad: auto_fetch_metadata was removed
// 2026-09-12. A config file still carrying it loads, with a removed-setting WARN.
func TestRemovedAutoFetchMetadata_WarnsOnLoad(t *testing.T) {
	restoreAppConfig(t)
	viper.Reset()
	t.Cleanup(viper.Reset)
	logs := captureRemovedKeyLogs(t)

	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("root_dir: /srv/books\nauto_fetch_metadata: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	viper.SetConfigFile(cfgPath)
	if err := viper.ReadInConfig(); err != nil {
		t.Fatalf("ReadInConfig: %v", err)
	}
	InitConfig()

	if got := AppConfig.RootDir; got != "/srv/books" {
		t.Errorf("root_dir = %q, want /srv/books", got)
	}
	out := logs.String()
	if !strings.Contains(out, "key=auto_fetch_metadata ") || !strings.Contains(out, "removed setting is ignored") {
		t.Errorf("no removed-setting WARN for auto_fetch_metadata in logs:\n%s", out)
	}
}

// TestRemovedWriteBackupBefore_WarnsOnLoad: metadata_scoring.write_backup_before
// was removed 2026-10-10 (create_backups is the one backup switch). A config
// file still carrying the nested key loads, with a removed-setting WARN, and a
// config update carrying the flat legacy name is refused.
func TestRemovedWriteBackupBefore_WarnsOnLoad(t *testing.T) {
	restoreAppConfig(t)
	viper.Reset()
	t.Cleanup(viper.Reset)
	logs := captureRemovedKeyLogs(t)

	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("root_dir: /srv/books\nmetadata_scoring:\n  write_backup_before: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	viper.SetConfigFile(cfgPath)
	if err := viper.ReadInConfig(); err != nil {
		t.Fatalf("ReadInConfig: %v", err)
	}
	InitConfig()

	out := logs.String()
	if !strings.Contains(out, "key=metadata_scoring.write_backup_before ") || !strings.Contains(out, "removed setting is ignored") {
		t.Errorf("no removed-setting WARN for metadata_scoring.write_backup_before in logs:\n%s", out)
	}
	if msg, ok := removedKeyInUpdate(map[string]any{"write_backup_before_tag_write": true}); !ok || !strings.Contains(msg, "create_backups") {
		t.Errorf("update with write_backup_before_tag_write: ok=%v msg=%q, want a refusal naming create_backups", ok, msg)
	}
}

// A nested write_backup_before -- the shape a Settings page loaded before the
// removal, or a settings export taken then, sends -- is refused with the
// removed-setting message, not unknownConfigKeys' generic one.
func TestUpdateConfig_RejectsNestedRemovedWriteBackupBefore(t *testing.T) {
	restoreAppConfig(t)
	ms, blobs := ladderTestStore(t)
	svc := NewUpdateService(ms)

	status, resp := svc.UpdateConfig(context.Background(), map[string]any{
		"metadata_scoring": map[string]any{"write_backup_before": true, "llm_enabled": false},
	})
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; resp = %v", status, resp)
	}
	msg, _ := resp["error"].(string)
	for _, want := range []string{"metadata_scoring.write_backup_before", "was removed", "create_backups"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not mention %q", msg, want)
		}
	}
	if _, generic := resp["unknown_keys"]; generic {
		t.Errorf("refused as a generic unknown key, want the removed-setting message: %v", resp)
	}
	if len(*blobs) != 0 {
		t.Errorf("rejected PUT persisted %d blob(s)", len(*blobs))
	}
}

// The config.yaml beside the database warns about a nested removed key too.
func TestLoadConfigFromFile_NestedRemovedKeyWarns(t *testing.T) {
	restoreAppConfig(t)
	logs := captureRemovedKeyLogs(t)

	dir := t.TempDir()
	Mutate(func(c *Config) { c.DatabasePath = filepath.Join(dir, "audiobooks.pebble") })
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"),
		[]byte("metadata_scoring:\n  write_backup_before: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadConfigFromFile(); err != nil {
		t.Fatalf("LoadConfigFromFile: %v", err)
	}
	if out := logs.String(); !strings.Contains(out, "level=WARN") || !strings.Contains(out, "key=metadata_scoring.write_backup_before ") {
		t.Errorf("expected a removed-setting WARN for metadata_scoring.write_backup_before, got:\n%s", out)
	}
}

func TestHasConfigKey(t *testing.T) {
	m := map[string]any{
		"flat.dotted": 1,
		"a":           map[string]any{"b": map[string]any{"c": nil}},
		"scalar":      3,
	}
	for _, tc := range []struct {
		key  string
		want bool
	}{
		{"flat.dotted", true},
		{"a.b.c", true},
		{"a.b", true},
		{"a.x", false},
		{"scalar.x", false},
		{"missing", false},
	} {
		if got := hasConfigKey(m, tc.key); got != tc.want {
			t.Errorf("hasConfigKey(%q) = %v, want %v", tc.key, got, tc.want)
		}
	}
}

// The qBittorrent and SABnzbd blocks were removed 2026-10-17 (01-P76). A config
// file still carrying them loads with a removed-setting WARN per block, and a
// config update carrying them gets the removal message, not the generic
// unknown-key one.
func TestRemovedDownloadClientKeys_WarnAndRefuse(t *testing.T) {
	restoreAppConfig(t)
	viper.Reset()
	t.Cleanup(viper.Reset)
	logs := captureRemovedKeyLogs(t)

	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	yamlText := "download_client:\n  torrent:\n    deluge:\n      host: 192.0.2.10\n" +
		"    qbittorrent:\n      host: 192.0.2.11\n      password: qbit-test-pass\n" +
		"  usenet:\n    sabnzbd:\n      api_key: sab-test-key\n"
	if err := os.WriteFile(cfgPath, []byte(yamlText), 0o600); err != nil {
		t.Fatal(err)
	}
	viper.SetConfigFile(cfgPath)
	if err := viper.ReadInConfig(); err != nil {
		t.Fatalf("ReadInConfig: %v", err)
	}
	InitConfig()

	out := logs.String()
	for _, key := range []string{"download_client.torrent.qbittorrent", "download_client.usenet"} {
		if !strings.Contains(out, "key="+key+" ") || !strings.Contains(out, "removed setting is ignored") {
			t.Errorf("no removed-setting WARN for %s in logs:\n%s", key, out)
		}
	}
	if strings.Contains(out, "key=download_client.torrent.deluge") {
		t.Errorf("the kept Deluge block was reported as removed:\n%s", out)
	}

	for _, payload := range []map[string]any{
		{"download_client": map[string]any{"torrent": map[string]any{"qbittorrent": map[string]any{"host": "192.0.2.11"}}}},
		{"download_client": map[string]any{"usenet": map[string]any{"type": "sabnzbd"}}},
	} {
		msg, ok := removedKeyInUpdate(payload)
		if !ok || !strings.Contains(msg, "download-client settings were removed") {
			t.Errorf("update %v: ok=%v msg=%q, want the removal message", payload, ok, msg)
		}
	}
	if _, ok := removedKeyInUpdate(map[string]any{"download_client": map[string]any{
		"torrent": map[string]any{"deluge": map[string]any{"host": "192.0.2.10"}}}}); ok {
		t.Error("an update touching only the kept Deluge settings was refused")
	}
}
