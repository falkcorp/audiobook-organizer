<!-- file: docs/design/2026-08-23-staged-library-scan-design.md -->
<!-- version: 1.3.0 -->
<!-- guid: 4c1e8b73-2a9f-4d06-b5e1-7f3a90c2d846 -->
<!-- last-edited: 2026-08-23 -->

# Staged Library Scan — Design

**Status:** DRAFT, awaiting review. Nothing here is implemented.
**Author:** drafted 2026-08-23 from a brainstorming session; three decisions were
made by the owner and are marked **[OWNER]**. Six were taken as defaults while the
owner was unavailable and are marked **[DEFAULT]** — each is a real decision point
and any of them can be reversed without redesigning the rest.

## The problem

A full `library.scan` takes about five hours. That figure is measured, not
estimated: the comment on `RegisterLibraryScanOp`
(`internal/server/library_core_ops.go:74-85`) records ~208 books/min against a
63,044-book production library, and notes that the previous 4h timeout was
"not a safety margin, it was a guillotine" — every full scan was structurally
guaranteed to be killed before finishing, at around 41%.

Two things have already been fixed and are **not** what this design addresses:

- **Resume.** `library.scan` is `ResumeRestart` and genuinely continues mid-scan:
  it calls `Checkpoint()` after every chunk and every completed folder, and
  `libraryScanParams` carries `resume_folder_idx` / `resume_item_offset`. Fixed
  2026-08-17.
- **The timeout.** Raised 4h → 24h on 2026-08-16.

What remains is the five hours itself. A user who drops a new book into the
library waits behind a full-library pass to see it.

### Where the five hours actually goes

Measured by reading the per-file path (`internal/scanner/process_file.go`):

| Work | Cost | Needed to make a book *usable*? |
|---|---|---|
| `os.Stat` — path, size, mtime | microseconds, no file read | yes |
| Embedded tag read | kilobytes off the file header | yes — this is title/author/series |
| **Full SHA-256 of file contents** (`computeHashFromReader`, `:117`) | **reads every byte** | **no** |
| **`ffprobe` subprocess per file** for chapters (`probeSingleFileChapters`, `synthesizeMultiFileChapters`) | **one process spawn per file** | **no** |

The library lives on a NAS (`/mnt/bigdata/books`). Hashing 63,000 books means
pulling every byte of the entire library across the network on every full scan.
**The scan is I/O-bound on content it does not need in order to show you the book.**

That is the whole basis of this design: the two expensive steps are exactly the two
that are not required for a book to appear, be browsed, and be played.

## Goal

A scan run returns in minutes having found and registered everything new. The
expensive per-file work drains afterwards in the background. Total CPU spent is
unchanged or slightly higher; **latency to a usable book drops from hours to
minutes**. **[OWNER]**

## Design

### Four stages

1. **Discover** — walk the tree, `os.Stat` only. Compare `(path, size, mtime)`
   against known rows. Emit the set of new/changed paths. No file contents read.
2. **Shallow ingest** — for each new path, read the embedded tag header and create
   the `book` / `book_file` rows with title, author, series, track/disc numbers.
   Mark them provisional. No hash, no `ffprobe`.
3. **Promote** — the rows are in the normal tables from the moment stage 2 writes
   them; "promotion" is not a copy step, it is simply that stage 2 writes to the
   real tables with a provisional marker set. See *Data model* for why there is no
   separate holding table.
4. **Deepen** — a background op walks provisional rows and does the expensive work:
   full hash, `ffprobe` chapters, fingerprinting. Clears the marker per file.

Stages 1–3 are the fast scan. Stage 4 is a separate, independently resumable op.

### Data model **[DEFAULT]**

Add one field to `BookFile`:

```go
// NeedsDeepScan marks a row created by the shallow ingest pass: its tags have
// been read but its content has not. FileHash, chapters and fingerprints are
// absent until the deep pass clears this.
NeedsDeepScan bool `json:"needs_deep_scan,omitempty"`
```

**No new table, and no migration.** `BookFile.FileHash` is already
`string` + `omitempty`, so a hash-less row is representable today. Because the
owner chose "fully visible and playable" (below), the rows must live in the normal
tables anyway — a separate staging table would buy nothing and cost a second read
path plus a copy step.

**Do not overload "empty hash" as the marker.** An empty `FileHash` is ambiguous:
it could mean *not attempted yet* or *attempted and failed*. An explicit boolean
distinguishes "still queued for deep scan" from "deep-scanned, but hashing failed"
— and the second needs to be visible, not silently retried forever. This repo has
been bitten by exactly this shape before (existence ≠ completeness).

