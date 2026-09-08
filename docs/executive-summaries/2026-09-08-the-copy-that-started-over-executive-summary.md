<!-- file: docs/executive-summaries/2026-09-08-the-copy-that-started-over-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8a3983cf-04c3-4969-8383-64e4f57acd8d -->
<!-- last-edited: 2026-09-08 -->

# The copy that started over every time

**Pull requests:** [#3141](https://github.com/falkcorp/audiobook-organizer/pull/3141) — merged,
plus the follow-up in this change.

## Executive Summary

- The app is midway through moving its activity history — the running log of
  everything it has done — into a new, much more efficient store. That move is a
  one-time copy of about **6.5 million records**, and it takes hours.
- **It had no memory of its own progress.** Every restart sent it back to the first
  record. On 8 September it restarted three times, throwing away roughly **81
  minutes** of work, and then spent about **three more hours** re-reading records it
  had already copied. Nothing was corrupted by this — the copy refuses to write
  anything twice — but the time was simply spent again.
- **The more serious problem was the one nobody would have seen.** The copy checks
  its own work: after copying a section it goes back over it and confirms nothing
  new lands. Only when every section passes does the app switch over to reading from
  the new store. That pass/fail verdict was held **only in memory**.
- That made it safe by accident rather than by design. If the copy found a problem
  and was then interrupted, the next run started over from record one and would find
  the problem again. Adding a memory of progress — the obvious fix to the wasted
  hours — would have **removed that accident**: a run that failed its check, died,
  and resumed would have looked at a clean remainder, concluded all was well, and
  **switched the app over to a copy that was never fully checked**. The fix records
  the verdict alongside the progress, so a section that failed must be re-copied in
  full before it can pass.
- **A review then found a case the first fix would still have gotten wrong**, and
  that case is the second half of this work. See section 2.
- Nothing here changes what the app shows you. It changes how long the move takes
  and, more importantly, whether "verified" actually means verified.

## 1. Why restarting was so expensive

Copying a record is cheap. *Finding* it is not: the copy reads several million
stored entries, unpacks each one, and offers it to the new store, which quietly
ignores anything it already has. So a restart did not duplicate any data — it just
paid the reading cost all over again.

The app now writes down where it has got to, continuously, and skips whole sections
it has already finished and verified. The position is only ever recorded *after* the
records it covers are safely stored, so the bookmark can lag the work but never run
ahead of it.

## 2. The case the first fix would still have gotten wrong

A bookmark only works if nothing new ever appears *behind* it.

For almost everything, that holds — while the move is under way, every new log
entry is written to both the old and the new store at once, so a skipped entry is
already safely on the other side.

There is one exception, and it is deliberate. Housekeeping jobs that tidy old logs —
in particular the one that condenses a day's entries into a single daily summary —
write **only** to the old store. That is the entire point of the move: after the
switch, that job runs against the new store instead. And the daily summary it writes
is **dated to the day it summarises**, not to the moment it was written. So a
housekeeping run that happens while the copy is paused can leave a new record
*behind* the bookmark, dated weeks earlier.

The copy would then step over it. And because the verification pass only re-checks
records it has just read, it would never notice — the section would be reported as
fully verified while quietly missing a record, and that missing record might itself
be a *correction* to one the new store already held an outdated version of.

The fix is narrow: the one category of record that can be back-dated this way is now
always re-read in full, never resumed. It amounts to about one record per day of
history, so the cost is negligible, while the several-million-record category that
made restarts painful in the first place still resumes normally.

## 3. What this was checked against

Each safeguard was verified by breaking it on purpose and confirming the test
notices:

- A run that fails its check, is interrupted, and resumes onto a clean remainder
  must still refuse to switch over. It does.
- A back-dated daily summary written behind the bookmark must still be copied. It is.
- Refusing to resume that one category must not quietly turn into refusing to resume
  *anything* — which would make the first fix pointless while leaving every test
  green. A separate check confirms the large categories still resume.
- A section re-read from the start must discard the counts belonging to the bookmark
  it replaced, so repeated restarts cannot inflate the totals.

## 4. What is not done

The move still has **no visible status**. There is no way to see that it is running,
how far along it is, or why the app has not switched over yet, short of reading raw
log text. That was asked for and is not in this change; it is written down as the
next piece of work, together with a shutdown-timing problem that has to be solved
first if the status is to be shown the same way every other long-running job is.
