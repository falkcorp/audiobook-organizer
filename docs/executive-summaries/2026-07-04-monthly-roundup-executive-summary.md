<!-- file: docs/executive-summaries/2026-07-04-monthly-roundup-executive-summary.md -->
<!-- version: 1.2.0 -->
<!-- guid: 5b0ee171-8a4f-4669-a2dd-91ffeabaa486 -->
<!-- last-edited: 2026-07-11 -->

# Executive Summary: June–July Monthly Roundup

**Shipped:** PRs [#1240–#1893](https://github.com/falkcorp/audiobook-organizer/pulls?q=is%3Apr+is%3Amerged+merged%3A2026-06-05..2026-07-11), covering 2026-06-05 through 2026-07-11 (~430 merged PRs total; the July 5–11 remaining-work execution added 12 code/CI PRs (#1878–#1888) plus the same-day Author/Series data-loss fix, #1893)
**Related doc:** [2026-07-03-itl-hardening-executive-summary.md](2026-07-03-itl-hardening-executive-summary.md) — full write-up of this month's iTunes library (`.itl`) write-back hardening (K13–K17), linked rather than repeated below.

This is a monthly roundup rather than a single-change summary: instead of one
section per pull request, each theme below groups the arc of a related set of
changes. The most important pull request numbers are named inline as evidence.

## Executive Summary

- **iTunes library (`.itl`) write-back hardening.** We closed out a
  multi-week effort to make the tool's direct writes into a user's iTunes
  library provably safe — including a finale (K13–K17) covered in its own
  linked report — after a run of foundational fixes (an atomic-write
  protocol and a "does this write make sense" safety-contract framework)
  earlier in the month.
- **Duplicate-detection pipeline overhaul.** The system that finds and
  merges duplicate audiobooks got a substantial rebuild: confidence-tiered
  automatic resolution, per-model matching thresholds, and fixes for two
  separate false-positive floods that were merging books that weren't
  actually duplicates.
- **Flexible AI backend (local vs. cloud).** Users can now switch between
  local, self-hosted AI models and cloud services (like OpenAI) for both
  matching text and generating embeddings (numeric fingerprints used for
  similarity search), instead of being locked into one — though the feature
  shipped with a same-week regression that was caught and fixed fast.
- **Book and chapter data-integrity fixes.** Several bugs were fixed where
  the library's internal database could silently lose or corrupt book
  data — including two cases where a core number (track duration) was
  simply wrong across the entire library.
- **Whisper transcription pipeline and GPU infrastructure.** The
  speech-to-text pipeline used to identify unlabeled audio picked up
  reliability fixes after we found it was quietly skipping large portions
  of the library instead of processing everything.
- **Database and storage architecture.** The underlying storage engine was
  simplified — a legacy SQLite database option and its associated system
  dependency were removed entirely, and a single, very large storage file
  was split into much smaller, more maintainable pieces.
- **Consultancy-driven architecture, performance, and process refactor.** An
  outside review of the codebase's architecture and performance drove a
  wave of structural cleanup — boot-sequence simplification, logging
  consistency fixes across dozens of call sites, and process changes meant
  to prevent this kind of accumulated debt going forward.
- **Security hardening.** A penetration test and a docs/config audit turned
  up and fixed a serious batch of issues, from a committed secret key to
  unmasked credentials in an API response.
- **CI reliability, concurrency races, and org migration.** Automated
  testing got more trustworthy this month: flaky tests were fixed, real
  concurrency races in the storage layer were closed, and infrastructure
  work supported an organization migration and nightly automation.
- **Frontend reliability and memory leaks.** The web interface had two
  serious memory problems fixed (one capable of consuming 20 GB of
  browser-tab memory, another exploitable via a crafted URL), plus a
  same-day revert of a dependency bump that had crashed the entire UI in
  production.
- **Store-getter fidelity unification.** The recurring class of bug where
  saving a book stripped heavy fields (fingerprints, durations) was attacked
  at its root: the distinction between a "full-fidelity" record and a
  "lightweight" in-memory copy is now encoded in the data type the code
  hands around, so the compiler itself prevents accidentally writing a
  lightweight copy back over a full record. A production data fix corrected
  a per-book duration field on 2,781 records.
- **Whole-library concurrency sweep.** Fifteen maintenance operations that
  walked the entire library one item at a time on a single CPU core were
  rewritten to use bounded worker pools. The headline case: a duplicate-scan
  had gone silent for over three hours pinned at 100% on one core; that bug
  and a latent race in the book-merge path were both fixed.
- **Full-scan nine-hour freeze root-caused and fixed.** A full
  duplicate-scan operation could freeze for roughly nine hours. Two root
  causes were found: the storage engine was stalling under a file-sync lock
  (relaxing the sync mode cut a nine-hour run to under two minutes, #1855)
  and an inefficient identifier-collection step that scaled quadratically
  with library size was made linear (#1857). Verified in production at
  roughly 606 books per second across 44,300 books.
- **First stable release since the organization migration — v0.217.7.**
  Seven unrelated release-blocking bugs across the build and release
  pipeline were fixed, producing the project's first clean stable release
  since the organization migration.
- **Duplicate-detection label quality and orphan cleanup.** The
  gold-standard label set the duplicate detector calibrates against was
  rebuilt, 14,257 orphaned AI-embedding records (pointing at already-deleted
  books) were purged, and the full-scan operation gained progress and
  time-remaining reporting. A precision ceiling in the detector was traced
  to contaminated "not a duplicate" labels rather than to the underlying
  model.
- **Remaining-work execution wave (PRs #1871–#1888).** A planned catalog of
  outstanding work was executed as ten code and CI pull requests across
  three waves: a dead search-relevance boost was fixed and per-user search
  filters that were being silently ignored were restored (#1871, #1874); a
  duplicate-detection candidate index (#1878) and a previously stubbed
  "same folder" duplicate-detection tier (#1875) were brought live;
  metadata-scoring constants were made configurable with no behavior
  change (#1879); a duplicate-scan hot loop was de-serialized off a single
  project-wide lock and verified race-free (#1883); search results are now
  hydrated in one batched database call instead of one call per result
  (#1882); the boilerplate-title blocklist used by duplicate detection was
  made operator-extendable (#1885); a broken internal dependency-direction
  guard was fixed (#1880); and a gap in a CI freshness check that could let
  nested test-mock files go stale unnoticed was closed (#1886).
- Dependency updates (7 PRs, entirely automated) were routine version bumps
  with no behavior changes and aren't broken out into their own section
  below.

**Highest-risk items this month** — the ones a stakeholder most needs to
know about, because each one touched security or could have destroyed user
data before it was caught:

- **#1574** — a secret key had been committed directly into the repository;
  it was scrubbed and the login flow hardened.
- **#1306** — a professional penetration test found 11 issues (2 critical,
  4 high-severity); all were remediated.
- **#1485** — the configuration API endpoint was returning secrets
  (credentials, API keys) in plain text to anyone who called it; now
  masked.
- **#1758** — real internal server IP addresses had been committed into
  deployment documentation; replaced with placeholder addresses.
- **#1549** — a bug in the "merge duplicate files" cleanup path could
  permanently delete a book's associated files instead of reattaching them
  to the surviving record.
- **#1552 / #1747** — two separate bugs where saving a book record back to
  the database would silently strip out real data (an audio fingerprint,
  and other book fields) that had been loaded into memory.
- **#1555 / #1523** — two bugs where a core number was wrong for the
  *entire* library at once: track duration was calculated roughly 2x off in
  one case, and stored in the wrong unit (milliseconds instead of seconds)
  in the other.
- **#1647 / #1653 / #1689 / #1694** — four bugs in the transcription
  pipeline that were each silently processing or recording less than the
  full library (skipping most books, missing single-file audiobooks,
  dropping empty file uploads, and failing to record statistics).
- **#1779** — a real production race condition where a duplicate,
  overlapping import trigger could cause the process to hang indefinitely.
- **#1781** — a latent bug where every attempt to write a file tag would
  fail with a server error if a particular optional integration (Deluge)
  wasn't configured.
- **#1786** — a production regression introduced by the new AI-backend
  toggle feature in the same week it shipped; caught and fixed before it
  caused lasting damage.
- **#1787** — a browser memory leak that could grow a single open tab to
  20 GB of RAM usage.
- **#1789** — a crafted URL parameter could be used to force the
  application to run out of memory (an out-of-memory, or OOM, condition).
- **#1431** — a routine dependency bump (a JavaScript build tool, Vite,
  version 7 to 8) crashed the entire frontend in production; reverted the
  same day it was noticed.
- **#1887 / #1893** — a production data-loss bug: creating an "organized
  version" of a book silently erased that book's Author and Series
  information. A test proved this happened against the real production
  storage engine, not just an in-memory approximation of it (#1887); the
  fix landed the same day on explicit sign-off (#1893), fetching the full
  book record before the write instead of a stripped-down copy, with a
  safe fallback so a book is never left in a broken half-organized state
  if that fetch fails.

Verification note: each item above shipped with its own fix verified in
context (tests, code review, or direct reproduction of the original bug);
the `.itl` hardening work additionally carries a concrete verification
result — clean runs against two real production libraries — detailed in
the linked report.

## What changed, in plain terms

### 1. iTunes library (`.itl`) write-back hardening

**What it was:** This month's headline safety project. The tool writes
directly into iTunes' internal library database file, and early in the
month that write path had no real safety net — a bug in the code could
write malformed data straight into a user's library with nothing to catch
it.

**Why it mattered:** A user's iTunes library represents years of
organization across tens of thousands of tracks. A single bad write can
corrupt or destroy that library, and until this month's work, several ways
that could happen were still open.

**The fix:** Earlier weeks landed the foundation — an atomic write protocol
(so a write either fully succeeds or leaves the original file untouched,
never a half-written file) and a safety-contract framework that other
checks could plug into. That groundwork set up the finale: five specific
corruption-prevention mechanisms (identity fingerprinting, a blast-radius
sanity check, closing a safety bypass, and more), covered in full in the
linked report rather than repeated here. See
[2026-07-03-itl-hardening-executive-summary.md](2026-07-03-itl-hardening-executive-summary.md)
for the complete breakdown, including verification against two real
production libraries.

### 2. Duplicate-detection pipeline overhaul

**What it was:** The tool's duplicate-audiobook detector compares books
using text similarity, audio fingerprints, and (newer this month) AI-based
embeddings — numeric representations of a book's title/author/audio that
let the system measure how similar two entries really are. Over the month,
several distinct problems surfaced: some legitimate books were being
flagged as duplicates of unrelated books (a "false-positive flood"), and a
race condition let two duplicate-processing operations run against the
same data at the same time.

**Why it mattered:** A false-positive flood risks merging two entirely
different audiobooks into one record, silently losing data. The race
condition (#1779) could corrupt the index used to track which duplicates
had already been resolved, and in production was linked to import
operations hanging indefinitely.

**The fix:** The team added an AcoustID (audio fingerprint) veto so
uncertain text matches get double-checked against acoustic data before
being merged (#1736), tightened metadata matching to require title
agreement (#1734), introduced a confidence-tiered auto-resolve system that
only acts automatically on the most certain matches while gating riskier
ones behind manual review (#1783), and made the underlying index writes
atomic to close the race condition (#1779).

### 3. Flexible AI backend (local vs. cloud)

**What it was:** Historically the tool's AI-assisted features — text
matching and embedding generation — were tied to a single provider. This
month added a toggle so those features can run against either a local,
self-hosted model or a cloud provider such as OpenAI (#1775), with
per-model matching thresholds so each backend's particular quirks are
accounted for (#1774) and a matching UI control (#1784).

**Why it mattered:** Users without an OpenAI account, or who prefer
keeping data local for privacy reasons, previously couldn't use these
features at all. But the same feature also shipped a genuine regression
the same week: certain server configurations without a cloud API key
configured would fail to start the duplicate-detection engine at all
(#1786) — a reminder that flexibility features need the same scrutiny as
any other production change.

**The fix:** The regression was caught and reverted/patched within days
(#1786), and the backend-toggle feature itself was hardened with explicit
handling for "keyless" (local-only) configurations going forward.

### 4. Book and chapter data-integrity fixes

**What it was:** A cluster of bugs in how book and chapter data moves
between the in-memory representation and the on-disk database (referred
to internally as "memdb round-trips"). Saving a book back to the database
could silently drop fields — an audio fingerprint in one case (#1552), and
other book metadata in another (#1747) — because the in-memory copy didn't
carry every field the database expected. Separately, two unrelated bugs
made a core number wrong across the entire library: track durations came
from an imprecise bitrate-based estimate that was roughly double the real
value (#1555), and were stored in milliseconds instead of seconds in
another code path (#1523).

**Why it mattered:** Silent field-dropping on save is a slow, invisible
form of data loss — the user has no indication anything went wrong until
they notice, say, a missing fingerprint used for duplicate detection. Wrong
durations affect play position, chapter timing, and any feature that
depends on knowing how long a book actually is.

**The fix:** The save paths were changed to preserve every field the
database expects rather than only the ones the in-memory object happened
to be carrying (#1552, #1747). Durations are now read from the real audio
file via `ffprobe` (a standard audio-inspection tool) instead of estimated
from bitrate (#1555), and stored consistently in seconds (#1523).

### 5. Whisper transcription pipeline and GPU infrastructure

**What it was:** The pipeline that runs OpenAI's Whisper speech-to-text
model over unlabeled audio files to help identify them had four separate
bugs, each causing it to silently process or record less than the entire
library: it stopped after roughly the first 400 books instead of
continuing (#1647), it skipped about 17,000 single-file audiobooks because
of how it looked up the file to transcribe (#1653), it could submit empty
file uploads because it opened the file handle in the wrong order (#1689),
and it failed to record aggregate statistics after a code change wrapped
the underlying data store (#1694).

**Why it mattered:** Each of these bugs meant the transcription pipeline
looked like it was working — it ran without errors — while quietly
covering only a fraction of the library it was supposed to process. That's
the most dangerous kind of pipeline bug: nothing alerts anyone that work
is incomplete.

**The fix:** Each root cause was fixed individually and the pipeline's
batch size, timeout, and chunk size were also tuned for reliability
(#1703, #1704) so it fails less often under real GPU load.

### 6. Database and storage architecture

**What it was:** The team removed an entire legacy database backend
(SQLite) and its system-level dependency (CGO, a Go feature that lets code
call into C libraries) that had been kept around as a secondary storage
option (#1412). Separately, one of the largest files in the codebase — the
primary storage engine's implementation, at over 11,000 lines — was split
into 20 smaller, domain-specific files (#1791), and a duplicate-index
staleness bug in the vector-search structure (HNSW, a fast approximate
nearest-neighbor search algorithm used for embedding similarity) was
hardened (#1769).

**Why it mattered:** A secondary database backend that's rarely tested is a
liability — bugs there go unnoticed until someone hits them. And an
11,000-line single file is difficult for anyone, human or automated
reviewer, to safely reason about or modify.

**The fix:** SQLite support and its CGO dependency were removed entirely,
simplifying the build and eliminating an entire class of
backend-inconsistency bugs (#1412). The storage engine file was split
along natural domain boundaries with no behavior change (#1791).

### 7. Consultancy-driven architecture, performance, and process refactor

**What it was:** An outside architecture and performance review of the
codebase drove a sustained cleanup effort across the month: the server's
startup sequence was broken into four distinct phases for clarity (#1792),
a repo-wide sweep fixed structured-logging inconsistencies across 79
separate call sites spanning four distinct defect patterns (#1788), and
several other large refactors followed the review's recommendations.

**Why it mattered:** Accumulated architectural debt — a monolithic startup
function, inconsistent logging that made production issues hard to
diagnose — slows down every future change and makes bugs harder to find.
Consistent logging in particular is what makes the "silent failure" class
of bug (several of which appear elsewhere in this roundup) discoverable in
the first place.

**The fix:** The startup sequence was decomposed into named, independently
testable phases (#1792), and the logging sweep standardized how structured
log fields are used so log output is now consistent enough to search and
alert on reliably (#1788).

### 8. Security hardening

**What it was:** A professional penetration test surfaced 11 findings (2
critical, 4 high-severity, 5 medium) across the application (#1306). In
parallel, a documentation and configuration audit found a live secret key
committed to the repository (#1574), a configuration API endpoint
returning secrets like credentials and API keys in plain text to any
caller (#1485), and real internal server IP addresses committed into
deployment example docs (#1758).

**Why it mattered:** A committed secret key or unmasked credential exposed
through an API is an immediate compromise risk the moment anyone with
repository or API access looks at the wrong place — no exploit
sophistication required. Real internal IPs in public-facing docs give an
attacker a map of infrastructure that should never have been visible.

**The fix:** The pen-test findings were remediated (#1306), the exposed
secret was scrubbed from history and the login flow hardened (#1574), the
config endpoint now masks all secret-shaped values before returning them
(#1485), and deployment docs were rewritten with placeholder IP ranges
(#1758).

### 9. CI reliability, concurrency races, and org migration

**What it was:** Several genuine concurrency races were found and fixed in
the storage layer — a race in duplicate-status index writes tied to real
production import hangs (#1779), and a shutdown-ordering race where a
background sweep process could still be running against an already-closed
database handle (#1781, #1778). Separately, flaky tests were stabilized
(#1765), a CI timeout that was killing otherwise-healthy test runs was
extended (#1780), and infrastructure work supported a broader
organizational migration and nightly automation.

**Why it mattered:** Concurrency races are some of the hardest bugs to
catch because they only manifest under specific timing — in production,
under real load, exactly when they're most costly. A CI environment that
kills healthy tests due to an arbitrary timeout also erodes trust in the
whole test suite, making engineers more likely to ignore a real failure
buried in the noise.

**The fix:** Index writes and shutdown sequencing were made atomic and
properly ordered so there's no longer a window where a race can occur
(#1779, #1778, #1781), and the CI timeout was raised to match realistic
test run times (#1780).

### 10. Frontend reliability and memory leaks

**What it was:** Two serious browser-side memory problems were found and
fixed: a set of four unbounded-growth code paths that together could grow
a single open browser tab to 20 gigabytes of memory usage over time
(#1787), and a URL parameter (controlling how many items are shown per
page) that had no upper bound, making it possible to force an
out-of-memory (OOM) condition just by crafting a URL (#1789). Separately, a
routine dependency bump — upgrading the Vite build tool from version 7 to
8 — crashed the entire frontend in production and had to be reverted the
same day (#1431).

**Why it mattered:** A 20 GB memory leak will eventually crash a user's
browser tab or slow their whole machine, and it's the kind of bug that's
easy to miss in normal testing because it only shows up after extended use.
A crafted-URL OOM vector is worse: it's remotely triggerable by anyone who
can get a user to open a link. And a build-tool bump that crashes
production for every user shows the value of fast detection and reversion.

**The fix:** The four unbounded-growth paths were capped (#1787), the
items-per-page parameter was clamped to a sane maximum (#1789), and the
Vite upgrade was reverted the same day it was noticed, pending a proper
compatibility fix (#1431).

### 11. Store-getter fidelity unification

**What it was:** A recurring family of bugs kept resurfacing throughout the
month (see items 4 and elsewhere in this roundup): a book gets loaded into
memory, some operation saves it back to the database, and a field the
in-memory copy wasn't carrying — an audio fingerprint, a duration — gets
silently wiped because the save path can't tell the difference between a
"lightweight" in-memory copy and the full record on disk.

**Why it mattered:** Each individual instance of this bug looked like a
one-off, but by July the pattern had repeated often enough (PRs #1552,
#1747, and others earlier in this roundup) that a point fix wouldn't have
stopped the next occurrence — the underlying ambiguity between "full
record" and "lightweight copy" lived in ordinary data structures that
looked interchangeable in the code, so nothing stopped a future change from
reintroducing the same bug.

**The fix:** The distinction between a full-fidelity record and a
lightweight in-memory copy was encoded directly into the data type the code
passes around (PRs #1837–#1861), so the compiler itself now refuses code
that would write a lightweight copy back over a full record — turning a
class of silent data-loss bug into a build-time error. As part of this
work, a production data fix corrected a per-book duration field on 2,781
records that had drifted from the earlier duration-calculation bugs
described in item 4.

### 12. Whole-library concurrency sweep

**What it was:** Fifteen separate maintenance operations across the
codebase — duplicate scans, backfills, and similar whole-library jobs —
walked the entire book library one item at a time in a plain loop, using
only a single CPU core no matter how many were available. The most visible
case: a duplicate-detection full scan went completely silent for more than
three hours while pinned at 100% CPU on a single core, with no indication
anything had gone wrong short of it simply never finishing.

**Why it mattered:** On a library with tens of thousands of books, a
single-threaded loop over every item turns a job that should take minutes
into one that can take hours, and — as the three-hour incident showed —
looks indistinguishable from a hang until someone investigates. A separate,
unrelated race condition in the book-merge path was also found during this
work, sitting latent until enough concurrent operations collided.

**The fix:** All fifteen operations were rewritten to use bounded worker
pools sized to the number of available CPU cores instead of a single serial
loop, cutting wall-clock time proportionally. The three-hour duplicate-scan
hang and the latent book-merge race were both fixed as part of the same
sweep.

### 13. Full-scan nine-hour freeze root-caused and fixed

**What it was:** Separately from the concurrency sweep above, a full
duplicate-detection scan could freeze for roughly nine hours on production
data. Two distinct root causes were found once it was properly
investigated: the underlying storage engine was stalling under a
file-synchronization lock during heavy write activity, and a step that
collects unique book identifiers was written in a way that got
disproportionately slower as the library grew (a quadratic, rather than
linear, relationship between library size and time taken).

**Why it mattered:** A nine-hour freeze on a routine maintenance job isn't
just slow — it can leave the system in an ambiguous state for the entire
duration, block other operations that depend on the same data, and (before
root-caused) looks exactly like an unrecoverable hang rather than a job
that will eventually finish.

**The fix:** The storage engine's write-synchronization mode was relaxed
for this workload, cutting the nine-hour run down to under two minutes
(#1855), and the identifier-collection step was rewritten so its running
time grows linearly with library size instead of quadratically (#1857).
The fix was verified directly in production, processing roughly 606 books
per second across a 44,300-book library.

### 14. First stable release since the organization migration — v0.217.7

**What it was:** An attempt to cut a new stable release surfaced seven
separate, unrelated bugs across the build and release pipeline — issues
that had accumulated since the project's move to its current organization
and had never been exercised end-to-end by an actual release attempt.

**Why it mattered:** A release pipeline that silently fails in multiple
independent ways isn't just an inconvenience — it means users can't
reliably get a working build of the software, and each new bug found
during the process indicated another gap the team didn't know it had.

**The fix:** All seven release-blocking bugs were fixed one at a time,
producing the project's first clean stable release, v0.217.7, since the
organization migration.

### 15. Duplicate-detection label quality and orphan cleanup

**What it was:** The duplicate-audiobook detector is calibrated against a
"gold standard" set of example pairs that are already known to be
duplicates or known not to be. That label set had accumulated errors, an
unrelated cleanup found 14,257 AI-embedding records still pointing at
books that had already been deleted, and the full duplicate-scan operation
gave users no indication of how far along it was or how much longer it
would take.

**Why it mattered:** A contaminated calibration set makes it impossible to
tell whether the duplicate detector's accuracy problems come from the
model or from bad training data — without cleaning the labels first, any
tuning work risks optimizing against the wrong target. Orphaned embedding
records are pure waste that slows down every search over that data. And a
long-running scan with no progress indicator is difficult to distinguish
from the kind of silent hang described in items 12 and 13.

**The fix:** The gold-standard label set was rebuilt, the 14,257 orphaned
embedding records were purged, and the full-scan operation gained
progress and estimated-time-remaining reporting. Investigating a
long-standing precision ceiling in the detector traced the problem to
contaminated "not a duplicate" labels in the gold set rather than to the
underlying matching model — meaning the fix belongs in how labels are
produced, not in further model tuning.

### 16. Remaining-work execution wave

**What it was:** Following a planning pass that cataloged roughly fifty
remaining items of outstanding work across the codebase, a first slice of
that catalog — ten separate code and CI changes — was actually built,
tested, and shipped over July 10–11.

**Why it mattered:** Several of the ten changes fixed real, live
correctness bugs rather than just technical debt: a search-relevance
setting that was supposed to make title and author matches count for more
than random tag matches turned out to be silently doing nothing, so search
results weren't reliably ranked by relevance. Separately, per-user search
filters computed correctly but were then thrown away without ever being
applied, meaning a filtered search could return results that didn't match
the filter at all — a plain, visible bug that erodes trust in search.

**The fix:** The dead search-relevance boost was wired in (#1871) and
per-user search filters were restored so they're actually applied to
results (#1874). A duplicate-detection candidate lookup index was added
(#1878), and a previously built but never-connected "same folder,
different file format" duplicate-detection tier was wired up so it returns
real results instead of always reporting nothing found (#1875).
Metadata-scoring constants used to judge how well a file matches an online
catalog entry were moved into configuration with no change in today's
behavior (#1879). A duplicate-scan hot loop that serialized unrelated work
behind a single project-wide lock was split into sixteen independent locks
and verified free of race conditions with the project's built-in race
detector (#1883). Library search was changed to fetch all the books needed
for a page of results in one batched database call instead of one call per
result (#1882). The boilerplate-title list used by duplicate detection
(generic placeholder titles that aren't real book identifiers) was moved
out of a heavily-edited shared file into its own operator-extendable
configuration (#1885). A project-wide code-health check that verifies a
low-level shared code package never depends on higher-level internal
code — which had been failing all session — was fixed by restructuring
which package owns which piece of logic (#1880). And a gap in the CI check
that verifies generated test-mock code is up to date was closed: the
check's folder-matching pattern only looked one level deep, so mock
folders nested two or more levels down could go stale without CI ever
noticing (#1886).

One more result from this wave belongs in the highest-risk list above
rather than here: a test written to settle whether a long-suspected bug was
real confirmed that creating an "organized version" of a book silently
erased that book's Author and Series information (#1887) — a genuine
production data-loss bug. It was fixed the same day (#1893): see the
highest-risk list above for the plain-language write-up of the bug and the
fix.

Dependency updates this month (7 pull requests, entirely automated version
bumps) had no behavior changes worth a full write-up and are noted here for
completeness rather than broken out into their own section.
