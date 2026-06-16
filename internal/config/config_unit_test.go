// file: internal/config/config_unit_test.go
// version: 1.7.0
// last-edited: 2026-06-16

package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/tools"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockSettingsStore implements database.SettingsStore for testing.
type mockSettingsStore struct {
	settings  map[string]*database.Setting
	setErr    error
	getAllErr error
	getErr    error
	deleteErr error
}

func newMockSettingsStore() *mockSettingsStore {
	return &mockSettingsStore{settings: make(map[string]*database.Setting)}
}

func (m *mockSettingsStore) GetSetting(key string) (*database.Setting, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	s, ok := m.settings[key]
	if !ok {
		return nil, fmt.Errorf("not found")
	}
	return s, nil
}

func (m *mockSettingsStore) SetSetting(key, value, typ string, isSecret bool) error {
	if m.setErr != nil {
		return m.setErr
	}
	m.settings[key] = &database.Setting{Key: key, Value: value, Type: typ, IsSecret: isSecret}
	return nil
}

func (m *mockSettingsStore) GetAllSettings() ([]database.Setting, error) {
	if m.getAllErr != nil {
		return nil, m.getAllErr
	}
	var result []database.Setting
	for _, s := range m.settings {
		result = append(result, *s)
	}
	return result, nil
}

func (m *mockSettingsStore) DeleteSetting(key string) error {
	if m.deleteErr != nil {
		return m.deleteErr
	}
	delete(m.settings, key)
	return nil
}

func TestHasBalancedBraces(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"empty string", "", true},
		{"no braces", "hello world", true},
		{"balanced single", "{author}", true},
		{"balanced multiple", "{author}/{title}", true},
		{"unbalanced open", "{author/{title}", false},
		{"unbalanced close", "author}/{title}", false},
		{"nested balanced", "{{nested}}", true},
		{"extra open", "{a}{b}{", false},
		{"extra close", "{a}}", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, hasBalancedBraces(tt.input))
		})
	}
}

func TestValidateNamingPattern(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		wantErr string
	}{
		{"valid simple", "{title}", ""},
		{"valid with separators", "{author}/{series}/{title}", ""},
		{"valid with literal text", "{title} ({print_year})", ""},
		{"empty pattern", "", "pattern cannot be empty"},
		{"whitespace only", "   ", "pattern cannot be empty"},
		{"unbalanced braces", "{author/{title}", "unbalanced braces"},
		{"invalid placeholder", "{author}/{bad placeholder}", "invalid placeholder format"},
		{"bare open brace", "{author}/{ }/{title}", "invalid placeholder format"},
		{"bare close brace", "{author}/}/{title}", "unbalanced braces"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateNamingPattern(tt.pattern)
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				assert.ErrorContains(t, err, tt.wantErr)
			}
		})
	}
}

func TestValidateParentDirExists(t *testing.T) {
	t.Run("empty path is ok", func(t *testing.T) {
		assert.NoError(t, validateParentDirExists("", "test_field"))
	})

	t.Run("whitespace path is ok", func(t *testing.T) {
		assert.NoError(t, validateParentDirExists("   ", "test_field"))
	})

	t.Run("existing parent dir", func(t *testing.T) {
		tmpDir := t.TempDir()
		path := filepath.Join(tmpDir, "somefile.db")
		assert.NoError(t, validateParentDirExists(path, "database_path"))
	})

	t.Run("non-existent parent dir", func(t *testing.T) {
		path := filepath.Join("/nonexistent_dir_12345", "subdir", "file.db")
		err := validateParentDirExists(path, "database_path")
		assert.ErrorContains(t, err, "does not exist")
	})

	t.Run("parent is a file not directory", func(t *testing.T) {
		tmpDir := t.TempDir()
		fakeParent := filepath.Join(tmpDir, "notadir")
		require.NoError(t, os.WriteFile(fakeParent, []byte("x"), 0644))
		path := filepath.Join(fakeParent, "file.db")
		err := validateParentDirExists(path, "database_path")
		assert.ErrorContains(t, err, "is not a directory")
	})
}

func TestValidateParentDirWritable(t *testing.T) {
	t.Run("empty path is ok", func(t *testing.T) {
		assert.NoError(t, validateParentDirWritable("", "test_field"))
	})

	t.Run("whitespace path is ok", func(t *testing.T) {
		assert.NoError(t, validateParentDirWritable("   ", "test_field"))
	})

	t.Run("writable parent dir", func(t *testing.T) {
		tmpDir := t.TempDir()
		path := filepath.Join(tmpDir, "somefile.db")
		assert.NoError(t, validateParentDirWritable(path, "database_path"))
	})

	t.Run("non-existent parent dir", func(t *testing.T) {
		path := filepath.Join("/nonexistent_dir_12345", "file.db")
		err := validateParentDirWritable(path, "database_path")
		assert.ErrorContains(t, err, "is not writable")
	})
}

