<!-- file: docs/executive-summaries/2026-08-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8b5837bb-9743-41d0-b054-66fb7e84f636 -->
<!-- last-edited: 2026-09-12 -->

# August 2026 — Monthly Executive Summary

This one report replaces the forty separate write-ups made during August and the
end-of-month roundup. It is grouped by theme rather than by date. Each section says what
was wrong, why it mattered to someone using the library, and what changed. Where an
earlier write-up gave a number that later turned out to be wrong, both numbers are kept
and the correction is stated: owning a mistake on the record was part of the month's work.

A few terms used throughout:

- **The library** is the audiobook collection the organiser manages: about 67,800 book
  records and roughly 16,500 books visible on the main shelves.
- **The mobile app** is the listening app that connects to the organiser as if it were
  its own server.
- **A background job** is a task the organiser runs on its own, such as a scan or a
  nightly clean-up.
- **The safety net** is the set of automated tests that click through the web interface
  the way a person would, in two different browsers.
- **Preview mode** (also called a "dry run") means a job reports what it *would* change
  without changing anything.

---

## Month at a glance

- **Numbers the library showed were wrong, and many are now right.** About 26,000 books
  showed running times that were far too long or far too short. About 31,000 books were
  wrongly marked "transcription failed". Nearly 7,000 series vanished from under 13,322
  books. About 16,700 books could not be found by search. Each was traced to its cause and
  either repaired or deliberately left visible for a person to decide.
- **Chapters went from zero to roughly one million.** Before 13 August no book in the
  library had real chapter markers. By the end of that day 24,647 books did, with about
  1,030,000 chapters between them.
- **Controls that appeared to work were doing nothing, or doing the wrong thing.** Among
  them: sidebar buttons, 10 of the 23 sort options, the "organise after import" checkbox,
  and actions on selected books that ran on the whole library. Worst of all, 18 background
  jobs advertised a harmless preview by default and then made real changes.
- **Several things that could destroy data were stopped before, or as, they did.** A
  backup routine filled the disk and crashed the database every 17 minutes and corrupted
  two saved passwords. A proposed repair would have deleted the only record of which files
  were missing. And series and author tidy-ups could delete records that thousands of
  books still pointed at.
- **The safety net had been partly switched off for four months.** Once it was repaired,
  146 of 288 tests were failing, and 11 of those failures were real faults in the product.
  By 9 August all 552 tests passed in both browsers, and a change that breaks one can no
  longer be merged.
- **Search was rebuilt around what people actually type.** Quoted phrases, wildcards,
  capital letters and the second page of results all work now. Thirteen search terms that
  always answered "nothing found" now either work or say plainly that they are unknown.
- **The database had grown to 30 GB, mostly out of sight.** A version history had grown
  without limit. Deleting records freed no space. Activity-log signposts filled 60% of a
  1.34 GB log. The measurements are done, a 35-fold reduction is designed, and a burndown
  list of 71 items had 15 closed by month end.
- **Silent failure was the theme of the month.** Jobs reported success on zero items. A
  check that existed was never run. A test could be passed by code that did nothing. Many
  fixes this month were about making failure loud rather than about the failure itself.

---

## 1. Numbers the library showed were wrong

### Running times (4 August)

**What it was.** About 26,000 books showed impossible running times. Examples included 158
hours for a 12-hour book, 294 hours for another, and 4 minutes for a 78-minute book. There
were two causes. First, one part of the organiser measured time in seconds and another in
thousandths of a second, and the two were mixed up by a factor of 1,000. Second, the same
audio file had often been recorded against its book more than once, so it was counted
repeatedly. One file had 130 copies. The 294-hour book was a single 21,877-second file
counted 26 times.

**Why it mattered.** Running time is one of the main ways a listener judges a book, and
one of the main signals the organiser uses to decide whether two copies are the same
book. Wrong times hurt both.

**The fix.**

- **Duplicate records removed.** 3,239 duplicate file records across 204 books were
  removed. A final scan found 314,153 records and 0 duplicates.
  - Getting there took three attempts. The first was stopped by a safety watchdog after 19
    books. The second hit its two-hour limit at 78 of 176. A parallel version then finished
    95 books in 9.5 minutes.
  - One stub book, a 13.5-second, 91 KB file, correctly reads 0.00 hours.
