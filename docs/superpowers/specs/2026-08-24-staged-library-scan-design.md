<!-- file: docs/superpowers/specs/2026-08-24-staged-library-scan-design.md -->
<!-- version: 1.0.0 -->
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

### 3. The cache mark covers only ~60% of the library. **This is the core defect.**

Sample of 200 books fetched individually from `/api/v1/audiobooks/:id`:

| condition | count | share |
|---|---|---|
| eligible for the skip cache (`file_path` AND `last_scan_mtime`) | 123 | 61.5% |
| of those cached, `needs_rescan=true` (present but never skips) | 4 | 3.3% |
| **actually skippable** | **119** | **59.5%** |
| **re-read on every scan** | **81** | **40.5%** |

Extrapolated to 56,724 books: **~23,000 books are fully re-read every tick.**
This is the "doing the same work every time" symptom. It is a *coverage* problem,
not a dirty-flag problem — `needs_rescan` accounts for only 4 of the 81.

It is also not caused by write errors: 236,143 prod log lines contain **zero**
`UpdateScanCache failed` warnings (verified against a known-good twin — 13,721
lines match `scanner`, so the grep is live).

The likely mechanism is at `internal/scanner/scanner.go:1166`:

```go
if dbBook, dbErr := store.GetBookByFilePath(books[idx].FilePath); dbErr == nil && dbBook != nil {
        if uerr := store.UpdateScanCache(dbBook.ID, ...); uerr != nil {
                warnSampled(...)   // only THIS branch warns
        }
}
```

When `GetBookByFilePath` returns `nil` or an error, the cache entry is silently
never written and there is no log line at all. The book is then re-read on every
future scan, forever, with nothing to indicate it. **Confirming this mechanism is
a prerequisite for the work below** — see Open Questions.

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

**Stage 2 must write a mark for every file it decides is unchanged**, including
ones that had no mark before. That single change is what closes the ~40% gap, and
it is worth landing on its own even if the rest of this design is rejected.

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
- Resume across a stage boundary keeps stage-1/2 results.

## Rollback

Stages land behind a config flag defaulting to the current behaviour. Reverting
the flag restores today's single-pass scan; no schema change to undo, since the
marks are existing fields on existing rows.

## Open questions (must be answered before implementation)

1. **Confirm the silent-skip mechanism** at `scanner.go:1166`. If books are
   missing marks for a different reason, stage 2's write may not close the gap.
   Cheapest probe: add a warn on the `dbBook == nil` branch and read one scan.
2. Should the weekly full sweep (`include_root_dir`, keeping the skip cache) be
   folded in here or shipped separately? The scheduler cannot request it today —
   `internal/scheduler/tasks.go:28` is `type libraryScanParams struct{}`, an empty
   struct distinct from `internal/server/library_core_ops.go:31`, so every
   scheduled tick is plain incremental and nothing has ever scheduled a full sweep.
3. Should the incremental interval rise from 360 min while the scan still exceeds
   it, to stop near-continuous scanning in the interim?