func TestConfigValidate(t *testing.T) {
	t.Run("nil config", func(t *testing.T) {
		var c *Config
		err := c.Validate()
		assert.ErrorContains(t, err, "config is nil")
	})

	t.Run("valid defaults", func(t *testing.T) {
		c := &Config{
			DatabaseType:         "pebble",
			OrganizationStrategy: "auto",
			ConcurrentScans:      4,
			FolderNamingPattern:  "{author}/{title}",
			FileNamingPattern:    "{title}",
			SupportedExtensions:  []string{".m4b", ".mp3"},
		}
		assert.NoError(t, c.Validate())
	})

	t.Run("invalid database type", func(t *testing.T) {
		c := &Config{DatabaseType: "mysql"}
		err := c.Validate()
		assert.ErrorContains(t, err, "database_type must be 'pebble' or 'sqlite'")
	})

	t.Run("sqlite is valid", func(t *testing.T) {
		c := &Config{DatabaseType: "sqlite", OrganizationStrategy: "auto"}
		assert.NoError(t, c.Validate())
	})

	t.Run("negative concurrent scans", func(t *testing.T) {
		c := &Config{DatabaseType: "pebble", ConcurrentScans: -1}
		err := c.Validate()
		assert.ErrorContains(t, err, "concurrent_scans must be >= 0")
	})

	t.Run("negative debounce seconds", func(t *testing.T) {
		c := &Config{DatabaseType: "pebble", AutoScanDebounceSeconds: -5}
		err := c.Validate()
		assert.ErrorContains(t, err, "auto_scan_debounce_seconds must be >= 0")
	})

	t.Run("negative operation timeout", func(t *testing.T) {
		c := &Config{DatabaseType: "pebble", OperationTimeoutMinutes: -1}
		err := c.Validate()
		assert.ErrorContains(t, err, "operation_timeout_minutes must be >= 0")
	})

	t.Run("negative API rate limit", func(t *testing.T) {
		c := &Config{DatabaseType: "pebble", APIRateLimitPerMinute: -1}
		err := c.Validate()
		assert.ErrorContains(t, err, "api_rate_limit_per_minute must be >= 0")
	})

	t.Run("negative auth rate limit", func(t *testing.T) {
		c := &Config{DatabaseType: "pebble", AuthRateLimitPerMinute: -1}
		err := c.Validate()
		assert.ErrorContains(t, err, "auth_rate_limit_per_minute must be >= 0")
	})

	t.Run("negative JSON body limit", func(t *testing.T) {
		c := &Config{DatabaseType: "pebble", JSONBodyLimitMB: -1}
		err := c.Validate()
		assert.ErrorContains(t, err, "json_body_limit_mb must be >= 0")
	})

	t.Run("negative upload body limit", func(t *testing.T) {
		c := &Config{DatabaseType: "pebble", UploadBodyLimitMB: -1}
		err := c.Validate()
		assert.ErrorContains(t, err, "upload_body_limit_mb must be >= 0")
	})

	t.Run("disk quota out of range", func(t *testing.T) {
		c := &Config{
			DatabaseType:     "pebble",
			EnableDiskQuota:  true,
			DiskQuotaPercent: 0,
		}
		err := c.Validate()
		assert.ErrorContains(t, err, "disk_quota_percent must be between 1 and 100")

		c.DiskQuotaPercent = 101
		err = c.Validate()
		assert.ErrorContains(t, err, "disk_quota_percent must be between 1 and 100")
	})

	t.Run("disk quota disabled ignores range", func(t *testing.T) {
		c := &Config{
			DatabaseType:     "pebble",
			EnableDiskQuota:  false,
			DiskQuotaPercent: 0,
		}
		assert.NoError(t, c.Validate())
	})

	t.Run("invalid organization strategy", func(t *testing.T) {
		c := &Config{DatabaseType: "pebble", OrganizationStrategy: "magic"}
		err := c.Validate()
		assert.ErrorContains(t, err, "organization_strategy must be one of")
	})

	t.Run("all valid strategies", func(t *testing.T) {
		for _, strategy := range []string{"auto", "copy", "hardlink", "reflink", "symlink"} {
			c := &Config{DatabaseType: "pebble", OrganizationStrategy: strategy}
			assert.NoError(t, c.Validate(), "strategy %q should be valid", strategy)
		}
	})

	t.Run("invalid folder naming pattern", func(t *testing.T) {
		c := &Config{
			DatabaseType:        "pebble",
			FolderNamingPattern: "{bad pattern",
		}
		err := c.Validate()
		assert.ErrorContains(t, err, "folder_naming_pattern")
	})

	t.Run("invalid file naming pattern", func(t *testing.T) {
		c := &Config{
			DatabaseType:      "pebble",
			FileNamingPattern: "{bad pattern",
		}
		err := c.Validate()
		assert.ErrorContains(t, err, "file_naming_pattern")
	})

	t.Run("extension without dot", func(t *testing.T) {
		c := &Config{
			DatabaseType:        "pebble",
			SupportedExtensions: []string{"m4b"},
		}
		err := c.Validate()
		assert.ErrorContains(t, err, "must start with '.'")
	})

	t.Run("empty extension is skipped", func(t *testing.T) {
		c := &Config{
			DatabaseType:        "pebble",
			SupportedExtensions: []string{"", ".mp3"},
		}
		assert.NoError(t, c.Validate())
	})

	t.Run("multiple errors collected", func(t *testing.T) {
		c := &Config{
			DatabaseType:    "invalid",
			ConcurrentScans: -1,
		}
		err := c.Validate()
		assert.ErrorContains(t, err, "database_type")
		assert.ErrorContains(t, err, "concurrent_scans")
	})

	t.Run("database path with existing parent", func(t *testing.T) {
		tmpDir := t.TempDir()
		c := &Config{
			DatabaseType: "pebble",
			DatabasePath: filepath.Join(tmpDir, "test.db"),
		}
		assert.NoError(t, c.Validate())
	})

	t.Run("database path with non-existent parent", func(t *testing.T) {
		c := &Config{
			DatabaseType: "pebble",
			DatabasePath: "/nonexistent_xyz_12345/sub/test.db",
		}
		err := c.Validate()
		assert.ErrorContains(t, err, "does not exist")
	})

	t.Run("playlist dir with non-existent parent", func(t *testing.T) {
		c := &Config{
			DatabaseType: "pebble",
			PlaylistDir:  "/nonexistent_xyz_12345/playlists",
		}
		err := c.Validate()
		assert.ErrorContains(t, err, "does not exist")
	})
}