- **Seconds-versus-thousandths repair.** This covered 214 books: 1,384 values were
  corrected and 9,352 were correctly left alone. An earlier estimate of about 6,000 bad
  values was four times too high.
  - The update path now converts values as they arrive, so a book showing 19,294 hours now
    shows 9.90, and one showing 15,557 hours now shows 8.05.
- **Related count fixes.**
  - The Authors tab had been built from 44,888 records instead of the 16,491 visible
    books.
  - The Narrators tab read only one of its three sources.
  - A match based on listening to the audio now applies only the title and author, not
    everything else it guessed.

### Series that were really book numbers (6 August)

**What it was.** Some series names were really a book's number within a series, stored in
the wrong field.

**The fix.**

- 25 fragmented series were folded into 21, 52 books gained their correct number, and 664
  were left for a person to review.
- The work separated clear cases (keywords, brackets, bare numbers) from real series that
  only look like numbers. "86—EIGHTY-SIX" is a real series of 17 books.
- The 198-item bracket group turned out to be mostly *shattered* books: a single novel had
  been split into many rows. One O'Keefe novel was split into 80 rows and one Santopolo
  novel into 25, and 63 rows were page titles from a download site. About 180 books need
  to be reassembled.

### The outage that marked the library broken (7 August)

**What it was.** On 1 July the transcription service was briefly unreachable. Every book
the organiser tried to transcribe during that window was marked "transcription failed".
That came to about 31,000 books, three quarters of the library.

**Why it mattered.** A book marked failed is skipped from then on, so the outage would
have become permanent.

**The fix.**

- A repair re-checked 44,877 books and corrected 30,820. It left alone the 1,463 real
  failures and 7 books that are genuinely silent.
- 33,633 books gained transcripts recorded per file.
- A connection failure now records nothing, instead of recording "failed".
- Title-reading fixes landed at the same time, covering "Chapter N" titles, translators
  and cover artists. Corrupted fields across 346 test recordings fell from 103 to 0, and
  12,990 titles were corrected.
- The re-read tool now defaults to preview.
- Transcription is now spread across several machines. Not yet built: automatically
  handing back work from a machine that disappears mid-job.

### Authors that began with "&" (14 and 17 August)

**What it was.** A book credited to two people was sometimes split badly, so it created an
author called, for example, "& India Fisher". There were 46 such entries across 145 books,
out of about 9,350 authors.

**The fix.**

- 31 were merged into the correctly named author who already existed, and 15 were renamed.
  No data was lost.
- Title fragments stored as author names (such as "and Thanks for All the Fish") and
  leftover copyright text were deliberately left visibly broken. Guessing a repair for
  them could make things worse.
- **Follow-up (17 August).** A trailing comma had created twin authors: "Alan Barnes," with
  14 books and "Alan Barnes" with 1. This affected 11 names, and 8 need merging. The
  clean-up trims commas but not the periods or hyphens that belong to real names, such as
  "Jr.", "E. E. Knight" and "Hill-Knight".
- **Empty authors.** 4,975 of 12,854 authors have no books at all. A purge job now reports
  what it would remove before removing anything. The narrator equivalent is not yet
  counted, and an earlier figure of 922 was wrong.

### The name that meant "we don't know" (25 August)

**What it was.** When a filename said "Unknown Author", the scan stored that as a real
author. The feature that repairs missing authors then skipped those books, because they
appeared to have one.

**Scale.** 3,407 books are affected. An earlier figure of 25,304 was withdrawn. Separately,
80 books store "Terry Pratchett" in a different field from the author field.

**The fix.**

- Both copies of the filename-reading logic now treat "Unknown Author" as blank, and the
  phrase is defined in exactly one place.
- Books already in the library are not repaired yet. They are only looked at again when
  their files change or during a full rescan.
- The machine that looks up authors was offline at the time.

---

## 2. Controls and screens that told the user something untrue

### "No Audiobooks Found", dead buttons and empty release notes (8 August)

- **"No Audiobooks Found" after every restart.** For about 40 seconds after every restart
  the library page said it had no books, while the server was still loading. A failed
  request now keeps the books already on screen and quietly retries.
