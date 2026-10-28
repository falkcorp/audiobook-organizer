<!-- file: docs/executive-summaries/2026-10-executive-summary.md -->
<!-- version: 1.1.0 -->
<!-- guid: 58ff982a-a61e-482f-a7a8-a64578984407 -->
<!-- last-edited: 2026-10-27 -->

# October 2026 — Monthly Executive Summary

**Coverage: 1–27 October 2026; later October work is added here as new sections.**

**Pull requests:** every merged pull request named for a section is listed in the index at the end.

This one report replaces the 73 separate write-ups made during October. It is grouped by
theme rather than by date. Each section says what was wrong, why it mattered to someone
using the library, and what changed. It also adds, from their pull requests, outcomes that
never had a write-up of their own: the retired-versions work, the permanent decision record,
the job that continues past its time limit, the 27 October trash fix, two dependency security
updates and five changes to the automated test system. A month that is already combined gets no new
per-day files, so anything that ships later in October is added below as a new section. The
record of decisions taken on the owner's behalf on 25 and 26 October is kept at the end as
an appendix.

A few terms used throughout:

- **The library** is the audiobook collection the organiser manages.
- **A version** (or **copy**) is one stored edition of a book. When there are several, one is
  the **primary**, the copy the listening apps show.
- **A repair** is a reviewable clean-up on the Repairs tab: it shows a plan first, changes
  only the rows the owner picks, and records each change so it can be undone.
- **Preview mode** (also called a "dry run") means a job reports what it would change
  without changing anything.
- **A background job** (or **operation**) is a task the organiser runs on its own, such as a
  scan or a nightly clean-up.
- **ASIN** is Audible's product number for a book; **ISBN** is the standard book number.
- **Audiobookshelf** is the listening server the organiser can feed; **the phone apps** connect
  to it.
- **API** (application programming interface) is the way another program, rather than a person
  on the website, asks the server to do something; an **API key** is the password such a
  program uses.
- **AI** (artificial intelligence) here means the language-model helpers that suggest fixes
  such as author merges.
- **CI** (continuous integration) is the automated system that builds and tests every proposed
  change before it is merged. It runs on GitHub and on the project's own machines through a
  tool called **Woodpecker**.
- **ID** is an identifier, the fixed number or code a record is known by. **CPU** is a
  computer's processor. **CSV** and **JSON** are common file formats for tables and structured
  data. **ZFS** is the server's file system, which keeps snapshots (older copies of the disk).
- **Pull requests** are the reviewed changes that make up the work; the numbers named in each
  section's original write-up are listed in the index at the end.

---

## Month at a glance

- **Books were being renamed, matched twice or stripped of their authors by the tools meant to
  tidy them.** Number-titled junk entries fell from 11,934 to 6,613 in one night; a scan renamed
  1,176 books and lost their matches; two clean-ups could leave a book with no author; and 13
  of 19 books built by one repair were second copies. Each was fixed so that tools hold or
  suggest instead of silently overwriting.
- **Metadata lookups got smarter, cheaper and harder to lose.** Matches that used to be deleted
  by routine fills (1,078 books ended up with an Audible number and nothing to review) are now
  kept. Lookups are shared across copies, Google Books has one daily allowance, and the
  missing-metadata count is a live number you can click.
- **Nine ways to apply a metadata match ignored the owner's rejection.** All now refuse it, and
  every merge, whether of books or of authors, can now be undone, with the link written before
  anything changes.
- **Listening history was protected from lost saves and from being read by the wrong people.**
  Five places that saved progress now take turns; collections show each viewer their own
  listening; and repair previews, change histories and merge-undo records no longer expose
  other people's reading to guests.
- **Deleting and purging got safer.** Progress is carried to a live copy before a purge, purges
  are previewed with a full breakdown, trash cleanup stays inside the library, the nightly
  cleanup no longer deletes the files of a version a book still plays, and two library jobs can
  no longer silently undo each other's file records.
- **The internet-facing surface was hardened.** API keys expire, phone apps stay signed in, guests
  get private phone-app keys, invitations are emailed, and the public server's redirects, rate
  limits and web policy were tightened.
- **The platform underneath got stricter and clearer about failure.** Startup steps, boot
  backfills and the operations catalog moved under rules, a crash on database close was fixed, and
  long jobs can now pause before their deadline and continue under the same operation.
- **Retired versions and the old Dedup page.** Trashed duplicates gained a safe "retired" state,
  and the nine-tab Dedup page was folded into the Review page.

---

## 1. Titles, authors and series that were wrong

### Books titled by a number (2–3 October)

**What it was.** Thousands of library entries had a title that was only a number, or a
number stuck to a name ("01", "077 - Chill", "1-06 Chapter 1_ Return to Peril"). Most were
not books. They were single chapter files that an old import had registered as books of
their own, while the real book that already owned the chapter sat next to them. About
4,900 of the stray entries pointed at a file that no longer existed. Where a chapter existed
twice (for example once in a download folder and once in the library), the repair tool
called both copies "ambiguous" and skipped them, 7,627 times, even though each pointed at a
single real book. In every one of these cases the real book was already proven to own the
file, by the same import location, the same content fingerprint, or its own record. The
largest case was one 71-hour Bible that already held all 1,189 files, plus 2,352 stray
copies.

**Why it mattered.** The library was inflated with fake books, the repair tool listed
thousands of them as "held" without ever acting, and the tool that proposes real titles
offered the first spoken sentence of a recording, a publisher's opening line or leftovers
such as "copy1". Folding one stray entry back took 11 seconds on larger books because the
database re-read the whole library twice each time, and a status counter that re-read the
library every five seconds used a quarter of the server's processor all day.

**The fix.** Overnight the count of number-titled primary books fell from 11,934 to 6,613
(the target of under 500 was not reached) and 9,563 books were folded into their real
book, nothing deleted and every change undoable. Ghost entries and proven duplicate
copies are now applied rather than merely listed, and a stray entry registered twice for the
same file is folded in together with its twin instead of blocking it. The Bible was retitled
The Holy Bible, its narrator credited correctly, and its copies folded in. The title reader
learned eleven more file-name shapes while leaving real titles such as "1984", "2001: A
Space Odyssey", "11/22/63", "9-11" and "10-Minute Toughness" alone, and spoken prose, a
publisher's opening announcement, chapter headings and file-name leftovers are refused as
titles. The two library re-reads per repair are gone and the counter runs once a minute, so
folding one entry takes 0.25 seconds, down from 0.7 seconds on small books and 11 seconds on
large ones (up to about forty times faster). The review page,
which took 8.3 seconds quiet and 40 to 105 seconds under load, now answers in 2.2 to 2.5
seconds warm and 4.0 cold. New dashboards track the number-titled count, repair speed,
review-page latency and fetch cache hits. Thirteen pull requests were merged.

| Kind of number-led title (primary books) | Before | After |
|---|---|---|
| Number, dash, text ("12 - Text") | 6,920 | 4,070 |
| Digits only ("03") | 1,578 | 958 |
| Disc and track ("1-06") | 1,189 | 327 |
| "N of M" | 932 | 621 |
| "copy" plus a number | 705 | 50 |
| Number then text, other, or year first | 610 | 587 |
| **All** | **11,934** | **6,613** |

