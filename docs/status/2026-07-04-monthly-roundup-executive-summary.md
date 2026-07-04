<!-- file: docs/status/2026-07-04-monthly-roundup-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5b0ee171-8a4f-4669-a2dd-91ffeabaa486 -->
<!-- last-edited: 2026-07-04 -->

# Executive Summary: June–July Monthly Roundup

**Shipped:** PRs [#1240–#1799](https://github.com/falkcorp/audiobook-organizer/pulls?q=is%3Apr+is%3Amerged+merged%3A2026-06-05..2026-07-04), covering 2026-06-05 through 2026-07-04 (412 merged PRs total; 405 non-dependency-bump PRs)
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

Dependency updates this month (7 pull requests, entirely automated version
bumps) had no behavior changes worth a full write-up and are noted here for
completeness rather than broken out into their own section.
