<!-- file: docs/executive-summaries/2026-08-24-the-work-that-never-came-back-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5b93ce41-8d27-4f06-a1c9-6e04b2d7f358 -->
<!-- last-edited: 2026-08-24 -->

# The work that never came back

**2026-08-24 — every long job stopped by a restart was quietly abandoned, and the
startup log said "nothing to resume"**

**PR:** [#2851](https://github.com/falkcorp/audiobook-organizer/pull/2851) —
`fix/v2-resume-quiesced-ops`.

---

## The short version

- Long jobs — a library scan, a duplicate scan, a transcription run — can take hours.
  When the app restarts (a deploy, a reboot), they are stopped part-way and marked
  "interrupted", with a note saying whether they should pick up again next time.
- On the next start, the app is supposed to read that note and resume them.
- **It never could.** It was looking in the wrong place: a short list of jobs that are
  *currently* in progress. Marking a job "interrupted" is precisely what takes it *off*
  that list. By the time anything looked, the job was gone from the only list being read.
- So the note about resuming was written, stored, and never once acted on. Every
  interrupted job simply stopped existing as far as the app was concerned.
- The startup log said **"no active ops to resume"** — which reads like *nothing was
  interrupted*, not *I am unable to see interrupted things*. Nothing looked wrong.

## How it showed up

A library scan was started overnight on 24 August. It got about 25,880 files in, a deploy
restarted the app, and it never came back. It sat untouched for roughly four hours while
the startup log reported nothing to do.

That was the second time. **The identical thing happened on 17 August** and was fixed —
but the app has two separate systems for this, an older one and a newer one, and the fix
was applied to the older one. Everything important now runs on the newer one. The repair
was real, it just wasn't where the damage was.

## What was actually broken

Nothing about the resume *instructions* was wrong. Each job correctly declared whether it
should restart. The declaration was simply never read, because the list it would have
been read from could not contain the job.

This is worth stating plainly: **a setting that is never consulted looks exactly like a
setting that is working.** Every job in the system carried a correct, carefully-chosen
resume policy, and for months not one of them had any effect after a graceful restart.

## The fix, and the trap inside it

The app now looks for interrupted jobs directly instead of relying on the "currently
running" list.

The obvious version of that fix would have been dangerous. Interrupted jobs pile up — one
for every restart, going back months. On the machine in question there were **27** of
them waiting, **21 of which were library scans**. Simply resuming everything found would
have launched 21 full library scans at the same moment on one restart: far worse than the
problem being fixed, and the usual protection against duplicate jobs does not apply on the
resume path.

So the app now keeps only the most recent interrupted run of each kind of job and retires
the rest. A job that is genuinely still queued or running always wins over an old
interrupted one. Jobs that were *deliberately* stopped — cancelled by a person, or
already retired on an earlier restart, or waiting on someone to decide — are left alone.
Resuming those would override a decision somebody already made.

## Confirmed working

On the first restart after the fix, on the real library:

| | before | after |
|---|---|---|
| startup log | "no active ops to resume" | "processing resumable ops, count 5" |
| old interrupted jobs retired | — | **22** |
| the stranded library scan | abandoned 4+ hours | **running again** |

The scan resumed and ran past the point it had died at. Four other genuinely unfinished
jobs — a duplicate scan, an audio-clip extraction, a transcription run and a file-row
cleanup — came back at the same time. All four had been silently discarded on every
restart before this.

## What to expect now

**Restarts will now start work that used to vanish.** That is the fix behaving correctly,
but it is a visible change: a deploy may kick off a multi-hour job that previously just
disappeared. Only one job of each kind, and only the newest — but the machine will be
busier after a restart than it used to be.

## Two measurements that were wrong along the way

Both are recorded because the *method* was the problem, not the arithmetic.

- **The number of stuck jobs was estimated at 2 and was actually 27.** The estimate came
  from a screen that only shows recent activity, filtered by when a job was first
  requested. Jobs older than the window were invisible to the count — and old jobs were
  exactly the population being counted. The safeguard above is what kept a wrong estimate
  from becoming an incident.
- **The same screen reported "no scan running" while the scan was running.** A resumed
  job keeps its *original* request time, so a "last 2 hours" view filters out a job that
  restarted 30 seconds ago but was first requested seven hours earlier.

The general lesson, now recorded in both cases: **counting a population with a tool that
quietly limits what it shows produces a confident number and a wrong one.**