**Still open.** 4,966 of the remaining 6,613 are chapter sets with no real book to fold
into (serial novels registered one chapter per book) and need a rule for combining many
short chapters with different titles in one folder, not a retitle. The largest are S. M.
Stirling (419 entries), Paolini's Inheritance (346), Lightbringer (309), Shadow's Edge (298),
Horizon Storms (293) and Delve (267). About 350 entries titled like "02 - No Quarter" are
refused because their title is marked as supplied by a provider, which no provider would
really do; whether the repair may override that mark is the owner's call. Five
folded-chapter groups (about 400 entries, the largest Eldest with 313 and Foundation with
74) are blocked by a second book owning the same file whose title is real (such as "Prelude
to Foundation"), which may be a legitimate second edition, so they are listed for the owner
rather than merged. Copies matching only by name and size stay skipped until hashed. Titles
made only of a number, such as "96 Hours" or a year, cannot be told apart from real titles
by shape and are left alone.

### No book is ever left without an author (21 October)

**What it was.** Two clean-up tools removed a bad author credit even when it was the only
author the book had. Names that are only a label ("Book 2 (Unabridged)", "Part 2",
"Disc 1", "Vol. 01", "[Track 003] 2") were unlinked whenever no evidence of the real author
was found, and the junk-author-record clean-up deleted records outright. It had hundreds of
"Track 01" to "Track 42" records in reach, crediting 343 books. Both tools reasoned that no
author was better than a fake one such as "Track 01".

**Why it mattered.** The owner decided on 21 October that a book must never be left
without an author. Removing the only credit left such books blank.

**The fix.** A label-only credit is now "swap or keep": it is replaced only when the
metadata provider, or two independent kinds of evidence, name a real author (a single file
tag is not enough); with no evidence at all, the label credit simply stays. A junk credit
that is a book's only author is held, and rechecked right before writing so a book that lost
its other authors after the plan was made is still protected; the record clean-up leaves any
record a book still depends on and reports how many were held. A junk credit beside a real
co-author is still removed.
Label detection now covers number ranges ("Books 1-3"), decimals ("Book 2.5"), letter
labels ("Track A"), front and back matter words and letterless disc or track codes
("1-02"), while names that merely contain such words next to a real name are kept.
Everything stays undoable. Held books show as "held" in the repair lists rather than as
removed; nothing is deleted or left blank, and the placeholder is replaced once a real
author is found.

### Production companies listed as the author (12 October)

**What it was.** Some books list a production company, such as a studio, as the author, and
a job looks those books up again by title to find the real author. Two copies of that job
existed. The one run by hand did nothing when applied; the scheduled one rewrote books
without waiting for a library scan to pause, and cancelling did not stop a lookup already
under way.

**Why it mattered.** A scan and the job could write the same book at once, so one could
silently undo the other, and a cancelled job could still write a book after it was stopped.

**The fix.** One job remains, under both names. When applied it pauses scans first, the same
way the other metadata jobs do, and resumes them after; it works a few books at a time
(the bulk metadata fetch's setting, at most 8), looks each book up once even
with two companies credited, and stops in-flight lookups on cancel. Without "apply" it
still only previews, and only people who manage settings may run it.

### Pruning duplicate and empty series, one reviewed row at a time (11 October)

**What it was.** The old duplicates page had a "Prune Series" button that merged every
duplicate series and deleted every empty one in a single confirm, with a preview built on
different rules than the nightly job (the job skipped series kept for an undo and refused
to delete any series a book still named; the preview did neither).

**Why it mattered.** Removing a series cannot be reversed, and 6,893 series had vanished in
August from under books that still named them. One button asked the owner to approve
changes he could not see.

**The fix.** A "Prune series" repair on the Repairs tab lists each duplicate group (same
name ignoring capitals and spacing, same author) and each empty series as its own row.
A series is removed only when no book in any state, trashed books and other versions
included, names it; rows failing this are listed as "still referenced" and cannot be
applied. Applying a duplicate row moves every book, other versions included, into the kept
series and removes the emptied copies; applying an empty-series row changes no book. Each
row is rechecked right before writing, a row whose books or counts changed since the plan is
refused, and every step is journaled first. The repair and the nightly job now share one
piece of merge-and-delete code, because a separate copy would drift and missing any one of
the job's safety checks, each added after a past incident, would bring that incident back; a
recording of the job's output before and after the move still matches, the only difference
being one new warning if the change log ever refuses to record a step. The repair and the
nightly job now wait for the other series jobs (rename, tidy and merge), and those wait for
them. The series list refreshes when an apply finishes, so the result shows at once instead
of after the day-old stored list expires. Applying is recorded but not undoable, because a
removed series cannot be brought back under its old number.

### Moved books and series named after authors (5 October)

**What it was.** When a book was moved, renamed or had its chapter files sorted differently,
the scanner did not recognise the stored book in time. The library also has many "series"
sharing a name with an author, and the scanner treated each as a real series.

**Why it mattered.** An author's name such as "Brandon Sanderson" could be filed as a series,
and a moved or renamed book could gain stray author, series and work entries or even a
second copy.

**The fix.** The scanner finds the stored book first and keeps its details. A series sharing
an author's name counts as a series only when its books show it is one, and the scanner and
importer no longer create series named after the author. A new Repairs list, "Series named
after the author", shows every book linked to such a series. A series counts as author-named
when every book in it is by that author and the author also has books outside it; the list
and the scanner use the same test, so they never disagree. Real series, locked series, books
with more than one author, books whose details were confirmed, series where it is the author
credit that is wrong, and Doctor Who / Big Finish books are listed with the reason and never
changed. Only approved books lose the series link and number, each change undoable, and
those books are searched again for suggestions only; nothing is applied automatically, and
the emptied series entries are kept so the undo still works. It had not been run on the live
library at the time.

## 2. Finding and matching book metadata

### Books with file-style names never got found (5 October)

**What it was.** About 1,150 books had titles that still looked like file names ("2018 -
Blueshift", "Discworld 24 - The Fifth Elephant - 01"). The metadata search sent that text
to Audible as written, and Audible has the book only under its real name, so the search
came back empty. Separately, nothing searched books added after the last manual run, so 613
later books were never searched.

**Why it mattered.** Audible finds a book only when the name matches, so roughly a thousand
easy-to-identify books stayed unmatched, and books added later were never looked up at all.

**The fix.** One shared reader now strips the year, author, the "Unknown Author"
placeholder, track number and recording details, and spots the series and book number (the
original text is still tried as a backup); the scanner uses it for new books, and two scoring rules that misread a track number as a
series number were corrected. On 20 sample books the old search found 0 and the new one
found 10, all the census could find in Audible's catalog; the other 10 are not in Audible's
catalog under that title, carry the wrong author, or are named only by series and number. A book whose title is just its
author's name falls back to the title heard in its audio or its folder name (a biography
named after its subject is still searched), and a book filed with its own title as the
"author" is searched without that fake author and only an exact, unambiguous match is kept.
A new job (every 6 hours, on by default) searches books never searched or whose earlier
results were cleared, at the providers' normal speed limits, collects suggestions only and
never changes a book. Books already in the library are never renamed by a rescan: a review
list shows the roughly 180 whose folder-made name would change, and only those the owner
approves are renamed. Books named only by series
and number still need series-and-number matching.

### Fetched matches no longer vanish (5 October)

**What it was.** The library keeps provider matches so the owner can review them. The
database treated any change to a book's Audible number (ASIN) or ISBN as "a different book
now" and deleted its matches, and three apply paths deleted all matches, including the one
just applied. Because a job fills in the ASIN every six hours, often from the very match
waiting for review, filling it in deleted the match.

**Why it mattered.** On production 1,078 books ended with an ASIN, no review decision and
nothing left to review; another 421 lost matches when their author or title was tidied on
4 October.

**The fix.** Matches are now deleted only when the title or author name changes, the details
the search was made from. A match whose ASIN disagrees with the book's cannot be applied
(the single-book window marks it "ASIN conflict" and asks for confirmation; a match found
under an older number is marked "Fetched for another ASIN"), and each kept match records the
ASIN it was found under so providers that give no ASIN (Open Library, Google Books) are
covered too. A new repair, "Refetch lost metadata candidates", lists books that had a match
and lost it and, for the books selected, asks the providers again. It changes no book, never
applies anything, can be dry-run first and no longer pauses library scans. It covers only
books the owner has not ruled on, looks up only the books picked, at the providers' normal
speed limits, and skips any book reviewed or re-searched since the plan. When a book's ASIN
is replaced or removed, a match without the new number cannot be applied automatically and
the background lookup searches that book again; clicking Apply on that one book in the
review lane still re-applies it. Follow-ups: the
bulk "apply cached matches" action skips already-applied books and reports them as "already
applied" (otherwise it would have redone file work, tag writing and iTunes updates, and in
replace mode could have overwritten later edits); undo no longer deletes matches; and the
overnight job that fills a blank title or author from the opening narration refuses a match
whose ASIN disagrees, because matches now outlive a number change and it could otherwise
have written another book's title onto this one. The repair had not been run on production;
running it and reviewing what comes back is the owner's call.

**Still open.** A title or author-name change still clears a book's matches: when a repair
tidies a junk title or relinks an author, the book loses its matches and nothing re-searches
it automatically. The recovery repair finds those books too, but someone has to run it;
keeping such matches would need the database to re-check them against the new name, a
larger change left for later.

### One Google Books allowance and a fallback for stuck books (6 October)

**What it was.** When Audible cannot match a book, the nightly fetch tries Open Library then
Google Books. The fallback did not run when Audible's matches were all unusable, so such
books were shown from cache forever and never searched again, and Google callers did not
share its 1,000-lookups-a-day limit.

**Why it mattered.** Books whose Audible matches were all unusable were stuck with nothing
new to review, and with no shared count any feature could use up Google's 1,000 daily
lookups, leaving none for the owner's own searches.

**The fix.** The fallback also runs when every Audible match was rejected, conflicts with
the ASIN or scores too low. New matches are added beside the old ones (ten are kept; the
weakest drop first, rejected ones before low scorers). If Audible itself fails, the fallback
waits for the next run instead of spending Google lookups; if Open Library fails, Google
Books is still tried. Doctor Who and Big Finish books still get an Open Library search but
never use Google lookups. Every Google caller now draws from
one daily count that resets at midnight Pacific, checked at the one place all requests pass;
automatic work (including searching many books at once) stops at 800 so 200 stay free for
the search dialog, reaching that limit never blocks searches the owner starts, and "Search
again" on a single book counts as the owner asking. A permanent refusal is recorded so the
book does not spend the allowance every day, and books put off longest go first. A
"deferred" chip lists books waiting, and a book leaves it as soon as it has a usable match;
result counts now include deferred and skipped books. Four review rounds fixed: stored
matches, including rejected and low-scoring ones still open to review, lost a little more
every night by a re-search that found nothing (including matches from the oldest search
version and from searches with no author), and "Search again" could do the same; adding Open
Library or Google matches wiping the stored Audible ones or bringing back a wrong volume an
older search had filtered out; a bulk apply applying a match nobody saw, and a "select all"
preview promising an Open Library or Google match for a book whose match never loaded on
screen; Audible matches usable by the system now rank above Open Library or Google matches;
unsearched books draw on the same allowance. By the owner's decision Open Library and Google
matches are only ever applied by hand from the review page, so the "Fetch metadata" button
now says "Match found, left for review". Organizing, the iTunes import, that button and the
production-company cleanup still look these sources up but never apply them; such a match
appears on the review page the next time the nightly fetch reaches that book, and the
production-company cleanup keeps none.

### Metadata lookups stop asking the same question twice (11 October)

**What it was.** Answers were remembered per book, so a second copy, a book split one row
per track, or a re-import asked the same question again, and "nothing found" was never
remembered, so every pass over the library re-asked about every unfindable book.

**Why it mattered.** Every repeated question cost another provider call, which matters most
for Google Books with its 1,000 lookups a day.

**The fix.** Answers are now remembered by the question (title and author normalised) and
shared. "Nothing found" is kept for 14 days on Audible and on the Audnexus book-data
service, 30 on Open Library and 7 on Google Books, the periods the owner chose; failed or
rate-limited requests are never remembered, so a passing provider fault can never pose as
"nothing found". In a made-up
library of 1,000 books asking 600 distinct questions, the first pass makes 600 provider
calls instead of 1,000 and an immediate second pass makes none instead of 400. "Search
again" clears the book's remembered answers; a setting turns the shared cache off at once
without a restart. A new sweep task removes expired answers and records of deleted books
(preview unless applied), and the database-health page shows the cache size and, on
request, how much has expired. Review fixes: a book's own earlier answer is no longer hidden
by another copy's "nothing found", copied answers keep their age, and the ASIN backfill asks
Audible afresh while its preview runs store nothing.

### Looking books up in the author catalog first (12 October)

**What it was.** The app keeps a catalog of every title Audible lists for the library's
authors, but the metadata search never read it, so each such book still cost Audible calls
and books with titles like "Series name, Book 3 - Real title" found nothing.

**Why it mattered.** Books whose stored title Audible cannot answer found nothing at all,
even though the app already held the right title in its own catalog.

**The fix.** Before asking any provider, the search can look the book up in the catalog by
its own ASIN or by each credited author in order (exact name or a filed alternate spelling),
comparing every title the book is known by with digits required to agree so book 12 is never
offered for book 13. Titles Audible no longer lists are skipped. Authors with more than
2,000 titles are skipped, and so are names shared by two Audible authors unless it is
already settled which one is meant; both are counted. Results appear as review-only "Audible Catalog (local)"
candidates; bulk apply never applies one, and the manual-only Doctor Who, Big Finish and
Torchwood rule still holds. The setting ships off until the owner reads the coverage report.
In a made-up library of 5,000 books and 800 authors of 100 titles each (plus 3 oversized
authors and 5 sharing a name), provider calls were identical (8,400) and 796 books gained a
catalog candidate; that mix is fixed by how the test library was built, so it shows the step
works, not how many real books will gain one. The candidate fetch's progress and final lines
now say how many candidates came from the catalog and how many from providers, and how many
authors were skipped. A book whose only candidate is from the catalog now counts as having a
usable candidate, so Open Library and Google are not asked about it; whether they still
should be is an open owner decision, filed in the TODO list. If every provider fails while
the catalog has a match, the search still counts as unanswered and the book keeps what it
had. A book filled from the catalog is ranked just below a live Audible match: a manual or
review-page Audible apply can replace it, the automatic upgrade sweep does not, and its year
is read as the audiobook's release year. The catalog harvest now also records alternate
spellings for every author. A separate switch (on) searches the roughly 1,270 books
whose stored title is a person's name by the name of their folder; those can be applied only
one book at a time from their own review row, and since this is a new question for those
books, the next candidate fetch asks the providers again.

### Missing metadata is a live, clickable count (11 October)

**What it was.** There was no single answer to "how many books are missing metadata", and
counting needed a scan of the whole library.

**Why it mattered.** Progress toward the owner's goal of fewer than 1,000 books missing
metadata could only be measured by scanning the whole library, and there was no way to click
from a count to the books behind it.

**The fix.** Every book now has one identification state: never looked up, looked up but
nothing found, suggestions waiting, applied, or held (ruled "no match"; rejecting one
suggestion does not hold a book; books with suggestions waiting do not count as missing).
The app keeps running counts, so "missing" (never looked
up plus nothing found, among books the Library shows by default) is instant and each count
on the review page links to the Library searching that state. The Library search gained an
identification-state filter (with exclusion) and an Audible ID filter, and its "Select all"
number is the same count. The "missing" number
is higher than the owner's goal (under 1,000) by the franchise excluded from the goal
(about 281 books) until a later change marks them. Counts update in the same database write
as each change; a test of several threads over 10,000 books checked them against a full
recount 50 times with no mismatch. On 40,000 synthetic books the count takes under a
microsecond and the first page of the filter 4 to 6 ms; an earlier draft slowed bulk saves
up to ten times and was redesigned to within about 12%; building the index took about 0.05
seconds per 5,000 books in testing. After an update the counts take a few seconds to build
and answer "try again shortly" meanwhile; if the build fails, the app retries on its own for
about two minutes, after which a maintenance task can rebuild it by hand.

**Still open.** The owner is asked to confirm or change the rule that a book is "held" only
when ruled "no match" (rejecting one suggestion does not hold it). A background job that
re-checks books whose state may have changed was still to come; this change only lays down
the list it will work from.

### Copying metadata to the version the library shows (6 October)

**What it was.** When the library holds two copies of one book, only the primary is the one
listening apps see. A count on 5 October found about 859 primaries counted as "metadata
missing" although the other copy was already matched or had suggestions waiting.

**Why it mattered.** It inflated the missing-metadata count and showed unfinished entries in
the listening apps for books that had in fact been identified.

**The fix.** A Repairs list shows those groups. For a matched twin it applies the same record
to the primary (undoable, as a normal apply is); for a twin with suggestions only it copies
the suggestions, which applies nothing; either "undo last apply" or reverting the repair's
run takes an apply back, and reverting removes copied suggestions. Narrator, ASIN, ISBN and
running time are copied only when the running times or narrator show it is the same
edition. It never changes which copy is primary, because doing otherwise can hide a book
from the listening apps, writes no book but the primary, and never touches iTunes-linked books, Doctor Who, Big
Finish or Torchwood books, books with hand-locked fields, or books apps do not list. Groups
whose copies look like different editions (running time more than 5% apart, abridged against
unabridged, a different narrator), whose titles or authors do not match, or that disagree
are held, each with its reason. Each book is checked again at the moment of writing, every
change is recorded, and anything that changed since the plan is refused.

**Still open.** How many of the 859 qualify will be known only from the first plan on the
live library. Copies matched before 5 October may have lost the search result they were
matched from; those rows are held and need a fresh search first.

### Hand-applied books are recognised by everything they carry (5 October)

**What it was.** Doctor Who, Big Finish and Torchwood books are applied by hand and no bulk
job may change them, but six jobs decided membership by looking only at the book's own
record (or only its folder). A book whose only clue was an audio file name, the intro
recording, an author credit such as "Big Finish Productions", its series or a franchise tag
slipped through.

**Why it mattered.** Many iTunes-imported Big Finish releases sit in neutral folders with junk
titles, so a bulk job could merge, regroup, re-credit or demote one of them, breaking the
owner's standing rule that these books are only ever changed by hand.

**The fix.** All six (regrouping iTunes albums, copying iTunes books into the library,
linking authors from folders, moving the main-copy flag, merging chapter files, replacing
title-shaped author names) now use the complete check the bulk-apply path already used. A
book that cannot be checked because a read failed is left alone and reported. The old check
can no longer be called from outside its module. The big iTunes regroup plans a 5,000-book
test library in about 55 milliseconds. Tests give each job a book whose only clue is a file,
a credit or a tag; they failed with the old check and pass now.

### Tag chips follow your search (6 October)

**What it was.** The Library page's "Browse by Tag" panel always listed every tag with
whole-library counts ("language: en (28697)"), including trashed books and hidden spare
copies, and clicking a second tag did nothing, because only the first tag was ever sent.

**Why it mattered.** The panel could not help narrow a search at all, its counts included
trashed books and hidden spare copies, and the page request behind it averaged almost 6
seconds.

**The fix.** The panel shows only tags on the current results with counts of those books,
each chip narrows the search again and clicking an active chip removes it. If the results
carry no tags, or the tags cannot load, the panel says so instead of showing tags from an
earlier search; repeated requests share one piece of work, which stops if you leave the
page. Counts take 1 to
22 ms on 5,000 books. The genre and language lists now read from memory, the likely cause of
that request averaging almost 6 seconds, and no longer count trashed books. A later change
(21 October) keeps the panel in place so "Select All" no longer jumps.

## 3. Listening history, accounts and collections

### Bringing Audible's "finished" marks into the library (5 October)

**What it was.** Years of listening history lived only in Audible, so in the library those
books looked unread and "Continue listening" and the finished shelf were wrong.

**Why it mattered.** Hand-marking hundreds of books is impractical, and writing them blindly
risks two kinds of damage: old Audible progress overwriting newer phone listening, or every
finish being stamped with the day of import so it beats the phone's real progress at the
next sync.

**The fix.** A repair reads an Audible library export and copies finished, partly listened
or not-started status onto matching books, for one named listener (named on every run, never
assumed; the account that syncs with iTunes is refused, so nothing reaches the iTunes
library). A book matches only when exactly one copy fits and its length is within 10% of
Audible's, by Audible product id first and then by title and author; a match whose length
could not be checked is flagged for a closer look. Doubtful rows (two candidates, a length that differs, a junk title, a
series number that disagrees, a future date, a date with no time zone, a title marked both
finished and unfinished, or two Audible books landing on one library book, even when the other would have been
skipped, so a mislabeled volume 1 cannot be marked finished because volume 2 was) are listed
for review and never applied. Newer listening in the library always wins on any copy, nothing is
ever marked unfinished, a book already finished is left as it is, hand-set abandoned or
unstarted marks are left alone, and a partly listened position is copied only onto a book
that has never been played in the library. Finish
dates come from Audible and the mark is locked so a later sync does not undo it; the import
takes the same per-book lock the audiobook apps use. It previews first, writes only picked
rows, records the previous state, and one undo restores it while nothing newer has happened.

### An uploaded reading list can update what a person has read (23 October)

**What it was.** The master-list import lets an admin upload a spreadsheet of books (titles,
authors, ISBN and Audible codes, finished or half listened) and make the library's reading
status match. Stage one only previewed. Stage two lets the admin apply it.

**Why it mattered.** Reading history is personal data that took years to build. A bulk import
that guessed wrong, or replaced real listening with an older spreadsheet, would be hard to
notice and hard to repair.

**The fix.** Applying is a rehearsal unless the admin turns it off, and every change can be
undone from the operation history. Only rows matched by a code with nothing against them are
written; the import never marks a finished book unfinished, never overrides a hand-set mark
and never overwrites more recent library listening. A row matching nothing is kept as a
"read elsewhere" note on its own page and never becomes a book, and only when the row itself
is sound (a bad code, a future finish date or an unreadable date goes to the review queue).
The same book is not filed twice because two lists name it by different codes. Just before a
write the import rechecks the person and the book, and writes nothing if either was removed
or merged, and the row says why. Admins can delete a stored list and each uploader keeps their ten newest by
default. Five matching gaps found in review were closed: an ISBN-10 ending in X,
disagreeing ISBN columns, one badly typed row spoiling a whole JSON file, an Audible code on
two editions that the ISBN could settle, and an ISBN that only a merged-away copy carried.
The older Audible-only import is still in place; moving it onto this one is next.

**Still open.** While a list was only previewed, anyone who could browse the library could
read its rows, including titles, authors and the person's reading status; the 25 October fix
described two sections below ("Closing the paths that showed other people's listening
history") closes that. The admin-only lock on this import now holds even when sign-in is
switched off; the same lock on a few unrelated admin tools (cache, debug and wipe) is
unchanged and tracked.

### Every place that saves listening progress waits its turn (18 October)

**What it was.** The phone apps' sync, the web player, the read-status tools, the iTunes
backfill, merging two copies, undoing a merge and "discard progress and purge" all save where
a reader is in a book. Most took a per-book lock; five did not (the web player, the
read-status recalculation and repair, the iTunes backfill, the progress-moving step of a
merge, and the discard).

**Why it mattered.** A listener's place could be lost or moved backwards when two devices, or
a device and a background job, saved at once. A position
saved by the web player during an undo could vanish, and a phone sync landing during a merge
could be thrown away with the merged-away copy.

**The fix.** All of them take the same per-book lock before reading progress and hold it
until they have written. A merge now holds its locks from its "before" note to its "after"
note, so a save in between is no longer left out of both and wiped by the undo, and a
leftover progress repair is finished before the first note, so undoing the merge keeps that
progress instead of leaving it on no book. Moving progress between two
copies takes both locks in a fixed order so opposite-direction merges cannot wait on each
other forever. Unused automatic iTunes position-sync code was deleted rather than locked,
since iTunes is a one-time manual import. Each fix has a test that recreates the collision,
and a stress test runs every writer at once on overlapping books and fails if they stick
(deliberately breaking the lock order made it fail).

### Closing the paths that showed other people's listening history (25 October)

**What it was.** Repair previews and results, each book's change history, operation change
records, book-merge undo records and the trash-purge entry in the activity log carried every
user's finished or started books, positions and some private playlists, and were readable by
anyone who could browse the library, including any "viewer" (the access every guest gets);
editors could see some of it too. Two repair tools, the Audible reading-status import and the
uploaded reading-list import, work on people's reading status, so their previews listed other
users' reading, and dismissing an operation hid its summary but kept its full change record,
which stayed readable.
Thousands of such entries were written on 1 and 2 October, before the journal recorded which
tool wrote each entry, so the first version of the fix still let them through.

**Why it mattered.** A guest could open a reading-status repair's preview or a retired book's
history and read what household members had read. No one was actually exposed: every account
on the production server was an admin when the fix was made.

**The fix.** A repair tool's preview and results now require the permissions of running that
tool; the repair page still lists every tool, but hides the run details of a tool you may not
use and says you lack permission. Entries holding personal data (listening state and
positions, private playlist membership, and now the name of the person who applied a repair)
are shown only to admins; an entry that does not say
which tool wrote it is judged by its operation, and stays admin-only when that is gone. Merge
undo pages drop per-person progress and any warning naming a person unless the reader is an
admin, and the trash-purge activity entry no longer shows each person's progress. Editors
keep every repair tool, including Undo, and the undo preview, which lists entries
without their contents, still works for anyone allowed to run the tool. Every kind of undo record is now labelled "personal
data" or "library data" and an automatic check fails the build if a new kind is unlabelled
(kinds defined elsewhere are not yet checked; the follow-up extends the check to them). Verified with route tests showing a viewer and
an editor see none of the personal data and an admin sees all, each also run with the fix
removed to confirm it fails. Four paths remained for a follow-up: the activity log's
copies of server log lines, a repair operation's log lines, merge-undo warnings, and the
Audible reading-status import preview, which becomes admin-only. Until it ships the goal that
only admins see others' listening is not fully met.

### Merging two accounts into one (22 October)

**What it was.** Some people had two accounts, one for the website and one from single
sign-on or an old import, with listening progress, bookmarks, saved sign-ins and API keys
split between them.

**Why it mattered.** One person's listening, bookmarks, sign-ins and keys were split across
two accounts, and combining them carelessly could lose data or count the same listening time
on both.

**The fix.** An admin tool merges one account into the other and can be undone. A "Sign-ins &
data" panel shows what an account holds: linked sign-ins (partly hidden) and counts of books,
positions, bookmarks and keys. "Merge into..." previews what would move, where both
accounts have progress on one book and which wins, and anything that would stop the merge; if
the account going away holds the owner's sign-in, or that cannot be told, the admin must tick
a box. A verified email is kept verified on the surviving account, and the preview warns when
an address would be left with no verified holder. Confirming carries a
fingerprint of the preview, so a change to either account in between sends the admin back to
a fresh preview. The merged-away account is locked, not deleted, and cannot be switched back
on by hand. Undo restores each change but leaves alone any book listened to after the merge
and anything else changed since (such as a key's later uses or a playlist's new name),
reporting it; moved sign-ins, keys, playlists, collections
and invites go back with only their owner changed, and the surviving account is signed out
of the website and Audiobookshelf apps when sign-ins or keys move back. A merge stopped
partway can be resumed against the accounts as they now are, and each step is written
together with its related rows so listening time is never counted on both. Only a signed-in
admin on the website can start it (API keys and Audiobookshelf tokens are refused); when an
owner is set, only the owner can, and the general operations page cannot start a merge. Its
record shows only counts and ids. Older merge and Audible-import undo records now follow a
merged account to the account holding its data. Tests covered merging, resuming and
undoing, including simulated crashes.

**Still open.** Nothing has been merged yet. The owner chooses which account stays and when;
before running it, check both accounts' Sign-ins & data panels, the preview, and the line
saying which account holds the owner's sign-in.

### Collections: the same for everyone, rolling dates, changes that cannot be lost (18 October)

**What it was.** A dynamic collection (a saved search such as "books I have not finished")
was worked out for whoever looked, so two people saw different collections; the search
understood only exact dates, and only for last played; and two requests changing one
collection at once could both read the old list so the later write silently undid the
earlier. The Library page treated any last-played value as "has ever been played", refused
the date-added term, and "not started" missed every book with no listening record, which is
most of a library.

**Why it mattered.** A shared collection that differs for each viewer is not one collection,
its listening terms described the wrong person, and a simultaneous add could silently undo
another person's change.

**The fix.** On 18 October the owner decided to keep the collection routes, work collections
out as their owner, add relative dates to the single search syntax, and make adding and
removing books one locked step. A dynamic collection was then worked out as its owner, or as
the first active administrator when the owner had been removed; existing ownerless
collections were assigned once at start-up, and a server with no active administrator said
so instead of guessing. Searches now take rolling windows
such as the last 30 days or the last year (days, weeks, months, years, also as ranges),
worked out each run so a saved search keeps sliding, refreshed hourly for the Audiobookshelf
side; a bare "30d" with no sign is refused with the corrected spelling, a mistyped listening date
is refused when the collection is saved instead of quietly matching nothing, a date with no time
means the whole day, and a list of choices means "either" for listening terms (it used to
demand both). The Library page now honours these searches, "added" and "modified" are new
searchable dates, and "not started" finds never-opened books everywhere. Adding and removing
books is one locked step, with two new routes and the four Audiobookshelf membership calls
using it, so two simultaneous adds both succeed, and any other change arriving while an add is running
gets a conflict instead of overwriting it. Only the person a collection is evaluated as
can write listening terms into its search, since anyone with edit rights could otherwise
rewrite it to ask which books the owner had finished; anyone else gets a clear refusal, and
ordinary searches, renames and book lists are unaffected. No listening data (durations, read
status, progress, last played) is removed or rewritten. Removed owners' collections keep the
books they had until an administrator explicitly takes them over. The sign-in answers now
list the user's permissions for the web app. The two new dates change the search-index
layout, so the first start after deploy rebuilds the whole index and Library search uses the
slower fallback meanwhile. The rolling dates had not yet been tried on the production
library. The owner-evaluation part was replaced on 25 October (below).

### Smart collections show each person their own listening (25 October)

**What it was.** The 18 October design worked a smart collection out as its creator and kept a
stored copy. If no person was identified, the missing person counted as having listened to
nothing, so "unstarted" matched the entire library.

**Why it mattered.** Any viewer could infer what the creator had finished, the phone app
served that stored copy directly, and a collection called "unfinished" was wrong for everyone
but its creator.

**The fix.** Listening terms are now evaluated for the person looking, every time, in the
website and the phone app. Admins get no special view. With nobody identified the answer is
empty. A collection's member count on the collections page is the viewer's own, and
collections that do not mention listening are unchanged. The take-ownership checkbox and
the "not re-evaluated" banner are gone, and the hint under the search box says listening
terms use the viewer's own listening. No stored copy is kept (existing ones are cleared once at start), the owner-takeover
machinery was removed, and the hourly refresh no longer touches these collections. Smart
collections and smart playlists that mention listening used to look at only the oldest
10,000 books, so on a large library part of the answer was missing; they now see the whole
library, a list is shown at 10,000 books at most, and the page says how many really match
(for a static copy it says "10,000 of N" until the copy is edited). A listening term on one
side of an "or" was quietly worked out as an "and", showing far fewer books than it said; it
is now refused when saved, with the spelling that works, and the main Library search shows the
same message (making such searches work is still to do). Meaningless values ("completed" as a
reading status, which is one of only four) used to show nothing, and their "not" form used to
show everything; they are refused with the four valid statuses listed, as are a few spellings
(a star or tilde after "finished", "percent listened under infinity", a date with a star)
that no one used. A collection whose members cannot be worked out (the search index is starting, the saved
search is no longer valid, or the person's listening history could not be read) says so
instead of looking empty; a saved collection or smart playlist whose search is no longer
valid shows "members unavailable" until edited, and the phone app stops showing its old
answer. Older database files, ZFS snapshots and backups can still hold a creator's old
stored list until they are compacted or expire; they are administrator-only places, and
purging snapshots early is the owner's call. Tests with two users, nobody identified and the
phone-app endpoints fail if the code is switched back to the creator's listening.

## 4. Duplicates, merges, versions and rejected matches

### The chapter sets that became second copies (5 October)

**What it was.** The repair that gathers stray chapter files into one book also built a
brand-new book from a folder of chapters with no owner, without asking whether that book was
already in the library. On the night of 4 to 5 October, 13 of the 19 books it built were
second copies of existing books (one 347-chapter "Book 2" copy sat beside the 349-file
original). About 2,250 stray chapters had also been left out of its list without a word. And
any other book sharing one of the files blocked the repair outright, so real repairs waited
forever and nothing told a second edition from a different work.

**Why it mattered.** Each run left two live entries for one work, with chapters split between
them and listening progress on the wrong one, and the unlisted chapters made the backlog look
smaller than it was.

**The fix.** Before building anything the repair looks for a same-titled book. Series numbers
are set aside but must still agree (volume 3 is never folded into volume 1, and a title with a
number on one side only is held). If the same work exists by the same author (taken from the
folder when files carry none) and running times agree within 2% or 5 minutes, the chapters
are folded in; any other match is held for the owner, and a folder with no recognisable title
is held rather than built blind. Re-tagged copies of another book's chapters (same place, same
length, a fixed size difference) are never built into a book, every dropped chapter now
appears with its reason, and a book that also holds one of the files is compared as a whole
(a different title or author blocks, a same-titled same-length book is a second copy to
deduplicate first, a different length is held, only an untitled stray lets it proceed), and
every such shared-file repair is marked for review. Chapters left out (too few, a stray in a
mixed folder, or no chapter number) are listed with that reason and never applied, and
subtitles such as "A Novel" no longer make unrelated books look alike. The
repair never folds into a book an earlier run of itself built (if that is the only match, it
waits until that earlier repair is undone), and rechecks for a same-titled
book just before applying. The 13 duplicates already made were left for a separate decision.

### Linking versions brings the whole family (5 October)

**What it was.** Linking book B into book A moved B into A's family of versions but left B's
own versions (an organized copy, an earlier merge) behind in a family of their own, so one
title appeared split in two. Undo had no record of those copies, could be run twice, and could
unpick a later merge. Separately, if the note recording a progress move could not be saved,
nothing moved, and weeks later the nightly purge permanently deleted the retired book and the
listener's progress with it.

**Why it mattered.** Copies looked like a never-linked book, one click could quietly reverse a
newer merge, and a rare failure could erase listening history for good.

**The fix.** A merge now brings the folded-in book's other versions into the kept book's
family as extra versions (never the main one) and deletes nothing; only the named book is
retired. Every such merge first writes down which versions moved, and if it cannot, it does
not merge. Undo returns only those versions with their old main-version setting, is refused
if already done, if the merge never happened, or if a later merge moved the same version, and
leaves versions moved elsewhere in place; it leaves each family with one main version and does
not bring back the folded-in book, which is restored from the trash or by the separate undo for
automatic merges. iTunes books and half-scanned books in the family
make the merge refuse. Sound quality alone decides which copy is kept: since the owner's
decision at 13:00 on 5 October, a better copy is not thrown away because someone listened to
the worse one or because a worse one is already filed into the library, and their progress moves to the version Audiobookshelf shows. If the kept
book is not filed into the library, a filed version becomes the visible main version (hand
choice still wins; an iTunes kept book stays main, and if it is not filed into the library,
Audiobookshelf will not show the title). Every merge button now reports which
version is main, which holds the progress and, when Audiobookshelf will not list the title,
why. Progress is moved before the retired book can be purged, the purge refuses retired books
still holding progress, and the duplicate-version clean-up and iTunes regroup move progress
first and delete after, all or nothing (bookmarks and the Audiobookshelf link must arrive too,
or every listener's progress is put back and the book kept; before, one listener's progress
could move while another's did not). Progress is moved only onto a copy Audiobookshelf shows
(otherwise the duplicate is kept and named, and the iTunes regroup keeps the book and says so),
each delete checks for new progress one last time, and a user account the safety check cannot
read stops the delete instead of being skipped. Some undos had brought a book back with no
progress and its Audiobookshelf link on another version; every progress move is now recorded,
and an undo that fails partway finishes when run again. Undoing an iTunes library copy moves
progress back to the original the same way, refuses if the original is in the trash, and
finishes if run again. The merge report no longer claims a title is visible in Audiobookshelf
when the visible version is in the trash or no longer main; it gives a reason. Ordinary
merged-away books, whose progress has already moved, still purge. Later review rounds closed
four more ways progress could end up rewound or out of sight, and tightened how called-off
moves put progress back: a newer position is kept if a move is called
off, listened time keeps the larger value and is no longer counted on both books, a later
"unfinished" choice stands, and when two copies cannot be put in time order the
one further ahead wins (owner decision). A restored trashed version returns to the merged
family, the iTunes clean-up uses the same merge, and the repair that rebuilds books from
chapters finds the kept book from the merge's own record.

**Still open.** Background repairs that merge books still report only what they merged, not
which version Audiobookshelf will show.

### Selecting many duplicates at once (6 October)

**What it was.** The owner wanted to tick every row on a page, then every row on every page,
and Shift-click a run on the duplicate-review screens. Along the way two wrong-merge risks
surfaced: merging one group shifted the tick-boxes and "keep this one" choices of every later
group in the Version Groups and Series tabs, and "merge everything matching" accepted any
status, so with the filter on Dismissed it would link every pair marked "not a duplicate".
Undoing a bulk dismiss could also quietly erase a reviewer's earlier verdict ("these match" or
"not sure"), or miss a pair, if one save step failed.

**Why it mattered.** A following merge could keep a book or series that belonged to a
different group, a bulk merge could link pairs someone had marked "not a duplicate", and an
undo could erase a reviewer's earlier verdict.

**The fix.** Every list with checkboxes has a page tick-box, a "Select all N matching" bar and
Shift-click; changing a filter clears the selection. The "Select all N matching" bar shows
the server's count, the same one the confirmation shows, instead of a total that included
pairs to be skipped. Acting on all matching is done by the
server, which rechecks the filter, reports changed and skipped counts (hand-pinned pairs are
never touched), asks for confirmation with the count and does nothing if the count changed.
The Acoustic tab's Keep A / Keep B use the same checks (same-path pairs and chain-forming links
are refused), only pairs still waiting for review are acted on, and groups are tracked by
content, not position. A bulk dismiss writes an undo note for every pair first (a pair whose
note cannot be saved is left alone and reported), undo never overwrites a later verdict and
can be re-run if stopped halfway, a book that exists but cannot be read stops the action
instead of counting as deleted, and a failed load no longer says "no duplicates found".

### The nightly scan no longer renames your books (6 October)

**What it was.** The 6 October scan changed the title or author of 1,176 books already in the
library from file tags and names. Some were better ("74" became "The Jasmine Throne"), many
worse (299 chapter pieces of one serial went from "183 of 301" to "of 301").

**Why it mattered.** A changed title or author throws away the metadata matches already
found, so all 1,176 books lost theirs, and the scan kept no record of what it changed.

**The fix.** A scan no longer changes the title, author or series of an existing book; when a
file disagrees it records the old and new name as a suggestion, still fills in what is
missing, and imports new books as before. Every change a scan does make is on the book's
history with source "scan", and "183 of 301" is read whole, for existing books and new imports alike. The books that lost matches still
need them refetched, and the "of 301" titles restored from saved copies; both are outside this
change.

### Undoing a repair no longer hides the real book (6 October)

**What it was.** Books exist in several copies, one marked "primary", the copy listening apps
show. Undoing a "consolidation leftovers" repair (folding an empty 0-minute leftover into the
real 15-minute copy) moved the primary mark back to the empty leftover and removed it from the
real copy, which the repair never changed, if both had been primary. A partly finished repair
recorded nothing and undid the same way. Separately, the check that stops writes to the active
iTunes library's books ran before a long progress move and not at undo.

**Why it mattered.** Listening apps showed a zero-length leftover instead of the real book,
with no warning, after any such undo; and a standing rule (never write the active iTunes
library) held only most of the time.

**The fix.** The repair now records whether it gave the primary mark, found it already there,
or refused; undo changes only what the repair changed, and the restored leftover comes back as
a non-primary copy, whether the repair finished or stopped partway. One shared iTunes check runs on exactly
the copies about to change, under the group's lock, and if any copy fails no mark in the group
changes (an unreadable copy is a failure to retry, not an iTunes book): the repair stops and
reports it, and undo names the iTunes book. The check covers the merge repairs (duplicate
copies, fragment consolidation, consolidation leftovers), the chapter-folder repair, approving
a "these are versions" review item, and undo. The folder-books repair
may update iTunes-folder records and its undo may reverse its own change. Older records undo the
old way except that they never change an iTunes book's mark. Other places that choose a primary
(scan, organize, iTunes import) are not yet covered, and undo of those older repairs is being
checked separately.

### Imported files are tied to their version and carry a checksum (10 October)

**What it was.** Every import (by hand or from a torrent client) wrote the version record
before the file record existed, so the two never pointed at each other and the file was stored
without its content checksum. Two imports of the same torrent at once could attach it to two
books or leave a second, unlinked copy behind.

**Why it mattered.** The torrent cleanup tool could not vouch for imported files, and duplicate
detection could not match them to identical copies until a background job filled the checksum
in later. Only a log warning said so.

**The fix.** The file is checksummed once and saved first, then the version is recorded and
attached with the torrent link in one step. Failures leave something repairable: a file with
no version yet, or a version a later pass can attach instead of recording a second. Imports of
the same torrent now take turns, so a double click or retry can no longer attach one torrent to
two books: the second is turned away with "already in the library" before it creates anything.
Files imported before the fix are handled by a separate repair ("Repair for imports made
before the 10 October fix", under theme 5), which had not been run on production. Tests that
failed before the fix now run against the real database engine; earlier tests used a stand-in,
which is how the problem had gone unnoticed.

### Rejected metadata matches were still being applied (10 October)

**What it was.** Rejecting a suggested match was remembered, but nine ways of applying metadata
never checked: the review page's apply buttons (one or bulk), scripted bulk applies, applying an
older search run, the audio-identified automatic match, the nightly "upgrade to a better source"
job (the worst: it overwrites existing values), the older bulk fetch, the manual pick in a
book's search window, the automatic "find and apply metadata" step (iTunes import, organize,
the Fetch metadata button, production-company author jobs), and the repair copying a matched
copy onto another. The ASIN backfill and the junk-title repair could also carry a rejected
match. A rejection was stored by provider and title only, so rejecting one edition blocked the
other, and it stayed at the top of the suggestion list. The provider tried after Audible serves the
same Audible product, so a match rejected from Audible could still be applied from there.

**Why it mattered.** A match the owner turned down could be written onto a book anyway,
including by unattended jobs, and with renaming and tag writing on, reach file names and audio
tags.

**The fix.** All nine routes (and the two jobs) refuse a rejected match, rechecking at the
moment of writing (each has a test that failed before the fix); if the rejection list cannot be read the book is left alone, and no apply
button lifts a rejection (a refused write also removes the author it had just added). A
rejection now remembers the record's ASIN and ISBNs, so a different edition of the same title
is not refused, and a rejected ASIN or ISBN counts for every provider (the older 10-digit and
newer 13-digit ISBN forms match). The automatic "find and apply metadata" step skips a rejected
record and tries the provider's next answer, then the next provider, and applies nothing (and
says so) when only rejected records match. The copy-between-copies repair holds the book for
review instead. Rejecting waits for any write to that book already in progress, so a rejection
made while a search is running is never missed.
Rejecting re-sorts the book's suggestions so the next best shows, with a one-time background
pass for books already affected. The review page marks the row "Rejected" and the search window
marks "Rejected by you" with an Undo. Older rejections still refuse every record of the title
until undone. Still to do: Undo on the review row, full safety checking in the older bulk fetch
(it applies the rejection rule only), and re-sorting after the copy repair. None of these can apply a rejected match.

