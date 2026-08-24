<!-- file: docs/superpowers/specs/2026-08-24-staged-library-scan-design.md -->
<!-- version: 4.0.0 -->
<!-- guid: c7f76dfd-5447-4c73-a9a6-b77f62db8736 -->
<!-- last-edited: 2026-08-24 -->

# Staged Library Scan — Design

**Status:** PROPOSAL. Not approved, not implemented.

## Problem

`library.scan` on prod runs for 5–6 hours and blocks the library the whole time.
The 2026-08-24 run started 04:15 and had not reached terminal status by 10:42.
The scheduled interval is 360 min, so the scan does not fit its own interval and
the library is effectively scanning continuously.

## What is actually wrong (measured 2026-08-24, not assumed)

Four measurements, because three plausible causes turned out to be wrong.

### 1. Resume works. The scan does NOT restart from the beginning.

Direct log evidence from the 07:24 restart:

```
library scan resuming from checkpoint  folder_idx=1 item_offset=6500
Resuming: skipping folder 1/11228 (already completed): /mnt/bigdata/books/abooks
Resuming folder /mnt/bigdata/books/newbooks at book 6500/17511
```

The op-level checkpoint (`ResumeFolderIdx` / `ResumeItemOffset`) is honoured. Any
redesign must keep this, not replace it.

### 2. The restarts were deploys, not crashes.

`systemd` events in the last 30h:

| time | event |
|---|---|
| 01:50:32 | `Main process exited, code=killed, status=9/KILL`, `Failed with result 'timeout'` |
| 02:12:57 | clean stop/start (deploy) |
| 07:24:21 | clean stop/start (deploy) |

Only 01:50 is a real fault: systemd `SIGKILL`ed the process after the shutdown
timeout expired. Memory peak 7.4–7.8 GB. There is no crash loop.

### 3. The skip cache works. Cost is real reads, not a broken cache.

A default scan walks **import paths only** — `internal/scanner/service.go:331`
excludes `RootDir`, confirmed on every prod run:

```
Scanning 2 total folders (2 import paths)
Library root /mnt/bigdata/books/audiobook-organizer excluded from this incremental scan
```

Mark coverage, sampled across 40 evenly-spread offsets (2,000 rows) with up to
120 detail fetches per tree, tracks exactly which trees are walked:

| tree | walked by a default scan | carries `last_scan_mtime` |
|---|---|---|
| `/mnt/bigdata/books/newbooks` | **yes** | 82.5% |
| `/mnt/bigdata/books/abooks` | **yes** | 68.0% |
| `/mnt/bigdata/books/audiobook-organizer` | no | 15.0% |
| `/mnt/bigdata/books/itunes` | no | 10.0% |

High where scanning happens, low where it does not. The cache is functioning and
correctly keyed. Unmarked books in the organized and iTunes trees are unmarked
because a default scan never touches them — expected behaviour, not a defect.

What remains is ordinary cost: **18-32% of walked books have no usable mark** on
any given run (new arrivals, changed files, and books whose mark was never
written), and each of those is a full tag read plus hashing against spinning
disks. Independently measured during the run: ~393% CPU of 4800% available,
`r_await` 9-13 ms, ~1080 IOPS aggregate, load average 31.7 at 8% CPU — classic
D-state I/O wait. The scan is I/O-bound, not CPU-bound and not cache-broken.

> **Two superseded claims, kept visible rather than deleted.**
>
> 1. *"~40% of books carry no `last_scan_mtime`, so ~23,000 are re-read every
>    tick."* Computed over the whole library including trees a default scan never
>    walks. Wrong.
> 2. *"The cache is keyed by the organize destination while the scan walks the
>    source path, so it can never hit."* Drawn from a sample of four contiguous
>    pages in which zero rows sat under an import path. A properly spread sample
>    puts that figure at **12.8%**, and the per-tree coverage above shows the
>    cache hitting normally on walked trees. Wrong.
>
> Both were stated confidently before being checked against a representative
> sample. The lesson worth keeping: a contiguous page range is not a sample.

