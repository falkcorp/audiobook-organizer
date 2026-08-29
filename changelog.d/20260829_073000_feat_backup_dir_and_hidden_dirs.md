### Added

#### Backups can now be stored somewhere other than the database's own disk

Where database backups are written is now a setting (`backup_dir`) instead of
being fixed to a folder next to the database. Pointing it at different storage
is the real fix for what took the app down on 29 August: a backup that shares a
disk with the database it is protecting can fill that disk and take the database
with it.

Leaving the setting empty keeps the previous behaviour exactly.

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
