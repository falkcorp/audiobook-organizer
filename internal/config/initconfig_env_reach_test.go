// file: internal/config/initconfig_env_reach_test.go
// version: 1.0.0
// guid: 5a8e1d37-c4b2-4f96-8e03-71d9b6c2a5f0
// last-edited: 2026-10-11

// End-to-end checks, through the environment, for settings InitConfig used
// to drop. TestInitConfigReadsEveryConfigKey covers every key through
// viper.Set (the config-file path); these pin the environment path for the
// ones an operator is told to set there, and the precedence the stored blob
// keeps over both.

package config

import (
	"testing"

	"github.com/spf13/viper"
)

// envInitConfig runs InitConfig the way cmd/root.go does: AutomaticEnv on.
func envInitConfig(t *testing.T) {
	t.Helper()
	restoreAppConfig(t)
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.AutomaticEnv()
	InitConfig()
}

// DelugeSettingsTab tells operators to "Set deluge_web_url in server config or
// environment". Before 2026-10-11 neither reached AppConfig.
func TestInitConfigReadsDelugeFromEnvironment(t *testing.T) {
	t.Setenv("DELUGE_WEB_URL", "http://192.0.2.10:8112")
	t.Setenv("DELUGE_WEB_PASSWORD", "pw")
	t.Setenv("DELUGE_DISCOVERY_LABEL", "audiobooks")
	t.Setenv("DELUGE_DISCOVERY_ENABLED", "true")
	t.Setenv("DELUGE_MOVE_ENABLED", "true")
	envInitConfig(t)

	c := Snapshot()
	if c.DelugeWebURL != "http://192.0.2.10:8112" || c.DelugeWebPassword != "pw" ||
		c.DelugeDiscoveryLabel != "audiobooks" || !c.DelugeDiscoveryEnabled || !c.DelugeMoveEnabled {
		t.Fatalf("deluge settings from the environment did not reach AppConfig: url=%q label=%q discovery=%v move=%v pw-set=%v",
			c.DelugeWebURL, c.DelugeDiscoveryLabel, c.DelugeDiscoveryEnabled, c.DelugeMoveEnabled, c.DelugeWebPassword != "")
	}
}

func TestInitConfigReadsAcoustIDBackfillFromEnvironment(t *testing.T) {
	t.Setenv("MAINTENANCE_ACOUSTID_BACKFILL", "true")
	envInitConfig(t)
	if !Snapshot().Maintenance.AcoustIDBackfill {
		t.Fatal("MAINTENANCE_ACOUSTID_BACKFILL=true did not reach AppConfig.Maintenance.AcoustIDBackfill")
	}
}

// A stored blob that carries the key still wins over the environment, as for
// every non-environment-authoritative setting. This is what decides prod after
// deploy: a blob holding acoustid_backfill=false keeps the backfill off even
// if the host sets MAINTENANCE_ACOUSTID_BACKFILL=true.
func TestStoredBlobBeatsEnvironmentForAcoustIDBackfill(t *testing.T) {
	t.Setenv("MAINTENANCE_ACOUSTID_BACKFILL", "true")
	t.Setenv("DELUGE_WEB_URL", "http://192.0.2.10:8112")
	envInitConfig(t)

	blob := `{"maintenance":{"acoustid_backfill":false},"deluge_web_url":"http://192.0.2.20:8112"}`
	if err := LoadConfigFromDatabase(storeWithBlob(t, blob)); err != nil {
		t.Fatalf("LoadConfigFromDatabase: %v", err)
	}
	c := Snapshot()
	if c.Maintenance.AcoustIDBackfill {
		t.Error("stored acoustid_backfill=false must win over the environment")
	}
	if c.DelugeWebURL != "http://192.0.2.20:8112" {
		t.Errorf("stored deluge_web_url must win over the environment, got %q", c.DelugeWebURL)
	}
}

// A blob that lacks the key inherits the startup (environment) value.
func TestBlobWithoutKeyInheritsEnvironmentValue(t *testing.T) {
	t.Setenv("DELUGE_WEB_URL", "http://192.0.2.10:8112")
	envInitConfig(t)
	if err := LoadConfigFromDatabase(storeWithBlob(t, `{"root_dir":"/mnt/books"}`)); err != nil {
		t.Fatalf("LoadConfigFromDatabase: %v", err)
	}
	if got := Snapshot().DelugeWebURL; got != "http://192.0.2.10:8112" {
		t.Errorf("deluge_web_url = %q, want the environment value for a blob without the key", got)
	}
}
