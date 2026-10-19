// file: internal/config/activity_db_path_env_test.go
// version: 2.0.0
// guid: 3f1c8a52-9d47-4e6b-8c03-2a7e5b9d1f64
// last-edited: 2026-10-19

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/viper"
)

// TestViperIsSetIsTrueForARegisteredDefault pins the trap behind envSupplied, so
// that nobody "simplifies" envSupplied back into viper.IsSet.
//
// The doc comment on applyEnvAuthoritativeConfig asserted for months that IsSet is
// "true only when a real override layer supplied the key, NOT for SetDefault". That is
// not how viper works: IsSet is Get(key) != nil, and a registered default IS a value.
// Every key guarded by IsSet is therefore assigned unconditionally. The key here is a
// probe that names no real setting, so the premise is tested on its own.
func TestViperIsSetIsTrueForARegisteredDefault(t *testing.T) {
	resetViper(t)
	viper.SetDefault("isset_probe_key", "")
	viper.BindEnv("isset_probe_key", "AO_ISSET_PROBE_KEY") //nolint:errcheck

	if !viper.IsSet("isset_probe_key") {
		t.Fatal("viper.IsSet is now false for a key with only a registered default — " +
			"the premise behind envSupplied changed; re-read applyEnvAuthoritativeConfig")
	}
	if got := viper.GetString("isset_probe_key"); got != "" {
		t.Fatalf("expected the empty default with no env set, got %q", got)
	}
}

// TestEnvSupplied covers the helper's contract directly, including the deliberate
// choice to treat an empty value as "not supplied".
func TestEnvSupplied(t *testing.T) {
	if envSupplied("AO_ENV_SUPPLIED_PROBE_DEFINITELY_UNSET_XYZ") {
		t.Error("an unset variable must not count as supplied")
	}

	t.Setenv("AO_ENV_SUPPLIED_PROBE", "")
	if envSupplied("AO_ENV_SUPPLIED_PROBE") {
		t.Error("an empty value must not count as supplied — systemd `Environment=FOO=` " +
			"must behave like an unset variable, not blank a persisted setting")
	}

	t.Setenv("AO_ENV_SUPPLIED_PROBE", "/mnt/x/value")
	if !envSupplied("AO_ENV_SUPPLIED_PROBE") {
		t.Error("a real value must count as supplied")
	}
}

// TestActivityBackend_BlobCannotSelectTheBackend: with the environment silent,
// the applied value is the registered "pebble" default, so a blob restored from
// before 01-P75 that still says "sqlite" is replaced rather than kept. A stored
// blob is not something the operator can see or edit, so it must never be what
// stops startup; a config file or the environment is (see the next test).
func TestActivityBackend_BlobCannotSelectTheBackend(t *testing.T) {
	resetViper(t)
	InitConfig() // no ACTIVITY_BACKEND in the environment

	c := &Config{ActivityBackend: "sqlite"} // as if restored from the blob
	applyEnvAuthoritativeConfig(c)

	if c.ActivityBackend != "pebble" {
		t.Fatalf("ActivityBackend = %q, want \"pebble\" — the blob must not be able to select "+
			"the activity backend when the operator's environment is silent", c.ActivityBackend)
	}
}

// TestActivityBackend_SQLiteFromConfFailsValidate is the fail-loud guarantee of
// 01-P75: a conf that still says activity_backend: sqlite, in the config file or
// the environment, loads into the field and is then refused by Validate (which
// runs at startup and on every PUT) with a message saying what to do. It must
// never load quietly and run on Pebble.
func TestActivityBackend_SQLiteFromConfFailsValidate(t *testing.T) {
	t.Run("config file", func(t *testing.T) {
		resetViper(t)
		t.Setenv("ACTIVITY_BACKEND", "")
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte("activity_backend: sqlite\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		viper.SetConfigFile(path)
		if err := viper.ReadInConfig(); err != nil {
			t.Fatal(err)
		}
		InitConfig()
		assertSQLiteRefused(t, Snapshot())
	})
	t.Run("environment", func(t *testing.T) {
		resetViper(t)
		t.Setenv("ACTIVITY_BACKEND", "sqlite")
		InitConfig()
		c := Snapshot()
		// The blob overlay must not hide it either: the environment wins.
		c.ActivityBackend = "pebble"
		applyEnvAuthoritativeConfig(&c)
		assertSQLiteRefused(t, c)
	})
}

func assertSQLiteRefused(t *testing.T, c Config) {
	t.Helper()
	if c.ActivityBackend != "sqlite" {
		t.Fatalf("ActivityBackend = %q, want the conf's \"sqlite\" to reach the field so it can be refused", c.ActivityBackend)
	}
	err := c.Validate()
	if err == nil {
		t.Fatal("Validate accepted activity_backend: sqlite; startup would run on Pebble with the conf ignored")
	}
	if !strings.Contains(err.Error(), "activity_backend: sqlite is no longer supported") {
		t.Fatalf("Validate error does not name the removed backend: %v", err)
	}
}

// TestValidateActivityBackend pins the accepted values: empty (a config that
// predates the setting) and "pebble", in any case and padding. Everything else
// is an error, "sqlite" with its own message.
func TestValidateActivityBackend(t *testing.T) {
	for _, v := range []string{"", "pebble", "PEBBLE", " pebble "} {
		if err := ValidateActivityBackend(v); err != nil {
			t.Errorf("ValidateActivityBackend(%q) = %v, want nil", v, err)
		}
	}
	for _, v := range []string{"sqlite", " SQLite "} {
		err := ValidateActivityBackend(v)
		if err == nil || !strings.Contains(err.Error(), "sqlite is no longer supported") {
			t.Errorf("ValidateActivityBackend(%q) = %v, want the sqlite-removed error", v, err)
		}
	}
	for _, v := range []string{"pebbel", "nutsdb", "dual"} {
		err := ValidateActivityBackend(v)
		if err == nil || !strings.Contains(err.Error(), "is not a supported value") {
			t.Errorf("ValidateActivityBackend(%q) = %v, want the unsupported-value error", v, err)
		}
	}
}
