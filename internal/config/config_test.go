// file: internal/config/config_test.go
// version: 1.5.0
// guid: b2c3d4e5-f6a7-8b9c-0d1e-2f3a4b5c6d7e
// last-edited: 2026-06-16

package config

import (
	"sync"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
)

// TestInitConfig tests configuration initialization with defaults
func TestInitConfig(t *testing.T) {
	// Arrange
	viper.Reset()

	// Act
	InitConfig()

	// Assert - Verify database defaults
	dbType := viper.GetString("database_type")
	if dbType != "pebble" {
		t.Errorf("Expected database_type to be 'pebble', got '%s'", dbType)
	}

	if enableSQLite := viper.GetBool("enable_sqlite3_i_know_the_risks"); enableSQLite {
		t.Error("Expected enable_sqlite3_i_know_the_risks to be false by default")
	}

	// Verify organization strategy defaults
	orgStrategy := viper.GetString("organization_strategy")
	if orgStrategy != "auto" {
		t.Errorf("Expected organization_strategy to be 'auto', got '%s'", orgStrategy)
	}

	if scanOnStartup := viper.GetBool("scan_on_startup"); scanOnStartup {
		t.Error("Expected scan_on_startup to be false by default")
	}

	if autoOrganize := viper.GetBool("auto_organize"); !autoOrganize {
		t.Error("Expected auto_organize to be true by default")
	}

	// Verify naming patterns
	expectedFolderPattern := "{author}/{series}/{title} ({print_year})"
	folderPattern := viper.GetString("folder_naming_pattern")
	if folderPattern != expectedFolderPattern {
		t.Errorf("Expected folder_naming_pattern to be '%s', got '%s'", expectedFolderPattern, folderPattern)
	}

	expectedFilePattern := "{title} - {author} - read by {narrator}"
	filePattern := viper.GetString("file_naming_pattern")
	if filePattern != expectedFilePattern {
		t.Errorf("Expected file_naming_pattern to be '%s', got '%s'", expectedFilePattern, filePattern)
	}

	// Verify backup defaults
	if createBackups := viper.GetBool("create_backups"); !createBackups {
		t.Error("Expected create_backups to be true by default")
	}
}

// TestStorageQuotaDefaults tests storage quota default values
func TestStorageQuotaDefaults(t *testing.T) {
	// Arrange-Act-Assert: Test disk quota defaults
	viper.Reset()
	InitConfig()

	if enableDiskQuota := viper.GetBool("enable_disk_quota"); enableDiskQuota {
		t.Error("Expected enable_disk_quota to be false by default")
	}

	diskQuotaPercent := viper.GetInt("disk_quota_percent")
	if diskQuotaPercent != 80 {
		t.Errorf("Expected disk_quota_percent to be 80, got %d", diskQuotaPercent)
	}

	if enableUserQuotas := viper.GetBool("enable_user_quotas"); enableUserQuotas {
		t.Error("Expected enable_user_quotas to be false by default")
	}

	defaultUserQuota := viper.GetInt("default_user_quota_gb")
	if defaultUserQuota != 100 {
		t.Errorf("Expected default_user_quota_gb to be 100, got %d", defaultUserQuota)
	}
}

// TestMetadataDefaults tests metadata configuration defaults
func TestMetadataDefaults(t *testing.T) {
	// Arrange-Act-Assert: Test metadata defaults
	viper.Reset()
	InitConfig()

	if autoFetch := viper.GetBool("auto_fetch_metadata"); !autoFetch {
		t.Error("Expected auto_fetch_metadata to be true by default")
	}

	language := viper.GetString("language")
	if language != "en" {
		t.Errorf("Expected language to be 'en', got '%s'", language)
	}

	// Verify metadata sources are populated
	if len(AppConfig.MetadataSources) == 0 {
		t.Error("Expected metadata sources to be populated")
	}

	// Verify Audible is first and enabled
	if len(AppConfig.MetadataSources) > 0 {
		first := AppConfig.MetadataSources[0]
		if first.ID != "audible" {
			t.Errorf("Expected first metadata source to be 'audible', got '%s'", first.ID)
		}
		if !first.Enabled {
			t.Error("Expected Audible to be enabled by default")
		}
	}
}

// TestAIParsingDefaults tests AI parsing configuration defaults
func TestAIParsingDefaults(t *testing.T) {
	// Arrange-Act-Assert: Test AI defaults
	viper.Reset()
	InitConfig()

	if enableAI := viper.GetBool("enable_ai_parsing"); !enableAI {
		t.Error("Expected enable_ai_parsing to be true by default")
	}

	apiKey := viper.GetString("openai_api_key")
	if apiKey != "" {
		t.Errorf("Expected openai_api_key to be empty by default, got '%s'", apiKey)
	}
}

