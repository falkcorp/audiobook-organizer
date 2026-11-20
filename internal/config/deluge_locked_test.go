// file: internal/config/deluge_locked_test.go
// version: 1.7.0
// guid: 8e2c4a17-5b9d-4f63-a1d8-0c7e3f95b264
// last-edited: 2026-11-09

// The five Deluge connection settings are config/env-authoritative (owner
// decision 2026-10-11): a value the environment (DELUGE_*) or the server's config
// file supplies beats the stored settings blob, INCLUDING a stored empty value, and
// the UI shows the field locked. With neither set the stored value wins as before.
//
// Hosts are synthetic (192.0.2.0/24, RFC 5737); the passwords are throwaway strings.

package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/spf13/viper"
)

// delugeInitConfig runs InitConfig the way cmd/root.go does (AutomaticEnv on),
// optionally with a config file whose YAML is fileYAML, written to a temp file and
// read with ReadInConfig so viper.ConfigFileUsed names it. It returns that path ("" when
// there is no file).
func delugeInitConfig(t *testing.T, fileYAML string) string {
	t.Helper()
	restoreAppConfig(t)
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.AutomaticEnv()
	path := ""
	if fileYAML != "" {
		path = filepath.Join(t.TempDir(), "audiobook-organizer.yaml")
		if err := os.WriteFile(path, []byte(fileYAML), 0o600); err != nil {
			t.Fatalf("write config file fixture: %v", err)
		}
		viper.SetConfigFile(path)
		if err := viper.ReadInConfig(); err != nil {
			t.Fatalf("read config file fixture: %v", err)
		}
	}
	InitConfig()
	return path
}

func clearDelugeEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"DELUGE_WEB_URL", "DELUGE_WEB_PASSWORD", "DELUGE_DISCOVERY_LABEL",
		"DELUGE_DISCOVERY_ENABLED", "DELUGE_MOVE_ENABLED"} {
		t.Setenv(k, "")
	}
}

var delugeKeys = []string{"deluge_web_url", "deluge_web_password", "deluge_discovery_label",
	"deluge_discovery_enabled", "deluge_move_enabled"}

// A key that can be locked by the environment, but is not a Deluge key, must not be
// locked by merely appearing in the config file: the file is a lock source for the
// Deluge keys only.
func TestDelugeLock_ConfigFileDoesNotLockNonDelugeKeys(t *testing.T) {
	clearDelugeEnv(t)
	t.Setenv("ACTIVITY_BACKEND", "")
	t.Setenv("DATABASE_PATH", "")
	delugeInitConfig(t, "activity_backend: pebble\ndatabase_path: /srv/db-file\n")

	for _, k := range []string{"activity_backend", "database_path"} {
		if got := SettingLockedBy(k); got != "" {
			t.Errorf("SettingLockedBy(%s) = %q, want \"\"", k, got)
		}
	}
}

// Every key in the lock tables is a real Config JSON field, and every Deluge key is
// lockable from the config file (the second table must not drift from the first).
func TestDelugeLock_TablesAreConsistent(t *testing.T) {
	fields := configJSONFieldNames(t)
	for _, k := range delugeKeys {
		if _, ok := envLockedSettings[k]; !ok {
			t.Errorf("%s is missing from envLockedSettings", k)
		}
		if !configFileLockedSettings[k] {
			t.Errorf("%s is missing from configFileLockedSettings", k)
		}
		if !slices.Contains(fields, k) {
			t.Errorf("%s is not a json field on Config", k)
		}
	}
	for k := range configFileLockedSettings {
		if _, ok := envLockedSettings[k]; !ok {
			t.Errorf("configFileLockedSettings key %q has no entry in envLockedSettings", k)
		}
	}
}
