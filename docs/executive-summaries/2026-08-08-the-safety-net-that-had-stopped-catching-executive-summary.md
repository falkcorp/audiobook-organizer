<!-- file: docs/executive-summaries/2026-08-08-the-safety-net-that-had-stopped-catching-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: b1ececb6-257a-4ad0-9722-453194d546da -->
<!-- last-edited: 2026-08-08 -->

# The safety net that had stopped catching — restored

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

**The suite can be trusted as a gate again.** It is now safe to require these
checks to pass before anything ships, which is what should have been true in April
and would have caught the navigation upgrade going out unverified.

**The lesson is about disabled tests.** The six files were not deleted; they were
silently skipped, and silence is indistinguishable from success. Where a feature
genuinely went away in this pass, its test was deleted rather than switched off —
deleted work is recoverable from history, whereas switched-off work rots in place
and is exactly how four months went by unnoticed.
