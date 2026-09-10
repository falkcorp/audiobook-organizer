<!-- file: docs/executive-summaries/2026-09-10-the-safety-checks-that-were-missing-executive-summary.md -->
<!-- version: 1.8.0 -->
<!-- guid: 5b9d2e47-8c1a-4f63-b2d7-1e6a4c9f0d38 -->
<!-- last-edited: 2026-09-10 -->

# The safety checks that were missing

**Pull requests:** #3181 (merge lock), #3180 (organize check), #3182 (author delete
guard), #3183 (backup verification), #3185 (orphan-file cleanup guard), #3187 (duplicate
merge audio guard), #3188 (duplicate rows in one batch), #3189 and #3190 (two more
series-delete guards), #3191 (series-dedup undo record and scan check), #3192 (the
deleted "fix library states" job), #3193 (author-merge preview error state), #3194 (iTunes
cleanup apply path retired), #3196 (iTunes write-back shutdown and single writer), #3197
(database upgrade bookkeeping), #3198 (preview for the operation-history delete). All are open and **held for the owner's review** because they touch paths that move or delete library data; this summary
will be updated with merge commits as they land. Merged: #3184 (ISBN sweep outage
reporting, `2ed12521b`), #3186 (scan reports a failed AI phase, `03286fa87`). Planning
package: #3179 (`docs/agent-tasks/todo-completion-2026-09/`).

## Executive Summary

- A full re-audit of the codebase on 2026-09-10 found a short list of places where the
  app could quietly damage or misreport library data. This is the first batch of fixes
  from that list, chosen because each one is a data-loss risk.
- **Two "merge" features could step on each other.** Merging duplicate books from the
  review screen and the separate "split-book" merge (which glues chapter files back onto
  one book) both rewrite the same records. Three of the four merge paths already took
  turns through one shared lock; the split-book path did not. Two merges touching the
  same book at once could leave it half-deleted, or with files pointing nowhere. It now
  takes the same lock as the others.
- **"Organize" could say a file was fine when it was gone.** When a book's recorded
  location already matched where the organizer wanted to put it, the organizer reported
  success without looking at the disk. A stale record pointing at a deleted or moved file
  was reported as organized. It now checks that the file is actually there and reports an
  error if it is not.
- **The "is this author still used?" check could miss books.** Before deleting an
  author that looks unused, the app counts every book that still credits them. That
  count scanned a slightly-too-narrow slice of the database: it covered every book id the
  app generates itself, but not ids supplied by an importer, migration, or restore. A book
  with one of those ids could credit an author and still not be counted, so the author
  looked unused and was deleted. The scan now covers every book record.
- **"Verify this backup before restoring" did nothing.** The restore screen has a
  "verify" option, on by default. It never verified anything, because the app computed a
  fingerprint when it made a backup and then threw it away. Restoring from a damaged
  backup file looked identical to restoring from a good one. The app now saves that
  fingerprint next to each backup and checks it on restore, refusing to restore a file
  that no longer matches.
- **The orphan-file cleanup trusted a cache that can be missing rows.** A maintenance
  job deletes file records that no longer belong to any book. It decided "belongs to no
  book" by consulting the in-memory copy of the book list, which after a restart warms up
  over a couple of minutes and can permanently lose rows if something goes wrong during
  that warm-up. Every file of a book missing from that copy looked orphaned and was
  deleted. The job now uses a version of the lookup that refuses to answer unless the
  in-memory copy is complete, and falls back to the on-disk database when it is not.
- **Merging duplicates could delete the copy that had the audio.** The iTunes repair
  path merges books it recognizes as the same recording and deletes the extras. It never
  checked that the book it kept actually had an audio file. It now refuses that merge
  and reports why, the same rule the other merge features already enforced.
- **The nightly ISBN lookup reported "checked" during outages.** When every book
  database was rate-limiting us or temporarily blocked, the sweep counted each book as
  checked with no result, identical to a book that truly has no ISBN anywhere. It now
  counts those separately, names which source failed and how often, and marks the run as
  failed when nothing was actually searched.
- **Two copies of one file in a single import doubled the book's length.** When a scan
  or iTunes import handed the database two records for the same file in one batch, both
  were saved, and the book's total duration and size were counted twice. The batch now
  merges the later record into the earlier one before saving.
