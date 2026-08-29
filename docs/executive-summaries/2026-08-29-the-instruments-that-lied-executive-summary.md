<!-- file: docs/executive-summaries/2026-08-29-the-instruments-that-lied-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: f6729b52-5e19-4e85-8ed8-2d5e1e3911e3 -->
<!-- last-edited: 2026-08-29 -->

# The instruments that lied

**2026-08-29 — a day spent on things that reported success while doing nothing:
safety limits that could not be satisfied, a setting that never reached the code
it configured, a saved index thrown away every restart, and a "lost work" alarm
about work that was never lost**

**21 pull requests merged.** Full list at the end.

---

## The short version

- The app is full of **controls** — a retention limit, a "keep this many" setting,
  a saved search index, a "force it to re-run" switch. Today's theme is that
  **several of them were not connected to anything.** They accepted your input,
  reported success, and changed nothing.
- None of these announced themselves. A control that silently does nothing looks
  exactly like a control that is working and has nothing to do. That is what made
  them survive so long.
- The single most consequential one: **your backup safety limit was impossible to
  satisfy**, which is what took the app down repeatedly and is the real reason new
  books were not appearing. That was fixed and deployed; uptime went from **13
  seconds to 15 minutes** and has held.
- We also **measured the database properly for the first time** and found the
  30 GB is mostly **history nobody asked for** — and that a redesign can cut the
  biggest slice by about **35x**.
- One alarm today was a **false alarm**, and chasing it correctly mattered: acting
  on it would have **undone a real fix**.

---

## The controls that were not connected

### 1. "Keep the last N snapshots" — the N never arrived

The app keeps a version history for each book so a bad edit can be undone. A new
maintenance job was added to trim that history, and it takes a setting: **how many
versions to keep**.

That setting never reached the job. It read from a storage location that **nothing
has written to since a system rewrite months ago**. Whatever number you typed, the
job used its built-in default of 10.

Worse, the test for it passed — because the test **supplied the value through a
back door that does not exist in the running app**. The test proved the job could
use a number handed to it directly. It never proved the number ever arrives.

This one is embarrassing in a useful way: **I shipped it that morning and found the
defect that afternoon.** Fixed in `#2966`, and the same dead path was found in
**four more jobs**, one of which — the job that undoes a bad metadata fetch —
**could not function at all**. That fix is `#2969`.

### 2. The "force it" switch that was ignored

A maintenance job that recalculates per-book totals has a **Force** option, for when
you want it to redo work it thinks is already done. The switch was read from your
request, and then **dropped before it reached the job**. Turning it on and leaving
it off produced identical behaviour. Now wired through, with tests.

### 3. The saved search index, thrown away every restart

The app builds an index so it can find books by meaning rather than exact spelling.
Building it is slow, so it is **saved to disk** and reloaded on restart.

It was reloaded, immediately judged **stale, and discarded — every single time.**
The staleness check compared the saved index's size against a *differently filtered*
count of books. Those two numbers **could never match** (17,706 versus 39,658), so
the answer was always "stale". The save file was written faithfully and never once
used. Fixed in `#2963` by recording, alongside the index, the number it was actually
built from.

### 4. The backup limit that could not be satisfied

Covered fully in [the backup that killed the database](2026-08-29-the-backup-that-killed-the-database-executive-summary.md).
In brief: the app kept the **last ten** database backups on the **same disk as the
database**. Backups grew from 247 MB to about **15 GB**. Ten of those is **150 GB on
a 141 GB disk** — the rule became arithmetically impossible, the disk filled, and the
app crash-looped every seventeen minutes for most of a night.

**The count-based limit never malfunctioned.** It did exactly what it was told,
while the files it was counting grew sixty-fold. A limit expressed as *"how many"*
cannot express *"don't fill the disk."*

Also fixed in the backup area today: backups now have a **configurable location**
(`#2958`), the app **refuses a backup that would fill the disk** (`#2953`), it
**prunes before reserving space and never deletes the last remaining copy**
(`#2957`), and listing backups **stopped re-checksumming every archive every time**
(`#2959`) — that was reading ~14 GB from disk just to draw a list.

---

## What we learned about the database

The database is **30 GB**, and until today nobody had asked *what of*. The answer:

- **7.65 GB is version history** — the "previous copies" of book records. The app
  saves a **complete copy of the whole record** on every edit, however small.