### What a provisional book can do **[OWNER]**

**Fully visible and playable, flagged as provisional.** It appears in the library
immediately, browsable and playable, with a "pending full scan" marker in the UI.

Consequences, accepted deliberately:

- **Dedup and version-grouping skip un-hashed rows.** They key off content hash;
  there is nothing to compare yet. They must filter on `NeedsDeepScan == false`
  rather than treat an empty hash as a distinct value that could collide.
- **Transient duplicates are visible.** If the same book is added twice before the
  deep pass runs, both appear until hashes arrive. This is the price of the
  latency win.
- **A user may act on a book whose metadata later changes.** Addressed by the write
  policy below.

### Write policy — honour the lock that already exists **[OWNER]**

The deep pass must never clobber metadata a user has curated.

**The mechanism already exists and is already populated.** A manual edit sets
`entry.OverrideValue` **and** `entry.OverrideLocked = true` in the same block
(`internal/audiobooks/service_mutation.go:327-334`), and records a `user_edit`
history entry. There is an explicit `UnlockOverrides` path to undo it.

**Almost nothing respects it.** Grepping every reader of `OverrideLocked`, only two
writers consult it before overwriting:

- `internal/plugins/maintenance/repair_junk_titles.go:141`
- `internal/plugins/maintenance/title_repair.go:117`

**The scanner does not.** That is the direct cause of the standing hazard "a running
scan CLOBBERS applied metadata" — the guard rail was built and populated, and the
scan write path simply never consults it.

So the deep pass does not need new provenance logic. It needs to check
`OverrideLocked` before writing any field. This **fixes the existing clobber bug as
a side effect**, rather than scheduling around it — which is what makes a
continuously-draining background pass safe at all. Without this, the whole design is
unsafe, because stage 4 is by definition a scan running while users are working.

**This is the load-bearing piece of the design.** If only one thing here is
implemented, it should be this, and it is worth shipping on its own ahead of
everything else.

### Deep-pass scope and ordering **[DEFAULT]**

- **New/changed files only.** The existing ~63,000 books already have hashes and
  chapters; sweeping them all would recreate the multi-hour job being removed. A
  separate opt-in op can re-deepen old rows if ever wanted.
- **Newest first**, so the books just added finish first — that is where the user's
  attention is.
- **Trigger:** backgrounded by default, periodic, with an on-demand trigger.
  The owner's framing: *"have a job periodically pick that up, or when a user adds
  new media, but let the user decide — by default it's just backgrounded."*

### Checkpointing and resume

Stage 4 is a long-running op over an unbounded work-set, so it must checkpoint —
this design exists partly *because* long jobs that restart from zero are painful.

- Declare `MinCheckpointInterval` and call `Checkpoint()` per chunk, following
  `library.scan`'s existing pattern. Resume granularity is one chunk, not one file.
- With a real cadence declared, `ResumeRestart` is correct **and** the watchdog's
  `uncheckpointed` strike becomes a meaningful check on this op rather than noise.
- Because the marker is persisted per row, a resumed deep pass is naturally
  idempotent: it re-derives its work-set from `NeedsDeepScan == true` rather than
  from a remembered offset. **Prefer that over the checkpoint offset** — it is
  self-healing and cannot skip a row because a checkpoint was stale.

### Concurrency

Per `CLAUDE.md`, the deep pass iterates a library-scale collection doing per-item
I/O and subprocess work, so it must be concurrent from the start: a bounded
`errgroup` sized to `runtime.NumCPU()` for hashing, with a **smaller separate limit
for `ffprobe`** subprocess spawns. Model on `internal/plugins/acoustid/backfill.go`'s
`registry.RunItems` pattern rather than writing a new sequential loop.

## What this does *not* do

- It does not make the deep work faster; it moves it off the critical path. Raw
  throughput is a separate lane.
- It does not backfill existing books.
- It does not change how a scan is triggered or watched.

## Testing

- **Discover:** a tree with known new/changed/unchanged files yields exactly the
  expected path set; unchanged files are not restated.
- **Shallow ingest:** a book appears with correct title/author **and**
  `NeedsDeepScan == true`, `FileHash == ""`, after a run that never opens the file
  beyond its tag header. Assert the absence of hashing by timing or by a counter —
  not by inspection.
- **The clobber regression, mutation-tested:** set `OverrideLocked` on a field, run
  the deep pass, assert the field is unchanged. Then mutate the guard away and
  confirm the test fails. This is the test that matters most.
- **Dedup exclusion:** a provisional row must not appear in a dedup candidate set,
  and must appear once the marker clears.
