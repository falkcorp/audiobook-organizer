<!-- file: docs/executive-summaries/2026-08-14-the-preview-button-that-was-not-a-preview-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: b7e4c210-5f83-4a96-9d1e-3c6208a7b41f -->
<!-- last-edited: 2026-08-14 -->

# Executive Summary: The Preview That Was Not a Preview

**Date:** 2026-08-14
**In one line:** Most maintenance jobs advertise themselves as "preview only" by default,
and the server ignored that and made the changes for real.

---

## What you saw

Nothing. That is the whole problem.

A maintenance job that previews its changes and one that makes them for real look
identical from the outside. Same button, same spinner, same "started" confirmation, same
place in the activity list. The only way to tell them apart is to go and check whether
your library actually changed — and if you believed you had run a preview, you had no
reason to check.

## What was actually happening

Every maintenance job publishes its own default settings, and the settings screen reads
them. **18 of the 34 jobs say the same thing: by default, preview only, don't change
anything.** That is the sensible default for a job that deletes folders or merges records,
and it is what the screen showed you.

The part of the server that actually *starts* the job never read that. It had its own
idea of the default, and its idea was the opposite one: make the changes for real.

So the two halves of the same feature disagreed. One half told you "this will only
preview." The other half applied the changes. Whenever the preview setting wasn't spelled
out explicitly in the request — which is exactly what happens when something trusts the
published default — you got a real change while being told it was a preview.

This was not one job. It was every one of those 18, including:

- the job that deletes series with only one book in them,
- the job that deletes empty folders from disk,
- the job that merges books it believes are duplicates,
- the job that rewrites file paths.

## Why the series job is the sharp example

That job's first step deletes every series holding exactly one book. A census taken the
same day found that of 6,245 single-book series in the library, **2,322 are genuinely
distinct real series** — not duplicates, not mistakes, just series you happen to own one
book from.

And a deleted series name does not come back. It is not stored on the book, not in the
undo history, and not in the file path. Only about 6% could be reconstructed from
information elsewhere. An accidental non-preview run of that job would have quietly
deleted thousands of real series names with no way to recover them.

## The second, worse version of the same problem

There is a related case that is harder to spot and worse when it happens.

If the server restarts while a maintenance job is running, it picks the job back up on
the way up. But it kept **no record of whether that job was a preview or a real run.**
The information existed only in the original request, and the request was gone.

So it guessed — and it guessed "real."

That means an operator who deliberately chose *preview*, watched it start, and then hit
an ordinary restart (a deploy, a reboot, a crash) would get a **real change** on the way
back up, filed under the same operation they had started as a preview. Seven jobs can be
resumed this way and default to preview, and one of them deletes directories from disk.

Nothing in the interface would have distinguished that from a normal resume.

## What changed

Two things, one for each half:

1. **When the preview setting isn't spelled out, the server now uses the default the job
   itself publishes** — the same one the settings screen shows you. If a job says it
   previews by default, it previews. If you explicitly ask it to apply, it still applies:
   saying so out loud is unchanged.

2. **Your choice is now saved with the job**, so a restart resumes it exactly as you
   started it — a preview stays a preview, a real run stays a real run. For jobs that
   were already running when this shipped, and so have nothing saved, the server falls
   back to the job's published default rather than to "real."

The 16 jobs that legitimately default to applying are unaffected.

## How we know it holds

The risky part of a fix like this is that it quietly stops working later — someone adds
job #35, forgets a detail, and the two halves drift apart again exactly as before.

So the main test does not check the jobs one at a time against a list someone typed out.
It reads the *same published settings the screen reads*, for **every** job that exists,
and requires the server to reach the same answer. A new job that disagrees with what it
advertises fails the build. It also fails if a job publishes its setting under a slightly
different name, which is the specific mistake that would have recreated this bug while
looking completely correct.

Each of the four separate parts of the fix was then individually broken on purpose to
confirm the tests actually catch it. Each break was caught, by a different test.

## What is still open

- The saved settings accumulate a small record per job run that nothing cleans up yet.
  It is tiny, but it grows without limit; a cleanup pass is filed.
- A separate, unrelated duplicate-series routine has **no preview option at all** — every
  run applies. It has never been run in this library (0 times out of 10,161 recorded
  operations), so there is nothing to repair; it is filed to be fixed before anything
  connects it to a button.