- **A scan whose AI step failed still reported a clean finish.** If the language-model
  service was down for the whole run, the scan finished green with nothing on its record.
  The failure now appears as a warning on the scan's own operation record.
- **Two more cleanup jobs could delete a series that trashed books still belonged to.**
  The series-normalize and series-denumber jobs only looked at live books when deciding
  a series was empty. A series whose members were all in the trash looked empty and was
  deleted, leaving those books pointing at nothing. Both now consult the unfiltered count
  first and keep the series when anything still references it. This closes the last two
  of four series-delete paths; the first two were fixed in August.
- **The series de-duplicator left no record of what it changed.** The job that folds
  duplicate series into one moved every book across and deleted the extras without
  writing anything to the undo ledger, and it could run while a library scan was
  re-creating the very series it was deleting. It now refuses to start while a scan is
  running or queued, records every book it moves so the move can be undone, and records
  every series it deletes.
- **A maintenance job that would have emptied the library view was one click away.** The
  "fix library states" job stamped every book with a status value nothing in the app
  reads, so the audiobook shelf would have shown nothing after it ran. It had never been
  run. It is now deleted outright, with a test that fails if anyone adds it back.
- **The author-merge preview could show an empty book list for an author with books.**
  When the lookup behind that preview failed, the screen said "No books found," which is
  what a reviewer would read as "safe to merge." It now shows a load error with a retry
  button and only says "no books" when the lookup actually succeeded with none.
- **An iTunes cleanup that could delete real chapter files was still one API call away.**
  Its removal rule had been judged unsafe in July and the decision was "measure, don't
  remove," but the apply path still worked. It now refuses before it touches the iTunes
  library file; the preview still works.
- **Shutting down while iTunes changes were being written could corrupt the library
  file.** The background writer that pushes changes into the iTunes library did not wait
  for its own workers on shutdown, and two of its flushes could write the same file at
  the same time. It now waits, and only one flush can write at a time.
- **A crash during a database upgrade could re-run the upgrade step.** The app recorded
  "upgrade N applied" and "database is now at version N" as two separate writes. A crash
  between them made the next start re-run step N. Both are now written together, a
  recorded step is never re-run, and a test checks that every upgrade step is safe to
  repeat.
- **Clearing operation history had no preview.** The one bulk delete in the operations
  screen's API ran immediately. It now has a dry-run mode that reports what it would
  remove, and the real delete reports the same counts.
- All eighteen fixes come with a test that reproduces the original problem and fails on
  the old code, so the gap cannot silently reopen.
- Each fix also turned up a sibling of the same shape (a second unguarded merge path in
  a maintenance job, and the in-place re-organize step). Those were deliberately left out
  of these changes and filed as tracked tasks so they get their own fix and review.

## 1. Two merge paths that did not take turns

**What it was.** The app has several ways to merge books: the duplicate-review merge,
the multi-file "combine," an iTunes-driven merge, and the split-book merge that
reassembles a book whose chapters were imported as separate books. Each one reads the
book, changes it, and deletes the losers. The first three already coordinate through a
single lock so only one runs at a time on any book. The split-book merge did not, and it
can be started from two places, a background bulk job and a button in the UI, neither of
which knew about the others.

**Why it mattered.** Two merges running on the same book at the same time can interleave
their steps. The known outcome of that pattern, seen once before in this project, is a
book that ends up both "primary" and "deleted," or chapter files orphaned mid-move. It is
a corruption you only notice later, when a book is missing or plays wrong.

**The fix.** The split-book merge now takes the same shared lock as the other three, so
any two merge operations on the library are mutually exclusive. A concurrency test races
it against the review merge and confirms only one runs at a time. A fifth path in an
older maintenance job was found doing the same thing without the lock; it is filed as a
separate task rather than folded into this change.

## 2. The organize step that trusted the record over the disk

**What it was.** When the organizer works out where a book's file should live and finds
the record already says it is there, it treats the job as done. It never looked at the
disk to confirm the file still existed.

**Why it mattered.** Records go stale: a file can be deleted, moved, or replaced after it
was organized, and a database record can be edited by hand. In every one of those cases
the organizer reported "organized" for a file that was not there, hiding exactly the
problem a user would want to know about, and letting downstream steps (the iTunes import
path uses this directly) act on a file that does not exist.

**The fix.** The organizer now checks the file on disk before reporting the no-change
case as a success, and returns a clear error naming the book and the missing path if it
is gone. The batch re-organize step has a similar shortcut that was left unchanged here
and is filed as its own task.

