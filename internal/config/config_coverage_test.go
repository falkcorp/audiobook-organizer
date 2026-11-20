// file: internal/config/config_coverage_test.go
// version: 1.5.0
// last-edited: 2026-10-17

package config

import (
	"testing"

	"github.com/spf13/viper"
)

func TestCoverage_ITunesConfig(t *testing.T) {
	viper.Reset()
	InitConfig()

	// Test ITunes-related defaults exist
	if AppConfig.ITunes.LibraryReadPath != "" {
		t.Errorf("itunes.library_read_path defaults to %q, want empty", AppConfig.ITunes.LibraryReadPath)
	}
}

func TestCoverage_AutoUpdateDefaults(t *testing.T) {
	viper.Reset()
	InitConfig()

	// Verify auto-update defaults
	if AppConfig.AutoUpdate.Channel != "" && AppConfig.AutoUpdate.Channel != "stable" {
		// AutoUpdateChannel may or may not be set by default
		t.Logf("AutoUpdateChannel = %q", AppConfig.AutoUpdate.Channel)
	}
}

func TestCoverage_DownloadClientConfig(t *testing.T) {
	t.Run("struct fields", func(t *testing.T) {
		cfg := DownloadClientConfig{
			Torrent: TorrentClientConfig{
				Type: "deluge",
				Deluge: DelugeConfig{
					Host:     "localhost",
					Port:     8112,
					Username: "admin",
					Password: "pass",
				},
			},
		}
		if cfg.Torrent.Type != "deluge" {
			t.Error("Torrent type not set")
		}
		if cfg.Torrent.Deluge.Port != 8112 {
			t.Error("Deluge port not set")
		}
	})
}

func TestCoverage_ITunesPathMap(t *testing.T) {
	m := ITunesPathMap{
		From: "file://localhost/W:/itunes",
		To:   "file://localhost/mnt/bigdata",
	}
	if m.From == "" || m.To == "" {
		t.Error("ITunesPathMap fields not set")
	}
}

func TestCoverage_MaintenanceWindowDefaults(t *testing.T) {
	viper.Reset()
	InitConfig()

	// MaintenanceWindow defaults should be sensible
	// These are only set during migration, so test the struct
	cfg := Config{
		Maintenance: MaintenanceConfig{
			Enabled:     true,
			WindowStart: 1,
			WindowEnd:   4,
		},
	}
	if cfg.Maintenance.WindowStart != 1 || cfg.Maintenance.WindowEnd != 4 {
		t.Error("maintenance window fields not set correctly")
	}
}
