<!-- file: docs/executive-summaries/2026-10-19-sqlite-activity-backend-removed-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 85964619-52cd-4f94-8d9a-ee430e26ba4c -->
<!-- last-edited: 2026-10-19 -->

# The second activity database is gone

Branch `task/01-p75`, PR not yet merged. Spec: `docs/proposals/2026-10-holistic/tasks/01/01-P75.md` (owner decision D11). The last build that can still use the removed code is tagged `activity-sqlite-last`.

## Executive Summary

- **One activity store instead of two.** The activity log (the history behind the Activity page) used to have two possible homes: a section of the main database, managed by Pebble (the storage engine the rest of the library uses), or a separate SQLite database file. Production already runs on Pebble. The SQLite option, the code that copied history between the two, and the settings that chose where the SQLite file lived are now removed. About 13,000 lines go with them, roughly half of them code and half tests.
- **An old setting fails loudly instead of being quietly ignored.** If a config file or the server's environment still asks for SQLite, the server refuses to start and the message says how to fix it (remove the setting or set it to `pebble`). A settings change that asks for SQLite is refused the same way. Production names Pebble explicitly, so it is not affected.
- **Nothing is deleted from disk.** The old SQLite activity file is neither read nor removed. It can be deleted by hand once nobody needs it.
- **Three maintenance jobs that only worked on SQLite are removed.** These are the nightly "optimize activity database" job, the "vacuum activity database" job, and the job that cleared copied rows out of Pebble after a move to SQLite. On a Pebble-only server each of them did nothing or refused to run.
- **A safety limit now covers the store production actually uses.** Activity summaries are capped at 8 KiB. That cap was added after one 9.5 MB summary helped bloat the SQLite file in September, but it was only ever applied when SQLite stored the row. Pebble now applies it on every write.
- **The Settings page no longer offers controls that did nothing.** The Activity Database path and "move the database" switch on Settings, Paths are gone.

## Changes

### Removing the SQLite activity store

- **What it was.** The activity log could be stored in SQLite as an alternative to Pebble. That came with a lot of supporting code: a migration that ran at startup, a mode that wrote every entry to both stores, a move feature for the file, and repair and statistics jobs specific to SQLite.
- **Why it mattered.** None of that code ran in production, but every change to the activity log still had to keep both stores working. It was also the only code that needed the SQLite library, which made the server binary bigger.
- **The fix.** The SQLite store and everything that existed only to support it is deleted, and the SQLite library is removed from the build. Code and comments that described the two-store arrangement now describe the one store.

### Stopping on a setting that asks for SQLite

- **What it was.** The choice of activity store came from a setting. Once SQLite is gone, a server whose configuration still asks for it would have nothing to give.
- **Why it mattered.** Quietly running on Pebble when the operator had asked for SQLite would hide a configuration mistake. Crashing with an unclear error would be worse.
- **The fix.** The server checks the setting when it starts, and again whenever settings are saved. `sqlite`, or any other value that is not `pebble` or empty, gets a message that names the fix. One exception: settings saved in the database are re-applied at boot with Pebble as the default, so an old stored value of `sqlite` loads as Pebble, the same as it already did.

### Applying the summary cap to Pebble

- **What it was.** Each activity entry has a one-line summary. The cap that stops a runaway summary from being stored whole was written into the SQLite store only.
- **Why it mattered.** With SQLite gone, removing that code would have left no cap at all. In fact Pebble had never had one, so one bad message could again write megabytes into the activity log.
- **The fix.** The Pebble store now applies the same idempotent cap to every entry it writes. A test writes an oversized summary through both of Pebble's write paths and checks that what is read back is capped.