## 3. The author-usage count that scanned too narrow a range

**What it was.** Deleting an author, whether by hand or through the "purge empty
authors" cleanup, is guarded by a count of every book that still credits them. Book
records are stored under keys that begin with the book's id, and the count walked the
range of keys that begins with a digit, because the ids the app mints itself always do.
But the app also accepts ids handed to it by an importer, a migration, or a backup
restore, and those can begin with a letter or an underscore. Those records sat just
outside the scanned range.

**Why it mattered.** A book with such an id that credited an author only through the
older single-author field was not counted. If that was the author's only remaining
credit, the guard reported zero, and the author record, including anything a user had
edited on it, was deleted with no error. The same too-narrow range was also used by the
lookup that merge and delete flows use to find which books to re-link first, so a merge
could skip re-linking those books.

**The fix.** Both scans now walk the full range of book records, with the same
structural filter the other already-fixed scans use to skip the app's secondary indexes.
Two tests create a book with a letter-leading id and confirm it is counted and re-linked.
The one-off check for how many such ids exist in the live library was left for the
owner to run, since it touches production data.

## 4. The backup verification that never ran

**What it was.** When a backup is created, the app computes a SHA-256 fingerprint of the
archive, a short value that changes if even one byte of the file changes. It reported
that fingerprint in the API response and then discarded it. The restore endpoint accepts
a "verify" flag, and the Settings page turns it on by default, but with nothing saved to
compare against, the flag only produced a log line saying verification was "not yet
implemented" and the restore went ahead.

**Why it mattered.** A restore is the moment you most need to know a backup is intact.
A truncated or corrupted archive, which can happen on a full disk or an interrupted copy,
restored with the same success message as a good one. The user saw "verified" behavior
in the UI that did not exist.

**The fix.** Each new backup now gets a small companion file holding its fingerprint,
written atomically so it is either complete or absent. A restore with "verify" on reads
that file, recomputes the fingerprint, and refuses with a clear "does not match" error
before touching anything if they differ. Backups made before this change have no
companion file; verifying one of those returns an explanatory error that tells the user
to restore with verification off or re-create the backup, rather than pretending. The
first version of this fix simply refused every verified restore, which would have broken
the default UI path; it was sent back and replaced with real verification.

## 5. The cleanup that deleted files based on an incomplete list

**What it was.** The "orphan book files" maintenance job finds file records whose owning
book no longer exists and, when asked to, deletes them permanently. To know which books
exist, it read the in-memory copy of the book table. That copy is rebuilt in the
background after every restart and, if the rebuild is interrupted or loses rows, it stays
short until the next restart. Two other lookups (series and authors) had already been
taught to refuse an answer in that state; this one had not.

**Why it mattered.** A book absent from the in-memory copy made every one of its files
look orphaned. With deletion enabled, the job would remove those file records, and the
book would lose track of its audio. Unlike most mistakes in the app, this deletion has no
undo.

**The fix.** The job now asks a stricter version of the lookup that checks the in-memory
copy is complete before answering; if it is not, the app logs an error naming how many
rows were lost and reads the full list from the on-disk database instead. If neither
source can vouch for the list, the job stops without deleting anything. The everyday book
listing used by the web pages was deliberately left on the fast path, because forcing it
onto the slow on-disk scan for the life of a degraded server was the very slowdown an
earlier fix warned against; only the delete-deciding path got the strict version.

## 6. The duplicate merge that could keep the empty copy

**What it was.** When the iTunes repair finds two library entries that are acoustically
the same recording, it keeps one, moves the other's iTunes details onto it, and deletes
the other. The keeper was chosen by other criteria and was never checked for having an
audio file of its own. Three sibling merge features already had that check; this fourth
one did not.

**Why it mattered.** If the keeper was an empty shell, a stale record with no file, and
the deleted copy was the one that pointed at the audio, the recording's only link to its
file was removed.

**The fix.** Before it writes anything, the merge now checks whether the keeper has an
audio route; if it does not and any of the candidates does, it refuses with the same
typed error the other merge features use, and the repair path logs the refusal and moves
on. A merge where none of the copies has a file, which the repair legitimately uses to
collapse empty shells, still proceeds.

## 7. The lookup sweep that looked fine during an outage

