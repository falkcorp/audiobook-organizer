<!-- file: docs/executive-summaries/2026-11-19-reconcile-apply-followups-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 25af5895-85e0-42d7-abad-71c5d6836840 -->
<!-- last-edited: 2026-11-19 -->

# Re-pointing a book at its found file: the last gaps in the undo history and the "file already taken" check are closed

**Date:** 19 November 2026. A follow-up to the reconcile apply work of 18 November
(PR #4719 and the review round after it). The "reconcile" action re-points a book
at an audio file the library found for it. Nothing about it moves, copies or
deletes a file; it only changes where the book and its file record point.

## Executive Summary

- Two books could end up claiming the same audio file. The apply step checked that
  the found file was not already taken, but a library scan could register that
  file for another book in the instant between the check and the write, and the
  write then took the claim away from that book. The library store now makes the
  check and the write one step, so the second book is refused instead. The match
  is reported as not applied and nothing is changed.
- The undo history no longer keeps entries for changes this run did not make.
  If another writer had already pointed the book at the found file, the run used
  to keep undo entries for a move it never made, so undoing the run could undo
  that other writer's later change. The run now takes those entries back, and when
  it cannot, the result says so.
- Finishing a half-applied match from an earlier run now also hands over the
  earlier run's entries about the book's old hashes. Before, undoing the earlier
  run after a later successful apply could put an old hash back on the book.
- A mistyped match type (for example "Hash" for "hash") used to be treated as an
  unproven match and silently cleared the book's hashes. It is now refused with a
  clear message, both when the request is sent and again when the match is applied.
- A match that was applied but could not hand its history over to an earlier run
  is now listed in the run's errors, so it is visible which earlier run is no
  longer safe to undo on its own. A half-applied match whose history cannot be read
  at all is refused with nothing written.
- The operation's own description said it moves files. It no longer does.

## What changed for the person using it

Nothing visible on the happy path. When two things collide, the result now says
what was refused or left undone and why, where before it either did the wrong
thing quietly or said nothing.

## One thing to know

History entries written before this change are not labelled with where they came
from, so a half-applied match from before this change is finished by undoing its
run rather than by re-running it.