### The AI scan's "Apply selected" button now merges authors, and merges can be undone (13 October)

**What it was.** The AI author scan finds probable same-person names. "Apply selected" only set
a flag on each suggestion and merged nothing, so real duplicates vanished from the to-do list.
The author merge ignored locked authors and could not be undone (the merged author was deleted,
its pen names stranded). A narrator flag in the nightly and batch scan format was dropped, so a
narrator's books could be merged into the author. A newer scan could replace an older scan
mid-apply, a scan that failed to start blocked every later one, and a double click could start
two paid scans.

**Why it mattered.** Real duplicates disappeared from the to-do list with nothing merged, one
click could silently move locked books or a narrator's books to another author, and a wrong
merge could only be repaired by hand, book by book. A stuck scan also silently stopped the
nightly scan while paid requests ran for nothing.

**The fix.** Apply now queues a job that performs each merge with the Authors view's code and
marks a suggestion done only if its merge fully worked (it previews by default); anything that
failed stays on the list with the reason. Each suggestion says which author is kept and which
are merged; renames and splits are shown read-only with a link to the Authors view, and for
older scans the apply re-reads the AI's original answer and refuses a suggestion whose narrator
flag was lost. Locked books
stay with their author and are listed. Every way of merging authors (Authors view, both AI
applies and four background clean-ups: duplicate names, stray "&", numbered names, id repair)
moves aliases, writes each step to the undo history first (no history entry, no move) and can
be undone: the author, pen names and each book's credits return unless something changed since
(reported, not overwritten). An undo that stopped partway finishes when run again, and the
restored author appears without restarting the server. A book whose author was merged away now
shows the kept author's name, and the stray-"&" clean-up no longer takes over a name another
author claimed while it ran, which used to leave that author unfindable. Merges made before this change cannot be undone, and four actions
of the older AI review and the id repair (two kinds of rename, adding a pen name, and creating
the shared "Unknown Author") still are not. A narrator the
apply can identify is reclassified, one it cannot refuses the suggestion. A scan is held while
applied (at most three hours; a crashed hold clears at once), a failed start ends the scan and
cancels its paid requests after about twenty seconds of retries, and the server refuses a
second start while one is unfinished; the nightly job is never refused, because the results it
records have already been paid for. Not yet run on production.

### The extra audio fingerprints are put to use (11 October)

**What it was.** The Macs took short fingerprints from the middle of every file (at 10, 50 and
90 percent), not just the start where most audiobooks share a publisher intro, but nothing ever
compared them.

**Why it mattered.** Fingerprints from the start of a file mostly capture the shared publisher
intro, so without the mid-file ones the fingerprints could not help find duplicates or match
stray chapter files to their book.

**The fix.** The server keeps a lookup table of these fingerprints, which finds files with the
same audio even when it starts seconds earlier or later (99 of every 100 true copies and no
unrelated ones in tests on made-up prints). A match adds a fixed 6 points to the duplicate
score (shown as "Window AcoustID"; AcoustID is the audio-fingerprint system) and can only back
up other evidence: it never makes a repair row ready to apply and is never shown as the reason a
pair is listed. Chapter-fragment rows
show how much of the chapter's audio was found in the parent, for information only. It stays
off until the owner runs the "Build window-print index" operation, which previews unless told
to apply and can be stopped and resumed. For 742,000 files the table needs about 1.6 GB (scaled up from a
4,000-file test database; at most 1.75 GB by estimate), no audio is read on the server, existing duplicate scores keep their old
formula label until a rescore (best run when no library scan is running; nothing rescans on
its own), and setting the points to 0 turns the effect off.

### Every book combine can be undone by reverting its operation (18 October)

**What it was.** Books are combined about a dozen ways (by hand, review approvals, duplicate
clean-up jobs, the iTunes repair, the chapter-merge tool). Each kept a private undo record, but
the general Revert action could find it for only one of them, and the two old undo addresses were
about to be retired.

**Why it mattered.** Without a link every combine except one would have become permanent. The
owner treats undo as a hard requirement.