func TestInitConfigDefaults(t *testing.T) {
	viper.Reset()
	InitConfig()

	t.Run("core defaults", func(t *testing.T) {
		assert.Equal(t, "pebble", AppConfig.DatabaseType)
		assert.False(t, AppConfig.EnableSQLite)
		assert.False(t, AppConfig.SetupComplete)
	})

	t.Run("library organization defaults", func(t *testing.T) {
		assert.Equal(t, "auto", AppConfig.OrganizationStrategy)
		assert.False(t, AppConfig.ScanOnStartup)
		assert.True(t, AppConfig.AutoOrganize)
		assert.False(t, AppConfig.AutoScanEnabled)
		assert.Equal(t, 30, AppConfig.AutoScanDebounceSeconds)
		assert.True(t, AppConfig.CreateBackups)
	})

	t.Run("metadata defaults", func(t *testing.T) {
		assert.True(t, AppConfig.AutoFetchMetadata)
		assert.False(t, AppConfig.WriteBackMetadata)
		assert.False(t, AppConfig.EmbedCoverArt)
		assert.Equal(t, "en", AppConfig.Language)
	})

	t.Run("performance defaults", func(t *testing.T) {
		expectedWorkers := runtime.NumCPU()
		if expectedWorkers < 4 {
			expectedWorkers = 4
		}
		assert.Equal(t, expectedWorkers, AppConfig.ConcurrentScans)
		assert.Equal(t, 30, AppConfig.OperationTimeoutMinutes)
	})

	t.Run("embedding dedup defaults", func(t *testing.T) {
		assert.True(t, AppConfig.Embedding.Enabled)
		assert.Equal(t, "text-embedding-3-large", AppConfig.Embedding.Model)
		assert.InDelta(t, 0.95, AppConfig.Dedup.BookHighThreshold, 0.001)
		assert.InDelta(t, 0.85, AppConfig.Dedup.BookLowThreshold, 0.001)
		assert.InDelta(t, 0.92, AppConfig.Dedup.AuthorHighThreshold, 0.001)
		assert.InDelta(t, 0.80, AppConfig.Dedup.AuthorLowThreshold, 0.001)
		assert.True(t, AppConfig.Dedup.AutoMergeEnabled)
		assert.False(t, AppConfig.Dedup.LLMAutoMergeHighConfidence)
	})

	t.Run("metadata scoring defaults", func(t *testing.T) {
		assert.False(t, AppConfig.MetadataScoring.EmbeddingEnabled)
		assert.InDelta(t, 0.82, AppConfig.MetadataScoring.EmbeddingMinScore, 0.001)
		assert.InDelta(t, 0.88, AppConfig.MetadataScoring.EmbeddingBestMatch, 0.001)
		assert.False(t, AppConfig.MetadataScoring.LLMEnabled)
		assert.InDelta(t, 0.05, AppConfig.MetadataScoring.LLMRerankEpsilon, 0.001)
		assert.Equal(t, 5, AppConfig.MetadataScoring.LLMRerankTopK)
	})

	t.Run("tag write backup default", func(t *testing.T) {
		assert.True(t, AppConfig.MetadataScoring.WriteBackupBefore)
	})

	t.Run("API and auth defaults", func(t *testing.T) {
		assert.Equal(t, 0, AppConfig.APIRateLimitPerMinute)
		assert.Equal(t, 10, AppConfig.AuthRateLimitPerMinute)
		assert.Equal(t, 1, AppConfig.JSONBodyLimitMB)
		assert.Equal(t, 10, AppConfig.UploadBodyLimitMB)
		assert.True(t, AppConfig.EnableAuth)
		assert.False(t, AppConfig.BasicAuthEnabled)
	})

	t.Run("memory management defaults", func(t *testing.T) {
		assert.Equal(t, "items", AppConfig.MemoryLimitType)
		assert.Equal(t, 1000, AppConfig.CacheSize)
		assert.Equal(t, 25, AppConfig.MemoryLimitPercent)
		assert.Equal(t, 512, AppConfig.MemoryLimitMB)
	})

	t.Run("logging defaults", func(t *testing.T) {
		assert.Equal(t, "info", AppConfig.LogLevel)
		assert.Equal(t, "text", AppConfig.LogFormat)
		assert.False(t, AppConfig.EnableJsonLogging)
	})

	t.Run("maintenance window defaults", func(t *testing.T) {
		assert.True(t, AppConfig.MaintenanceWindowEnabled)
		assert.Equal(t, 1, AppConfig.MaintenanceWindowStart)
		assert.Equal(t, 4, AppConfig.MaintenanceWindowEnd)
		assert.True(t, AppConfig.MaintenanceWindowDedupRefresh)
		assert.True(t, AppConfig.MaintenanceWindowDbOptimize)
		assert.False(t, AppConfig.MaintenanceWindowLibraryScan)
		assert.False(t, AppConfig.MaintenanceWindowLibraryOrganize)
		assert.False(t, AppConfig.MaintenanceWindowMetadataRefresh)
	})

	t.Run("iTunes defaults", func(t *testing.T) {
		assert.True(t, AppConfig.ITunes.SyncEnabled)
		assert.Equal(t, 30, AppConfig.ITunes.SyncInterval)
		assert.False(t, AppConfig.ITunes.AutoWriteBack)
	})

	t.Run("auto-update defaults", func(t *testing.T) {
		assert.False(t, AppConfig.AutoUpdateEnabled)
		assert.Equal(t, "stable", AppConfig.AutoUpdateChannel)
		assert.Equal(t, 60, AppConfig.AutoUpdateCheckMinutes)
	})

	t.Run("default metadata sources", func(t *testing.T) {
		assert.GreaterOrEqual(t, len(AppConfig.MetadataSources), 3)
		assert.Equal(t, "audible", AppConfig.MetadataSources[0].ID)
		assert.True(t, AppConfig.MetadataSources[0].Enabled)
		assert.Equal(t, "openlibrary", AppConfig.MetadataSources[1].ID)
	})

	t.Run("supported extensions default", func(t *testing.T) {
		assert.Contains(t, AppConfig.SupportedExtensions, ".m4b")
		assert.Contains(t, AppConfig.SupportedExtensions, ".mp3")
		assert.Contains(t, AppConfig.SupportedExtensions, ".flac")
	})

	t.Run("path formatting defaults", func(t *testing.T) {
		assert.Equal(t, "{author}/{series_prefix}{title}/{track_title}.{ext}", AppConfig.PathFormat)
		assert.True(t, AppConfig.AutoRenameOnApply)
		assert.True(t, AppConfig.AutoWriteTagsOnApply)
		assert.True(t, AppConfig.VerifyAfterWrite)
	})

	t.Run("tools defaults", func(t *testing.T) {
		assert.Equal(t, "/var/lib/audiobook-organizer/tools", AppConfig.Tools.ManagedDir)
		assert.Equal(t, tools.ToolModeSystem, AppConfig.Tools.Ollama.Mode)
		assert.Equal(t, tools.ToolModeSystem, AppConfig.Tools.Fpcalc.Mode)
		assert.False(t, AppConfig.Tools.AllowPeriodicOllama)
		assert.Equal(t, 10, AppConfig.Tools.OllamaDebounceMin)
	})
}

func TestInitConfigDatabaseTypeNormalization(t *testing.T) {
	t.Run("sqlite3 normalized to sqlite", func(t *testing.T) {
		viper.Reset()
		viper.Set("database_type", "sqlite3")
		InitConfig()
		assert.Equal(t, "sqlite", AppConfig.DatabaseType)
	})

	t.Run("empty normalized to pebble", func(t *testing.T) {
		viper.Reset()
		viper.Set("database_type", "")
		InitConfig()
		assert.Equal(t, "pebble", AppConfig.DatabaseType)
	})
}