- **Resume:** interrupt a deep pass mid-run, restart, assert no row is processed
  twice and none is skipped.
- **End-to-end latency:** the assertion that justifies the design — a new file is
  visible in the library after the fast pass alone, with no deep pass run at all.

## Risks

1. **Transient duplicates** are user-visible until the deep pass catches up.
   Accepted **[OWNER]**.
2. **Every dedup-sensitive read path** needs the `NeedsDeepScan` filter. Missing one
   means comparing books by an absent hash. This is the widest-blast-radius part of
   the change and needs a deliberate enumeration of call sites, not a grep.
3. **A permanently-stuck provisional row** (deep pass fails repeatedly on one file)
   would sit provisional forever with nothing surfacing it. Needs an attempt counter
   and a way to see the failures — silence here is the failure mode this repo keeps
   rediscovering.
4. **Ordering vs. the metadata-apply hazard.** The `OverrideLocked` fix should land
   **before** the background deep pass is enabled, not with it. Enabling a
   continuous background scanner while the clobber bug is live would make the
   existing hazard dramatically more likely to fire.

## Resolved in review — 2026-08-23 **[OWNER]**

### Duration comes from the tag header, not ffprobe

The fast pass already opens and parses each file's tag header. Duration usually
lives in that same structure — the `moov` atom for M4B/M4A, the Xing/Info frame
for VBR MP3 — so reading it costs no extra subprocess and no extra file read.

- Where the header carries a duration, the provisional row shows the real one.
- Where it does not (a minority: raw CBR MP3 without a Xing frame, some odd
  encoders), duration stays zero and the deep pass fills it in.
- `ffprobe` stays **out** of the fast pass. It is the second-largest cost in the
  current scan and putting it back on the critical path defeats the redesign.

This adds a dependency on header parsing being correct for duration, which the
deep pass later corroborates — a disagreement between header duration and
`ffprobe` duration is a signal worth logging, not silently overwriting.

### Provisional books are excluded from bulk writes, not just dedup

Browsing and playing an un-hashed book is harmless. **Merging** one is not: a
merge decided without a hash rests on title/author similarity alone, and this
repo has already measured that dedup collisions are frequently genuine duplicate
books rather than false pairs. A bulk operation that silently joins two different
books is exactly the class of data loss the missing-file lane exists to prevent.

So `NeedsDeepScan == true` excludes a row from:

- dedup candidate sets (as drafted), **and**
- bulk apply / bulk merge / organize.

It does **not** restrict browsing, playing, or a single deliberate manual edit —
a user acting on one book can see what they are doing. This preserves the "fully
visible and playable" decision while removing the automated-write hazard.

### OverrideLocked: extract one predicate, repoint the existing sites

Enforcement today is **three hand-rolled sites, and they do not agree** (measured
on `main`, 2026-08-23; an earlier draft of this document said two):

| Site | Condition |
|---|---|
| `internal/plugins/maintenance/repair_junk_titles.go:141` | `OverrideLocked \|\| OverrideValue != nil \|\| FetchedValue != nil` |
| `internal/plugins/maintenance/title_repair.go:117` | `OverrideLocked \|\| OverrideValue != nil` |
| `internal/server/handlers/metadata/handler.go:1001` | `OverrideLocked \|\| OverrideValue != nil` |

The other 23 non-test references are struct fields and DTO pass-throughs — they
carry the flag, they do not enforce it.

The deep pass must **not** become a fourth variant. Extract a single canonical
predicate, migrate all three existing sites onto it, and have the deep pass call
the same one. This costs more changed call sites and is the correct fix: the
drifting `FetchedValue` clause is resolved deliberately rather than copied or
dropped by accident, and there is one place to mutation-test.

Landing it as part of the scan work rather than a preceding PR was the owner's
call; Risk 4 below still holds, so the predicate and its regression test must be
in place **before** the background deep pass is switched on.

### A stuck provisional row must surface itself

Bounded retry with a visible terminus:

- Count attempts on the row.
- Past the threshold, stop retrying, mark the row failed and record the last error.
- Surface it in the existing operations/diagnostics view.

Retrying forever with only a log line is rejected: it burns cycles on a
permanently bad file and makes the log the sole witness. Silence is the failure
mode this repo keeps rediscovering.

## Resolved in review — round 2, 2026-08-23 **[OWNER]**

### The provisional filter lives in the store, not at each call site

Risk 2 said a missed call site means comparing books by an absent hash, and that
it needed a deliberate enumeration rather than a grep. The accepted answer removes
the need for the enumeration to be complete: **the dedup- and bulk-sensitive read
paths go through store methods that exclude provisional rows by default.**

