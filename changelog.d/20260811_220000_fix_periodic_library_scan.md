### Fixed

#### Newly added books were never discovered — nothing scanned automatically

Copying audiobooks into a watched folder did nothing until somebody opened the
UI and pressed **Scan**. This was not a broken feature; automatic discovery had
simply never been wired up. Four separate gaps produced the one symptom.

**The scheduled scan could not tick.** `library_scan` was a registered
scheduler task, but its `GetInterval` returned a hard-coded `0`. The scheduler
only starts a ticker when `IsEnabled() && GetInterval() > 0`, so no ticker was
ever created for it. `IsEnabled` and `RunOnStart` both read `scan_on_startup`,
which defaults off, so the task was inert on every axis. There is now a
`scheduled.library_scan` config family (`enabled` / `interval` / `on_startup`)
that ships **enabled with a 6-hour interval** — the only member of that family
that defaults on, because it is the only unattended discovery path. The legacy
`scan_on_startup` flag still enables the task and still triggers a startup
scan, so nobody loses behaviour they had configured.

**The maintenance-window toggle was dead config.** `maintenance.library_scan`
existed in the settings UI and in the config struct, but the maintenance-window
operation only ever iterates `MaintenanceOrder()`, and `library_scan` was
missing from that list. Flipping the toggle did nothing at all. It is now in
the list, placed **last**: the window operation stops running tasks the moment
the window closes, so a full library walk at the front would have starved
dedup, purge and optimize on the same night.

**File watchers only ever saw the import paths that existed at boot.** The
auto-scan watchers were started from a single startup snapshot of the import
paths, so a folder added later got no watcher until the process was restarted.
Watchers are now managed by a supervisor that re-reads the desired set every
five minutes: a path added, enabled, disabled or removed at runtime is picked
up without a restart, and the watcher for a removed path is stopped rather than
leaked. (These watchers remain the low-latency path only — fsnotify does not
see writes made by remote NFS/SMB clients and can exhaust the kernel watch
limit on a large tree, so the timed scan is the guaranteed catch-all.)

**A failed import-path read started zero watchers in silence.** The startup
code was `if err == nil && len(importPaths) > 0 { ... }` — a database read
failure fell straight through the condition with no log line, leaving auto-scan
apparently enabled but completely blind. Enumeration failures are now logged
and pushed to the activity log, and a transient failure leaves the existing
watchers running instead of tearing them down.

A repeated scan can no longer pile up: the scheduled task skips a tick while
the previous scan it enqueued is still queued or running, which matters because
the operation dispatcher serializes a duplicate rather than rejecting it.

One deliberate limit, now recorded in the code rather than left as an accident:
a default scan still does **not** walk the organized library root. That
directory is the destination the organizer writes into (and on this deployment
the hands-off iTunes tree sits underneath it), so folding it into a scan that
now runs every six hours would feed already-organized books back through the
organize path on a loop. A folder dropped straight into the library root is
therefore still not auto-discovered — add it as an import path, or run a scan
with `force_update`. The scan log now says so out loud instead of staying
quiet about it.