- **Dead sidebar buttons.** The In Progress and Finished buttons did nothing. Two faults
  caused this: a highlight comparison that never matched, and a switch that ignored its own
  echo but got stuck on. Both are fixed, and browser tests now cover them.
- **Empty release notes.** Release notes said "No commits available". Version 0.218.0 was
  published with 484 changes (142 fixes and 95 new capabilities) after 87 unreleased
  candidates had piled up. The rule is now at most 10 candidates.
  - **25 August:** 271 candidates and 955 version tags were cleared down to 13, and version
    0.219.2 was published with 4 builds.

### The invisible sheet (10–11 August)

**What it was.** After a dropdown or filter panel closed, a transparent layer sometimes
stayed on the page and blocked every click.

**Why it was hard.** How often it happened depended on the closing animation. It failed 0
times in 20 at normal speed, 8 in 20 at a quarter-second, and 20 in 20 with the animation
removed. The first fix only made it rarer: under a different sequence of clicks, the old
code still failed 9 or 10 times in 10.

**The fix.** The panel now closes immediately rather than waiting for the animation, and
passes 10 out of 10. The underlying cause inside the screen-building library is still
unknown.

### Instructions that were thrown away (11 August)

**What it was.** Scanning, organising or converting *selected* books quietly ran on the
whole library, because the list of chosen books was lost on the way to the server. Convert
was visibly broken.

**The fix.** A one-line correction to how the request is packed. Unpacking now fails loudly
in 13 places, where before it silently fell back to "everything". It is not confirmed that
this was the only cause on the live server, and the fix does not undo past runs.

### The mobile app's requests that were answered anyway (13 August)

**What it was.** The server accepted the mobile app's requests but ignored parts of them.

- A series filter was ignored: it returned 34,280 results and now returns the correct 2.
- Every one of the 14,625 series showed an empty book list. They are now filled.
- The requested page size was ignored. One reply shrank from 3.4 MB to 2 KB.
- Playlists were redirected somewhere unreadable. They now open, one with 77 items.

A slip during this work broke six playlist actions, and it was fixed the same day. The 28
recordings used to test mobile-app behaviour contained no filter or page-size
instructions, which is how the gap went unnoticed. Author and series detail pages were
still missing at month end.

### The preview button that was not a preview (14 August)

**What it was.** 18 of 34 background jobs advertised preview as their default, but the
server made real changes whenever a request did not say otherwise. After a restart, jobs
resuming mid-way guessed "real" too. Seven jobs were affected by that, and one of them
deletes folders.

**Why it mattered.** This was found while looking at 6,245 single-book series, of which
2,322 are genuinely distinct. Only about 6% of the lost names can be recovered, which shows
what a "preview" that really applies can cost.

**The fix.** Every job now uses its own published default, and remembers the choice across
a restart. A test reads every job rather than a sample. The duplicate-series routine still
has no preview option. It was run 0 times against its 10,161 pending operations.

### Controls that did nothing (29 August)

- A "force" option was dropped before it reached the job.
- The setting for how many past versions to keep always kept 10. A value of zero now falls
  back to the default instead.
- The book-similarity index was thrown away on every start and rebuilt, which took about 2
  minutes. Its two counts disagreed: 17,706 against 39,658.
- The same broken settings path affected four more jobs. One of them, a job that reverts
  fetched book information, could not work at all.
- About 22,000 index records that were skipped went unreported.

### Rescan, sorting, the filter menu and the import checkbox (24–25 August)

- **Rescan.** The button called "Rescan" only rechecked file sizes, so it is now named
  "Reconcile File Sizes". Two new buttons were added:
  - "Force Rescan" rescans one book. Before, a change could take up to 6 hours to show.
  - "Rescan Whole Folder" rescans a folder. One folder holds 1,458 files.

  The scan now reports what share of files it skipped and why, sorted into five reasons.
- **Sorting.** 10 of the 23 sort options, and 3 alternative names for them, did not sort
  at all. All 23 now work. The old tests only checked that sorting twice gave the same
  answer and lost no books, and a sort that does nothing passes both. A deliberately
  planted fault was missed at first; the test that now catches it was added.
