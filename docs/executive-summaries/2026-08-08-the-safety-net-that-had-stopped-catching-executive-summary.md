<!-- file: docs/executive-summaries/2026-08-08-the-safety-net-that-had-stopped-catching-executive-summary.md -->
<!-- version: 1.2.0 -->
<!-- guid: b1ececb6-257a-4ad0-9722-453194d546da -->
<!-- last-edited: 2026-08-09 -->

# The safety net that had stopped catching — restored

> **Correction, 2026-08-09.** The claim below that the suite "can be trusted as a
> gate again" was wrong. It rested on a run that had been checked through a
> command which hid most of its output, against a server left running for hours.
> A clean run the same evening reported **146 of 288 tests failing** — the 43
> described here were real and really fixed, but they were a fraction of what was
> broken. All 146 have since been repaired; see
> [the 2026-08-09 summary](2026-08-09-the-half-red-safety-net-executive-summary.md).


## What was wrong

Before any change to the website ships, an automated suite drives a real browser
through it: click Import Files, open a book, cancel a running job, check the text
that comes back. It exists to catch the kind of breakage a person only notices
after it is live — a button that no longer does anything, a page that renders an
error where a list should be.

In April, a one-line mistake in the test harness broke six of those test files at
startup. They did not report failures; they simply never ran. For roughly four
months the suite looked green while a large part of it was doing nothing at all.
During that window a major navigation library upgrade shipped with no browser-level
checks behind it whatsoever.

The harness mistake was fixed on August 7th. The suite then started failing
honestly: 43 tests, in six files, all of them broken. None of them were reporting
a real defect in the product — every single one was the test itself describing a
version of the app that no longer exists.

## What happened

All 43 are now passing again, in two passes. The first (August 7th) cleared the
Dashboard and Book Detail screens. This second pass cleared the remaining 34:
error handling, the server file browser, the import-a-file dialog, and operation
monitoring.

Two kinds of rot were found, and they are worth telling apart.

**Most of it — 24 of the 34 — was one shared bug in the fake server the tests run
against.** At some point the real backend started wrapping its answers in an outer
container, and the website was updated to unwrap it. The fake server used by the
tests was never updated, so it kept handing back the old shape. The website, doing
exactly what it does in production, looked inside the container, found nothing, and
rendered "not found" or an internal error. The tests were faithfully reporting that
the fake server was lying to them. The worst offender was the login-status check:
because it failed, the app never finished starting up in any test, which quietly
degraded every screen the suite touched. Fixing that one endpoint fixed tests in
three different files at once.

**The rest was genuine drift — the app moved and the tests did not.** The file
browser's plain "filter by extension" box became a full search box that also
filters folders. The metadata editor grew a padlock button beside every field.
Import paths moved onto their own tab in Settings. Each of these is a deliberate
improvement; the tests simply still described the old screen.

One file needed more than a refresh. The entire Operations page had been deleted
and replaced by a unified Activity page, with `/operations` now forwarding to it.
Ten tests were driving a screen that no longer exists. They were rewritten against
the real Activity page: its live operations list, its cancel button, its log
drawer, its filters and its paging. One test — retrying a failed job — was deleted
outright rather than left disabled, because the Activity page has no retry button
and pretending otherwise would put the suite right back where it started.

## What this means going forward

**No product code was changed.** That is the important result. Thirty-four red
tests, and not one of them was a real defect — the app has been behaving correctly
throughout. The failures were entirely in the description of it.

**~~The suite can be trusted as a gate again.~~ — RETRACTED the same day.**
This was written believing the whole suite passed. It does not. When the tests
were finally run automatically, on a clean machine, for the first time ever, the
score was **146 failed and 138 passed**. Roughly half the safety net is still on
the floor.

The claim above came from a run that looked clean and was not, for three separate
reasons at once: it quietly reused a copy of the application that was hours out of
date; it only actually ran 137 of 576 tests; and the way the command was written
threw away the pass/fail answer entirely, so "it passed" was never something that
had been measured.

The thirty-four tests this work repaired are genuinely repaired, and the paragraph
above them still stands. What is not true is that the rest of the suite is
healthy. Running the tests automatically is what revealed that — on its first day,
which is the strongest possible argument for having done it. The remaining
failures are now written down and being worked through rather than sitting
invisible.

**The lesson is about disabled tests.** The six files were not deleted; they were
silently skipped, and silence is indistinguishable from success. Where a feature
genuinely went away in this pass, its test was deleted rather than switched off —
deleted work is recoverable from history, whereas switched-off work rots in place
and is exactly how four months went by unnoticed.
