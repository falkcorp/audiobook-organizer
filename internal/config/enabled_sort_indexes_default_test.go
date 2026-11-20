// file: internal/config/enabled_sort_indexes_default_test.go
// version: 1.4.1
// guid: 4c1e8a7d-2b95-4f63-9d0a-6e3b7f58c1a2
// last-edited: 2026-11-09

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
