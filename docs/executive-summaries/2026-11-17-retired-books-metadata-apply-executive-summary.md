<!-- file: docs/executive-summaries/2026-11-17-retired-books-metadata-apply-executive-summary.md -->
<!-- version: 1.3.0 -->
<!-- guid: 2e8c5a71-4b93-4d06-9f1a-7c3d0b6e8a52 -->
<!-- last-edited: 2026-11-17 -->

# Automatic metadata changes no longer land on books that have been put away

**Date:** 17 November 2026. One pull request, `fix(metadata): unattended apply
refuses retired books`.

## Executive Summary

- A book can be put away in two ways: moved to the trash, or merged into
  another book (its copy becomes a hidden shell that points at the survivor).
  Either way it is no longer a book anyone is working on.
- Until now, the automatic ways of filling in a book's details did not check for
  that. The bulk apply from the review page, the apply that follows a metadata
  search, the nightly upgrade to a better source and the transcript-based
  matcher could all write a title, description or cover onto a book that had
  already been put away. A test run over 4,000 review-page entries found 30
  where the safety check said "go ahead" for a book that was in the trash or
  merged away.
- Those writes are now refused, and the refusal is reported like the others
  ("book retired") in the dry run, the apply summary and the per-book results.
  Nothing is written. In the jobs named in this summary, a retired book is
  reported as skipped, with its own reason, rather than as a failed apply. A
  book put away while a job is already under way (after the job read it,
  before it wrote) is caught at the write and reported the same way; the
  version-twin repair reports it with its own outcome, "changed since it was
  planned", which is not a failure either.
- Looking up a book's details costs a lookup against an outside service that
  has a daily limit, and retired books were being looked up and then refused
  anyway. The lists that feed the lookups now leave them out and say how many
  they left out: the bulk metadata fetch (both the chosen-books and the
  whole-library forms), the review page's batch fetch, the stale refetch, the scheduled unfetched run,
  the transcript-based matcher, the nightly upgrade, the lost-candidates
  repair and the production-company author resolver (its dry run no longer
  counts them either). A book that is put away while a run is already
  searching it can still be looked up once; its result is not written.
- The automatic Audible-id backfill checks again under the write lock, so a
  book put away during its Audible request is left alone and counted as such.
- Three more automatic writers now check again at the moment of writing, so a
  book put away a moment earlier is left alone: the jobs that fill in a
  production company as a book's author or publisher, the copy of a book's
  details that is kept on its library copy after every metadata apply (a copy
  that was trashed or merged away is refused and the file is not rewritten),
  and the iTunes import's author fill.
- The lost-candidates repair now reports a book that was put away between its
  re-check and its re-search as "changed since it was planned", like the
  version-twin repair, instead of as a failure. "Search again" now says so when
  some books were left out as put away and others because they were already
  being searched.
- The job that advances a book through identification used to log a retired
  book as "marked no match". It now records its own reason, "retired", so a
  book that was only put away is not described as one with no match.
- "Search again" on several selected books now says how many books were
  searched and how many retired ones were left out, instead of the number
  selected. A selection that is all retired starts nothing and says why.
- What did not change: you can still pick a match for one book yourself, on any
  book, including one in the trash; approving a single row on the review page
  still applies it; and "Search again" on one book still searches it. The
  search job itself now refuses that one-book mode if it is handed more than
  one book, so no other caller can switch it on for a whole list.
- The one-click "fetch metadata" function is covered as well. Other automatic
  jobs call it too (organize's fetch-first step, the iTunes import, the
  production-company author resolvers), so every place that calls it now says
  whether a person asked for that fetch or a job did. A job's fetch of a book
  that has been put away is refused and reported as a skip, not a failure, and
  does not stop the run. The single-book button is unchanged.
- Left for follow-ups, filed in the to-do list: the review page still lists
  retired books, about nineteen other places in the code each spell out
  "retired" their own way, and a second change will make the store itself refuse
  to write onto a put-away book, which closes the remaining jobs (fourteen are
  listed by name) that do not check at all. One known gap stays: a single-book
  apply a person queued behind a library scan still goes through if the book
  was trashed or merged while it waited.
