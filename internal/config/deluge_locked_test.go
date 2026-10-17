// file: internal/config/deluge_locked_test.go
// version: 1.5.0
// guid: 8e2c4a17-5b9d-4f63-a1d8-0c7e3f95b264
// last-edited: 2026-10-17

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
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/viper"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
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
// optionally with a config file whose YAML is fileYAML, written to a temp file and
// read with ReadInConfig so viper.ConfigFileUsed names it. It returns that path ("" when
// there is no file).
func delugeInitConfig(t *testing.T, fileYAML string) string {
	t.Helper()
	restoreAppConfig(t)
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.AutomaticEnv()
	path := ""
	if fileYAML != "" {
		path = filepath.Join(t.TempDir(), "audiobook-organizer.yaml")
		if err := os.WriteFile(path, []byte(fileYAML), 0o600); err != nil {
			t.Fatalf("write config file fixture: %v", err)
		}
		viper.SetConfigFile(path)
		if err := viper.ReadInConfig(); err != nil {
			t.Fatalf("read config file fixture: %v", err)
		}
	}
	InitConfig()
	return path
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
	path := delugeInitConfig(t, "deluge_web_url: "+delugeFileURL+"\ndeluge_discovery_label: file-label\n"+
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
		// The reason names the file viper loaded, so the operator edits the right one.
		if got, want := SettingLocks()[k], LockedByConfigFile+" "+path; got != want {
			t.Errorf("SettingLocks()[%s] = %q, want %q", k, got, want)
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

// savedBlob returns the config_blob text a save left in the store.
func delugeSavedBlob(t *testing.T, store *mockSettingsStore) string {
	t.Helper()
	row, err := store.GetSetting("config_blob")
	if err != nil || row == nil {
		t.Fatalf("config_blob not persisted: %v", err)
	}
	return row.Value
}

// With no stored blob, a supplied password is not frozen into the first one.
func TestDelugeLock_SuppliedPasswordIsNotWrittenToTheBlob(t *testing.T) {
	setAllDelugeEnv(t)
	delugeInitConfig(t, "")
	store := newMockSettingsStore()
	if err := SaveConfigToDatabase(store); err != nil {
		t.Fatalf("save: %v", err)
	}
	blob := delugeSavedBlob(t, store)
	if strings.Contains(blob, delugeEnvPW) {
		t.Error("the env-supplied Deluge password was persisted into the stored blob")
	}
	if !strings.Contains(blob, delugeEnvURL) {
		t.Error("precondition: the non-secret locked values are still persisted")
	}
	if got := loadFromStore(t, store).DelugeWebPassword; got != delugeEnvPW {
		t.Error("the environment password must still apply after reloading that blob")
	}
}

// The operator's own stored password survives a save made while the environment
// supplies another, and is effective again once the environment entry is removed.
// Writing "" here instead lost it from every layer, and the Deluge client then fell
// back to its default password.
func TestDelugeLock_StoredPasswordSurvivesASaveUnderAnEnvLock(t *testing.T) {
	const uiPW = "ui-pass-9120"
	clearDelugeEnv(t)
	t.Setenv("DELUGE_WEB_PASSWORD", delugeEnvPW)
	delugeInitConfig(t, "")
	store := storeWithBlob(t, `{"deluge_web_password":"`+uiPW+`"}`)

	if got := loadFromStore(t, store).DelugeWebPassword; got != delugeEnvPW {
		t.Fatalf("precondition: the environment password should be live, got a different one")
	}
	if err := SaveConfigToDatabase(store); err != nil {
		t.Fatalf("save: %v", err)
	}
	blob := delugeSavedBlob(t, store)
	if strings.Contains(blob, delugeEnvPW) {
		t.Error("the env-supplied password was frozen into the stored blob")
	}
	if !strings.Contains(blob, `"deluge_web_password":"`+uiPW+`"`) {
		t.Errorf("the stored UI password was not carried forward: %s", blob)
	}

	// The environment entry is removed and the process restarts.
	t.Setenv("DELUGE_WEB_PASSWORD", "")
	delugeInitConfig(t, "")
	if got := loadFromStore(t, store).DelugeWebPassword; got != uiPW {
		t.Errorf("after the env entry is removed the stored password is not effective (got %q)", got)
	}
}

// Same, for a password the config file supplies instead of the environment. The
// file value is applied and never lands in the blob; the stored one is kept.
func TestDelugeLock_ConfigFilePasswordIsAppliedAndNeverSaved(t *testing.T) {
	const (
		uiPW   = "ui-pass-4417"
		filePW = "file-pass-6603"
	)
	clearDelugeEnv(t)
	delugeInitConfig(t, "deluge_web_password: "+filePW+"\n")
	store := storeWithBlob(t, `{"deluge_web_password":"`+uiPW+`"}`)

	if got := loadFromStore(t, store).DelugeWebPassword; got != filePW {
		t.Fatalf("the config file password was not applied over the stored one")
	}
	if got := SettingLockedBy("deluge_web_password"); !strings.HasPrefix(got, LockedByConfigFile) {
		t.Fatalf("SettingLockedBy = %q, want a config file lock", got)
	}
	if err := SaveConfigToDatabase(store); err != nil {
		t.Fatalf("save: %v", err)
	}
	blob := delugeSavedBlob(t, store)
	if strings.Contains(blob, filePW) {
		t.Error("the config-file password was written into the stored blob")
	}
	if !strings.Contains(blob, `"deluge_web_password":"`+uiPW+`"`) {
		t.Errorf("the stored UI password was not carried forward: %s", blob)
	}
}

// PUT of a new password under a lock: accepted and live in memory until the next
// load, but the stored value is not replaced.
func TestDelugeLock_PutOfALockedPasswordKeepsTheStoredOne(t *testing.T) {
	const (
		uiPW  = "ui-pass-2288"
		putPW = "put-pass-3951"
	)
	clearDelugeEnv(t)
	t.Setenv("DELUGE_WEB_PASSWORD", delugeEnvPW)
	delugeInitConfig(t, "")
	store := storeWithBlob(t, `{"deluge_web_password":"`+uiPW+`"}`)
	loadFromStore(t, store)

	ctx := auth.WithMethod(context.Background(), auth.MethodSession)
	if status, resp := NewUpdateService(store).UpdateConfig(ctx, map[string]any{"deluge_web_password": putPW}); status != http.StatusOK {
		t.Fatalf("PUT status %d (%v)", status, resp["error"])
	}
	blob := delugeSavedBlob(t, store)
	if strings.Contains(blob, putPW) || strings.Contains(blob, delugeEnvPW) {
		t.Errorf("a locked password was persisted by the PUT: %s", blob)
	}
	if !strings.Contains(blob, `"deluge_web_password":"`+uiPW+`"`) {
		t.Errorf("the previously stored password was not kept: %s", blob)
	}
}

// A read failure that is not "not found" must fail the save rather than carry an
// empty password forward over the stored one.
func TestDelugeLock_SaveFailsRatherThanBlankingOnAReadError(t *testing.T) {
	setAllDelugeEnv(t)
	delugeInitConfig(t, "")
	store := storeWithBlob(t, `{"deluge_web_password":"ui-pass-0001"}`)
	store.getErr = errors.New("backend unavailable")
	if err := SaveConfigToDatabase(store); err == nil {
		t.Fatal("save succeeded although the stored password could not be read")
	}
	store.getErr = nil
	if row, _ := store.GetSetting("config_blob"); row == nil || !strings.Contains(row.Value, "ui-pass-0001") {
		t.Error("the stored blob changed despite the failed save")
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
	if !strings.Contains(delugeSavedBlob(t, store), delugeStoredPW) {
		t.Error("with no lock a UI-set Deluge password must still be persisted")
	}
}

// DELUGE_DISCOVERY_ENABLED=false is a real value: it forces the toggle off over a
// stored true. (Guards against treating the lock as "apply only when true".)
func TestDelugeLock_EnvFalseBeatsStoredTrue(t *testing.T) {
	clearDelugeEnv(t)
	t.Setenv("DELUGE_DISCOVERY_ENABLED", "false")
	t.Setenv("DELUGE_MOVE_ENABLED", "false")
	delugeInitConfig(t, "")

	c := loadBlob(t, `{"deluge_discovery_enabled":true,"deluge_move_enabled":true}`)
	if c.DelugeDiscoveryEnabled || c.DelugeMoveEnabled {
		t.Errorf("env false did not beat stored true: discovery=%v move=%v", c.DelugeDiscoveryEnabled, c.DelugeMoveEnabled)
	}
}

// A key that can be locked by the environment, but is not a Deluge key, must not be
// locked by merely appearing in the config file: the file is a lock source for the
// Deluge keys only.
func TestDelugeLock_ConfigFileDoesNotLockNonDelugeKeys(t *testing.T) {
	clearDelugeEnv(t)
	t.Setenv("ACTIVITY_DB_PATH", "")
	t.Setenv("ACTIVITY_DB_MOVE_ON_CHANGE", "")
	t.Setenv("ACTIVITY_BACKEND", "")
	t.Setenv("DATABASE_PATH", "")
	delugeInitConfig(t, "activity_db_path: /srv/activity-file.sqlite\nactivity_db_move_on_change: true\n"+
		"activity_backend: sqlite\ndatabase_path: /srv/db-file\n")

	for _, k := range []string{"activity_db_path", "activity_db_move_on_change", "activity_backend", "database_path"} {
		if slices.Contains(EnvLockedSettings(), k) {
			t.Errorf("%s was reported locked by the config file", k)
		}
		if got := SettingLockedBy(k); got != "" {
			t.Errorf("SettingLockedBy(%s) = %q, want \"\"", k, got)
		}
	}
}

// A boolean that does not parse is forced to false -- say so, naming the source.
func TestDelugeLock_UnparseableBooleanIsWarnedAbout(t *testing.T) {
	clearDelugeEnv(t)
	t.Setenv("DELUGE_MOVE_ENABLED", "yes-please")
	logs := logtest.Capture(t, slog.LevelWarn)
	delugeInitConfig(t, "")

	c := loadBlob(t, `{"deluge_move_enabled":true}`)
	if c.DelugeMoveEnabled {
		t.Error("an unparseable boolean should resolve to false")
	}
	out := logs.String()
	if !strings.Contains(out, "deluge_move_enabled") || !strings.Contains(out, "DELUGE_MOVE_ENABLED") {
		t.Errorf("no warning naming the key and its source; log output: %q", out)
	}
}

// SaveConfigToFile rewrites <database dir>/config.yaml. Keys it does not manage
// must survive, because an operator can point --config at that file and the Deluge
// keys in it are what lock those settings; a secret that is now empty is removed.
func TestSaveConfigToFile_KeepsKeysItDoesNotManage(t *testing.T) {
	clearDelugeEnv(t)
	delugeInitConfig(t, "")
	dir := t.TempDir()
	Mutate(func(c *Config) {
		c.DatabasePath = filepath.Join(dir, "db.pebble")
		c.RootDir = "/srv/library"
		c.OpenAIAPIKey = ""
	})
	path := ConfigFilePath()
	prior := "deluge_web_url: " + delugeFileURL + "\ndeluge_move_enabled: false\nopenai_api_key: old-key\nroot_dir: /old\n"
	if err := os.WriteFile(path, []byte(prior), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SaveConfigToFile(); err != nil {
		t.Fatalf("SaveConfigToFile: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := string(data)
	if !strings.Contains(out, "deluge_web_url: "+delugeFileURL) || !strings.Contains(out, "deluge_move_enabled: false") {
		t.Errorf("unmanaged keys were dropped by the rewrite:\n%s", out)
	}
	if !strings.Contains(out, "root_dir: /srv/library") {
		t.Errorf("a managed key was not updated:\n%s", out)
	}
	if strings.Contains(out, "old-key") {
		t.Errorf("a secret that is now empty was left behind:\n%s", out)
	}
}

// saveFileFixture points the config at a temp database dir, writes prior to the
// <db dir>/config.yaml that SaveConfigToFile rewrites, and returns its path.
func saveFileFixture(t *testing.T, prior string) string {
	t.Helper()
	clearDelugeEnv(t)
	delugeInitConfig(t, "")
	dir := t.TempDir()
	Mutate(func(c *Config) {
		c.DatabasePath = filepath.Join(dir, "db.pebble")
		c.RootDir = "/srv/library"
		c.OpenAIAPIKey = ""
	})
	path := ConfigFilePath()
	if err := os.WriteFile(path, []byte(prior), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A file the merge cannot parse is left exactly as it is, and the failure names it.
// Starting from an empty map and writing would have dropped the operator's Deluge
// lock and password, silently.
func TestSaveConfigToFile_CorruptFileIsNeverOverwritten(t *testing.T) {
	corrupt := "deluge_web_password: pw-keep-me\nroot_dir: [unterminated\n  bad: : line\n"
	path := saveFileFixture(t, corrupt)

	err := SaveConfigToFile()
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("SaveConfigToFile = %v, want an error naming %s", err, path)
	}
	got, rerr := os.ReadFile(path)
	if rerr != nil || string(got) != corrupt {
		t.Errorf("the unparseable file was modified:\n%s", got)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

// The database save is not lost when the file step declines, and the warning
// names the file.
func TestSaveConfigToDatabase_StillSavesWhenTheFileIsCorrupt(t *testing.T) {
	logs := logtest.Capture(t, slog.LevelWarn)
	path := saveFileFixture(t, "root_dir: [unterminated\n")
	store := newMockSettingsStore()

	if err := SaveConfigToDatabase(store); err != nil {
		t.Fatalf("SaveConfigToDatabase: %v", err)
	}
	if !strings.Contains(delugeSavedBlob(t, store), "/srv/library") {
		t.Error("the database blob was not saved")
	}
	if !strings.Contains(logs.String(), path) {
		t.Errorf("no warning naming %s; log output: %q", path, logs.String())
	}
}

// The write goes through a temp file and a rename: no temp file is left, an
// existing file's mode is kept, a new file is 0600, and a symlink survives.
func TestSaveConfigToFile_WritesAtomicallyAndKeepsMode(t *testing.T) {
	path := saveFileFixture(t, "deluge_web_url: "+delugeFileURL+"\n")
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(path)
	if err := SaveConfigToFile(); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v, want the existing 0640 kept", after.Mode().Perm())
	}
	if os.SameFile(before, after) {
		t.Error("the file was rewritten in place; want a temp file renamed over it")
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}

	// A new file is private.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := SaveConfigToFile(); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("new file mode = %v, want 0600", info.Mode().Perm())
	}

	// A symlinked config file stays a symlink; its target is what changes.
	target := filepath.Join(t.TempDir(), "real.yaml")
	if err := os.WriteFile(target, []byte("deluge_move_enabled: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := SaveConfigToFile(); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Lstat(path); info.Mode()&os.ModeSymlink == 0 {
		t.Error("the symlink was replaced by a regular file")
	}
	if data, _ := os.ReadFile(target); !strings.Contains(string(data), "deluge_move_enabled: false") ||
		!strings.Contains(string(data), "root_dir: /srv/library") {
		t.Errorf("the symlink target was not merged and updated:\n%s", data)
	}
}

// When neither the file nor its directory is writable, the save fails and the
// original is untouched.
func TestSaveConfigToFile_FailureLeavesTheOriginalIntact(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("file permissions do not bind root")
	}
	prior := "deluge_web_url: " + delugeFileURL + "\nroot_dir: /old\n"
	path := saveFileFixture(t, prior)
	dir := filepath.Dir(path)
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if err := SaveConfigToFile(); err == nil {
		t.Fatal("expected an error when neither the file nor its directory is writable")
	}
	if got, _ := os.ReadFile(path); string(got) != prior {
		t.Errorf("the original was modified by a failed save:\n%s", got)
	}
}

// A config file the process can write but whose directory it cannot (a symlink into
// a root-owned directory, say) used to save fine. The temp-file-and-rename path needs
// the directory, so it falls back to writing the file in place, keeps the inode and
// mode, and warns that the write was not atomic.
func TestSaveConfigToFile_WritesInPlaceWhenTheDirectoryIsReadOnly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("directory permissions do not bind root")
	}
	logs := logtest.Capture(t, slog.LevelWarn)
	path := saveFileFixture(t, "deluge_web_url: "+delugeFileURL+"\nroot_dir: /old\n")
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(path)
	dir := filepath.Dir(path)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if err := SaveConfigToFile(); err != nil {
		t.Fatalf("SaveConfigToFile with a read-only directory: %v", err)
	}
	data, _ := os.ReadFile(path)
	out := string(data)
	if !strings.Contains(out, "root_dir: /srv/library") || !strings.Contains(out, "deluge_web_url: "+delugeFileURL) {
		t.Errorf("the in-place write did not merge and update:\n%s", out)
	}
	after, _ := os.Stat(path)
	if !os.SameFile(before, after) || after.Mode().Perm() != 0o640 {
		t.Errorf("in-place write must keep the inode and mode (same file=%v, mode=%v)",
			os.SameFile(before, after), after.Mode().Perm())
	}
	if !strings.Contains(logs.String(), path) || !strings.Contains(logs.String(), "not atomic") {
		t.Errorf("no warning naming the file and the non-atomic write; log output: %q", logs.String())
	}
}

// A symlink whose target does not exist is not replaced by a regular file: the save
// fails, names the path, and leaves the link as it was.
func TestSaveConfigToFile_DanglingSymlinkIsNotReplaced(t *testing.T) {
	path := saveFileFixture(t, "")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "missing", "real.yaml")
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	err := SaveConfigToFile()
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("SaveConfigToFile = %v, want an error naming %s", err, path)
	}
	if info, lerr := os.Lstat(path); lerr != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the dangling symlink was replaced (err=%v)", lerr)
	}
	if _, serr := os.Stat(target); serr == nil {
		t.Error("the symlink target was created")
	}
}

// A retired key does not survive a save (a save used to drop it; the merge must
// too, or LoadConfigFromFile warns about it on every boot), including a nested one.
func TestSaveConfigToFile_DropsRetiredKeys(t *testing.T) {
	path := saveFileFixture(t, "auto_fetch_metadata: true\nAuto_Fetch_Metadata: true\n"+
		"metadata_scoring:\n  write_backup_before: true\n  keep_me: 7\nenable_sqlite:\n  nested: 1\n"+
		"deluge_web_url: "+delugeFileURL+"\n")
	if err := SaveConfigToFile(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	out := string(data)
	for _, gone := range []string{"auto_fetch_metadata", "Auto_Fetch_Metadata", "write_backup_before", "enable_sqlite"} {
		if strings.Contains(out, gone) {
			t.Errorf("retired key %q survived the save:\n%s", gone, out)
		}
	}
	if !strings.Contains(out, "keep_me: 7") || !strings.Contains(out, "deluge_web_url: "+delugeFileURL) {
		t.Errorf("an unrelated key was lost:\n%s", out)
	}
}

// Keys fold case-insensitively: an operator's ROOT_DIR must not survive beside the
// managed root_dir, and an empty secret is removed whatever its case.
func TestSaveConfigToFile_FoldsKeyCase(t *testing.T) {
	path := saveFileFixture(t, "ROOT_DIR: /old\nOpenAI_API_Key: stale-key\nDELUGE_WEB_URL: "+delugeFileURL+"\n")
	if err := SaveConfigToFile(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	out := string(data)
	if strings.Contains(out, "ROOT_DIR") || strings.Contains(out, "/old") {
		t.Errorf("a differently-cased managed key survived:\n%s", out)
	}
	if !strings.Contains(out, "root_dir: /srv/library") {
		t.Errorf("the managed key is missing:\n%s", out)
	}
	if strings.Contains(out, "stale-key") {
		t.Errorf("an empty secret was left behind under another case:\n%s", out)
	}
	if !strings.Contains(out, "DELUGE_WEB_URL: "+delugeFileURL) {
		t.Errorf("an unmanaged key was changed:\n%s", out)
	}
}

// slowStore is a settings store safe for concurrent use whose config_blob write is
// slow, so a save that snapshotted earlier can land its blob after a newer one's.
type slowStore struct {
	mu    sync.Mutex
	inner *mockSettingsStore
}

func (s *slowStore) GetSetting(k string) (*database.Setting, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inner.GetSetting(k)
}

func (s *slowStore) SetSetting(k, v, typ string, secret bool) error {
	if k == "config_blob" {
		time.Sleep(time.Millisecond)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inner.SetSetting(k, v, typ, secret)
}

func (s *slowStore) GetAllSettings() ([]database.Setting, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inner.GetAllSettings()
}

func (s *slowStore) DeleteSetting(k string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inner.DeleteSetting(k)
}

// gatedStore holds the first config_blob write until the second one begins (or a
// timeout passes), so a stale first write lands last whenever nothing orders the two
// saves. With saves serialised the second save cannot begin until the first returns,
// so the first just waits out the timeout.
type gatedStore struct {
	mu          sync.Mutex
	inner       *mockSettingsStore
	blobWrites  atomic.Int32
	firstInside chan struct{}
	secondBegan chan struct{}
}

func (g *gatedStore) GetSetting(k string) (*database.Setting, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.inner.GetSetting(k)
}

func (g *gatedStore) SetSetting(k, v, typ string, secret bool) error {
	if k == "config_blob" {
		switch g.blobWrites.Add(1) {
		case 1:
			close(g.firstInside)
			select {
			case <-g.secondBegan:
			case <-time.After(150 * time.Millisecond):
			}
		case 2:
			close(g.secondBegan)
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.inner.SetSetting(k, v, typ, secret)
}

func (g *gatedStore) GetAllSettings() ([]database.Setting, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.inner.GetAllSettings()
}

func (g *gatedStore) DeleteSetting(k string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.inner.DeleteSetting(k)
}

// Deterministic: save 1 is parked inside its config_blob write; the config then
// changes and save 2 starts. If nothing serialises them, save 2 writes the newer
// blob first and save 1's older one lands last -- every run. With configBlobSaveMu,
// save 2 waits for save 1 and the newer blob is the last one written.
func TestSaveConfigToDatabase_SecondSaveNeverLosesToTheFirst(t *testing.T) {
	clearDelugeEnv(t)
	delugeInitConfig(t, "")
	Mutate(func(c *Config) { c.DatabasePath = ""; c.RootDir = "/lib/first" })
	store := &gatedStore{
		inner:       newMockSettingsStore(),
		firstInside: make(chan struct{}),
		secondBegan: make(chan struct{}),
	}

	var wg sync.WaitGroup
	save := func(name string) {
		defer wg.Done()
		if err := SaveConfigToDatabase(store); err != nil {
			t.Errorf("%s save: %v", name, err)
		}
	}
	wg.Add(1)
	go save("first")
	<-store.firstInside

	Mutate(func(c *Config) { c.RootDir = "/lib/second" })
	wg.Add(1)
	go save("second")
	wg.Wait()

	row, err := store.GetSetting("config_blob")
	if err != nil || row == nil {
		t.Fatalf("no blob: %v", err)
	}
	if !strings.Contains(row.Value, `"root_dir":"/lib/second"`) {
		t.Errorf("the older save's blob landed last: %s", row.Value[:min(len(row.Value), 200)])
	}
}

// Concurrent saves of different values: the blob left behind must be the final
// in-memory config, never an older snapshot that landed last. Run under -race with
// a high -count; it fails if configBlobSaveMu is removed or the snapshot is taken
// before taking it.
func TestSaveConfigToDatabase_ConcurrentSavesLeaveTheNewestBlob(t *testing.T) {
	clearDelugeEnv(t)
	t.Setenv("ACTIVITY_DB_PATH", "")
	delugeInitConfig(t, "")
	Mutate(func(c *Config) { c.DatabasePath = ""; c.RootDir = "" })
	store := &slowStore{inner: newMockSettingsStore()}

	const writers = 8
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			Mutate(func(c *Config) { c.RootDir = fmt.Sprintf("/lib/%d", i) })
			if err := SaveConfigToDatabase(store); err != nil {
				t.Errorf("save %d: %v", i, err)
			}
		}()
	}
	wg.Wait()

	want := Snapshot().RootDir
	row, err := store.GetSetting("config_blob")
	if err != nil || row == nil {
		t.Fatalf("no blob: %v", err)
	}
	var blob struct {
		RootDir string `json:"root_dir"`
	}
	if err := json.Unmarshal([]byte(row.Value), &blob); err != nil {
		t.Fatal(err)
	}
	if blob.RootDir != want {
		t.Errorf("stored root_dir = %q, but the final config is %q: an older save landed last", blob.RootDir, want)
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

// The removed qBittorrent and SABnzbd blocks are scrubbed from config.yaml on
// save, so a plaintext password or API key does not stay behind; the kept
// Deluge block survives.
func TestSaveConfigToFile_ScrubsRemovedDownloadClientBlocks(t *testing.T) {
	path := saveFileFixture(t, "download_client:\n  torrent:\n    deluge:\n      host: 192.0.2.10\n"+
		"    qbittorrent:\n      password: qbit-test-pass\n"+
		"  usenet:\n    sabnzbd:\n      api_key: sab-test-key\n"+
		"deluge_web_url: "+delugeFileURL+"\n")
	if err := SaveConfigToFile(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	out := string(data)
	for _, gone := range []string{"qbittorrent", "qbit-test-pass", "usenet", "sab-test-key"} {
		if strings.Contains(out, gone) {
			t.Errorf("removed download-client data %q survived the save:\n%s", gone, out)
		}
	}
	if !strings.Contains(out, "192.0.2.10") {
		t.Errorf("the kept Deluge settings were lost:\n%s", out)
	}
}
