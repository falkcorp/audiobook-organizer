// file: internal/config/activity_db_path_env_test.go
// version: 1.0.0
// guid: 3f1c8a52-9d47-4e6b-8c03-2a7e5b9d1f64
// last-edited: 2026-09-07

package config

import (
	"strings"
	"testing"

	"github.com/spf13/viper"
)

// TestViperIsSetIsTrueForARegisteredDefault pins the trap that caused this bug, so
// that nobody "simplifies" envSupplied back into viper.IsSet.
//
// The doc comment on applyEnvAuthoritativeConfig asserted for months that IsSet is
// "true only when a real override layer supplied the key, NOT for SetDefault". That is
// not how viper works: IsSet is Get(key) != nil, and a registered default IS a value.
// Every key guarded by IsSet is therefore assigned unconditionally.
func TestViperIsSetIsTrueForARegisteredDefault(t *testing.T) {
	resetViper(t)
	viper.SetDefault("activity_db_path", "")
	viper.BindEnv("activity_db_path", "ACTIVITY_DB_PATH") //nolint:errcheck

	if !viper.IsSet("activity_db_path") {
		t.Fatal("viper.IsSet is now false for a key with only a registered default — " +
			"the premise behind envSupplied changed; re-read applyEnvAuthoritativeConfig")
	}
	if got := viper.GetString("activity_db_path"); got != "" {
		t.Fatalf("expected the empty default with no env set, got %q", got)
	}
}

// TestEnvSupplied covers the helper's contract directly, including the deliberate
// choice to treat an empty value as "not supplied".
func TestEnvSupplied(t *testing.T) {
	if envSupplied("ACTIVITY_DB_PATH_DEFINITELY_UNSET_XYZ") {
		t.Error("an unset variable must not count as supplied")
	}

	t.Setenv("ACTIVITY_DB_PATH", "")
	if envSupplied("ACTIVITY_DB_PATH") {
		t.Error("an empty value must not count as supplied — systemd `Environment=FOO=` " +
			"must behave like an unset variable, not blank a persisted setting")
	}

	t.Setenv("ACTIVITY_DB_PATH", "/mnt/x/activity.sqlite")
	if !envSupplied("ACTIVITY_DB_PATH") {
		t.Error("a real value must count as supplied")
	}
}

// TestActivityDBPath_BlobSurvivesWhenEnvIsAbsent is the regression guard for the
// user-visible defect: the activity-database location is settable in Settings, which
// persists it into the DB config blob. LoadConfigFromDatabase restores the blob and
// then calls ApplyEnvAuthoritativeConfig, which overwrote the field with the empty
// default on EVERY boot — so the control silently did nothing, everywhere, whether or
// not the operator had set ACTIVITY_DB_PATH.
//
// This exercises the real save/load path rather than calling the overlay directly,
// because the blob round-trip is the half that has to keep working for the setting to
// mean anything.
func TestActivityDBPath_BlobSurvivesWhenEnvIsAbsent(t *testing.T) {
	resetViper(t)
	InitConfig()
	store := newMockSettingsStore()

	const want = "/mnt/bigdata/books/audiobook-organizer/.activity/activity.sqlite"
	Mutate(func(c *Config) { c.ActivityDBPath = want })
	if err := SaveConfigToDatabase(store); err != nil {
		t.Fatalf("save: %v", err)
	}

	// The blob must actually carry the value; a passing round-trip that never
	// serialized it would prove nothing.
	blob, err := store.GetSetting("config_blob")
	if err != nil || blob == nil {
		t.Fatalf("config_blob not persisted: %v", err)
	}
	if !strings.Contains(blob.Value, want) {
		t.Fatal("config_blob does not contain the activity DB path — the setting is not persisted at all")
	}

	// Simulate the boot-time wipe: the blob load replaces the whole struct.
	Mutate(func(c *Config) { c.ActivityDBPath = "sentinel-should-be-replaced-by-blob" })
	if err := LoadConfigFromDatabase(store); err != nil {
		t.Fatalf("load: %v", err)
	}

	if got := Snapshot().ActivityDBPath; got != want {
		t.Fatalf("ActivityDBPath = %q after load with no ACTIVITY_DB_PATH set, want %q — "+
			"the persisted setting was destroyed by applyEnvAuthoritativeConfig", got, want)
	}
}

// TestActivityDBPath_EnvStillWinsOverBlob guards the other direction. The operator
// lever must not regress: when ACTIVITY_DB_PATH is set in the unit file it beats
// whatever the blob holds, which is how production currently pins the activity DB.
func TestActivityDBPath_EnvStillWinsOverBlob(t *testing.T) {
	resetViper(t)
	t.Setenv("ACTIVITY_DB_PATH", "/var/lib/audiobook-organizer/activity-v2.sqlite")
	InitConfig()

	c := &Config{ActivityDBPath: "/from/the/blob.sqlite"}
	applyEnvAuthoritativeConfig(c)

	if c.ActivityDBPath != "/var/lib/audiobook-organizer/activity-v2.sqlite" {
		t.Fatalf("ActivityDBPath = %q, want the environment value — "+
			"a systemd Environment= line must override the blob", c.ActivityDBPath)
	}
}

// TestActivityBackend_BlobStillCannotOverrideTheRollbackLever pins the deliberate
// asymmetry between the two activity keys, which is easy to "tidy up" into a bug.
//
// activity_db_path moved to envSupplied() so the UI can own it. activity_backend must
// NOT: it is the OOM rollback lever from the 2026-09-07 SQLite incident, and an
// operator who pulls the env var to stop an OOM loop must not have a restored blob
// re-engage SQLite underneath them.
func TestActivityBackend_BlobStillCannotOverrideTheRollbackLever(t *testing.T) {
	resetViper(t)
	InitConfig() // no ACTIVITY_BACKEND in the environment

	c := &Config{ActivityBackend: "sqlite"} // as if restored from the blob
	applyEnvAuthoritativeConfig(c)

	if c.ActivityBackend != "" {
		t.Fatalf("ActivityBackend = %q, want \"\" — the blob must not be able to select "+
			"the activity backend when the operator's environment is silent", c.ActivityBackend)
	}
}
