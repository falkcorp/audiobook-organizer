<!-- file: docs/executive-summaries/2026-11-21-restore-across-filesystems-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: eed57e59-60d4-4bf9-8a10-b91f46e8ab4c -->
<!-- last-edited: 2026-11-21 -->

# Rolling back the database now works when its files live on two disks

**Date:** 21 November 2026. Pull requests #4932, #4933 and #4935 are merged; the
cleanup and durability fixes are in the follow-up pull request that carries this
file.

## Executive Summary

- During a rehearsal of the planned settings switch-over on a copy of the
  production server, the rollback command that restores the database from its
  pre-upgrade checkpoint refused to run. The server keeps its database on one
  disk pool and its private state files, the encryption key among them, on
  another. The restore prepared every replacement file next to the database,
  and a file cannot be swapped into place in one step from another disk, so the
  restore stopped before changing anything. After the switch-over this restore
  is the only way back, so the switch-over was blocked until it worked.
- The restore now prepares each replacement file on the same disk as the file it
  replaces. It cleans up after itself on both disks whether it finishes, is
  refused, or takes the emergency route used after a failed restore. The
  crash-safety tests, which stop the restore at every step and check that the
  server comes back whole, now also run with the state files on a second disk.
- The backup service's own restore had a separate fault that would have refused
  every restore of an archive carrying the settings file. It wrote the settings
  copy into the restore's folder before that folder was registered, and
  registering refuses a folder that already exists. Nothing calls that path in
  production yet. It now writes the copy afterwards. If the copy cannot be
  written, the restore still goes ahead and tells the operator, in its result
  and in its saved note, that the copy is missing.
- A restore killed part way used to leave its prepared copies behind, the
  encryption key among them, and nothing ever removed them. The next restore now
  removes them, and leaves in place and reports anything that does not look
  exactly like such leftovers. Each copy is now private from the moment it is
  created, and so is every folder above it.
- The folders holding the prepared copies are now written to disk before the
  swap starts. Before, a power cut at the wrong moment could lose a prepared
  file, and the restore would then finish with the encryption key missing.
- Verified by tests that fail on the old code and pass on the new. The checks
  that a disk boundary is respected run against a simulated second disk; the
  real proof is the rollback rehearsal on the production copy.

## The restore refused when the database and the state files were on two disks

- **What it was:** the restore prepared each replacement state file next to the
  database. On a server whose state directory is on another disk, the
  pre-flight check found that the swap would cross disks, so it refused.
- **Why it mattered:** after the switch-over the old settings format cannot be
  read, and this restore is the only way to return to the version before it. A
  rollback that refuses leaves the server stuck on a release that is not
  working.
- **The fix:** each file is prepared in a private folder on its own target's
  disk, and every one of those folders is cleaned up on each way out of the
  restore.

## The backup service's restore refused archives that carry the settings file

- **What it was:** the settings copy was written into the restore's folder
  before the restore was registered, and registering refuses a folder that
  already exists.
- **Why it mattered:** once the backup service is wired in, every restore of
  such an archive would have failed. The tests used a stand-in for the
  registration step that did not make this check, so they never caught it.
- **The fix:** the copy is read and checked first and written after
  registration. A failed write is reported to the operator, not just logged.

## A killed restore left copies of the encryption key behind

- **What it was:** each restore uses a fresh name, so whatever a killed attempt
  had prepared stayed on disk for good, encryption key included.
- **Why it mattered:** stray copies of a secret pile up in places nobody checks.
  The folder holding them was already private, but each copy was made private
  only after it was fully written, so a copy left by a kill at that moment
  stayed at ordinary permissions.
- **The fix:** the next restore removes leftovers that match exactly. It leaves
  in place and reports anything with extra content, links, or a registered
  restore. The copies are private from the moment they exist.

## A power cut could lose a prepared file mid-restore

- **What it was:** the folders holding the prepared files were created but not
  forced to disk.
- **Why it mattered:** after the live encryption key is moved aside, the
  prepared copy is the one that goes into its place. Losing it to a power cut
  would have finished the restore with no key, and the database could not be
  opened.
- **The fix:** every level of those folders is forced to disk before the swap
  starts, the same rule the swap already uses for its own folders.