- **85.4% of every one of those copies is a single unchanged block** — an internal
  fingerprint that changes in only about **4%** of edits. It is copied in full
  every time regardless.
- Measured concretely: of **22.09 MB** rewritten across a sample, **0.64 MB had
  actually changed.**

Storing only what changed — the way large-scale databases do — is a **~35x
reduction on that slice**, roughly **7.65 GB → 0.22 GB**. Designed and written up;
**not yet built**.

A related finding worth stating plainly: **deleting data from this database frees
zero disk space** until it later rewrites its files in the background. A cleanup job
had been deleting old logs for months, and the space **never came back** — the
"optimize the database now" button that would have completed the job **existed in
the code with nothing calling it.** That is the next piece of work.

We also benchmarked backup compression properly rather than guessing. The
recommended setting compresses **as well as the slowest one at roughly a fifth of
the time** (2.69x in 8.4s, versus 2.66x in 39.7s). Awaiting your go-ahead.

**A blocker you should know about:** the database disk currently has **8.67 GB
free**, with **148 GB held by 861 filesystem snapshots**. Deleting files reclaims
nothing while those exist, and the cleanup that would reclaim space **needs free
space to run.** Destroying snapshots is your call, so this is parked.

---

## The false alarm, and why chasing it properly mattered

An automated helper reported that a performance improvement was "**one commit from
being lost**" on a local branch that had never been pushed.

Every check agreed. The commit was not in the main line by ancestry. No remote copy
existed. No pull request had ever been opened. A second, patch-level check also said
"not upstream."

**All of it was wrong. The work was already fully merged**, byte for byte.

Each check had failed for its own reason — one asked whether a *specific revision*
was a parent of the main line, which this project's merge style rewrites; another
compared *patches*, and the patch had changed because the commit also touched
auto-generated files that moved separately.

The point worth keeping: **the natural instinct in "rescue mode" is to prefer the
endangered copy.** Doing that here would have introduced a real bug — it would have
**deleted the size-accounting code that the backup disk-space guard depends on**,
quietly reverting one of the day's most important fixes. Main had moved for a
reason.

The only check that answered correctly was the direct one: **apply the change and
see whether anything is different.** Nothing was.

---

## Everything merged today

**The crash loop and the disk**
`#2953` refuse a backup that would fill the disk ·
`#2957` prune before the space check, never delete the last archive ·
`#2958` configurable backup directory ·
`#2959` stop re-checksumming every archive on every listing

**Controls that did nothing**
`#2966` the prune job's keep-count never reached the job ·
`#2963` stop discarding the saved search index on every restart ·
`#2960` the library-wide history prune job itself ·
`#2951` re-read queued settings after claiming a job

**Correctness and safety**
`#2954` let the server correct an over-eager apply ·
`#2950` hide saved credentials from the settings screen ·
`#2952` pick the right transcription hardware instead of assuming ·
`#2968` account for every skipped record when building the search index ·
`#2962` a read-only endpoint to size the orphaned-author problem before repairing it

**Build and release**
`#2961` pin the compiler version for every build target ·
`#2949` promote to a proper release instead of failing after 10 attempts

**Written down**
`#2967` the three controls that silently did nothing ·
`#2955` the backup that killed the database ·
`#2948` the AI endpoint pool design ·
`#2947` split-book merge status ·
`#2956` and `#2964` corrections to previously recorded claims

**Still open:** `#2969` — the four remaining jobs on the disconnected settings path.

---

## What needs a decision from you

1. **Filesystem snapshots** — 148 GB held by 861 of them, two snapshot services
   running at once. Nothing reclaims disk space until some are destroyed. **Your
   call, not mine.**
2. **Backup compression setting** — the benchmark says we can compress about as
   well in a fifth of the time.
3. **Two saved passwords still need re-entering** after the outage corrupted them.
4. **22 stale planning issues** (`#6`–`#27`) are machine-generated leftovers from a
   retired script. I would close them, but that is a judgement call about your
   backlog.

## What I am doing next, without needing you

- Build the "optimize the database now" action, so cleanup actually reclaims space.
- Build the changed-values-only version history (the ~35x reduction).
- Daily log compaction and rollup.
- Continue the burndown list — **15 of 71 items were already fixed** and have been
  closed; **52 remain**.
