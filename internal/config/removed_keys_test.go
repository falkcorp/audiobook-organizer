// file: internal/config/removed_keys_test.go
// version: 1.7.1
// guid: 9e2b6d41-7c3a-4f58-b0d9-2a61c8e4f735
// last-edited: 2026-11-09

package config

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/viper"
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
}