func TestInitConfigITunesBackwardCompat(t *testing.T) {
	t.Run("old itunes_library_itl_path maps to write path", func(t *testing.T) {
		viper.Reset()
		viper.Set("itunes_library_itl_path", "/old/path.itl")
		InitConfig()
		assert.Equal(t, "/old/path.itl", AppConfig.ITunes.LibraryWritePath)
	})

	t.Run("old itunes_library_xml_path maps to read path", func(t *testing.T) {
		viper.Reset()
		viper.Set("itunes_library_xml_path", "/old/library.xml")
		InitConfig()
		assert.Equal(t, "/old/library.xml", AppConfig.ITunes.LibraryReadPath)
	})

	t.Run("new path takes precedence over old", func(t *testing.T) {
		viper.Reset()
		viper.Set("itunes_library_write_path", "/new/path.itl")
		viper.Set("itunes_library_itl_path", "/old/path.itl")
		InitConfig()
		assert.Equal(t, "/new/path.itl", AppConfig.ITunes.LibraryWritePath)
	})
}

func TestInitConfigITLWriteBackAutoEnable(t *testing.T) {
	viper.Reset()
	viper.Set("itunes_library_write_path", "/some/path.itl")
	viper.Set("itl_write_back_enabled", false)
	InitConfig()
	assert.True(t, AppConfig.ITunes.WriteBackEnabled, "should auto-enable when write path is set")
}

func TestInitConfigOpenLibraryDumpDir(t *testing.T) {
	t.Run("auto-set when root_dir present", func(t *testing.T) {
		viper.Reset()
		viper.Set("root_dir", "/media/audiobooks")
		InitConfig()
		assert.Equal(t, "/media/audiobooks/openlibrary-dumps", AppConfig.OpenLibraryDumpDir)
	})

	t.Run("not set when root_dir empty", func(t *testing.T) {
		viper.Reset()
		InitConfig()
		assert.Equal(t, "", AppConfig.OpenLibraryDumpDir)
	})

	t.Run("explicit value preserved", func(t *testing.T) {
		viper.Reset()
		viper.Set("root_dir", "/media/audiobooks")
		viper.Set("openlibrary_dump_dir", "/custom/dumps")
		InitConfig()
		assert.Equal(t, "/custom/dumps", AppConfig.OpenLibraryDumpDir)
	})
}

func TestResetToDefaultsPreservesPaths(t *testing.T) {
	// Set paths that should be preserved
	AppConfig = Config{
		RootDir:      "/media/audiobooks",
		DatabasePath: "/data/audiobooks.pebble",
		PlaylistDir:  "/media/playlists",
		LogLevel:     "debug",
		CacheSize:    5000,
	}

	ResetToDefaults()

	// Paths should be preserved
	assert.Equal(t, "/media/audiobooks", AppConfig.RootDir)
	assert.Equal(t, "/data/audiobooks.pebble", AppConfig.DatabasePath)
	assert.Equal(t, "/media/playlists", AppConfig.PlaylistDir)

	// Other fields should be reset
	assert.Equal(t, "pebble", AppConfig.DatabaseType)
	assert.Equal(t, "info", AppConfig.LogLevel)
	assert.Equal(t, 1000, AppConfig.CacheSize)
	assert.True(t, AppConfig.AutoOrganize)
	assert.Equal(t, "auto", AppConfig.OrganizationStrategy)
}

// ---------------------------------------------------------------------------
// applySetting — comprehensive table-driven tests (26% -> ~100%)
// ---------------------------------------------------------------------------

func TestApplySettingStringKeys(t *testing.T) {
	tests := []struct {
		key   string
		value string
		field func() string
	}{
		{"root_dir", "/media/books", func() string { return AppConfig.RootDir }},
		{"database_path", "/data/test.db", func() string { return AppConfig.DatabasePath }},
		{"playlist_dir", "/playlists", func() string { return AppConfig.PlaylistDir }},
		{"organization_strategy", "copy", func() string { return AppConfig.OrganizationStrategy }},
		{"folder_naming_pattern", "{author}/{title}", func() string { return AppConfig.FolderNamingPattern }},
		{"file_naming_pattern", "{title}", func() string { return AppConfig.FileNamingPattern }},
		{"language", "fr", func() string { return AppConfig.Language }},
		{"metadata_review_default_view", "grid", func() string { return AppConfig.MetadataReviewDefaultView }},
		{"openlibrary_dump_dir", "/dumps", func() string { return AppConfig.OpenLibraryDumpDir }},
		{"hardcover_api_token", "tok123", func() string { return AppConfig.HardcoverAPIToken }},
		{"openai_api_key", "sk-abc", func() string { return AppConfig.OpenAIAPIKey }},
		{"google_books_api_key", "gk-xyz", func() string { return AppConfig.GoogleBooksAPIKey }},
		{"memory_limit_type", "percent", func() string { return AppConfig.MemoryLimitType }},
		{"log_level", "debug", func() string { return AppConfig.LogLevel }},
		{"log_format", "json", func() string { return AppConfig.LogFormat }},
		{"auto_update_channel", "beta", func() string { return AppConfig.AutoUpdateChannel }},
		{"itunes_library_write_path", "/itl/path", func() string { return AppConfig.ITunes.LibraryWritePath }},
		{"itunes_library_itl_path", "/itl/path2", func() string { return AppConfig.ITunes.LibraryWritePath }},
		{"itunes_library_read_path", "/xml/path", func() string { return AppConfig.ITunes.LibraryReadPath }},
		{"itunes_library_xml_path", "/xml/path2", func() string { return AppConfig.ITunes.LibraryReadPath }},
		{"basic_auth_username", "admin", func() string { return AppConfig.BasicAuthUsername }},
		{"basic_auth_password", "secret", func() string { return AppConfig.BasicAuthPassword }},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			AppConfig = Config{}
			err := applySetting(tt.key, tt.value, "string")
			require.NoError(t, err)
			assert.Equal(t, tt.value, tt.field())
		})
	}
}

