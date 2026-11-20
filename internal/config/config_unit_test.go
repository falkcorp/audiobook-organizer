// file: internal/config/config_unit_test.go
// version: 1.21.2
// guid: c50805f7-8e88-4a02-a03a-2e48863e2619
// last-edited: 2026-11-09

package config

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/tools"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
			// Was "{title}", which is the shape that stranded 2,584 files in
			// production: with no per-track placeholder every file of a
			// multi-file book expands to the same name. Validate now rejects
			// it, so this case uses the real shipped default.
			FileNamingPattern:   DefaultFileNamingPattern,
			SupportedExtensions: []string{".m4b", ".mp3"},
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

	t.Run("negative JSON body limit", func(t *testing.T) {
		c := &Config{DatabaseType: "pebble", JSONBodyLimitMB: -1}
		err := c.Validate()
		assert.ErrorContains(t, err, "json_body_limit_mb must be >= 0")
	})

	t.Run("negative master list retention", func(t *testing.T) {
		c := &Config{DatabaseType: "pebble", MasterListRetention: -1}
		assert.ErrorContains(t, c.Validate(), "master_list_retention must be >= 0")
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
		assert.False(t, AppConfig.WriteBackMetadata)
		assert.True(t, AppConfig.EmbedCoverArt)
	})

	t.Run("performance defaults", func(t *testing.T) {
		expectedWorkers := max(runtime.NumCPU(), 4)
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

	t.Run("API and auth defaults", func(t *testing.T) {
		assert.Equal(t, 100, AppConfig.APIRateLimitPerMinute)
		assert.Equal(t, 1, AppConfig.JSONBodyLimitMB)
		assert.Equal(t, 10, AppConfig.UploadBodyLimitMB)
		assert.Equal(t, 10, AppConfig.MasterListRetention)
		assert.True(t, AppConfig.EnableAuth)
		assert.False(t, AppConfig.BasicAuthEnabled)
	})

	t.Run("logging defaults", func(t *testing.T) {
		assert.Equal(t, "info", AppConfig.LogLevel)
	})

	t.Run("maintenance window defaults", func(t *testing.T) {
		assert.True(t, AppConfig.Maintenance.Enabled)
		assert.Equal(t, 1, AppConfig.Maintenance.WindowStart)
		assert.Equal(t, 4, AppConfig.Maintenance.WindowEnd)
		assert.True(t, AppConfig.Maintenance.DedupRefresh)
		assert.True(t, AppConfig.Maintenance.DbOptimize)
		assert.False(t, AppConfig.Maintenance.LibraryScan)
		assert.False(t, AppConfig.Maintenance.LibraryOrganize)
		assert.False(t, AppConfig.Maintenance.MetadataRefresh)
	})

	t.Run("iTunes defaults", func(t *testing.T) {
		assert.Equal(t, "", AppConfig.ITunes.LibraryReadPath)
		assert.Equal(t, "", AppConfig.ITunes.LibraryITLPath)
	})

	t.Run("auto-update defaults", func(t *testing.T) {
		assert.False(t, AppConfig.AutoUpdate.Enabled)
		assert.Equal(t, "stable", AppConfig.AutoUpdate.Channel)
		assert.Equal(t, 60, AppConfig.AutoUpdate.CheckMinutes)
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
		// path_format is gone — folder_naming_pattern + file_naming_pattern are
		// the only path configuration. The file default carries {track:02d},
		// which the naming-pattern validator must accept.
		assert.Equal(t, "{author}/{series}/{title} ({print_year})", AppConfig.FolderNamingPattern)
		assert.Equal(t, "{title} - {track:02d}", AppConfig.FileNamingPattern)
		assert.NoError(t, validateNamingPattern(AppConfig.FileNamingPattern))
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
		assert.Equal(t, "/old/path.itl", AppConfig.ITunes.LibraryITLPath)
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
		assert.Equal(t, "/new/path.itl", AppConfig.ITunes.LibraryITLPath)
	})
}

// A configured ITL path no longer turns anything on: iTunes write-back was
// removed on 2026-10-07, and the old rule that auto-enabled it whenever
// library_write_path was set (overriding ITUNES_WRITE_BACK_ENABLED=false in
// prod) went with it. The removed keys still load without error.
func TestInitConfigITLPathEnablesNoWriteBack(t *testing.T) {
	viper.Reset()
	t.Setenv("ITUNES_WRITE_BACK_ENABLED", "true")
	t.Setenv("ITUNES_AUTO_WRITE_BACK", "true")
	t.Setenv("ITUNES_WRITEBACK_DRYRUN", "true")
	viper.Set("itunes_library_write_path", "/some/path.itl")
	viper.Set("itl_write_back_enabled", true)
	viper.Set("itunes.write_back_enabled", true)
	viper.Set("itunes.auto_write_back", true)
	viper.Set("itunes.write_back_dry_run", true)
	InitConfig()
	assert.Equal(t, "/some/path.itl", AppConfig.ITunes.LibraryITLPath)
	_, ok := reflect.TypeFor[ITunesConfig]().FieldByName("WriteBackEnabled")
	assert.False(t, ok, "ITunesConfig must carry no write-back switch")
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
	// ResetToDefaultsForTest rebuilds the whole process-wide Config. Leaving factory
	// defaults (or this test's fixture) behind couples every later test to
	// whether this one ran first, which is exactly the order dependence that
	// made internal/config fail 20 of 24 -shuffle seeds.
	before := Snapshot()
	t.Cleanup(func() { Mutate(func(c *Config) { *c = before }) })

	// Set paths that should be preserved
	AppConfig = Config{
		RootDir:      "/media/audiobooks",
		DatabasePath: "/data/audiobooks.pebble",
		PlaylistDir:  "/media/playlists",
		LogLevel:     "debug",
	}

	ResetToDefaultsForTest()

	// Paths should be preserved
	assert.Equal(t, "/media/audiobooks", AppConfig.RootDir)
	assert.Equal(t, "/data/audiobooks.pebble", AppConfig.DatabasePath)
	assert.Equal(t, "/media/playlists", AppConfig.PlaylistDir)

	// Other fields should be reset
	assert.Equal(t, "pebble", AppConfig.DatabaseType)
	assert.Equal(t, "info", AppConfig.LogLevel)
	assert.True(t, AppConfig.AutoOrganize)
	assert.Equal(t, "auto", AppConfig.OrganizationStrategy)
}

// ---------------------------------------------------------------------------
// applySetting — comprehensive table-driven tests (26% -> ~100%)
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// ConfigFilePath
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// LoadConfigFromFile
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// SaveConfigToFile
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// SaveConfigToDatabase
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// LoadConfigFromDatabase
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// MigrateMaintenanceWindow
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// SyncConfigFromEnv
// ---------------------------------------------------------------------------

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
