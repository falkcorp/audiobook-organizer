### Fixed

#### Files a purge kept because another book or row named them are removed once nothing names them

When a version is purged, a file that another book_file row or another book (a
trashed one included) still names stays on disk and is recorded on the version
(`PurgeKept`). Nothing looked at it again: after the trashed book was hard-deleted,
or the lingering row went, the file stayed on disk with no owner.

The nightly trash cleanup (`maintenance.trash-cleanup`) now ends with
`versions.SweepKeptFiles`, which ships **report-only**: while the new setting
`kept_sweep_apply_enabled` (env `KEPT_SWEEP_APPLY_ENABLED`, off by default) is off
it makes every decision below but unlinks no file and drops no note, and lists in
the operation result and summary the files it would remove and the notes it would
drop (`kept_sweep_report_only`, `kept_sweep_would_remove`, `kept_sweep_would_drop`
and the matching `*_files` lists with version, book, path and the reason the file
had been kept; the operation page shows them). The owner reads a night or two of
that list and flips the switch. Report-only is not read-only for the version
record: a note the sweep really cannot settle is still counted, and at the third
night the version moves to `purge_failed` with the unsettled notes in
`purge_failed` (see below). No file is touched. The setting is read once per run. For each `inactive_purged` version with kept-file notes
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
- A note the sweep cannot settle (an owner lookup that errors, a path the disk
  cannot settle, a failed removal) no longer warns every night for ever. The nights
  are counted on the version in the purge's own counter
  (`BookVersion.PurgeUndecidedNights`), at most one a calendar day
  (`PurgeUndecidedNightOn`, used by the sweep only); after
  `PurgeUndecidedEscalateAfter` (3) in a row the version moves from
  `inactive_purged` to `purge_failed`, with the unsettled notes in `purge_failed`.
  The trashed-versions page then shows it with its files and reasons, Purge now
  retries it and hard delete drops it. The sweep stops warning about the unsettled
  notes, but keeps sweeping the other kept notes of a `purge_failed` version every
  night (report-only lists them as would-remove too); it never escalates it again.
  Only real "could not settle" counts: not a decision that would only have removed
  something in report-only mode, not a book that serves nothing on disk (the
  purge's `ErrPurgeNothingServed`), nor an unready library or a busy merge lock. The store now accepts `inactive_purged` to `purge_failed` for
  this (and nothing else from `inactive_purged`). The escalation is reported in the
  result on the night it happens (`kept_sweep_escalated`).
- A file outside the version's own `.versions/{id}/` folder is never removed by the
  sweep while the version's own book row exists, trashed or live: the note was kept
  because that book served the file, and a trashed book is restorable and wants its
  audio back. Only a book that is gone entirely hands the file back.
- Purge now on a `purging` or `purge_failed` version (a retry, including one the
  sweep escalated) applies the "the book serves nothing that exists on disk" refusal
  too, which used to hold only for a trashed version, so a retry can no longer
  delete a file the sweep refused to. It also decides every file named in the
  version's `PurgeKept` and `PurgeFailed` notes, not only the slot and the rows;
  before, a note the retry did not find was erased with its file still on disk. Each
  is removed, kept (and noted again, with the same owner rule as above) or left
  undecided.
- The result panel of the trash-cleanup operation now shows the kept-file sweep: the
  report-only line, the would-remove and would-drop lists (path, version, book, the
  reason the file was kept) and any version it escalated.
