// file: internal/config/metadata_fetch_config_test.go
// version: 1.2.1
// guid: 14574916-bcbe-4887-ba11-5520e206bed2
// last-edited: 2026-11-09

package config

import (
	"testing"
)

// ResetToDefaultsForTest restores the fetch-cache TTL: 0 would mean "never expire"
// and every cached answer would live forever after a reset.
func TestResetToDefaults_MetadataFetchCacheTTL(t *testing.T) {
	restoreAppConfig(t)
	Mutate(func(c *Config) {
		c.MetadataFetchCacheTTLDays = 3
		c.MetadataFetch.SharedCache = false
		c.MetadataFetch.CatalogStage = true
		c.MetadataFetch.FolderParsePersonTitle = false
	})
	ResetToDefaultsForTest()
	snap := Snapshot()
	if snap.MetadataFetchCacheTTLDays != DefaultMetadataFetchCacheTTLDays {
		t.Errorf("MetadataFetchCacheTTLDays after reset = %d, want %d", snap.MetadataFetchCacheTTLDays, DefaultMetadataFetchCacheTTLDays)
	}
	if snap.MetadataFetch != DefaultMetadataFetchConfig() {
		t.Errorf("MetadataFetch after reset = %+v, want the defaults", snap.MetadataFetch)
	}
}
