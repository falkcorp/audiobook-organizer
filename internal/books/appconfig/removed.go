// file: internal/books/appconfig/removed.go
// version: 1.0.0
// guid: 6b0e3d52-9a47-4c18-8e65-2f7d1a4c9b30
// last-edited: 2026-11-20

package appconfig

import (
	"fmt"
	"slices"
	"strings"
)

// A key that HEAD removed is refused with the reason, not with core's bare
// "unknown key": HEAD answered a PUT naming one with its own removal message,
// because dropping it silently would tell a client its change applied, and a
// stale Settings page or an old settings export still sends them. The names and
// texts are HEAD's removedConfigKeys, kept here because the importer's frozen copy
// of the table (internal/books/cutover/head_removed_keys.go) is deleted in R3 and
// this refusal is not.

const removedSqliteRemovedMessage = "the enable_sqlite setting (the --enable-sqlite3-i-know-the-risks flag) " +
	"was removed: SQLite is no longer selectable as the database backend and PebbleDB is the only backend"

const removedAutoFetchRemovedMessage = "the auto_fetch_metadata setting was removed: nothing ever read it. " +
	"Each auto-fetch has its own switch: organize's fetch_metadata_first, the iTunes import's fetch-metadata option, " +
	"and the per-book Fetch button"

const removedWriteBackupBeforeRemovedMessage = "the metadata_scoring.write_backup_before setting (formerly " +
	"write_backup_before_tag_write) was removed: create_backups is the one switch for keeping a .bak-* " +
	"copy of a file before a tag write"

const removedDownloadClientRemovedMessage = "the qBittorrent and SABnzbd download-client settings were removed: " +
	"nothing ever read them. Only the Deluge settings under download_client.torrent are still used; " +
	"delete this block, including any stored qBittorrent password or SABnzbd API key"

const removedUnreadSettingsRemovedMessage = "this setting was removed: it was stored and shown on the Settings page " +
	"but nothing ever read it, so changing it had no effect"

const removedReviewServerQueryRemovedMessage = "the review_metadata_server_query setting was removed: the Review > " +
	"Metadata lane always asks the server for one filtered page (view=page) and the full-index mode it " +
	"switched away from is gone, so there is nothing left to switch"

const removedActivityDBRemovedMessage = "this setting was removed with the SQLite activity backend: Pebble, inside the " +
	"main database, is the only activity store, so there is no separate activity database to place or move. " +
	"An existing activity.sqlite file is left where it is and can be deleted by hand"

const removedGuestServiceTokenRemovedMessage = "the shared guest Cloudflare service token (INVITE_CF_ACCESS_CLIENT_ID and " +
	"INVITE_CF_ACCESS_CLIENT_SECRET) was removed: guests now get through Cloudflare by email login, because " +
	"accepting an invitation adds the invitee's email to the guest Access policy (CF_ACCOUNT_ID, " +
	"CF_GUESTS_POLICY_ID, CF_POLICY_API_TOKEN). Delete both variables, and revoke the service token in Cloudflare"

// removedKeys are the retired settings, in the order a refusal names them.
var removedKeys = []struct{ key, reason string }{
	{"enable_sqlite", removedSqliteRemovedMessage},
	{"enable_sqlite3_i_know_the_risks", removedSqliteRemovedMessage},
	{"auto_fetch_metadata", removedAutoFetchRemovedMessage},
	{"write_backup_before_tag_write", removedWriteBackupBeforeRemovedMessage},
	{"metadata_scoring.write_backup_before", removedWriteBackupBeforeRemovedMessage},
	{"download_client.torrent.qbittorrent", removedDownloadClientRemovedMessage},
	{"download_client.usenet", removedDownloadClientRemovedMessage},
	{"metadata_review_default_view", removedUnreadSettingsRemovedMessage},
	{"default_user_quota_gb", removedUnreadSettingsRemovedMessage},
	{"memory_limit_type", removedUnreadSettingsRemovedMessage},
	{"cache_size", removedUnreadSettingsRemovedMessage},
	{"memory_limit_percent", removedUnreadSettingsRemovedMessage},
	{"memory_limit_mb", removedUnreadSettingsRemovedMessage},
	{"log_format", removedUnreadSettingsRemovedMessage},
	{"enable_json_logging", removedUnreadSettingsRemovedMessage},
	{"auth_rate_limit_per_minute", removedUnreadSettingsRemovedMessage + "; sign-in attempts are limited by fixed per-IP and per-account throttles, not a setting"},
	{"api_keys", removedUnreadSettingsRemovedMessage},
	{"language", removedUnreadSettingsRemovedMessage},
	{"review_metadata_server_query", removedReviewServerQueryRemovedMessage},
	{"activity_db_path", removedActivityDBRemovedMessage},
	{"activity_db_move_on_change", removedActivityDBRemovedMessage},
	{"invite_cf_access_client_id", removedGuestServiceTokenRemovedMessage},
	{"invite_cf_access_client_secret", removedGuestServiceTokenRemovedMessage},
}

// removedKeyReason is the removal text for head, or false when head was never a
// removed key. A nested name HEAD removed ("metadata_scoring.write_backup_before")
// is matched whole; so is a block ("download_client.usenet") and anything under it.
func removedKeyReason(head string) (string, bool) {
	for _, r := range removedKeys {
		if head == r.key || strings.HasPrefix(head, r.key+".") {
			return r.reason, true
		}
	}
	return "", false
}

// unknownKeysMessage lists the keys no row covers, each removed one with why it
// is gone.
func unknownKeysMessage(unknown []string) string {
	parts := make([]string, 0, len(unknown))
	for _, k := range slices.Sorted(slices.Values(unknown)) {
		if why, ok := removedKeyReason(k); ok {
			parts = append(parts, fmt.Sprintf("%s cannot be set: %s; remove %s from the request", k, why, k))
			continue
		}
		parts = append(parts, k)
	}
	return strings.Join(parts, ", ")
}
