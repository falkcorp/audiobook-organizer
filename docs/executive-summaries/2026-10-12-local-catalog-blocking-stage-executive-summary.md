<!-- file: docs/executive-summaries/2026-10-12-local-catalog-blocking-stage-executive-summary.md -->
<!-- version: 1.2.0 -->
<!-- guid: a53f4aca-4029-4768-a139-d539fca84e17 -->
<!-- last-edited: 2026-10-12 -->

# Looking books up in the author catalog before asking Audible

Branch `task/02-pr7b` (holistic plan task 02-PR7b).

## Executive Summary

- **What was missing.** The app already keeps a catalog of every title Audible
  lists for the authors in your library, but finding a book's metadata never
  read it. Every book whose author is in that catalog still cost Audible
  calls, and books whose stored title Audible cannot answer (a title written
  as "Series name, Book 3 - Real title", for example) found nothing at all.
- **The change.** Before asking any provider, the search can now look the book
  up in the catalog: by its own ASIN if it has one, otherwise by each of its
  authors in the order they are credited. The author is found by exact name,
  or by a spelling the catalog filed for them (a missing middle name,
  initials, "Last, First"). The author's titles are compared with every title
  the book is known by, and digits must agree, so book 12 is never offered
  for book 13. Titles Audible no longer lists are skipped. An author whose
  name two different Audible authors share is skipped unless the author lists
  or the catalog already settled which one is meant, and an author with more
  than 2,000 titles (a junk or franchise entry) is skipped and counted.
- **Review only.** What the catalog finds is shown as "Audible Catalog
  (local)" candidates, marked B1 (matched by ASIN) or B2 (matched by author),
  with the position of the author that found it. They are review-only, like
  Open Library and Google Books today: bulk apply never applies one, and only
  you can, by hand. Doctor Who, Big Finish and Torchwood entries are still
  held by the manual-only rule. The calls made to Audible and the other
  providers are exactly the same as without it.
- **Off until you decide.** The setting `metadata_fetch.catalog_stage` ships
  off. Read the catalog coverage report first, then turn it on with
  `PUT /api/v1/config` `{"metadata_fetch":{"catalog_stage":true}}`. Turning
  it off again takes effect at once.
- **What you will see.** The candidate fetch's progress and final lines now end
  with how many candidates came from the catalog (no provider call) and how
  many from providers, and how many authors were skipped as too large or
  shared. They count that run's searches only.
- **Measured.** In a made-up library of 5,000 books against 800 authors of 100
  titles each (plus 3 over-large authors and 5 authors sharing a name), the
  provider calls were the same with and without the stage (8,400), and 796
  books gained a catalog candidate they could not have had before. That mix is
  fixed by how the test library was built, so it shows the stage works, not
  what your library will get.
- **A side effect to know about.** A book whose only candidate comes from the
  catalog now counts as having a usable candidate, so Open Library and Google
  Books are not asked about it. If you would rather they still were, that is a
  decision for you (filed in the TODO list).
- **A separate change, with its own switch.** A book whose stored title is just
  a person's name, or is its own author, is now searched by the name of the
  folder holding its files when that folder names a real title (the roughly
  1,270 books in that group). It is a new question for those books, so the
  next candidate fetch asks the providers again for them. The setting
  `metadata_fetch.folder_parse_person_title` ships on; set to `false` it asks
  the stored title again. What that search finds cannot be applied by the
  review page's bulk button or any unattended apply, as for any book searched
  by its folder, but you can apply it from that book's own review row, one
  book at a time (a request that pairs a single-row approval with other books
  is refused). Only this kind of folder search gets that exception, and only
  while the switch is on; the folder search for a book with no title at all is
  still never applied that way. The dry-run report marks these rows so they are
  not mistaken for ones a bulk apply would land.
- **Safety.** If every provider fails while the catalog has a match, the search
  still counts as unanswered and the book keeps what it had. A Doctor Who, Big
  Finish or Torchwood entry from the catalog is held by the manual-only rule
  wherever it was flagged.
- **Housekeeping.** The catalog harvest now files the alternate spellings of
  every author it stores, and at the end of every run files them for what it
  stored before. A book you fill from a catalog candidate is recorded as an
  "Audible catalog" book, ranked just below Audible: a hand-picked or
  review-page apply from live Audible can replace it, but the automatic
  upgrade sweep does not revisit it. Its year is read as the audiobook's
  release year.
