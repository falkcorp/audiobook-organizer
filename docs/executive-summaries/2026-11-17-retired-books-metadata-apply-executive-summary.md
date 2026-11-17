<!-- file: docs/executive-summaries/2026-11-17-retired-books-metadata-apply-executive-summary.md -->
<!-- version: 1.0.0 -->
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
  Nothing is written, and nothing is counted as a failure.
- The searches stop too. Looking up a book's details costs a lookup against an
  outside service that has a daily limit; retired books were being looked up
  and then refused anyway. The lists that feed the search now leave them out
  and say how many they left out.
- What did not change: you can still pick a match for one book yourself, on any
  book, including one in the trash; approving a single row on the review page
  still applies it; and "Search again" on one book still searches it.
- Left for follow-ups, filed in the to-do list: the one-click "fetch metadata"
  function that other automatic jobs also call does not check yet (it is shared
  with the single-book button, so it needs a decision), the review page still
  lists retired books, and about twenty other places in the code each spell out
  "retired" their own way.
