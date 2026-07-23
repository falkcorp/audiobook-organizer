<!-- file: docs/plans/2026-07-23-itunes-2way-sync-continuation.md -->
<!-- version: 1.0.0 -->
<!-- guid: 92609748-e3b2-4e21-8a54-ffc76f68f45b -->
<!-- last-edited: 2026-07-23 -->

# iTunes Writeback / 2-Way-Sync — Continuation Handoff

Session-independent handoff for the next instance. Paste the **Prompt** section
into a fresh session, or read this whole doc. Companion references:
[`../specs/2026-07-22-itunes-2way-sync-writeback-design.md`](../specs/2026-07-22-itunes-2way-sync-writeback-design.md)
and the memory file `project_itunes_writeback_pathnorm_bug.md`.

## What already shipped (2026-07-22 / 2026-07-23) — do not redo

- **P1 relocate — APPLIED + itl-diff-verified on prod.** `POST /api/v1/itunes/relocate`
  repoints each iTunes track at its book_file's current AO path, matched per-**file**
  `BookFile.ITunesPersistentID`. Location-only: never adds/removes. 6,414 relocated;
  tracks Δ0, playlists 357→357 Δ0, all 10 guards pass. Code:
  `internal/itunes/relocate.go`, `internal/server/itl_relocate.go`.
- **`POST /api/v1/itunes/adopt-base`** re-blesses the `.identity.json` sidecar after the
  writeback library is reseeded/changed (else K13/K14 identity guards reject writes).
- **P2 (adds) — 0 work** (`unmatched_files: 0`; every primary book_file already has a track).
- **P3 cleanup (`POST /api/v1/itunes/cleanup-merged`) — BUILT but HELD. DO NOT APPLY.**
  Its criterion (`IsPrimaryVersion==false && !ownedByPrimary`) is TOO BROAD: the dry-run
  sample showed the 5,967 "to_remove" are real chapter/part files with EMPTY
  `merged_into_book_id` — removing them would delete real audio, not duplicates.

## Problem 1 — definitively solve relocation + removals (redefine P3)

The removal set must be provable duplicates only. Investigate and decide the correct
criterion — likely: remove a non-primary track ONLY when its book shares a
`version_group_id` with a PRIMARY book whose own track is kept, OR `MergedIntoBookID`
is set to a book whose track is kept. Specifically:

- Why are **4,298 PIDs owned by BOTH a primary and a non-primary book_file**
  (`shared_skipped`)? Is that a data-integrity bug (same file on two book records)?
  This thread probably unravels the whole non-primary confusion.
- How many non-primary tracks are genuinely merged (`MergedIntoBookID` set) vs
  shattered-book / version-group artifacts that must NOT be removed (they may need
  RE-GROUPING via reconciliation, not deletion)?
- Is relocation itself fully correct now, or do shattered books cause wrong/duplicate
  relocations?

Redefine `ComputeMergedTrackCleanup`, re-measure via dry-run + sample, get sign-off
before any apply. Entangled with
[`../specs/2026-07-19-fingerprint-driven-reconciliation-design.md`](../specs/2026-07-19-fingerprint-driven-reconciliation-design.md).

## Problem 2 — reverse direction (iTunes → writeback → AO), for full-time use

Once iTunes is used full-time, media added/played/rated/playlisted IN iTunes gets
written to the writeback library. Design + build the steady-state so those changes
sync back and nothing is lost:

- **New audiobooks/tracks added in iTunes are NOT in AO's DB.** Relocate correctly
  leaves them alone, but AO must IMPORT them (from the writeback library, **not** the
  deprecated `books/itunes/`). Confirm the importer source and wire it.
- **Play counts / last-played / bookmarks / new playlists** created in iTunes → decide
  whether to ingest back into AO (P4 read-back, currently deferred / preserve-only) and
  build it if needed.
- **Pick and document the source-of-truth model:** is the writeback library
  authoritative for membership (AO imports from it + only relocates paths), or is AO
  authoritative? Define the loop end-to-end. This is the biggest real risk before heavy
  full-time use.

## Problem 3 — audit for other footguns

- **`/rebuild` and `/rebuild-full`** (`internal/itunes/rebuild.go` — `ComputeITLDiff` /
  `RebuildITLFromDB`) are DB-authoritative and would GUT the now-real library
  (~85k removals). Guard or deprecate them so they can't be run against a
  non-throwaway library.
- **`adopt-base` / identity steady-state:** when iTunes changes the library, does the
  sidecar drift and block the next relocate? Define exactly when adopt-base must re-run.
- Any other place that assumes the writeback library is disposable.

## Hard constraints / reference

- **NEVER write to `books/itunes/**`** (real active iTunes library, hands-off). All
  writeback targets ONLY `.itunes-writeback/`.
- Every writeback goes through `SafeWriteITL` (backup + 10-guard `ITLSafetyContract` +
  rollback); bounded-delta caps removes at 5000. `cmd/itl-diff --audit <pre> <post>` is
  the acceptance oracle (decrypt+inflate; the `.itl` body is AES-encrypted, so raw
  grep = 0 matches by design).
- Endpoints: `POST /api/v1/itunes/relocate`, `/adopt-base`, `/cleanup-merged` (all
  `dry_run=true`-capable), plus the destructive `/rebuild` + `/rebuild-full`.
- The Claude Code classifier **BLOCKS prod-write curls** (POST without `dry_run`); the
  owner runs applies himself with a `!` prefix. Dry-runs (reads) are fine.
- Deploy is `make deploy` from the up-to-date main tree. Worktree discipline: branch per
  change, PR + rebase-merge, remove worktree after merge.
- **Fleet-internal specifics are NOT in this public repo** (per the private-knowledge
  rule): the prod API host + port, the `.api-token` format/extraction, the ZFS-snapshot
  read-only Pebble-DB scan procedure, and the deploy/sudo details live in the **private
  memory file `project_itunes_writeback_pathnorm_bug.md`** (and falkcorp/infra-docs).
  Read those for the actual values. Never commit IPs / internal hosts / tokens here.

**Deliverable:** a findings doc + design under `docs/` and a `PLAN.md`; do not apply any
destructive prod op without a dry-run, a reviewed sample, and explicit owner go-ahead.

---

## Prompt (paste into a fresh session)

> iTunes writeback / 2-way-sync — continue this work. Read
> `docs/plans/2026-07-23-itunes-2way-sync-continuation.md` and the memory file
> `project_itunes_writeback_pathnorm_bug.md` in full first. Do a READ-ONLY
> investigation and present findings + a written PLAN.md before any code or prod
> action. Solve, in order: (1) redefine the P3 removal/relocation criterion to
> provable-duplicates-only (version_group / MergedIntoBookID linkage) and explain the
> 4,298 shared-PID oddity; (2) design + build the reverse sync (iTunes → writeback →
> AO) for full-time use, including importing iTunes-added media from the writeback
> library and deciding the source-of-truth model; (3) guard/deprecate the destructive
> `/rebuild` + `/rebuild-full` against the now-real library and define the adopt-base
> steady-state. Constraints: NEVER write `books/itunes/**`; writeback targets only
> `.itunes-writeback/`; prod-write curls are classifier-blocked so I run applies with
> `!`; every write via SafeWriteITL; verify with `cmd/itl-diff --audit`; dry-run +
> reviewed sample + my go-ahead before any destructive apply.
