<!-- file: docs/status/2026-07-10-execution-wave1-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9c4e2b71-5a83-4f96-b0d7-1e6c8a3f5d29 -->
<!-- last-edited: 2026-07-10 -->

# Executive Summary: Remaining-Work Execution, Wave 1

**Shipped:** PRs [#1871](https://github.com/falkcorp/audiobook-organizer/pull/1871), [#1872](https://github.com/falkcorp/audiobook-organizer/pull/1872), [#1873](https://github.com/falkcorp/audiobook-organizer/pull/1873), [#1874](https://github.com/falkcorp/audiobook-organizer/pull/1874), [#1875](https://github.com/falkcorp/audiobook-organizer/pull/1875), all merged into `main` on 2026-07-10/11.
**Related plan:** [2026-07-10 remaining-work execution manifest](../plans/2026-07-10-execution-manifest.md), from the planning package merged in PR [#1870](https://github.com/falkcorp/audiobook-organizer/pull/1870).

## Executive Summary

Earlier this month we produced a full planning package covering ten separate
areas of outstanding work in the audiobook library manager. This wave is the
first slice of that plan actually built, tested, and shipped. Five changes
went out, and all of them passed the project's full automated test suite
before merging:

- Fixed a **search-ranking bug** where a setting meant to make title and
  author matches count more than random tag matches was silently doing
  nothing — search results were being ranked as if every piece of
  information about a book mattered equally.
- Fixed the **single highest-priority correctness bug** in the plan: filtering
  your library by read status (for example, "show only unread books") was
  quietly being ignored, so a filtered search could return books that didn't
  match the filter at all.
- Closed a **background-process cleanup gap** where four routine
  cache-refresh tasks could keep running briefly after the server was told to
  shut down, instead of stopping cleanly — a class of bug that has caused
  real reliability incidents before.
- Unified **two separate pieces of code that scored how well a book's runtime
  matched its expected duration**, which could occasionally disagree with
  each other. We proved the replacement produces identical scores to the old
  code before switching over, so there's no behavior change — just one
  correct answer instead of two that might drift apart.
- Turned on a **duplicate-detection feature that had been silently disabled**
  since it was built: the part of the system that spots "same book, same
  folder, different file format" duplicates existed in full but was wired to
  a stub that always returned "nothing found." It now returns real results.

All five changes were reviewed against a written, pre-verified plan before
any code was touched, and every change is independently reversible (a plain
code revert, no data migrations involved).

## What changed, in plain terms

### 1. Search relevance boosts were dead code

**What it was:** The search engine has a setting that's supposed to make a
match in a book's title or author name count for more than a match in, say,
a loosely-related tag. The plumbing to read that setting existed, but a
coding mistake meant the value was read and then never actually used when
scoring results.

**Why it mattered:** Every search result was effectively being ranked as if
title, author, and random metadata were all equally important — meaning the
most relevant book for a given search term didn't reliably show up first.

**The fix:** Wired the setting into the part of the code that actually scores
matches, and added tests that search results with a title match now
correctly outrank results that only match on secondary metadata.

### 2. Read-status filtering silently did nothing

**What it was:** When a user searched their library while also filtering by
something like read status or reading progress, the filter value was being
computed correctly — and then thrown away without ever being applied to the
results.

**Why it mattered:** This was a plain, visible correctness bug: searching for
"unread books" could return books the user had already finished, with no
error or warning that anything was wrong. It's exactly the kind of bug that
quietly erodes trust in search results.

**The fix:** The filter is now actually applied to search results.
If the library's owner state can't be read for some reason, the code fails
open (shows the results anyway rather than hiding everything), and logs a
warning — plus there's a configuration switch to turn this feature off
entirely if it ever needs to be disabled in an emergency.

### 3. Cache-refresh tasks could outlive a server shutdown

**What it was:** Four background tasks that refresh cached counts (things
like how many books are in each genre, or total library size) were started
as "fire and forget" — the server didn't keep track of whether they were
still running, so it couldn't guarantee they'd finished before shutting down.

**Why it mattered:** This is the same category of bug that has caused real
incidents in this project before: something started in the background
outlives an intentional shutdown or restart, potentially leaving things in a
half-finished state.

**The fix:** All four tasks are now registered with the server's shutdown
tracker, the same mechanism already used for similar background work, so the
server can properly wait for them (or cancel them cleanly) during a
shutdown.

### 4. Two duration-scoring formulas that could disagree

**What it was:** When the system tries to match an audiobook file to the
right online catalog entry, it partly relies on comparing the file's actual
runtime to the expected runtime from the catalog. Two different pieces of
code did this comparison, and while they were meant to agree, they weren't
guaranteed to.

**Why it mattered:** Having two sources of truth for the same calculation is
a maintenance risk — a future change to one function but not the other could
silently make matching less accurate in ways that are very hard to notice
without side-by-side comparison.

**The fix:** Before making any change, the old code's exact scoring behavior
was captured as a set of test cases. Then the two functions were replaced
with one shared implementation, and the test cases confirmed the new code
produces the exact same scores as the old code across dozens of scenarios —
this was a cleanup with zero change in matching behavior, not a re-tuning.

### 5. A duplicate-detection feature that was built but never turned on

**What it was:** The system is designed to detect several different kinds of
duplicate audiobooks — for example, "the same book title sitting in the same
folder, once as an M4B file and once as an MP3." The matching logic for this
specific case had been fully written, including the part of the interface
that shows it to users, but the underlying data lookup it depended on was
left as a placeholder that always reported "nothing found."

**Why it mattered:** This meant a whole category of duplicates that the
system was designed to catch was invisible to users, even though every other
piece of the feature was in place and working.

**The fix:** Implemented the missing data lookup for real, on both of the
storage paths the system supports, with a shared test that runs the same
scenarios against both to make sure they agree — including a case where a
book's files are split across multiple folders and correctly gets skipped
rather than incorrectly grouped.

## What's next

This was the first wave of a much larger plan; the remaining work (roughly
forty-five more individual changes) is tracked in the execution manifest
linked above, along with a handful of items that require a human decision
before any work can start (for example, a proposed change to how the system
talks to torrent software, which needs to be tested against real software
first). A second wave of five changes was started the same day but not
completed in time to ship — that work is safely saved on unmerged branches
for a future session to pick up, none of it reached production.
