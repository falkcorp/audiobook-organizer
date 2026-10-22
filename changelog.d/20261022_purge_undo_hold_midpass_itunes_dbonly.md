The purge's undo hold is now decided against a fresh view of the combine
journals. The shell index used to be built once per pass, so a combine that
journaled while a pass ran did not hold its shell and the purge could delete
it, making that combine un-revertable (fail-open). Every combine-journal write
now bumps a generation counter (`merge.CombineJournalGeneration`); the purge's
per-book decision, made under the merge lock where every journal writer also
holds it, rebuilds the index when the counter moved, on both the plain delete
and the carry-listening-state path. A rebuild that cannot read the journals
keeps the book and reports it as failed with the reason.

A merge may now make database-only changes to an iTunes book it did not select
(owner decision 2026-10-22). A non-selected iTunes version-group sibling or
group member is re-homed or demoted by writing its `version_group_id` and
primary flag only, instead of refusing the whole merge; its file path, tags,
book_file rows and file are untouched. Books the caller selects are still
refused when they are in the iTunes library, and a combine (which moves files)
still refuses iTunes inputs. `PreflightLink` matches: it no longer refuses
because of a non-selected iTunes copy. The comment citing the removed
`itunes.ComputeMergedTrackCleanup` is rewritten to the actual reason a survivor
holding an iTunes persistent ID keeps the primary flag (the PID integrity audit).
