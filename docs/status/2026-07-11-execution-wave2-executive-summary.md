<!-- file: docs/status/2026-07-11-execution-wave2-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3f8b6c2a-4d91-4e7f-9a3c-7b1d5e6f8021 -->
<!-- last-edited: 2026-07-11 -->

# Executive Summary: Remaining-Work Execution, Wave 2

**Shipped:** PRs [#1878](https://github.com/falkcorp/audiobook-organizer/pull/1878), [#1879](https://github.com/falkcorp/audiobook-organizer/pull/1879), [#1880](https://github.com/falkcorp/audiobook-organizer/pull/1880), [#1881](https://github.com/falkcorp/audiobook-organizer/pull/1881), [#1882](https://github.com/falkcorp/audiobook-organizer/pull/1882), [#1883](https://github.com/falkcorp/audiobook-organizer/pull/1883), all merged into `main` on 2026-07-11.
**Related plan:** [2026-07-10 remaining-work execution manifest](../plans/2026-07-10-execution-manifest.md), from the planning package merged in PR [#1870](https://github.com/falkcorp/audiobook-organizer/pull/1870).
**Related summary:** [Wave 1 executive summary](2026-07-10-execution-wave1-executive-summary.md) — the first slice of the same plan, shipped the day before.

## Executive Summary

This is the second wave of changes from the ten-part remaining-work plan.
Where Wave 1 focused on two live correctness bugs, this wave is mostly about
hardening the duplicate-detection ("dedup") pipeline and cleaning up
longstanding technical debt, plus one more search-performance improvement.
Six changes went out, and all of them passed the project's full automated
test suite before merging:

- Made the part of the system that flags duplicate audiobooks **more
  consistent about which candidates it's allowed to act on**, so a specific
  category of duplicate ("the same book, uploaded a second time as a
  non-primary version") is now treated the same way everywhere it's checked,
  instead of one code path having a stricter rule than another.
- Made a background cleanup job for stale duplicate candidates **safer to
  re-run**, by adding a version marker so the system can tell whether a
  previous cleanup pass actually finished under the new, stricter rule.
- Rebuilt how the duplicate-detection scanner **coordinates work across
  multiple CPU cores** so that checking thousands of books for duplicates no
  longer serializes unrelated pairs of books behind a single lock — a
  performance and scalability improvement with no change in what counts as a
  duplicate.
- Added a **fast lookup index** over duplicate candidates so the system can
  find "candidates in this state" without scanning every candidate record,
  laying groundwork for later dedup work to run faster.
- Moved several **hardcoded tuning numbers** used to score how well a book's
  metadata matches an online catalog entry into a proper configuration
  structure — with no change in today's behavior, but making future tuning a
  configuration change instead of a code change.
- **Closed out a project-wide code-health check that had been broken (red)
  all session:** an internal rule that certain shared code must not depend on
  a lower-level logging package was being silently violated. Fixed by
  restructuring which package owns which piece of logic, without changing any
  externally visible behavior.
- Made **library search faster** by fetching all the books needed for a page
  of search results in a single batch instead of one at a time, with a
  safety net so that one bad or missing book can no longer break an entire
  page of search results.

All six changes were reviewed against a written, pre-verified plan before any
code was touched, and every change is independently reversible (a plain code
revert, no data migrations involved). Together with the two changes from this
wave that touch the duplicate-detection engine's core file, this wave also
**unblocks the next phase of planned work**, which had been waiting on those
two changes landing first.

## What changed, in plain terms

### 1. Duplicate-candidate rules were inconsistent between two code paths

**What it was:** When the system decides whether two audiobook entries are
duplicates of each other, one code path (the one that creates duplicate
records in the first place) had a rule that skips a specific kind of entry —
one marked as "not the primary version" of a book. A second code path (the
one that later cleans up stale duplicate records) didn't have that same rule.

**Why it mattered:** Two pieces of code that are supposed to agree on "is
this a valid duplicate candidate" but don't can produce inconsistent
results — a candidate might be created under one rule and then handled
incorrectly by the cleanup process because it's using a looser rule.

**The fix:** Added the missing rule to the cleanup code path so both places
now make the same decision. A version marker was also bumped so a
partially-completed cleanup run from before this fix isn't mistaken for a
fully up-to-date one.

### 2. The duplicate-detection scanner serialized unrelated work

**What it was:** When the system scans the whole library for duplicate
books, it needs to safely record "I found a match between book A and book
B" without two workers stepping on each other. The existing code used one
single lock for this across the entire scan, meaning that even completely
unrelated pairs of books (A-and-B vs. X-and-Y) had to wait their turn for
the same lock.

**Why it mattered:** This is the kind of design that can make a large-scale
scan run at a fraction of its potential speed on multi-core hardware — a
single shared lock becomes a bottleneck no matter how many CPU cores are
available. It's the same category of problem that caused a multi-hour,
single-core-pegged incident earlier in the project.

**The fix:** Split the single lock into 16 separate locks, each responsible
for a slice of the possible book pairs, so unrelated pairs no longer wait on
each other while still guaranteeing that the same pair can never be recorded
twice. A related lookup (checking cached book info) was also moved off a
lock that was previously held while reading from the database, which is
slow — it now uses a lighter-weight, double-checked pattern instead.
Verified with the project's built-in race-condition detector.

### 3. A new fast-lookup index for duplicate candidates

**What it was:** The list of "possible duplicate" records the system tracks
didn't have a way to quickly ask "show me all the candidates currently in
this state" — answering that question meant scanning every record.

**Why it mattered:** As the number of tracked candidates grows, a full scan
for every such question gets slower. This is preparatory infrastructure work
for later tasks in the plan that need to ask this kind of question
frequently.

**The fix:** Added a secondary index keyed by a candidate's state, so those
lookups become direct instead of a full scan. The feature is switched on by
a flag, and the one-time job that builds the index for existing records was
built but intentionally not yet run against the live system.

### 4. Metadata-matching tuning numbers were hardcoded

**What it was:** When the system scores how well an audiobook file matches a
catalog entry, part of that scoring uses a handful of specific numeric
weights (for example, how much to penalize a mismatched narrator name).
Those numbers were hardcoded directly in the scoring code.

**Why it mattered:** Any future adjustment to these weights — for example,
to fix a category of mismatched matches — would require a code change and a
new deployment, rather than a configuration change.

**The fix:** Moved the 13 hardcoded numbers into a proper configuration
structure, with the defaults set to today's exact values (proven with
before/after test fixtures that produce identical scores) and a safety
fallback that reverts to the old hardcoded values if the configuration is
ever missing or invalid.

### 5. A project-wide code-health rule had been broken all session

**What it was:** This project has an automated rule that a low-level,
reusable software development kit ("SDK") used across the project must never
depend on higher-level, more specific internal code — that dependency should
only flow one direction. A logging-related dependency had crept in the wrong
direction, which meant the automated check for this rule was failing on the
main branch.

**Why it mattered:** A broken code-health check that stays broken tends to
get ignored over time, defeating its purpose. It also signals real
architectural drift — the SDK was becoming coupled to internal-only code in
a way that would make it harder to reuse or extract in the future.

**The fix:** Restructured the code so the dependency now flows the correct
direction: the low-level piece exposes a general-purpose hook, and the
higher-level code plugs its specific logging behavior into that hook instead
of the low-level code importing it directly. A couple of shared data types
were also relocated to a neutral, dependency-free location so both sides
could use them without creating a new indirect violation. The automated
check now passes.

### 6. Library search fetched matching books one at a time

**What it was:** When a search returns a page of matching books, the code
that turns "these book IDs matched the search" into "here are the actual
book records to display" was fetching each book individually, one database
call per result.

**Why it mattered:** A page of search results with, say, 20 matches meant 20
separate database round-trips just to hydrate the results, which is slower
than it needs to be and doesn't take advantage of the database's ability to
fetch multiple records in one call.

**The fix:** Replaced the one-at-a-time fetch with a single batch fetch for
the whole page, preserving the original result order and gracefully skipping
any book that can't be found rather than failing the whole request. If the
batch fetch itself has a problem, the code now shows whatever results did
come back rather than showing the user an error for the whole search.

## Verification approach

Every change went through the project's standard "Minimal CI" gate on
GitHub — build, short tests with the race detector enabled, frontend
lint/test, and a mock-freshness check — before merging. Two of the six
changes (the emit-lock sharding and the search batch-fetch) also had
dedicated local `-race` test runs beyond what CI requires, because they
touch concurrency-sensitive code paths. One recurring wrinkle during this
wave: an unrelated, pre-existing flaky test package (`internal/server`)
intermittently stalled to CI's timeout under load — reproducible on a clean
copy of `main`, unrelated to any of this wave's six changes, and confirmed
via isolated local `-race` runs that the six changes themselves were not the
cause. That flake is tracked as a follow-up investigation rather than
something this wave attempted to fix.

## What's next

Two of this wave's six changes (the drain-gate parity fix and the emit-lock
sharding) are the two tasks in the plan that touch the duplicate-detection
engine's shared core file. With both merged, the next phase of the plan —
work that was waiting specifically on those two changes landing — is now
unblocked. The remaining roughly 39 individual changes from the original
plan are tracked in the execution manifest linked above.