func TestApplySettingBoolKeys(t *testing.T) {
	tests := []struct {
		key   string
		field func() bool
	}{
		{"setup_complete", func() bool { return AppConfig.SetupComplete }},
		{"scan_on_startup", func() bool { return AppConfig.ScanOnStartup }},
		{"auto_organize", func() bool { return AppConfig.AutoOrganize }},
		{"create_backups", func() bool { return AppConfig.CreateBackups }},
		{"enable_disk_quota", func() bool { return AppConfig.EnableDiskQuota }},
		{"enable_user_quotas", func() bool { return AppConfig.EnableUserQuotas }},
		{"auto_fetch_metadata", func() bool { return AppConfig.AutoFetchMetadata }},
		{"openlibrary_dump_enabled", func() bool { return AppConfig.OpenLibraryDumpEnabled }},
		{"enable_ai_parsing", func() bool { return AppConfig.EnableAIParsing }},
		{"enable_auth", func() bool { return AppConfig.EnableAuth }},
		{"write_back_metadata", func() bool { return AppConfig.WriteBackMetadata }},
		{"embed_cover_art", func() bool { return AppConfig.EmbedCoverArt }},
		{"auto_scan_enabled", func() bool { return AppConfig.AutoScanEnabled }},
		{"enable_json_logging", func() bool { return AppConfig.EnableJsonLogging }},
		{"auto_update_enabled", func() bool { return AppConfig.AutoUpdateEnabled }},
		{"purge_soft_deleted_delete_files", func() bool { return AppConfig.PurgeSoftDeletedDeleteFiles }},
		{"itunes_sync_enabled", func() bool { return AppConfig.ITunes.SyncEnabled }},
		{"itl_write_back_enabled", func() bool { return AppConfig.ITunes.WriteBackEnabled }},
		{"itunes_auto_write_back", func() bool { return AppConfig.ITunes.AutoWriteBack }},
		{"maintenance_window_enabled", func() bool { return AppConfig.MaintenanceWindowEnabled }},
		{"maintenance_window_dedup_refresh", func() bool { return AppConfig.MaintenanceWindowDedupRefresh }},
		{"maintenance_window_series_prune", func() bool { return AppConfig.MaintenanceWindowSeriesPrune }},
		{"maintenance_window_author_split", func() bool { return AppConfig.MaintenanceWindowAuthorSplit }},
		{"maintenance_window_tombstone_cleanup", func() bool { return AppConfig.MaintenanceWindowTombstoneCleanup }},
		{"maintenance_window_reconcile", func() bool { return AppConfig.MaintenanceWindowReconcile }},
		{"maintenance_window_purge_deleted", func() bool { return AppConfig.MaintenanceWindowPurgeDeleted }},
		{"maintenance_window_purge_old_logs", func() bool { return AppConfig.MaintenanceWindowPurgeOldLogs }},
		{"maintenance_window_db_optimize", func() bool { return AppConfig.MaintenanceWindowDbOptimize }},
		{"maintenance_window_library_scan", func() bool { return AppConfig.MaintenanceWindowLibraryScan }},
		{"maintenance_window_library_organize", func() bool { return AppConfig.MaintenanceWindowLibraryOrganize }},
		{"maintenance_window_metadata_refresh", func() bool { return AppConfig.MaintenanceWindowMetadataRefresh }},
		{"basic_auth_enabled", func() bool { return AppConfig.BasicAuthEnabled }},
		{"scheduled_dedup_refresh_enabled", func() bool { return AppConfig.ScheduledDedupRefreshEnabled }},
		{"scheduled_dedup_refresh_on_startup", func() bool { return AppConfig.ScheduledDedupRefreshOnStartup }},
		{"scheduled_author_split_enabled", func() bool { return AppConfig.ScheduledAuthorSplitEnabled }},
		{"scheduled_author_split_on_startup", func() bool { return AppConfig.ScheduledAuthorSplitOnStartup }},
		{"scheduled_db_optimize_enabled", func() bool { return AppConfig.ScheduledDbOptimizeEnabled }},
		{"scheduled_db_optimize_on_startup", func() bool { return AppConfig.ScheduledDbOptimizeOnStartup }},
		{"scheduled_metadata_refresh_enabled", func() bool { return AppConfig.ScheduledMetadataRefreshEnabled }},
		{"scheduled_metadata_refresh_on_startup", func() bool { return AppConfig.ScheduledMetadataRefreshOnStartup }},
		{"scheduled_resolve_production_authors_enabled", func() bool { return AppConfig.ScheduledResolveProductionAuthorsEnabled }},
		{"scheduled_series_prune_enabled", func() bool { return AppConfig.ScheduledSeriesPruneEnabled }},
		{"scheduled_series_prune_on_startup", func() bool { return AppConfig.ScheduledSeriesPruneOnStartup }},
	}
	for _, tt := range tests {
		t.Run(tt.key+"_true", func(t *testing.T) {
			AppConfig = Config{}
			err := applySetting(tt.key, "true", "bool")
			require.NoError(t, err)
			assert.True(t, tt.field())
		})
		t.Run(tt.key+"_false", func(t *testing.T) {
			AppConfig = Config{}
			err := applySetting(tt.key, "false", "bool")
			require.NoError(t, err)
			assert.False(t, tt.field())
		})
	}
}

func TestApplySettingBoolInvalidValue(t *testing.T) {
	AppConfig = Config{}
	// Invalid bool string should be silently ignored (no error returned)
	err := applySetting("setup_complete", "notabool", "bool")
	assert.NoError(t, err)
	assert.False(t, AppConfig.SetupComplete)
}

