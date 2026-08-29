### Fixed

#### The disk-full safeguard can now clear the space it needs, instead of getting stuck

Yesterday's fix stopped the app filling its own disk with database backups — it
now refuses to start a backup there is no room for, which is what was crashing
the app every seventeen minutes.

Watching it run in production showed the refusal worked but left the app stuck.
The check for free space happened *before* the clean-up of old backups, so the
app correctly declined to make things worse and then had no way to make things
better: the old copies that caused the shortage were never cleared, and every
later attempt declined for exactly the same reason. Only someone deleting files
by hand could break the deadlock.

Old copies are now cleared before that check, so the app can recover on its own.

#### A clean-up will never delete your last backup

Making the clean-up actually run revealed that it could delete *everything*. The
size limit can be impossible to satisfy — if a new backup is 30 GB and the limit
is 40 GB, no existing 15 GB copy can be kept — and the clean-up would keep
deleting until nothing was left, to make room for a new copy that might then
fail. That could leave you with no backups at all.

The most recent backup is now never deleted to make room. If the size limit
cannot be met without it, the app keeps the backup and says so in the log, since
that is a setting that needs a person to look at it — either a larger limit, or
a backup location that is not on the same disk as the database.
