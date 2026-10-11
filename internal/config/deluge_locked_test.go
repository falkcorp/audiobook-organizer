// file: internal/config/deluge_locked_test.go
// version: 1.1.0
// guid: 8e2c4a17-5b9d-4f63-a1d8-0c7e3f95b264
// last-edited: 2026-10-11

// The five Deluge connection settings are config/env-authoritative (owner
// decision 2026-10-11): a value the environment (DELUGE_*) or the server's config
// file supplies beats the stored settings blob, INCLUDING a stored empty value, and
// the UI shows the field locked. With neither set the stored value wins as before.
//
// Hosts are synthetic (192.0.2.0/24, RFC 5737); the passwords are throwaway strings.

package config

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/viper"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/logger/logtest"
)

const (
	delugeEnvURL    = "http://192.0.2.10:8112"
	delugeFileURL   = "http://192.0.2.11:8112"
	delugeStoredURL = "http://192.0.2.20:8112"
	delugeEnvPW     = "dlw-env-secret-7731"
	delugeStoredPW  = "dlw-stored-secret-4402"
)

// delugeInitConfig runs InitConfig the way cmd/root.go does (AutomaticEnv on),
// optionally with a config file whose YAML is fileYAML. viper.ReadConfig fills the
// same file layer ReadInConfig does, which is the layer viper.InConfig inspects.
func delugeInitConfig(t *testing.T, fileYAML string) {
	t.Helper()
	restoreAppConfig(t)
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.AutomaticEnv()
	if fileYAML != "" {
		viper.SetConfigType("yaml")
		if err := viper.ReadConfig(strings.NewReader(fileYAML)); err != nil {
			t.Fatalf("read config file fixture: %v", err)
		}
	}
	InitConfig()
}

func loadBlob(t *testing.T, blob string) Config {
	t.Helper()
	if err := LoadConfigFromDatabase(storeWithBlob(t, blob)); err != nil {
		t.Fatalf("LoadConfigFromDatabase: %v", err)
	}
	return Snapshot()
}

// setAllDelugeEnv sets the five variables to values that differ from every stored
// value used below.
func setAllDelugeEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DELUGE_WEB_URL", delugeEnvURL)
	t.Setenv("DELUGE_WEB_PASSWORD", delugeEnvPW)
	t.Setenv("DELUGE_DISCOVERY_LABEL", "env-label")
	t.Setenv("DELUGE_DISCOVERY_ENABLED", "true")
	t.Setenv("DELUGE_MOVE_ENABLED", "true")
}

func clearDelugeEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"DELUGE_WEB_URL", "DELUGE_WEB_PASSWORD", "DELUGE_DISCOVERY_LABEL",
		"DELUGE_DISCOVERY_ENABLED", "DELUGE_MOVE_ENABLED"} {
		t.Setenv(k, "")
	}
}

var delugeKeys = []string{"deluge_web_url", "deluge_web_password", "deluge_discovery_label",
	"deluge_discovery_enabled", "deluge_move_enabled"}

// The stored blob carries a DIFFERENT non-empty value for every field; the
// environment must win for all five.
func TestDelugeLock_EnvBeatsStoredValue(t *testing.T) {
	setAllDelugeEnv(t)
	delugeInitConfig(t, "")

	c := loadBlob(t, `{"deluge_web_url":"`+delugeStoredURL+`","deluge_web_password":"`+delugeStoredPW+`",`+
		`"deluge_discovery_label":"stored-label","deluge_discovery_enabled":false,"deluge_move_enabled":false}`)

	if c.DelugeWebURL != delugeEnvURL {
		t.Errorf("deluge_web_url = %q, want the environment value", c.DelugeWebURL)
	}
	if c.DelugeWebPassword != delugeEnvPW {
		t.Error("deluge_web_password: the stored value beat the environment")
	}
	if c.DelugeDiscoveryLabel != "env-label" {
		t.Errorf("deluge_discovery_label = %q, want the environment value", c.DelugeDiscoveryLabel)
	}
	if !c.DelugeDiscoveryEnabled || !c.DelugeMoveEnabled {
		t.Errorf("discovery=%v move=%v, want both true from the environment",
			c.DelugeDiscoveryEnabled, c.DelugeMoveEnabled)
	}
}

