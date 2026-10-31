// file: internal/config/reset_factory_persist_test.go
// version: 1.0.0
// guid: 3c5e8a74-1b2d-4f60-9a8e-7d4c2b1e6f05
// last-edited: 2026-10-30

package config

import (
	"encoding/json"
	"testing"
)

// TestFactoryResetPersistsNonZeroForPreviouslyMissingLeaves runs the two calls
// FactoryReset makes (ResetToDefaults, SaveConfigToDatabase) and reads the
// stored blob back. Before ResetToDefaults was derived from the viper defaults
// these leaves were absent from its literal and came out zero/null.
func TestFactoryResetPersistsNonZeroForPreviouslyMissingLeaves(t *testing.T) {
	restoreAppConfig(t)
	ResetToDefaults()
	store := newMockSettingsStore()
	if err := SaveConfigToDatabase(store); err != nil {
		t.Fatalf("SaveConfigToDatabase: %v", err)
	}
	s, err := store.GetSetting("config_blob")
	if err != nil {
		t.Fatalf("config_blob not stored: %v", err)
	}
	var blob map[string]any
	if err := json.Unmarshal([]byte(s.Value), &blob); err != nil {
		t.Fatalf("blob is not JSON: %v", err)
	}

	pick := func(path ...string) any {
		var cur any = blob
		for _, p := range path {
			m, ok := cur.(map[string]any)
			if !ok {
				return nil
			}
			cur = m[p]
		}
		return cur
	}
	for _, path := range [][]string{
		{"search", "result_cache", "max_bytes"},
		{"search", "result_cache", "wait_seconds"},
		{"search", "result_cache", "enabled"},
		{"fp_parallel_workers"},
		{"fingerprint_length_sec"},
		{"oauth_default_role"},
		{"backup_compression"},
		{"activity_backend"},
		{"telemetry_environment"},
		{"library_counts_cache_min_interval_seconds"},
		{"list_warmer_heap_delta_mb"},
		{"abs_auth_modes"},
		{"abs_access_token_ttl"},
		{"scheduled", "dedup_refresh", "interval"},
	} {
		switch v := pick(path...).(type) {
		case nil:
			t.Errorf("%v is absent/null in the stored blob", path)
		case float64:
			if v == 0 {
				t.Errorf("%v = 0 in the stored blob", path)
			}
		case string:
			if v == "" {
				t.Errorf("%v is empty in the stored blob", path)
			}
		case bool:
			if !v {
				t.Errorf("%v = false in the stored blob, want the true default", path)
			}
		}
	}
}