func TestApplySettingIntKeys(t *testing.T) {
	tests := []struct {
		key   string
		value string
		field func() int
	}{
		{"disk_quota_percent", "80", func() int { return AppConfig.DiskQuotaPercent }},
		{"default_user_quota_gb", "50", func() int { return AppConfig.DefaultUserQuotaGB }},
		{"concurrent_scans", "8", func() int { return AppConfig.ConcurrentScans }},
		{"operation_timeout_minutes", "60", func() int { return AppConfig.OperationTimeoutMinutes }},
		{"api_rate_limit_per_minute", "100", func() int { return AppConfig.APIRateLimitPerMinute }},
		{"auth_rate_limit_per_minute", "20", func() int { return AppConfig.AuthRateLimitPerMinute }},
		{"json_body_limit_mb", "5", func() int { return AppConfig.JSONBodyLimitMB }},
		{"upload_body_limit_mb", "50", func() int { return AppConfig.UploadBodyLimitMB }},
		{"auto_scan_debounce_seconds", "10", func() int { return AppConfig.AutoScanDebounceSeconds }},
		{"cache_size", "2000", func() int { return AppConfig.CacheSize }},
		{"memory_limit_percent", "50", func() int { return AppConfig.MemoryLimitPercent }},
		{"memory_limit_mb", "1024", func() int { return AppConfig.MemoryLimitMB }},
		{"auto_update_check_minutes", "120", func() int { return AppConfig.AutoUpdateCheckMinutes }},
		{"auto_update_window_start", "2", func() int { return AppConfig.AutoUpdateWindowStart }},
		{"auto_update_window_end", "5", func() int { return AppConfig.AutoUpdateWindowEnd }},
		{"purge_soft_deleted_after_days", "30", func() int { return AppConfig.PurgeSoftDeletedAfterDays }},
		{"itunes_sync_interval", "60", func() int { return AppConfig.ITunes.SyncInterval }},
		{"maintenance_window_start", "3", func() int { return AppConfig.MaintenanceWindowStart }},
		{"maintenance_window_end", "6", func() int { return AppConfig.MaintenanceWindowEnd }},
		{"scheduled_dedup_refresh_interval", "24", func() int { return AppConfig.ScheduledDedupRefreshInterval }},
		{"scheduled_author_split_interval", "12", func() int { return AppConfig.ScheduledAuthorSplitInterval }},
		{"scheduled_db_optimize_interval", "48", func() int { return AppConfig.ScheduledDbOptimizeInterval }},
		{"scheduled_metadata_refresh_interval", "72", func() int { return AppConfig.ScheduledMetadataRefreshInterval }},
		{"scheduled_resolve_production_authors_interval", "96", func() int { return AppConfig.ScheduledResolveProductionAuthorsInterval }},
		{"scheduled_series_prune_interval", "168", func() int { return AppConfig.ScheduledSeriesPruneInterval }},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			AppConfig = Config{}
			err := applySetting(tt.key, tt.value, "int")
			require.NoError(t, err)
			expected := 0
			fmt.Sscanf(tt.value, "%d", &expected)
			assert.Equal(t, expected, tt.field())
		})
	}
}

func TestApplySettingIntInvalidValue(t *testing.T) {
	AppConfig = Config{}
	err := applySetting("concurrent_scans", "notanint", "int")
	assert.NoError(t, err)
	assert.Equal(t, 0, AppConfig.ConcurrentScans)
}

func TestApplySettingJSONKeys(t *testing.T) {
	t.Run("supported_extensions", func(t *testing.T) {
		AppConfig = Config{}
		err := applySetting("supported_extensions", `[".m4b",".mp3"]`, "json")
		require.NoError(t, err)
		assert.Equal(t, []string{".m4b", ".mp3"}, AppConfig.SupportedExtensions)
	})

	t.Run("supported_extensions_empty_array_ignored", func(t *testing.T) {
		AppConfig = Config{SupportedExtensions: []string{".flac"}}
		err := applySetting("supported_extensions", `[]`, "json")
		require.NoError(t, err)
		assert.Equal(t, []string{".flac"}, AppConfig.SupportedExtensions)
	})

	t.Run("supported_extensions_invalid_json", func(t *testing.T) {
		AppConfig = Config{SupportedExtensions: []string{".m4b"}}
		err := applySetting("supported_extensions", `not json`, "json")
		require.NoError(t, err)
		assert.Equal(t, []string{".m4b"}, AppConfig.SupportedExtensions)
	})

	t.Run("exclude_patterns", func(t *testing.T) {
		AppConfig = Config{}
		err := applySetting("exclude_patterns", `["*.tmp",".*"]`, "json")
		require.NoError(t, err)
		assert.Equal(t, []string{"*.tmp", ".*"}, AppConfig.ExcludePatterns)
	})

	t.Run("exclude_patterns_empty_array_clears", func(t *testing.T) {
		AppConfig = Config{ExcludePatterns: []string{"*.bak"}}
		err := applySetting("exclude_patterns", `[]`, "json")
		require.NoError(t, err)
		assert.Empty(t, AppConfig.ExcludePatterns)
	})

	t.Run("metadata_sources", func(t *testing.T) {
		AppConfig = Config{}
		sources := []MetadataSource{{ID: "audible", Name: "Audible", Enabled: true}}
		data, _ := json.Marshal(sources)
		err := applySetting("metadata_sources", string(data), "json")
		require.NoError(t, err)
		require.Len(t, AppConfig.MetadataSources, 1)
		assert.Equal(t, "audible", AppConfig.MetadataSources[0].ID)
	})

	t.Run("metadata_sources_empty_ignored", func(t *testing.T) {
		orig := []MetadataSource{{ID: "x"}}
		AppConfig = Config{MetadataSources: orig}
		err := applySetting("metadata_sources", `[]`, "json")
		require.NoError(t, err)
		assert.Equal(t, orig, AppConfig.MetadataSources)
	})

	t.Run("itunes_path_mappings", func(t *testing.T) {
		AppConfig = Config{}
		mappings := []ITunesPathMap{{From: "/itunes", To: "/local"}}
		data, _ := json.Marshal(mappings)
		err := applySetting("itunes_path_mappings", string(data), "json")
		require.NoError(t, err)
		require.Len(t, AppConfig.ITunes.PathMappings, 1)
		assert.Equal(t, "/itunes", AppConfig.ITunes.PathMappings[0].From)
	})
}

func TestApplySettingUnknownKey(t *testing.T) {
	err := applySetting("totally_unknown_key", "value", "string")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown setting key")
}

func TestApplySettingInternalStateKeys(t *testing.T) {
	// These should return nil without error
	for _, key := range []string{"maintenance_window_migrated", "maintenance_window_last_run", "maintenance_window_update_completed"} {
		t.Run(key, func(t *testing.T) {
			err := applySetting(key, "true", "bool")
			assert.NoError(t, err)
		})
	}
}

// ---------------------------------------------------------------------------
// ConfigFilePath
// ---------------------------------------------------------------------------

func TestConfigFilePathVariants(t *testing.T) {
	t.Run("database path set", func(t *testing.T) {
		AppConfig = Config{DatabasePath: "/data/audiobooks.pebble"}
		path := ConfigFilePath()
		assert.Equal(t, "/data/config.yaml", path)
	})

	t.Run("root dir fallback", func(t *testing.T) {
		AppConfig = Config{RootDir: "/media/books"}
		path := ConfigFilePath()
		assert.Equal(t, "/media/books/config.yaml", path)
	})

	t.Run("both empty", func(t *testing.T) {
		AppConfig = Config{}
		path := ConfigFilePath()
		assert.Empty(t, path)
	})

	t.Run("database path takes precedence", func(t *testing.T) {
		AppConfig = Config{DatabasePath: "/db/test.pebble", RootDir: "/root"}
		path := ConfigFilePath()
		assert.Equal(t, "/db/config.yaml", path)
	})
}