The one genuine defect this uncovered is **instrumentation**. `shouldSkipFile`
returns silently — no counter, no log, no metric. Nothing in the logs
distinguishes "cache hit, skipped" from "cache missed, re-read everything," which
is why two wrong diagnoses survived this long. A skipped/processed counter in the
scan summary is a prerequisite for any further work here.

### 3b. Where the wall clock actually goes (measured, 07:24-11:20)

The mark is not merely present, it is *correct*: 203 marked books in the walked
trees were stat'd on prod and compared against their stored mark.

```
n=202
  WOULD SKIP (mtime AND size match): 202 (100.0%)
  mtime differs only: 0   size differs only: 0   both differ: 0   missing: 0
```

100% would skip. The cache is not the problem, and this closes the question.

Attribution over the 3.98 h since the 07:24 resume:

| wall clock | share | phase |
|---|---|---|
| 1.00 h | 25% | per-book processing (64,005 events) |
| 0.86 h | 22% | multi-file group consolidation (7,911 groups) |
| 0.78 h | 20% | folder walk / batching |
| 0.55 h | 14% | **AI parsing — 268 `context deadline exceeded` + 268 backoff retries** |
| 0.38 h | 10% | tag reads |

No stalls: 267,926 log lines, largest gap 15 s, zero gaps over 30 s. The process
is continuously busy, not blocked.

**Concurrency contention, previously misattributed.** `dedup.full-scan` ran
alongside `library.scan` for most of this window and produced *more* log volume
than the scan itself:

| op | log lines since 07:24 |
|---|---|
| `dedup.full-scan` | 119,145 |
| `library.scan` | 99,982 |

The `Scanning books: N / 56729` and `Composing scores` lines belong to **dedup**,
not to the scan. Both ops were restored together by the 2026-08-24 resume fix, so
two whole-library passes landed on the same spinning disks at once. They hold
separate `ConcurrencyKey`s, so nothing serializes them.

> **Caveat on the table.** Wall clock is attributed to the log line preceding
> each gap. With 48 workers logging concurrently that is suggestive, not proof.
> The *counts* (7,911 groups, 268 AI timeouts, 119,145 dedup lines, 202/202 mark
> matches) are solid; the hour splits are approximate.

### 3c. Is the diff predicate strong enough?

`entry.Mtime == mtime && entry.Size == size && !entry.NeedsRescan`.

- **Missing a real change:** possible only for an in-place edit that preserves
  byte size *and* restores mtime. Not a practical concern here.
- **Falsely invalidating:** any tool that touches mtime forces a full re-read of
  that file, and **nothing in the logs would reveal it**. This is the direction
  that matters, and it is unobservable today — see the instrumentation gap.

### 4. The denominator grows during the run.

`progress_total` over one run: 40,111 → 44,439 → 57,417 → 58,812 → 59,617 →
60,864. The consolidation/assemble phase creates new book rows, and each
`ProcessBooksParallel` call re-logs `scan started: N total files` (202 such calls
in 30h, `scanner.go:748`). Progress therefore appears to reset repeatedly even
though one op is running. This is a *reporting* defect, and it is most of why the
scan "looks like" it restarts.

## Design

Split `library.scan` into three stages. Stages are phases of one op, keeping the
existing checkpoint/resume machinery.

### Stage 1 — Enumerate (cheap, no file reads)

Walk the tree collecting `(path, mtime, size)` via `os.Stat` only. No tag reads,
no hashing, no ffprobe. Emit a complete file list before any processing begins.

This answers "shouldn't finding all files be an easy first query": today the walk
is interleaved with processing, so the total is not known until late — which is
exactly why the denominator moves. Stage 1 makes the denominator final and
correct at the moment stage 2 starts.

### Stage 2 — Diff (cheap, DB-only)

Compare the stage-1 list against the persisted `(last_scan_mtime, last_scan_size)`
marks. Produce three sets: **new**, **changed**, **unchanged**. Unchanged files
are dropped here and never touched again this run.

