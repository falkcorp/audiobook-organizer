<!-- file: docs/executive-summaries/2026-07-13-dedup-precision-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5c9e2b17-0d84-4a63-9f21-8e46c1a7d3b0 -->
<!-- last-edited: 2026-07-13 -->

# Executive Summary: Duplicate-Detection Confidence Reaches Its Target

**Shipped:** PRs [#1926](https://github.com/falkcorp/audiobook-organizer/pull/1926)
(the fix), [#1927](https://github.com/falkcorp/audiobook-organizer/pull/1927)
(keeps it from coming back), building on the INIT-1 label-quality work
(#1897–#1925). Verified live on the production library on 2026-07-13.

## Executive Summary

The app finds duplicate books in your library and rates how sure it is that
two books are really the same. That confidence rating had been stuck: it could
only *confidently* flag about **one in three** real duplicates, so most had to
be checked by hand. We tracked down why, fixed it, and confirmed the fix on the
real library.

The confidence rating now correctly flags **about 7 in 10** real duplicates
while being right **96.7%** of the time — more than double the reach it had
before, at *higher* accuracy. The very top "sure enough to merge automatically"
tier is **98.3%** accurate.

In plain terms: far fewer duplicates will slip through needing manual review,
and the ones the app surfaces as high-confidence are more trustworthy than they
have ever been.

## What was wrong, in plain terms

The app rates a pair of books using several independent clues at once — how
similar their text is, whether their audio fingerprints match, whether their
durations line up, shared ISBNs, and so on. Combining all those clues is what
gives the best answer.

To *tune* that combined rating, the app compares its scores against a set of
human-reviewed examples — pairs a person confirmed as "duplicate" or "not a
duplicate." But the tuning kept failing with "not enough data." The reason was
subtle and, once found, obvious:

- When a person marks a pair as **"not a duplicate,"** the app stops tracking
  that pair as a candidate — sensibly, to avoid wasting effort on it.
- A side effect was that these confirmed *non*-duplicates never got the detailed
  score breakdown saved that the tuning step needs to learn from.
- So the tuning step was trying to learn where to draw the line between
  "duplicate" and "not," but had almost **none** of the "not" examples to learn
  from. It couldn't do its job.

The confirmed non-duplicates are exactly the examples the tuning needs most —
and they were the ones being thrown away.

## What we changed

1. **We built a one-time repair pass** that goes back over every human-reviewed
   pair and re-computes the detailed score breakdown, saving it where the tuning
   step looks. Crucially, it keeps the low-scoring "not a duplicate" pairs that
   the normal process discards — those are the missing puzzle pieces. It ran on
   the production library and repaired 1,428 examples, leaving every human
   decision untouched.

2. **We closed the leak going forward** so the gap can't slowly return. Now, the
   moment someone marks a pair "not a duplicate" or changes a label, the app
   saves that pair's score breakdown right then. No more re-running the repair
   pass forever.

3. **We re-tuned the confidence rating** against the now-complete data and
   applied the improved settings to production. The headline change: the
   "high-confidence" line was drawn too conservatively; nudging it lets the app
   confidently surface roughly twice as many real duplicates while its accuracy
   actually went *up*.

## Why it matters

Getting the confidence rating right is what decides how much duplicate cleanup
you have to do by hand versus how much the app can shortlist for you. Before,
two-thirds of real duplicates fell into the "not sure, please check manually"
bucket. Now most of them are surfaced with high confidence you can trust, and
the small "merge automatically" tier is right 98 times out of 100 — so the
library gets cleaner with less manual effort and no drop in safety.

Every human "duplicate / not a duplicate" decision was preserved throughout,
the repair only writes derived score data (it can never trigger a wrong merge
on its own), and the previous settings were recorded so the change can be rolled
back at any time.