// ---------------------------------------------------------------------------
// LoadConfigFromFile
// ---------------------------------------------------------------------------

func TestLoadConfigFromFileUnit(t *testing.T) {
	t.Run("no path returns nil", func(t *testing.T) {
		AppConfig = Config{}
		err := LoadConfigFromFile()
		assert.NoError(t, err)
	})

	t.Run("nonexistent file returns nil", func(t *testing.T) {
		tmpDir := t.TempDir()
		AppConfig = Config{DatabasePath: filepath.Join(tmpDir, "test.db")}
		err := LoadConfigFromFile()
		assert.NoError(t, err)
	})

	t.Run("loads string fallbacks", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")
		content := "openai_api_key: sk-test123\nlanguage: de\n"
		require.NoError(t, os.WriteFile(configPath, []byte(content), 0o600))

		AppConfig = Config{DatabasePath: filepath.Join(tmpDir, "test.db")}
		err := LoadConfigFromFile()
		assert.NoError(t, err)
		assert.Equal(t, "sk-test123", AppConfig.OpenAIAPIKey)
		assert.Equal(t, "de", AppConfig.Language)
	})

	t.Run("does not overwrite existing values", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")
		content := "openai_api_key: sk-fromfile\n"
		require.NoError(t, os.WriteFile(configPath, []byte(content), 0o600))

		AppConfig = Config{DatabasePath: filepath.Join(tmpDir, "test.db"), OpenAIAPIKey: "sk-existing"}
		err := LoadConfigFromFile()
		assert.NoError(t, err)
		assert.Equal(t, "sk-existing", AppConfig.OpenAIAPIKey)
	})

	t.Run("loads enable_ai_parsing", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")
		content := "enable_ai_parsing: true\n"
		require.NoError(t, os.WriteFile(configPath, []byte(content), 0o600))

		AppConfig = Config{DatabasePath: filepath.Join(tmpDir, "test.db")}
		err := LoadConfigFromFile()
		assert.NoError(t, err)
		assert.True(t, AppConfig.EnableAIParsing)
	})

	t.Run("invalid yaml returns nil with warning", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.yaml")
		require.NoError(t, os.WriteFile(configPath, []byte(":::invalid"), 0o600))

		AppConfig = Config{DatabasePath: filepath.Join(tmpDir, "test.db")}
		err := LoadConfigFromFile()
		assert.NoError(t, err)
	})
}

// ---------------------------------------------------------------------------
// SaveConfigToFile
// ---------------------------------------------------------------------------

func TestSaveConfigToFileUnit(t *testing.T) {
	t.Run("no path returns error", func(t *testing.T) {
		AppConfig = Config{}
		err := SaveConfigToFile()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "cannot determine config file path")
	})

	t.Run("writes config file", func(t *testing.T) {
		tmpDir := t.TempDir()
		AppConfig = Config{
			DatabasePath: filepath.Join(tmpDir, "test.db"),
			RootDir:      "/media",
			OpenAIAPIKey: "sk-test",
			LogLevel:     "debug",
		}
		err := SaveConfigToFile()
		assert.NoError(t, err)

		configPath := filepath.Join(tmpDir, "config.yaml")
		data, err := os.ReadFile(configPath)
		require.NoError(t, err)
		assert.Contains(t, string(data), "root_dir")
		assert.Contains(t, string(data), "openai_api_key")
	})

	t.Run("omits empty secrets", func(t *testing.T) {
		tmpDir := t.TempDir()
		AppConfig = Config{
			DatabasePath: filepath.Join(tmpDir, "test.db"),
		}
		err := SaveConfigToFile()
		assert.NoError(t, err)

		configPath := filepath.Join(tmpDir, "config.yaml")
		data, err := os.ReadFile(configPath)
		require.NoError(t, err)
		assert.NotContains(t, string(data), "openai_api_key")
	})
}

// ---------------------------------------------------------------------------
// SaveConfigToDatabase
// ---------------------------------------------------------------------------

func TestSaveConfigToDatabaseUnit(t *testing.T) {
	t.Run("nil store", func(t *testing.T) {
		err := SaveConfigToDatabase(nil)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "store is nil")
	})

	t.Run("saves settings successfully", func(t *testing.T) {
		tmpDir := t.TempDir()
		AppConfig = Config{
			DatabasePath:         filepath.Join(tmpDir, "test.db"),
			RootDir:              "/media",
			OrganizationStrategy: "auto",
			ConcurrentScans:      4,
		}
		store := newMockSettingsStore()
		err := SaveConfigToDatabase(store)
		assert.NoError(t, err)
		assert.Greater(t, len(store.settings), 0)
		blobSetting, ok := store.settings["config_blob"]
		if assert.True(t, ok, "config_blob should be saved") {
			var loaded Config
			if assert.NoError(t, json.Unmarshal([]byte(blobSetting.Value), &loaded)) {
				assert.Equal(t, "/media", loaded.RootDir)
				assert.Equal(t, 4, loaded.ConcurrentScans)
			}
		}
	})

	t.Run("preserves empty secrets from DB", func(t *testing.T) {
		tmpDir := t.TempDir()
		AppConfig = Config{
			DatabasePath: filepath.Join(tmpDir, "test.db"),
			OpenAIAPIKey: "", // empty in AppConfig
		}
		store := newMockSettingsStore()
		// Pre-populate store with existing secret
		store.settings["openai_api_key"] = &database.Setting{
			Key: "openai_api_key", Value: "sk-existing", Type: "string", IsSecret: true,
		}

		err := SaveConfigToDatabase(store)
		assert.NoError(t, err)
		// Existing value should be preserved
		assert.Equal(t, "sk-existing", store.settings["openai_api_key"].Value)
	})

	t.Run("blob save error is returned", func(t *testing.T) {
		tmpDir := t.TempDir()
		AppConfig = Config{
			DatabasePath: filepath.Join(tmpDir, "test.db"),
		}
		store := newMockSettingsStore()
		store.setErr = fmt.Errorf("write failed")

		err := SaveConfigToDatabase(store)
		assert.Error(t, err) // blob save failure propagates
	})
}

// ---------------------------------------------------------------------------
// LoadConfigFromDatabase
// ---------------------------------------------------------------------------

