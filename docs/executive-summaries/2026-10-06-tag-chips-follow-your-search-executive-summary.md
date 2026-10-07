<!-- file: docs/executive-summaries/2026-10-06-tag-chips-follow-your-search-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6f2a9d4c-1e8b-4c37-a5d0-3b7e9c1f2a64 -->
<!-- last-edited: 2026-10-06 -->

# Tag chips on the Library page follow your search

PR: not yet opened (branch `feat/scoped-tag-facets`)

## Executive Summary

- **What was wrong.** The "Browse by Tag" panel on the Library page always
  listed every tag in the whole library with whole-library counts, such as
  "language: en (28697)", no matter what you had searched for or filtered.
  It could not help you narrow anything down. Those counts also included
  books in the trash and spare copies of a book that the list never shows.
  Clicking a second tag did nothing, because only the first tag was ever
  sent to the server.
- **What it does now.** The panel shows only the tags found on the books in
  your current results. Each count is the number of those books that carry
  the tag. Clicking a chip adds `tag:"…"` to the search box and narrows the
  list. Each further chip narrows it again. Clicking an active chip removes
  it. If the results carry no tags, the panel says so. If the tags cannot be
  loaded, it says that too, instead of showing tags from a different search.
- **Speed.** The tag counts are worked out from the same results the list
  already found. On a test library of 5,000 books this took between 1 and
  22 milliseconds. The genre and language lists on the same page now read
  from memory instead of from disk, which was the likely cause of that page
  request averaging almost 6 seconds. Repeat requests share one piece of work.
  If you leave the page, that work stops.
- **Also fixed.** Genre and language counts no longer include books in the
  trash.
