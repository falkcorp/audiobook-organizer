### Fixed

#### A database backup can no longer fill the disk and kill the database

On 2026-08-29 production had been restarting every ~17 minutes for hours and no
library scan had ever completed. New downloads never appeared in the library,
and every one of the 28 scheduled tasks except one showed "last run: never".

The cause was the backup taken before organizing. It archives the whole database
into `backups/`, which sits on the **same filesystem as the database**. That
filesystem reached 100% full, so the archive write failed — and then PebbleDB,
writing its own log to the same filesystem, hit `no space left on device` and
the process exited. systemd restarted it, the scan resumed, the backup ran
again, and the cycle repeated.

Three things combined, and all three are fixed:

- **Nothing checked for free space.** The backup opened the archive file and
  streamed into it until the write failed, consuming the last free bytes on the
  way. It now measures the space it needs first and refuses if the archive plus
  a 2 GiB margin will not fit. The margin matters because the database stays
  live for the 20-25 minutes the archive takes; leaving room for exactly the
  archive still starves the database of room to write meanwhile. A refused
  backup logs a warning and organizing continues, which is what already happened
  for any other backup failure.
- **Old backups were only cleaned up after a successful backup.** Exactly
  backwards: when the disk was full the backup failed, so the cleanup that would
  have freed room never ran, and every retry refilled the disk. Cleanup now runs
  before the archive is written as well, and accounts for the archive about to
  be created — so a backup that only fits after cleanup can now be taken.
- **The retention limit was a file count, on files that had grown 60x.** It kept
  the newest 10 archives. That was written when an archive was 247 MB, for 2.5 GB
  retained. Archives are now about 15 GB, so the same rule was aiming to keep
  150 GB — on a 141 GB disk. The limit was not failing; it was doing exactly
  what it was told, and what it was told had become impossible. Retention is now
  bounded by total size as well (40 GiB by default), so growth in archive size
  can no longer turn the policy into the thing that fills the disk.

If your backups directory is already oversized, this change will not delete
anything on its own beyond the new limits; check `backups/` and remove old
archives if the filesystem is still full.

Also: the free-space helper that already existed for the system dashboard moved
to a shared location so the backup path uses the same implementation rather than
a second copy.
