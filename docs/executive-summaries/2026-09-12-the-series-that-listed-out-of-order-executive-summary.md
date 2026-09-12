<!-- file: docs/executive-summaries/2026-09-12-the-series-that-listed-out-of-order-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9e2d4c71-5a38-4b0f-8c16-d7a3e5f20b94 -->
<!-- last-edited: 2026-09-12 -->

# The series that listed out of order

PR: feat/series-view-position-sort (series-position sort, series filter chip,
author-books paging stop). Follows the book-detail series link added in #3280.

## Executive Summary

A book's detail page links to "every book in this series". That link opened
the library filtered to the series, but the page had no good way to show it.

- **Books now appear in reading order.** A series view lists book 1, then 1.5,
  then 2, and so on up to 10 and beyond, with unnumbered books at the end. Before
  this, the only series sort compared series names. Every book in one series has
  the same series name, so the order was effectively random.
- **The filter can be seen and removed.** The series filter now shows as a
  labelled chip with the series name and a close button. Before, it narrowed the
  list silently, and the only way to clear it was to edit the address bar.
- **The "Series #" column header sorts again.** Clicking it asked the server for
  an order it did not recognise, so nothing happened. It now uses the new
  reading-order sort.
- **The author list used when merging authors no longer stops early.** It
  fetches an author's books a page at a time. It treated any page smaller than
  requested as the last one, which only worked because the requested size
  happened to equal the server's limit. It now stops when it has as many books
  as the server reported. If it gives up, it reports an error instead of showing
  a shortened list.

## 1. No reading order for a series

**What it was.** The library could sort by series, but only by the series'
name. Inside one series every book ties on that name, so the list came back in
whatever order the database produced.

**Why it mattered.** Opening a series from a book's page is how a reader checks
what comes next. A shuffled list defeats that. It also made paging unreliable:
with nothing to break the tie, a book could appear on two pages or on none.

**The fix.** A new "series position" sort orders books by their number in the
series. It compares the numbers as numbers, so 10 comes after 2, and it keeps
half-steps like 1.5 when the metadata source provided one. Books with no number
go last. Ties are broken by title and then by the book's permanent ID, so every
page of the list is stable. A series view uses this order unless the reader
picks another. A sort the reader picks is remembered in the page address.

## 2. An invisible series filter

**What it was.** The series link filtered the library, but nothing on the page
said so. The filter panel has no series-by-ID control, so there was nothing to
switch off.

**Why it mattered.** A reader could land on a short list, forget how they got
there, and conclude the library was missing books.

**The fix.** The page shows a chip reading "Series: *name*". The name comes from
the series list the page already loads. If the series is unknown the chip reads
"Series #*number*". Closing the chip removes only the series filter; any other
filters and the chosen sort stay as they were.

## 3. An author list that could be cut short

**What it was.** When merging authors, the app shows each author's books so
the user can decide which author to keep. It fetched those books a page at a
time and stopped at the first page that came back smaller than requested.

**Why it mattered.** If the server ever returned smaller pages than requested,
the list would stop after the first page, and the author would appear to have
fewer books than they do. That is exactly the information a merge decision
depends on.

**The fix.** The list now keeps fetching until it has the number of books the
server reported, or the server returns an empty page. Each request starts where
the last one ended, so a short page cannot skip books. If it reaches its
safety limit of 100 pages first, it reports an error rather than returning part
of the list.

Verified with new automated tests. Each one fails against the code before this
change and passes after it.
