### Fixed

#### Listing backups no longer reads every backup file

Asking the app what backups exist used to make it read every backup archive from
beginning to end in order to compute a checksum for each one — a checksum
nothing actually used. With around 16 GB of backups that page simply never
loaded.

The same listing runs during every backup, before deciding whether old backups
need clearing out. That decision takes an instant, but it was preceded by
reading the entire backup folder: the backup that reported failing "after
18m38s" spent almost all of that time reading files in order to report that the
disk was full.

Listing is now instant. Checksums are still available for anyone who wants to
verify a backup's integrity — they are just requested deliberately rather than
computed every time.