**The fix.** The link is written at the single point every combine passes through, right after
its undo record and before any book changes; if it cannot be written, the combine is refused.
Background jobs undo as a whole run (the duplicate engine's merges each get their own number, by
the owner's choice); manual combines get a fresh number shown in the surviving book's history.
Revert restores only the books that combine itself trashed, moves created file records back to
the book they belong to instead of deleting them, puts moved editions back in their old version
group (refusing the whole undo if a later merge moved the same edition), and works even if the
surviving book was purged. A combine now notes each step before taking it, so one that stops
partway is undone exactly; it shows its undo number, and the Combine message has Undo and Retry
buttons (manual combines' numbers are in the surviving book's history, not on the Operations
page). An undo notes "started" before changing anything, so one cut short by a crash finishes
when run again; a test crashed it at 46 different points and everything came back each time.
Undo never deletes an author and keeps credits added since, such as a narrator or co-author;
running a finished undo again no longer removes a lock or choice the user made since or
subtracts listening time twice; a complete undo no longer reports itself as partly done; and
undo takes the same per-book lock as the phone apps, so a sync during an undo is not thrown
away. The nightly trash purge can no longer delete a book an undo has just put progress back
on, or one restored from the trash a moment earlier, and it no longer makes the chapter repair
reload the whole library (about 460 MB) for nearly every book. Six review rounds closed these
and other gaps, among them a crash right after trashing a book, overrides and title changes
written before being noted, track renumbering of untouched books, and progress restored onto a
vanished book. A one-time maintenance job (preview by default) gives
every earlier combine its own undo number, never the job's, so reverting the job cannot undo
every past combine at once; the owner decides when to run it for real.

**Still open.** A stop in the middle of moving a reader's progress can leave a copy of it on
the surviving book as well; the progress on the original book always comes back.

### Approving a review hold merges it once and recovers after a crash (18 October)

**What it was.** Bulk-approving review holds stopped silently at the first failure; two approvals,
or an approval and a reject, arriving together could run the same merge twice; a hold left
"in progress" by a crash was hidden forever (the promised 15-minute release could never happen);
a merge that worked but whose "done" note failed could be re-run; and the nightly hold clean-up
could erase a decision made moments earlier.

**Why it mattered.** A merge moves files and permanently retires extra book records, and the review
page cannot bring them back.

**The fix.** Every hold is tried and the result lists merged, failed (with reason) and skipped.
Approving first marks the hold "being applied" in a step only one request can win, and refuses every
other decision until it finishes; a stale view is refused. Each server run has a random identity,
so a mark from an earlier run means that run stopped and the next approval takes the hold over and
finishes the merge (skipping work already done). A failed second try keeps the hold out of the
queue; "Abandon" moves it to a "needs attention" list for someone to check the library. Saving
"done" is retried several times; if it still fails, the reviewer is told the merge ran and the
result was not recorded. Failed holds keep their place in the list, so trying again retries
only them. The review page shows a "held mid-apply" notice with Finish and
Abandon, and the clean-up deletes only undecided holds. If merging were switched off, an
interrupted hold could only be abandoned; production has had merging on since 17 August. Not
yet run against the production library.

**Still open.** The retry does not redo the disc and track numbering of a multi-disc merge that
was cut off partway, and a "this folder duplicates an existing book" merge can fail on its
second try if its first got partway. Abandoning undoes nothing; a person must check what a
half-done merge changed.

### A hold whose first merge failed partway stays held (18 October)

**What it was.** Approving a hold runs one of three actions (combine, link as versions, merge debris
into an existing book), none all-or-nothing. When the first try failed after changing part of the
library, the hold returned to the queue looking untouched.

**Why it mattered.** The library was half changed while the hold looked untouched, so a
reviewer's reject or different action would be recorded on top of changes nobody had accounted
for.

**The fix (owner decision of 18 October).** Only a failure that changed nothing (such as a refused
precondition or the iTunes protection) returns the hold to the queue. Any failure not known to be clean, and any crash, holds it, with the same way out as a
failed second try (approve again to finish, or abandon). A classification mistake can only keep a
hold longer, never hand back a half-changed library. Approving many holds at once reports a
held one as skipped, with the reason. This has not been run against the production library.

### The old Dedup page is gone; Review does all of its jobs (18 October)

**What it was.** The Dedup page was an older second screen with nine tabs for duplicate books,
authors and series, overlapping the Review page with different data; its Duplicate Scan tab produced
results that never reached the review queue.

**Why it mattered.** Two screens gave two answers to "is this a duplicate?", and a decision
made on one was not always visible on the other.

**The fix.** Sixteen changes over several days moved every job into Review: filters for how a pair
was found, keep-older or keep-newer for every matching pair, a Clusters view, spreadsheet (CSV) export, Labels and
Suspicious views, a new Authors & series lane (Duplicates, Metadata, Review queue, Authors & series,
Repairs are now the five tabs), and series pruning, missing-file matching and split-book detection
as try-then-apply repairs. Version Groups (covered by the exact-match filter plus the Clusters view) and Duplicate Scan
were retired; the seven server requests
behind them answer "gone" for one release. Every old address redirects to the matching place.
"Reset all audio fingerprints" was deliberately not ported: on production it would wipe every file's
seven stored audio-fingerprint segments and the audio-match suggestions waiting for review, and queue a rescan that production refuses to do, and the Mac workers do not
currently write those fingerprints, so the data would be lost with nothing to rebuild it (the
owner decided it stays out until that is fixed; the old server request must not be called). The
Library page's own "Re-fingerprint ALL files" button is unchanged.
Follow-ups: remove the seven "gone" requests one release later; teach the Macs to rebuild the
fingerprints, after which the reset may return to Review with a warning and a check that a
worker is connected; compare split-book and fragment repairs; make "keep recommended" a server
answer so it can apply across pages; move clustering to the server so the Clusters view is not
limited to the pairs on screen.

### The Review page can no longer load the whole library into your browser (19 October)

**What it was.** The Metadata tab could fetch every book awaiting review in one huge download and
sort, filter and count in the browser, which let one Review tab grow to many gigabytes.

**Why it mattered.** The browser, not the server, carried the whole review backlog, so one open tab could grow to many gigabytes.

**The fix.** The tab asks the server for one screenful plus the chip counts, as it has in production
since 18 October 16:05 with no review errors. The owner waived the usual one-release wait on
19 October, because no full release had gone out since the switch was turned on. Nothing is
written by either way of loading, and no book data was touched. The switch is gone (an old settings file that mentions it still loads), and the old
request answers "gone" and is counted on the latency dashboard, which now times the screenful
requests against a 3-second target for the slowest 5 in 100. Two faults of the screenful
version were fixed: the red "Rejected" mark never showed, and a reviewer without the settings
permission fell back to a default apply limit. Changing the Title box again drops selected books in
every chip whose title no longer matches.

### The split-books repair stopped proposing to merge different books (20 October)

**What it was.** The repair decided a folder was one book when files shared an author and series and
carried a run of numbers, which a series of separate books also does.

**Why it mattered.** Approving such a row would have silently combined distinct books into one entry,
with files, progress and identifiers following the first book.

**The fix.** A folder is no longer treated as chapters when it shows signs of several books:
different volume numbers, differing subtitles ("Episode 1/2"), Roman numeral or word volumes, two or
more different store IDs, a "Books 1-4" or "Series" folder, a run of years, copy-numbered files,
subfolders naming different things, or any single file as long as a whole book (two hours). A folder whose books have no file
records, or whose file lengths are unknown, is also held. Those
folders appear as held with reasons, not merged. Of the 64 rows that were offered, 49 could be
checked offline: 35 are no longer applicable (29 wrong rows plus about 6 genuine chapter splits) and
14 stay; 15 very large folders need a fresh trial. Of the 29 wrong rows, 3 are now skipped for
having no file records, 2 are held as distinct works and 24 by the two-hour limit; 27 of the 29
also trip a name or store-ID rule. These are offline estimates: exact counts come after the next
scan and a fresh Repairs trial. Every rule holds a row rather than merging it, so
a genuine split can be held, for example when a chapter file runs two hours or more, the folder
is called "Series", its parts say "Episode" or "Book One", or a chapter number is 1900 or higher;
the 6 genuine rows are held and need the owner's review by hand. Folders of short novellas with bare numbers can still be offered, so read each row's titles
before applying it. The confirmation before applying lists the rows (title and number of books,
ten at most, then "and N more"), the "Apply finished" message names the rows applied, a refused
suggestion stays on the list with nothing changed, and a practice run of the batch merge says
which would be refused. Saved
suggestions are rechecked at merge time. The Split Books scan should be rerun after deploy.

### Retiring single-file copies of multi-file books, safely and undoably (22–23 October)

**What it was.** Many multi-file books also exist as separate one-file books that are copies of one
track; the clean-up noticed them only when names looked like chapters. Retiring a book also dropped
its ratings, notes, and its places in collections and playlists, and undoing in any order but newest
first put books back in the wrong list position.

**Why it mattered.** Duplicates inflate the library, split progress and make the multi-file book look
incomplete. A wrong position in a hand-ordered playlist is a silent error.

**The fix.** A proposal now recognises a copy by file fingerprint, offering one only when exactly one
other book owns a matching file and the evidence holds (both files exist, are non-empty, same size,
not the same file, not a short intro or credits clip; large files are compared in full). Anything
else is held with its reason. The proposals appear as a preview in Repairs, with held copies and
their reasons; nothing changes until the owner applies rows. Retiring carries ratings and notes (into empty fields only) and
collection and playlist places to the survivor, recorded for undo; this applies to all three
retire repairs (this one, duplicate copies and consolidation leftovers). The undo refuses to
overwrite something the owner edited since, and a store that cannot reach collections or
playlists refuses the retire before writing anything. Each retire records its neighbours,
journal entries carry a never-backward sequence number, and undo rebuilds exact order whatever order
undos run in; a hand-edited list is left as the owner placed it and reported. The list and the
"undone" mark are written in one step under a per-list lock. Tests ran the real undo against more than
210,000 simulated histories: 140,000 random (all exact), 47,000 adversarial (all exact), 14,500 with
leftover notes (11,500 exact; of the 3,000 also hand-edited, 1,314 exact, 1,485 restored with the
deviation reported and 201 refused for manual review) and 12,000 with hand edits afterward (5,716
exact, 5,544 restored with the deviation reported, 740 refused with a reason); none ended wrong
without a report, and the tests caught 35 of 35 injected faults. A leftover note whose record is
missing is ignored and reported instead of keeping a book in a list. Around 52 proposals are expected
(a prediction). The first start after deploy reads the whole undo journal once, logging progress every 50,000
records; 500,000 records took under a second on a development Mac (about 680,000 a second), so
even several million should take seconds, though it was not measured on production. The server
does not serve until it finishes, and the read is skipped after that.

**Still open.** The ordering fix cannot repair entries already written out of order, and the
locking protects only a single running server.

### A safe preview for cleaning up trashed duplicate books (22 October)

**What it was.** Tens of thousands of books were merged into another copy and left in the trash.
Moving their file records onto the kept copy is the last step before purging, so it needs a careful look.

**Why it mattered.** The real clean-up will touch every one of those books just before the
trash is purged, so the owner needs to see how each would be handled before anything changes
them.

**The fix.** A preview-only repair sorts every trashed book into a plain group ("would be retired",
"left alone, the owner rejected this pairing", "needs approval because a file is in the iTunes folder",
"too recent"); Doctor Who, Big Finish and Torchwood books get their own "owner applies by hand" group;
books removed by an undoable combine are flagged, so a later clean-up can be reversed by
undoing that combine, and their count opens exactly those books. It has no Apply button, the server refuses applies, and no book, file record or file on disk
is modified.
Every Review repair plan is now stored as a small summary plus one record per row (this one was about
39 MB as a single record), so each plan must be run once more after deploy (opening an old plan shows a "Run plan again"
button; nothing is lost, since plans are only previews); first start empties the old
huge records (keeping each plan's summary, skipping and logging a damaged one instead of
stopping start-up, and logging how much space was freed) and disk space returns only after the database compacts and old snapshots are purged. Checks
on the iTunes folder can no longer hang on a dead network drive (15 seconds, then "could not tell" and the
book is skipped); a new dashboard gauge counts file checks stuck on a dead network drive, and
it should read zero.

### Retired versions: a trashed duplicate's files kept under the live book (23–27 October)

**What it was.** A trashed duplicate's files were stranded, so purging the trash risked losing them or
their identity, and several systems (dedup, the iTunes importer, the scanner, torrent linking) could
treat a stranded file as new content.

**Why it mattered.** Files must never be deleted when a record is folded away; the owner's rule is to
repoint them. Without a first-class "retired" state, the clean-up could not be built safely.

**The fix.** A nine-step series, of which five steps had merged by 27 October, added a retired state: a file row carries a retired flag and a version carries a retired
status with its source book and operation, retiring re-keys the row under the live book and never
deletes it, and every default read, count and total skips retired rows. Single lookups answer the live
file only, with special readers for retired holders. Locks cover retire, restore and re-home so a
concurrent fingerprint or transcript write is not undone. Callers know the difference: dedup, the iTunes
heal and regroup, and Deluge linking skip retired rows; the iTunes importer treats a track held only by a
retired row as already in the library (no second book); the scanner treats content matching a retired
version by hash as known and skips it; path-claim checks count retired rows as occupying their path; the
iTunes track audits report them separately. A resolver follows a retired shell to the book holding its files
and the clean-up's plan holds any row the owner rejected, and refuses to run if the rejects cannot be
loaded. A retired-versions panel on the book's Files and History tab lists them with a Restore action and
a "Show retired files" switch (retired rows are read-only). The step that actually retires files and the
undo path were still in later pull requests at month end, and restore does not yet write a journal entry (and refuses a shell that was already purged).
Nothing in the merged steps retires a file yet, so running behavior did not change: 17,602
owner-approved rows wait for the apply step, which ships behind a switch that is off by default
and is to start with a 100-row trial and then go in batches by category.

### A permanent record of every accept and reject decision (21 October)

**What it was.** Owner decisions to reject or approve a proposal had no durable, shared home, so each tool
kept (or lost) its own idea of what had been turned down.

**Why it mattered.** Without one shared record, a rejection one tool remembered meant nothing to
another, so a turned-down change could be proposed again; this store is step one of the plan to
make a reject on any proposal stick everywhere.

**The fix.** A decision store (step one of the "reject on every proposal" plan, no screens yet) keeps an
append-only log of every reject, approve, apply, undo, unreject, auto-apply, reopen and skip, with evidence,
reason, actor, source and time, and a stable fingerprint for each proposal. Pair vetoes follow from
rejects; a reject applies to a whole question family (rejecting a split-books pairing also stops fragment
consolidation and folder-books proposing it). Reopening allows new suggestions but never lifts the veto on
the originally rejected value, an unreject needs a human, and anything unclear (such as an unknown kind of question) fails closed. At
first a book merged into another was not followed to the book it merged into; the retired-versions
step merged on 27 October closed that, so a veto naming a purged, retired or merged-away book holds.
Nothing ever prunes the record, and tests prove that every listed clean-up and expiry job leaves it
byte-identical and that a backup restores it.
The owner approved the one-time baseline exception in the dead-code check for the new package on
21 October.

## 5. Trash, deletes, purges and files on disk

### The trash keeps your listening progress (5 October)

**What it was.** A week earlier the nightly trash cleanup had learned to skip any book that someone had listening progress on, so progress was never lost. Nothing ever cleared those books, though, so they sat in the trash forever with no explanation.

**Why it mattered.** Progress is the one thing the library cannot rebuild. Keeping it was right, but a trash that slowly filled with books that could never leave, and never said why, was not.

**The fix.** The cleanup now moves the progress (where you stopped, finished or not, time listened, bookmarks) onto another live copy of the book, but only a copy the audiobook apps can see. It uses the same all-or-nothing move as the merge tools: if the move does not finish, nothing is deleted. When no such copy exists the book stays in the trash, marked "has progress" with whose progress it is, and a new "Discard progress and purge" button lets the owner drop it on purpose after a confirmation naming the book and what will be lost. That button works only on books already in the trash, and every use is written to the activity log; when another copy exists, the trash list offers "Move progress and purge" instead. The nightly report gained three counts: progress moved, books kept for progress, books kept because a move failed. A review found gaps that were closed in the same week. "Purge now" and deleting from the library, alone or in bulk, now follow the same rule; "Purge now" used to delete a trashed book and its progress straight away, and now moves the progress first or stops and explains why. A bookmark alone now counts as progress. Combining two copies no longer lowers total listening time (it keeps the larger figure, for example 5,000 seconds rather than 200, and never adds the two together). Putting progress back after an undo is now one step that either fully happens or not at all (before, a failure part-way could leave a book with no saved position), and a related fix stops an old, undated position from overriding a newer choice to mark the book unfinished. The trash list shows whether a target copy exists, shows "progress unknown" when it cannot be read, and shows each person their own progress plus a count of other people with progress (people who manage users see everything). Moving a copy to the trash or purging it no longer removes an iTunes track that another copy still uses, and a purge changes nothing in iTunes unless the book was really deleted.

### Trash cleanup only deletes files inside the library (6 October)

**What it was.** The cleanup can also delete a trashed book's audio file from disk (when that setting is on). It was meant to do so only inside the library folder, but nothing checked. A book pointing at a file in a neighbouring folder, a downloads folder or through a shortcut leading outside could have had that file deleted.

**Why it mattered.** Audio deleted from disk cannot be brought back, and these were files the cleanup was never meant to touch.

**The fix.** A file outside the library folder is now always kept and named in the cleanup report. The same review round fixed two progress edge cases: an undo of a merge could move a listener back to an older spot if they kept listening during the undo (it now checks for newer listening right before writing), and undoing an Audible import now restores positions in one all-or-nothing step. Books refused on every run because their link history is damaged now say so instead of a vague "could not read", and a book that still owns files, or whose progress could not be moved, gets its own error so the trash page shows the right message.

### Trashed book versions really get 14 days (11 October)

**What it was.** Sending one copy ("version") of a book to the trash promises 14 days to change your mind. The daily 6 a.m. cleanup counted those 14 days from when the copy was first added to the library, not from when it was trashed. Any copy more than two weeks old was therefore deleted from disk the next morning after trashing, anywhere from minutes to 24 hours later. Separately, "Purge now" would accept any copy on the server side, including the one you listen to, though the page only offered it for trashed copies; that would have deleted the book's playable files. Both were found in code review.

**Why it mattered.** Deleted audio files cannot be brought back. Library records were always kept, but the files are gone, and this fix cannot restore copies the old cleanup had already removed.

**The fix.** Each copy now records when it was trashed; restoring clears the record and trashing again starts a fresh 14 days. Copies already in the trash have no such record, so the first cleanup after the update gives each a full 14 days from that run and deletes nothing that morning. "Purge now" is refused for anything not in the trash, and the trash page shows the trashed date. Until the update was installed, pausing the "purge trashed book versions" job stopped further deletions.

### The playing version's files are no longer deleted from the trash (merged 27 October, pending)

**What it was.** Trashing the version a book is currently playing left its files in place, and the nightly cleanup deleted them 14 days later, leaving the book pointing at a missing file.

**Why it mattered.** Deleted audio files cannot be brought back.

**The fix.** The cleanup now never deletes a file the book still plays; a version whose files are still in use stays in the trash and is reported instead. Trashing the playing version will switch the book to another copy first (follow-up).

### Purging no longer destroys undo, and merges and purges stop freezing the page (22 October, final polish to 26 October)

**What it was.** Four problems around the Trash, "Merge as versions" and "Merge into one book". The serious one: some operations (combining several books into one, a folder regroup or retire, an automatic duplicate merge) leave a leftover book in the trash so they can be undone, and "Purge soft-deleted" did not know that. It deleted them for good while the undo was still possible, so the undo could no longer restore them and one that had already moved some books stopped half-way. The night before the 21 October reports, the purge had removed 21 such leftovers. Also, three buttons waited for the whole job inside the web request and hung the page on a real library; the purge showed no result (not how many were removed, why others were kept, or that it was cancelled early); and "Merge as versions" failed when one book was already in the trash, yet the page said "Merged" because it only reported that the request was queued.

**Why it mattered.** The first could silently make an operation impossible to undo, which is data loss. The rest left the owner unable to tell whether a merge or purge had worked.

**The fix.** Every purge path now keeps a leftover book that an unreversed combine, folder operation or automatic merge still needs ("kept: undo possible"), rechecked per book at the moment of deletion; if those records cannot be read at that moment, the book is kept and the reason shown. The purge refuses to run at all until the operation-record index is built after a start. Undoing an automatic merge now checks that the merged-away book still exists before moving anything, so it can no longer stop half-way. Apart from these three kinds of undo, which books can be purged is unchanged, and books that still own files are never purged. Merging as versions, merging into one book and purging now run in the background: the dialog closes at once, a notice links to the operation, and the true outcome (merged N, skipped M, or failed with the reason) appears when it ends; requests that cannot succeed (a book that is gone, already in the trash, or in the iTunes library) are refused up front. Books with a merge or purge already running cannot be submitted again, and if the page loses contact with the server it says it is still checking rather than calling the work failed; it keeps following running merges and purges across page changes and reloads. Purge shows a preview first ("N can be purged, M are kept and why"), then a progress bar, then a full breakdown, and the trash list shows purgeable versus kept counts. Merge as versions skips books already in the trash and shows which copy is kept, with its title, file count and location, and lets you choose. Deleting a book no longer blocks its file fingerprint by default, and never when another live book has the same file, because a blocked fingerprint makes the scanner skip that other book. Merging no longer refuses because a version group holds an unselected iTunes copy (owner-approved 22 October), since only its group marker in the database changes, never its file or tags; an iTunes book you do select is still refused, and combining books, which moves files, still refuses iTunes books. The iTunes track check no longer calls a track "left over" when its book is a version in a group that still has a live main copy. Organizing and the duplicate scan now look at every book holding a file, so the oldest copy is no longer mistaken for itself and let through.

Over the following days thirteen review rounds tightened the purge. Together they added: a purge deletes only what the preview showed, leaves alone books trashed after the preview and books the preview showed as kept, and is refused if no preview exists or the server no longer remembers it (after a restart, or once more than four newer previews have run); with "also delete files" the preview lists the files to be deleted and kept, and a file is never removed while any trashed book still names it, while the nightly purge still works by its retention window; files are decided first and written to a pending-removal list before any book is forgotten, so a server crash cannot leave files behind and a file is rechecked (same size, time and place) just before deletion; a purge refuses to remove files when the library folder looks unmounted (a share that has not mounted looks empty, so every file would read as "already gone"): it samples up to fifty of the library's own files spread across it and needs more than half visible, start-up retries for about fifteen minutes, and a hung share is given up on after ten seconds; a disk or share hiccup no longer counts as a missing file; waiting file removals are carried out at start-up, and the purge window offers "Finish removing N files" when only those remain; a purge that loses the library folder mid-run stops once with one reason instead of failing every remaining book; the preview says up front whether deleting files would be refused and why, and "Purge now" and the batch delete answer a clear "the library folder is not available"; files whose folder is missing are kept and counted separately, and the owner can review and, after confirmation, forget only those pending-removal notes, never a file or book (logged with who did it and every file; if the folder later returns, its files are still on disk and a scan would import them as new books); a hung drive cannot pile up stuck checks; single-book purge follows the same order; trash times are recorded for every route into the trash so a re-trashed book gets a fresh window, and a trashed book with no recorded trash time is judged by its last change instead of being treated as long overdue; and the nightly purge now fails visibly instead of reporting success when it refused or stopped early. The price is that a library that really lost most of its files is refused until they return, because that looks identical to an unmounted share, and a trashed book whose folder is gone, or whose file was deleted by hand leaving an empty folder, cannot be purged with "also delete files" until the folder is readable or removed (a purge without that option still works). Restoring or permanently deleting a book while a combine runs now waits at most 3 seconds and then says "try again in a moment" instead of hanging. A combine that runs out of time or is stopped by a shutdown now offers Undo for what it moved, never leaves a book live with no files, and says how many books it moved and how many it left untouched. The file-lookup change also gave each book its own "is this file already here" entry, so one book's change can no longer erase another's; a repair that runs on every start rewrites any missing entry, including ones the 21 October purge may have removed. The purge and combine requests now return an operation to follow instead of the finished result, so direct callers must read the outcome there. This was merged late on 26 October.

**Still open.** Kept leftover books stay in the trash until their undo is run, because those records never expire; older folder-regroup leftovers now show as "kept: undo possible". One damaged combine record pauses all purging, the nightly one included, until fixed or removed (a read-only check lists them). Right after the update, a duplicate check can briefly miss a book until the first repair finishes, and rolling back to an older build afterwards finds no matches by file fingerprint until you go forward again.

### Bulk delete no longer moves progress off a book it then keeps (25 October)

**What it was.** A bulk permanent delete first moved each book's listening progress to another copy and only then tried to delete the book. A book that still has audio files is never allowed to be deleted, so the delete was refused after the progress had already moved, leaving the book in the library without its progress. The single-book delete and the trash purge had the same weakness in smaller form. The bulk delete is an API operation the website does not offer, so exposure was direct API use.

**Why it mattered.** The book stayed in the library but no longer showed its listening progress, which had moved to another copy, while the delete itself was refused, so the listener lost their place for nothing.

**The fix.** All three paths now share one check that looks at files first, again at the last moment inside the protected step, and also counts files kept for older versions (which cannot be moved). A book with files is skipped before anything about its progress is touched, with the reason stated, and counted as not deleted; books in the same request that have no files are still deleted, with their progress moved to the Audiobookshelf-listed copy as before. A single-book delete caught by the last-moment check now changes nothing, not even the "block this file from being re-imported" mark, and the nightly report counts each refused book once. If a file still slips in afterwards, the message says plainly that progress moved and the book stayed; "Discard progress and purge" now keeps a record of what it erased in that case. The same change closed a permissions gap: anyone allowed to edit metadata could permanently delete, trash or restore books in bulk through the API. Those now need the delete permission (trash and permanent delete) or the organize permission (restore), a bulk edit that quietly changes a trashed book's state is checked the same way, and a bulk request with no permission information is refused (nothing in the app sends one today; this guards against a future feature doing so by accident). Ordinary bulk edits of titles and authors still need only the edit permission, and a book the person may not change is reported as not changed, naming the missing permission, while the rest of the request goes ahead. The website's own buttons already checked permissions, so this gap was reachable only through direct API use. The library page greys out the buttons with a reason on hover (on a brand-new install with no accounts yet they stay enabled), and purge and discard messages no longer tell you to trash a book that is already in the trash. Anyone whose progress looks reset after an earlier bulk delete will find it on the Audiobookshelf-listed copy in the book's version group, unless that copy was later removed.

**Still open.** A file attached by the folder scanner or an import in the instant after the last check could still slip through (the store's own refusal is the final guard). The duplicate clean-up and the iTunes regroup share the same gaps and are filed as follow-up work.

### Organize stays inside the books or folder it was given (10 October)

**What it was.** The organize operation accepted a folder setting and ignored it, so asking it to tidy one folder organized the whole library: books were copied or linked into the library folder and others renamed. Two more ways to spread were found: organizing a few chosen books with "fetch metadata first" then organized every book, and an empty list of books meant "all books". Nothing in the app's own screens sends a folder, but the operation could be started directly.

**Why it mattered.** Organize moves and renames real audio files, so overreach changes the library without anyone noticing until files are not where they were.

**The fix.** The limit is honoured everywhere. A blank, relative or self-escaping folder stops the run before it touches anything, and an empty book list means no books. A book is organized only if every file it would touch is inside the folder, rechecked right before each book, so a stale file record, a mixed inside/outside book or a book moved by a concurrent scan is left alone and reported. The limit chooses which books are organized, not where they go: they still land in the library folder, and empty folders are tidied up to, but never above, the library folder. Each case has a test that failed before the fix.

### Deluge imports can no longer overwrite a book or point two books at one file (11 October)

**What it was.** Three tools copy audiobooks downloaded by Deluge (a torrent client) into the library: the bulk import, "centralize" and the one-at-a-time import. Only the last had safety checks. The bulk import could overwrite a same-named file that was a different book's audio, and both could point two books at one file when two downloads shared a name and contents (so deleting or moving either took the other's audio), and copy files out of the iTunes library or other protected folders, skipping the preview-and-approve step, and could ask Deluge to move the torrent. It was found in code review of the new tool linking files to torrents.

**Why it mattered.** An overwritten file destroyed another book's audio, and two books sharing one file meant deleting, merging or moving either one also deleted or moved the other's audio.

**The fix.** All three now use the same import path, which locks the destination, refuses a destination another book already uses and refuses protected sources such as iTunes. It reuses an existing file only if it is a regular file with identical bytes (otherwise the import stops and asks you to remove the leftover copy), and removes its own copy if saving the database change fails. When Deluge cannot be reached, books are reported as "Deluge list not loaded" instead of "protected", and the bulk import asks once instead of per book. The new tool linking library files to torrents now works in rounds and stops early enough to save progress before its 4-hour limit, so the next run continues instead of starting over. No file record is deleted, and a refused import leaves the book waiting.

### Repair for imports made before the 10 October fix (11 October)

**What it was.** Until 10 October every import recorded a version of the book (source and torrent) but never attached the file to it. The fix stopped new cases but left the old ones.

**Why it mattered.** Once a file is attached, purging that version deletes the file from disk, usually one in an import or Deluge download folder, whereas before no audio was deleted. A version swap also moves an attached file only with its own version. Trashing gives 14 days (counted from the trash date, per the fix above), after which the daily cleanup deletes the file; "Purge now" deletes it at once, but only for a version already in the trash. Every file imported since 10 October already works this way.

**The fix.** A new "Link pre-fix imports to their versions" repair on the Repairs tab runs as a trial that changes nothing. It lists every candidate with the book, the file, the proposed version and why, grouped with counts, and only rows the owner selects are written. Only two kinds are ever attached: a file whose book has exactly one unattached version with the same torrent (or none, for manual imports), and a file of the same torrent as a sibling already attached. Nearly every row will be the first kind, so each row shows whether version and file agree (created together, consistent format, still in an import folder, not marked missing) and disagreeing rows are marked for review in groups by the first failed check. The date check goes last because it is the noisiest (every import before 25 August fails it, since its file record was created later by a scan), and the trial log counts how many rows each check flags so the owner can see whether one check flags nearly every row. Everything ambiguous is listed and left alone. It never creates, deletes or changes a version or file record, rechecks right before writing under the locks that stop a merge or import running, refuses a row changed since the trial and keeps unrelated edits made at the same moment. Missing checksums are filled in where the file is readable, otherwise the backfill job does it later. The importer's own linking step now has the same protection, and running the repair again finds nothing new. Each link is recorded in the operation's history, but the Operations page undo does not unlink these files, because reversing a link would only bring back the broken state.

**Still open.** It has not been run on production; it needs the owner's review of the trial list and an apply of the chosen rows. Trashing a version in the same instant as the write can still win, and if that happened before the 14-day fix was installed, the next daily cleanup would delete the file.

### Books whose files went missing, reviewed one row at a time (14 October)

**What it was.** The "Match missing files" scan finds books whose file is no longer on disk and looks for it elsewhere, by content or failing that by name. Its results were reviewed on a tab of the old duplicates page, which is being retired.

**Why it mattered.** The old apply job updated only the book and left its file record pointing at the missing file.

**The fix.** A "Missing files" repair on the Repairs tab shows one row per book: where it used to point, the matched file, how and how surely it matched, and books with no candidate are listed too. It reads the newest scan that finished with readable results, so a later failed scan does not hide a good one. A folder that has come back counts as present, and an unreadable location is reported as an error, never as missing. A match is offered only while the book still points at the missing file, that file is still gone, the match is still on disk and unchanged, and no other book uses it. Rechecks happen at apply time and again just before writing; a changed row is refused. iTunes books and matches inside the iTunes library are skipped. By owner decision (14 October), applying points the book at the file and then moves its file record, each recorded so undo restores both; an interrupted apply is finished by resuming it, and however the applies are undone, the book and its file record never end up pointing at different files. The repair no longer uses the old apply job, and a book something else has already pointed at the matched file is shown as changed and not applied. A file matched to several books skips all of those rows for a person to decide. Content fingerprints are kept for content matches and cleared otherwise (undo restores them), and right after a restart the repair says the file index is warming up instead of guessing. Only administrators may run it; it is the first repair that needs more than the edit-metadata permission. Nothing is moved on disk, and no file record is created or deleted.

**Still open.** Cleared book-level fingerprints are not refilled automatically until a scan or organize, and whether the repair should do it is a TODO for the owner; the file-level fingerprint returns only when someone runs the file-hash backfill job, which is not scheduled and previews by default. The old apply job still updates only the book's main location, and which permission it should require is also the owner's call.

### Two library jobs no longer silently undo each other's file-record changes (25 to 27 October)

**What it was.** The library keeps one record per audio file (location, content fingerprint, length). Several jobs read a record, change one field and save it, and the save only checked that the record still existed. If another job changed the same record in that gap, the slower job put the old values back and left the lookup tables for content fingerprint and iTunes ID pointing at values the record no longer held. The two whole-library sweeps (clearing fingerprints, removing leftover fingerprint columns) were worse: they read every record up front and rewrote them minutes later with no check at all. No loss has been found in production; it needed two jobs on the same file at the same instant and was found in code review.

**Why it mattered.** The only symptom would be a file that later looked unscanned or a duplicate that was not recognised, with no error to trace it.

**The fix.** Before saving, the library now checks the record is exactly what the job read, under the lock. On a mismatch the job rereads, redoes its own change and retries, and after repeated failure reports the file as busy rather than writing something stale; batches wait a short random time between attempts. This covers every updater (hash and metadata updates, field patches, intro transcripts, scan bookkeeping, file moves between books, retiring and restoring versions), the intro-timeout writers, and deletes, which had the mirror-image gap. The sweeps now save 64 records at a time under the same check, follow a record moved to another book, and give busy ones a last pass; a record still busy is counted and left alone, the segment cleanup no longer marks itself finished when it skipped files, and the fingerprint reset says "incomplete", keeps its search index and asks to be rerun (the reset job itself still finishes without an error; only its final message says it is incomplete). The file-hash backfill counts busy files separately and retries them next run instead of treating them as write errors.

Several review rounds then caught: a bulk delete now names the book each record should belong to and refuses any record moved elsewhere meanwhile; a busy batch refuses only the changing records and saves the rest; very large deletes run 64 records at a time, since thousands at once could have used a gigabyte of memory, every reason to refuse is checked for the whole delete first, and if a later group fails, the records already removed are reported with the error; the duplicate-removal job's undo log stays accurate when a delete removed nothing or removed a slightly different record; combining books when files are busy is answered "try again later" before any file moves, instead of leaving the combine half done, and the message no longer says a still-loading library is the only possible cause; damaged records filed under the wrong book are reported by name instead of being treated as missing and retried until they look busy. Tests deliberately make a second job change a record in the gap, and fail if the check is removed. This was re-cut as one change and merged on 27 October.

## 6. iTunes: from two-way sync to import-only

### Edits were silently thrown away on the way to iTunes (7 October)

**What it was.** When a book is edited, the organizer is meant to copy the change (title, author, file location, removals) into the iTunes library. Since iTunes was pointed at the organizer's own copy of the library, a safety check had refused every one of those updates, because it mistook the library's own music folder for a leftover temporary folder. After three refusals the change was discarded and only a log line recorded it. For removals it was worse: the track was marked removed in the database before the attempt, so the database said "gone" while iTunes still had it.

**Why it mattered.** The surviving logs (from 23 September on) show 662 batches discarded, about 4,300 book updates and one track removal; that removal came from merging two copies of a book on 6 October, and the track is most likely still in iTunes. The problem very likely began at the switch (iTunes last saved that library on 28 July), but older logs are gone, so earlier losses cannot be counted. The lost updates cannot be recovered from logs, only regenerated by comparing the database with iTunes.

**The fix.** The check now recognises the organizer's own library folder and nothing else. Waiting updates are stored in the database so a restart cannot lose them, and an update is never discarded: failures retry after a minute, then less often, at least hourly. A removal is recorded only after iTunes has really changed. A status view in the API shows how many updates wait, why the last try failed and when it will try again (it has no screen in the web app). Unusually large removal requests are held for the owner to approve. Backups are now made only before a real write (failed attempts had filled all five backup slots with identical copies, pushing out older useful ones), and a separate bug that blocked any removal of more than about 27 tracks was fixed. Deploying this would have turned write-back on for the first time since July, so the plan was to close iTunes on Windows first and optionally start in dry-run mode. (Superseded by the next section, which removed write-back entirely.)

### Owner decision: drop write-back, import only (7 October)

**What it was.** The owner decided: "Drop itunes writeback. We will do import only and then not care."

**Why it mattered.** Write-back had failed quietly for weeks, and any write to that file lands in the library iTunes actually opens. With no writer left, nothing can damage the iTunes library by mistake.

**The fix.** The organizer still reads iTunes: import, sync from iTunes, play positions, finished books and the iTunes ids kept on each book all work as before. Every path that could change the iTunes library file is gone: the background push queue, rebuild, relocate, cleanup of merged tracks, upload, restore, and the "Write Back to iTunes" and "Force Sync to iTunes" buttons. Editing, merging, organizing or deleting a book now changes only the organizer's own data and files (writing tags into audio files is a separate feature and is unchanged). Leftover queue entries are deleted once at startup. The library file setting stays only for a process check and the download button.

**Still open.** The host's old iTunes write settings can be removed after deploy, and a few follow-ups are listed in the removal plan.

### One manual import, safe to run again (8 October)

**What it was.** Besides the import, the app had a second incremental "sync" that could run before every organize (from a switch that was on by default) or from a "Sync Now" button, plus a conflict dialog wired to a server route that never existed. Re-importing also only filled iTunes play data where a book had none, and in organize mode it copied every book in the "imported" state into the library folder, not just the ones that run had added.

**Why it mattered.** Two ways to bring iTunes in meant two sets of matching rules, and one could run unasked. With the sync gone, re-importing is the only way to bring newer play data across, so it had to be able to update existing books. A re-import would have moved books it had only linked and changed where the app thinks their files are. The owner wants iTunes brought in only when someone chooses.

**The fix.** There is now a single **Import iTunes library** button; the background sync, the pre-organize sync step, the schedule setting and their buttons, including a "Force Import" button that carried a misleading warning, are removed (old saved settings and server settings that mention the sync still load and are ignored). Each album is first matched to an existing book: skipped if its iTunes id belongs to a deleted book, otherwise linked by that id, or by file path, or by holding one of its tracks. Only unmatched albums become new books, so a second import of the same library adds none, a rule checked by importing the same made-up library twice. A link refreshes play count, rating, bookmark and last-played date, but only the fields the file actually carries, so importing from the binary format never wipes a bookmark. Organize and metadata lookup act only on books the current run added, so files never move on a re-import. If any book is already linked to iTunes, the button first warns that albums iTunes re-created with new ids, or whose files moved, cannot be matched and may appear as duplicates, and offers Import anyway or Cancel; the warning also says nothing in iTunes changes and no files move, and it appears whenever the app cannot tell whether books are already linked. The result panel reports linked, added and skipped counts. The merge safety check that refuses merges when an iTunes library is known to exist but its folder is not configured now keys on the configured library file instead of the removed sync switch; on an install with no iTunes paths at all, merges are no longer refused, and the built-in guard on the standard iTunes media folder still applies. Rules for deleted books and for iTunes ids following books through merges and repairs are unchanged.


## 7. Security and access

### API keys expire and can no longer become a signed-in admin (7 October)

**What it was.** An admin API key could reset any user's password or create a new admin, sign in as that user and act as the owner, including on the Repairs actions meant only for the owner. The emergency "bootstrap" key was assumed to expire after 8 hours, but only the local copy of it was deleted; the key itself worked for 30 days. Keys could also be created with no expiry.

**Why it mattered.** A stolen or leaked key was effectively a full account takeover, and the owner-only approval could be satisfied by anything the server itself could create or reset.

**The fix.** No API key can now change a password, create or invite a user, switch an account on or off, make a sign-in link, make a key for someone else, wipe or restore the database, or change who may sign in; each gets a clear "sign in to do this" refusal. Keys also cannot change where the server reads, writes or runs things (library, backup and database locations, iTunes paths, helper programs, plugin settings, installing tools, applying updates, adding scan folders). The bootstrap key now really stops after 8 hours and can never be stretched beyond a day. Every key expires: new keys last 30 days by default, up to a year, and "Never" is gone. Older never-expiring keys received a 30-day expiry at first start, listed in the server log, and the keys page shows expiry dates. Owner-only actions (the Repairs "Apply (owner)" button, releasing held iTunes track removals, and rebuilding, relocating, re-approving after a swap, uploading or restoring the iTunes library file) now count only a Cloudflare Access login whose email matches the owner email set on the server; password, Google, GitHub, temporary and invite sign-ins no longer qualify. Only that owner login can change the owner email or the Cloudflare settings, or wipe or restore the database; a reset no longer clears an owner email set on the server, and none of the owner's own API keys can act as the owner. The owner email can be set only on the server; until it is, nobody (not even the owner) can set it or the Cloudflare settings from the web app, and owner apply is off for everyone. Any admin can still run previews of these actions, each one is logged with who did it, and a request carrying both an API key and a Cloudflare login is treated as the API key, with all of a key's limits. Doing these things while signed in works as before, saving the settings page unchanged still works, and a key made by the bootstrap key cannot outlive it. Four security review rounds each found gaps that were closed: setting names spelled with different capitals bypassed the check, five pages had no guard, and keys with no expiry counted as eternal (round one); look-alike Unicode letters (a Kelvin-sign "K" matching "k") let a different Cloudflare account pass as the owner, and a click-approval could be reused by another job (it now works only in the job it started and stops working if the owner email changes) (round two); any admin, or anyone with a stolen password, could change the owner email to their own or point the Cloudflare settings at a sign-in service they control (round three); and any admitted user could claim ownership first when none was set (round four).

**Still open.** Integrations with never-expiring keys (a metrics scraper, a fingerprint worker) stop 30 days after deploy unless their keys are replaced. A key with the settings permission can still point outbound services (the AI address, the download client) at another host and so receive the stored password or key; whether those also need a signed-in session is the owner's call.

### Phone apps stay signed in while they are used (19 October)

**What it was.** The audiobook phone apps that use this server were signed out exactly 30 days after sign-in, however often they were used, because the server-side sign-in record had a fixed end date equal to the app's short pass.

**Why it mattered.** On day 30 both expired together, the app could not renew, and every phone landed on the login screen, often mid-listen.

**The fix.** A sign-in now lasts 60 days from its last use (renewals and ordinary use, recorded at most once a day, reset the clock), with an optional hard limit that is off by default. Existing active sign-ins were moved to 60 days from sign-in at startup, never shortened and never revived; those already expired must sign in once more. The server now guarantees the short pass expires before the sign-in, even if the settings are set equal (the old default), by shortening the pass with a warning rather than refusing to start. Changes to a sign-in record now happen one at a time, so a renewal can no longer undo a logout or "log out everywhere" and clean-up can no longer be undone by a late extension. Changing a password, an admin reset, or deactivating an account now signs out every phone for that person, using a counter saved in the same step as the password so there is no gap; reactivating does not bring old sign-ins back, and sign-ins started before the change are refused when finishing. A profile edit or a routine password-storage upgrade made at the same moment as a password change or deactivation can no longer bring back the old password or switch the account back on, and a reset link is issued only after the sign-out counter is saved. Phones signed in through Cloudflare Access are refused once and then re-issued from their Cloudflare identity. Phone-app settings that were saved but silently overwritten at restart are now refused with a message naming the server setting to use. Expired passes are logged as expired rather than invalid, and expired records are kept 7 extra days so returning apps are logged correctly; logging routine expiries as "invalid" had made them look like a security problem and sent an investigation the wrong way.

**Still open.** The web interface has the same gap (a changed password does not end existing browser sign-ins), recorded as follow-up. A reset link does not stop a stolen password working until the person picks a new one.

### Single sign-on in the AudioBooth app no longer crashes it (19 October)

**What it was.** Tapping "Login with OpenId" crashed the app every time. The server replied with the app's own private return address, which the secure browser window cannot open.

**Why it mattered.** Single sign-on was unusable in production, on every attempt.

**The fix.** Sign-in is now two steps. The app registers a request and receives a normal web page address on this server; in the browser, Cloudflare signs the person in and a page asks "Sign in to AudioBooth as <account>?" with Allow and Deny. Only Allow returns a one-time code, valid for one minute and usable only with the proof the app kept. The Allow step exists because nothing ties the browser to the phone, so without it a forwarded link would sign someone in on the first person's behalf. Pending requests expire after 5 minutes, and each Cloudflare credential has caps on how many it can hold and how fast it can start them, with a full list dropping the oldest of the biggest holder, so one person or group cannot jam sign-ins for others. Three automated security rounds fixed the Allow page and limits, group-token device fairness, and an unverifiable client-address header; an independent review found nothing blocking. Tests covered the full flow, wrong and replayed codes and expiry, and all 52 key checks were confirmed by deliberately breaking them. The Allow button was also tested in two real browser engines, which caught a setting that would have made every Allow fail.

**Still open.** The official Audiobookshelf app now gets the format it expects but still cannot sign in here: its first request lacks the extra Cloudflare pass this site requires, and its return address is not on the default list. The step-around for phones that send the group token to the browser is not yet proven in production; it depends on a Cloudflare account setting nobody could read: the server reloads the page once without the group token, and depending on that setting Cloudflare may block the page or the sign-in may end in "access denied". The server now logs which case occurs on each page load.

### A phone browser stuck on the login page now recovers (20 October)

**What it was.** After single sign-on in AudioBooth, Safari on the same phone could no longer reach the web app, and a second app sign-in was refused. The app's sign-in sheet shares cookies with Safari, so the Cloudflare cookie issued to the app's device token replaced the one for the person, and every later visit looked like a device, not an account. (A device token is a machine credential that names no person.)

**Why it mattered.** The owner was locked out of the web app on a phone, and a redirect cannot remove a cookie, so the app's own retry failed the same way. Anyone whose browser held the bad cookie had to clear it by hand.

**The fix.** When a browser page is opened as a device token, the server answers with a small page that clears the bad cookie and returns to the same address, so Cloudflare asks for the person normally. The return trip is marked so it can never loop; a browser still treated as a device gets a plain message with a link to Cloudflare's sign-out page. The app's sign-in step now clears the cookie and retries, giving up only after two tries. App requests, streaming, the Watch app, background web requests and API-key use are never redirected, and a device token is never treated as a person's account. The debug log now reports the sign-in cookie length separately from an unrelated bot-check cookie.

**Still open.** Not proven on a real iPhone against the live Cloudflare setup; tests used made-up data. A follow-up considers Cloudflare's stricter device-token setting once streaming and the Watch app are checked.

### Invitations are emailed, with safe sign-on linking (19 October)

**What it was.** Adding a guest meant copying an invite code from the Users page and hand-sending the Cloudflare connection lines and app instructions. Guests also had to be added by hand to two security-sensitive allow lists, and nothing removed their access when they were deactivated.

**Why it mattered.** Many hand-written messages, secrets travelling through whatever channel was handy, a deactivated guest could keep browser access indefinitely, and mid-build a flaw let anyone type another person's address into their profile and capture that person's first Google or GitHub sign-in (never shipped). A recovery-token gap could open only on a server with no administrator, and nobody who triggered it received a password or key.

**The fix.** The invite dialog can send one email with the sign-up link and setup steps; if mail fails the owner still gets the link, and resend is limited to once a minute per invite. The guest picks a password on a new sign-up page. When an administrator emailed the invite, accepting it can also allow Google or GitHub sign-in and add the address to the one pre-agreed Cloudflare browser rule (a Cloudflare failure never blocks the account, and the Users page says to add the address by hand). An administrator can now set or clear another person's email on the Users page. Deactivation or an email change removes those grants, except for addresses still legitimately used elsewhere. Linking a Google or GitHub login to an existing account now trusts only verified addresses (emailed invite from an admin, confirmed by the provider, set by an admin, or first-run setup); only one account can hold an address as verified, look-alike spellings count as the same, and profile-typed addresses are never verified (the profile page accepts any address and records it as unverified, so it cannot be used to find out whose an address is). When a verified sign-in finds its address typed into other profiles, those accounts keep working but lose the address, and the audit log names them; an account that holds the address as verified is never displaced. Existing accounts were labelled once from their strongest record, so previously squatted addresses are not grandfathered. Invite links are never shown to API keys, and revoke and resend use a separate reference that cannot accept the invite. Mail and Cloudflare secrets live only in the server environment. The emergency recovery token is now checked before any admin account is created, the generated admin password is much stronger, and startup no longer creates an admin nobody can log into. A setting naming the three tunnel relay machines lets the server read each visitor's real address and give each their own invite-page limit; unlisted machines sending Cloudflare headers share a quarter of the overall allowance, and home-network recovery attempts are capped at 50 an hour in total (attempts from the server itself are never counted).

**Still open.** Before deploying, check administrators whose email was last set via their profile page: it is labelled unverified, so a first Google or GitHub sign-in with that address that is not yet linked to an account would move the address to a new account. The server logs a warning naming such accounts at each start until they are merged. Do not fix this by setting the address on the Users page unless only one administrator has it; otherwise leave the accounts alone until they are merged. The same applies when two accounts share an address.

### The public-facing server is harder to abuse (21 October)

**What it was.** An internal review of everything an outsider can reach through the Cloudflare tunnel found several weaknesses.

**Why it mattered.** Together they allowed header-chosen redirect paths, unstoppable floods from inside the network, one attacker blocking every login behind a relay, a missing browser script policy (any injected script would have run with the user's login), and a guest key shared by all invitations that anyone holding a live link could read. One mistaken port forward would also have exposed the whole server to the internet. The redirects never left this site.

**The fix.** Automatic trailing-slash and case redirects are off, so a near-miss address answers "not found" rather than redirecting or showing the web app. The setup and invitation limiters no longer exempt local-network callers from the overall ceiling. Failed-login counting uses the real visitor address (trusted only from a listed relay or verified Cloudflare login; newer-style IPv6 internet addresses are grouped by address block) with one counter per username shared by web and phone login, identical treatment and timing for unknown names, and separate counting of dead phone refresh tokens, so a phone with an expired session can always sign in again. Overall limits on failed attempts, per relay and for the whole service, slow visitors down rather than locking everyone out, and people signed in through Cloudflare are exempt; while those limits are used up, logins are also capped at two per second with a burst of eight (adjustable; a mistyped value stops startup). A visitor is refused outright only after five failures on that name, or one failure while the limits are used up, and is told how long to wait (a few seconds for the overall limit, never more than 100 seconds for any allowed setting, or the rest of their own 15-minute window). A successful login clears only that visitor's misses on that account, so one valid account cannot be used to reset the counters. The server itself and the tunnel relays are always allowed, and restarting the service clears a used-up setup limit. A web login to a switched-off account is now refused. Login forms are read first, capped at 4 kilobytes and a few seconds, with four logins in progress per visitor. A new allowed-networks setting refuses strangers on every listener before routing (a mistyped entry stops startup; empty means open, with a warning). The web app now has a content security policy (one inline script allowed by fingerprint) plus a header disabling camera, microphone, location, payment and USB; blocked-content reports are logged with secrets stripped, and the pages were checked in a real browser with no violations. The shared guest Cloudflare pass is removed from emails, pages, admin views and settings: guests now sign in to Cloudflare with their own email, added when they accept, and are browser-only until per-guest phone keys arrive. A server that still has the old shared-pass settings warns and ignores them.

**Still open.** The old shared token should be revoked in Cloudflare. While limits are used up, someone guessing at one account can get that account's password login refused for up to 15 minutes (people already signed in are unaffected; a restart clears it), and during a login flood an ordinary user may briefly be turned away. Nobody checked whether any client relied on the old redirects, though a test confirms none remain.

### Each guest gets a private phone-app key (23 October)

**What it was.** Guests on the phone app cannot use Cloudflare's browser sign-in, and the shared key was removed on 21 October.

**Why it mattered.** Phone-app guests needed a way in that does not recreate the shared-secret exposure.

**The fix.** Accepting an invitation asks Cloudflare for a key made for that guest, admits only that key to the phone-app entrance, and shows the two values once. The server stores only a scrambled fingerprint of the secret. Deactivating a guest cuts off their key, and an admin can revoke any one guest's key from the Users page, which lists each key, its creation time and last use. Creations and removals go to the audit log without the secret. The owner's own keys cannot be touched, only keys the server made for guests, and a Cloudflare permission failure names the missing permission and changes nothing. It is off until the owner enables it, and a failed key never blocks account creation.

**Still open.** The server's Cloudflare token needs one added permission (Access: Service Tokens: Edit) plus two settings. A guest whose key failed at accept time cannot be given one later without more work, and a reactivated guest gets no new key automatically. Not yet exercised against the real Cloudflare service, only recorded request shapes.

### Dependency security updates (20 and 25 October)

**What it was.** Dependabot, GitHub's automatic security-alert service, flagged two sets of vulnerable web-frontend packages. The build tool (vite) bundled a component with three open advisories (1 high, 2 medium) that affect only the development server, mostly on Windows; a test-only package (undici, pulled in by the test browser simulator) had a denial-of-service flaw from too many content encodings.

**Why it mattered.** Open alerts hide new ones and keep the security audit red. Neither problem reached what users run: the build tool's flaw is in the development server, and the test package is not part of the built web app.

**The fix.** The build tool's minimum version was raised from 8.2.1 to the fixed 8.2.4 (the installed version is 8.3.4); the high-severity audit finding went from 1 to 0, and the frontend build, type check and 2,784 frontend tests passed. The test package moved from 8.10.2 to 8.12.3, which leaves the production web app unchanged.

## 8. Background jobs, startup and the operations platform

### One list of operation states (10 October)

**What it was.** Every state a background operation can be in (queued, waiting, running, finished, failed, canceled and the "interrupted" family) was kept in seven places in the server code and three in the web code, and the lists already disagreed.

**Why it mattered.** Each time a new "interrupted" state was added in one place and missed in another, the web page showed a finished operation as still running, the bell badge counted it, or a scheduled task waited on it forever.

**The fix.** The states and their meanings now live in one place, and the web app's list is generated from the server's. An automated check fails if they drift apart. Nothing visible changes and no saved operation is rewritten.

### Every startup step either stops the server or is reported (10 October)

**What it was.** Whether a failure during startup stopped the server depended on how each step happened to be written. If the saved settings could not be read, or one setting was damaged, the server started anyway on built-in defaults.

**Why it mattered.** Saving any setting writes the whole settings record, so the first save from such a server would have silently replaced everything saved with defaults. The same hole existed in the separate helper process that runs some jobs (for example, duplicate-detection calibration). Meanwhile a failure to start the metrics system took the whole library offline, and a missing search index was invisible outside the log.

**The fix.** Every startup step is now in one reviewed list that says whether its failure stops the server or lets it run in a reduced ("degraded") mode, and why. An unreadable or damaged settings record, or a database error while reading, now stops both the server and the helper process before they do anything; the error names the setting but never shows its contents, since it may be a password. A fresh install with nothing saved still starts normally, and repairing or removing the named setting lets the server start again. A metrics failure no longer stops the server, and the health check reports it as disabled. A missing search index is likewise named in the health check. A check fails if a step is added without a list entry or the list keeps an entry for a step that is gone.

### A second, exact record of every operation run (11 October)

**What it was.** Each background run had always been saved as one row in an older format. A new format now also records exact counts per state, a history of every state change, and the bookkeeping that the chunk-by-chunk runner needs. A one-time upgrade builds the new records from existing rows without changing any operation ID or removing any old row, and every save writes both formats together or neither, so the previous app version can still be redeployed.

**Why it mattered.** The previous version updates only the old rows, and the one-time upgrade does not run again on roll-forward. After a rollback, any run that version created, finished or deleted left a stale new record, so counts were wrong with nothing to correct them. Separately, every progress update rewrote the whole new record, including parameters and result. A run that was stopped but kept going past its grace period was also reported as finished while still active. The same records decide what happens to interrupted runs at startup, and a run nobody touched again stayed wrong for good.

**The fix.** Each new record stores a fingerprint of the exact old row it matches. At every start, before operations resume, the app rebuilds records whose row changed, adds missing ones and removes orphans. On an already-correct store this only reads: about 0.7 seconds for 100,000 runs. If that startup check itself fails, the app still starts, because the old rows remain the source of truth. A new-format save is refused if the fingerprint no longer matches, so a stale record can never overwrite a newer row, revive a deleted run or delete one that moved on. One unreadable entry is counted and skipped instead of blocking start-up. The record is split so a progress update writes only a small entry: in a test with 6 KB of parameters and result, the new format's share of each update fell from about 6,800 bytes to about 380, and total bytes written roughly halved (13,709 to 7,257), with no change in speed. Stuck runs are shown as "stopping" until they truly end, and one that hit its time limit finishes as "timed out" rather than "canceled"; a user's cancel at shutdown is kept, and a retry of a different run of the same kind is no longer refused while a stuck run winds down. Canceling a run that is already being canceled no longer marks it finished early. Tests simulate a rollback and confirm that every affected record is repaired and untouched runs stay exactly the same.

### Settings the server never read, and old logs never cleaned up (11 October)

**What it was.** A weekly cleanup job is meant to delete system-log entries and entries of an older operation-log style once they are older than 90 days. It never ran: startup skipped the "days of logs to keep" setting, so the server always saw 0, and 0 meant "keep forever". The same startup code ignored 28 other settings, 29 in all, including the Deluge connection, the audio-fingerprint (AcoustID) backfill switch, the protected-paths list, activity-log retention windows and plugin settings. A value set only in the config file or an environment variable never took effect. The managed-tools section's seven settings were fixed values that ignored the config file. Settings saved through the app were never affected.

**Why it mattered.** Operators could not configure the server through the documented routes, and old logs piled up for months. Since late August the help text had said 0 turns the cleanup off, so a stored 0 may have been chosen on purpose. An earlier draft of the fix would also have deleted undo history older than 90 days; review caught it before it ran.

**The fix.** All 29 settings are now read, including list and table settings (AI endpoints, plugins, path aliases, protected paths) in the form each source can carry; a plugin setting written as a bare number or yes/no value no longer makes the server ignore every plugin. An automated test lists every setting and fails if startup does not read one, reads it from the wrong place, or sets a default for a setting that does not exist. Saved settings still win. A stored 0 for log retention now means the default of 90 days (it is dropped once with a warning; -1 keeps logs forever). Undo history is never touched by this cleanup; giving it its own setting is separate work that needs the owner's sign-off, and its first run will only count and report. On production, only one behavior changes: logs are kept 90 days instead of forever. The first cleanup deletes months of entries in chunks of 5,000 within a 30-minute budget; if it is stopped or runs out of time, the next run continues where it left off. Disk use may rise briefly because the old files are only freed at compaction and ZFS snapshots keep old blocks until they expire.

**Still open.** Before the first cleanup (within a week of deploy), set log retention to -1 if logs should be kept forever, and check that the data volume has room. Setting names with capital letters still lose them in config files that are not YAML or JSON, with a warning.

### Shutting down during a long database read no longer crashes the app (11 October)

**What it was.** On shutdown or restart the app closes its database (Pebble), which did not wait for reads still in progress, such as a library scan, the start-up operations-history check or a statistics refresh. A read finishing after the close drove the engine's internal bookkeeping negative and crashed the process. Earlier fixes covered one background task at a time.

**Why it mattered.** A restart right after a deploy could end in a crash that looked like a serious engine failure rather than a timing problem.

**The fix.** All database access, including the stores that share the main database (duplicate candidates, activity log, metrics, AI scan results, catalog), now goes through one gatekeeper that counts work in progress. Shutdown refuses new work with an ordinary "database is closing" error, waits about 10 seconds for running work, and only then closes. Reads already in progress run to completion and are never cut short, and a save that started before shutdown can still finish writing to disk (before, it was refused and reported that it might not have been saved). If something is still reading after that, the app leaves the database open and exits rather than closing it underneath the read. Fast-mode saves are forced to disk when shutdown starts and again when the wait ends, so everything that finished saving survives and is replayed on the next open; one narrow loss remains only after a timed-out wait. A stuck disk no longer blocks the start of shutdown (each forced write gets a few seconds), although the engine's own final close is still outside the app's time limit. If a delete or reset could not close the Open Library cache in time, the app now remembers it and finishes closing it at the next delete or import, deleting nothing until that close succeeds (before, its files could be deleted while still in use), and metadata searches follow it across close and reopen. The start-up history check stops as soon as shutdown begins. Two listening-statistics lookups that never closed their reads now do. The failing test passed 200 runs in a row, plus 600 across one, two and eight processor cores.

### Deploys wait until the server is ready (12 October)

**What it was.** A deploy waited three seconds after restarting the server and then checked the version, but the server can need several minutes to be fully up.

**Why it mattered.** A deploy could report "done" while the library was still slow or not answering, and a port held by another program could leave the server looking started while serving nothing.

**The fix.** The server now sends the standard "I'm up" signal to Linux service management once it passes its startup checks and accepts connections, and deploys and rollbacks wait up to five minutes for it, printing how long the server took. A blocked port stops startup with a clear error. Slow steps such as database upgrades and opening the search index report progress so a long upgrade is not cut off, while a truly stuck step still is. Rollback asks the previous build whether it sends the signal and, if not, switches the service to the older startup mode so it is not restarted in a loop; the next deploy of a current build switches it back.

**Still open.** The owner installs the updated service file only after deploying a build that includes this change, because an older build under the new service file never sends the signal; nothing here installs it, and the owner's private deploy settings need two lines added from the deployment guide.

### A new kit for writing background operations (13 October)

**What it was.** Operations were written by hand against loose conventions: each author had to remember to run in parallel, check for cancel, report progress, save a resume point and write safely. Many did not, so most operations were dropped on every deploy, others redid everything after the first unfinished item even when most later items were done, and several whole-library jobs ran on one core. A canceled apply that kept writing caused a real incident on 1 October, when it kept retiring books.

**Why it mattered.** A deploy during a long repair threw away hours of work, and a job that forgot to parallelize took many times longer than needed.

**The fix.** An operation is now one short declaration of what it reads and writes, who may start it and the work for one item; the framework supplies workers, cancel, pause, progress, resume and safe writes. Batches use every processor core unless the author writes down why not. Work is cut into chunks of 256 items, each finished chunk is recorded, and after a restart the operation resumes under the same run ID and redoes only unfinished chunks. Progress counts whole finished chunks, so it never moves backwards. Every write goes through one gatekeeper that refuses writes from a canceled or abandoned run, notes each write before making it so a crash leaves a record of what may be half-applied, records history only after the write happened, cannot delete a book or book file, and fails a run whose writes are refused. A crash inside an operation fails that run, not the server. An interrupted fixer still reports every change it made across a restart, and its follow-up runs once. Randomized interruption tests check that every item runs at least once and no recorded chunk runs again. Nothing in production uses the kit yet; existing operations are ported later. The existing Repairs tools use the same gatekeeper and behave as before, except that a repair write with nowhere to record its history is refused (every current repair has one). A late write can no longer re-create storage for a run that has already ended, which used to be left uncleaned.

**Still open.** If a person discards an interrupted fixer run instead of retrying it, the follow-up for changes it already made never runs. A third button, "Finish without resuming", is decided and planned to run just that follow-up.

### Search follows every book change through one path (13 October)

**What it was.** Two mechanisms told the search index about book changes: a wrapper around the database that noticed only five kinds of save, and a hook inside the database for everything else (tags, running-time recalculation, author and series renames). They disagreed about what counted.

**Why it mattered.** A change only one of them knew about, such as a background running-time recalculation, could leave search showing an old title or author until a periodic repair pass noticed. The wrapper also hid database features from code that needed them, which once silently switched off repair jobs on the live server.

**The fix.** The wrapper is gone and the database tells the index about every change it commits, as the only path. A book already waiting for refresh is not queued twice, and changes made during startup are remembered for the repair pass. Nothing visible changes.

### The server stops spending its first half hour cleaning up after itself (14 October)

**What it was.** For about 30 minutes after every restart, the job pre-computing the library list forced a full memory clean-up and handed freed memory back to the operating system after each query. A profile showed the memory manager using most of four processor cores for that time, slowing every other startup task. The growth safety check depended on those clean-ups for a trustworthy reading. Three other startup readers also went to disk for data already in memory: the primary-book count scanned every book, the author and series lists started reading while the in-memory copy was loading, and file records decoded large audio fingerprints no caller used.

**Why it mattered.** The interface stayed sluggish right after each restart, which is also when the most work happens.

**The fix.** The forced clean-ups are removed except for one baseline, and a test fails if there is more than one. The growth check now reads memory actually in use after the last natural clean-up. Review found it still could not fire on production numbers (about 5.9 GB settled, 4 GB allowed growth, 9 GB limit), so the line is now the lower of baseline plus growth and 80% of the memory limit, about 7.2 GB there. The primary-book count comes from memory when that copy is complete and falls back to the disk scan otherwise. The author and series lists wait up to five minutes for the load, and file-record reads skip fingerprints. These are expected results checked by tests, not measured on the live server. After the next deploy, the memory manager should use under 10% of CPU 10 to 30 minutes after a restart, the memory in use should stay within 6.0 GB at 25 minutes, and the forced clean-up count should stay flat.

### Startup library jobs show up on the Operations page (15 October)

**What it was.** Each restart ran four one-time library jobs invisibly (linking iTunes IDs, cleaning movement tags from audio files, repairing broken M4B audiobook files, and re-encoding or quarantining unrepairable ones), plus checks of the book-history index, the book-location index and the activity filter table. None showed progress or could be stopped, and they started while the library was still loading. Two ignored the stop signal, so a shutdown could keep one writing while the database closed.

**Why it mattered.** Hours of work were happening out of sight, and a shutdown during one risked a write into a closing database.

**The fix.** All seven now run as ordinary operations with a progress bar and cancel button, waiting up to five minutes for the library to load, and all stop when told to. Stopping a repair or re-encode ends the file in progress immediately and leaves it untouched, not marked as broken. Where there is nothing to do, each startup run finishes at once and is hidden from the activity feed by default (the unfiltered page still lists them), and running any of these jobs from the Operations page does exactly what it did before. Until the history and location checks finish, book history and folder lookups are still correct, only slower. Failures that were only a log line, such as an untrustworthy history index or unreadable book records, now show as failed. Owner decision (15 October): an interrupted re-encode waits for confirmation before running again, startup does not start a new one while it waits, and startup no longer queues it once re-encode and quarantine have both finished. The activity filter check had two bugs found along the way: it compared against a progress mark that had already moved forward, and the mark lived only in memory, so a restart before the check ran lost the repair. The starting point is now saved to the database and cleared only when a check finds nothing missing or a full rebuild finishes. If the check fails part way, the next start runs the full rebuild by itself (owner decision). Until that check finishes, entries an older version left behind may be missing when filtering by source, type or level.

### One job list, and a startup check that none went missing (16 October)

**What it was.** Background jobs were pulled in from several places, so a job could be fully written yet never included in the program, and nothing noticed. The metadata-fetch jobs were missing from production for weeks that way. The ledger of every job name ever used was only checked in tests. The project's job checker matched on file names, produced hundreds of false alarms and ran nowhere automatically.

**Why it mattered.** A job renamed without a forwarding entry, or left out of the build, would vanish quietly, and scheduled runs and saved references to it would stop working with nothing on screen to say why.

**The fix.** A single catalog now holds the job list, and a test fails if any job package is not on it. At startup the server checks that every ledger name is a known job, an old name pointing to a current job, or a name retired with a written reason; otherwise it refuses to start and lists every problem at once. Jobs tied to optional services such as Deluge count as known even when the service is off. The checker was rewritten to understand the code's types and now runs in the standard pre-merge checks: it catches a job left out of its list, a job writing to the database outside the shared writer, and any use of the delete capability, which it sends to the owner for review. Existing jobs run exactly as before. It reports nothing on current code; the older job style still has 13 direct database writes, noted but not enforced.

### The second activity database is gone (19 October)

**What it was.** The activity log behind the Activity page could live in the main database store (Pebble) or in a separate SQLite file, with code to copy history between them. Production already ran on Pebble. The 8-kilobyte cap on activity summaries, added after one 9.5 MB summary helped bloat the SQLite file in September, only applied to SQLite.

**Why it mattered.** Every change to the activity log had to keep both stores working, and Pebble had never had the cap.

**The fix.** The SQLite store, its migration and mirror-write code, three SQLite-only maintenance jobs (optimize, vacuum, and clearing copied rows) and the SQLite library are removed, about 13,000 lines, roughly half code and half tests. A configuration still asking for SQLite now stops the server at start, or refuses the settings change, with a message naming the fix; a stored old value loads as Pebble as before. Production already names the main store, so the new refusal does not affect it. The old SQLite file is neither read nor deleted and can be removed by hand once nobody needs it; the last build that still has the old code is tagged, so it can be rolled back to. Pebble now applies the 8-kilobyte cap on every write, and the two Settings controls that did nothing are gone. It merged on 19 October.

### Missing files reported instead of warned about, in the file-hash backfill (25–26 October)

**What it was.** The job that computes a fingerprint (hash) for every audio file warned once for each file it could not find. About 515,000 files had no fingerprint, and every one sampled was simply gone from the server, so one run would have buried the activity log in warnings. An earlier design that marked missing files on each book record was dropped (decision D23 in the appendix, taken on the owner's behalf) because it could overwrite a concurrent edit.

**Why it mattered.** The log was unreadable and there was no usable list of what was missing. A restarted run also rewrote some report lines and restarted its totals from zero, and a lost bookmark could erase the earlier report entirely.

**The fix.** A missing file is counted as "not found", the first 50 are written to the activity log, and every unhashed file the run reaches gets one line in a report file in a hidden reports folder inside the library. Nothing is written to book records for a missing file. Permission and drive errors stay separate and are retried each run, and files outside the library folder, or any file when no library folder is configured, are counted as "unverified". A restarted run skips files already hashed or listed and seeds its totals from the report, so final totals cover the whole job, while a brand-new run checks missing files again so one that returned is hashed. Each batch's lines (512 files to a batch) are written only after the batch is safely recorded, with a crash-recovery note in the database, so even a hard kill leaves the report and totals exact. A hash is stored only if the record still points at the same file; otherwise it is "busy" and retried next run. Book totals refresh once per batch instead of per file; if a book's totals cannot be refreshed, the run carries on, keeps the fingerprints and ends with a warning naming those books. The job refuses to start, preview included, if the library folder cannot be read, is not a full path, holds only hidden entries (how an unmounted drive looks), or its reports folder cannot be written. It also stops mid-run if the drive drops out, keeping the report so far rather than listing the rest of the library as missing. The leftover-files repair tool now uses the same check, so it also refuses a library folder that holds only hidden entries. The report is treated as untrusted: it is used only to skip and count, never to take a fingerprint or location, and the folder is checked for shortcuts and wrong owners; if the folder is open to everyone, the job removes only that permission and leaves group access alone, so the production folder needed no change before deploy. Automated tests, including a mid-batch stop and resume, pass; it had not been run against the production library when written (the first production dry run is described in the next section).

**Still open.** The attached-drive check only catches an empty or hidden-only folder; it does not prove the drive is healthy.

### A job too big for one run now continues under the same operation (26–27 October)

**What it was.** The production dry run of the same hash backfill, over 791,451 file records, was cancelled at 307,328 by its 4-hour limit. A timed-out run is final and a retry is a new operation whose report starts from row zero, so a library this size could never finish.

**Why it mattered.** The job could not complete at all, however often it was retried.

**The fix.** A resumable operation can now hand itself back before its deadline. The hash backfill does this once a chunk is safely saved and less than 5 minutes of the limit remains (or less than twice its slowest chunk, if that is longer). The registry records a pause and restarts the same operation under the same ID, from its report, with a fresh 4-hour limit. Safeguards: the pause is honored only if that attempt made progress, otherwise the run fails with a message instead of looping forever; a deadline that fires is still "timed out", a watchdog kill is still "canceled", and only an in-process run whose context is still live may yield. A pause is not announced as an ending: no completion summary, no "finished" event (so the bell keeps showing it running) and no failure told to dependents. The old attempt's progress writer is finished first so it cannot overwrite the new one's, and the progress bar resumes where it left off. An end-to-end test with a 4-minute limit finishes with two continuations and every row exactly once. Hashing already used a bounded pool of 8 workers; tests now confirm 1 and 8 workers give identical counts, reports and stored hashes. A sliding-window speedup is filed as a todo pending a throughput measurement. Merged on 27 October.

## 9. Transcription (Whisper) and book intros

### Listening to book openings works again, without the server touching audio (22 October)

**What it was.** The tool that listens to the first minute and a half of each audiobook to read out title, author and narrator stopped working. On 9 October the main server was told never to process audio itself, only the Macs may, but this tool still cut clips on the server, so on 22 October it stopped within a fraction of a second with "audio decoding is not allowed on this server" while the Mac helpers sat idle.

**Why it mattered.** Identifying books by what the narrator says at the start is something the rest of the library clean-up relies on, and the tool had to be restored without breaking the rule that the server never processes audio.

**The fix.** The server no longer cuts audio. It names the book file, and a Mac helper opens it over the network from the shared library folder, cuts the clip and listens. The tool picks this route automatically and will not fall back to the server. If the Mac loses the library folder before a batch, the batch pauses untouched. If it drops partway, an unreadable book gets a retryable "path unmapped" note rather than being marked as bad audio. A wrongly set-up helper leaves books untouched, and a book is never recorded as silent just because its check failed. The longer retries for books that open with music or silence use the Mac route too. The Mac needs the library folder mounted for its own login and a path mapping, documented with a login item that re-mounts it; until then the tool refuses to run.

### Listening to the start of every audio file (22 October)

**What it was.** Only the first audio file of each book was ever "listened to", so a book of dozens of tracks was judged on track 1. The owner approved listening to the first 90 seconds of every file.

**Why it mattered.** A book made of dozens of tracks was judged on its first track alone.

**The fix.** Each file gets its own saved transcript with the speech model and clip length noted. Every book's first file goes first across the whole library. A book's own transcript is filled from its first file only if it has none and is never replaced. Byte-identical files are listened to once. The server does no audio work. A run with no instructions only counts the work and estimates time, can be stopped and resumed without repeating anything, waits while a scan or large metadata update runs, and records nothing if the workers are unreachable, trying those files again next time rather than marking thousands as failed. Progress shows how many of the library's files now have a transcript. Any run can be rolled back by its run number after a report of how many files and book transcripts it would clear; a forced redo of files that already had transcripts cannot bring the older text back. The September design review's claim that every rescan wiped saved transcripts had already been fixed by an earlier change; this work adds protection and named tests. A full run's duration is an estimate until measured against the real workers.

### One bad audio clip can no longer freeze all the listening helpers (25–27 October)

**What it was.** Late on 23 October all four Mac helpers stopped (the decision log records them stuck from just after 1 a.m. on 24 October). One audio file sent the speech engine into an endless loop, and the helper had no time limit. After ten minutes the server judged the helpers "unreachable" and re-sent the batch to the next helper, so each got stuck on the same file in turn.

**Why it mattered.** A single unreadable file silently stopped all audiobook identification, and the retries spread the damage to every helper. Now the damage is limited to that one file, the helpers recover on their own, and a stuck pool is reported plainly instead of looking like an outage.

**The fix.** Each helper now listens in a separate worker process with a time limit (one minute, or four times the clip length if longer); on timeout that process is killed, a fresh one takes over, and the file is marked "timed out". The server tells "helper down" from "helper stuck", splits a stuck batch in half to pin down the one file, and keeps files already finished. A file that times out on two different machines is set aside until someone asks for a retry; two timeouts on the same machine count once, machines are identified by a code that does not reveal their name or address, and with only one machine a file is never set aside this way. A file that timed out once goes to a different machine next time, a later success wipes its strikes, a file two helpers in a row cannot finish waits for the next run, and a set-aside file returns when someone asks for a retry or the file is replaced. If a helper's listening process cannot start, files are left alone and the helper reports itself unavailable for five minutes; a clip that crashes the process counts like a timeout. A request the server had already given up on is reported as "no answer" and never counted against the file. A file is blamed at most once per run and once per twelve hours. A helper that mostly times out is treated as the problem, not the files, and when the trouble is timeouts the other helpers on the same Mac are set aside with it. When every helper struggled, the server plays a two-second test recording to each; one that cannot answer it is treated as ill and must answer it before getting work again, so an ill machine costs a two-second test per batch instead of up to an hour and a half of waiting. Kills from a machine whose listening was mostly killed are withheld from files, and a test recording cannot clear a machine of kills. The test recording, its wait for the helper's turn, the rule that a failed machine must pass before working again, and withholding of mostly-kill batches were added in the final review rounds. The server now asks a helper how long it has been working instead of waiting a fixed ten minutes (older helpers keep the fixed wait). Health checks now report time spent on the current clip, and a helper past its limit is unhealthy. Three stuck batches in a row stop a run, a six-minute wait applies when no helper is available, the book tool stops after five batches in a row with no helper and the file tool after five tries of one batch, and a "300-second listen that could not finish" is no longer recorded as silent, so books with a long music opening may be retried each night until that listen succeeds. Rolling back a run clears its "timed out" labels, and the listening statistics show a timed-out count. Operation logs show real error text instead of an empty placeholder. Roll out the server first, because an older server would record the new helpers' replies as file failures, then restart helpers one at a time and confirm each reports itself idle.

## 10. Build, tests and the automated test system

### Test-fixture checks run as three parallel workflows (18 and 27 October)

**What it was.** The "fixtures" test run on Woodpecker failed now and then by running out of time. As one step it ran every fixture package (a group of tests that need prepared sample data) at once, and on the slower test machines the packages competed for the processor: in one documentation-only run a package hit its 25-minute limit at about 1,500 seconds with nothing hung, while others took about 1,100 to 1,200 seconds. On 18 October it was split into three shards (8, 9 and 7 of the 24 packages) run one after another inside a shared 55-minute budget, so that a slow package would time out before Woodpecker's 60-minute limit killed the whole run.

**Why it mattered.** Run back to back, the three shards took about 3,400 to 3,500 seconds (57 to 58 minutes) of test time, so the whole run used about 59 of its 60 minutes. On 27 October one pull request failed three times and another twice on time alone, always in the last shard, which was cut off when the shared budget ran out. A check that fails on time alone blocks good changes.

**The fix.** By owner decision the three shards are now three parallel workflows, each with its own 60-minute pipeline and the full 25-minute per-package timeout, and the shared-budget runner script is removed. Woodpecker pull request statuses go from 7 to 9. No tests are skipped.

### Test-system package installs retry instead of hanging (23 October)

**What it was.** On 23 October the download servers for system packages hung, leaving the steps that install packages in the CI runs stuck for 34 minutes.

**Why it mattered.** A stuck install held a test run for over half an hour before anything failed.

**The fix.** All 11 workflow install steps now go through one script with retries, 30-second timeouts, a 600-second cap per call and three attempts for the whole sequence.

### Database tests that waited on disk now run in memory (4 October)

**What it was.** The database package's quick test run took 859 seconds at 3% CPU on a Mac, because each of about 1,500 tests opened an on-disk store and nearly all the time was spent waiting for the disk to confirm each write, and some fixtures wrote 1,000 to 15,001 rows one at a time.

**Why it mattered.** One package took about a quarter of an hour on a Mac, nearly all of it spent waiting for the disk rather than testing.

**The fix.** Those tests use in-memory stores (121 inline calls plus the shared helpers) and batched seeding, while tests that need a real disk, reopen or deliberate-failure behavior stay on disk. Back-to-back timings went from 1,079.8 s to 41.3 s at a machine load of 4 to 10; figures depend on load, and the gain on the Linux test machines is smaller because confirming a write to disk costs far less there. No test was skipped and no check removed; deliberately breaking the code still made the relevant tests fail. Statement coverage in the quick run rose from 21,950 to 21,983 of 28,902, with 147 packages passing.

### A flaky cancel test fixed (25 October)

**What it was.** A reconcile test failed now and then on slow race-checking runners with "362 of 3000 ran" stat calls after a cancel. The code was right: it stops starting work after cancel, with at most the workers already in flight finishing. The test canceled after releasing its lock, so a slow runner let other workers keep going before the cancel took effect.

**Why it mattered.** A test that fails at random turns checks red for changes that did nothing wrong.

**The fix.** The test now cancels while holding the lock and checks that work begun after the cancel stays below the worker limit, independent of runner speed. A similar hash-cancel test got the same fix. Deliberately removing the check from the code makes both tests fail, and 20 repeated race runs pass.

### Automated tests no longer reach paid services (13 October)

**What it was.** A Mac test machine passed a paid AI provider key into its test steps, so live tests against that provider ran and failed because the provider refused the extra requests (rate limiting).

**Why it mattered.** The failures blocked merges, and continuous integration is never meant to call paid providers.

**The fix.** Every step on the main test machines now clears five provider keys (two AI services, the audio-fingerprint service, Google Books and Hardcover) and the address of the live speech service before running.

---

## Appendix: decisions taken on the owner's behalf, 25–26 October

From 2026-10-25 12:53 EDT to 2026-10-26 12:53 EDT (or until you say otherwise), every question gets its **Recommended** answer without waiting. Each one is logged here. Overturn any of them by number.

Terms used in the table: **prod** is the live server; a **PR** (pull request) is a proposed change; a **blocker** must be fixed before merging, a **should-fix** is a real problem that may go to a follow-up, and a **nit** is a small wording or style point; **E2E** (end-to-end) tests drive the real web app in a browser; **IMAP** is the standard way a program reads a mailbox. Search terms such as `tag:"sci-fi"` or `read_status:completed` are shown exactly as a person types them in the search box.

| # | When | Question | Options considered | Taken (Recommended) | Why |
|---|---|---|---|---|---|
| D1 | 10-25 earlier, your answer | Dynamic collections: whose state decides membership by read status/progress/last played? | Per viewer · Owner's state · Shared | **Per viewer** (you chose) | Another user's listening data is admins only |
| D2 | 10-25 earlier, your answer | Bulk trash/restore permissions | Match the single-book permissions · Admins only · Unchanged | **Match single** (you chose): moving to the trash needs the delete permission, restore needs the organize permission | Same rule as deleting one book |
| D3 | 10-25 12:53 | Hash backfill: 515k files with no hash; the sampled failures were all files missing on the server. Skip known-missing files? | Skip known-missing and report the count · Keep retrying every run · Delete the rows | **Skip known-missing and report the count** | Retrying files that aren't there wastes the run; file records are never deleted |
| D4 | 10-25 12:53 | #4140 purge: leftover file records when the trash is empty. Replay at boot, or a button in the purge dialog? | Dialog button · Boot replay · Both | **Dialog button** (worker's choice, under review) | At boot an unmounted drive reads as empty, so a replay would treat the files as gone and clear their records |
| D5 | 10-25 12:53 | #4140: "Discard progress and purge" still deletes the book's record before checking its file. Reorder it the same way? | Fix in #4140 · Follow-up PR · Leave | **Fix in #4140** | Same kind of bug as the one this PR already fixes; fix it properly |
| D6 | 10-25 12:53 | E2E invite test: how to receive the email | Cloudflare Email Routing catch-all on the owner's domain · Dedicated Gmail over IMAP | **Cloudflare catch-all** | No new account, scriptable |
| D7 | 10-25 12:53 | #4155: per-person "not started" and negated filters read every book's state. Add a database lookup that reads only one person's state? | Add the method · Accept the scan cost | **Add the method** (pending the review's numbers) | Keeps collection reads from scaling with library size |
| D8 | 10-25 14:35 | #4149 privacy scan: 7 strings echo real titles/transcripts (blockers), 14 more look invented but match or resemble real works (warnings), incl. a real person's name. Earlier commits also carry real titles and would land on main with a rebase merge. | Fix blockers + warnings and squash into a new PR · Fix blockers only · Fix all, keep history | **Fix all 21 and squash into a new PR that supersedes #4149** | Your rule bans even word-swapped titles; a rebase merge would publish the old commits |
| D9 | 10-25 14:35 | #4136 re-review: 0 blockers, 4 should-fix (combine during startup warmup warns instead of refusing; regroup hides warnings; undo puts a file on the wrong book when the original owner was purged; untested claim). Merge now or fix first? | Fix all should-fix items first · Merge now, follow up | **Fix first** | It runs on prod without a flag, so standing rule says should-fixes before merge |
| D10 | 10-25 15:00 | Hash backfill (#4158): the first run may flag ~515k missing files, and each flag write recomputes the book's totals (one recompute per file). | Batch the writes per book · Accept the one-time cost | **Batch per book** | 515k recomputes on prod is hours of load; per-book batching is what the missing-files job already does. The live apply still waits for a dry-run preview with numbers shown first |
| D11 | 10-25 15:10 | #4155 review: 2 blockers (an unparseable per-user query still serves the creator's stored list; per-user collections only look at the 10,000 oldest books, also true on main) and 6 should-fix. Fix the 10k cap here or as a follow-up? | Fix everything in #4155, incl. the per-user state lookup (~1 day) · Merge with the cap as a follow-up | **Fix everything in #4155** | The PR makes the capped path the only path for every viewer; per D7 the lookup was already chosen |
| D12 | 10-25 16:10 | #4140 review: the purge already replays file records at boot when a run resumes, so an unmounted library can still be read as "files gone". Add a mount check? | Mount-readiness check before any replay or file decision, then also add the boot replay · Keep the dialog-only approach | **Mount check + boot replay** (replaces D4) | The boot path exists anyway; the check is what keeps records safe |
| D13 | 10-25 16:10 | #4140: 0 blockers, 4 should-fix (new button unreachable on an empty trash; stale decision at removal; single-book paths leave files with no record). Merge now or fix first? | Fix first · Merge, follow up | **Fix first** | Runs on prod without a flag |
| D14 | 10-25 16:50 | #4149 superseded by squashed #4159. The old #4149 commits (and the #4145 head) still hold real titles on GitHub. | Delete the old branches and add both PR heads to the GitHub Support purge request · Leave them | **Delete + purge request** (branch delete needs you: the classifier blocks remote branch deletes) | Public repo; closed-PR refs stay reachable until GitHub purges them |
| D15 | 10-25 18:05 | Whisper: one audiobook intro loops forever and had all four Mac workers stuck since 10-24 01:06. Restart now and clean up? | Restart the 4 workers, stop the orphaned looping processes, delete 1,260 leaked clip folders (7.4 GB) · Wait for the code fix | **Restart + clean up now** (done 18:00) | Workers were useless while stuck; no live run starts until the code fix ships |
| D16 | 10-25 18:05 | Whisper code fix scope | Per-clip time limit in a killable child + timeout as a per-file result, permanent after 2 strikes; split timed-out batches; health shows busy time; log real errors · Just add a timeout | **Full fix (A–E), 2 strikes then permanent skip** | A plain timeout leaves the loop retried forever; 2 strikes stops the poison file without a manual prod write |
| D17 | 10-25 18:05 | #4159 re-scan: 1 real title left (split across two comment lines), 3 weak lookalikes | Fix all 4 in a new squashed PR · Fix the blocker only · Amend + force-push | **Fix all 4, new squashed PR** | Force-push is banned; a fix-up commit would land the bad commit on main |
| D18 | 10-25 19:12 | #4158 review blocker: flagging absent files with the existing "missing" flag lets a version with missing chapters win the choice of main version (that flag already means "superseded" elsewhere). | (a) separate "hash found it absent" marker read only by this job · (b) new superseded marker + backfill + election fix · (c) hold the apply | **(a) separate marker**, plus mount check, preview-only Fingerprint button, per-row report | Narrowest change that cannot affect elections or other readers |
| D19 | 10-25 19:30 | #4153 re-review: 0 blockers, 4 should-fix (incl. an older window where discard can erase progress then answer "nothing changed"), and a scanner path that can take a trashed book out of the trash (older, outside this PR). | Fix the should-fixes + nits in a rebased new PR; file the scanner issue as a TODO · Also fix the scanner here · Merge as is | **Fix in rebased PR, scanner as TODO** | Bulk-delete changes run on prod without a flag; the scanner issue is a separate path |
| D20 | 10-25 19:40 | #4162 (was #4136): combine warnings now reach the review approve response, but the web UI drops them. Show them? | Toast in the regroup lane, no storage change · Also store them on the review item (schema change) · Leave backend-only | **Toast, no storage change** | The fix is only real if the person approving sees the warning |
| D21 | 10-25 22:00 | #4140: the new "is the library mounted" check only tests that the folder is non-empty, but on prod the app's own data folders live inside it, so it would never trip. How to detect an unmounted library? | Sample-check that most live files are present + treat "missing" as gone only when the file's folder exists · Ignore the app's own folders when counting · Keep as is | **Sample check + parent-folder rule** | Works with nested datasets and doesn't depend on folder names |
| D22 | 10-25 22:40 | #4164 review: a per-user term inside OR / NOT(...) is silently evaluated as AND (older bug; prod has 0 dynamic collections, 1 static playlist). Fix now or refuse? | Refuse those shapes with a clear message now and file real OR/NOT support · Implement full boolean evaluation now (~half day) | **Refuse now, file full support** | No silently wrong results; nothing on prod uses these shapes |
| D23 | 10-25 23:30 | #4158 re-review: the "absent" marker write can overwrite a concurrent scanner change to the same file row (blocker), and the marker saves no disk work anyway. | Drop the marker: just count/report not-found files, no database writes; fix the underlying write race for all file-row writes in a separate PR · Keep the marker and add the store fix in #4158 | **Drop the marker + separate store-fix PR** | Simplest version with no new write path; the race also affects existing hash writes, so it gets its own fix |
| D24 | 10-25 23:45 | #4166 security review: 2 blockers, both older than the PR but reachable by non-admins on prod: (B1) the audit tier of the activity feed shows other users' account events and "username (id)" actors; (B2) a run of the repair for merged users' listening state lists per-user listening state to any viewer. Plus 7 should-fixes (id-only log lines missed, diagnostics export dumps full operation records, owner-apply line served raw, editor can revert user-state rows they can't read, event streams keep old permissions, a slow-path activity read, account-merge result shows account ids). | Fix both blockers + all 7 should-fixes in #4166 (add a "result carries user data" access scope covering the merged-users listening-state repair and the account merge; require the manage-users permission to revert listening-state rows) · Fix only the blockers, should-fixes to a follow-up · Merge as is | **Fix all in #4166** | The PR runs on prod without a flag, so should-fixes are fixed before merge; one access scope closes B2 and S7 together |
| D25 | 10-26 00:10 | #4165 (purge): with the new mount check, a trashed book whose whole folder is gone can't be purged with "delete files" until the folder is readable again, and a library that really lost most of its files is refused a delete-files purge (looks the same as an unmounted share). Accept? | Accept: refusing when unsure is the safe side; a purge without delete-files still works · Add an admin override to force it · Drop the folder rule | **Accept** | A wrong "gone" decision deletes nothing it shouldn't, while a wrong "present" decision on an unmounted share could; the cost only delays cleanup |
| D26 | 10-26 00:30 | #4158 re-review (marker gone): a run resumed after a restart lists some files twice in its report and its summary counts only the part after the resume (report only, no database change). Also: the repo can't squash, so the PR's earlier commits (with the old marker and race) would land on main; executive summary missing; the run now refuses in four situations, only one documented. | New squashed PR with: report rows written only at each checkpoint + resume reads the existing report to skip listed files and restore counts; executive summary; document all four refusals; wording fixes · Fix in #4158 as more commits · Accept the duplicate rows | **New squashed PR with all fixes** | A one-time owner report should be exact; history on main must not carry the reverted race code |
| D27 | 10-26 00:45 | #4165 round-7 review: 0 blockers but CI red (the new folder check misfires on folders the purge itself just emptied). Also: records of deleted books' files are dropped after 3 runs during an outage, so the files could be re-imported as books the owner purged; the "is it mounted" sample can land on books with no file in the library and say "ready"; each check copies the whole library. | Fix all in #4165: clean up folders only after all deletions, keep missing-folder records (one summary line, no pass cap), sample with a new store method that keeps looking until 50 in-library files are found, plus all nits · Fix only the CI failure | **Fix all in #4165** | Runs on prod without a flag; a purged book coming back is a silently wrong result |
| D28 | 10-26 00:50 | #4168 Whisper review: 1 blocker (the per-book job counts a timeout against the book's first file even when the second file caused it, so a good file can be set aside for good) plus 9 should-fixes (a slow Mac can give both strikes in one night; the request time limit grew ~10×; worker crashes recorded as per-book errors again; finished results thrown away when a later batch fails; strike write can undo a scanner change; etc.). | Fix all in #4168: strike the file actually sent; strikes must come from different runs; watch worker health during long requests; worker faults are deferred not recorded; keep finished results; take the scan write gate; clear strikes on success · Fix only the blocker | **Fix all in #4168** | Wrongly set-aside files silently drop out of identification; the Whisper rerun waits on this anyway |
| D29 | 10-26 00:55 | #4164 delta review: D22 refusal holds, but an older bug of the same kind: per-user values are never checked, so `read_status:completed` (not a real status) or `read_status:fin*` silently returns nothing, and its negation returns the whole library. Plus: the smart-playlist snapshot skips the 10,000 cap; the playlist page ignores the true total; redaction removed all detail from failure logs; the library search box still treats per-user OR/NOT as AND. | Fix all in #4164: refuse invalid per-user values (only the real statuses, numeric progress, no wildcards) with a clear message; cap the snapshot; playlist page shows the total; redact only the state-read errors; apply the same refusal to the library search box · Blocker only, rest to TODO | **Fix all in #4164** | Same class as D22 (no silently wrong results); each fix is under an hour or two |
| D30 | 10-26 01:20 | #4169 (file-record write race) review: 0 blockers, core locking correct; 9 should-fixes (a file moved during a library-wide sweep is skipped and can be left fingerprinted but unindexed; one repair op writes two undo records on a retry; deletes still work from a stale read and can leave orphan index entries; large moves now fail more easily under churn; two unused store methods still do the old unsafe write). | Fix all in #4169, delete the two unused methods · Merge, follow-up PR | **Fix all in #4169** | Runs on prod without a flag; deletes are the same race class the PR exists to close |
| D31 | 10-26 01:25 | #4158 replacement: a resumed run is now exact except one case: if the whole process is killed (not cancelled) in the middle of saving a chunk's hashes, the report and count can be short by up to 512 files (database stays correct). Close it? | Close it now (~40 lines: a small sidecar record of the chunk being saved + a kill-between-steps test) · Leave it documented | **Close it now** | The report is the owner's evidence for missing files; small cost |
| D32 | 10-26 02:15 | #4170 review: 1 blocker (in a few narrow crash windows a rerun restarts the report from empty, so already-hashed files vanish from the report and totals; database fine) + a stale hash could be stored and become the file's permanent original hash + guaranteed conflict with #4169. Repo is rebase-only, so 6 commits would land. | Fix in a new single-commit PR: never truncate an existing report, always resume from row 0 skipping rows already listed or already hashed; store a hash only if none is stored and the path still matches (through the write that first checks the record has not changed); a separate "busy, retry" bucket for #4169's conflict error · Downgrade the blocker, document | **New single-commit PR with all fixes** | The report is the owner's evidence; exactness was the point of this round |
| D33 | 10-26 03:10 | #4165 round 11: optional extra rule — a missing file in a folder that exists but is empty counts as "can't tell" (protects a nested share that is mounted-empty). Cost: trashed books whose files were deleted by hand and whose folder was left empty stay in the trash for delete-files purges until the folder is removed or refilled (purges without delete-files unaffected; count unmeasured). Keep it? | Keep (separate commit, revertable alone) · Drop it | **Keep** | Same safe side as D25: a wrong "gone" can delete the wrong thing on an unmounted share; the cost is only delayed cleanup |
| D34 | 10-26 04:00 | #4165 round 8: 0 blockers. (A) a hung library mount can pile up stuck threads until the server crashes (batch hard delete starts a new 10 s check per book). (B) records for files in missing folders are kept forever with no way to clear them, and the purge dialog shows them as an unfinished purge with a "Finish removing" button that never clears them. | (A) refuse new checks while one is unanswered + stop the pass when the root isn't ready + check before the lock everywhere. (B) separate count, truthful dialog text, and an audited "forget these records" action for the owner; no automatic expiry · (B) alternative: expire 30 days after the folder went missing | **Both fixes; owner-triggered forget, no auto-expiry** | Auto-expiry during a long outage could still let purged files be re-imported; an explicit, logged action keeps the owner in control |
| D35 | 10-26 04:10 | #4166 security re-review: all claimed fixes verified; 1 older blocker (the metadata candidate-feedback list gives editors other judges' usernames; prod currently returns no rows) + 8 should-fixes (an admin's diagnostics export can be downloaded by anyone with settings rights; admin exports started from the generic route are wrongly narrowed; scoped API keys get full exports; a few id-only log shapes pass the filter; activity details unfiltered; per-event permission re-check too costly; per-op activity read now decodes every row; combine undo exception). | Fix all in #4166; combine/sibling undo: document the exception (it restores the library structure, not one user's listening state) · Gate combine undo too | **Fix all; document the combine-undo exception** | PR runs on prod without a flag; the download bypass is a real exposure for any custom settings role |
| D36 | 10-26 04:20 | #4164 round-4 review: 1 blocker (older bug, newly mis-documented): the web library search box sends read-status and progress terms as structured filters, which skip the new checks, so `-read_status:completed` still returns the whole library and `read_status:finished OR author:x` is still treated as AND; the docs claim the box refuses them. Also: a test file uses a real author's surname (already on main from older work; 3 new uses). | Fix the filter path too (value + shape checks, `(a|b)` handled, web sends OR/parenthesised queries whole so the server can refuse them), correct the docs, and replace the surname with Batman characters across the whole file · Only correct the docs | **Fix the filter path + docs + fixture** | The library search box is the most-used surface; the fixture rule is the owner's standing ban |
| D37 | 10-26 05:15 | #4169 delta review: 0 blockers; 5 should-fixes (new stress test flaky ~30%; tag-backfill now stops when a big batch stays busy; a bulk delete retried after a regroup can delete the moved row under its new book (older race, slightly widened); undo records not voided when a delete gives up; large merges fail hard on a conflict). | Fix all in #4169: deterministic stress assertion; refuse only the stale rows and commit the rest; expected-owner guard on retried deletes; void undo records on give-up; jittered backoff + retry-later for merges · Merge, follow-ups | **Fix all in #4169** | Runs on prod without a flag; a wrong file-row delete is the worst outcome in this codebase |
| D38 | 10-26 05:30 | #4172 (#4167 nits): the "refuse when there's no permission information" rule covers bulk edits only, not delete/hard-delete/restore (unreachable today; ~4 production call sites + tests to change). Do the code fix or only correct the docs? | Do the code fix in #4172 (check the caller's permission for delete, permanent delete and restore, including restore from the trash) · Docs only (already done) | **Do the code fix** | Fix it right: the protection should be real, not just documented |
| D39 | 10-26 06:10 | Hash backfill: the new check on the hidden reports folder refuses a group-writable folder, and prod's was created group-writable. Refuse it (owner changes the permissions first) or let the helper tighten a folder it owns? | Self-tighten when the folder is real, not a shortcut and owned by the app's user; refuse only if foreign-owned or the change fails · Refuse, and the owner changes the permissions before deploy | **Self-tighten** (revised by D43) | No owner step, and a deploy can't break every report-writing op |
| D40 | 10-26 07:27 | #4168 Whisper delta re-review: 1 blocker (a book can be sealed as silent while another file has a real transcript) and 7 should-fixes. Fix only the blocker or everything? | Fix the blocker, all seven should-fixes and the nits that change behaviour, then re-cut as a single-commit PR from main · Blocker only | **Fix everything, re-cut as one commit** | Runs on prod with no flag; #4168 now conflicts anyway |
| D41 | 10-26 07:31 | #4173 review: 0 blockers, 3 should-fixes, none reachable on prod today (no retired rows). Fix them now or wait for the retire-apply PR? | Fix them in #4173 with tests, together with its CI failures, plus the contained nits · Merge as is and gate retire-apply on them | **Fix in #4173** | The code is this PR's, and the first should-fix becomes a data-corruption blocker once retire-apply ships |
| D42 | 10-26 07:35 | #4172 review: 0 blockers, 1 should-fix (the check on the locked row during a restore is never exercised) and 7 nits. #4172 is also the fix for main being red. Merge now or fix first? | Fix the should-fix and all 7 nits first · Merge now to turn main green, follow-up PR | **Fix first** | PRs that run on prod without a flag get their should-fixes before merge, and the fixes are small |
| D43 | 10-26 07:48 | #4174 review: prod's hidden reports folder is shared with a group, with an extra access list for two users and that group, so D39's tightening to owner-and-group-only would strip their write access. Which permission rule should the helper use? | Remove write-for-everyone only, leave group access, the access list and the inherit-group setting alone, create files with the project's standard permissions, still refuse a foreign-owned path, a shortcut or a non-folder · Only refuse a folder anyone can write, change nothing · Keep D39's owner-and-group-only | **Remove write-for-everyone only** (revises D39) | Prod's shared-group permissions stay untouched; the same round also batches the per-book re-total, fixes the PR wording and nits, and files report pruning as a todo |
| D44 | 10-26 09:06 | #4169 file-record write race: three nits left for a decision (an orphan journal row after a crash, a delete compare that copies about 1 GB at 2,000 fingerprinted rows, an index key read outside the locks). | Cap the batch size inside the delete and chunk internally; file the other two as todos with costs · Compare a digest instead (saves about half the memory); do all three here | **Cap the batch, file the other two as todos**, then re-cut #4169 as one commit | The two todo items predate the PR and are design changes, so they are a separate problem |
| D45 | 10-26 09:53 | #4174 re-review: a failed per-book re-total now stops the whole run, so one bad book could wedge resume and every new run. Prod shows 0 such failures in 14 days. Keep stopping or carry on? | Carry on: keep the hashes, log one aggregated error, finish with a warning naming the count and book ids · Keep stopping (fail closed) · Log only | **Carry on with a named warning** | Nothing is silent and nothing wedges; the same round adds a test and fixes four small nits |
| D46 | 10-26 10:01 | #4175 blocker: the search index splits tags into words, so `tag:"sci-fi"` also matches "hard sci-fi" and `-tag:"it"` returns the whole library. How to make `tag:` exact? | Evaluate a top-level `tag:` term as the exact tag filter on the server and refuse it inside OR, alternation or negation with an error; file the index mapping and reindex as a todo · Change the mapping now, forcing a full prod reindex · Web-only fix | **Exact on the server, no reindex now** | Exact everywhere it is accepted, fails closed elsewhere, and puts no reindex on prod today |
| D47 | 10-26 11:35 | #4175 review 3: 0 blockers, 1 should-fix (viewer state loaded per book), 5 nits, and a live-on-prod problem (the Library box mishandles negated, grouped and OR'd `tag:` terms). How far to fix? | Load viewer state in bulk and filter before loading books (no 10k cap), fix all 5 nits, and have the web send any boolean query whole so the server answers it or refuses it with an error · Narrower fixes | **The wider correct fix** | Same class as D46: no silently wrong results; the reindex prerequisite stays in the todo |
| D48 | 10-26 12:46 | #4181 review 4: 1 blocker (the boolean-structure check counts parentheses and pipes inside regex and plain values, so working regex filters get a wrong answer or an error) plus 6 nits. | Decide structure from the parsed query rather than raw characters, refuse a regex in the main search box with a message, and fix all 6 nits (mixed single and bulk reads, cancellation check and tests, case-insensitive check, docs reason, a boost-weight edge case, the task-list file header) · Blocker only | **Parsed-query fix plus all 6 nits** | A raw character scan cannot tell syntax from values; regex filters that work on prod must keep working |

### Owner decisions outside the window

- **10-26 11:14, direct from the owner (not auto-Recommended):** the request to GitHub Support to purge the cached views and pull-request refs of the closed PRs #4145, #4149, #4156 and #4159 will not be sent. This replaces the purge-request half of D14. It is filed as a far-future, low-priority todo in case the owner ever wants it.

### Closing note

The auto-Recommended window ended on 2026-10-26 at 12:53 EDT. From then on, questions go to the owner again.

---

## Index: pull requests by section

Merged pull requests in the falkcorp/audiobook-organizer repository. Rows marked "cited" use the
number the section's original write-up gave; rows marked "by title" were matched to the section
by the merged pull request's title (used where the write-up named no number, or named a draft
that was later closed and replaced); rows marked "checked" come from the pull requests read for
this summary. A section not listed here has no number identified (the review-hold write-up cited
#4062, which is an unrelated change, so it is left out).

| Section | Pull requests |
|---|---|
| Books titled by a number | 13 pull requests between #3672 and #3685 (cited as a range, which also holds unrelated changes) |
| Production companies listed as the author | #3984 (cited) |
| Hand-applied books are recognised by everything they carry | #3754 (cited) |
| Copying metadata to the version the library shows | #3797 (cited) |
| Bringing Audible's "finished" marks into the library | #3746 (cited) |
| An uploaded reading list can update what a person has read | #4154 (by title) |
| Every place that saves listening progress waits its turn | #4073 (cited) |
| Closing the paths that showed other people's listening history | #4148 (cited; the follow-up was still open at month end) |
| Smart collections show each person their own listening | #4181 (by title; the write-up cited the closed draft #4164) |
| Linking versions brings the whole family | #3756, #3758, #3762 (cited) |
| Undoing a repair no longer hides the real book | #3805 (cited) |
| The AI scan's "Apply selected" button now merges authors | #4013 (cited) |
| Every book combine can be undone by reverting its operation | #4064, #4060 (cited) |
| The old Dedup page is gone | #4065 (cited) |
| The split-books repair stopped proposing to merge different books | #4105 (cited) |
| Retiring single-file copies of multi-file books | #4124 (by title; the write-up cited the closed draft #4107) |
| A safe preview for cleaning up trashed duplicate books | #4114 (by title) |
| Retired versions | #4128, #4134, #4163, #4179, #4143, #4141 (checked; still open at month end: #4144 apply, #4146 undo, #4210 trash and purge guards) |
| A permanent record of every accept and reject decision | #4110 (checked) |
| The trash keeps your listening progress | #3771, #3777 (cited) |
| Trash cleanup only deletes files inside the library | #3792, #3777 (cited) |
| The playing version's files are no longer deleted from the trash | pending |
| Purging no longer destroys undo | #4192 (checked) |
| Bulk delete no longer moves progress off a book it then keeps | #4167, #4172 (by title) |
| Deluge imports can no longer overwrite a book | #3972 (cited) |
| Two library jobs no longer silently undo each other's file-record changes | #4198 (checked; the write-up cited the closed draft #4169) |
| Owner decision: drop write-back, import only | #3834 (by title) |
| One manual import, safe to run again | #3839 (by title) |
| Phone apps stay signed in while they are used | #4081 (cited) |
| Invitations are emailed, with safe sign-on linking | #4090 (cited) |
| Each guest gets a private phone-app key | #4138 (by title) |
| Dependency security updates | #4102, #4152 (checked) |
| One list of operation states | #3920 (cited) |
| Every startup step either stops the server or is reported | #3930 (cited) |
| A second, exact record of every operation run | #3983 (cited) |
| Settings the server never read | #3955 (by title; the write-up cited #3952, an unrelated change, in error) |
| The server stops spending its first half hour cleaning up after itself | #4020 (cited) |
| Startup library jobs show up on the Operations page | #4027, #4028, #4032 (cited) |
| One job list, and a startup check that none went missing | #4044 (cited) |
| Missing files reported instead of warned about, in the file-hash backfill | #4184 (by title; the write-up cited the closed drafts #4158, #4170 and #4174) |
| A job too big for one run now continues under the same operation | #4205 (checked) |
| One bad audio clip can no longer freeze all the listening helpers | #4193 (by title; the write-up cited the closed draft #4182) |
| Test-fixture checks run as three parallel workflows | #4068, #4204 (checked) |
| Test-system package installs retry instead of hanging | #4131 (checked) |
| Database tests that waited on disk now run in memory | #3708 (checked) |
| A flaky cancel test fixed | #4157 (checked) |
| Automated tests no longer reach paid services | #3998 (checked) |
