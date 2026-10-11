// file: internal/config/log_retention_test.go
// version: 1.0.0
// guid: 2e7b4c91-0a6d-4f38-9c15-b8d3e6a0f472
// last-edited: 2026-10-11

// log_retention_days was never read by InitConfig, so AppConfig carried 0 on
// every boot and purge_old_logs was disabled everywhere; the first settings
// save then froze that 0 into the stored blob. These pin the repaired
// semantics: default 90 reaches AppConfig, 0 means the default, -1 is the
// only "keep forever", and a stored 0 is dropped from the blob once.

package config

import (
	"encoding/json"
	"testing"

	"github.com/spf13/viper"
)

func TestResolveLogRetentionDays(t *testing.T) {
	for _, tc := range []struct{ in, want int }{
		{in: 0, want: DefaultLogRetentionDays},
		{in: 30, want: 30},
		{in: LogRetentionKeepForever, want: 0},
		{in: -7, want: 0},
	} {
		if got := ResolveLogRetentionDays(tc.in); got != tc.want {
			t.Errorf("ResolveLogRetentionDays(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestInitConfigReadsLogRetentionDefault(t *testing.T) {
	restoreAppConfig(t)
	viper.Reset()
	t.Cleanup(viper.Reset)
	InitConfig()
	if got := Snapshot().LogRetentionDays; got != DefaultLogRetentionDays {
		t.Fatalf("LogRetentionDays after InitConfig = %d, want %d (the purge_old_logs task is disabled at 0)",
			got, DefaultLogRetentionDays)
	}
}

func TestValidateNormalizesZeroLogRetention(t *testing.T) {
	restoreAppConfig(t)
	viper.Reset()
	t.Cleanup(viper.Reset)
	InitConfig()
	cfg := Snapshot()
	for _, tc := range []struct{ in, want int }{
		{in: 0, want: DefaultLogRetentionDays},
		{in: LogRetentionKeepForever, want: LogRetentionKeepForever},
		{in: 14, want: 14},
	} {
		c := cfg
		c.LogRetentionDays = tc.in
		_ = c.Validate()
		if c.LogRetentionDays != tc.want {
			t.Errorf("Validate left log_retention_days=%d as %d, want %d", tc.in, c.LogRetentionDays, tc.want)
		}
	}
}

func TestBlobLogRetentionMigration(t *testing.T) {
	cases := []struct {
		name        string
		blob        string
		markerSet   bool
		want        int
		wantRewrite bool
	}{
		{name: "stored 0 is dropped and the default applies", blob: `{"log_retention_days":0}`, want: DefaultLogRetentionDays, wantRewrite: true},
		{name: "stored value kept", blob: `{"log_retention_days":30}`, want: 30},
		{name: "keep forever kept", blob: `{"log_retention_days":-1}`, want: LogRetentionKeepForever},
		{name: "absent key gets the default", blob: `{"root_dir":"/mnt/books"}`, want: DefaultLogRetentionDays},
		// After the marker, the blob is not rewritten again; a 0 that somehow
		// came back is still normalized in memory on load.
		{name: "marker set: no rewrite, still normalized", blob: `{"log_retention_days":0}`, markerSet: true, want: DefaultLogRetentionDays},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			restoreAppConfig(t)
			viper.Reset()
			t.Cleanup(viper.Reset)
			InitConfig()

			store := storeWithBlob(t, tc.blob)
			if tc.markerSet {
				if err := store.SetSetting(logRetentionZeroMigratedKey, "true", "bool", false); err != nil {
					t.Fatal(err)
				}
			}
			if err := LoadConfigFromDatabase(store); err != nil {
				t.Fatalf("LoadConfigFromDatabase: %v", err)
			}
			if got := Snapshot().LogRetentionDays; got != tc.want {
				t.Errorf("LogRetentionDays = %d, want %d", got, tc.want)
			}
			if m, _ := store.GetSetting(logRetentionZeroMigratedKey); m == nil {
				t.Error("migration marker not recorded")
			}

			// Other one-shot migrations (ai_endpoints derivation) also rewrite
			// the blob, so compare only this key, before and after.
			keyOf := func(blob string) (string, bool) {
				var raw map[string]json.RawMessage
				if err := json.Unmarshal([]byte(blob), &raw); err != nil {
					t.Fatalf("blob %s: %v", blob, err)
				}
				v, ok := raw["log_retention_days"]
				return string(v), ok
			}
			stored, _ := store.GetSetting("config_blob")
			before, hadKey := keyOf(tc.blob)
			after, hasKey := keyOf(stored.Value)
			if tc.wantRewrite {
				if hasKey {
					t.Errorf("stored log_retention_days=%s still present after migration", after)
				}
				return
			}
			if hadKey != hasKey || before != after {
				t.Errorf("log_retention_days changed in the stored blob: before %q (present=%v), after %q (present=%v)",
					before, hadKey, after, hasKey)
			}
		})
	}
}