- **The filter menu.**
  - 4,975 of 12,854 authors (38.7%) had no visible book, so choosing them showed an empty
    shelf. The menu now uses the same list as the tabs.
  - The sort order on the Authors tab was ignored in both directions; it is fixed.
  - The menu took more than 7 seconds to build on every page load and is now kept for 5
    minutes.
  - An earlier note called a pile-up of simultaneous rebuilds harmless. That was wrong, and
    the pile-up is fixed.
  - The series list has not been measured.
- **Import.** "Organise after import" was ignored. Imported books were also missing the
  link to their file that organising needs. Both are fixed, and the box is now unticked by
  default.

### Collections (roundup)

The mobile app has a "collections" button, but the server never supported it. On 16
August it was tried five times in two seconds and failed every time, while the list of
collections politely showed as empty.

Collections now exist in two forms:

- **Hand-picked:** a shelf you add books to yourself.
- **Rule-based:** a saved search that fills itself.

They are shared across the whole server. Editing them needs administrator rights or a new
"manage collections" permission. A rule-based collection updates when opened in the web
interface. Nothing refreshes it in the background yet.

Two defects were caught before release. The first was an overly broad change that would
have broken the web features it was meant to support. The second was that simply viewing a
rule-based collection rewrote it every time.

---

## 3. Checks that could not fail

### The safety net that had stopped catching (8–9 August)

**What it was.** A set-up mistake had silenced six test files for about four months.

**The correction.**

- On 8 August, 43 failures were fixed, 24 of 34 of them caused by a fake server wrapper,
  and the suite was declared trustworthy. That claim was withdrawn the next day.
- The real state was 146 of 288 tests failing.
- All 146 were then fixed. Eleven were genuine product faults:
  - the "deleted" filter was broken and the genre box was empty (both fixed);
  - there was no sort control;
  - search dropped the active filters and fired on every keystroke;
  - seven others. An initial claim of an "author crash" was later corrected.

**The fix.**

- The build server showed 179 failures that never appeared on a developer's machine. The
  causes were images stored outside the normal download, contention for the processor,
  and timeouts. All were fixed.
- The result is 552 passing and 0 failing across both browsers, with 7 tests marked as
  expected to fail. The roundup also quotes 544 tests overall, and 278 as of 10 August.
- Since 9 August a failing browser test blocks a change from merging.
- Search now keeps its filters and waits for typing to pause, and two of the
  expected-to-fail tests now pass.

### Checking our own homework (12 August)

**The fixes.**

- The server had said "yes" to features it did not support. It now says no.
- Two descriptions of the server's interface were merged into one, which showed that 48
  described features do not exist.
- A script reported success after processing zero items.
- A clean-up job that seemed idle had in fact run, clearing 7,891 items. Its backlog then
  refilled from about 1,300 to about 6,000 in three and a half weeks.

**Six wrong claims caught the same day.**

- Nine places that write data were missed.
- 46 working functions were switched off and then restored.
- User management and password reset were missed, because six groups of addresses are
  assembled only at start-up.
- A "not allowed" harm that was claimed turned out not to be real.
- A track-number rule was misread.
- A test failure blamed on bad luck had a real cause.

**Comparing against the real mobile app.** The comparison suite now checks values, not
just shapes. It found five issues:

- Listening position drifted by 2.2 seconds across 6 parts, and by about 10 seconds
  across 20.
- The listening-sessions page ignored its page size. Fixed: 10 per page, tested with 12
  sessions.
- The device type is unknown when the app does not say. This is blocked, because the
  recordings lack that information.
- Bit rate was rounded to thousands.
- A year of "800BC" came back as "800".

All 28 recordings are now compared, including 4 that had never been read, and a tolerance
limit caught a 4-minute chapter error. A licence-policy check was proven with a throwaway
change (4 minutes). Four decisions were left with the owner. None of this was deployed
at the time.

### Checks that existed but never ran (17–20 August)

- **17 August.** A check was deleted because nothing could make it fail. The 37
  maintenance jobs are now each set up on their own terms, since 4 needed different
  settings. The set of database actions one area could reach was cut from 398 to 187.
- **18 August.** Oversized "parts lists" that each component had to accept were cut from
  28 to 5. The largest, with 182 entries, was not used at all. One 44-item component was
  left as it is, and a guard now caps new ones at 5.