Stage 2 must write a mark for every file it declares unchanged, including ones
that had none before, so coverage converges instead of leaving a standing 18-32%
of walked books unmarked.

It must also emit a **skipped / processed counter**. Today `shouldSkipFile`
returns silently, so there is no way to tell from the logs whether the cache is
working — the single most valuable change in this document, and the reason two
wrong diagnoses survived. It is worth landing on its own, before any staging.

For the **new** and **changed** sets only, stage 2 then reads the tag header —
and nothing else. No hashing, no ffprobe, no AI fallback. This is what lets a
book enter the holding area with a real title, author and series instead of an
empty row, and it is bounded: the tag header is a single small read at the front
of the file, not a full-content pass.

### Stage 3 — Deep pass (expensive, bounded)

Only new + changed files. Tag read, hashing, mediainfo, AI fallback, chapter
persistence. Ordered **newest first** so recently added books appear soonest.

- Must honour `OverrideLocked` — never clobber user-applied metadata.
- Checkpoints per batch as today.
- Is the only stage that may be interrupted without losing stage-1/2 work.

### Holding area

New/changed books are promoted into the library immediately after stage 2 with a
flag on the existing row (`library_state` already exists and carries values such
as `suspicious`) marking them as awaiting the deep pass. **No new table.** The
book is visible and browsable straight away; the deep pass fills in the rest.

## What to fix first (independent of this design)

Ranked by measured cost against effort. **None of these require the staging work
below**, and the staging work does not deliver them:

1. **The 268 AI parsing timeouts** — ~14% of the run spent failing and retrying
   LLM calls. Largest single win, fully independent.
2. **Stop `dedup.full-scan` and `library.scan` running concurrently.** Two
   whole-library I/O passes on the same spindles, with no shared
   `ConcurrencyKey` to serialize them.
3. **A skipped/processed counter.** Cheapest of the three and the reason the
   first three diagnoses of this problem were all wrong.

The staged pipeline below stands on its own merits — a non-blocking scan, a
holding area, and honest progress reporting — but it is **not** the fix for the
5-6 hour runtime. Recording that explicitly so it is not sold as one.

## Non-goals

- Not changing `force_update` semantics.
- Not adding a weekly full sweep (tracked separately; see Open Questions).
- Not touching the op-level resume machinery, which works.

## Test strategy

- Stage 2 writes a mark for a previously unmarked, unchanged file (the ~40% fix)
  — mutation-tested by removing the write and asserting the test fails.
- Stage 2 classifies new / changed / unchanged correctly against a fixture with a
  known mark set. The fixture must contain a file with **no** prior mark; an
  all-marked fixture cannot observe the defect.
- Stage 3 leaves an `OverrideLocked` field untouched.
- Stage 1 performs no file opens (assert via a counting fake).
- Stage 2 reads the tag header for new/changed files but never hashes and
  never invokes ffprobe (assert via counting fakes on both).
- Resume across a stage boundary keeps stage-1/2 results.

## Rollback

Stages land behind a config flag defaulting to the current behaviour. Reverting
the flag restores today's single-pass scan; no schema change to undo, since the
marks are existing fields on existing rows.

## Open questions (must be answered before implementation)

1. ~~Confirm the silent-skip mechanism.~~ **CLOSED 2026-08-24 — there is no
   silent-skip defect.** Per-tree mark coverage (finding 3) shows the cache
   working normally on walked trees. A warn on the `dbBook == nil` branch is
   still worth adding, since that branch fails silently, but it is not the cause
   of anything observed. The real prerequisite is the skipped/processed counter.

2. Should the weekly full sweep (`include_root_dir`, keeping the skip cache) be
   folded in here or shipped separately? The scheduler cannot request it today —
   `internal/scheduler/tasks.go:28` is `type libraryScanParams struct{}`, an empty
   struct distinct from `internal/server/library_core_ops.go:31`, so every
   scheduled tick is plain incremental and nothing has ever scheduled a full sweep.
3. Should the incremental interval rise from 360 min while the scan still exceeds
   it, to stop near-continuous scanning in the interim?
