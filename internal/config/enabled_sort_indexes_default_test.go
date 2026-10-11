// file: internal/config/enabled_sort_indexes_default_test.go
// version: 1.0.0
// guid: 4c1e8a7d-2b95-4f63-9d0a-6e3b7f58c1a2
// last-edited: 2026-10-11

// enabled_sort_indexes was reported as null by GET /api/v1/config on prod, so
// the shipped default never reached cmd/root.go's SetEnabledSortIndexes. Two
// independent causes, both pinned here:
//
//  1. InitConfig builds AppConfig from an explicit struct literal and omitted
//     the field, so it was nil even with no stored blob.
//  2. A stored blob carrying "enabled_sort_indexes": null decodes to a nil
//     slice (an ABSENT key keeps the seeded default; a present null does not).
//
// An explicit list, including an explicit empty list, must still be honored.

package config

import (
	"slices"
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

func TestBlobEnabledSortIndexes(t *testing.T) {
	cases := []struct {
		name      string
		blob      string
		want      []string
		wantEmpty bool // want a non-nil empty list
	}{
		{name: "key absent keeps default", blob: `{"root_dir":"/mnt/books"}`, want: wantDefaultSortIndexes},
		{name: "null falls back to default", blob: `{"enabled_sort_indexes":null}`, want: wantDefaultSortIndexes},
		{name: "explicit list is honored", blob: `{"enabled_sort_indexes":["duration"]}`, want: []string{"duration"}},
		{name: "explicit empty list is honored", blob: `{"enabled_sort_indexes":[]}`, wantEmpty: true},
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
			got := Snapshot().EnabledSortIndexes
			if tc.wantEmpty {
				if got == nil || len(got) != 0 {
					t.Fatalf("explicit [] must stay a non-nil empty list, got %#v", got)
				}
				return
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("EnabledSortIndexes = %#v, want %v", got, tc.want)
			}
		})
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
