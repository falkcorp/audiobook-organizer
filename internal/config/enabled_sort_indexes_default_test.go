// file: internal/config/enabled_sort_indexes_default_test.go
// version: 1.4.0
// guid: 4c1e8a7d-2b95-4f63-9d0a-6e3b7f58c1a2
// last-edited: 2026-10-17

// enabled_sort_indexes was reported as null by GET /api/v1/config on prod, so
// the shipped default never reached cmd/root.go's SetEnabledSortIndexes,
// because InitConfig's explicit struct literal omitted the field.
//
// The setting is BOOT-ONLY: the store builds its sort indexes once, at open,
// from the value InitConfig read (default, config file, env). The stored blob
// is read after the store exists, so it can never change what was built.
// LoadConfigFromDatabase therefore pins the field to the boot value whatever
// the blob says, and UpdateConfig refuses a PUT that changes it, so GET always
// reports the set that is running.

package config

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/viper"
)

var wantDefaultSortIndexes = []string{"year", "created_at"}

func TestInitConfigResolvesEnabledSortIndexesDefault(t *testing.T) {
	restoreAppConfig(t)
	viper.Reset()
	t.Cleanup(viper.Reset)

	InitConfig()

	if got := Snapshot().EnabledSortIndexes; !slices.Equal(got, wantDefaultSortIndexes) {
		t.Fatalf("InitConfig left EnabledSortIndexes = %#v, want %v; cmd/root.go would hand "+
			"database.SetEnabledSortIndexes a nil list", got, wantDefaultSortIndexes)
	}
}