**What it was.** A nightly job walks the library looking up missing ISBN and Audible
identifiers from several online sources. If a source returned an error, because we had
hit its rate limit or our own circuit breaker had tripped, the job silently treated that
as "no result" and counted the book as checked.

**Why it mattered.** A night when every source was down produced the same "checked
2,000, updated 0" line as a night when 2,000 books genuinely had no identifier. Nobody
could tell the job had not actually searched, and books that could have been enriched
were quietly skipped past.

**The fix.** Errors are now kept and counted per source. A book where every source
errored is reported as "errored", not "checked", the summary line names each source's
error count, and a run where nothing was actually searched is marked failed so it shows
red in the operations list.

## 8. Two records for one file in a single batch

**What it was.** Scans and iTunes imports save file records in batches of a few hundred.
Each incoming record was matched only against records already committed to the database,
so if the same file appeared twice in one batch (a re-observed path, or two entries with
the same iTunes id) neither one saw the other, and both were written.

**Why it mattered.** The book's totals are recomputed from its file records, so a
duplicated 10-minute file became a 20-minute book with twice the size on disk. Those
wrong numbers feed sorting, display, and the duplicate detector.

**The fix.** The batch now keeps a running map of what it has staged and merges a later
record into the earlier one, preferring the newer content but keeping the original
record's identity, exactly as if the two had been saved one after the other. Tests pin
the file-path case, the iTunes-id case, and the order of the checks, since checking the
committed records first would reopen the hole. Whether existing duplicate records on disk
need cleaning up is a separate decision left to the owner; the existing dry-run counting
job reports a lower bound, and the summary in PR #3188 explains what it misses.

## 9. The scan that finished green after its AI step died

**What it was.** When the AI-parse queue is unavailable, the scan runs the AI batch
inline. That phase already produced a summary of what it did and did not manage, and the
queued version of the job already reports that summary. The scan simply threw the summary
away and returned success.

**Why it mattered.** A revoked API key or an exhausted quota, the shape of the August 16
incident, aborted every batch, and the scan still showed a full progress bar and
COMPLETED. The only trace was buried in the server log.

**The fix.** The scan now passes the summary to its own operation record as a warning
when the phase failed, for both the library scan and the folder auto-scan. It is a
warning rather than a failure on purpose: an AI outage should not fail an otherwise good
scan chunk.

## 10. Two series cleanups that could not see trashed books

**What it was.** Four different features can delete a series record once it has no
books left. Each one lists the series' members before deleting, and that listing
deliberately leaves out books in the trash. Two of the four (the review-screen merge and
the author cleanup) were taught in August to double-check against an unfiltered count
before deleting. The other two, the series-normalize job and the series-denumber job,
were not.

**Why it mattered.** A series whose every member had been moved to the trash listed as
empty, so the job deleted it. If any of those books was later restored from the trash, it
came back pointing at a series that no longer existed, and nothing in the app could show
or repair that link.

**The fix.** Both jobs now fetch the unfiltered reference count once per run and refuse
to delete any series that still has references the filtered listing cannot see. The
books they can see are still merged; only the delete is held back, and the run's summary
says how many were held and why. The dry-run preview of the denumber job reports the same
held-back count, so what it promises matches what apply does. If the reference count
cannot be read at all, both jobs stop rather than guess.

## 11. The series cleanup that kept no record

**What it was.** The series de-duplicator finds groups of series that are really the
same one (differing only in punctuation or numbering), moves every book onto one keeper,
and deletes the rest. Every other bulk change in the app writes a line to an undo ledger
for each record it touches. This one wrote nothing. It also had no check for whether a
library scan was running, and a scan re-derives series from folder names, so the two
could fight over the same records.

**Why it mattered.** If the de-duplicator picked the wrong keeper or merged two series
that were not actually the same, there was no record of which books had moved and no way
to undo it. A scan running at the same time could re-create a series the job had just
deleted, or overwrite a book's series assignment mid-move, leaving the library in a state
neither feature intended.

**The fix.** Before it changes anything, the job now asks the scanner to stand down and
refuses to run if a scan is active or queued. It writes one undo-ledger line per book it
moves, so those moves can be reversed from the operations screen, and one audit line per
series it deletes. If writing a ledger line fails, the job reports that specific book or
series in its result rather than continuing silently. The dry-run preview, which is
still the default, touches nothing. One known limit: the "series deleted" lines are a
record only; restoring a deleted series from the undo screen is not yet supported and is
tracked separately.