// TestPerformanceDefaults tests performance configuration defaults
func TestPerformanceDefaults(t *testing.T) {
	// Arrange-Act-Assert: Test performance defaults
	viper.Reset()
	InitConfig()

	concurrentScans := viper.GetInt("concurrent_scans")
	if concurrentScans < 4 {
		t.Errorf("Expected concurrent_scans to be >= 4, got %d", concurrentScans)
	}
}

// TestMemoryManagementDefaults tests memory management defaults
func TestMemoryManagementDefaults(t *testing.T) {
	// Arrange-Act-Assert: Test memory defaults
	viper.Reset()
	InitConfig()

	memoryLimitType := viper.GetString("memory_limit_type")
	if memoryLimitType != "items" {
		t.Errorf("Expected memory_limit_type to be 'items', got '%s'", memoryLimitType)
	}

	cacheSize := viper.GetInt("cache_size")
	if cacheSize != 1000 {
		t.Errorf("Expected cache_size to be 1000, got %d", cacheSize)
	}

	memoryLimitPercent := viper.GetInt("memory_limit_percent")
	if memoryLimitPercent != 25 {
		t.Errorf("Expected memory_limit_percent to be 25, got %d", memoryLimitPercent)
	}

	memoryLimitMB := viper.GetInt("memory_limit_mb")
	if memoryLimitMB != 512 {
		t.Errorf("Expected memory_limit_mb to be 512, got %d", memoryLimitMB)
	}
}

// TestLoggingDefaults tests logging configuration defaults
func TestLoggingDefaults(t *testing.T) {
	// Arrange-Act-Assert: Test logging defaults
	viper.Reset()
	InitConfig()

	logLevel := viper.GetString("log_level")
	if logLevel != "info" {
		t.Errorf("Expected log_level to be 'info', got '%s'", logLevel)
	}

	logFormat := viper.GetString("log_format")
	if logFormat != "text" {
		t.Errorf("Expected log_format to be 'text', got '%s'", logFormat)
	}

	if enableJSON := viper.GetBool("enable_json_logging"); enableJSON {
		t.Error("Expected enable_json_logging to be false by default")
	}
}

// TestConfigStructure tests the Config struct
func TestConfigStructure(t *testing.T) {
	// Arrange
	config := Config{
		RootDir:              "/media/audiobooks",
		DatabasePath:         "/data/audiobooks.db",
		DatabaseType:         "pebble",
		EnableSQLite:         false,
		OrganizationStrategy: "auto",
		AutoFetchMetadata:    true,
		Language:             "en",
	}

	// Act & Assert
	if config.RootDir != "/media/audiobooks" {
		t.Errorf("Expected RootDir to be '/media/audiobooks', got '%s'", config.RootDir)
	}

	if config.DatabaseType != "pebble" {
		t.Errorf("Expected DatabaseType to be 'pebble', got '%s'", config.DatabaseType)
	}

	if config.EnableSQLite {
		t.Error("Expected EnableSQLite to be false")
	}

	if config.OrganizationStrategy != "auto" {
		t.Errorf("Expected OrganizationStrategy to be 'auto', got '%s'", config.OrganizationStrategy)
	}
}

// TestMetadataSourceStructure tests the MetadataSource struct
func TestMetadataSourceStructure(t *testing.T) {
	// Arrange
	credentials := map[string]string{
		"api_key": "test_key",
	}

	source := MetadataSource{
		ID:           "audible",
		Name:         "Audible",
		Enabled:      true,
		Priority:     1,
		RequiresAuth: true,
		Credentials:  credentials,
	}

	// Act & Assert
	if source.ID != "audible" {
		t.Errorf("Expected ID to be 'audible', got '%s'", source.ID)
	}

	if !source.Enabled {
		t.Error("Expected Enabled to be true")
	}

	if source.Priority != 1 {
		t.Errorf("Expected Priority to be 1, got %d", source.Priority)
	}

	if !source.RequiresAuth {
		t.Error("Expected RequiresAuth to be true")
	}

	if len(source.Credentials) != 1 {
		t.Errorf("Expected 1 credential, got %d", len(source.Credentials))
	}
}

// TestSupportedExtensionsDefaults tests supported extensions defaults
func TestSupportedExtensionsDefaults(t *testing.T) {
	// Arrange-Act
	viper.Reset()
	InitConfig()

	// Assert
	extensions := AppConfig.SupportedExtensions
	expectedExtensions := []string{".m4b", ".mp3", ".m4a", ".aac", ".ogg", ".flac", ".wma"}

	if len(extensions) != len(expectedExtensions) {
		t.Errorf("Expected %d extensions, got %d", len(expectedExtensions), len(extensions))
	}

	// Verify specific extensions
	extensionMap := make(map[string]bool)
	for _, ext := range extensions {
		extensionMap[ext] = true
	}

	for _, expected := range expectedExtensions {
		if !extensionMap[expected] {
			t.Errorf("Expected extension '%s' not found in defaults", expected)
		}
	}
}