- **20 August.**
  - A guard on the plug-in toolkit had been failing for 33 days without anyone seeing it.
    It was rebuilt with an approved list plus a snapshot.
  - Speed-measurement tools had not built for four months. The fix was 4 lines.
  - Both now run on the build server, though neither is yet required to pass.
  - The code formatter had drifted in 43 files across 24 areas.
- **Duplicate-detection reasons (20 August).**
  - Reasons were filled in for 18,311 possible duplicate pairs: 1,469 certain, 16,582
    medium and 0 high.
  - No "certain" pair rests on a title match alone, and 11 are eligible for automatic
    merging.
  - Reasons to *doubt* a match are not yet recorded.
- **Settings (20 August).** 565 options were inventoried, and 25 places that read settings
  directly were moved onto the shared settings system. Eleven tests were fixed, four dead
  example entries were removed, and the setting that overrides the AI service's address now
  reaches everywhere it should.
- **The automatic-merge guard.** It had never fired for any pair. It is now worked out
  fresh each time and covered by two new tests. Automatic merging remains off. A
  quality-gate fix raised three warnings, one of which exposed logic that had been copied
  by hand.

### Untangling the wiring (19 August)

**What it was.** One connection point between the organiser's parts had 398 separate
plugs. A 24,613-line imitation of it, used in tests, was deleted.

**What turned up.** Four features had never been connected:

- fast storage for activity;
- resetting audio fingerprints, now about 100 times faster;
- three services that answered "unsupported";
- a start-up warm-up step that was sometimes skipped.

**Where it ended.** Ten wide users remain: six on purpose, three test helpers, and one
missing-file task. Everything else was relabelled into six bundles. The checker crashed
the first time it found nothing wrong, and that was fixed.

### Author-similarity check (24 August)

- Groupings of similar author names came out differently from run to run. They are now
  consistent.
- The check makes 26.4 million comparisons across 7,261 surnames. A quick length test
  removes 61% of them, and the rest run in parallel. The time fell from 4.6 seconds to 0.5
  seconds, about 9 times faster.
- A July review document was corrected.

---

## 4. Search

- **The second page that was never there (12 August).** Filters were applied *after*
  results were split into pages, so later pages came up short or empty. A search for
  "honour" gave pages of 1, 0 and 0 results. It now gives 5, 1 and 0, with a total of 6.
  Totals above 10,000 are approximate.
- **The books search could not see (13 August).** 16,738 books, about a quarter of the
  library, were missing from the search index because a rebuild had been interrupted. The
  organiser now compares counts at start-up and queues whatever is missing. Three places
  that silently fell back to "no search" now say so in the log.
- **When quotes did not mean quotes (13 August).**
  - A capitalised wildcard found nothing. "Hyperion*" went from 0 results to 21, and
    "Dragon*" from 0 to 1,757.
  - Quotation marks were ignored, and small common words were dropped, so "All Jobs" went
    from 300 results to 3.
  - The index was rebuilt: 67,824 books in about 36 minutes, with 0 failures. The rebuild
    survived a redeploy part-way through and kept the 3,497 books it had already done.
  - The "close match" operator has the same capital-letter fault, and exact-match fields
    are still split into words.
- **Thirteen search terms that always said no (14 August).** "duration:1" returned 0
  results while the equivalent longer term returned 25,090. Unknown terms now give an error
  instead of an empty result. "year:" checks both of the year fields a book can have, and a
  test compares the lists of supported terms on both sides.
- **The iTunes write-back preview** included 3,953 deleted books. It now leaves them out.
- **Design.** A search design was written up. Sorting will move from the browser to the
  server.

---

## 5. Deleted books, series and merges

### Deleted but not gone (13 August)

- **Display copies.** Groups of copies of a book each need one copy shown on the shelf.
  479 groups were repaired. Groups with no display copy fell from 490 to 11, and books
  trapped out of sight fell from 737 to 12, all of them in the trash.
- **The 14 TB "disk emergency" was not one.** About 21.8 TB was already shared between
  copies. A 50-file test freed nothing. Making copies fully separate would have freed only
  5.4 GB.
