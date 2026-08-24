<!-- file: docs/executive-summaries/2026-08-24-the-list-the-merge-trusted-executive-summary.md -->
<!-- version: 1.1.0 -->
<!-- guid: 2f6b81d4-9a07-4c3e-85b1-703de9c2af58 -->
<!-- last-edited: 2026-08-24 -->

# The list the merge trusted

**2026-08-24 — the merge asked a fast index which books were in a series, and never asked
whether that index still had all of them**

**PR:** `fix/series-membership-memdb-guard` — the guard, its four tests, and this report.
**Direct sequel to:** [The copies the merge left
behind](2026-08-23-the-copies-the-merge-left-behind-executive-summary.md) (Aug 23), which
fixed *which books the question returns*. This one fixes *whether the answer can be
trusted at all*.

---

## The short version

- To merge two duplicate series, the app gets a list of every book in the series being
  merged away, moves each one to the surviving series, and then deletes the empty one.
- That list came from a fast in-memory index — a copy of the library kept in RAM so pages
  load quickly.
- The app **already knew** when rows had gone missing from that index. It recorded the
  loss and logged it. It just never checked before running a merge.
- So if the index was short, the merge moved the books it was shown, called itself a
  success, and deleted the series anyway. Any book missing from the index was left
  pointing at a series that no longer existed — invisible under that series, and with
  nothing in the logs saying so.
- The lookup now checks first, and when the index is short it reads from the real
  on-disk database instead. The merge finishes **correctly** rather than either stranding
  books or giving up.

## Why this one is worth reading

We have seen this exact damage before. On August 14 we found **13,322 books holding 6,893
series IDs that had been deleted out from under them** — every one of them showing no
series at all. That was fixed. This is the same failure arriving through a different door.

The uncomfortable part is that the safety check needed here already existed and was
already switched on — in two other places. Both of those places are *counters*: code that
tallies how many books reference a series in order to **report** a number. Neither of them
deletes anything.

The one piece of code that actually authorizes a deletion had no check at all.

**The code that observes was protected. The code that acts was not.** That is worth
stating plainly, because it is not an oversight anyone would spot by looking at the
safety check itself — it looked well-used and well-tested. You only see it by asking the
opposite question: not "is this guard good?" but "what does this guard *not* cover?"

## What we chose not to do

There were two reasonable fixes and they are not equally good.

The obvious one was to make the merge **refuse** when the index looks short. Safe, simple,
and it would have been enough.

We did something stronger instead: the real database is sitting right there and always has
the complete answer. Being short on the fast copy is a recoverable problem, so the app now
falls back to the slower, authoritative source and **completes the merge properly**.
Refusing would have aborted work that could have been done correctly — and a maintenance
job that fails every time it is degraded is a job people learn to ignore.

## What we deliberately left alone

Series *listing* pages still read from the fast index even when it is short.

That is a real trade and worth being explicit about. A missing row on a listing page is a
slightly incomplete page. A missing row during a merge is a deleted series. And a lost row
does not repair itself until the app restarts — so making listings fall back too would
mean every series page in the library ran a full database scan, permanently, in exchange
for a marginally more complete list.

One of the four tests exists purely to hold that line: it fails if someone later "tidies
up" by moving the check one level down, which would look harmless and would quietly make
every series page slow.

Books in the trash are a separate, still-open case, tracked on its own. This fix does not
change anything about them.

## Reach

The fix went into the shared lookup rather than into the one place the problem was
reported, so **seven** different merge and cleanup paths are covered instead of one —
series pruning, duplicate-series cleanup, the deduplication tools, and the series
renumbering job.

## The fix was wrong the first time, and that is the part worth reading

The first version of this fix shipped with a hole in it, found in review before it merged.

It checked whether the fast index was short, then read from the on-disk database instead.
Correct as far as it went. But *one of the three things that can make the index short* is
the database holding a book record that cannot be read at all — and the fall-back read
skipped unreadable records without saying so. So on that one trigger, the "safe" path
returned exactly the same short list, the merge deleted the series anyway, and the log
recorded that it had fallen back to safety.

The fix had a second, older gap in the same read: books are looked up over a range of
keys that, read literally, only covers IDs starting with a digit. Every ID the app
generates starts with a digit, so this had never bitten — but an ID supplied from
outside could start with a letter, and such a book was simply invisible to the merge.
A different part of the codebase found and fixed this same bug in the same key range
one day earlier; this read had not been updated.

**Why it was missed:** the fall-back read is an *existing* function that was not changed
by this fix — it was only given a new job. Nothing in the change showed it, because the
newly-inadequate code was not part of the change. That is the general lesson: **giving
old code a more important job silently transfers every safety assumption ever written
about its old job, and no diff will show you that.**

All checks passed on the flawed version, because no test data contained an unreadable
record. Green tests answered "did anything break," not "is this correct."

A second review pass found the same iterator-bounds defect one function over, in the
counter three delete sites consult before removing a series row at all. Fixed the same
way, with the same before/after proof. **Undercounting there is the sharper failure**: a
series absent from that counter's answer is not a wrong number on a page, it is the
signal "nothing points here, safe to delete."

## Confidence

Ten new tests across both fixes. Every one was confirmed to **fail against the code as
it stood** before its fix was written — the only way to know a test reaches the thing it
claims to check.

The fix was then re-broken eight different ways to confirm the tests notice. All eight
were caught, including one — a storage-layer read error part-way through a scan — first
written up as untestable without fault-injection machinery this codebase does not have.
That was wrong: the store's own test constructor already takes a pluggable filesystem,
and the open-source database driver ships an error-injecting one. Two dedicated tests
force the read to fail after the data is written and confirm both scans refuse rather
than answer short — closing the one gap this review had flagged as inspection-only.

The pre-existing test suite for this area would **not** have caught any of it. Those
tests only ever run against a healthy index, so they pass identically before and after —
which would have read as "still verified" while covering none of this.
