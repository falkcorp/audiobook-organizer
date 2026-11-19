<!-- file: docs/executive-summaries/2026-11-19-maintenance-review-followups-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: e7ca9f10-f9d8-4ed9-9655-e339f35d7621 -->
<!-- last-edited: 2026-11-19 -->

# Follow-ups to the cleanup-job review: fewer half-finished edits

**Date:** 19 November 2026. These checks ran against test libraries; none of
these jobs has been run against the real library with this change.

## Executive Summary

- Reviewers of two recent changes (the "Fix mistaken merges" preview and the
  change that makes cleanup jobs respect locked fields) left a list of smaller
  problems, eleven in all. This work closes nine of them and a tenth in part (the
  gap described in the last point). The eleventh cannot be changed: it is the
  wording of a commit that has already shipped.
- The one that mattered most: the job that fills in missing authors from a file's
  tags saved a book's main author first and its co-authors second. If the app
  stopped between the two saves, the book kept its main author and lost its
  co-authors, and no later run would notice, because the book no longer looked
  like it was missing an author. Both are now saved together, so a stop leaves the
  book exactly as it was and the next run picks it up again.
- The author split did the opposite kind of damage when the database could not save
  both parts of a credit at once: it created the new author entries first and then
  failed on every book, leaving entries that no book used. It now checks once at
  the start and refuses without writing anything.
- The author-repair preview now reads many authors at the same time instead of one
  after another, so a library-sized preview uses the machine's cores. What it
  reports is unchanged. The real repair still runs in order.
- The mistaken-merge preview no longer treats a book as the owner of a retired
  version just because the version record names it; it checks that the book holds
  the files. Its explanations of why a row is held now say which book and which
  version group the finding came from.
- One gap stays open and is written down: while filling in an author, the job can
  still create one author entry just before it saves, and that entry stays unused
  if the save is then refused. Deleting it safely needs a new database operation,
  so it is left for a decision.
