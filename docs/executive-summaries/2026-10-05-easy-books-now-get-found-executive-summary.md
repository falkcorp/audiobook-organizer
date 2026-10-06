<!-- file: docs/executive-summaries/2026-10-05-easy-books-now-get-found-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: b351a976-9287-4897-af9a-3525619265bc -->
<!-- last-edited: 2026-10-05 -->

# Easy books now get found

## Executive Summary

- **Books with file-style names were never found.** When a book's title
  still looked like a file name -- "2018 - Blueshift", "Discworld 24 - The
  Fifth Elephant - 01" -- the metadata
  search sent that text to Audible as written. Audible has the book, but not
  under a name with a year, a track number or the author glued on, so the
  search came back empty. About 1,150 books are in this shape.
- **The search now reads the name out first.** One shared reader now
  removes the year, the author, the "Unknown Author" placeholder, the track
  number and recording details, and spots the series and book number. The
  library scanner uses the same reader, so both sides agree on what a
  book's name is.
- **Checked against the real catalog.** On the 20 sample books the
  October census picked, the old search found 0. The new one finds 10 --
  all 10 the census could find in Audible's catalog. The other 10 are not
  there under that title, carry the wrong author, or are named only by a
  series and number.
- **A person's name is no longer searched as a title.** A book whose title
  is just its author's name now falls back to the title heard in its audio
  or its folder name. A biography named after its subject is still
  searched. A book filed with its own title as the "author" is searched
  without that fake author, and then only an exact, unambiguous match is
  kept.
- **New books get searched on their own.** Until now the search ran only
  when someone started it by hand, so 613 books added later were never
  searched. A new scheduled job (every 6 hours, on by default) searches
  books that have never been searched, or whose earlier results were
  cleared. It only collects suggestions for review; it never changes a
  book.
- **Not yet addressed.** Results are suggestions only -- nothing is applied
  automatically. Books named only by a series and number ("Some Series 03")
  still need series-and-number matching.

## Titles that look like file names

**What it was.** The search used the stored title as written whenever it
looked like a real title. File-style titles passed that check.

**Why it mattered.** Audible only finds a book when the name matches, so
roughly a thousand easy books stayed unmatched.

**The fix.** The year, author, placeholder, track number and recording
details are removed before searching, and the original text is still tried
as a backup. Two scoring rules that misread the track number as the book's
number in its series were corrected too.

## Searching new books automatically

**What it was.** Nothing started a search for books added after the last
manual run.

**The fix.** A scheduled job finds books with no search results on record
and searches them at the providers' normal speed limits. It writes only
suggestions for the review page.
