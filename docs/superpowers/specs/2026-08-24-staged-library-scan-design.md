<!-- file: docs/superpowers/specs/2026-08-24-staged-library-scan-design.md -->
<!-- version: 2.0.0 -->
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

### 3. The skip cache cannot hit on the population that is scanned. **Core defect.**

A default scan walks **import paths only**. `internal/scanner/service.go:331`
excludes `RootDir` unless `force_update` or `include_root_dir` is set, and prod
confirms it on every run:

```
Scanning 2 total folders (2 import paths)
Library root /mnt/bigdata/books/audiobook-organizer excluded from this incremental scan
```

The two walked paths are `/mnt/bigdata/books/abooks` and
`/mnt/bigdata/books/newbooks` (the latter alone holds 17,511 books).

But of 1,200 sampled book rows, **zero** have a `file_path` under either import
path. Every row points at the organize destination (948) or the hands-off iTunes
tree (251). Organize rewrites `book.FilePath` to the destination; the source file
remains in the import path.

The consequence is structural, at `internal/scanner/scanner.go:866` and `:1166`:

1. `shouldSkipFile(<source path>)` looks up `cache[<source path>]`. The cache is
   built by `GetScanCacheMap`, keyed by `book.FilePath` — the *destination*
   path. **Miss, every time. Nothing is ever skipped.**
2. The write-back then does `GetBookByFilePath(<source path>)`, which returns
   `nil` for the same reason, so `UpdateScanCache` is never called and no mark is
   ever created. The `nil` branch logs **nothing**:

```go
if dbBook, dbErr := store.GetBookByFilePath(books[idx].FilePath); dbErr == nil && dbBook != nil {
        if uerr := store.UpdateScanCache(dbBook.ID, ...); uerr != nil {
                warnSampled(...)   // only THIS branch warns
        }
}
```

It is self-perpetuating: the lookup that would record the skip fails for the same
reason the skip failed. Every file in the import paths is fully re-read on every
scan, forever. **This is what makes the "incremental" scan take 5-6 hours.**

Two supporting negatives, so this is not confused with adjacent causes:

- Not write errors: 236,143 prod log lines contain **zero** `UpdateScanCache
  failed` warnings (verified against a known-good twin — 13,721 lines match
  `scanner`, so the grep is live).
- Not the dirty flag: of 123 sampled books that *do* carry a mark, only 4 (3.3%)
  have `needs_rescan=true`.

> **Superseded claim.** An earlier draft of this spec said "~40% of books carry no
> `last_scan_mtime`, so ~23,000 are re-read every tick." That number was computed
> over the whole library and is wrong: the unmarked books cluster in the organized
> tree and the iTunes tree, neither of which a default scan walks, so they are not
> re-read at all. The correct finding is narrower in population and worse in
> effect — on the population that *is* walked, the hit rate is structurally zero.

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

**Stage 2 must key the mark by the path that was actually walked.** Today the
cache is keyed by `book.FilePath` (the organize *destination*) while the scan
walks the *source* path, so the lookup can never hit. Stage 2 must either record
the source path on the book row and key the cache by it, or resolve source ->
book via the existing path index before comparing. Whichever is chosen, the
invariant to test is: **the key written by the deep pass is the same key the diff
looks up.**

This is worth landing on its own even if the rest of this design is rejected — it
is the whole of the 5-6 hour problem, and it does not depend on staging.

Stage 2 must also write a mark for every file it declares unchanged, including
ones that had none before, so the set converges instead of rebuilding each run.

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

1. ~~Confirm the silent-skip mechanism.~~ **RESOLVED 2026-08-24** — see finding
   3. The cache is keyed by the organize destination while the scan walks the
   source path, so both the skip lookup and the mark write-back miss. A warn on
   the `dbBook == nil` branch is still worth adding regardless, because that
   branch currently fails silently and would hide any future recurrence.
   **Decide:** record the source path on the book row, or resolve source -> book
   through the path index? The first is a schema addition on an existing row; the
   second adds a lookup to a hot loop.
2. Should the weekly full sweep (`include_root_dir`, keeping the skip cache) be
   folded in here or shipped separately? The scheduler cannot request it today —
   `internal/scheduler/tasks.go:28` is `type libraryScanParams struct{}`, an empty
   struct distinct from `internal/server/library_core_ops.go:31`, so every
   scheduled tick is plain incremental and nothing has ever scheduled a full sweep.
3. Should the incremental interval rise from 360 min while the scan still exceeds
   it, to stop near-continuous scanning in the interim?
