// file: internal/config/removed_keys.go
// version: 1.1.0
// guid: 4c3e37ec-db93-46dc-bc36-a98e5c169f53
// last-edited: 2026-09-12

package config

import (
	"log/slog"

	"github.com/spf13/viper"
)

// sqliteRemovedMessage explains the removal of the SQLite opt-in in terms an
// operator can act on: which knob is gone, why it is gone, and what to do.
const sqliteRemovedMessage = "the enable_sqlite setting (the --enable-sqlite3-i-know-the-risks flag) " +
	"was removed: SQLite is no longer selectable as the database backend and PebbleDB is the only backend"

// autoFetchRemovedMessage explains the removal of auto_fetch_metadata.
const autoFetchRemovedMessage = "the auto_fetch_metadata setting was removed: nothing ever read it. " +
	"Each auto-fetch has its own switch: organize's fetch_metadata_first, the iTunes import's fetch-metadata option, " +
	"and the per-book Fetch button"

// removedConfigKey is a setting that used to exist and no longer does.
type removedConfigKey struct {
	key     string
	message string
}

// removedConfigKeys lists retired settings. A slice rather than a map so the
// first match — and therefore the error text — is deterministic.
//
// Two paths consume it, with opposite policies on purpose:
//   - PUT /api/v1/config REJECTS (400) a payload carrying any of them, with the
//     key's own removal message. Dropping one silently would tell a client its
//     change applied. This includes viper/flag-only names that were never API
//     fields: since 2026-09-12 every key Config does not have is refused (see
//     unknownConfigKeys), and a removed key gets the more useful message.
//   - Startup (viper config file / environment, the config.yaml next to the
//     database, and legacy per-key settings rows) only WARNS. A stale key in a
//     file the operator wrote years ago must never stop the server starting.
var removedConfigKeys = []removedConfigKey{
	{key: "enable_sqlite", message: sqliteRemovedMessage},
	// The viper/env name of the removed --enable-sqlite3-i-know-the-risks flag
	// (env: ENABLE_SQLITE3_I_KNOW_THE_RISKS under viper.AutomaticEnv).
	{key: "enable_sqlite3_i_know_the_risks", message: sqliteRemovedMessage},
	// Removed 2026-09-12. Listed so a config file, the environment or a legacy
	// settings row still carrying it gets the removed-setting warning instead of
	// being ignored in silence. The web settings page no longer sends it.
	{key: "auto_fetch_metadata", message: autoFetchRemovedMessage},
}

// removedKeyInUpdate returns the rejection text for the first removed key
// present in a config-update payload, or ok=false when there is none.
func removedKeyInUpdate(payload map[string]any) (string, bool) {
	for _, rk := range removedConfigKeys {
		if _, present := payload[rk.key]; present {
			return rk.key + " cannot be set: " + rk.message + "; remove " + rk.key + " from the request", true
		}
	}
	return "", false
}

// removedConfigKeyMessage reports whether key is a retired setting and, if so,
// the explanation to log.
func removedConfigKeyMessage(key string) (string, bool) {
	for _, rk := range removedConfigKeys {
		if rk.key == key {
			return rk.message, true
		}
	}
	return "", false
}

// warnRemovedKey logs the standard startup warning for a retired key found in
// source. It never returns an error: startup must not break over a stale key.
func warnRemovedKey(key, source string) {
	msg, ok := removedConfigKeyMessage(key)
	if !ok {
		return
	}
	slog.Warn("config: removed setting is ignored; delete it", "key", key, "source", source, "reason", msg)
}

// warnRemovedViperKeys warns about every retired key viper can see — from the
// config file read by cmd's initConfig or from the environment via
// AutomaticEnv. viper itself ignores unknown keys silently, so without this an
// operator who still sets the removed flag's key in a file gets no signal.
func warnRemovedViperKeys() {
	for _, rk := range removedConfigKeys {
		if viper.IsSet(rk.key) {
			warnRemovedKey(rk.key, "config file or environment")
		}
	}
}