// The case that made DELUGE_* look dead in production: the blob writes every field
// it knows, so an install that has ever saved settings stores "" and false for the
// Deluge keys. That stored emptiness must not mask the environment.
func TestDelugeLock_EnvBeatsStoredEmptyValue(t *testing.T) {
	setAllDelugeEnv(t)
	delugeInitConfig(t, "")

	c := loadBlob(t, `{"deluge_web_url":"","deluge_web_password":"","deluge_discovery_label":"",`+
		`"deluge_discovery_enabled":false,"deluge_move_enabled":false}`)

	if c.DelugeWebURL != delugeEnvURL || c.DelugeWebPassword != delugeEnvPW ||
		c.DelugeDiscoveryLabel != "env-label" || !c.DelugeDiscoveryEnabled || !c.DelugeMoveEnabled {
		t.Fatalf("stored empty values masked the environment: url=%q label=%q discovery=%v move=%v pw-from-env=%v",
			c.DelugeWebURL, c.DelugeDiscoveryLabel, c.DelugeDiscoveryEnabled, c.DelugeMoveEnabled,
			c.DelugeWebPassword == delugeEnvPW)
	}
}

// With the environment (and file) silent nothing changes: the stored value wins, and
// nothing is reported as locked. Guards against "fixing" the above by applying the
// viper value unconditionally, which would blank every stored Deluge setting.
func TestDelugeLock_StoredValueWinsWhenEnvIsUnset(t *testing.T) {
	clearDelugeEnv(t)
	delugeInitConfig(t, "")

	c := loadBlob(t, `{"deluge_web_url":"`+delugeStoredURL+`","deluge_web_password":"`+delugeStoredPW+`",`+
		`"deluge_discovery_label":"stored-label","deluge_discovery_enabled":true,"deluge_move_enabled":true}`)

	if c.DelugeWebURL != delugeStoredURL || c.DelugeWebPassword != delugeStoredPW ||
		c.DelugeDiscoveryLabel != "stored-label" || !c.DelugeDiscoveryEnabled || !c.DelugeMoveEnabled {
		t.Fatalf("with env unset the stored values must survive: url=%q label=%q discovery=%v move=%v",
			c.DelugeWebURL, c.DelugeDiscoveryLabel, c.DelugeDiscoveryEnabled, c.DelugeMoveEnabled)
	}
	for _, k := range delugeKeys {
		if slices.Contains(EnvLockedSettings(), k) {
			t.Errorf("%s reported locked with neither env nor config file setting it", k)
		}
		if got := SettingLockedBy(k); got != "" {
			t.Errorf("SettingLockedBy(%s) = %q, want \"\"", k, got)
		}
	}
}

// A lock is per key: setting only the URL must leave the other four editable.
func TestDelugeLock_OnlyTheSuppliedKeyIsLocked(t *testing.T) {
	clearDelugeEnv(t)
	t.Setenv("DELUGE_WEB_URL", delugeEnvURL)
	delugeInitConfig(t, "")

	c := loadBlob(t, `{"deluge_web_url":"`+delugeStoredURL+`","deluge_discovery_label":"stored-label","deluge_move_enabled":true}`)

	if c.DelugeWebURL != delugeEnvURL {
		t.Errorf("deluge_web_url = %q, want the environment value", c.DelugeWebURL)
	}
	if c.DelugeDiscoveryLabel != "stored-label" || !c.DelugeMoveEnabled {
		t.Errorf("unsupplied keys must keep their stored value: label=%q move=%v",
			c.DelugeDiscoveryLabel, c.DelugeMoveEnabled)
	}
	if got := EnvLockedSettings(); !slices.Contains(got, "deluge_web_url") || slices.Contains(got, "deluge_discovery_label") {
		t.Errorf("EnvLockedSettings() = %v, want only deluge_web_url among the Deluge keys", got)
	}
	if got := SettingLocks()["deluge_web_url"]; got != "DELUGE_WEB_URL" {
		t.Errorf("SettingLocks()[deluge_web_url] = %q, want DELUGE_WEB_URL", got)
	}
}

// The config file is the second source, with the same effect as the environment.
func TestDelugeLock_ConfigFileBeatsStoredValue(t *testing.T) {
	clearDelugeEnv(t)
	delugeInitConfig(t, "deluge_web_url: "+delugeFileURL+"\ndeluge_discovery_label: file-label\n"+
		"deluge_move_enabled: false\ndeluge_discovery_enabled: true\n")

	c := loadBlob(t, `{"deluge_web_url":"`+delugeStoredURL+`","deluge_discovery_label":"","deluge_move_enabled":true,"deluge_discovery_enabled":false}`)

	if c.DelugeWebURL != delugeFileURL || c.DelugeDiscoveryLabel != "file-label" || !c.DelugeDiscoveryEnabled {
		t.Errorf("config file did not beat the blob: url=%q label=%q discovery=%v",
			c.DelugeWebURL, c.DelugeDiscoveryLabel, c.DelugeDiscoveryEnabled)
	}
	// false in the file is a real value, not an absence.
	if c.DelugeMoveEnabled {
		t.Error("deluge_move_enabled: false in the config file must force the toggle off over a stored true")
	}
	for _, k := range []string{"deluge_web_url", "deluge_discovery_label", "deluge_move_enabled", "deluge_discovery_enabled"} {
		if got := SettingLocks()[k]; got != LockedByConfigFile {
			t.Errorf("SettingLocks()[%s] = %q, want %q", k, got, LockedByConfigFile)
		}
	}
	if _, ok := SettingLocks()["deluge_web_password"]; ok {
		t.Error("deluge_web_password is not in the file and must not be locked")
	}
}

