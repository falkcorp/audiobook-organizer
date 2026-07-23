<!-- file: docs/specs/2026-07-23-itunes-2way-sync-continuation-findings.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7c1a9f4e-2d63-4b0a-9e57-8a1c4d2f6b03 -->
<!-- last-edited: 2026-07-23 -->

# iTunes 2-Way-Sync — Continuation Findings + Design (READ-ONLY investigation)

> **STATUS: findings for owner review. Read-only code investigation only — no code
> written, no prod actions taken.** Companion:
> [`2026-07-22-itunes-2way-sync-writeback-design.md`](2026-07-22-itunes-2way-sync-writeback-design.md),
> [`2026-07-19-fingerprint-driven-reconciliation-design.md`](2026-07-19-fingerprint-driven-reconciliation-design.md),
> and the private memory `project_itunes_writeback_pathnorm_bug.md` (prod host/port,
> token format, ZFS-snapshot DB-scan procedure — kept out of this public repo).

This resolves the three problems in the continuation handoff by reading the merge
apply path, the cleanup/relocate ops, the config wiring, and the identity/contract
guards. The headline result: **Problem 1 (P3 redefinition) is a measure-and-stop, not a
build** — the current cleanup looks at the wrong set of books entirely, and the correctly
narrowed removable set may be ~0.

---

## Problem 1 — relocation + removals (the P3 redefinition)

### 1.1 The merge path already removes ITL tracks inline; soft-deleted losers are invisible to `cleanup-merged`

`internal/merge/service.go` `MergeBooks` (per-loser cleanup, lines ~196–250) does, for
each non-primary "loser":

- **(a)** collects the loser's iTunes external-ID PIDs *before* reassignment;
- **(b)** reassigns external IDs to the winner;
- **(c)** `writeBackBatcher.EnqueueRemove(pid)` for each collected PID — **the ITL track
  removal is queued at merge time**;
- **(d)** `SoftDeleteBook` → `MarkedForDeletion = true`, `IsPrimaryVersion = false`.

So *merged-loser ITL cleanup is a real-time side effect of the merge itself*, not a job a
backfill re-derives later.

Critically, `GetAllBooksFullFrom` (`internal/database/pebble_store.go:488`) **skips
soft-deleted books** (`if book.MarkedForDeletion != nil && *book.MarkedForDeletion {
continue }`, ~line 204). `ComputeMergedTrackCleanup` enumerates the DB *only* through
`GetAllBooksFullFrom`, so **it never sees merge losers.** Every book in its `nonPrimary`
set is therefore a **live, non-soft-deleted, `IsPrimaryVersion==false`** book — a
version-group alternate or a shattered fragment whose `book_files` are **real,
un-superseded audio**.

**This is exactly why the 40-track dry-run sample showed empty `merged_into_book_id` and
real chapter/part paths** (Wind and Truth Ch39/87, Hyperion Ch108, …). The current P3
criterion `IsPrimaryVersion==false && !ownedByPrimary` does not select merged duplicates
— **it selects live non-primary audio, and applying it would delete real content.** P3 as
built must be retired or redefined; it is not merely "too broad."

### 1.2 `MergedIntoBookID` is set by a different path and is rare among *live* books

The `MergeBooks` version-group path (above) **soft-deletes** losers and does **not** set
`MergedIntoBookID`. `MergedIntoBookID` is set by the consolidation/combine path
(`CombineBooks`, which `MoveBookFilesToBook` moves files to the survivor). A *live*
`IsPrimaryVersion==false` book that still owns `book_files` **and** carries
`MergedIntoBookID` is therefore unusual — and it is the only shape that is a
provable duplicate.

### 1.3 Reject the `version_group_id` path outright

"Shares a `version_group_id` with a primary" **does not prove same audio.** Version groups
hold genuinely *different* physical files — different editions/snapshots (`[[feedback_version_snapshot]]`:
"Version" = different physical files). Removing an ITL track because its book shares a
version group with a primary would **delete a real alternate edition's audio.** It is not a
provable duplicate.

