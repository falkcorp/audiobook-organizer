<!-- file: docs/status/2026-07-11-execution-wave2b-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9d4e7f31-6b8c-4a2d-b5e9-1c3f8a7d6042 -->
<!-- last-edited: 2026-07-11 -->

# Executive Summary: Remaining-Work Execution, Wave 2b

**Shipped:** PRs [#1885](https://github.com/falkcorp/audiobook-organizer/pull/1885), [#1886](https://github.com/falkcorp/audiobook-organizer/pull/1886), [#1887](https://github.com/falkcorp/audiobook-organizer/pull/1887), [#1888](https://github.com/falkcorp/audiobook-organizer/pull/1888), all merged into `main` on 2026-07-11.
**Related plan:** [2026-07-10 remaining-work execution manifest](../plans/2026-07-10-execution-manifest.md), from the planning package merged in PR [#1870](https://github.com/falkcorp/audiobook-organizer/pull/1870).
**Related summaries:** [Wave 1 executive summary](2026-07-10-execution-wave1-executive-summary.md), [Wave 2 executive summary](2026-07-11-execution-wave2-executive-summary.md) — the two earlier slices of the same plan.

## The most important thing in this wave: a confirmed prod data-loss bug (not fixed yet — needs a human decision)

One of these four changes is test-only and does not fix anything. It exists specifically to
settle, with a real automated test against the real production database engine, whether a
long-suspected bug is actually happening. **It is.**

**What's broken:** When a user organizes a book into a new location and the system creates an
"organized version," it also writes back a small update to the *original* book record — marking
it as a non-primary version of the new one. That write-back silently erases the original book's
**Author and Series** information, because the record being written was built from a data path
that never carried Author/Series to begin with, and the database layer's "don't overwrite good
data with a blank" safety net (which already protects several other fields) doesn't cover
Author/Series. This is not a new bug — it was suspected and documented on 2026-07-07 — but this
wave's test is the first proof that it happens against the actual production database engine, not
just a in-memory approximation of it.

**Why it wasn't fixed in this wave:** the correct fix is more delicate than "just re-fetch the
record first," because that same write-back also has to demote the original book to non-primary
so the system never ends up with two "primary" versions of the same book. If the re-fetch step
itself fails, the fix has to decide which invariant to protect — never leave Author/Series wiped,
or never leave two primary versions — and that's a real trade-off a human should sign off on, not
something to silently pick during an autonomous pass. The new test is written to serve as the
acceptance check for whichever fix gets approved: it currently skips itself with an explicit
"KNOWN BUG CONFIRMED" message and will turn into a real pass once the fix lands.

**What to do next:** this needs your decision on the trade-off above, and then a tracking GitHub
issue and a follow-up fix PR — both intentionally left undone here. See the escalated entry in
`TODO.md` ("🔴 HIGH — PROD DATA-LOSS: CreateOrganizedVersion original-book slim-writeback wipes
Author/Series") for the exact file and line references.

## The other three changes (low-risk, all shipped)

### A CI safety check had a blind spot for nested folders

**What it was:** The automated check that verifies generated mock code is up to date used a
folder-matching pattern that only looked one level deep. Mock folders nested two or more levels
down (which do exist in this project) were invisible to the check — they could go stale and CI
would never notice.

**Why it mattered:** This is the exact kind of gap that let a stale nested mock slip past review
earlier in the same work session.

**The fix:** Changed the pattern to explicitly match at any depth. No mocks were actually stale
today — this closes the blind spot before it causes a real problem, not because it already had.
**Note:** a second copy of the exact same broken pattern was found in the local `make
mocks-check` target (`Makefile`) during this wave's review — it wasn't touched (out of scope for
this PR) and is now a tracked open follow-up.

### A hardcoded list moved to a place operators can extend

**What it was:** The list of "boilerplate" audiobook titles the duplicate-detection system
ignores (things like generic placeholder titles that aren't real book identifiers) was hardcoded
directly inside a large, sensitive file that several other changes this session also had to
modify.

**Why it mattered:** Two problems at once: hardcoded lists can't be extended without a code
change, and living inside a heavily-edited shared file made this list an unnecessary source of
merge friction for unrelated work.

**The fix:** Moved the list into its own small file with the exact same default values, and made
it extensible through configuration. Nothing about which titles are treated as boilerplate changed
today.

### Search results can now show facet counts

**What it was:** Search results already supported filtering by genre, language, and tag, but the
response didn't tell the frontend how many results exist in each category — so a filter UI
couldn't show "Fantasy (42)" without a separate query.

**Why it mattered:** This is groundwork for a nicer filtering experience; without counts, any
future genre/language/tag filter UI would need extra round-trips or would have to guess.

**The fix:** Added the counts as new fields in the search response, computed by one shared
function used both when a live search runs and when the results are pre-warmed into cache — so
the two code paths can never disagree on the numbers. If anything goes wrong computing them, the
new fields are just left out and the rest of the response is unaffected; no existing frontend code
needs to change to keep working.

## Verification approach

All four changes passed the project's standard "Minimal CI" gate (build, short tests with the
race detector, frontend lint/test, mock-freshness check) before merging. The data-loss-confirming
test (#1887) is deliberately a `t.Skipf`, not a `t.Fatal` — it documents the bug without failing
CI, since fixing it is out of scope for this PR.

## What's next

- **Human decision needed** on the Author/Series write-back fix trade-off (see above) before any
  code changes there.
- The Makefile mock-freshness follow-up (same bug #1886 fixed in CI, still present locally) is a
  small, low-risk next-wave candidate.
- INIT-4 (filtering-search) is now 5 of 6 tasks shipped — only T06 (heavy-filter perf pushdown)
  remains, and it's flagged as review-critical since it touches search query construction.
- The remaining ~35 tasks from the original 50-task catalog are tracked in the execution manifest
  linked above.