## 12. The job that would have emptied the shelf

**What it was.** A maintenance job named "fix library states" was meant to reconcile each
book's status with whether its files were present on disk. It did that by writing the
words "present" or "missing" into a field the rest of the app reads as "organized" or
"imported." Nothing anywhere reads "present" or "missing." It was listed in the
maintenance screen alongside the safe jobs.

**Why it mattered.** The audiobook shelf only shows books marked "organized." Running
this job would have re-labelled every book to a value that filter rejects, and the shelf
would have gone empty. Recovering would have meant a full re-scan. It had never been run,
and the to-do list carried a "do not run" warning, but a warning in a document is not a
control.

**The fix.** The job is deleted, not hidden. Its registration is gone, its name is
removed from the API description, and a test now fails if a job with that name is ever
registered again. A sweep confirmed nothing else in the app produces or consumes those
two status words.

## 13. The merge preview that could not tell "empty" from "failed"

**What it was.** On the duplicate-authors screen, a reviewer can open a preview of each
author's books before deciding to merge two authors. When the lookup behind that preview
failed for any reason, the code treated the failure as an empty result and the preview
said "No books found."

**Why it mattered.** "No books found" reads as "this author record is empty, merging is
harmless." A temporary server error could therefore steer a reviewer into merging an
author who actually had a full shelf of books. The merge itself is reversible, but only
if someone notices.

**The fix.** The preview now tracks failures separately from empty results. A failed
lookup shows "Could not load" with a retry button that actually re-fetches, and "No books
found" appears only when the lookup succeeded and returned nothing. The count shown next
to the merge button comes from the server independently and was never affected.

## 14. The iTunes cleanup that was decided against but still worked

**What it was.** An API call could remove "superseded" tracks from the iTunes library
file. In July a census of every track found nothing that was safe to remove, and the
rule the call used to pick tracks could select real chapter files rather than true
duplicates. The decision then was to keep the measurement and never build removal. The
apply path was left in place anyway.

**Why it mattered.** One direct API call, with no confirmation, could delete entries from
the live iTunes library based on a rule already known to be wrong.

**The fix.** The apply path now refuses with a clear "retired" response before it even
locates the library file, so a missing file cannot turn the refusal into a crash. The
preview mode that counts what would be affected still works. Nothing in the web app
called this endpoint.

## 15. The iTunes writer that did not wait for itself

**What it was.** Changes to the iTunes library are batched and written by a background
worker. On shutdown that worker set a flag and wrote once, but did not wait for the
helpers it had started, and nothing stopped two of its write cycles from running at the
same time.

**Why it mattered.** Two writers on the same file at once is how a library file gets
truncated or half-written. The test that reproduces this shows the exact shape: two
cycles fighting over the same temporary file, one of them failing to rename it into
place. A shutdown mid-write could also drop the last batch of changes.

**The fix.** Shutdown now waits for every helper to finish, refuses new work after that
point, and then performs one final write. Only one write cycle can be inside the file at
a time. The change adds no new writes; it only narrows when they happen. One open
question is left for the owner: the shutdown wait has no time limit, because a limit
would mean dropping the final batch on a slow disk.

## 16. The database upgrade that could run twice

**What it was.** When the app starts and finds its database behind the current version,
it runs each upgrade step and then records two things: that the step was applied, and
that the database is now at that version. Those were two separate writes.

**Why it mattered.** A crash between them left the version number behind, so the next
start re-ran the step. Every step registered today is safe to repeat, so this has not
bitten anyone, but the next real database change would have inherited the hazard and
nothing checked for it.

**The fix.** Both records are now written together in one durable batch. On start, a
step that is already recorded is never re-run; only the version is caught up. And a test
now runs every registered step twice against a fresh database and fails if the second
run changes anything, so a non-repeatable step cannot be added by accident.

## 17. The bulk delete with no preview

**What it was.** The operations screen's API has one bulk delete, for old operation
history records in finished states. Every other destructive action in the app previews
first; this one deleted immediately and reported only a count afterwards.

**Why it mattered.** These are audit records with no undo. An operator had no way to see
how many rows a call would remove before removing them.

**The fix.** A dry-run flag now returns the count by status and removes nothing, and the
real delete reports the same counts alongside what it removed. The default behaviour of
the call is unchanged for existing callers. A per-record delete was considered and not
built; the recommended shape is written up in the pull request for the owner.
