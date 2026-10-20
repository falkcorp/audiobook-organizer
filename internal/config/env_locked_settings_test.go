// file: internal/config/env_locked_settings_test.go
// version: 1.2.2
// guid: 4f1c9a83-6d27-4b50-9e18-2c7a5b0e3d64
// last-edited: 2026-10-19

package config

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// configJSONFieldNames reflects over Config and returns every json tag name.
func configJSONFieldNames(t *testing.T) []string {
	t.Helper()
	typ := reflect.TypeFor[Config]()
	names := make([]string, 0, typ.NumField())
	for field := range typ.Fields() {
		tag := field.Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		names = append(names, strings.Split(tag, ",")[0])
	}
	return names
}

// TestEnvLockedSettings_EmptyWhenTheEnvironmentIsSilent is the case that matters
// for every developer machine and for any deployment that does not pin these in a
// unit file: with nothing set, no control should be marked read-only.
func TestEnvLockedSettings_EmptyWhenTheEnvironmentIsSilent(t *testing.T) {
	for _, v := range []string{"DATABASE_PATH", "ACTIVITY_BACKEND"} {
		t.Setenv(v, "")
	}
	if got := EnvLockedSettings(); len(got) != 0 {
		t.Fatalf("EnvLockedSettings() = %v, want empty — nothing in the environment is set", got)
	}
}

// TestEnvLockedSettings_ABSKeysAreNeverReported: the environment-only ABS keys
// are refused on write (TestABSConfig_UpdateRefusesEnvOnlyKeys), but they are not
// env_locked, set or unset. env_locked means "a variable is forcing this control",
// and no Settings control renders an ABS key.
func TestEnvLockedSettings_ABSKeysAreNeverReported(t *testing.T) {
	for _, set := range []bool{false, true} {
		for _, envVar := range absEnvOnlySettings {
			if set {
				t.Setenv(envVar, "1")
			} else {
				t.Setenv(envVar, "")
			}
		}
		locked, locks := EnvLockedSettings(), SettingLocks()
		for key := range absEnvOnlySettings {
			if slices.Contains(locked, key) {
				t.Errorf("variables set=%v: EnvLockedSettings() contains %s", set, key)
			}
			if _, ok := locks[key]; ok {
				t.Errorf("variables set=%v: SettingLocks() contains %s", set, key)
			}
		}
	}
}

func TestABSEnvOnlySettings_EveryKeyNamesARealConfigField(t *testing.T) {
	fields := configJSONFieldNames(t)
	for key := range absEnvOnlySettings {
		if !slices.Contains(fields, key) {
			t.Errorf("absEnvOnlySettings key %q is not a json field on Config", key)
		}
	}
}

// TestEnvLockedSettings_ReportsOnlyWhatIsActuallySupplied pins the behaviour the
// Settings page depends on: locks are per-key, so pinning the database path from a
// unit file must not also lock an unrelated setting.
func TestEnvLockedSettings_ReportsOnlyWhatIsActuallySupplied(t *testing.T) {
	t.Setenv("ACTIVITY_BACKEND", "")
	t.Setenv("DATABASE_PATH", "/var/lib/audiobook-organizer/audiobooks.pebble")

	got := EnvLockedSettings()
	if !slices.Contains(got, "database_path") {
		t.Errorf("EnvLockedSettings() = %v, want it to contain database_path", got)
	}
	if slices.Contains(got, "activity_backend") {
		t.Errorf("EnvLockedSettings() = %v — activity_backend was not supplied", got)
	}
}

// TestEnvLockedSettings_AnEmptyValueIsNotALock mirrors envSupplied's rule at the API
// boundary. systemd `Environment=DATABASE_PATH=` and an unset variable must behave
// identically: neither overrides the saved config, so neither may disable the control.
// Reporting a lock here would leave the operator with a field they cannot edit and a
// setting nothing is actually forcing.
func TestEnvLockedSettings_AnEmptyValueIsNotALock(t *testing.T) {
	t.Setenv("ACTIVITY_BACKEND", "")
	t.Setenv("DATABASE_PATH", "")

	if got := EnvLockedSettings(); slices.Contains(got, "database_path") {
		t.Fatalf("EnvLockedSettings() = %v — an empty value is not an override", got)
	}
}

// TestEnvLockedSettings_IsSorted keeps the API response stable so a client can
// compare successive payloads without spurious diffs from Go's map iteration order.
func TestEnvLockedSettings_IsSorted(t *testing.T) {
	t.Setenv("DATABASE_PATH", "/tmp/a.pebble")
	t.Setenv("ACTIVITY_BACKEND", "pebble")

	got := EnvLockedSettings()
	if !slices.IsSorted(got) {
		t.Errorf("EnvLockedSettings() = %v, want sorted", got)
	}
	if len(got) != 2 {
		t.Errorf("EnvLockedSettings() = %v, want both keys locked", got)
	}
}

// TestEnvLockedSettings_EveryKeyNamesARealConfigField guards the pair of maps from
// drifting. A typo'd key here is invisible at runtime — the server reports a lock for
// a field the UI never looks up, so the control silently stays editable.
func TestEnvLockedSettings_EveryKeyNamesARealConfigField(t *testing.T) {
	fields := configJSONFieldNames(t)
	for key := range envLockedSettings {
		if !slices.Contains(fields, key) {
			t.Errorf("envLockedSettings key %q is not a json field on Config", key)
		}
	}
}
