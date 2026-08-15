<!-- file: docs/handoffs/2026-08-15-writeback-io-and-parallelism-session.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2d84f0a6-7e13-4b95-8c02-6f1a9d3e5b74 -->
<!-- last-edited: 2026-08-15 -->

# Handoff — 2026-08-15 (overnight write-back + parallelism session)

Owner went to bed at ~08:36 EDT with: "combine functions where we can", "have
another agent working on migrating the different areas to be multithreaded",
"get all of this done so I can start fetching metadata and know it'll write
correctly."

## READ THIS FIRST — the deploy is deliberately NOT done

**Nothing from this session is on prod.** Two PRs, both intentionally left for a
supervised deploy:

- **#2468** `perf/writeback-io-amplification` — write/tag correctness + I/O
- **#2469** `perf/server-metadata-parallelism` — the multithreading migration

The reason is specific, not caution theatre: **production compiles with
`-tags native_taglib` (CGO), and that code path has never been executed against a
real file.** The static libs in `third_party/taglib/lib/` are absent on darwin, so
`go test -tags native_taglib` fails to link locally, and CI builds the WASM
variant. The native writer is type-checked only. Since this PR changes *how tags
get written*, that gap should be closed on the box (or with a canary on a small
cohort) before a full-library write-back runs.

## What was actually wrong (the headline)

The owner's premise was that write-back was single-threaded and needed worker
pools. That was **half wrong**, and acting on it alone would have made things
worse — more workers would have multiplied the redundant I/O below against the
NAS.

### 1. Multi-file books rewrote every file forever, for a tag that never landed

Two independent defects that sustained each other:

- `FilterUnchangedTags` had no `currentVals` entry for `track`, so it hit the
  "unknown key — always write" branch. The code documented this as intended:
  `// Unknown field (e.g. "track") — always write.`
- **`buildWriteTagMap` had no case for `track` at all** — the taglib writers
  silently discarded it.

Emit track → filter forces a write → writer drops track → file never gets the tag
→ next read finds none → repeat. Forever. `len(tagMap) == 0` was unreachable for
every multi-file book.

**Fixing either half alone does nothing.** This is why the second half was nearly
missed — see "how it was caught" below.

### 2. Every tag write wrapped the file twice

`fileops.WriteTagsSafe` (hash, copy, writeFn, rename, hash) was called with a
`writeFn` that was *itself* another `WriteTagsSafe`. Per file per write: **4 full
SHA-256 passes + 2 full copies** of the audio, with the inner pair computed and
discarded. `ComputeFileHash` streams the whole file, no size cap.

### 3. Per-chapter titles were being destroyed (data loss)

Both multi-file paths overwrote `title` on every file with a synthetic
`"NN - Book Title"`. "Chapter 1: Departure" → "01 - Book Title", unrecoverable.
Defect 1 meant this ran on *every* write-back.

### 4. Cover art was all-or-nothing per book

`embedCoverInBookFiles` compared only `files[0]` and returned for the whole book
on a match, so files missing artwork stayed missing permanently. Directly against
the owner's stated requirement that every file of a multi-file book carries the
image.

### 5. Two near-identical write-back implementations, drifted

`writeBackMetadata` was a ~160-line duplicate of `WriteBackMetadataForBook` whose
only distinct input was three fallback values. The duplicate never embedded
covers from an already-downloaded local cover, never propagated to version-group
siblings, never redirected protected paths to the library copy, and never stamped
`LastWrittenAt` for multi-file books. Now one implementation (179 lines → 25),
exported signature unchanged so all nine callers and mocks are untouched.

## How defect 1's second half was caught — worth internalising

Every existing `FilterUnchangedTags` test pointed at a **nonexistent path**, so
`ExtractMetadata` failed, the function returned early, and the mapping never ran.
They passed regardless of what it did, and one asserted that `track` survives —
pinning the bug in place as expected behaviour.

After fixing the filter, unit tests on constructed `Metadata` structs went green.
**They were wrong.** A test written against a real ffmpeg-synthesized audio file
immediately failed with `Should be empty, but was map[track:1/12]` on a file that
had just been written — which is what exposed the writer-side half. A test on
constructed structs cannot catch a writer that never writes.

Both fixes were mutation-tested (disable the mapping → 3 fail with the literal
production symptom; revert the title logic → 4 fail).

## State

| | |
|---|---|
| Branch #2468 | `perf/writeback-io-amplification`, 7 commits, CI running at handoff |
| Branch #2469 | `perf/server-metadata-parallelism` (agent-authored), CI running |
| Worktrees | `.worktrees/writeback-io-amp`, `.worktrees/server-parallelism` — remove after merge |
| Local tests | metafetch, metadata, tagger, fileops, organizer, audiobooks: **6/6 pass** |
| Known-noise | `internal/database` and `internal/server` fail at **exactly 600s** — the documented stall signature; neither package is touched by #2468's diff (verified with `git diff --stat`) |

## Next actions, in order

1. **Merge #2468 and #2469** once CI is green (required set: `Minimal CI / Minimal
   CI Summary`, `Require changelog fragment`, `TODO Fragment Headers`).
2. **Close the native-taglib gap before a bulk run.** Either build the static libs
   locally, or deploy and run a *small* write-back cohort first and verify with
   `ffprobe` that tags landed and files are intact.
3. **Measure.** Nothing here has a stopwatch on it. The removed I/O is provable
   from the code; the wall-clock saving is not yet measured. Run write-back twice
   over a fixed unchanged cohort — the second run must write **0** files. Today
   it writes all of them for multi-file books, so this is a measurement that
   currently fails and must start passing.
4. Phase 4 of the plan is **not started**: metadata *fetch* fan-out. Do NOT raise
   fetch concurrency before building rate limiting — 5 of 6 providers (Audible,
   Audnexus, Google Books, OpenLibrary, Wikipedia) have **zero** outbound
   throttling, no jitter, no 429/`Retry-After` handling. Hardcover's 60/min
   limiter is a process-wide mutex that will serialise any fan-out. Plan:
   `~/.claude/plans/unified-weaving-grove.md`.

## Carried over, still unresolved from the previous session

- **Boot check #2 for `#2466` was never verified.** No evidence any organize ran
  after the 01:10:53 restart. (My "1,096 organize lines" figure was garbage —
  `grep -i organize` matches `audiobook-organiz**er**` in every journal line.)
- **The UA-purge census is dead**, killed by the registry stuck-op watchdog at
  5m18s. It emits no progress ticks, so it **cannot** complete no matter how long
  it runs. Re-triggering as-is will die identically at minute five. Needs progress
  reporting first — note `RunItems` auto-reports per item, which is the cheap fix.
- **ABS playlists endpoint ignores `page`/`limit`** — every page returns the
  identical full set, so the client appends duplicates. This is the "same playlist
  three times" report. Not fixed; unowned.
