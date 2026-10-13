// file: internal/config/metadata_fetch_config_test.go
// version: 1.2.0
// guid: 14574916-bcbe-4887-ba11-5520e206bed2
// last-edited: 2026-10-12

package config

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// The documented rollback of the shared provider-answer cache is
// PUT /api/v1/config {"metadata_fetch":{"shared_cache":false}}. It must flip
// the switch, leave the negative TTLs it does not name alone, and persist.
func TestUpdateConfig_SharedCacheRollbackSwitch(t *testing.T) {
	restoreAppConfig(t)
	Mutate(func(c *Config) {
		c.EnableAuth = false
		c.MetadataFetch = DefaultMetadataFetchConfig()
	})
	ms, blobs := ladderTestStore(t)

	status, resp := NewUpdateService(ms).UpdateConfig(context.Background(), map[string]any{
		"metadata_fetch": map[string]any{"shared_cache": false},
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d; resp = %v", status, resp)
	}
	got := Snapshot().MetadataFetch
	if got.SharedCache {
		t.Fatal("shared_cache is still on after the rollback PUT")
	}
	if got.NegativeTTLDays != DefaultMetadataFetchConfig().NegativeTTLDays {
		t.Errorf("the rollback PUT changed the negative TTLs it did not name: %+v", got.NegativeTTLDays)
	}
	if len(*blobs) == 0 || !strings.Contains((*blobs)[len(*blobs)-1], `"shared_cache":false`) {
		t.Errorf("the rollback was not persisted; blobs = %v", *blobs)
	}

	status, resp = NewUpdateService(ms).UpdateConfig(context.Background(), map[string]any{
		"metadata_fetch": map[string]any{"shared_cache": true},
	})
	if status != http.StatusOK || !Snapshot().MetadataFetch.SharedCache {
		t.Fatalf("turning the switch back on: status = %d, shared_cache = %v; resp = %v",
			status, Snapshot().MetadataFetch.SharedCache, resp)
	}
}

// The local catalog stage ships off, is switched by
// PUT /api/v1/config {"metadata_fetch":{"catalog_stage":true}}, persists, and
// leaves the shared-cache switch it does not name alone.
func TestUpdateConfig_CatalogStageSwitch(t *testing.T) {
	restoreAppConfig(t)
	Mutate(func(c *Config) {
		c.EnableAuth = false
		c.MetadataFetch = DefaultMetadataFetchConfig()
	})
	if Snapshot().MetadataFetch.CatalogStage {
		t.Fatal("catalog_stage must ship off")
	}
	ms, blobs := ladderTestStore(t)

	status, resp := NewUpdateService(ms).UpdateConfig(context.Background(), map[string]any{
		"metadata_fetch": map[string]any{"catalog_stage": true},
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d; resp = %v", status, resp)
	}
	got := Snapshot().MetadataFetch
	if !got.CatalogStage || !got.SharedCache {
		t.Fatalf("after turning catalog_stage on: %+v (shared_cache must be untouched)", got)
	}
	if len(*blobs) == 0 || !strings.Contains((*blobs)[len(*blobs)-1], `"catalog_stage":true`) {
		t.Errorf("the switch was not persisted; blobs = %v", *blobs)
	}

	status, resp = NewUpdateService(ms).UpdateConfig(context.Background(), map[string]any{
		"metadata_fetch": map[string]any{"catalog_stage": false},
	})
	if status != http.StatusOK || Snapshot().MetadataFetch.CatalogStage {
		t.Fatalf("turning it back off: status = %d, catalog_stage = %v; resp = %v", status, Snapshot().MetadataFetch.CatalogStage, resp)
	}
}

// The folder-parse variant ships on and rolls back with
// PUT /api/v1/config {"metadata_fetch":{"folder_parse_person_title":false}},
// which persists and leaves the other metadata_fetch switches alone.
func TestUpdateConfig_FolderParsePersonTitleSwitch(t *testing.T) {
	restoreAppConfig(t)
	Mutate(func(c *Config) {
		c.EnableAuth = false
		c.MetadataFetch = DefaultMetadataFetchConfig()
	})
	if !Snapshot().MetadataFetch.FolderParsePersonTitle {
		t.Fatal("folder_parse_person_title must ship on")
	}
	ms, blobs := ladderTestStore(t)

	status, resp := NewUpdateService(ms).UpdateConfig(context.Background(), map[string]any{
		"metadata_fetch": map[string]any{"folder_parse_person_title": false},
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d; resp = %v", status, resp)
	}
	got := Snapshot().MetadataFetch
	if got.FolderParsePersonTitle || !got.SharedCache || got.CatalogStage {
		t.Fatalf("after the rollback PUT: %+v (only folder_parse_person_title may change)", got)
	}
	if len(*blobs) == 0 || !strings.Contains((*blobs)[len(*blobs)-1], `"folder_parse_person_title":false`) {
		t.Errorf("the rollback was not persisted; blobs = %v", *blobs)
	}

	status, resp = NewUpdateService(ms).UpdateConfig(context.Background(), map[string]any{
		"metadata_fetch": map[string]any{"folder_parse_person_title": true},
	})
	if status != http.StatusOK || !Snapshot().MetadataFetch.FolderParsePersonTitle {
		t.Fatalf("turning it back on: status = %d; resp = %v", status, resp)
	}
}

// ResetToDefaults restores the fetch-cache TTL: 0 would mean "never expire"
// and every cached answer would live forever after a reset.
func TestResetToDefaults_MetadataFetchCacheTTL(t *testing.T) {
	restoreAppConfig(t)
	Mutate(func(c *Config) {
		c.MetadataFetchCacheTTLDays = 3
		c.MetadataFetch.SharedCache = false
		c.MetadataFetch.CatalogStage = true
		c.MetadataFetch.FolderParsePersonTitle = false
	})
	ResetToDefaults()
	snap := Snapshot()
	if snap.MetadataFetchCacheTTLDays != DefaultMetadataFetchCacheTTLDays {
		t.Errorf("MetadataFetchCacheTTLDays after reset = %d, want %d", snap.MetadataFetchCacheTTLDays, DefaultMetadataFetchCacheTTLDays)
	}
	if snap.MetadataFetch != DefaultMetadataFetchConfig() {
		t.Errorf("MetadataFetch after reset = %+v, want the defaults", snap.MetadataFetch)
	}
}
