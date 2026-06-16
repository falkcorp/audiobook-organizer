// file: internal/config/persistence_test.go
// version: 1.11.0
// guid: 5e6f7a8b-9c0d-1e2f-3a4b-5c6d7e8f9a0b
// last-edited: 2026-06-16

package config

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func resetConfigTestState() {
	viper.Reset()
	AppConfig = Config{}
}

// setupMigrationExpectations adds Maybe() expectations for the maintenance window
// migration calls (GetSetting + SetSetting) that happen in LoadConfigFromDatabase.
func setupMigrationExpectations(store *mocks.MockStore) {
	store.On("GetSetting", "maintenance_window_migrated").Return(nil, fmt.Errorf("not found")).Maybe()
	store.On("SetSetting", "maintenance_window_migrated", "true", "bool", false).Return(nil).Maybe()
}

func TestLoadConfigFromDatabase(t *testing.T) {
	resetConfigTestState()
	t.Cleanup(resetConfigTestState)

	t.Run("returns error for nil store", func(t *testing.T) {
		err := LoadConfigFromDatabase(nil)
		if err == nil {
			t.Error("expected error for nil store")
		}
	})

	t.Run("returns nil when store GetAllSettings errors", func(t *testing.T) {
		store := mocks.NewMockStore(t)
		store.EXPECT().GetAllSettings().Return(nil, fmt.Errorf("boom")).Once()

		err := LoadConfigFromDatabase(store)
		if err != nil {
			t.Fatalf("expected nil error when GetAllSettings fails, got %v", err)
		}
	})

	t.Run("handles empty settings gracefully", func(t *testing.T) {
		store := mocks.NewMockStore(t)
		setupMigrationExpectations(store)
		store.EXPECT().GetAllSettings().Return([]database.Setting{}, nil).Once()
		err := LoadConfigFromDatabase(store)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("loads string settings", func(t *testing.T) {
		store := mocks.NewMockStore(t)
		setupMigrationExpectations(store)
		store.EXPECT().GetAllSettings().Return([]database.Setting{
			{
				Key:   "root_dir",
				Value: "/test/audiobooks",
				Type:  "string",
			},
			{
				Key:   "organization_strategy",
				Value: "copy",
				Type:  "string",
			},
		}, nil).Once()

		// Reset AppConfig
		AppConfig = Config{}

		err := LoadConfigFromDatabase(store)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if AppConfig.RootDir != "/test/audiobooks" {
			t.Errorf("expected RootDir='/test/audiobooks', got '%s'", AppConfig.RootDir)
		}
		if AppConfig.OrganizationStrategy != "copy" {
			t.Errorf("expected OrganizationStrategy='copy', got '%s'", AppConfig.OrganizationStrategy)
		}
	})

	t.Run("loads boolean settings", func(t *testing.T) {
		store := mocks.NewMockStore(t)
		setupMigrationExpectations(store)
		store.EXPECT().GetAllSettings().Return([]database.Setting{
			{
				Key:   "scan_on_startup",
				Value: "true",
				Type:  "bool",
			},
			{
				Key:   "auto_organize",
				Value: "false",
				Type:  "bool",
			},
		}, nil).Once()

		AppConfig = Config{}

		err := LoadConfigFromDatabase(store)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if !AppConfig.ScanOnStartup {
			t.Error("expected ScanOnStartup=true")
		}
		if AppConfig.AutoOrganize {
			t.Error("expected AutoOrganize=false")
		}
	})

	t.Run("loads integer settings", func(t *testing.T) {
		store := mocks.NewMockStore(t)
		setupMigrationExpectations(store)
		store.EXPECT().GetAllSettings().Return([]database.Setting{
			{
				Key:   "concurrent_scans",
				Value: "8",
				Type:  "int",
			},
			{
				Key:   "cache_size",
				Value: "2000",
				Type:  "int",
			},
			{
				Key:   "disk_quota_percent",
				Value: "90",
				Type:  "int",
			},
		}, nil).Once()

		AppConfig = Config{}

		err := LoadConfigFromDatabase(store)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if AppConfig.ConcurrentScans != 8 {
			t.Errorf("expected ConcurrentScans=8, got %d", AppConfig.ConcurrentScans)
		}
		if AppConfig.CacheSize != 2000 {
			t.Errorf("expected CacheSize=2000, got %d", AppConfig.CacheSize)
		}
		if AppConfig.DiskQuotaPercent != 90 {
			t.Errorf("expected DiskQuotaPercent=90, got %d", AppConfig.DiskQuotaPercent)
		}
	})

	t.Run("skips secret setting when decrypt fails", func(t *testing.T) {
		store := mocks.NewMockStore(t)
		setupMigrationExpectations(store)
		store.EXPECT().GetAllSettings().Return([]database.Setting{
			{
				Key:      "openai_api_key",
				Value:    "not-base64",
				Type:     "string",
				IsSecret: true,
			},
		}, nil).Once()

		AppConfig = Config{OpenAIAPIKey: "keep-me"}
		store.EXPECT().SetSetting("openai_api_key", "keep-me", "string", true).Return(nil).Maybe()

		err := LoadConfigFromDatabase(store)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if AppConfig.OpenAIAPIKey != "keep-me" {
			t.Fatalf("expected OpenAIAPIKey to remain unchanged, got %q", AppConfig.OpenAIAPIKey)
		}
	})

	t.Run("handles invalid boolean gracefully", func(t *testing.T) {
		store := mocks.NewMockStore(t)
		setupMigrationExpectations(store)
		store.EXPECT().GetAllSettings().Return([]database.Setting{
			{
				Key:   "scan_on_startup",
				Value: "not-a-bool",
				Type:  "bool",
			},
		}, nil).Once()

		AppConfig = Config{ScanOnStartup: true}

		err := LoadConfigFromDatabase(store)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Should remain unchanged due to parse error
		if !AppConfig.ScanOnStartup {
			t.Error("ScanOnStartup should not have changed on parse error")
		}
	})

	t.Run("handles invalid integer gracefully", func(t *testing.T) {
		store := mocks.NewMockStore(t)
		setupMigrationExpectations(store)
		store.EXPECT().GetAllSettings().Return([]database.Setting{
			{
				Key:   "concurrent_scans",
				Value: "not-an-int",
				Type:  "int",
			},
		}, nil).Once()

		AppConfig = Config{ConcurrentScans: 4}

		err := LoadConfigFromDatabase(store)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Should remain unchanged due to parse error
		if AppConfig.ConcurrentScans != 4 {
			t.Errorf("ConcurrentScans should not have changed on parse error, got %d", AppConfig.ConcurrentScans)
		}
	})
}

func TestApplySetting(t *testing.T) {
	resetConfigTestState()
	t.Cleanup(resetConfigTestState)

	tests := []struct {
		name    string
		key     string
		value   string
		typ     string
		check   func() bool
		setup   func()
		wantErr bool
	}{
		{
			name:  "root_dir",
			key:   "root_dir",
			value: "/new/path",
			typ:   "string",
			setup: func() { AppConfig.RootDir = "" },
			check: func() bool { return AppConfig.RootDir == "/new/path" },
		},
		{
			name:  "database_path",
			key:   "database_path",
			value: "/data/db.pebble",
			typ:   "string",
			setup: func() { AppConfig.DatabasePath = "" },
			check: func() bool { return AppConfig.DatabasePath == "/data/db.pebble" },
		},
		{
			name:  "playlist_dir",
			key:   "playlist_dir",
			value: "/playlists",
			typ:   "string",
			setup: func() { AppConfig.PlaylistDir = "" },
			check: func() bool { return AppConfig.PlaylistDir == "/playlists" },
		},
		{
			name:  "organization_strategy",
			key:   "organization_strategy",
			value: "hardlink",
			typ:   "string",
			setup: func() { AppConfig.OrganizationStrategy = "" },
			check: func() bool { return AppConfig.OrganizationStrategy == "hardlink" },
		},
		{
			name:  "scan_on_startup",
			key:   "scan_on_startup",
			value: "true",
			typ:   "bool",
			setup: func() { AppConfig.ScanOnStartup = false },
			check: func() bool { return AppConfig.ScanOnStartup },
		},
		{
			name:  "auto_organize",
			key:   "auto_organize",
			value: "false",
			typ:   "bool",
			setup: func() { AppConfig.AutoOrganize = true },
			check: func() bool { return !AppConfig.AutoOrganize },
		},
		{
			name:  "folder_naming_pattern",
			key:   "folder_naming_pattern",
			value: "{author}/{title}",
			typ:   "string",
			setup: func() { AppConfig.FolderNamingPattern = "" },
			check: func() bool { return AppConfig.FolderNamingPattern == "{author}/{title}" },
		},
		{
			name:  "file_naming_pattern",
			key:   "file_naming_pattern",
			value: "{title}",
			typ:   "string",
			setup: func() { AppConfig.FileNamingPattern = "" },
			check: func() bool { return AppConfig.FileNamingPattern == "{title}" },
		},
		{
			name:  "create_backups",
			key:   "create_backups",
			value: "false",
			typ:   "bool",
			setup: func() { AppConfig.CreateBackups = true },
			check: func() bool { return !AppConfig.CreateBackups },
		},
		{
			name:  "enable_disk_quota",
			key:   "enable_disk_quota",
			value: "true",
			typ:   "bool",
			setup: func() { AppConfig.EnableDiskQuota = false },
			check: func() bool { return AppConfig.EnableDiskQuota },
		},
		{
			name:  "disk_quota_percent",
			key:   "disk_quota_percent",
			value: "95",
			typ:   "int",
			setup: func() { AppConfig.DiskQuotaPercent = 0 },
			check: func() bool { return AppConfig.DiskQuotaPercent == 95 },
		},
		{
			name:  "enable_user_quotas",
			key:   "enable_user_quotas",
			value: "true",
			typ:   "bool",
			setup: func() { AppConfig.EnableUserQuotas = false },
			check: func() bool { return AppConfig.EnableUserQuotas },
		},
		{
			name:  "default_user_quota_gb",
			key:   "default_user_quota_gb",
			value: "50",
			typ:   "int",
			setup: func() { AppConfig.DefaultUserQuotaGB = 0 },
			check: func() bool { return AppConfig.DefaultUserQuotaGB == 50 },
		},
		{
			name:  "auto_fetch_metadata",
			key:   "auto_fetch_metadata",
			value: "false",
			typ:   "bool",
			setup: func() { AppConfig.AutoFetchMetadata = true },
			check: func() bool { return !AppConfig.AutoFetchMetadata },
		},
		{
			name:  "language",
			key:   "language",
			value: "de",
			typ:   "string",
			setup: func() { AppConfig.Language = "" },
			check: func() bool { return AppConfig.Language == "de" },
		},
		{
			name:  "enable_ai_parsing",
			key:   "enable_ai_parsing",
			value: "true",
			typ:   "bool",
			setup: func() { AppConfig.EnableAIParsing = false },
			check: func() bool { return AppConfig.EnableAIParsing },
		},
		{
			name:  "openai_api_key",
			key:   "openai_api_key",
			value: "sk-test-key",
			typ:   "string",
			setup: func() { AppConfig.OpenAIAPIKey = "" },
			check: func() bool { return AppConfig.OpenAIAPIKey == "sk-test-key" },
		},
		{
			name:  "concurrent_scans",
			key:   "concurrent_scans",
			value: "16",
			typ:   "int",
			setup: func() { AppConfig.ConcurrentScans = 0 },
			check: func() bool { return AppConfig.ConcurrentScans == 16 },
		},
		{
			name:  "memory_limit_type",
			key:   "memory_limit_type",
			value: "percent",
			typ:   "string",
			setup: func() { AppConfig.MemoryLimitType = "" },
			check: func() bool { return AppConfig.MemoryLimitType == "percent" },
		},
		{
			name:  "cache_size",
			key:   "cache_size",
			value: "5000",
			typ:   "int",
			setup: func() { AppConfig.CacheSize = 0 },
			check: func() bool { return AppConfig.CacheSize == 5000 },
		},
		{
			name:  "memory_limit_percent",
			key:   "memory_limit_percent",
			value: "50",
			typ:   "int",
			setup: func() { AppConfig.MemoryLimitPercent = 0 },
			check: func() bool { return AppConfig.MemoryLimitPercent == 50 },
		},
		{
			name:  "memory_limit_mb",
			key:   "memory_limit_mb",
			value: "1024",
			typ:   "int",
			setup: func() { AppConfig.MemoryLimitMB = 0 },
			check: func() bool { return AppConfig.MemoryLimitMB == 1024 },
		},
		{
			name:  "log_level",
			key:   "log_level",
			value: "debug",
			typ:   "string",
			setup: func() { AppConfig.LogLevel = "" },
			check: func() bool { return AppConfig.LogLevel == "debug" },
		},
		{
			name:  "log_format",
			key:   "log_format",
			value: "json",
			typ:   "string",
			setup: func() { AppConfig.LogFormat = "" },
			check: func() bool { return AppConfig.LogFormat == "json" },
		},
		{
			name:  "enable_json_logging",
			key:   "enable_json_logging",
			value: "true",
			typ:   "bool",
			setup: func() { AppConfig.EnableJsonLogging = false },
			check: func() bool { return AppConfig.EnableJsonLogging },
		},
		{
			name:    "unknown_key",
			key:     "unknown_key",
			value:   "value",
			typ:     "string",
			setup:   func() {},
			check:   func() bool { return true },
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup()
			err := applySetting(tt.key, tt.value, tt.typ)
			if (err != nil) != tt.wantErr {
				t.Errorf("applySetting() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && !tt.check() {
				t.Errorf("applySetting() did not apply setting correctly")
			}
		})
	}
}

func TestSaveConfigToDatabase(t *testing.T) {
	resetConfigTestState()
	t.Cleanup(resetConfigTestState)

	t.Run("returns error for nil store", func(t *testing.T) {
		err := SaveConfigToDatabase(nil)
		if err == nil {
			t.Error("expected error for nil store")
		}
	})

	t.Run("saves all config values", func(t *testing.T) {
		store := mocks.NewMockStore(t)
		seen := map[string]struct{}{}
		savedValues := map[string]string{}
		store.EXPECT().GetSetting(mock.Anything).Return((*database.Setting)(nil), nil).Maybe()
		store.EXPECT().SetSetting(mock.Anything, mock.Anything, mock.Anything, mock.Anything).
			Run(func(key string, value string, typ string, isSecret bool) {
				seen[key] = struct{}{}
				savedValues[key] = value
			}).
			Return(nil)

		AppConfig = Config{
			RootDir:              "/test/audiobooks",
			DatabasePath:         "/data/db.pebble",
			PlaylistDir:          "/playlists",
			OrganizationStrategy: "copy",
			ScanOnStartup:        true,
			AutoOrganize:         false,
			FolderNamingPattern:  "{author}/{title}",
			FileNamingPattern:    "{title}",
			CreateBackups:        true,
			EnableDiskQuota:      true,
			DiskQuotaPercent:     90,
			EnableUserQuotas:     true,
			DefaultUserQuotaGB:   50,
			AutoFetchMetadata:    true,
			Language:             "de",
			EnableAIParsing:      true,
			OpenAIAPIKey:         "sk-test",
			ConcurrentScans:      8,
			MemoryLimitType:      "percent",
			CacheSize:            2000,
			MemoryLimitPercent:   50,
			MemoryLimitMB:        1024,
			LogLevel:             "debug",
			LogFormat:            "json",
			EnableJsonLogging:    true,
		}
		err := SaveConfigToDatabase(store)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Non-secret fields are stored as a single config_blob JSON entry
		if _, ok := seen["config_blob"]; !ok {
			t.Fatalf("expected config_blob to be saved")
		}
		// Parse blob and verify a sample of fields were captured
		var loaded Config
		if err := json.Unmarshal([]byte(savedValues["config_blob"]), &loaded); err != nil {
			t.Fatalf("failed to parse config_blob: %v", err)
		}
		if loaded.RootDir != "/test/audiobooks" {
			t.Errorf("expected RootDir /test/audiobooks, got %q", loaded.RootDir)
		}
		if loaded.ConcurrentScans != 8 {
			t.Errorf("expected ConcurrentScans 8, got %d", loaded.ConcurrentScans)
		}
		// Secrets are stored as separate encrypted entries
		for _, secretKey := range []string{"openai_api_key"} {
			if _, ok := seen[secretKey]; !ok {
				t.Fatalf("expected secret %q to be saved when non-empty", secretKey)
			}
		}
	})

	t.Run("preserves existing secret when AppConfig value is empty", func(t *testing.T) {
		store := mocks.NewMockStore(t)
		seen := map[string]struct{}{}
		store.EXPECT().SetSetting(mock.Anything, mock.Anything, mock.Anything, mock.Anything).
			Run(func(key string, value string, typ string, isSecret bool) {
				seen[key] = struct{}{}
			}).
			Return(nil)

		// Simulate: DB has an encrypted key, but decryption failed so AppConfig is empty
		store.EXPECT().GetSetting("openai_api_key").Return(&database.Setting{
			Key:      "openai_api_key",
			Value:    "encrypted-value-in-db",
			Type:     "string",
			IsSecret: true,
		}, nil).Once()
		// Other secrets with no DB value
		store.EXPECT().GetSetting(mock.Anything).Return((*database.Setting)(nil), nil).Maybe()

		AppConfig = Config{
			OpenAIAPIKey: "", // empty because decryption failed on load
		}

		err := SaveConfigToDatabase(store)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// The secret should NOT have been overwritten — it was preserved
		if _, ok := seen["openai_api_key"]; ok {
			t.Fatalf("did not expect openai_api_key to be saved (should preserve existing DB value)")
		}
		// Non-secret fields are stored in the config_blob
		if _, ok := seen["config_blob"]; !ok {
			t.Fatalf("expected config_blob to be saved")
		}
	})

	t.Run("saves new secret value when provided", func(t *testing.T) {
		store := mocks.NewMockStore(t)
		seen := map[string]struct{}{}
		store.EXPECT().GetSetting(mock.Anything).Return((*database.Setting)(nil), nil).Maybe()
		store.EXPECT().SetSetting(mock.Anything, mock.Anything, mock.Anything, mock.Anything).
			Run(func(key string, value string, typ string, isSecret bool) {
				seen[key] = struct{}{}
				if key == "openai_api_key" {
					if value != "sk-test-123" {
						t.Errorf("expected openai_api_key value 'sk-test-123', got %q", value)
					}
					if !isSecret {
						t.Error("expected openai_api_key to be marked as secret")
					}
				}
			}).
			Return(nil)

		AppConfig = Config{
			OpenAIAPIKey: "sk-test-123",
		}

		err := SaveConfigToDatabase(store)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if _, ok := seen["openai_api_key"]; !ok {
			t.Fatal("expected openai_api_key to be saved when non-empty")
		}
	})

	t.Run("allows deletion when DB has no existing value", func(t *testing.T) {
		store := mocks.NewMockStore(t)
		seen := map[string]struct{}{}
		store.EXPECT().SetSetting(mock.Anything, mock.Anything, mock.Anything, mock.Anything).
			Run(func(key string, value string, typ string, isSecret bool) {
				seen[key] = struct{}{}
			}).
			Return(nil)

		// DB has no existing value for any secret
		store.EXPECT().GetSetting(mock.Anything).Return((*database.Setting)(nil), nil)

		AppConfig = Config{
			OpenAIAPIKey: "", // empty and DB also empty — allow the write
		}

		err := SaveConfigToDatabase(store)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Should be saved (empty value written to DB since nothing to preserve)
		if _, ok := seen["openai_api_key"]; !ok {
			t.Fatal("expected openai_api_key to be saved when DB has no existing value")
		}
	})

	t.Run("allows deletion when GetSetting returns error", func(t *testing.T) {
		store := mocks.NewMockStore(t)
		seen := map[string]struct{}{}
		store.EXPECT().SetSetting(mock.Anything, mock.Anything, mock.Anything, mock.Anything).
			Run(func(key string, value string, typ string, isSecret bool) {
				seen[key] = struct{}{}
			}).
			Return(nil)

		// GetSetting returns an error for all secrets
		store.EXPECT().GetSetting(mock.Anything).Return((*database.Setting)(nil), fmt.Errorf("not found"))

		AppConfig = Config{
			OpenAIAPIKey: "",
		}

		err := SaveConfigToDatabase(store)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Should still be saved since we couldn't confirm an existing value
		if _, ok := seen["openai_api_key"]; !ok {
			t.Fatal("expected openai_api_key to be saved when GetSetting errors")
		}
	})

	t.Run("allows deletion when existing DB value is empty string", func(t *testing.T) {
		store := mocks.NewMockStore(t)
		seen := map[string]struct{}{}
		store.EXPECT().SetSetting(mock.Anything, mock.Anything, mock.Anything, mock.Anything).
			Run(func(key string, value string, typ string, isSecret bool) {
				seen[key] = struct{}{}
			}).
			Return(nil)

		// All secrets have empty DB values
		store.EXPECT().GetSetting(mock.Anything).Return(&database.Setting{
			Key:      "",
			Value:    "",
			Type:     "string",
			IsSecret: true,
		}, nil)

		AppConfig = Config{
			OpenAIAPIKey: "",
		}

		err := SaveConfigToDatabase(store)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Empty existing value means nothing to preserve — allow the write
		if _, ok := seen["openai_api_key"]; !ok {
			t.Fatal("expected openai_api_key to be saved when existing DB value is empty")
		}
	})
}

func TestSyncConfigFromEnv(t *testing.T) {
	t.Run("overrides only set values", func(t *testing.T) {
		resetConfigTestState()
		t.Cleanup(resetConfigTestState)

		AppConfig = Config{
			RootDir:         "/existing/root",
			OpenAIAPIKey:    "existing-key",
			EnableAIParsing: false,
		}

		viper.Set("root_dir", "/env/root")
		viper.Set("openai_api_key", "env-key")
		viper.Set("enable_ai_parsing", true)

		SyncConfigFromEnv()

		if AppConfig.RootDir != "/env/root" {
			t.Errorf("expected RootDir to be overridden, got %q", AppConfig.RootDir)
		}
		if AppConfig.OpenAIAPIKey != "env-key" {
			t.Errorf("expected OpenAIAPIKey to be overridden, got %q", AppConfig.OpenAIAPIKey)
		}
		if !AppConfig.EnableAIParsing {
			t.Errorf("expected EnableAIParsing to be overridden to true")
		}
	})

	t.Run("does not change unset values", func(t *testing.T) {
		resetConfigTestState()
		t.Cleanup(resetConfigTestState)

		AppConfig = Config{RootDir: "/keep"}

		SyncConfigFromEnv()

		if AppConfig.RootDir != "/keep" {
			t.Errorf("expected RootDir to remain unchanged, got %q", AppConfig.RootDir)
		}
	})

	t.Run("does not overwrite DB-loaded key with empty env value", func(t *testing.T) {
		resetConfigTestState()
		t.Cleanup(resetConfigTestState)

		// Simulate: DB loaded a key, but env var is empty
		AppConfig = Config{
			OpenAIAPIKey: "sk-db-loaded-key-1234",
			RootDir:      "/db-loaded-root",
		}

		viper.Set("openai_api_key", "")
		viper.Set("root_dir", "")

		SyncConfigFromEnv()

		if AppConfig.OpenAIAPIKey != "sk-db-loaded-key-1234" {
			t.Errorf("expected OpenAIAPIKey to remain DB value, got %q", AppConfig.OpenAIAPIKey)
		}
		if AppConfig.RootDir != "/db-loaded-root" {
			t.Errorf("expected RootDir to remain DB value, got %q", AppConfig.RootDir)
		}
	})
}

func TestLifecycleRetentionSettings(t *testing.T) {
	t.Run("lifecycle settings are applied from database", func(t *testing.T) {
		resetConfigTestState()
		t.Cleanup(resetConfigTestState)

		InitConfig()

		store := mocks.NewMockStore(t)
		setupMigrationExpectations(store)
		store.EXPECT().GetAllSettings().Return([]database.Setting{
			{
				Key:   "purge_soft_deleted_after_days",
				Value: "60",
				Type:  "int",
			},
			{
				Key:   "purge_soft_deleted_delete_files",
				Value: "true",
				Type:  "bool",
			},
		}, nil).Once()

		err := LoadConfigFromDatabase(store)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if AppConfig.PurgeSoftDeletedAfterDays != 60 {
			t.Errorf("expected PurgeSoftDeletedAfterDays to be 60, got %d", AppConfig.PurgeSoftDeletedAfterDays)
		}
		if !AppConfig.PurgeSoftDeletedDeleteFiles {
			t.Errorf("expected PurgeSoftDeletedDeleteFiles to be true, got %v", AppConfig.PurgeSoftDeletedDeleteFiles)
		}
	})
}

func TestMigrateEmbeddingFields_FlatBlob(t *testing.T) {
	flatBlob := `{
		"embedding_enabled": true,
		"embedding_model": "text-embedding-3-large",
		"embedding_dimensions": 3072,
		"embedding_base_url": "http://localhost:11434/v1",
		"vector_index_backend": "hnsw",
		"root_dir": "/data"
	}`

	migrated, changed := migrateEmbeddingBlob(flatBlob)
	require.True(t, changed, "flat blob should be migrated")

	var result map[string]any
	require.NoError(t, json.Unmarshal([]byte(migrated), &result))

	emb, ok := result["embedding"].(map[string]any)
	require.True(t, ok, "embedding key should exist as object")
	assert.Equal(t, true, emb["enabled"])
	assert.Equal(t, "text-embedding-3-large", emb["model"])
	assert.Equal(t, float64(3072), emb["dimensions"])
	assert.Equal(t, "http://localhost:11434/v1", emb["base_url"])
	assert.Equal(t, "hnsw", emb["vector_backend"])

	// flat keys must be gone
	assert.NotContains(t, result, "embedding_enabled")
	assert.NotContains(t, result, "embedding_model")
	assert.NotContains(t, result, "embedding_dimensions")
	assert.NotContains(t, result, "embedding_base_url")
	assert.NotContains(t, result, "vector_index_backend")

	// unrelated keys must be preserved
	assert.Equal(t, "/data", result["root_dir"])
}

func TestMigrateEmbeddingFields_AlreadyNested(t *testing.T) {
	nestedBlob := `{"embedding": {"enabled": true, "model": "bge-m3"}, "root_dir": "/data"}`
	_, changed := migrateEmbeddingBlob(nestedBlob)
	assert.False(t, changed, "already-nested blob should be a no-op")
}

func TestMigrateEmbeddingFields_EmptyBlob(t *testing.T) {
	_, changed := migrateEmbeddingBlob(`{}`)
	assert.False(t, changed, "empty blob should be a no-op")
}

func TestRemapEmbeddingKeys_FlatKeys(t *testing.T) {
	payload := map[string]any{
		"embedding_enabled":    true,
		"embedding_model":      "bge-m3",
		"embedding_dimensions": float64(1024),
		"root_dir":             "/data",
	}
	result := remapEmbeddingKeys(payload)

	emb, ok := result["embedding"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, emb["enabled"])
	assert.Equal(t, "bge-m3", emb["model"])
	assert.Equal(t, float64(1024), emb["dimensions"])
	assert.Equal(t, "/data", result["root_dir"]) // untouched
	assert.NotContains(t, result, "embedding_enabled")
	assert.NotContains(t, result, "embedding_model")
}

func TestRemapEmbeddingKeys_MixedKeys(t *testing.T) {
	// Client sends both flat legacy key AND new nested key — merge, don't overwrite
	payload := map[string]any{
		"embedding_enabled": false,
		"embedding":         map[string]any{"model": "bge-m3"},
	}
	result := remapEmbeddingKeys(payload)
	emb := result["embedding"].(map[string]any)
	assert.Equal(t, false, emb["enabled"])
	assert.Equal(t, "bge-m3", emb["model"])
}

func TestRemapEmbeddingKeys_NoFlatKeys(t *testing.T) {
	payload := map[string]any{"root_dir": "/data"}
	result := remapEmbeddingKeys(payload)
	assert.Equal(t, map[string]any{"root_dir": "/data"}, result)
}

func TestMigrateDedupFields_FlatBlob(t *testing.T) {
	flatBlob := `{
		"dedup_book_high_threshold": 0.95,
		"dedup_book_low_threshold": 0.85,
		"dedup_author_high_threshold": 0.92,
		"dedup_author_low_threshold": 0.80,
		"dedup_auto_merge_enabled": true,
		"dedup_embeddings_enabled": false,
		"dedup_llm_auto_merge_high_confidence": false,
		"dedup_on_import_via_scheduler": true,
		"dedup_review_model": "gpt-5-mini",
		"root_dir": "/data"
	}`
	migrated, changed := migrateDedupBlob(flatBlob)
	require.True(t, changed)
	var result map[string]any
	require.NoError(t, json.Unmarshal([]byte(migrated), &result))
	d, ok := result["dedup"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(0.95), d["book_high_threshold"])
	assert.Equal(t, "gpt-5-mini", d["review_model"])
	assert.Equal(t, true, d["auto_merge_enabled"])
	assert.Equal(t, "/data", result["root_dir"])
	assert.NotContains(t, result, "dedup_book_high_threshold")
	assert.NotContains(t, result, "dedup_review_model")
}

func TestMigrateDedupFields_AlreadyNested(t *testing.T) {
	_, changed := migrateDedupBlob(`{"dedup": {"book_high_threshold": 0.95}}`)
	assert.False(t, changed)
}

func TestMigrateDedupFields_EmptyBlob(t *testing.T) {
	_, changed := migrateDedupBlob(`{}`)
	assert.False(t, changed)
}

func TestRemapDedupKeys_FlatKeys(t *testing.T) {
	payload := map[string]any{
		"dedup_book_high_threshold": float64(0.95),
		"dedup_review_model":        "gpt-5-mini",
		"root_dir":                  "/data",
	}
	result := remapDedupKeys(payload)
	d, ok := result["dedup"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(0.95), d["book_high_threshold"])
	assert.Equal(t, "gpt-5-mini", d["review_model"])
	assert.Equal(t, "/data", result["root_dir"])
	assert.NotContains(t, result, "dedup_book_high_threshold")
}

func TestRemapDedupKeys_NoFlatKeys(t *testing.T) {
	payload := map[string]any{"root_dir": "/data"}
	result := remapDedupKeys(payload)
	assert.Equal(t, map[string]any{"root_dir": "/data"}, result)
}

func TestMigrateMetadataScoringFields_FlatBlob(t *testing.T) {
	flatBlob := `{
		"metadata_embedding_scoring_enabled": true,
		"metadata_embedding_min_score": 0.82,
		"metadata_embedding_best_match_min": 0.88,
		"metadata_llm_scoring_enabled": false,
		"metadata_llm_rerank_epsilon": 0.05,
		"metadata_llm_rerank_top_k": 5,
		"write_backup_before_tag_write": true,
		"root_dir": "/data"
	}`
	migrated, changed := migrateMetadataScoringBlob(flatBlob)
	require.True(t, changed)
	var result map[string]any
	require.NoError(t, json.Unmarshal([]byte(migrated), &result))
	ms, ok := result["metadata_scoring"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, ms["embedding_enabled"])
	assert.Equal(t, float64(0.82), ms["embedding_min_score"])
	assert.Equal(t, float64(0.88), ms["embedding_best_match"])
	assert.Equal(t, false, ms["llm_enabled"])
	assert.Equal(t, float64(5), ms["llm_rerank_top_k"])
	assert.Equal(t, true, ms["write_backup_before"])
	assert.Equal(t, "/data", result["root_dir"])
	assert.NotContains(t, result, "metadata_embedding_scoring_enabled")
	assert.NotContains(t, result, "write_backup_before_tag_write")
}

func TestMigrateMetadataScoringFields_AlreadyNested(t *testing.T) {
	_, changed := migrateMetadataScoringBlob(`{"metadata_scoring": {"embedding_enabled": true}}`)
	assert.False(t, changed)
}

func TestMigrateMetadataScoringFields_EmptyBlob(t *testing.T) {
	_, changed := migrateMetadataScoringBlob(`{}`)
	assert.False(t, changed)
}

func TestRemapMetadataScoringKeys_FlatKeys(t *testing.T) {
	payload := map[string]any{
		"metadata_embedding_scoring_enabled": true,
		"write_backup_before_tag_write":      false,
		"root_dir":                           "/data",
	}
	result := remapMetadataScoringKeys(payload)
	ms, ok := result["metadata_scoring"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, ms["embedding_enabled"])
	assert.Equal(t, false, ms["write_backup_before"])
	assert.Equal(t, "/data", result["root_dir"])
	assert.NotContains(t, result, "metadata_embedding_scoring_enabled")
}

func TestRemapMetadataScoringKeys_NoFlatKeys(t *testing.T) {
	payload := map[string]any{"root_dir": "/data"}
	result := remapMetadataScoringKeys(payload)
	assert.Equal(t, map[string]any{"root_dir": "/data"}, result)
}

func TestMigrateITunesFields_FlatBlob(t *testing.T) {
	flatBlob := `{
		"itunes_sync_enabled": true,
		"itunes_sync_interval": 30,
		"itl_write_back_enabled": false,
		"itunes_library_write_path": "/mnt/itunes.itl",
		"itunes_library_read_path": "/mnt/iTunes Library.xml",
		"itunes_auto_write_back": false,
		"itunes_path_trim_enabled": true,
		"itunes_windows_root_path": "C:\\Users\\",
		"itunes_media_root": "/mnt/media",
		"root_dir": "/data"
	}`
	migrated, changed := migrateITunesBlob(flatBlob)
	require.True(t, changed)
	var result map[string]any
	require.NoError(t, json.Unmarshal([]byte(migrated), &result))
	it, ok := result["itunes"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, it["sync_enabled"])
	assert.Equal(t, float64(30), it["sync_interval"])
	assert.Equal(t, false, it["write_back_enabled"])
	assert.Equal(t, "/mnt/itunes.itl", it["library_write_path"])
	assert.Equal(t, true, it["path_trim_enabled"])
	assert.Equal(t, "/data", result["root_dir"])
	assert.NotContains(t, result, "itunes_sync_enabled")
	assert.NotContains(t, result, "itl_write_back_enabled")
}

func TestMigrateITunesFields_AlreadyNested(t *testing.T) {
	_, changed := migrateITunesBlob(`{"itunes": {"sync_enabled": true}}`)
	assert.False(t, changed)
}

func TestMigrateITunesFields_EmptyBlob(t *testing.T) {
	_, changed := migrateITunesBlob(`{}`)
	assert.False(t, changed)
}

func TestRemapITunesKeys_FlatKeys(t *testing.T) {
	payload := map[string]any{
		"itunes_sync_enabled":    true,
		"itl_write_back_enabled": false,
		"root_dir":               "/data",
	}
	result := remapITunesKeys(payload)
	it, ok := result["itunes"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, it["sync_enabled"])
	assert.Equal(t, false, it["write_back_enabled"])
	assert.Equal(t, "/data", result["root_dir"])
	assert.NotContains(t, result, "itunes_sync_enabled")
}

func TestRemapITunesKeys_NoFlatKeys(t *testing.T) {
	payload := map[string]any{"root_dir": "/data"}
	result := remapITunesKeys(payload)
	assert.Equal(t, map[string]any{"root_dir": "/data"}, result)
}

func TestMigrateMaintenanceFields_FlatBlob(t *testing.T) {
	flatBlob := `{
		"maintenance_window_enabled": false,
		"maintenance_window_start": 2,
		"maintenance_window_end": 5,
		"maintenance_window_dedup_refresh": true,
		"maintenance_window_series_prune": true,
		"maintenance_window_db_optimize": true,
		"acoustid_online_lookup_nightly_limit": 500,
		"root_dir": "/data"
	}`
	migrated, changed := migrateMaintenanceBlob(flatBlob)
	require.True(t, changed)
	var result map[string]any
	require.NoError(t, json.Unmarshal([]byte(migrated), &result))
	m, ok := result["maintenance"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, false, m["enabled"])
	assert.Equal(t, float64(2), m["window_start"])
	assert.Equal(t, float64(5), m["window_end"])
	assert.Equal(t, true, m["dedup_refresh"])
	assert.Equal(t, float64(500), m["acoustid_nightly_limit"])
	assert.Equal(t, "/data", result["root_dir"])
	assert.NotContains(t, result, "maintenance_window_enabled")
	assert.NotContains(t, result, "acoustid_online_lookup_nightly_limit")
}

func TestMigrateMaintenanceFields_AlreadyNested(t *testing.T) {
	_, changed := migrateMaintenanceBlob(`{"maintenance": {"enabled": false}}`)
	assert.False(t, changed)
}

func TestMigrateMaintenanceFields_EmptyBlob(t *testing.T) {
	_, changed := migrateMaintenanceBlob(`{}`)
	assert.False(t, changed)
}

func TestRemapMaintenanceKeys_FlatKeys(t *testing.T) {
	payload := map[string]any{
		"maintenance_window_enabled":           false,
		"acoustid_online_lookup_nightly_limit": float64(500),
		"root_dir":                             "/data",
	}
	result := remapMaintenanceKeys(payload)
	m, ok := result["maintenance"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, false, m["enabled"])
	assert.Equal(t, float64(500), m["acoustid_nightly_limit"])
	assert.Equal(t, "/data", result["root_dir"])
	assert.NotContains(t, result, "maintenance_window_enabled")
}

func TestRemapMaintenanceKeys_NoFlatKeys(t *testing.T) {
	payload := map[string]any{"root_dir": "/data"}
	result := remapMaintenanceKeys(payload)
	assert.Equal(t, map[string]any{"root_dir": "/data"}, result)
}