// TestDatabaseTypeNormalization tests SQLite3 to SQLite normalization
func TestDatabaseTypeNormalization(t *testing.T) {
	// Arrange
	viper.Reset()
	viper.Set("database_type", "sqlite3")

	// Act
	InitConfig()

	// Assert
	if AppConfig.DatabaseType != "sqlite" {
		t.Errorf("Expected database_type to be normalized to 'sqlite', got '%s'", AppConfig.DatabaseType)
	}
}

// TestDefaultMetadataSources tests the default metadata sources configuration
func TestDefaultMetadataSources(t *testing.T) {
	// Arrange
	viper.Reset()

	// Act
	InitConfig()

	// Assert
	if len(AppConfig.MetadataSources) < 5 {
		t.Errorf("Expected at least 5 default metadata sources, got %d", len(AppConfig.MetadataSources))
	}

	// Verify Audible is first priority (best for audiobook search, no auth needed)
	audible := AppConfig.MetadataSources[0]
	if audible.ID != "audible" || !audible.Enabled || audible.Priority != 1 {
		t.Errorf("Audible metadata source not configured correctly: id=%s enabled=%v priority=%d", audible.ID, audible.Enabled, audible.Priority)
	}

	// Verify Open Library is second
	openlibrary := AppConfig.MetadataSources[1]
	if openlibrary.ID != "openlibrary" || !openlibrary.Enabled || openlibrary.Priority != 2 {
		t.Errorf("Open Library metadata source not configured correctly: id=%s enabled=%v priority=%d", openlibrary.ID, openlibrary.Enabled, openlibrary.Priority)
	}

	// Verify Audnexus is third (ASIN-only, limited title search)
	audnexus := AppConfig.MetadataSources[2]
	if audnexus.ID != "audnexus" || !audnexus.Enabled || audnexus.Priority != 3 {
		t.Errorf("Audnexus metadata source not configured correctly: id=%s enabled=%v priority=%d", audnexus.ID, audnexus.Enabled, audnexus.Priority)
	}
}

// TestConfigurationValidation tests basic configuration validation
func TestConfigurationValidation(t *testing.T) {
	// Arrange-Act-Assert: Verify that invalid database types are handled
	viper.Reset()
	InitConfig()

	// Verify valid database types
	validTypes := []string{"pebble", "sqlite"}
	dbType := AppConfig.DatabaseType

	isValid := false
	for _, valid := range validTypes {
		if dbType == valid {
			isValid = true
			break
		}
	}

	if !isValid {
		t.Errorf("Database type '%s' is not a valid type. Expected one of: %v", dbType, validTypes)
	}
}