- The safe behaviour is what a caller gets by writing the obvious thing.
- Including provisional rows requires calling a differently-named method, so it is
  visible in review and greppable after the fact.
- A read path added next year inherits the exclusion without anyone remembering.

This is the same shape as the compile-probe / let-the-compiler-partition work in
the store-decoupling lane, one rung down: not type-enforced, but the default is
safe and the unsafe case has to be spelled out.

### A changed file keeps its old hash, marked stale

When Discover sees size or mtime differ on a known file, the row goes back to
`NeedsDeepScan = true` **and keeps its previous `FileHash`, plus a stale marker.**

- Dedup already excludes provisional rows, so the stale hash is never compared.
- If the deep pass then fails repeatedly, the book still has its previous hash on
  record rather than nothing — the failure is recoverable and non-destructive.
- Clearing the hash immediately was rejected for exactly that reason: it converts a
  deep-pass failure into permanent data loss on a row that was previously fine.

### A moved file is repointed, not re-ingested

If a path disappears and a new path appears with the same `(size, mtime)`, Discover
treats it as a **move**: the existing row is repointed to the new path, keeping its
hash, chapters and metadata, and `NeedsDeepScan` stays `false`. No re-hash, no
provisional window, no transient duplicate.

Two consequences worth stating:

- **This is upstream of the repoint lane.** That lane measured 14,439 repointable
  rows and found the collision bucket to be genuine duplicate books. Detecting the
  move at scan time stops new rows entering that backlog at the source, rather than
  repairing them afterwards.
- **Discover becomes two-phase.** "This path is gone" is only knowable once the
  whole walk has finished, so move matching runs after the walk, not during it.

### New media auto-queues a deep pass at low priority

Adding media queues a deep pass for just those books at **low priority** — it starts
on its own and yields to interactive work — with a control to promote it to run
immediately. The default requires no click, matching *"by default it's just
backgrounded"*, while the override is one action rather than a settings change.
Prompting on every import was rejected as inconsistent with that default.

## Resolved in review — round 3, 2026-08-23 **[OWNER]**

### The fast pass never writes metadata to a book that already exists

This closes a clobber vector the earlier drafts did not cover: they discussed
`OverrideLocked` only in terms of the deep pass, but the fast pass also applies
"basic metadata", and on a *changed* file that lands on a row a user may have
curated.

- **New file** → write header-derived metadata. There is nothing to lose.
- **Changed file** → set `NeedsDeepScan` and the stale marker, and touch nothing
  else. Title, author, series, everything: untouched.

So exactly **one** code path writes metadata to an existing book — the deep pass,
through the extracted predicate. One path to audit, one place to mutation-test.
Routing the fast pass through the same predicate was rejected not because it is
unsafe in principle but because it doubles the number of guarded write paths for a
benefit (a slightly fresher title after a re-tag) the deep pass delivers anyway.

### An ambiguous move is not a move

If two or more vanished rows match a new file's `(size, mtime)`, Discover declines
to repoint: the file is ingested as a new provisional book and the ambiguity is
recorded.

The asymmetry of the failure modes decides it. A wrong repoint attaches one book's
hash, chapters and curated metadata to a different book's file, silently and
permanently. A declined repoint costs a transient duplicate that the deep pass and
dedup resolve. Guessing by path similarity would handle folder renames
automatically, but its wrong answers are silent, and this is metadata a user may
have spent effort on.

### An unparseable header still produces a visible row

A file whose tag header cannot be read is ingested with a title derived from its
path, and flagged twice: `NeedsDeepScan = true` and header-unparseable. The deep
pass then tries harder, with `ffprobe`.

- **Nothing on disk is invisible in the app.** Skipping the file was rejected on
  exactly that ground — silent absence is this repo's recurring failure mode, and a
  diagnostics-only report is not a substitute for the book appearing.
- **The junk title is marked as junk.** An unflagged path-derived title is
  indistinguishable from a curated one, which is how junk-title debt accumulated
  before; the flag is what lets a later pass find and fix these.

### Provisional state rides on the existing book payload

`needs_deep_scan`, plus the stale-hash and unparseable-header markers, are fields
on the book response the UI already fetches. No second request, and no separate
list that can drift out of sync with the library view it annotates.

A dedicated count endpoint was considered for a global "N pending" indicator and
deferred — it can be added later without changing this shape, and is only worth it
if the indicator turns out to need it.

## Open questions for review

1. Does the "pending full scan" state need a UI affordance beyond a badge — e.g. a
   count, or a way to prioritise one book's deep pass on demand?
