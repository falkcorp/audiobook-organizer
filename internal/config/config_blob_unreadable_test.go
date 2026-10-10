// file: internal/config/config_blob_unreadable_test.go
// version: 1.0.0
// guid: 7c41e9d2-5b8a-4f13-a6e0-2d9f3b7c8e14
// last-edited: 2026-10-10

package config

import (
	"maps"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// A config_blob row that exists but will not load used to log a Warn and fall
// back to the legacy per-key rows, i.e. run on defaults; the next
// SaveConfigToDatabase then overwrote the repairable blob. It is now an error
// (cmd runs LoadConfigFromDatabase as the Fatal step "loadConfigFromDB"), and
// nothing is written to the store or applied to AppConfig before it returns.
func TestLoadConfigFromDatabase_UnreadableBlobIsAnError(t *testing.T) {
	cases := []struct {
		name, blob, wantInErr string
	}{
		{"syntax error", `{"root_dir": "/x"`, "not a JSON object"},
		{"empty value", ``, "empty"},
		{"json null", `null`, "null"},
		{"array", `[1,2]`, "not a JSON object"},
		{"string", `"hello"`, "not a JSON object"},
		// Parses as an object but a field has the wrong type.
		{"field type mismatch", `{"root_dir": 5}`, "root_dir"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			restoreAppConfig(t)
			Mutate(func(c *Config) { c.RootDir = "/before" })

			store := newMockSettingsStore()
			store.settings["config_blob"] = &database.Setting{Key: "config_blob", Value: tc.blob, Type: "json"}
			// A legacy per-key row the old fallback would have applied.
			store.settings["root_dir"] = &database.Setting{Key: "root_dir", Value: "/legacy", Type: "string"}
			before := map[string]database.Setting{}
			for k, v := range store.settings {
				before[k] = *v
			}

			err := LoadConfigFromDatabase(store)
			if err == nil {
				t.Fatal("LoadConfigFromDatabase returned nil for an unreadable config_blob")
			}
			if !strings.Contains(err.Error(), "config_blob") || !strings.Contains(err.Error(), tc.wantInErr) {
				t.Fatalf("error %q should name config_blob and contain %q", err, tc.wantInErr)
			}
			if got := Snapshot().RootDir; got != "/before" {
				t.Fatalf("RootDir = %q: the legacy fallback or the blob was applied", got)
			}
			// No migration marker, no rewritten blob: the store is untouched.
			after := map[string]database.Setting{}
			for k, v := range store.settings {
				after[k] = *v
			}
			if !maps.Equal(before, after) {
				t.Fatalf("store was written before the error:\nbefore %+v\nafter  %+v", before, after)
			}
		})
	}
}

// With NO config_blob row the legacy per-key path still applies: that is the
// pre-blob install the fallback exists for.
func TestLoadConfigFromDatabase_NoBlobRowUsesLegacyKeys(t *testing.T) {
	restoreAppConfig(t)
	Mutate(func(c *Config) { c.RootDir = "/before" })

	store := newMockSettingsStore()
	store.settings["root_dir"] = &database.Setting{Key: "root_dir", Value: "/legacy", Type: "string"}

	if err := LoadConfigFromDatabase(store); err != nil {
		t.Fatalf("LoadConfigFromDatabase: %v", err)
	}
	if got := Snapshot().RootDir; got != "/legacy" {
		t.Fatalf("RootDir = %q, want the legacy row's /legacy", got)
	}
}
