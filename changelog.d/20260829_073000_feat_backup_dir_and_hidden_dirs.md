### Added

#### Backups can now be stored somewhere other than the database's own disk

Where database backups are written is now a setting (`backup_dir`) instead of
being fixed to a folder next to the database. Pointing it at different storage
is the real fix for what took the app down on 29 August: a backup that shares a
disk with the database it is protecting can fill that disk and take the database
with it.

Leaving the setting empty keeps the previous behaviour exactly.

#### How much space backups may use is now a setting

`backup_max_total_bytes` sets the ceiling on the total size of kept backup
archives. Leaving it unset keeps the previous built-in limit of 40 GB.

This pairs with the setting above. Moving backups onto a much larger drive
achieved little while the app still refused to use more than 40 GB for them —
with a backup of this database being around 15 GB, that allowed roughly one
backup to be kept no matter how much room the new drive had.

### Fixed

#### Scans and sweeps no longer look inside folders whose name starts with a dot

The app keeps its own working files inside the library folder — backups,
playlists, iTunes write-back staging. Anything named with a leading dot is now
skipped by the library scan, by the file counter that drives the progress bar,
by the folder watcher, and by the organiser.

Previously only one such folder was skipped, by name, which meant every new one
had to remember to add itself to a list. This matters more now that backups can
live in the library folder: a 15 GB backup archive would otherwise be discovered
and considered for import as though it were an audiobook, and writing one would
have triggered the folder watcher into starting a scan.

A folder you deliberately point the app at is still scanned, even if its own
name starts with a dot. The rule applies to what is found inside it.

One folder is deliberately exempt: `.alternates`, which will hold alternate
versions of a book. That is library content rather than app working state, so
the scan must still see it. The exemption is an exact name match, and it is kept
in one place so future exceptions do not get scattered across the code.

#### A backup that could not be deleted was counted as deleted anyway

When clearing out old backups to stay within the size limit, the app counted
every archive it *tried* to remove as removed — including ones the removal
actually failed on. It would then stop early, believing it had freed room that
was still occupied, and quietly stay over its limit.

This became possible to hit now that backups can live in a shared folder, where
the app may find archives it does not have permission to delete. Failed
deletions are now counted honestly, and the app says so in the log rather than
carrying on with wrong numbers.

#### The backup log now says where the backup is going

Backups record the destination folder, not just the database being backed up.
If the destination setting is ever lost, archives silently revert to sitting
beside the database — the arrangement that filled the disk — and nothing said
so. Now it does.
