<!-- file: docs/executive-summaries/2026-07-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: e5c01e3e-3ee0-41e6-8c8e-0ce53cc3d4e2 -->
<!-- last-edited: 2026-09-12 -->

# Executive Summary: July 2026

**Period covered:** 2026-06-05 through 2026-07-31. There is no separate June summary:
the early-July roundup that this file absorbs already covered work from June 5 onward,
so this document is the record for both months.
**Shipped:** roughly 430 merged pull requests between June 5 and July 11
([#1240–#1893+](https://github.com/falkcorp/audiobook-organizer/pulls?q=is%3Apr+is%3Amerged+merged%3A2026-06-05..2026-07-11)),
plus the July 12–31 work named inline below (through #2045).
**Replaces:** the 21 per-day and per-topic summaries written during July, combined here
at month end under the convention in
[`docs/process/executive-summaries.md`](../process/executive-summaries.md).
**Related specs:** the iTunes library
[identity hardening](../archive/2026-07-consolidation/specs/2026-07-03-itl-identity-and-external-truth-hardening.md)
and [file-format deep dive](../archive/2026-07-consolidation/specs/2026-07-03-itl-format-and-foolproofing-deep-dive.md);
the [two-way iTunes sync design](../specs/2026-07-23-itunes-2way-sync-system-design.md)
and its [safety findings](../specs/2026-07-23-itunes-2way-p0-findings.md); the
[phone-app sync design](../specs/2026-07-29-abs-sync-api-design.md); and the running
[duplicate-detection status record](../dedup/STATUS.md).

This is written for someone who does not work on the code. It is grouped by theme, not
by date. Pull request numbers appear only as evidence for anyone who wants to look
further.

---

## The month in one paragraph

This period was about **data that disappeared without anyone being told**. Not crashes or
error messages — saves that quietly dropped fields, merges that quietly stranded audio
files, background jobs that quietly never ran or only covered part of the library, and
review decisions that quietly came back. Most of these were found by going and looking
rather than by being reported, which means they had been happening for some time. Alongside
that repair work, the tool's writes into a live iTunes library were made provably safe and
then used for real for the first time, duplicate detection got measurably better, a
penetration test and security audit were cleared, and the groundwork was laid for listening
to the library on a phone.

## Executive Summary

- **Saving a book could erase parts of it.** Several different paths saved an incomplete
  copy of a book over the complete one, wiping author, series, fingerprints, ratings,
  transcriptions and more. Each was fixed, and the root cause was then closed in a way the
  build itself now enforces, so this class of mistake can no longer compile. A production
  repair corrected 2,781 books whose running time had drifted.
- **Merging duplicates could lose or scramble books.** Two merges running at once could
  corrupt a group of versions; a failed file move during a merge could strand audio; and an
  older merge button deleted books outright and left iTunes pointing at nothing. All three
  paths are fixed, and merges now retire entries instead of erasing them.
- **Background work reported success while doing little or nothing.** The audio
  fingerprinting job had failed on every restart for months, leaving about two-thirds of the
  library without fingerprints. Several whole-library jobs stopped at 10,000 or 20,000 books
  in a library of more than 30,000. The transcription pipeline skipped about 17,000
  single-file books. Dismissed duplicate suggestions came back after every scan. All fixed.
- **Duplicate detection got much better.** High-confidence detection went from catching
  about one in three real duplicates to about seven in ten, while its accuracy rose to
  96.7%. The duplicate backlog was cut from 9,074 suggestions to about 1,300.
- **iTunes write-back was made safe, then used.** A reversed text-encoding bug that was
  garbling libraries was fixed and five new safety checks were added; two real libraries of
  90,900 and 95,238 tracks passed every check for the first time. The tool then made its
  first real change to a live 97,999-track iTunes library, verified byte for byte.
- **Security issues were closed.** A professional penetration test found 11 issues (2
  critical, 4 high-severity), all fixed. A committed secret key, a settings endpoint that
  handed out passwords and API keys in plain text, and real internal server addresses in
  public documentation were all removed.
- **The server got faster and quieter.** An idle server that had burned two CPU cores
  around the clock since early May dropped to effectively zero. A duplicate scan that could
  freeze for nine hours now finishes in under two minutes. Fifteen whole-library jobs that
  used a single core now use all of them.
- **Groundwork for listening on a phone.** Before building, the team measured what real
  phone apps require and found five problems that would each have caused silent damage —
  including one that would have wiped every saved listening position.

**Verification:** each fix shipped with its own test, review, or reproduction of the
original bug. The larger ones were proven against a disposable full copy of the production
system before touching production, and the iTunes work was verified against real libraries.

---

## Highest-risk items

These are the ones a stakeholder most needs to know about, because each touched security or
could have destroyed user data before it was caught.

- **A secret key had been committed into the repository** (#1574). It was scrubbed and the
  login flow hardened.
- **A penetration test found 11 issues, 2 critical and 4 high-severity** (#1306). All were
  fixed.
- **The settings endpoint returned credentials and API keys in plain text** to anyone who
  called it (#1485). Secrets are now masked.
- **Real internal server addresses were committed into deployment documentation** (#1758).
  They were replaced with placeholder addresses.
- **A "merge duplicate files" cleanup could permanently delete a book's files** instead of
  reattaching them to the surviving record (#1549).
- **Saving a book could silently strip real data** — an audio fingerprint in one case,
  other book details in another (#1552, #1747).
- **A core number was wrong for the entire library at once, twice.** Track duration was
  estimated roughly twice too long in one case (#1555) and stored in milliseconds instead of
  seconds in another (#1523).
- **Four transcription bugs each processed less than the whole library** — stopping early,
  skipping single-file books, sending empty uploads, and not recording statistics (#1647,
  #1653, #1689, #1694).
- **An overlapping import trigger could make the server hang indefinitely** (#1779).
- **Every attempt to write a file tag failed** if one optional download-client integration
  (Deluge) was not configured (#1781).
- **The new local-versus-cloud AI setting broke duplicate detection** on servers with no
  cloud key, in the same week it shipped; it was fixed within days (#1786).
- **A browser tab could grow to 20 GB of memory** (#1787), and **a crafted link could force
  the server to run out of memory** (#1789).
- **A routine build-tool upgrade crashed the whole web interface in production** and was
  reverted the same day (#1431).
- **Creating an "organized version" of a book erased its author and series.** A test proved
  it against the real production storage engine (#1887); the fix landed the same day (#1893)
  and was then generalized (see theme 1).

---

## 1. Saves that quietly threw data away

The largest and most serious cluster of the period. Almost every item has the same shape:
an operation loads a **lightweight copy** of a book — one that deliberately leaves out heavy
fields so it runs fast — and then saves that copy back over the complete record. Whatever the
copy left out is simply gone.

**Fingerprints and book details dropped on save** (#1552, #1747).
*What it was:* saving a book back to the database dropped fields the in-memory copy was not
carrying, including the audio fingerprint used for duplicate detection.
*Why it mattered:* no warning; the user found out only by noticing something missing.
*The fix:* the save paths now preserve every field the database holds.

**Rescanning a file wiped twenty kinds of information.**
*What it was:* when an already-imported file was rescanned, the scanner saved a record built
only from the file's own audio tags. That erased everything tags do not carry: author and
series links when the file had no such tag, all ratings, speech-to-text transcriptions,
"metadata reviewed" status, genre, and technical audio details.
*The fix:* the scanner now starts from the book's complete existing record and changes only
the handful of fields it legitimately owns (file location, checksums, size, and tag-derived
title, author and narrator when present). A test proves twenty previously-wiped fields now
survive a rescan, and any field added in future is kept automatically.

**A repair tool that overwrote whole records with a single value.**
*What it was:* the tool that finds a real author for books credited only to a production
company saved each book carrying *only* the one value it had just worked out. Because a save
replaces the whole record, that erased the title, file location, series, narrator, genre,
ISBN and every rating for every book it touched, and broke the lookup that finds a book by
its file location.
*The fix:* it now loads the complete record, changes one value, and saves the whole thing.
If it cannot load the record first, it skips that book and logs why.

**Author and series erased from books** (#1887, #1893, and the July 13 follow-on).
*What it was:* the save routine already protected most heavy fields from being blanked by a
lightweight copy, but author and series were missing from that protected list. Any job
saving a lightweight copy — such as the one that splits "Author A & Author B" into two
authors — erased them. Creating an organized version of a book did the same.
*The fix:* the save routine now keeps stored author and series whenever an incoming save
lacks them, protecting every job of this kind, including ones nobody had found yet. The two
author-splitting jobs were changed to load the full record and set a correct new author
name, and a not-yet-used file-move step that would have wiped whole records was fixed before
it was switched on. Tests reproduce the loss against the real storage engine.

**Changing a book's author left the old name showing** (July 16).
*What it was:* the opposite failure. Every book stores both a reference to its author and a
saved copy of the author's name so lists load fast. Changing the reference did not refresh
the name, so a book could belong to "New Author" while every screen showed "Old Author." The
same applied to series.
*The fix:* an edit now refreshes the saved names before the record is written. Any book that
already had a mismatch corrects itself the next time it is edited.

**The wrong publication year and language** (July 13).
*What it was:* an audiobook has two years — when the *recording* was released and when the
*book* was first printed. For a classic those can be decades apart (a 1937 novel, a 2010
recording). Matching against a book catalog copied the print year over the correct release
year, and that wrong year was then written into the audio files' own tags on disk.
Separately, one catalog returns a jumble of languages across every edition ever published
and the tool took the first one, so an English book could vanish from the English filter.
*The fix:* the two years now live in separate fields and neither can overwrite the other.
When editions disagree on language, no language is attached — no label is better than a
wrong one. For a short while, older remembered catalog results may leave the release year
blank (never wrong) until they refresh, and remembered results that stored the year in the
wrong slot now correct themselves when read.

**Durations wrong across the entire library** (#1555, #1523).
Durations were estimated from bitrate, which came out roughly double the real value, and
one path stored them in milliseconds instead of seconds. Durations are now read from the
actual audio with a standard inspection tool (ffprobe) and stored in seconds.

**Closing the whole class at its root** (#1837–#1861).
*What it was:* by July this pattern had repeated often enough that fixing one path at a time
would not stop the next. A complete record and a lightweight copy looked interchangeable in
the code, so nothing prevented a future change from reintroducing the bug.
*The fix:* the difference is now built into the data types the code passes around, so the
build fails if code tries to save a lightweight copy over a complete record — a silent
data-loss bug became a compile error. As part of this work a production repair corrected the
per-book duration on **2,781** records that had drifted because of the duration bugs above.

## 2. Merges that lost, stranded or scrambled books

When the tool finds two entries that are really the same audiobook, it can merge them: keep
the best copy and file the others away as alternate versions. Several separate defects lived
in this area.

**Two merges at once could corrupt a version group** (July 13).
*What it was:* the automatic duplicate scan, the auto-resolve pass and manual merges from the
web page all share the same merge machinery, and nothing stopped two of them working on the
same book at the same moment. The result could be a book marked both "kept" and "filed away,"
a version group split in two, or the copy meant to be kept filed away instead — so a whole
set of versions dropped out of the library view. Audio on disk was never touched.
*The fix:* merges now take turns. A same-day follow-up put the manual "combine" action and
the duplicate-cleanup merge (used by iTunes reconcile and the merge cleanup job) under the
same lock, so no two book-merging actions of any kind can overlap on one book. Automatic
merges also now write their "how to undo this" record *before* making the change, and skip
the merge if that record cannot be written; and the currently-off AI-review merge path skips
a book already merged away earlier in the same batch.

**A failed file move during a split-book merge stranded audio** (July 16).
*What it was:* a book imported as one entry per chapter or disc can be stitched back
together. If moving one entry's files failed partway, the tool deleted that entry anyway —
leaving its audio on disk but attached to nothing, while the web page reported success.
*The fix:* a leftover entry is removed only after its files are confirmed moved. A failed
entry is left exactly as it was so the merge can be retried.

**The older merge button deleted books outright and orphaned iTunes links** (July 18).
*What it was:* it permanently deleted the extra entry, leaving iTunes identifiers pointing at
nothing and the duplicate's tracks stuck in iTunes forever. The merge could not be undone.
*The fix:* the button now uses the newer merge path, which moves iTunes links to the kept
entry, removes the duplicate's tracks from iTunes, carries across ratings and play counts,
and retires the extra entry instead of erasing it. A follow-up test found the shared merge
engine itself could still lose a book that appeared on both sides of a merge request, making
it neither the kept copy nor a retired one; that guard now lives inside the engine, so every
caller is protected.

**The quality sweep closed the remaining merge and file-safety gaps** (July 17–18). The older
merge action also orphaned ISBN and ASIN lookups; merging now moves those references to the
surviving copy and keeps a recoverable record. Renaming and organizing files now refuses to
overwrite an existing file, undoes half-finished renames instead of stranding files, and
recovers files left in a temporary state by an earlier crash. Grouped books can no longer end
up with two "main" copies.

**Cleanup actions that carried on after a failed check** (July 16).
*What it was:* before deleting a "now-empty" entry, the iTunes cleanup checks that it has no
files and no iTunes links. If that check failed to read, the missing answer was treated as
"nothing there" and the entry was deleted anyway. Separately, when combining entries with a
chosen author, a failed save of that author was reported as success.
*The fix:* an unreadable check now means "do not delete," and the dropped-author case now
logs a warning instead of passing silently.

**Deleted books kept appearing in version and work lists** (July 13).
*What it was:* a shortcut index kept a snapshot of each book so these lists load fast, and
the snapshot was not refreshed when a book was deleted, hidden or merged away. Permanently
deleting a book also left some shortcut entries behind.
*The fix:* the lists now check each book's current record and skip deleted or hidden ones;
leftover shortcuts are cleaned up on delete. In the same change, two imports creating the
same narrator at the same moment could produce a duplicate or half-written narrator; that is
now a single locked step.

## 3. Work that said it was done when it was not

**The audio-fingerprinting job failed on every restart, for months** (July 17).
*What it was:* a fingerprint is a compact signature of how a recording sounds — the most
reliable way to tell whether two books are really the same. The job that computes them
started, hit an error loading the book list, logged a failure and stopped, every time.
Nothing visible said so. About **two-thirds of the library** has no fingerprint as a result,
which is the biggest reason duplicate detection falls back on fuzzier title and duration
matching.
*Why it happened:* the database stores lookup shortcuts ("find the book with this ISBN")
next to the books. Reading "every book" walked past the books into the shortcuts and treated
an empty one as a fatal error. A rule skipped one kind of shortcut; nine more kinds had been
added since. It only broke when something read *all* books directly at startup, which is
exactly what this job did.
*The fix:* shortcuts are now recognized by their shape — every shortcut label contains a
colon and no book identifier does — so future shortcuts cannot reintroduce the failure. The
same change fixed three other jobs exposed the same way (the fingerprint rescan, the
duplicate engine, and the relink report). The missing fingerprints still need a separate
catch-up pass.

**Whole-library jobs stopped partway through** (July 16).
Several "run across the whole library" tasks asked for "all books" with a hidden ceiling of
10,000 or 20,000, in a library of more than 30,000. Anything past the cutoff was never
processed and nothing reported a partial run. Affected: splitting combined author names;
filling in metadata after an iTunes import; the "which books have incomplete metadata?"
report, which under-counted; and the one-time scan for unreadable files, which marks itself
done and would have left the rest of the library unscanned forever. All five affected lookups
now ask for the whole library, and a test keeps the ceiling from coming back. About ten other
jobs use a much higher ceiling (100,000 or one million) and are logged as a follow-up.

**The transcription pipeline covered a fraction of the library** (#1647, #1653, #1689,
#1694). The speech-to-text pipeline used to identify unlabeled audio stopped after about the
first 400 books, skipped about **17,000** single-file audiobooks, could send empty file
uploads, and stopped recording statistics. Each ran without errors. All four were fixed, and
batch size, timeouts and chunk size were tuned for real GPU load (#1703, #1704).

**Dismissed duplicates came back after every scan** (July 17).
*What it was:* you review a suspected duplicate pair and say "these are different." The next
scan put it back as if you had never looked, so the queue never shrank. The scan was careful
to protect the pair's similarity scores but not the decision itself.
*How it was proven:* running the real scan on a disposable full copy of production changed
exactly **43 pairs** from dismissed back to needs-review, and nothing else.
*The fix:* once a pair is dismissed or merged, a scan cannot revert it; only another decision
can. Dismissals already lost to earlier scans are gone and those pairs need dismissing once
more.

**Scheduled jobs that did nothing and called it success** (July 17–18). Some unfinished jobs
reported "success" every few minutes while doing nothing; they no longer run on a schedule
and state plainly that they did nothing if triggered. A job that hangs immediately is now
noticed, a stuck job can no longer block every future job of its kind, and long jobs
(re-encoding, metadata fill-in) now report real progress instead of going dark for hours.

## 4. Duplicate detection got measurably better

**Pipeline overhaul (June–early July).** Two separate false-positive floods had been
flagging unrelated books as duplicates, which risks merging two different audiobooks into
one. Uncertain text matches are now double-checked against audio fingerprints before merging
(#1736), metadata matches must agree on title (#1734), and automatic resolution acts only on
the most certain matches while riskier ones go to manual review (#1783). A race in the
duplicate-status index, linked to import hangs in production, was closed (#1779).

**Cleaner calibration data.** The reference set of known duplicate and known non-duplicate
pairs was rebuilt, **14,257** leftover AI-embedding records pointing at deleted books were
purged, and the full scan gained progress and time-remaining reporting. A long-standing
accuracy ceiling was traced to contaminated "not a duplicate" examples rather than to the
matching model itself.

**Confidence reached its target** (#1926, #1927, building on #1897–#1925).
*What it was:* the confidence rating combines several clues — text similarity, fingerprints,
durations, shared ISBNs. Tuning it kept failing with "not enough data" because pairs a person
marked "not a duplicate" were dropped from tracking before their score breakdown was saved,
so the tuning had almost no negative examples to learn from.
*The fix:* a one-time repair rebuilt **1,428** reviewed examples without changing any human
decision, the leak was closed so new decisions save their breakdown immediately, and the
rating was re-tuned. High-confidence detection went from about **one in three** real
duplicates to about **seven in ten**, at **96.7%** accuracy, and the "merge automatically"
tier is **98.3%** accurate. Previous settings were recorded so the change can be rolled back.

**The backlog was cut by about 85%** (July 17–18, #1972–#1986 and #2001–#2010).
A five-part health review (duplicate detection, file handling, background jobs, logging,
operations) produced **24 fixes** over two days. The headline: most of the **9,074**
"possible duplicate" suggestions were junk, created by books whose titles had been wrongly
taken from a chapter name. A three-step repair fixed the leaked titles, restored missing
evidence scores, and let the triage tool recognize the junk. It was proven with zero errors on
a full copy of the library, the live dry run matched that copy exactly, and with explicit
sign-off about **7,900** junk suggestions were dismissed — reversibly, not deleted — leaving
about **1,300** real ones for review.

**Other duplicate-detection improvements** (#1875, #1878, #1879, #1883, #1885). A
"same folder, different file format" tier that had been built but never connected now returns
real results; a candidate lookup index was added; matching-score constants became
configurable with no behavior change; a scan loop that queued unrelated work behind one lock
was split into sixteen independent locks and checked for races; and the list of generic
placeholder titles became operator-extendable.

## 5. iTunes: from reading the library to writing into it safely

A user's iTunes library holds years of organization across tens of thousands of tracks, and
one bad write can destroy it.

**Write-back hardening** (#1793).
- A **reversed text-encoding flag** was writing "plain English" text with the marker for
  "special characters" and vice versa, garbling names and breaking file locations so iTunes
  could not find audio. It was found by comparing against bytes iTunes itself writes.
- A **library identity fingerprint** now records a sample of track IDs, counts and a checksum,
  so the tool can tell "the same library, updated" from "an empty library iTunes rebuilt under
  the same name" — a case that really happened to this user. It refuses to write when they do
  not match.
- An **emergency "force" setting** no longer lets a rebuild skip the identity check.
- A **plausibility check** blocks a write that would change far more tracks than the operation
  should.
- A **checksum of each write** now reveals when something else changed the library in between.
- **Three failure paths that logged and moved on** now retry up to a limit and then stop with
  a clear error instead of silently dropping a write.
- Two reference documents now record the file format and each corruption scenario.

Verified on real data: a **90,900-track** production library and a **95,238-track** live
library passed every safety check cleanly for the first time.

**Proving two-way sync safe before building it** (#2041–#2045), each proof run against a copy
of the real **97,999-track** library:
- The library keeps no reliable record of which duplicates were merged into which, so the tool
  cannot safely auto-delete leftover tracks — that cleanup is deliberately switched off rather
  than guessing.
- There is not a single case where the tool would rewrite a music or podcast track.
- Updating a book's file location was proven, byte for byte on every track, to change nothing
  else: not bookmarks, play counts, ratings or dates.
- Every future update is checked track by track against what it was supposed to do, and rolled
  back automatically if anything else changed.
- One real blocker was fixed: a safety check refused to write at all because the library's own
  home folder had a name it treated as suspicious. It now tells the two apart.

**The first real write to the live library** (July 25). The tool waited for iTunes to close,
confirmed the file had not changed at the last moment, saved a full backup, corrected one
book whose file location iTunes had recorded wrongly, and re-read the result. Only that one
location changed; all **358 playlists** (including smart-playlist rules), bookmarks, play
counts and ratings were untouched, and a follow-up check showed all 97,999 tracks in sync. The
single-book change was deliberately small: it proved the full chain — wait, back up, write,
verify, undo on any surprise — on the real library. Next, the tool will also push the details
it owns (title, author, series, genre) while treating listening state as off-limits.

## 6. Speed and server load

**An idle server burned two CPU cores around the clock** (July 18).
To show a live book count, the server read and fully decoded all ~**44,000** books — about
**5.6 seconds** each time — and a dashboard task asked every five seconds, so counts ran back
to back forever, with a second core cleaning up after them. The health check called the same
count, so it too took 5.6 seconds, which monitoring can mistake for "the server is down." This
had been running since early May. The count is now remembered for 30 seconds; idle load
dropped from two cores to **effectively zero** and the health check answers in a fraction of
a second.

**Whole-library jobs moved off a single core.** Fifteen maintenance jobs walked the whole
library one item at a time on one CPU core. One duplicate scan went silent for more than three
hours pinned at 100% on one core. All fifteen now use a bounded pool of workers sized to the
machine, and a latent race in the merge path found during the work was fixed.

**A nine-hour scan freeze became under two minutes** (#1855, #1857). The storage engine was
stalling on a disk-sync lock under heavy writes, and one step got disproportionately slower as
the library grew. Relaxing the sync mode and making that step scale in proportion to library
size fixed both; verified in production at about **606 books per second across 44,300 books**.

**Metadata lookups that no longer hammer or hang** (July 13). The "10 requests a second" limit
on outside services like Audible, Open Library and Hardcover was counted per *book*, but each
book makes many requests, so batches stampeded those services. The failure breaker and rate
limiter were rebuilt for every book, so they never tripped. And one unfound Audible ID tried
nine regional stores for up to 30 seconds each — up to **270 seconds** stuck, with no way to
cancel. Limits now count real requests, the breaker and limiter are shared across a run, each
regional try is capped at 10 seconds (worst case about **90 seconds**), and cancelling an
import or batch now stops an in-flight lookup immediately.

**Search** (#1871, #1874, #1882). A setting meant to rank title and author matches above tag
matches was doing nothing, and per-user search filters were computed and then thrown away, so
filtered searches could return non-matching results. Both are fixed, and a page of results now
loads in one database call instead of one per result.

## 7. Security and the web interface

**Security** (#1306, #1574, #1485, #1758). The penetration test found 11 issues (2 critical, 4
high-severity, 5 medium), all remediated. A live secret key committed to the repository was
scrubbed from history and the login flow hardened. The settings endpoint now masks every
secret-shaped value. Deployment examples now use placeholder addresses instead of real internal
ones.

**Web interface memory and stability** (#1787, #1789, #1431). Four unbounded-growth paths that
together could grow a tab to **20 GB** were capped; a "items per page" link parameter with no
upper limit, which let a crafted link exhaust server memory, now has a sane maximum; and a
build-tool upgrade (Vite 7 to 8) that crashed the whole interface in production was reverted the
same day.

**Tag browsing** (July 11–13). The "Metadata" and "Dedup" tag bubbles showed inflated counts and
loaded no books when clicked. Checking against real data showed the cause was not cosmetic: tag
names containing a colon — which is how every automatically applied tag is labeled — were read
back wrongly, so every such tag was miscounted or unsearchable. The fix was read-only, so no
stored data needed migrating. "All Books" now reliably clears an active tag filter, and a slow
book-list load offers a cancel button after a few seconds. Whether internal bookkeeping tags
should appear in the tag list at all was left as an open preference for the owner.

## 8. Platform, releases and process

- **Local or cloud AI** (#1774, #1775, #1784, #1786). AI matching and embeddings (numeric
  fingerprints used for similarity search) can now run on a self-hosted model or a cloud service
  such as OpenAI, with per-model matching thresholds and a settings control. A same-week
  regression that stopped duplicate detection on servers without a cloud key was fixed within
  days, and key-free setups are now handled explicitly.
- **Simpler storage** (#1412, #1791, #1769). The rarely-tested SQLite database option and the
  C-library build dependency it needed were removed; an 11,000-line storage file was split into
  20 smaller files with no behavior change; and a staleness bug in the fast similarity-search
  index was hardened.
- **Architecture review follow-through** (#1788, #1792). Following an outside architecture and
  performance review, server startup was split into four named, testable phases, and a logging
  sweep fixed inconsistencies at 79 places across four defect patterns, so logs can be searched
  and alerted on reliably.
- **Races and test reliability** (#1765, #1778, #1779, #1780, #1781). A shutdown-ordering race
  where a background sweep could run against an already-closed database was fixed, flaky tests
  were stabilized, and a test timeout that killed healthy runs was raised to match reality.
  Infrastructure work supported the move to the current GitHub organization and nightly
  automation.
- **First stable release since the organization move — v0.217.7.** Seven unrelated
  release-pipeline bugs, never exercised end to end since the move, were fixed one at a time.
- **Remaining-work execution** (July 10–11, #1871–#1888). A catalog of about fifty outstanding
  items was started; the first ten code and CI changes shipped, including the search and
  duplicate-detection items above, a repaired guard on which internal code may depend on which
  (#1880), and a check for stale generated test code that had only looked one folder deep
  (#1886).
- Seven automated dependency updates had no behavior changes.

## 9. Listening on a phone: the groundwork

The goal is to listen straight from the user's own server with an ordinary iPhone app —
downloading over WiFi, listening offline in the car, and keeping the right place across devices —
instead of syncing through iTunes or Apple Books. The apps worth using speak the Audiobookshelf
protocol, which has **no maintained specification**; its own documentation says so. So the team
ran a real Audiobookshelf server, recorded every request and response, and read two phone apps'
source code line by line. That found five problems that would each have looked like a mystery
bug months later:

- **Listening positions could have been deleted.** One app removes any saved position the server
  does not mention when it checks in. Sending the list in pages — the obvious choice for a
  44,000-book library — would have wiped the user's place in every book not on the first page,
  every time the app opened.
- **Book IDs were the wrong length.** One app cuts IDs at a fixed position; ours are shorter, so
  progress would have been saved against malformed addresses. A second, correctly shaped ID is now
  issued just for the apps.
- **Finished books would have stuck at 99%.** The same book reports three total lengths that differ
  by about a twentieth of a second, so an exact comparison never triggers "finished."
- **Listening time would have recorded as zero.** The apps send "time since last check-in" and
  "total time" under similar names; reading the wrong one records nothing. A published reference
  implementation has that exact bug.
- **One app cannot log in** if a particular field is empty — and the reference implementation
  returns it empty.

The protocol choice was tested against Jellyfin, Emby, Plex, Subsonic and OPDS: Audiobookshelf has
about eight maintained iPhone apps against one or two for the others, and is the only one that
properly returns progress recorded while offline. Every endpoint stays behind the existing
Cloudflare login (verified in the apps' code, including their first connection test); the only
exception is cover images for the home-screen widget, reachable only by an unguessable ID.

Built and tested so far: reading chapter marks and joining multi-file books into one timeline
(matching the real server's numbers exactly); audio serving where seeking works and interrupted
downloads resume (tested on a 115 MB book); durable IDs that survive renames, moves, retagging and
merges; tested rules for when two devices disagree — an offline phone may move the user *forward*
but never *backward*; and a harness that compares our answers field by field against the real
server's recordings. **Known gap:** the durable-ID promise holds for the main merge path but not
yet for two others, one of which deletes records outright so a lost link cannot be recovered; that
work is scheduled. Next comes the app-facing layer itself: login, browsing and playback.

---

## Themes worth carrying forward

1. **The dangerous bugs were all silent.** Not one of the data-loss defects announced itself; they
   were found by looking. That argues for checks that verify an operation did what it claimed, not
   just that it returned without an error.
2. **"Lightweight copy" is a recurring trap.** A partial record saved over a complete one is fast
   and usually harmless — and when it is not, the missing fields are simply gone. The type-level fix
   in theme 1 is the durable answer.
3. **A job that fails at startup and carries on looks exactly like a job with nothing to do.** The
   fingerprint job went unnoticed for months for that reason.
4. **Prove it on a copy first.** The dismissed-duplicates bug, the fingerprint failure, the idle CPU
   load and the backlog cleanup were all found or proven on a disposable full copy of production —
   evidence that reading the code alone would not have produced.
