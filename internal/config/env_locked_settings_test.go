// file: internal/config/env_locked_settings_test.go
// version: 1.1.0
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
	typ := reflect.TypeOf(Config{})
	names := make([]string, 0, typ.NumField())
	for i := range typ.NumField() {
		tag := typ.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		names = append(names, strings.Split(tag, ",")[0])
	}
	return names
}

// absEnvOnlyKeys is the sorted key set of absEnvOnlySettings, which is locked
// whatever the environment says.
func absEnvOnlyKeys() []string {
	keys := make([]string, 0, len(absEnvOnlySettings))
	for k := range absEnvOnlySettings {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// TestEnvLockedSettings_EmptyWhenTheEnvironmentIsSilent is the case that matters
// for every developer machine and for any deployment that does not pin these in a
// unit file: with nothing set, no ordinary control is marked read-only. Only the
// environment-only ABS keys remain, because the overlay applies their registered
// default over any saved value even when no variable is set.
func TestEnvLockedSettings_EmptyWhenTheEnvironmentIsSilent(t *testing.T) {
	for _, v := range []string{"ACTIVITY_DB_PATH", "ACTIVITY_DB_MOVE_ON_CHANGE", "ACTIVITY_BACKEND"} {
		t.Setenv(v, "")
	}
	if got := EnvLockedSettings(); !slices.Equal(got, absEnvOnlyKeys()) {
		t.Fatalf("EnvLockedSettings() = %v, want only the environment-only ABS keys %v", got, absEnvOnlyKeys())
	}
}

// TestEnvLockedSettings_ABSKeysAreAlwaysLockedWithTheirVariable: every ABS key is
// locked with no variable set, and SettingLocks names the variable to set.
func TestEnvLockedSettings_ABSKeysAreAlwaysLockedWithTheirVariable(t *testing.T) {
	t.Setenv("ABS_SESSION_MAX_TTL", "")
	locks := SettingLocks()
	for key, envVar := range absEnvOnlySettings {
		if got := locks[key]; got != envVar {
			t.Errorf("SettingLocks()[%s] = %q, want %q", key, got, envVar)
		}
	}
	if locks["abs_session_max_ttl"] != "ABS_SESSION_MAX_TTL" {
		t.Error("abs_session_max_ttl must be reported env-locked")
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
// Settings page depends on: locks are per-key, so pinning the path from a unit file
// must not also freeze the unrelated move toggle.
func TestEnvLockedSettings_ReportsOnlyWhatIsActuallySupplied(t *testing.T) {
	t.Setenv("ACTIVITY_DB_MOVE_ON_CHANGE", "")
	t.Setenv("ACTIVITY_BACKEND", "")
	t.Setenv("ACTIVITY_DB_PATH", "/var/lib/audiobook-organizer/activity-v2.sqlite")

	got := EnvLockedSettings()
	if !slices.Contains(got, "activity_db_path") {
		t.Errorf("EnvLockedSettings() = %v, want it to contain activity_db_path", got)
	}
	if slices.Contains(got, "activity_db_move_on_change") {
		t.Errorf("EnvLockedSettings() = %v — locking the path must not lock the move toggle", got)
	}
	if slices.Contains(got, "activity_backend") {
		t.Errorf("EnvLockedSettings() = %v — activity_backend was not supplied", got)
	}
}

// TestEnvLockedSettings_AnEmptyValueIsNotALock mirrors envSupplied's rule at the API
// boundary. systemd `Environment=ACTIVITY_DB_PATH=` and an unset variable must behave
// identically: neither overrides the saved config, so neither may disable the control.
// Reporting a lock here would leave the operator with a field they cannot edit and a
// setting nothing is actually forcing.
func TestEnvLockedSettings_AnEmptyValueIsNotALock(t *testing.T) {
	t.Setenv("ACTIVITY_DB_MOVE_ON_CHANGE", "")
	t.Setenv("ACTIVITY_BACKEND", "")
	t.Setenv("ACTIVITY_DB_PATH", "")

	if got := EnvLockedSettings(); slices.Contains(got, "activity_db_path") {
		t.Fatalf("EnvLockedSettings() = %v — an empty value is not an override", got)
	}
}

// TestEnvLockedSettings_IsSorted keeps the API response stable so a client can
// compare successive payloads without spurious diffs from Go's map iteration order.
func TestEnvLockedSettings_IsSorted(t *testing.T) {
	t.Setenv("ACTIVITY_DB_PATH", "/tmp/a.sqlite")
	t.Setenv("ACTIVITY_DB_MOVE_ON_CHANGE", "false")
	t.Setenv("ACTIVITY_BACKEND", "sqlite")

	got := EnvLockedSettings()
	if !slices.IsSorted(got) {
		t.Errorf("EnvLockedSettings() = %v, want sorted", got)
	}
	if len(got) != 3+len(absEnvOnlySettings) {
		t.Errorf("EnvLockedSettings() = %v, want all three keys locked plus the ABS keys", got)
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