// An empty value in the file is not an override, mirroring an empty environment
// variable: it must not blank a stored setting.
func TestDelugeLock_EmptyConfigFileValueIsNotALock(t *testing.T) {
	clearDelugeEnv(t)
	delugeInitConfig(t, "deluge_web_url: \"\"\ndeluge_discovery_label:\n")

	c := loadBlob(t, `{"deluge_web_url":"`+delugeStoredURL+`","deluge_discovery_label":"stored-label"}`)

	if c.DelugeWebURL != delugeStoredURL || c.DelugeDiscoveryLabel != "stored-label" {
		t.Errorf("empty file values blanked stored settings: url=%q label=%q", c.DelugeWebURL, c.DelugeDiscoveryLabel)
	}
	if got := SettingLockedBy("deluge_web_url"); got != "" {
		t.Errorf("SettingLockedBy = %q for an empty file value, want \"\"", got)
	}
}

// Both set: the environment wins, as viper orders them, and is what the UI names.
func TestDelugeLock_EnvBeatsConfigFile(t *testing.T) {
	clearDelugeEnv(t)
	t.Setenv("DELUGE_WEB_URL", delugeEnvURL)
	delugeInitConfig(t, "deluge_web_url: "+delugeFileURL+"\n")

	c := loadBlob(t, `{"deluge_web_url":"`+delugeStoredURL+`"}`)

	if c.DelugeWebURL != delugeEnvURL {
		t.Errorf("deluge_web_url = %q, want the environment value over the file", c.DelugeWebURL)
	}
	if got := SettingLockedBy("deluge_web_url"); got != "DELUGE_WEB_URL" {
		t.Errorf("SettingLockedBy = %q, want the environment variable when both apply", got)
	}
}

// PUT /config on a locked Deluge key is handled exactly like a locked
// activity_db_move_on_change (the existing env-locked toggle): accepted and
// persisted for an interactive session -- the lock is advisory at the API -- and then
// overridden by the supplied value on the next load.
func TestDelugeLock_PutIsHandledLikeOtherLockedSettings(t *testing.T) {
	setAllDelugeEnv(t)
	t.Setenv("ACTIVITY_DB_MOVE_ON_CHANGE", "true")
	delugeInitConfig(t, "")
	store := newMockSettingsStore()
	us := NewUpdateService(store)
	ctx := auth.WithMethod(context.Background(), auth.MethodSession)

	if !slices.Contains(EnvLockedSettings(), "activity_db_move_on_change") {
		t.Fatal("precondition: the comparison setting must be locked")
	}
	statusOther, respOther := us.UpdateConfig(ctx, map[string]any{"activity_db_move_on_change": false})
	statusDeluge, respDeluge := us.UpdateConfig(ctx, map[string]any{"deluge_web_url": delugeStoredURL})
	if statusDeluge != statusOther || statusDeluge != http.StatusOK {
		t.Fatalf("PUT of a locked deluge key returned %d (%v); a locked activity_db_move_on_change returned %d (%v) -- must match and be accepted",
			statusDeluge, respDeluge["error"], statusOther, respOther["error"])
	}
	blob, err := store.GetSetting("config_blob")
	if err != nil || blob == nil || !strings.Contains(blob.Value, delugeStoredURL) {
		t.Fatalf("PUT of a locked key must be stored like the others; blob=%v err=%v", blob, err)
	}

	// Next boot: the stored value is overridden.
	delugeInitConfig(t, "")
	if got := loadFromStore(t, store).DelugeWebURL; got != delugeEnvURL {
		t.Errorf("after reload deluge_web_url = %q, want the environment value", got)
	}
}

func loadFromStore(t *testing.T, store *mockSettingsStore) Config {
	t.Helper()
	if err := LoadConfigFromDatabase(store); err != nil {
		t.Fatalf("LoadConfigFromDatabase: %v", err)
	}
	return Snapshot()
}