// Every blob shape loses to the boot value: absent, null, a different list
// (an operator's earlier PUT, or a list a past save froze into the blob) and
// an empty list. None of them was ever built.
func TestBlobEnabledSortIndexesIsIgnoredBootValueWins(t *testing.T) {
	cases := []struct {
		name string
		blob string
	}{
		{name: "key absent", blob: `{"root_dir":"/mnt/books"}`},
		{name: "null", blob: `{"enabled_sort_indexes":null}`},
		{name: "different list", blob: `{"enabled_sort_indexes":["duration"]}`},
		{name: "superset", blob: `{"enabled_sort_indexes":["year","created_at","duration"]}`},
		{name: "empty list", blob: `{"enabled_sort_indexes":[]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			restoreAppConfig(t)
			viper.Reset()
			t.Cleanup(viper.Reset)
			InitConfig() // pre-blob state, as at boot

			if err := LoadConfigFromDatabase(storeWithBlob(t, tc.blob)); err != nil {
				t.Fatalf("LoadConfigFromDatabase: %v", err)
			}
			if got := Snapshot().EnabledSortIndexes; !slices.Equal(got, wantDefaultSortIndexes) {
				t.Fatalf("EnabledSortIndexes = %#v after blob %s, want the boot value %v",
					got, tc.blob, wantDefaultSortIndexes)
			}
		})
	}
}

// A config-file [] builds no optional index, and a blob list must not make GET
// claim otherwise.
func TestBlobEnabledSortIndexesCannotOverrideBootEmptyList(t *testing.T) {
	restoreAppConfig(t)
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set("enabled_sort_indexes", []string{})
	InitConfig()
	if got := Snapshot().EnabledSortIndexes; len(got) != 0 {
		t.Fatalf("fixture: InitConfig EnabledSortIndexes = %#v, want empty", got)
	}

	if err := LoadConfigFromDatabase(storeWithBlob(t, `{"enabled_sort_indexes":["year"]}`)); err != nil {
		t.Fatalf("LoadConfigFromDatabase: %v", err)
	}
	if got := Snapshot().EnabledSortIndexes; len(got) != 0 {
		t.Fatalf("EnabledSortIndexes = %#v, want empty: the blob's list was never built", got)
	}
}

func TestUpdateConfigRefusesEnabledSortIndexesChange(t *testing.T) {
	cases := []struct {
		name string
		put  any
	}{
		{name: "add an index", put: []any{"year", "created_at", "duration"}},
		{name: "drop an index", put: []any{"year"}},
		{name: "empty", put: []any{}},
		{name: "null", put: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			restoreAppConfig(t)
			// Auth off: these tests are about the boot-only rule, not the
			// protected-field rule, and must not depend on what an earlier test
			// left in EnableAuth.
			Mutate(func(c *Config) {
				c.EnabledSortIndexes = slices.Clone(wantDefaultSortIndexes)
				c.EnableAuth = false
			})
			ms, blobs := ladderTestStore(t)
			svc := NewUpdateService(ms)

			status, resp := svc.UpdateConfig(context.Background(), map[string]any{
				"enabled_sort_indexes": tc.put,
				"log_level":            "warn",
			})
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; resp = %v", status, resp)
			}
			msg, _ := resp["error"].(string)
			for _, want := range []string{"config file", "ENABLED_SORT_INDEXES", "restart"} {
				if !strings.Contains(msg, want) {
					t.Errorf("refusal %q does not mention %q", msg, want)
				}
			}
			snap := Snapshot()
			if !slices.Equal(snap.EnabledSortIndexes, wantDefaultSortIndexes) {
				t.Errorf("EnabledSortIndexes = %#v after a refused PUT, want %v", snap.EnabledSortIndexes, wantDefaultSortIndexes)
			}
			if snap.LogLevel == "warn" {
				t.Error("refused PUT still applied log_level")
			}
			if len(*blobs) != 0 {
				t.Error("refused PUT persisted a blob")
			}
		})
	}
}

// A GET-then-PUT round trip carries the running set back. Same set, any order
// or case, is a no-op and must not block the rest of the save.
func TestUpdateConfigAcceptsUnchangedEnabledSortIndexes(t *testing.T) {
	restoreAppConfig(t)
	// Auth off: these tests are about the boot-only rule, not the
	// protected-field rule, and must not depend on what an earlier test
	// left in EnableAuth.
	Mutate(func(c *Config) {
		c.EnabledSortIndexes = slices.Clone(wantDefaultSortIndexes)
		c.EnableAuth = false
	})
	ms, blobs := ladderTestStore(t)
	svc := NewUpdateService(ms)

	status, resp := svc.UpdateConfig(context.Background(), map[string]any{
		"enabled_sort_indexes": []any{"Created_At", "year"},
		"log_level":            "warn",
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d; resp = %v", status, resp)
	}
	snap := Snapshot()
	if snap.LogLevel != "warn" {
		t.Errorf("log_level = %q, want warn", snap.LogLevel)
	}
	if !slices.Equal(snap.EnabledSortIndexes, wantDefaultSortIndexes) {
		t.Errorf("EnabledSortIndexes = %#v, want the running value %v unchanged", snap.EnabledSortIndexes, wantDefaultSortIndexes)
	}
	if len(*blobs) == 0 {
		t.Error("accepted PUT persisted no blob")
	}
}

// The duration-tier slices share the null mechanism and the same fix.
func TestBlobNullDurationTiersFallBackToDefaults(t *testing.T) {
	restoreAppConfig(t)
	viper.Reset()
	t.Cleanup(viper.Reset)
	InitConfig()
	want := slices.Clone(Snapshot().MetadataScoring.DurationTierScores)
	if len(want) == 0 {
		t.Fatal("fixture: expected a non-empty default duration_tier_scores")
	}

	blob := `{"metadata_scoring":{"duration_tier_scores":null,"duration_tier_multipliers":null}}`
	if err := LoadConfigFromDatabase(storeWithBlob(t, blob)); err != nil {
		t.Fatalf("LoadConfigFromDatabase: %v", err)
	}
	ms := Snapshot().MetadataScoring
	if !slices.Equal(ms.DurationTierScores, want) || len(ms.DurationTierMultipliers) == 0 {
		t.Fatalf("null duration tiers not restored: scores=%v multipliers=%v", ms.DurationTierScores, ms.DurationTierMultipliers)
	}
}

// A stored null for metadata_sources used to leave ZERO metadata sources, and
// a null supported_extensions a nil list. Both restore their shipped default;
// an explicit empty list is still honored.
func TestBlobNullMetadataSourcesAndExtensionsFallBackToDefaults(t *testing.T) {
	restoreAppConfig(t)
	viper.Reset()
	t.Cleanup(viper.Reset)
	InitConfig()
	base := Snapshot()
	if len(base.MetadataSources) == 0 || len(base.SupportedExtensions) == 0 {
		t.Fatalf("fixture: expected non-empty defaults, got %d sources, %d extensions",
			len(base.MetadataSources), len(base.SupportedExtensions))
	}
	wantIDs := make([]string, 0, len(base.MetadataSources))
	for _, s := range base.MetadataSources {
		wantIDs = append(wantIDs, s.ID)
	}

	if err := LoadConfigFromDatabase(storeWithBlob(t, `{"metadata_sources":null,"supported_extensions":null}`)); err != nil {
		t.Fatalf("LoadConfigFromDatabase: %v", err)
	}
	got := Snapshot()
	gotIDs := make([]string, 0, len(got.MetadataSources))
	for _, s := range got.MetadataSources {
		gotIDs = append(gotIDs, s.ID)
	}
	if !slices.Equal(gotIDs, wantIDs) {
		t.Errorf("metadata_sources after null = %v, want the defaults %v", gotIDs, wantIDs)
	}
	if !slices.Equal(got.SupportedExtensions, base.SupportedExtensions) {
		t.Errorf("supported_extensions after null = %v, want %v", got.SupportedExtensions, base.SupportedExtensions)
	}

	// The restored list must not share a credentials map with the config it
	// was copied from.
	for i := range got.MetadataSources {
		if got.MetadataSources[i].Credentials == nil || base.MetadataSources[i].Credentials == nil {
			continue
		}
		got.MetadataSources[i].Credentials["__probe"] = "x"
		if _, leaked := base.MetadataSources[i].Credentials["__probe"]; leaked {
			t.Fatalf("restored metadata_sources[%d] shares its credentials map with the pre-blob config", i)
		}
		delete(got.MetadataSources[i].Credentials, "__probe")
	}
}

func TestBlobEmptyMetadataSourcesIsHonored(t *testing.T) {
	restoreAppConfig(t)
	viper.Reset()
	t.Cleanup(viper.Reset)
	InitConfig()

	if err := LoadConfigFromDatabase(storeWithBlob(t, `{"metadata_sources":[],"supported_extensions":[]}`)); err != nil {
		t.Fatalf("LoadConfigFromDatabase: %v", err)
	}
	got := Snapshot()
	if got.MetadataSources == nil || len(got.MetadataSources) != 0 {
		t.Errorf("explicit [] metadata_sources = %#v, want a non-nil empty list", got.MetadataSources)
	}
	if got.SupportedExtensions == nil || len(got.SupportedExtensions) != 0 {
		t.Errorf("explicit [] supported_extensions = %#v, want a non-nil empty list", got.SupportedExtensions)
	}
}

// A factory reset rebuilds AppConfig from a literal, but cannot rebuild the
// store's indexes: GET must keep reporting the built set, and a PUT carrying
// that set back must still be accepted.
func TestResetToDefaultsKeepsBootOnlySortIndexes(t *testing.T) {
	restoreAppConfig(t)
	// Auth off: these tests are about the boot-only rule, not the
	// protected-field rule, and must not depend on what an earlier test
	// left in EnableAuth.
	Mutate(func(c *Config) {
		c.EnabledSortIndexes = slices.Clone(wantDefaultSortIndexes)
		c.EnableAuth = false
	})

	ResetToDefaults()

	if got := Snapshot().EnabledSortIndexes; !slices.Equal(got, wantDefaultSortIndexes) {
		t.Fatalf("EnabledSortIndexes after ResetToDefaults = %#v, want the built set %v", got, wantDefaultSortIndexes)
	}
	ms, _ := ladderTestStore(t)
	status, resp := NewUpdateService(ms).UpdateConfig(context.Background(), map[string]any{
		"enabled_sort_indexes": []any{"year", "created_at"},
	})
	if status != http.StatusOK {
		t.Fatalf("PUT of the running set after a reset: status = %d; resp = %v", status, resp)
	}
}

// captureWarnings routes slog.Default (which internal/logger writes through)
// into a buffer for the duration of the test and returns it.
func captureWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func savedBlob(t *testing.T, store *mockSettingsStore) string {
	t.Helper()
	row, ok := store.settings["config_blob"]
	if !ok || row == nil {
		t.Fatalf("SaveConfigToDatabase wrote no config_blob")
	}
	return row.Value
}

// A save must not persist the boot-only key, so the blob can never carry a
// value that goes stale when the operator changes the config file.
func TestSaveConfigToDatabaseOmitsBootOnlySortIndexes(t *testing.T) {
	restoreAppConfig(t)
	Mutate(func(c *Config) { c.EnabledSortIndexes = slices.Clone(wantDefaultSortIndexes) })
	store := newMockSettingsStore()

	if err := SaveConfigToDatabase(store); err != nil {
		t.Fatalf("SaveConfigToDatabase: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(savedBlob(t, store)), &m); err != nil {
		t.Fatalf("blob is not a JSON object: %v", err)
	}
	if _, present := m["enabled_sort_indexes"]; present {
		t.Fatalf("config_blob contains enabled_sort_indexes; boot-only keys must not be persisted")
	}
	if _, present := m["root_dir"]; !present {
		t.Fatalf("config_blob lost ordinary keys: %v", m)
	}
}

// The sanctioned workflow: save, then change the boot set via file/env and
// restart. Boot must neither warn nor list the key as a kept shipped default.
func TestBootAfterSaveWithChangedFileSortIndexesIsQuiet(t *testing.T) {
	restoreAppConfig(t)
	Mutate(func(c *Config) { c.EnabledSortIndexes = slices.Clone(wantDefaultSortIndexes) })
	store := newMockSettingsStore()
	if err := SaveConfigToDatabase(store); err != nil {
		t.Fatalf("SaveConfigToDatabase: %v", err)
	}

	built := []string{"year", "created_at", "duration"}
	Mutate(func(c *Config) { c.EnabledSortIndexes = slices.Clone(built) }) // the new boot's InitConfig value
	logs := captureWarnings(t)

	if err := LoadConfigFromDatabase(store); err != nil {
		t.Fatalf("LoadConfigFromDatabase: %v", err)
	}
	if got := Snapshot().EnabledSortIndexes; !slices.Equal(got, built) {
		t.Fatalf("EnabledSortIndexes = %v, want the file-configured %v", got, built)
	}
	if strings.Contains(logs.String(), "enabled_sort_indexes") {
		t.Fatalf("boot logged about enabled_sort_indexes after the sanctioned change:\n%s", logs.String())
	}
	if got := defaultsPreservedOverBlob(savedBlob(t, store), Snapshot()); slices.Contains(got, "enabled_sort_indexes") {
		t.Fatalf("boot-only key reported as a kept shipped default: %v", got)
	}
}

// A blob saved before this change can still hold a differing value: warn once
// (without the doubled subsystem prefix), and the next save drops the key.
func TestLegacyBlobSortIndexesWarnsOnceThenIsDroppedBySave(t *testing.T) {
	restoreAppConfig(t)
	Mutate(func(c *Config) { c.EnabledSortIndexes = []string{"year", "created_at", "duration"} })
	store := storeWithBlob(t, `{"enabled_sort_indexes":["year","created_at"]}`)
	logs := captureWarnings(t)

	if err := LoadConfigFromDatabase(store); err != nil {
		t.Fatalf("LoadConfigFromDatabase: %v", err)
	}
	if n := strings.Count(logs.String(), "stored enabled_sort_indexes="); n != 1 {
		t.Fatalf("legacy blob produced %d warnings, want 1:\n%s", n, logs.String())
	}
	if strings.Contains(logs.String(), "config: config: stored enabled_sort_indexes") {
		t.Fatalf("warning has a doubled subsystem prefix:\n%s", logs.String())
	}

	if err := SaveConfigToDatabase(store); err != nil {
		t.Fatalf("SaveConfigToDatabase: %v", err)
	}
	logs.Reset()
	if err := LoadConfigFromDatabase(store); err != nil {
		t.Fatalf("second LoadConfigFromDatabase: %v", err)
	}
	if strings.Contains(logs.String(), "enabled_sort_indexes") {
		t.Fatalf("warning recurred after a save:\n%s", logs.String())
	}
}
