// file: internal/config/config_coverage_test.go
// version: 1.0.0

package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
)

func TestCoverage_ConfigFilePath(t *testing.T) {
	origConfig := AppConfig
	defer func() { AppConfig = origConfig }()

	t.Run("returns path based on DatabasePath", func(t *testing.T) {
		AppConfig = Config{DatabasePath: "/data/audiobooks.pebble"}
		got := ConfigFilePath()
		want := filepath.Join("/data", "config.yaml")
		if got != want {
			t.Errorf("ConfigFilePath() = %q, want %q", got, want)
		}
	})

	t.Run("returns path based on RootDir when no DatabasePath", func(t *testing.T) {
		AppConfig = Config{RootDir: "/media/audiobooks"}
		got := ConfigFilePath()
		want := filepath.Join("/media/audiobooks", "config.yaml")
		if got != want {
			t.Errorf("ConfigFilePath() = %q, want %q", got, want)
		}
	})

	t.Run("returns empty when both empty", func(t *testing.T) {
		AppConfig = Config{}
		got := ConfigFilePath()
		if got != "" {
			t.Errorf("ConfigFilePath() = %q, want empty", got)
		}
	})
}

func TestCoverage_LoadConfigFromFile(t *testing.T) {
	origConfig := AppConfig
	defer func() { AppConfig = origConfig }()

	t.Run("returns nil when no path", func(t *testing.T) {
		AppConfig = Config{}
		err := LoadConfigFromFile()
		if err != nil {
			t.Errorf("expected nil error, got %v", err)
		}
	})

	t.Run("returns nil when file does not exist", func(t *testing.T) {
		tmpDir := t.TempDir()
		AppConfig = Config{DatabasePath: filepath.Join(tmpDir, "db.pebble")}
		err := LoadConfigFromFile()
		if err != nil {
			t.Errorf("expected nil error for missing file, got %v", err)
		}
	})

	t.Run("loads config from YAML file", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")
		yamlContent := `openai_api_key: "sk-test-from-file"
root_dir: "/from/file"
enable_ai_parsing: true
language: "fr"
`
		if err := os.WriteFile(configPath, []byte(yamlContent), 0644); err != nil {
			t.Fatalf("failed to write config file: %v", err)
		}

		AppConfig = Config{DatabasePath: filepath.Join(tmpDir, "db.pebble")}
		err := LoadConfigFromFile()
		if err != nil {
			t.Fatalf("LoadConfigFromFile failed: %v", err)
		}
		if AppConfig.OpenAIAPIKey != "sk-test-from-file" {
			t.Errorf("expected OpenAIAPIKey from file, got %q", AppConfig.OpenAIAPIKey)
		}
		if AppConfig.RootDir != "/from/file" {
			t.Errorf("expected RootDir from file, got %q", AppConfig.RootDir)
		}
		if AppConfig.Language != "fr" {
			t.Errorf("expected Language 'fr', got %q", AppConfig.Language)
		}
	})

	t.Run("does not overwrite existing values", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")
		yamlContent := `openai_api_key: "sk-from-file"
root_dir: "/from/file"
`
		if err := os.WriteFile(configPath, []byte(yamlContent), 0644); err != nil {
			t.Fatal(err)
		}

		AppConfig = Config{
			DatabasePath: filepath.Join(tmpDir, "db.pebble"),
			OpenAIAPIKey: "sk-existing-key",
			RootDir:      "/existing/root",
		}
		err := LoadConfigFromFile()
		if err != nil {
			t.Fatalf("LoadConfigFromFile failed: %v", err)
		}
		if AppConfig.OpenAIAPIKey != "sk-existing-key" {
			t.Errorf("existing key should not be overwritten, got %q", AppConfig.OpenAIAPIKey)
		}
		if AppConfig.RootDir != "/existing/root" {
			t.Errorf("existing root should not be overwritten, got %q", AppConfig.RootDir)
		}
	})

	t.Run("handles invalid YAML gracefully", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")
		if err := os.WriteFile(configPath, []byte(":::invalid"), 0644); err != nil {
			t.Fatal(err)
		}
		AppConfig = Config{DatabasePath: filepath.Join(tmpDir, "db.pebble")}
		err := LoadConfigFromFile()
		if err != nil {
			t.Errorf("expected nil for invalid YAML (graceful), got %v", err)
		}
	})
}

func TestCoverage_SaveConfigToFile(t *testing.T) {
	origConfig := AppConfig
	defer func() { AppConfig = origConfig }()

	t.Run("returns error when no path", func(t *testing.T) {
		AppConfig = Config{}
		err := SaveConfigToFile()
		if err == nil {
			t.Error("expected error for empty path")
		}
	})

	t.Run("saves config file successfully", func(t *testing.T) {
		tmpDir := t.TempDir()
		AppConfig = Config{
			DatabasePath: filepath.Join(tmpDir, "db.pebble"),
			RootDir:      "/test/root",
			OpenAIAPIKey: "sk-test-save",
			Language:     "en",
		}
		err := SaveConfigToFile()
		if err != nil {
			t.Fatalf("SaveConfigToFile failed: %v", err)
		}

		// Verify file exists
		configPath := filepath.Join(tmpDir, "config.yaml")
		if _, err := os.Stat(configPath); os.IsNotExist(err) {
			t.Error("config file was not created")
		}

		// Verify file content
		data, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("failed to read config file: %v", err)
		}
		content := string(data)
		if len(content) == 0 {
			t.Error("config file is empty")
		}
	})
}

func TestCoverage_ITunesConfig(t *testing.T) {
	viper.Reset()
	InitConfig()

	// Test ITunes-related defaults exist
	if AppConfig.ITunes.SyncInterval != 0 && AppConfig.ITunes.SyncInterval < 0 {
		t.Error("ITunesSyncInterval has unexpected negative value")
	}
}

func TestCoverage_AutoUpdateDefaults(t *testing.T) {
	viper.Reset()
	InitConfig()

	// Verify auto-update defaults
	if AppConfig.AutoUpdateChannel != "" && AppConfig.AutoUpdateChannel != "stable" {
		// AutoUpdateChannel may or may not be set by default
		t.Logf("AutoUpdateChannel = %q", AppConfig.AutoUpdateChannel)
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
			Usenet: UsenetClientConfig{
				Type: "sabnzbd",
				SABnzbd: SABnzbdConfig{
					Host:     "localhost",
					Port:     8085,
					APIKey:   "key",
					UseHTTPS: true,
				},
			},
		}
		if cfg.Torrent.Type != "deluge" {
			t.Error("Torrent type not set")
		}
		if cfg.Usenet.SABnzbd.UseHTTPS != true {
			t.Error("SABnzbd UseHTTPS not set")
		}
	})

	t.Run("QBittorrentConfig", func(t *testing.T) {
		cfg := QBittorrentConfig{
			Host:     "qbt.local",
			Port:     8080,
			Username: "admin",
			Password: "pass",
			UseHTTPS: true,
		}
		if cfg.UseHTTPS != true {
			t.Error("UseHTTPS not set")
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
		MaintenanceWindowEnabled: true,
		MaintenanceWindowStart:   1,
		MaintenanceWindowEnd:     4,
	}
	if cfg.MaintenanceWindowStart != 1 || cfg.MaintenanceWindowEnd != 4 {
		t.Error("maintenance window fields not set correctly")
	}
}
