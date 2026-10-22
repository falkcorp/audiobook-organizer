// file: internal/config/removed_keys.go
// version: 1.10.0
// guid: 4c3e37ec-db93-46dc-bc36-a98e5c169f53
// last-edited: 2026-10-21

package config

import (
	"log/slog"
	"strings"

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

// writeBackupBeforeRemovedMessage explains the removal of the second backup switch.
const writeBackupBeforeRemovedMessage = "the metadata_scoring.write_backup_before setting (formerly " +
	"write_backup_before_tag_write) was removed: create_backups is the one switch for keeping a .bak-* " +
	"copy of a file before a tag write"

// downloadClientRemovedMessage explains the removal of the qBittorrent and
// SABnzbd client settings. Only the Deluge settings under download_client.torrent
// are still read.
const downloadClientRemovedMessage = "the qBittorrent and SABnzbd download-client settings were removed: " +
	"nothing ever read them. Only the Deluge settings under download_client.torrent are still used; " +
	"delete this block, including any stored qBittorrent password or SABnzbd API key"

// unreadSettingsRemovedMessage explains the removal of the eleven settings that
// were loaded, saved and shown on the Settings page but never read by any code.
const unreadSettingsRemovedMessage = "this setting was removed: it was stored and shown on the Settings page " +
	"but nothing ever read it, so changing it had no effect"

// reviewServerQueryRemovedMessage explains the removal of the flag that chose
// between the Review > Metadata lane's two ways of loading its queue.
const reviewServerQueryRemovedMessage = "the review_metadata_server_query setting was removed: the Review > " +
	"Metadata lane always asks the server for one filtered page (view=page) and the full-index mode it " +
	"switched away from is gone, so there is nothing left to switch"

// activityDBRemovedMessage explains the removal of the SQLite activity
// database's location settings.
const activityDBRemovedMessage = "this setting was removed with the SQLite activity backend: Pebble, inside the " +
	"main database, is the only activity store, so there is no separate activity database to place or move. " +
	"An existing activity.sqlite file is left where it is and can be deleted by hand"

// guestServiceTokenRemovedMessage explains the removal of the shared guest
// Cloudflare Access service token that invitations used to hand out.
const guestServiceTokenRemovedMessage = "the shared guest Cloudflare service token (INVITE_CF_ACCESS_CLIENT_ID and " +
	"INVITE_CF_ACCESS_CLIENT_SECRET) was removed: guests now get through Cloudflare by email login, because " +
	"accepting an invitation adds the invitee's email to the guest Access policy (CF_ACCOUNT_ID, " +
	"CF_GUESTS_POLICY_ID, CF_POLICY_API_TOKEN). Delete both variables, and revoke the service token in Cloudflare"

// removedConfigKey is a setting that used to exist and no longer does.
type removedConfigKey struct {
	key     string
	message string
	// fileOnly marks a key whose bare name is also a common environment
	// variable (LANGUAGE, the POSIX locale variable), so the viper scan only
	// warns when the key is in a config file (viper.InConfig) and never for the
	// environment: an operator's locale would otherwise draw a bogus warning on
	// every start. It is still scrubbed on save and refused in an update.
	//
	// Only language is exempt on purpose. The other environment names
	// (LOG_FORMAT, CACHE_SIZE, ...) are not set by shells or distros, so a
	// removed-setting warning for one of them means a stale deploy file, which
	// is exactly what the warning is for.
	fileOnly bool
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
	// Removed 2026-10-10 (01-P79b). The flat legacy row/blob name and the
	// nested viper/env name.
	{key: "write_backup_before_tag_write", message: writeBackupBeforeRemovedMessage},
	{key: "metadata_scoring.write_backup_before", message: writeBackupBeforeRemovedMessage},
	// Removed 2026-10-17 (01-P76). Whole blocks, so the nested credentials
	// (qBittorrent password, SABnzbd API key) are scrubbed from config.yaml on save.
	{key: "download_client.torrent.qbittorrent", message: downloadClientRemovedMessage},
	{key: "download_client.usenet", message: downloadClientRemovedMessage},
	// Removed 2026-10-17 (01-P80): loaded and saved but never read. api_keys was
	// an empty struct. language is the TOP-LEVEL key only; catalog.language is a
	// different, live setting and is not matched by a literal top-level lookup.
	{key: "metadata_review_default_view", message: unreadSettingsRemovedMessage},
	{key: "default_user_quota_gb", message: unreadSettingsRemovedMessage},
	{key: "memory_limit_type", message: unreadSettingsRemovedMessage},
	{key: "cache_size", message: unreadSettingsRemovedMessage},
	{key: "memory_limit_percent", message: unreadSettingsRemovedMessage},
	{key: "memory_limit_mb", message: unreadSettingsRemovedMessage},
	{key: "log_format", message: unreadSettingsRemovedMessage},
	{key: "enable_json_logging", message: unreadSettingsRemovedMessage},
	{key: "auth_rate_limit_per_minute", message: unreadSettingsRemovedMessage + "; sign-in attempts are limited by fixed per-IP and per-account throttles, not a setting"},
	{key: "api_keys", message: unreadSettingsRemovedMessage},
	{key: "language", message: unreadSettingsRemovedMessage, fileOnly: true},
	// Removed 2026-10-19 (02-PR19). It was ON in production from 2026-10-18.
	{key: "review_metadata_server_query", message: reviewServerQueryRemovedMessage},
	// Removed 2026-10-19 (01-P75) with the SQLite activity backend. The
	// environment names (ACTIVITY_DB_PATH, ACTIVITY_DB_MOVE_ON_CHANGE) are no
	// longer explicitly bound, but cmd's initConfig calls viper.AutomaticEnv(),
	// so a stale ACTIVITY_DB_PATH in the environment still reaches viper and
	// warnRemovedViperKeys warns about it. A config file, a legacy per-key
	// settings row and an update payload can carry them too; a stored blob
	// drops them silently. activity_backend itself is still a setting: it is
	// validated, and "sqlite" stops startup (config.ValidateActivityBackend).
	{key: "activity_db_path", message: activityDBRemovedMessage},
	{key: "activity_db_move_on_change", message: activityDBRemovedMessage},
	// Removed 2026-10-21 (public-surface hardening). The pair was environment
	// only; both names warn at startup and a PUT naming one is refused with the
	// removal message.
	{key: "invite_cf_access_client_id", message: guestServiceTokenRemovedMessage},
	{key: "invite_cf_access_client_secret", message: guestServiceTokenRemovedMessage},
}

// removedKeyInUpdate returns the rejection text for the first removed key
// present in a config-update payload, or ok=false when there is none. A
// dotted key matches either the literal top-level key or the nested path, so
// {"metadata_scoring":{"write_backup_before":true}} (what a stale Settings
// page or an old settings export sends) gets the removed-setting message
// rather than unknownConfigKeys' generic one.
func removedKeyInUpdate(payload map[string]any) (string, bool) {
	for _, rk := range removedConfigKeys {
		if hasConfigKey(payload, rk.key) {
			return rk.key + " cannot be set: " + rk.message + "; remove " + rk.key + " from the request", true
		}
	}
	return "", false
}

// hasConfigKey reports whether m carries key, either literally at the top
// level or, for a dotted key, as the nested path of objects it names.
func hasConfigKey(m map[string]any, key string) bool {
	if _, ok := m[key]; ok {
		return true
	}
	parts := strings.Split(key, ".")
	if len(parts) < 2 {
		return false
	}
	cur := m
	for i, part := range parts {
		v, ok := cur[part]
		if !ok {
			return false
		}
		if i == len(parts)-1 {
			return true
		}
		next, ok := v.(map[string]any)
		if !ok {
			return false
		}
		cur = next
	}
	return false
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
		if rk.fileOnly {
			if viper.InConfig(rk.key) {
				warnRemovedKey(rk.key, "config file")
			}
			continue
		}
		if viper.IsSet(rk.key) {
			warnRemovedKey(rk.key, "config file or environment")
		}
	}
}