func TestLoadConfigFromDatabaseUnit(t *testing.T) {
	t.Run("nil store", func(t *testing.T) {
		err := LoadConfigFromDatabase(nil)
		assert.Error(t, err)
	})

	t.Run("getAllSettings error returns nil", func(t *testing.T) {
		store := newMockSettingsStore()
		store.getAllErr = fmt.Errorf("table not found")
		err := LoadConfigFromDatabase(store)
		assert.NoError(t, err)
	})

	t.Run("applies settings from store", func(t *testing.T) {
		AppConfig = Config{}
		store := newMockSettingsStore()
		store.settings["root_dir"] = &database.Setting{Key: "root_dir", Value: "/loaded", Type: "string"}
		store.settings["concurrent_scans"] = &database.Setting{Key: "concurrent_scans", Value: "12", Type: "int"}
		store.settings["auto_organize"] = &database.Setting{Key: "auto_organize", Value: "true", Type: "bool"}
		// mark migration as done to skip migration logic
		store.settings["maintenance_window_migrated"] = &database.Setting{Key: "maintenance_window_migrated", Value: "true", Type: "bool"}

		err := LoadConfigFromDatabase(store)
		assert.NoError(t, err)
		assert.Equal(t, "/loaded", AppConfig.RootDir)
		assert.Equal(t, 12, AppConfig.ConcurrentScans)
		assert.True(t, AppConfig.AutoOrganize)
	})

	t.Run("unknown key logs warning but continues", func(t *testing.T) {
		AppConfig = Config{}
		store := newMockSettingsStore()
		store.settings["unknown_key_xyz"] = &database.Setting{Key: "unknown_key_xyz", Value: "val", Type: "string"}
		store.settings["maintenance_window_migrated"] = &database.Setting{Key: "maintenance_window_migrated", Value: "true", Type: "bool"}

		err := LoadConfigFromDatabase(store)
		assert.NoError(t, err)
	})

	t.Run("derives OpenLibraryDumpDir from RootDir", func(t *testing.T) {
		AppConfig = Config{}
		store := newMockSettingsStore()
		store.settings["root_dir"] = &database.Setting{Key: "root_dir", Value: "/media/audio", Type: "string"}
		store.settings["maintenance_window_migrated"] = &database.Setting{Key: "maintenance_window_migrated", Value: "true", Type: "bool"}

		err := LoadConfigFromDatabase(store)
		assert.NoError(t, err)
		assert.Equal(t, "/media/audio/openlibrary-dumps", AppConfig.OpenLibraryDumpDir)
	})
}

// ---------------------------------------------------------------------------
// MigrateMaintenanceWindow
// ---------------------------------------------------------------------------

func TestMigrateMaintenanceWindow(t *testing.T) {
	t.Run("already migrated", func(t *testing.T) {
		store := newMockSettingsStore()
		store.settings["maintenance_window_migrated"] = &database.Setting{
			Key: "maintenance_window_migrated", Value: "true", Type: "bool",
		}
		AppConfig = Config{MaintenanceWindowStart: 0, MaintenanceWindowEnd: 0}
		MigrateMaintenanceWindow(store)
		// Should not change defaults since migration was already done
		assert.Equal(t, 0, AppConfig.MaintenanceWindowStart)
	})

	t.Run("migrates from auto-update window", func(t *testing.T) {
		store := newMockSettingsStore()
		AppConfig = Config{
			AutoUpdateWindowStart:  2,
			AutoUpdateWindowEnd:    5,
			MaintenanceWindowStart: 0,
			MaintenanceWindowEnd:   0,
		}
		MigrateMaintenanceWindow(store)
		assert.Equal(t, 2, AppConfig.MaintenanceWindowStart)
		assert.Equal(t, 5, AppConfig.MaintenanceWindowEnd)
		assert.Equal(t, "true", store.settings["maintenance_window_migrated"].Value)
	})

	t.Run("defaults when both zero", func(t *testing.T) {
		store := newMockSettingsStore()
		AppConfig = Config{}
		MigrateMaintenanceWindow(store)
		assert.Equal(t, 1, AppConfig.MaintenanceWindowStart)
		assert.Equal(t, 4, AppConfig.MaintenanceWindowEnd)
	})
}

// ---------------------------------------------------------------------------
// SyncConfigFromEnv
// ---------------------------------------------------------------------------

func TestSyncConfigFromEnvUnit(t *testing.T) {
	t.Run("overrides from env", func(t *testing.T) {
		viper.Reset()
		viper.Set("root_dir", "/env/root")
		viper.Set("openai_api_key", "sk-env")
		viper.Set("google_books_api_key", "gk-env")
		viper.Set("enable_ai_parsing", true)

		AppConfig = Config{}
		SyncConfigFromEnv()

		assert.Equal(t, "/env/root", AppConfig.RootDir)
		assert.Equal(t, "sk-env", AppConfig.OpenAIAPIKey)
		assert.Equal(t, "gk-env", AppConfig.GoogleBooksAPIKey)
		assert.True(t, AppConfig.EnableAIParsing)
	})

	t.Run("empty env values do not override", func(t *testing.T) {
		viper.Reset()
		viper.Set("root_dir", "")
		viper.Set("openai_api_key", "")

		AppConfig = Config{RootDir: "/existing", OpenAIAPIKey: "sk-existing"}
		SyncConfigFromEnv()

		assert.Equal(t, "/existing", AppConfig.RootDir)
		assert.Equal(t, "sk-existing", AppConfig.OpenAIAPIKey)
	})

	t.Run("unset keys do not override", func(t *testing.T) {
		viper.Reset()
		AppConfig = Config{RootDir: "/keep", OpenAIAPIKey: "sk-keep"}
		SyncConfigFromEnv()

		assert.Equal(t, "/keep", AppConfig.RootDir)
		assert.Equal(t, "sk-keep", AppConfig.OpenAIAPIKey)
	})
}

func TestMinBookSizeBytesDefault(t *testing.T) {
	c := &Config{MinBookSizeBytes: 0}
	_ = c.Validate()
	assert.Equal(t, int64(5*1024*1024), c.MinBookSizeBytes,
		"zero value should be coerced to 5 MB default")
}

func TestMinBookSizeBytesDisable(t *testing.T) {
	c := &Config{MinBookSizeBytes: -1}
	_ = c.Validate()
	assert.Equal(t, int64(-1), c.MinBookSizeBytes,
		"-1 sentinel should not be coerced")
}

func TestMinBookSizeBytesCustom(t *testing.T) {
	c := &Config{MinBookSizeBytes: 5 * 1024 * 1024}
	_ = c.Validate()
	assert.Equal(t, int64(5*1024*1024), c.MinBookSizeBytes,
		"explicit value should be preserved")
}