The only defensible duplicate guarantee is **provenance you can name a survivor for**:
> Remove an audiobook ITL track only when you can point to the *surviving* track that
> carries the same audio and is being kept.

Concretely: `MergedIntoBookID = X`, where `X` resolves to a **kept primary** whose
`book_file` PID is present in the ITL and is **not itself** in the remove set. Anything you
cannot name a survivor for → **review / re-group** (reconciliation), **never delete.**

### 1.4 The genuinely-removable population is "merge orphans" — and `cleanup-merged` can't currently see them

There *is* a legitimate cleanup target, but it is a different set than the code selects.
During the broken-writeback window (pre-2026-07-22), `writeBackBatcher.EnqueueRemove` was
effectively a no-op whenever writeback was disabled/failing, so **merged-loser tracks were
enqueued for removal but never actually removed from the ITL.** Those loser books are now
soft-deleted, so their PIDs belong to **no live `book_file`** — the tracks sit in the ITL
as **orphans** (a subset of the ~11,999 "library-only" tracks measured in P0, alongside
music/podcasts the relocate correctly ignores).

To clean those safely we must:
1. enumerate **soft-deleted** iTunes-audiobook losers (needs a store accessor that
   *includes* `MarkedForDeletion`, unlike `GetAllBooksFullFrom`);
2. for each, confirm the **survivor** (its merge target / version-group primary) has a
   `book_file` PID that **is present in the ITL and kept**;
3. remove only that loser's orphaned PID.

**But the size of this set is unknown and may be ~0** if the batcher was functioning for
most merges. → **Measure before building.** Redefine `ComputeMergedTrackCleanup` only if
the provable orphan set is non-trivial; otherwise P3 becomes a measure-and-stop like P2.

### 1.5 Q1 — the 4,298 `shared_skipped` PIDs, and whether relocate is fully correct

`shared_skipped` = a PID present on **both** a primary and a non-primary *live* book_file.
Per-file PIDs are minted **unique** at provision time (`TrackProvisioner.Provision`
mints one `GeneratePIDHex()` per `book_file`), so the same PID string on two book_file rows
is a genuine **book_file duplication anomaly** — most likely the same underlying file
referenced by two book records (a shatter/version artifact), not two distinct files.
The current code handles it safely-by-default (relocate skips non-primary books; cleanup
*keeps* shared PIDs), so it is not an active hazard — but it is worth quantifying as a
data-integrity signal, and it is *the* probe for relocate correctness:

> **Relocate-correctness probe:** are there PIDs on **>1 PRIMARY book_file with different
> `FilePath`s**? If yes, `relocateOpsFromTracks`'s `emitted` first-wins guard
> (`relocate.go:120`) is **iteration-order-dependent** and could point a track at the wrong
> file. If the count is **0**, relocate is provably fully correct. If **>0**, add a
> deterministic tie-break (or route those PIDs to review) — do not leave it order-dependent.

Shattered books themselves do **not** break relocate: each fragment is its own 1-file
primary book, matched per-file PID, repointed at its own current path — correct by
construction. The only correctness risk is the shared-PID anomaly above.

### 1.5b Census — measured on prod (2026-07-23, ZFS-snapshot read-only scan)

Ran `ComputePIDIntegrity` (`cmd/pid-census`) against a snapshot copy of the live Pebble DB:

| Metric | Count |
|---|---|
| book_file rows carrying a PID | 93,590 |
| distinct PIDs | 84,535 |
| **duplicate PIDs (owned by >1 row)** | **8,987** |
| — `same_file` (all owners share FilePath → duplicate rows) | 8,762 (97.5%) |
| — `diff_file` (owners point at different files → copied PID) | 225 (2.5%) |
| duplicate PIDs present in the ITL | 8,987 (all) |
| **relocate probe: PIDs on >1 primary, differing paths** | **94** (non-zero → relocate first-wins IS order-dependent) |