- **Trashed books were still being processed.** 3,953 of them were affected. One job
  brought deleted books back, and duplicate clean-up could keep the deleted copy and delete
  the live one. The clean-up of orphaned files now protects trashed books.
- **A count correction.** The number of hidden books, first taken from an unreliable
  search, was 6,157. The true figure is 724.

### The series that vanished from under 13,322 books (14 August)

**What it was.** A weekly job decided whether a series was in use by reading a badge
counter instead of counting the books.

**Scale.** Books referred to 21,190 series, but only 14,626 existed. 6,893 were missing,
still pointed at by 13,322 books plus 702 in the trash. By 11 August, 5,367 books carried
copies of references that were already broken. The last new one appeared on 19 July.

**The fix.**

- 17 series were merged and 326 genuinely empty ones deleted.
- "Queen of Fire", whose only book is in the trash, was kept.
- The list of series had been a day out of date. It is now refreshed immediately.
- The lost names cannot be recovered. The books' references were left intact so that
  nothing more is lost.

### The copies the merge left behind (23–24 August)

**What it was.** Merging books or authors dealt only with the main copy.

- Alternate copies were stranded, and author merges did not move the credits on those
  copies.
- The merge button on the review screen, and a guard against renumbering, could never
  fire.
- A tidy-up step refused forever.
- If moving books failed, the series was deleted anyway.
- A change that widened the file list had to be reverted.
- The scheduled job had never run: 0 of its 10,161 operations.

