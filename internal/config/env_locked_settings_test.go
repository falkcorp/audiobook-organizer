// file: internal/config/env_locked_settings_test.go
// version: 1.3.0
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

// TestSettingLockedBy_EmptyWhenTheEnvironmentIsSilent is the case that matters
// for every developer machine and for any deployment that does not pin these in a
// unit file: with nothing set, no control should be marked read-only.
func TestSettingLockedBy_EmptyWhenTheEnvironmentIsSilent(t *testing.T) {
	for _, v := range []string{"DATABASE_PATH", "ACTIVITY_BACKEND"} {
		t.Setenv(v, "")
	}
	for key := range envLockedSettings {
		if got := SettingLockedBy(key); got != "" {
			t.Errorf("SettingLockedBy(%s) = %q, want \"\" — nothing in the environment is set", key, got)
		}
	}
}

// TestSettingLockedBy_ABSKeysAreNeverReported: the environment-only ABS keys
// are refused on write (TestABSConfig_UpdateRefusesEnvOnlyKeys), but they are not
// locked, set or unset. A lock means "a variable is forcing this control", and no
// Settings control renders an ABS key.
func TestSettingLockedBy_ABSKeysAreNeverReported(t *testing.T) {
	for _, set := range []bool{false, true} {
		for _, envVar := range absEnvOnlySettings {
			if set {
				t.Setenv(envVar, "1")
			} else {
				t.Setenv(envVar, "")
			}
		}
		for key := range absEnvOnlySettings {
			if got := SettingLockedBy(key); got != "" {
				t.Errorf("variables set=%v: SettingLockedBy(%s) = %q", set, key, got)
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

// TestSettingLockedBy_ReportsOnlyWhatIsActuallySupplied: locks are per key, so
// pinning the database path from a unit file must not also lock an unrelated
// setting.
func TestSettingLockedBy_ReportsOnlyWhatIsActuallySupplied(t *testing.T) {
	t.Setenv("ACTIVITY_BACKEND", "")
	t.Setenv("DATABASE_PATH", "/var/lib/audiobook-organizer/audiobooks.pebble")

	if got := SettingLockedBy("database_path"); got != "DATABASE_PATH" {
		t.Errorf("SettingLockedBy(database_path) = %q, want DATABASE_PATH", got)
	}
	if got := SettingLockedBy("activity_backend"); got != "" {
		t.Errorf("SettingLockedBy(activity_backend) = %q — it was not supplied", got)
	}
}

// TestSettingLockedBy_AnEmptyValueIsNotALock mirrors envSupplied's rule at the API
// boundary. systemd `Environment=DATABASE_PATH=` and an unset variable must behave
// identically: neither overrides the saved config, so neither may disable the control.
// Reporting a lock here would leave the operator with a field they cannot edit and a
// setting nothing is actually forcing.
func TestSettingLockedBy_AnEmptyValueIsNotALock(t *testing.T) {
	t.Setenv("ACTIVITY_BACKEND", "")
	t.Setenv("DATABASE_PATH", "")

	if got := SettingLockedBy("database_path"); got != "" {
		t.Fatalf("SettingLockedBy(database_path) = %q — an empty value is not an override", got)
	}
}

// TestEnvLockedSettings_EveryKeyNamesARealConfigField guards the lock table from
// drifting. A typo'd key here is invisible at runtime — no lock is ever reported for
// the field a caller asks about, so the control silently stays editable.
func TestEnvLockedSettings_EveryKeyNamesARealConfigField(t *testing.T) {
	fields := configJSONFieldNames(t)
	for key := range envLockedSettings {
		if !slices.Contains(fields, key) {
			t.Errorf("envLockedSettings key %q is not a json field on Config", key)
		}
	}
}