// TestResetToDefaults tests ResetToDefaults function
func TestResetToDefaults(t *testing.T) {
	// Arrange: Set up custom configuration
	originalRootDir := AppConfig.RootDir
	originalDatabasePath := AppConfig.DatabasePath
	originalPlaylistDir := AppConfig.PlaylistDir

	AppConfig.RootDir = "/custom/root"
	AppConfig.DatabasePath = "/custom/db"
	AppConfig.PlaylistDir = "/custom/playlists"
	AppConfig.DatabaseType = "sqlite"
	AppConfig.EnableSQLite = true
	AppConfig.SetupComplete = true
	AppConfig.OrganizationStrategy = "manual"
	AppConfig.ScanOnStartup = true
	AppConfig.AutoOrganize = false
	AppConfig.FolderNamingPattern = "{title}"
	AppConfig.FileNamingPattern = "{title}"
	AppConfig.CreateBackups = false
	AppConfig.EnableDiskQuota = true
	AppConfig.DiskQuotaPercent = 50

	// Act: Reset to defaults
	ResetToDefaults()

	// Assert: Verify defaults are restored while paths are preserved
	if AppConfig.RootDir != "/custom/root" {
		t.Errorf("expected RootDir to be preserved as '/custom/root', got %q", AppConfig.RootDir)
	}
	if AppConfig.DatabasePath != "/custom/db" {
		t.Errorf("expected DatabasePath to be preserved as '/custom/db', got %q", AppConfig.DatabasePath)
	}
	if AppConfig.PlaylistDir != "/custom/playlists" {
		t.Errorf("expected PlaylistDir to be preserved as '/custom/playlists', got %q", AppConfig.PlaylistDir)
	}

	// Verify database defaults
	if AppConfig.DatabaseType != "pebble" {
		t.Errorf("expected DatabaseType to be reset to 'pebble', got %q", AppConfig.DatabaseType)
	}
	if AppConfig.EnableSQLite {
		t.Error("expected EnableSQLite to be reset to false")
	}
	if AppConfig.SetupComplete {
		t.Error("expected SetupComplete to be reset to false")
	}

	// Verify organization defaults
	if AppConfig.OrganizationStrategy != "auto" {
		t.Errorf("expected OrganizationStrategy to be reset to 'auto', got %q", AppConfig.OrganizationStrategy)
	}
	if AppConfig.ScanOnStartup {
		t.Error("expected ScanOnStartup to be reset to false")
	}
	if !AppConfig.AutoOrganize {
		t.Error("expected AutoOrganize to be reset to true")
	}

	// Verify naming pattern defaults
	expectedFolderPattern := "{author}/{series}/{title} ({print_year})"
	if AppConfig.FolderNamingPattern != expectedFolderPattern {
		t.Errorf("expected FolderNamingPattern to be reset to %q, got %q", expectedFolderPattern, AppConfig.FolderNamingPattern)
	}

	expectedFilePattern := "{title} - {author} - read by {narrator}"
	if AppConfig.FileNamingPattern != expectedFilePattern {
		t.Errorf("expected FileNamingPattern to be reset to %q, got %q", expectedFilePattern, AppConfig.FileNamingPattern)
	}

	// Verify backup defaults
	if !AppConfig.CreateBackups {
		t.Error("expected CreateBackups to be reset to true")
	}

	// Verify storage quota defaults
	if AppConfig.EnableDiskQuota {
		t.Error("expected EnableDiskQuota to be reset to false")
	}
	if AppConfig.DiskQuotaPercent != 80 {
		t.Errorf("expected DiskQuotaPercent to be reset to 80, got %d", AppConfig.DiskQuotaPercent)
	}

	// Restore original values
	AppConfig.RootDir = originalRootDir
	AppConfig.DatabasePath = originalDatabasePath
	AppConfig.PlaylistDir = originalPlaylistDir
}

// TestAppConfigRace_MutateSnapshot is a regression test for the data race that
// was detected at internal/config/update_service.go:130 (concurrent Mutate
// vs Snapshot calls).  Run with -race to confirm no races are reported.
//
// WHY: before T028, AppConfig was a plain package-level var with no
// synchronization.  The update service and test setups mutated it while
// background readers consumed it, triggering -race failures.  Mutate and
// Snapshot wrap all writes and snapshot-reads under the same RWMutex.
func TestAppConfigRace_MutateSnapshot(t *testing.T) {
	const goroutines = 8
	const iters = 500

	// Save and restore AppConfig around the test so other tests are unaffected.
	original := Snapshot()
	t.Cleanup(func() { Mutate(func(c *Config) { *c = original }) })

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(2)

		// Writer goroutine: simulates update_service applying a new root dir.
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				Mutate(func(c *Config) {
					c.RootDir = "/race/test/path"
					c.SetupComplete = c.RootDir != ""
				})
			}
		}(i)

		// Reader goroutine: simulates a background goroutine reading the snapshot.
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				snap := Snapshot()
				// Access a few fields to make the read non-trivial.
				_ = snap.RootDir
				_ = snap.SetupComplete
				_ = snap.OpenAIAPIKey
			}
		}(i)
	}

	wg.Wait()
}

func TestInitConfig_EmbeddingDefaults(t *testing.T) {
	viper.Reset()
	InitConfig()
	snap := Snapshot()

	assert.True(t, snap.Embedding.Enabled)
	assert.Equal(t, "text-embedding-3-large", snap.Embedding.Model)
	assert.Equal(t, 3072, snap.Embedding.Dimensions)
	assert.Equal(t, "", snap.Embedding.BaseURL)
	assert.Equal(t, "chromem", snap.Embedding.VectorBackend)
}

func TestInitConfig_EmbeddingFromEnv(t *testing.T) {
	t.Setenv("EMBEDDING_ENABLED", "false")
	t.Setenv("EMBEDDING_BASE_URL", "http://localhost:11434/v1")
	t.Setenv("EMBEDDING_MODEL", "bge-m3")
	t.Setenv("EMBEDDING_DIMENSIONS", "1024")
	t.Setenv("VECTOR_INDEX_BACKEND", "hnsw")
	viper.Reset()
	InitConfig()
	snap := Snapshot()

	assert.False(t, snap.Embedding.Enabled)
	assert.Equal(t, "http://localhost:11434/v1", snap.Embedding.BaseURL)
	assert.Equal(t, "bge-m3", snap.Embedding.Model)
	assert.Equal(t, 1024, snap.Embedding.Dimensions)
	assert.Equal(t, "hnsw", snap.Embedding.VectorBackend)
}
