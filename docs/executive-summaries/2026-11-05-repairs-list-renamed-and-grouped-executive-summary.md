<!-- file: docs/executive-summaries/2026-11-05-repairs-list-renamed-and-grouped-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7d41c9e2-5b86-4f03-a1d7-92e8c3b60f54 -->
<!-- last-edited: 2026-11-05 -->

# The Repairs list now reads in plain words, in groups, and no longer offers jobs that cannot work

**Date:** 5 November 2026

## Executive Summary

- Every repair job in the Repairs list now has a plain name of the form "what it
  does, then what it fixes", such as "Merge chapter files into their book" or
  "Fix junk titles". The old names were nicknames that only made sense to the
  people who wrote the jobs.
- The 22 jobs are now sorted under eight headings, most-used first: Duplicates
  and split books, Titles, Authors, Series, Metadata and tags, Files and iTunes
  paths, Versions, and Imports and one-shot tools. The order is fixed, so the
  list no longer shifts around after a trial.
- Each job shows a one-line summary in the list. The long explanation, with its
  safety warnings, is still there when the job is opened.
- Three jobs that had stopped finding anything to fix were retired by the
  owner's decision, after a fresh trial of each: a title fix for books whose
  number was typed as the letter "l", a repair for books that lost their series
  link, and a job that merged books split into one record per chapter.
- Three jobs that can only be started from the API now say so with an "API
  only" badge and a switched-off Run trial button that explains why. Before,
  the button was there and could only fail.
- Nothing in the library's history was rewritten. Past runs of the retired jobs
  stay in the history, and an old bookmark to one still opens the page with a
  note saying the job is retired.

## The names were nicknames

**What it was.** The list showed names like "Stale series objects",
"Letter-l ordinals" and "Chapter fragments", written for whoever built each job.
Some names carried a state ("Retired versions (preview)"), and some job
descriptions pointed to another job by its internal id instead of its name.

**Why it mattered.** The owner decides what to apply from this list. A name that
has to be decoded makes the wrong job easy to pick, or makes a safe job look
risky and gets it skipped.

**The fix.** Every job got a name that follows one rule: a verb from a short
fixed list, then what it fixes, in about 45 characters or fewer, with no
brackets, ids or jargon. "Preview only" and "API only" are now badges next to
the name, not part of it. Job ids are unchanged, so saved links, stored plans
and history keep working. Anywhere one job's text told the owner to use another
job, it now quotes that job's new name.

## One flat list, in no useful order

**What it was.** The jobs came out in one long list, in the order they happened
to be registered.

**Why it mattered.** The jobs most people open most often (merging duplicates,
fixing titles and authors) sat among one-off tools that were run once during a
past clean-up. Finding the right one meant reading all of them.

**The fix.** Jobs are grouped under eight headings, with the most-used group
first and the one-shot tools last. Inside a group the order is fixed by a rank
on the job itself, not by sorting in the browser. For example, "Fix swapped
title and author" is listed before "Fix junk titles". The server
sends the groups, ranks and summaries, and the page draws them in the order it
receives. A new job cannot be added without a group, a rank and a summary.

## Three jobs offered a button that could not work

**What it was.** The Run trial button starts a trial with no settings. Three
jobs cannot be tried that way, because they need information the page has no way
to collect: "Revert titles a scan rewrote" needs an exact list of books,
"Import read status from Audible" needs an exported file and the user it is for,
and "Import read status from a master list" needs a user and a list.

**Why it mattered.** Pressing the button could only fail, which looks like a
broken page.

**The fix.** Each of these now shows an "API only" badge and a switched-off
button. Hovering over it says what the job needs. The server also refuses a stray
request to start one, with the same reason. Their earlier plans can still be
read and applied. The master-list import is the judgment call: a settings form
on the page could make it startable, but that is a new feature rather than a fix
to this bug, so it was left for later.

## Three jobs retired

**What it was.** The owner ran a fresh trial of three jobs. The letter-"l" title
fix found 0 rows. The series-link repair found 3 rows, none of them safe to
apply, and the database already repairs those rows the next time a book is
saved. The job for books split into one record per chapter could apply 0 of 239
clusters, and "Merge chapter files into their book" already covers that work.

**Why it mattered.** A job that finds nothing still costs attention. It sits in
the list, it has to be maintained, and its old instructions kept pointing the
owner to other jobs that no longer helped.

**The fix.** The three jobs, their tests and every mention of them in the page
were removed, along with a service that only the chapter-split job used. The old
"Split Books" address now opens "Merge chapter files into their book". A skipped
book that used to say "run the series relink job first" now says its series must
be chosen by hand, and it is still held until then. Everything the retired jobs
already did stays as it was, and the undo history for it still works. The saved
"last run" markers for the three jobs were left in place, since nothing reads
them any more.

## How it was checked

The list order, the groups, the naming rule and the API-only rules are covered by
automated tests on both the server and the page, using made-up Batman-themed
books. A test also checks that a retired job's address is refused cleanly rather
than crashing, and that the page labels such a link as retired.