Sample confirms the mechanism: the dup owners are an organized AO copy
(`…/audiobook-organizer/…`) + the deprecated iTunes-tree original
(`…/books/itunes/…`) sharing one PID — the organizer version-split copy (culprit #1).

**Repair plan (dry-run):** auto-resolves **8,984 / 8,987** (8,762 same_file keep-one +
222 diff_file keep-by-ITL-location); **3 ambiguous** diff_file groups left UNTOUCHED for
review (fail-safe); **9,050 redundant PID copies cleared**. Clearing is DB-field-only
(no row/file deletion) and, because each resolved PID ends with exactly one owner, it also
eliminates the 94 relocate-order-dependence cases.

### 1.6 Problem 1 conclusion

- **Retire the current P3 criterion** — it targets live non-primary audio, not duplicates.
- **Redefine** (if the measured set warrants) to *provenance-anchored* removal:
  `MergedIntoBookID → kept-primary-with-track`, enumerating soft-deleted losers.
- **Reject** version_group-based removal entirely (→ reconciliation/regroup, not delete).
- **Measure first** via an extended dry-run: (i) count `MergedIntoBookID`-anchored provable
  removals, (ii) count soft-deleted merge-orphans in the ITL, (iii) count PIDs on >1 primary
  book_file with differing paths (relocate probe), (iv) explain the 4,298 shared set.
- **Relocate is safe and shipped**; the probe in 1.5 either confirms it fully correct or
  surfaces a bounded tie-break fix.

---

## Problem 2 — reverse direction (iTunes → writeback → AO)

### 2.1 Importer source wiring

- **Import source** = `config.ITunes.LibraryReadPath` (`itunes.library_read_path`) —
  today the **deprecated** `books/itunes/iTunes Library.xml`.
- **Writeback target** = `config.ITunes.LibraryWritePath` (`itunes.library_write_path`) =
  `.itunes-writeback/iTunes Library.itl`.

For full-time use, AO must import iTunes-added audiobooks **from the writeback library**
(the one iTunes actively mutates), not from `books/itunes/`. This is a config/wiring change
plus a source decision — **not** repointing `library_read_path` blindly (see the hazard).

### 2.2 The import-loop hazard (must design around)

The writeback library now contains **AO's own relocated audiobook tracks**
(`W:\audiobook-organizer\…`). If AO imports audiobooks from it **without dedup-on-import**,
it re-ingests its own output → **duplication.** This is the same ordering constraint the
reconciliation spec already flagged: **import-back depends on dedup-on-import (reconciliation
P4) existing first.** New audiobooks added *directly in iTunes* are the only legitimate
import candidates, and they must be dedup-matched against AO before insert.

### 2.3 Source-of-truth model — **the owner decision** (recommend-and-confirm)

Authority is **split**, and this defines the whole loop, so it must be an explicit sign-off:

- **Writeback library authoritative** for non-audiobook membership (music/podcasts) **and
  all playlists** — AO never owns those; it preserves them verbatim.
- **AO authoritative** for the **audiobook set** — AO's dedup/organization wins; the sync
  only relocates paths and (rarely) adds/removes audiobook tracks.
- **New audiobooks added in iTunes** → read the writeback library, filter `IsAudiobook`,
  and import those whose PID is absent from the DB — **gated on dedup-on-import** so a book
  that already exists in AO merges into the existing primary instead of duplicating.

Read-back of **play counts / last-played / bookmarks / new playlists** into the AO DB is the
deferred **P4** (currently preserve-only: edit-in-place keeps them in the *library* for free,
they just don't flow into the DB). Whether to build DB ingest now is a second owner decision.

### 2.4 Steady-state loop (proposed)

```
iTunes (full-time use) ──mutates──► .itunes-writeback/iTunes Library.itl  (authoritative for music+playlists+play-state)
        │                                             │
        │ new audiobook added in iTunes               │ AO reorganizes / dedups audiobooks
        ▼                                             ▼
  AO import (from writeback, IsAudiobook,     AO relocate (per-file PID, location-only)
   dedup-on-import gated) ─────────────────►  + (rare) provenance-anchored cleanup
```

Everything AO writes goes back through `SafeWriteITL` into the same `.itunes-writeback`
base, so iTunes-side changes are always the starting point of the next cycle — the "2-way"
property. `itl-diff --audit <pre> <post>` is the per-cycle oracle.

---

## Problem 3 — footguns / guards

### 3.1 `/rebuild` + `/rebuild-full` would gut the now-real library

- `/rebuild-full` (`RebuildITLFromDB`, `rebuild.go:363`/`432`) passes
  **`ForceContractConfig()`**, which overrides `bounded-delta`. Against the reseeded
  97,999-track library it would emit ~85k removals and shatter the 356 playlists — and the
  force flag means the contract would *not* stop it on magnitude.
- `/rebuild` (`ComputeITLDiff`) is DB-authoritative but does **not** force, so `bounded-delta`
  would refuse the mass shrink — still dangerous and easy to mis-invoke.

**Guard design (belt-and-suspenders, fail-safe):** add a *target-shape precheck* to both
rebuild handlers that refuses to run when the target library "looks real," independent of
the contract:
- non-audiobook (`!IsAudiobook`) track count over a threshold (a disposable prototype has ~0
  non-audiobook tracks; the real library has ~86k), **or**
- playlist count over a small threshold (prototype had 14; real has 356), **or**
- identity `LibraryPID` != a recorded disposable-prototype marker.

Require an explicit `allow_full_library=true` request param to override, and log loudly.
This fails safe even if someone calls `/rebuild-full` by habit — stronger than deprecation.
(Deprecation alone doesn't stop a muscle-memory curl.)

### 3.2 `adopt-base` / identity steady-state — verified

The identity sidecar (`internal/itunes/itl_identity.go`, `LibraryIdentity`) records
`LibraryPID` + `TrackCount` + `PlaylistCount`. On write, K13 anchors `LibraryPID`; K14
(`guardExpectedMagnitude`, `itl_safety_contract.go:715`) requires the post-write `after`
count to be within **`MagnitudeTolerancePct` (default 10%)** of
`ExpectedTrackCount = sidecar.TrackCount + len(Adds) − len(Removes)`
(`itl_combined_mutate.go:110`).

**Consequences for steady-state:**
- A **location-only relocate** (0 adds / 0 removes) needs `after ≈ sidecar.TrackCount`. As
  long as iTunes-side membership drift stays **within ~10%** of the recorded count, the next
  relocate passes **without** re-running `adopt-base`. Minor iTunes additions do not block it.
- `adopt-base` **must** re-run when: (a) the library is **reseeded/replaced** (LibraryPID
  changes → K13 would reject), or (b) iTunes-side membership drifts **>10%** from the sidecar
  count (K14 would reject).
- **Recommendation:** because the writeback library is the one iTunes actively mutates,
  relocate/cleanup should re-anchor `ExpectedTrackCount` to the **current on-disk base count**
  each cycle (preserving `LibraryPID` for anti-swap), rather than trusting a possibly-stale
  sidecar count — i.e., fold an *auto-refresh of count-while-PID-unchanged* into the write
  path so `adopt-base` is only ever needed for a genuine reseed. **[VERIFY in P0: does K13
  compare `LibraryPID` only, or also a track-PID sample? If a PID sample, define the reseed
  vs. drift boundary precisely.]**

### 3.3 Other disposable-library assumptions to sweep

- Confirm nothing else calls `RebuildITLFromDB` / `ComputeITLDiff` outside the two guarded
  handlers (a scheduled job, a CLI, a test fixture pointed at prod).
- Confirm `ProtectedPaths` is populated on prod (open item from the reconciliation spec §6)
  so no scan/organize path can reach `books/itunes/**`.

---

## Cross-cutting: nothing here has been applied

- No code written; no prod writes; relocate (P1) remains the only applied writeback and is
  itl-diff-verified.
- All measurements proposed above are **reads** (dry-runs / ZFS-snapshot DB scan) and are
  safe to run. Any destructive apply (a redefined cleanup) requires a dry-run + reviewed
  sample + explicit owner go-ahead, per the prod-apply review gate.
