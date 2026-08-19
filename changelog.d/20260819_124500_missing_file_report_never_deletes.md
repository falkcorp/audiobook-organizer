### Changed

#### The missing-file repair job never deletes anything again

`maintenance.missing-file-repair` used to delete `book_file` rows whose bytes
were gone, for any book that still had at least one surviving file. That
deletion has been removed outright, and the job is now a report: it stats every
row, groups the dead ones per book, and surfaces what needs a human decision.
Passing `{"apply": true}` is now a hard error rather than a silent no-op, so a
caller still asking for the old behaviour is told the behaviour is gone instead
of being told it succeeded.

The reason is the 2026-08-17 full-population audit. Between 2026-03-03 and
2026-08-15 the shipped default filename format put the track number as
`{track}/{total_tracks}`, and the `/` was never sanitised — so "track 70 of 131"
became a directory named `... - 70` containing a file named `131.mp3`. The disk
was later repaired; the database rows were not. Every one of the 101 rows of
that shape checked in the audit had its bytes present on disk under the flat
name. Those rows are the only pointer to a file that exists, and the old job
classified them as safe to prune.

The job's `library.write` capability was dropped along with the code, and the
narrow `bookFileBulkDeleter` interface it used was deleted, so re-adding
deletion now requires writing the interface again and justifying it.

### Added

#### A shape-classification pass that can size the recoverable population

`maintenance.missing-file-audit` takes a new `{"classify": true}` parameter.
It runs over **every** missing row rather than the sample, derives the flat path
each track-slash row's bytes would live at, and asks the filesystem whether they
are there. It reports how many rows are recoverable, how many match the shape
but have genuinely lost their bytes, and how many do not match at all.

This exists because the sample could never answer "how many". The audit collects
its sample as the first N missing rows in iteration order, so it is clustered by
book — widening it yields a wider clustered sample, not a rate.

The pass plants deliberately bogus control paths in the same stat batch and
**fails the whole run** if any of them resolve. Without that check, "every
candidate resolved" would be equally consistent with a stat that always succeeds
against a wrong mount. It is off by default because it doubles the stat load on
a network mount.
