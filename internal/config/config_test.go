// file: internal/config/config_test.go
// version: 1.25.2
// guid: b2c3d4e5-f6a7-8b9c-0d1e-2f3a4b5c6d7e
// last-edited: 2026-11-09

package config

import (
	"reflect"
	"slices"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestFactoryDefaultsCore pins the shipped core defaults of a fresh install.
func TestFactoryDefaultsCore(t *testing.T) {
	d := factoryDefaultConfigForTest()

	if d.DatabaseType != "pebble" {
		t.Errorf("Expected database_type to be 'pebble', got '%s'", d.DatabaseType)
	}
	if d.OrganizationStrategy != "auto" {
		t.Errorf("Expected organization_strategy to be 'auto', got '%s'", d.OrganizationStrategy)
	}
	if d.ScanOnStartup {
		t.Error("Expected scan_on_startup to be false by default")
	}
	if !d.AutoOrganize {
		t.Error("Expected auto_organize to be true by default")
	}
	if want := "{author}/{series}/{title} ({print_year})"; d.FolderNamingPattern != want {
		t.Errorf("Expected folder_naming_pattern to be '%s', got '%s'", want, d.FolderNamingPattern)
	}
	if want := "{title} - {track:02d}"; d.FileNamingPattern != want {
		t.Errorf("Expected file_naming_pattern to be '%s', got '%s'", want, d.FileNamingPattern)
	}
	if !d.CreateBackups {
		t.Error("Expected create_backups to be true by default")
	}
}

// TestStorageQuotaDefaults tests storage quota default values
func TestStorageQuotaDefaults(t *testing.T) {
	d := factoryDefaultConfigForTest()

	if d.EnableDiskQuota {
		t.Error("Expected enable_disk_quota to be false by default")
	}
	if d.DiskQuotaPercent != 80 {
		t.Errorf("Expected disk_quota_percent to be 80, got %d", d.DiskQuotaPercent)
	}
	if d.EnableUserQuotas {
		t.Error("Expected enable_user_quotas to be false by default")
	}
}

// TestMetadataDefaults tests metadata configuration defaults
func TestMetadataDefaults(t *testing.T) {
	// Arrange-Act-Assert: Test metadata defaults
	AppConfig := factoryDefaultConfigForTest()

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
	d := factoryDefaultConfigForTest()

	if !d.EnableAIParsing {
		t.Error("Expected enable_ai_parsing to be true by default")
	}
	if d.OpenAIAPIKey != "" {
		t.Errorf("Expected openai_api_key to be empty by default, got '%s'", d.OpenAIAPIKey)
	}
}

// TestPerformanceDefaults tests performance configuration defaults
func TestPerformanceDefaults(t *testing.T) {
	if got := factoryDefaultConfigForTest().ConcurrentScans; got < 4 {
		t.Errorf("Expected concurrent_scans to be >= 4, got %d", got)
	}
}

// TestLoggingDefaults tests logging configuration defaults
func TestLoggingDefaults(t *testing.T) {
	if got := factoryDefaultConfigForTest().LogLevel; got != "info" {
		t.Errorf("Expected log_level to be 'info', got '%s'", got)
	}
}

// TestConfigStructure tests the Config struct
func TestConfigStructure(t *testing.T) {
	// Arrange
	config := Config{
		RootDir:              "/media/audiobooks",
		DatabasePath:         "/data/audiobooks.db",
		DatabaseType:         "pebble",
		OrganizationStrategy: "auto",
	}

	// Act & Assert
	if config.RootDir != "/media/audiobooks" {
		t.Errorf("Expected RootDir to be '/media/audiobooks', got '%s'", config.RootDir)
	}

	if config.DatabaseType != "pebble" {
		t.Errorf("Expected DatabaseType to be 'pebble', got '%s'", config.DatabaseType)
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
	AppConfig := factoryDefaultConfigForTest()

	// Assert
	extensions := AppConfig.SupportedExtensions
	expectedExtensions := []string{
		".m4b", ".mp3", ".m4a", ".aac", ".ogg", ".flac", ".wma",
		".opus", ".oga", ".wav", ".aiff", ".aif", ".mka", ".aax", ".aaxc",
	}

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

// TestDefaultMetadataSources tests the default metadata sources configuration
func TestDefaultMetadataSources(t *testing.T) {
	// Arrange
	AppConfig := factoryDefaultConfigForTest()

	// Assert
	if len(AppConfig.MetadataSources) < 5 {
		t.Fatalf("Expected at least 5 default metadata sources, got %d", len(AppConfig.MetadataSources))
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
	AppConfig := factoryDefaultConfigForTest()

	// Verify valid database types
	validTypes := []string{"pebble", "sqlite"}
	dbType := AppConfig.DatabaseType

	isValid := slices.Contains(validTypes, dbType)

	if !isValid {
		t.Errorf("Database type '%s' is not a valid type. Expected one of: %v", dbType, validTypes)
	}
}

// TestResetToDefaults tests ResetToDefaultsForTest function
func TestResetToDefaults(t *testing.T) {
	// Restore the WHOLE struct, not just the three paths this test used to put
	// back by hand: ResetToDefaultsForTest rewrites every field, so the other ~119 were
	// being left at factory defaults for whatever ran next.
	before := Snapshot()
	t.Cleanup(func() { Mutate(func(c *Config) { *c = before }) })

	// Arrange: Set up custom configuration
	originalRootDir := AppConfig.RootDir
	originalDatabasePath := AppConfig.DatabasePath
	originalPlaylistDir := AppConfig.PlaylistDir

	AppConfig.RootDir = "/custom/root"
	AppConfig.DatabasePath = "/custom/db"
	AppConfig.PlaylistDir = "/custom/playlists"
	AppConfig.DatabaseType = "sqlite"
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
	ResetToDefaultsForTest()

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

	expectedFilePattern := "{title} - {track:02d}"
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

	// Verify chapter consolidation defaults
	if AppConfig.ChapterConsolidationThresholdMin != 10 {
		t.Errorf("expected ChapterConsolidationThresholdMin to be reset to 10, got %d", AppConfig.ChapterConsolidationThresholdMin)
	}
	// The repair jobs' chapter limit is its own setting (owner 2026-10-03).
	if AppConfig.RepairChapterMaxMin != 120 {
		t.Errorf("expected RepairChapterMaxMin to be reset to 120, got %d", AppConfig.RepairChapterMaxMin)
	}

	// Verify ai_backend.local_base_url resets to empty, not a hardcoded LAN IP
	// (TASK-018). ResetToDefaultsForTest previously reinstated
	// http://192.168.0.20:11434/v1 here independently of the viper default.
	if AppConfig.AIBackend.LocalBaseURL != "" {
		t.Errorf("expected AIBackend.LocalBaseURL to be reset to empty, got %q", AppConfig.AIBackend.LocalBaseURL)
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
	for i := range goroutines {
		wg.Add(2)

		// Writer goroutine: simulates update_service applying a new root dir.
		go func(id int) {
			defer wg.Done()
			for range iters {
				Mutate(func(c *Config) {
					c.RootDir = "/race/test/path"
					c.SetupComplete = c.RootDir != ""
				})
			}
		}(i)

		// Reader goroutine: simulates a background goroutine reading the snapshot.
		go func(id int) {
			defer wg.Done()
			for range iters {
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

func TestFactoryDefaults_EmbeddingDefaults(t *testing.T) {
	snap := factoryDefaultConfigForTest()

	assert.True(t, snap.Embedding.Enabled)
	assert.Equal(t, "text-embedding-3-large", snap.Embedding.Model)
	assert.Equal(t, 3072, snap.Embedding.Dimensions)
	assert.Equal(t, "", snap.Embedding.BaseURL)
	// Was "chromem" until 2026-08-30. That assertion encoded the bug: chromem is
	// a brute-force cosine scan (210x slower than HNSW at 50K vectors, measured),
	// so the shipped default silently cost ~1.9 CPU-hours per dedup.full-scan on
	// a ~61K-book library. Deliberately changed, not quietly edited.
	assert.Equal(t, "hnsw", snap.Embedding.VectorBackend)
}

// TestFactoryDefaults_AIBackendLocalBaseURLDefaultsEmpty locks the fresh-install
// default for ai_backend.local_base_url at empty string. It previously
// hardcoded one developer's own LAN Ollama host
// (http://192.168.0.20:11434/v1), which every other install silently
// inherited as a dead endpoint default (TASK-018). It must never be
// reinstated as a shipped default; see the SetDefault call site and
// ResetToDefaultsForTest in config.go for the full rationale.
func TestFactoryDefaults_AIBackendLocalBaseURLDefaultsEmpty(t *testing.T) {
	snap := factoryDefaultConfigForTest()

	assert.Equal(t, "", snap.AIBackend.LocalBaseURL)
}

func TestFactoryDefaults_MetadataScoringDefaults(t *testing.T) {
	snap := factoryDefaultConfigForTest()
	assert.False(t, snap.MetadataScoring.EmbeddingEnabled)
	assert.Equal(t, 0.82, snap.MetadataScoring.EmbeddingMinScore)
	assert.Equal(t, 0.88, snap.MetadataScoring.EmbeddingBestMatch)
	assert.False(t, snap.MetadataScoring.LLMEnabled)
	assert.Equal(t, 0.05, snap.MetadataScoring.LLMRerankEpsilon)
	assert.Equal(t, 5, snap.MetadataScoring.LLMRerankTopK)
}

// The incremental iTunes sync was removed on 2026-10-08 (import is manual
// only), and with it the sync_enabled / sync_interval settings.
func TestITunesConfigHasNoSyncSettings(t *testing.T) {
	typ := reflect.TypeOf(ITunesConfig{})
	for _, name := range []string{"SyncEnabled", "SyncInterval"} {
		_, ok := typ.FieldByName(name)
		assert.False(t, ok, "ITunesConfig must carry no %s: there is no iTunes sync", name)
	}
}

func TestFactoryDefaults_MaintenanceDefaults(t *testing.T) {
	snap := factoryDefaultConfigForTest()
	assert.True(t, snap.Maintenance.Enabled)
	assert.Equal(t, 1, snap.Maintenance.WindowStart)
	assert.Equal(t, 4, snap.Maintenance.WindowEnd)
	assert.True(t, snap.Maintenance.DedupRefresh)
	assert.False(t, snap.Maintenance.AcoustIDOnlineLookup)
	assert.Equal(t, 5000, snap.Maintenance.AcoustIDNightlyLimit)
}

func TestFactoryDefaults_ScheduledDefaults(t *testing.T) {
	snap := factoryDefaultConfigForTest()
	assert.False(t, snap.Scheduled.DedupRefresh.Enabled)
	assert.Equal(t, 360, snap.Scheduled.DedupRefresh.Interval)
	assert.Equal(t, 1440, snap.Scheduled.DbOptimize.Interval)
	assert.False(t, snap.Scheduled.AIDedupBatch.Enabled)
	assert.Equal(t, 1440, snap.Scheduled.AIDedupBatch.Interval)
	assert.False(t, snap.Scheduled.ResolveProductionAuthors.Enabled)
}

func TestFactoryDefaults_AutoUpdateDefaults(t *testing.T) {
	snap := factoryDefaultConfigForTest()
	assert.False(t, snap.AutoUpdate.Enabled)
	assert.Equal(t, "stable", snap.AutoUpdate.Channel)
	assert.Equal(t, 60, snap.AutoUpdate.CheckMinutes)
	assert.Equal(t, 2, snap.AutoUpdate.WindowStart)
	assert.Equal(t, 5, snap.AutoUpdate.WindowEnd)
}
