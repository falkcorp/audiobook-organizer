<!-- file: docs/system/r3-release-note.md -->
<!-- version: 0.1.0 -->
<!-- guid: 71ac3f91-c326-400e-a86b-f4351dfc88a6 -->
<!-- last-edited: 2026-11-21 -->

> **DRAFT (R3-04a).** Not final. Two inputs do not exist yet, and this draft keeps them as placeholders so nothing
> is guessed:
>
> - `<R2 release>` is the literal R2 tag. It is known only once OWNER-05 cuts R2; the final text copies it from
>   OWNER-05's tracking-issue record (its `decision: kept (R2 release <tag>, cut from <rc tag>)` line), the same
>   source R3-02 writes into `UpgradeFloor.InstallFirst`. Never take it from the newest release.
> - The path table under "New page addresses" is 07 §7.11's list. F9-03 has not merged, so its full former-to-new
>   table is still to be compared with it.
>
> The closing PR (R3-04b) fills both, adds the executive summary and the TODO check-offs, and removes this banner.

# Release notes: R3 (cleanup and the upgrade floor)

## Before you upgrade

This release needs a database that ran <R2 release>. An older database stops the service at start with the message `install <R2 release> first`: install that release, start it once, then upgrade.

The first start of this release deletes the old settings key file `.encryption_key` and the old search index directory. Keep your off-host copy of `.encryption_key` with any backup taken before <R2 release>: restoring such a backup needs it, and needs <R2 release> installed first.

## If the service will not start

An older database makes the service exit with code 78. Its last log line is `install <R2 release> first`; under Docker, that line in `docker logs` is the message.

- **systemd:** the unit does not restart the service (`RestartPreventExitStatus=78`).
- **macOS (launchd):** launchd would restart the service at once, so the binary waits 60 seconds before it exits 78. Stop the job with `launchctl bootout gui/$(id -u)/<your job label>` before you install the R2 release.
- **Docker:** `restart: unless-stopped` restarts the container. Stop it with `docker compose stop` before you install the R2 release.

Then install the R2 release (<R2 release>), start it once so that it migrates the database, and upgrade again.

## Restoring an old backup

An archive taken before <R2 release> cannot be restored by this release: restoring refuses it before anything is extracted, so a fresh host never ends with an empty store. To restore one:

1. Install <R2 release>.
2. Put the archive's off-host `.encryption_key` and the gap-fill `config.yaml` into the state directory and beside the database.
3. Restore the archive with `storage restore-offline --archive`.
4. Start the service once, so that it migrates the database.
5. Upgrade to this release.

Without the off-host `.encryption_key`, <R2 release> refuses to migrate the restored store, because this release deleted the host's copy.

## Removed options

- The `--db-type` and `--enable-sqlite3-i-know-the-risks` command-line flags are removed (they had no effect since PebbleDB became the only backend). A service definition or script that still passes either flag fails to start with `unknown flag`: remove the flag.

## New page addresses

The interface has new page addresses, and no old address is redirected. A bookmark to a former address opens the not-found page: update your bookmarks.

<!-- Pending F9-03's merged table: these rows are 07 §7.11's. Every other former page moves under /books/. -->

| Former address | New address |
| --- | --- |
| `/dashboard` | `/` |
| `/files` | `/books/files` |
| `/diagnostics` | `/system/diagnostics` |
| `/operations` | `/activity` |
| `/maintenance` | `/system` |
| every other page (Library, Book detail, Authors, Series, Works, Collections, Playlists, Trashed versions, the review workspace, and so on) | the same page under `/books/...` |

Seven interface preferences (the theme excepted) reset once with this release: the new interface stores them under new names. They are the activity source and pinned-operations settings, the table column settings, the dismissed announcements, the welcome wizard's completed flag, the operations indicator's collapsed state, and the advanced-settings toggle.