// The password is never returned to a client or written to a log. The env value
// reaches AppConfig (the Deluge client needs it), so the three outbound surfaces are
// checked: the masked config GET /config serves, the lock annotations, and the log
// output of a load.
func TestDelugeLock_PasswordIsNeverEchoedOrLogged(t *testing.T) {
	setAllDelugeEnv(t)
	logs := logtest.Capture(t, slog.LevelDebug)
	delugeInitConfig(t, "")

	c := loadBlob(t, `{"deluge_web_password":"`+delugeStoredPW+`"}`)
	if c.DelugeWebPassword != delugeEnvPW {
		t.Fatal("precondition: the environment password should have been applied")
	}
	// Exercise the reset path too, which re-applies the same overrides.
	ResetToDefaults()

	masked := NewUpdateService(newMockSettingsStore()).MaskSecrets(Snapshot())
	body, err := json.Marshal(masked)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), delugeEnvPW) || strings.Contains(string(body), delugeStoredPW) {
		t.Error("the masked config served by GET /config carries the Deluge password in cleartext")
	}
	if !strings.Contains(string(body), `"deluge_web_password":"`) || strings.Contains(string(body), `"deluge_web_password":""`) {
		t.Error("the masked password should be present (non-empty mask) so the UI knows one is set")
	}

	locks, _ := json.Marshal(SettingLocks())
	locked, _ := json.Marshal(EnvLockedSettings())
	for _, out := range []string{string(locks), string(locked)} {
		if strings.Contains(out, delugeEnvPW) {
			t.Errorf("lock annotation leaks the password value: %s", out)
		}
	}
	if got := SettingLocks()["deluge_web_password"]; got != "DELUGE_WEB_PASSWORD" {
		t.Errorf("SettingLocks()[deluge_web_password] = %q, want the variable NAME only", got)
	}

	if strings.Contains(logs.String(), delugeEnvPW) || strings.Contains(logs.String(), delugeStoredPW) {
		t.Error("the Deluge password appeared in log output")
	}
}

// An env-supplied password must not be frozen into the stored blob in plaintext by
// the first settings save (TODO DELUGE-PASSWORD-ENV-VS-BLOB): the lock re-applies it
// on every load, so the stored copy could only leak. With the lock absent, a UI-set
// password is still persisted as before.
func TestDelugeLock_SuppliedPasswordIsNotWrittenToTheBlob(t *testing.T) {
	setAllDelugeEnv(t)
	delugeInitConfig(t, "")
	store := newMockSettingsStore()
	if err := SaveConfigToDatabase(store); err != nil {
		t.Fatalf("save: %v", err)
	}
	blob, err := store.GetSetting("config_blob")
	if err != nil || blob == nil {
		t.Fatalf("config_blob not persisted: %v", err)
	}
	if strings.Contains(blob.Value, delugeEnvPW) {
		t.Error("the env-supplied Deluge password was persisted into the stored blob")
	}
	if !strings.Contains(blob.Value, delugeEnvURL) {
		t.Error("precondition: the non-secret locked values are still persisted")
	}
	if got := loadFromStore(t, store).DelugeWebPassword; got != delugeEnvPW {
		t.Error("the environment password must still apply after reloading that blob")
	}
}

func TestDelugeLock_UnlockedPasswordIsStillPersisted(t *testing.T) {
	clearDelugeEnv(t)
	delugeInitConfig(t, "")
	Mutate(func(c *Config) { c.DelugeWebPassword = delugeStoredPW })
	store := newMockSettingsStore()
	if err := SaveConfigToDatabase(store); err != nil {
		t.Fatalf("save: %v", err)
	}
	blob, _ := store.GetSetting("config_blob")
	if blob == nil || !strings.Contains(blob.Value, delugeStoredPW) {
		t.Error("with no lock a UI-set Deluge password must still be persisted")
	}
}

// Every key in the lock tables is a real Config JSON field, and every Deluge key is
// lockable from the config file (the second table must not drift from the first).
func TestDelugeLock_TablesAreConsistent(t *testing.T) {
	fields := configJSONFieldNames(t)
	for _, k := range delugeKeys {
		if _, ok := envLockedSettings[k]; !ok {
			t.Errorf("%s is missing from envLockedSettings", k)
		}
		if !configFileLockedSettings[k] {
			t.Errorf("%s is missing from configFileLockedSettings", k)
		}
		if !slices.Contains(fields, k) {
			t.Errorf("%s is not a json field on Config", k)
		}
	}
	for k := range configFileLockedSettings {
		if _, ok := envLockedSettings[k]; !ok {
			t.Errorf("configFileLockedSettings key %q has no entry in envLockedSettings", k)
		}
	}
}