All of these were fixed across three changes (#2821, #2825, #2826).

### The guard that was never on duty (23 August)

**What it was.** Before deleting a record, a guard checks that nothing still points at it.
It read an in-memory copy that could be incomplete, and then reported zero references.

**The fix.**

- The guard now falls back to the disk when the in-memory copy cannot be trusted.
- Three faults on the disk path were fixed, including one that ignored any identifier not
  starting with a digit.
- A filter bug at start-up had raised a false alarm on every restart. That is fixed.
- Authors and co-authors are now covered, 4,975 candidates in all. The feature that splits
  a combined author was found to create records that are not acceptable.

It is not known whether anything was deleted before the fix.

### The list the merge trusted (24 August)

**What it was.** Merges trusted a fast index that could be incomplete.

**The fix.**

- A merge now checks that the index is complete, and falls back to the disk if it is not.
  Seven merge paths are covered. Ordinary lists still use the fast index.
- The first version of this fix had holes: it missed records it could not read and a
  boundary in the key range. Those were closed.
- Ten tests were added. They caught all eight deliberately planted faults.

### Two more series deleters (30 August)

Another series deleter now takes a full count first, refuses to delete a series that
still has books it has not moved, and reports how many it really merged. Before, it always
said "merged 3". The roughly 6,893 existing broken references are not repaired, and two
more deleters with the same weakness remain.

---

## 6. Files, disk space and chapters

### One chapter, twenty-four hours (13 August)

**What it was.** No book had real chapters. 500 sampled books all had exactly one chapter
per file. 213 long single-file books each had a single chapter covering the whole book.

**The fix.**

- 24,647 books gained real chapters, about 1,030,000 in all. The final pass alone added
  888,732 chapters across 21,231 books, broken down as 9,563, 4,675, 1,127 and 765 across
  its four sources.
- 89% of the long single-file books now have real chapters. *Sequel.exe* went from 1
  chapter to 85.
- Books whose files could not be read fell from 16,130 to 742. Two books are missing
  entirely.
- About 30% of the organiser's records of where files live were out of date, but 97% of
  the files were still on disk.
- Everything done here can be reversed.

### The 580 megabytes read and thrown away (13 August)

Every start read about 729 MB of stored data. 580 MB of it was per-book audio signatures,
about 22 KB for each of roughly 67,800 books, and it was immediately discarded. A migration
tool to fix this was built but not yet run, and it is designed to avoid writing 1.5 GB of
history. It was later previewed against 26,159 books.

### The tag that was never written (15 August)

- A loop meant to write track numbers was fixed.
- Chapter titles had been overwritten, turning "Chapter 1: Departure" into "01 - Book
  Title". They are now kept.
- Files were being copied and checksummed twice. That is fixed.
- Cover art is now checked for each file.
- Two separate tag writers were merged into one.
- The speed gain has not been measured.

### The tug-of-war over where books live (15 August)

Three parts of the organiser each had their own answer for where a book's files belong.
There is now one answer, and files move once. A safeguard stops a book's chapters being
collapsed by mistake. A "/" in a title had once split a single book into 85.

### The work that said it succeeded (16 August)

- A rename that failed still reported "applied".
- Organising could run on an empty folder.
- Activity messages were cut short.
- Jobs stuck on "pending" were fixed and checked on the live server.
- Three iTunes jobs (Sync, Path Reconcile and Path Repair) were placeholders that raised
  378 warnings. A job that fails to load now stops start-up, and Position Sync now fails
  loudly.
- A scan was killed after walking 3,917 files. The AI service's credit had run out, and
  about 25 minutes were spent retrying.
- Every one of the 146 jobs must now report its progress.
- Three enabled jobs had never run, among them the metadata upgrade and organise.
- The test suite had been running twice. It now takes 8 minutes instead of 16.

### The books that would not download (17 August)

- 552 of 1,322 download entries (41.8%) were dead.
- This affected 49 of 120 books. Five had no working entry at all, but 115 still have at
  least one good file.
- There were 1,036 genuine "not found" answers.
- A read-only checker was added, and it separates five different reasons a file can be
  missing.
- An earlier report that everything was fine (1,786 out of 1,786) was false.

### The repair that would have deleted the evidence (19 August)

**What it was.** From early March to mid-August, the default file-name pattern wrote
track numbers as "70/131", meaning "track 70 of 131". On disk, a slash means "go into a
folder", so the library recorded files at locations that do not exist. The proposed
repair would have deleted every such record as dead. Yet all 101 records checked still
had their audio file on disk under the correct name. Each wrong record was the only
pointer to a real file.

**The fix.**

- The deleting code was removed.
- A full-scan check was added, with planted test cases to prove it works.
- A repair that points records at the right file was not built at the time.
- 16,265 books have no working file at all.

### The backup that killed the database (29 August)

**What it was.** Backups grew from 247 MB to about 15 GB each. Keeping ten of them needed
150 GB on a 141 GB disk, so the database crashed every 17 minutes.

**Why it mattered.** Two saved secrets were corrupted: a login password and the key for an
outside book-information service. Nothing else could restore them.

**The fix.**

- Backups now check free space first, delete old copies before writing a new one, and
  obey a limit on total size. Once this was deployed, the app went from staying up for
  13 seconds at a time to 15 minutes, and it has held. This was also the real reason new
  books had stopped appearing.
- The three oldest copies were saved.
- The review queue had been losing changes that failed to apply. It now keeps them, and
  "rejected" is now split into its separate meanings.

### The instruments that lied (29 August)

This work spanned 22 changes.

- **Version history.** The database was 30 GB, and 7.65 GB of it was version history.
  85.4% of that history was an unchanged block copied again and again, even though only
  0.64 MB of a 22.09 MB record had changed. A design to cut it about 35-fold, to 0.22 GB,
  is ready.
- **Space.**
  - Deleting records freed no space. A compaction button existed, but nothing called it.
  - Compression reached 2.69 to 1 in 8.4 seconds, against 2.66 to 1 in 39.7 seconds for
    the slower option.
  - The disk had 8.67 GB free, while 861 snapshots held 148 GB.
- **A false alarm.** A warning that the work was "one change from being lost" was false,
  and acting on it would have removed the new size guard.
- **Backups.** The backup folder can now be chosen, and listing backups no longer
  re-checksums about 14 GB each time.
- **Progress.** By month end, 15 of the 71 items on the list were closed and 52 remained.

### The delete that left its index behind (29 August)

**What it was.** Deleting activity-log entries left their index signposts behind. The
signposts took up about 0.78 GB (60%) of a 1.34 GB log, somewhere between 6 and 24 million
of them.

**The fix.** All four delete paths and the "clear" action are fixed, and a nightly repair
runs. The space is not recovered until compaction runs.

### Other file work (roundup)

- **The library's own folders (30 August).** Sixteen file walks now skip the organiser's
  own folders, which hold about 100 GB and more than 1,000 catalogue files. Two routines
  that deleted empty folders had been deleting inside the backup area. Twenty-one checks
  confirm the fix.
- **File totals.** Bulk file changes now recalculate each book's totals. The scanner had
  been overwriting them. Moving files between books still does not update totals; eight
  places call that code.

---

## 7. Background jobs, restarts and the servers underneath

- **The activity page that ran the server out of memory (12 August).** Each read of the
  activity page used about 9 GB. On one occasion 30 copies were running at once, using
  30.8 GB with nobody watching, and the server crashed five times. Reads now go newest
  first, can be cancelled, and take 55 milliseconds, and the list of sources is kept for
  45 seconds. A hazard around compaction remains.
- **The second set of books (23 August).**
  - Maintenance jobs were recorded twice, and finished jobs came back to life. The old
    ledger still holds about 1,700 inert entries.
  - Five jobs had stopped resuming, and the bulk metadata fetch restarted from zero.
  - Shutting down now records "interrupted" instead of "cancelled".
- **The work that never came back (24 August, #2851).** A scan stopped at about 25,880
  files and was abandoned for 4 hours. There were 27 interrupted jobs, 21 of them scans.
  The organiser now keeps only the newest of each kind: 22 were retired and 5 resumed.
- **The ID that belonged to nobody (24 August).** Handing an iTunes identifier from one
  record to another now happens in a single step, so it can never belong to neither. Two
  tests that checked nothing were fixed.
- **Seven nights of missed maintenance (17–23 August).** None of the 12 nightly jobs ran
  for seven nights, because the supervisor crashed. After the fix, the author clean-up
  covered 14,948 authors, up from 12,856. This was confirmed by inspection only.
- **"No credit left" read as "busy" (23 August).** All 77 batches misread an out-of-credit
  answer from the AI service as "try again", and a 3,917-file pass was lost. The indexing
  job was also making three times as many requests as it needed. The test for this had
  used a made-up reply rather than a real one.
- **Monitoring (14 August).** Monitoring was restored with a single credential file. An
  iTunes backlog reported as about 9,000 was really 2. A dry-run switch was fixed. A trial
  rewrite of tags on 100 books was too slow to use.
- **Owner-review queue (6 August).**
  - 777 items were waiting, and 762 of them showed the same sentence. Running time was
    missing for 97.5% of the queue.
  - Now 1,593 of the 1,831 pieces have a running time. 286 of 356 items get a clear
    answer, and 70 cannot be decided automatically.
  - Three "multi-disc" items were really two copies of the same book: *Brother Wulf* (6.3
    hours), *Sevenfold Sword* (about 21 hours) and *The Warring Son* (11.8 hours). A
    snapshot of 132 groups, covering 4,146 files, was taken first.
  - A repair job was wasting 1.35 seconds per row.
  - A second pass found 434 of about 1,019 folders could be linked automatically, and 585
    need a person.
  - Five security advisories were closed.
  - A decision to overrule the queue was not being saved. It is now saved in one step.
  - Applying changes from the queue stayed switched off.

---

## Lessons carried into September

1. **A silent fallback is worse than a loud failure.** The transcription outage, the empty
   library page and search quietly running without its index all had the same shape:
   something degraded, nothing said so, and the result looked normal.
2. **A measurement is only as good as the thing it ran against.** The tests showed green
   in four misleading ways: hidden output, a stale server, a developer machine that was
   not the build server, and a test that invented its own input and so confirmed the bug.
3. **Being wrong on the record is fine; leaving it there is not.** Several corrections this
   month are listed above: the safety-net retraction, the 14 TB non-emergency, 25,304
   withdrawn in favour of 3,407, and an estimate of 6,000 against 1,384 measured.
4. **A note that records a fact goes stale faster than a rule the code enforces.** One
   decision in the collections work was justified by "we checked, the conflicting thing
   does not exist". That was true when written and false one change later.
5. **A check that exists is not a check that runs.** Two checks had been failing for 33
   days and for four months while the board showed green.
6. **A test can ask a question the bug answers correctly.** Both sorting tests passed for
   a sort that did nothing. Ask of any test what a do-nothing version would do to it.
7. **A feature can be inert because of a missing link far away.** The import checkbox did
   everything the tests looked at, and still organised nothing. The book it named was
   missing a link that a different part of the system was responsible for creating.
