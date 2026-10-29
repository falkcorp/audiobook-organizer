### Fixed

#### Files a purge kept because another book or row named them are removed once nothing names them

When a version is purged, a file that another book_file row or another book (a
trashed one included) still names stays on disk and is recorded on the version
(`PurgeKept`). Nothing looked at it again: after the trashed book was hard-deleted,
or the lingering row went, the file stayed on disk with no owner.

The nightly trash cleanup (`maintenance.trash-cleanup`) now ends with
`versions.SweepKeptFiles`. For each `inactive_purged` version with kept-file notes
it asks the purge's own per-path decision again (`classifyPurgePath`: served by a
live book, any other row or book at any spelling of the path, the purge guard for
import paths, the iTunes library, `.failed`, the library root and a folder that
holds files, and whether the disk can say the file is gone), so the sweep and the
purge cannot disagree. A file nothing owns any more is removed and its note
dropped; a note for a file that is already gone is dropped; everything else is
left alone. A removal is allowed only while the version's own live book is known to
serve a file that exists, as the purge requires of a trashed version.

- It fails closed: a read error, a path the disk cannot settle, a library that is
  not ready, a busy merge lock, a failed removal or a failed record keep the file
  and the note and are retried next night.
- The note is dropped after the unlink, never before, and the version is written
  after each removal. The merge lock is held for each version that has something to
  remove, as the purge holds it. Emptied version folders are tidied only when no
  note is left.
- Versions are partitioned across a bounded worker pool so that two workers never
  handle the same version or the same file (versions that note one file are one
  work unit). Versions the same run just purged are not asked twice.
- The operation result and log report files removed, notes dropped for files
  already gone, files still owned and files kept on error
  (`kept_sweep_removed`, `kept_sweep_gone`, `kept_sweep_still_kept`,
  `kept_sweep_kept_on_error`, with the removed and errored paths). A sweep that
  cannot list the purged versions fails the night.
