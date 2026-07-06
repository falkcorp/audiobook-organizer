<!-- file: docs/audits/2026-07-05-updatebookfile-memdb-writeback-fingerprint-wipe.md -->
<!-- version: 1.1.0 -->
<!-- guid: 3f9b1c22-7a4e-4d19-9c66-2e0a5b8d4471 -->
<!-- last-edited: 2026-07-06 -->

# UpdateBookFile memdb-writeback fingerprint wipe (discovered during STOREFID P3-W3 audit)

## Summary

Four whole-library maintenance jobs read book files via `GetAllBookFiles()` (which
returns the **memdb-slim projection** under prod's `UseMemDB=true` default — with
`AcoustIDFingerprint`, `AcoustIDSeg0..6`, `FingerprintFailureReason`,
`FingerprintFailureDetail`, `FingerprintDiagnosticJSON` nil'd to save RAM), then write
the slim struct back via the **bare, unguarded `PebbleStore.UpdateBookFile`**, which is a
**blind full-record replace**. The nil fingerprint/diagnostic fields overwrite the real
stored values → **silent fingerprint data loss in Pebble**.

This is independent of the STOREFID type refactor — it exists on `main` today. STOREFID's
`GetAllBookFiles → BookFileCore` retype is what surfaced it (the slim type makes the
fidelity mismatch a compile error instead of a silent nil).

## Root cause

`UpdateBookFile` (`internal/database/pebble_store_bookfiles.go`) had **no
preserve-on-empty guard**. Its siblings **already have the guard**:
- `UpsertBookFile` — restores from `existing` when incoming is empty (comment cites PERF-7).
- `BatchUpsertBookFiles` — same guard (comment names `tag-backfill`).

`UpdateBookFile` was deliberately left unguarded because it doubles as the fingerprint
**write** path: `internal/plugins/acoustid/backfill.go` calls it with a *fresh non-empty*
`AcoustIDFingerprint` and **intentionally nil-clears the diagnostic fields on success** to
drop a prior failure tombstone. A blanket 4-field guard (mirroring `UpsertBookFile`) would
strand a stale failure reason forever.

## Fix (PR-A — shipped)

Add a preserve-on-empty guard to `UpdateBookFile` for **`AcoustIDFingerprint` only** — the
critical ~230 KB/file field, provably never intentionally cleared via `UpdateBookFile`
(the only non-empty writer is `backfill.go`; the two `AcoustIDFingerprint = nil` sites are
`stripBookForMemdb`'s projection build and `ClearAllAcoustIDFingerprints`' raw bulk-clear,
neither via `UpdateBookFile`). The 3 diagnostic fields are deliberately **not** guarded
here so backfill's clear-on-success keeps working.

Regression test asserts BOTH directions
(`internal/database/pebble_bookfile_preserve_test.go`):
- `TestUpdateBookFile_PreservesFingerprintOnEmptyIncoming` — slim round-trip keeps the fingerprint.
- `TestUpdateBookFile_WritesFreshFingerprintAndClearsFailureDiagnostics` — a real write still overwrites the fingerprint AND clears diagnostics (guard did not over-fire).

### Known residual gap (owned by W3, not unowned)

The 3 diagnostic fields (`FingerprintFailureReason`/`Detail`/`DiagnosticJSON`) are still
wiped for **failed-fp books** touched by the 4 slim-round-trip jobs. This is operationally
minor: `FingerprintFailedAt` (which drives `backfill.go`'s skip logic) is memdb-**kept**, so
correctness is unaffected — only the human-readable reason/detail is lost. The **structural**
fix is STOREFID W3 (PR-C): once `GetAllBookFiles` returns `[]BookFileCore`, those 4 jobs can
no longer write a whole `BookFile` back, forcing a field-scoped update or a full hydrate.

## Affected callers

### HEAVY-WRITEBACK — active/latent fingerprint wipe (prod)
| Job | file:line | DryRun default | Exposure |
|---|---|---|---|
| `recompute_itunes_paths` | `internal/maintenance/jobs/recompute_itunes_paths.go:33,51` | **false** | **Active data loss** on every run that changes an iTunes path |
| `enrich_book_files` | `internal/maintenance/jobs/enrich_book_files.go:38,64` | **false** | **Active data loss** on every run that sets a track number |
| `fix_book_file_paths` | `internal/maintenance/jobs/fix_book_file_paths.go:33,51` | true | Wipes only when operator disables dry-run |
| `repair_missing_files` | `internal/maintenance/jobs/repair_missing_files.go:48,542` | true | Same |

PR-A's `AcoustIDFingerprint` guard stops the critical wipe for all four immediately; W3
closes the diagnostic-field residue by making them field-scoped/hydrate.

### HEAVY-READ — functional no-op under memdb (prod)  → PR-B
| Op | file:line | Symptom |
|---|---|---|
| `acoustid online_lookup` | `internal/plugins/acoustid/online_lookup.go:97,112` | `len(f.AcoustIDFingerprint)==0` always true → drops every candidate → op reports success, does nothing. |
| `acoustid lsh_backfill` | `internal/plugins/acoustid/lsh_backfill.go:80,97` | Identical pattern. |
| `dedup lsh_index_build` | `internal/plugins/dedup/lsh_index_build.go:114,178` | Self-documented skip ("memdb strips AcoustIDFingerprint… Skip silently"). |

Fix (PR-B) = **proxy-then-hydrate**: filter the slim list on KEPT proxy fields
(`AcoustIDFingerprintDurationSec > 0`, `FingerprintFailedAt == nil`), then per candidate call
`GetBookFiles(bookID)` (full/raw Pebble). No bulk-fingerprint getter (would reintroduce the
RAM blowup memdb prevents).

### TEST-FALLBACK — not a prod risk
`acoustid reset_all` (`internal/plugins/acoustid/reset_all.go:88`) — the per-row
`UpdateBookFile` path is gated behind a `*PebbleStore` type assertion for mock/sqlite
tests; prod uses the bulk-clear path (`ClearAllAcoustIDFingerprints`). Unreachable in prod.

## Fix sequencing

1. **PR-A (shipped):** `AcoustIDFingerprint` preserve-on-empty guard on `UpdateBookFile` + two-direction regression test. Stops the critical wipe.
2. **PR-B:** reroute the 3 HEAVY-READ fingerprint ops to proxy-then-hydrate (restores coverage).
3. **PR-C (= STOREFID W3):** retype `GetAllBookFiles → []BookFileCore`; 13 LIGHT callers retype; the 4 writeback jobs move to field-scoped update/hydrate (closing the diagnostic residue). **W3 landmine: do NOT let the 4 writeback jobs use a `BookFileCore.ToBookFile()` bridge then write back — that reconstructs a nil-fingerprint `BookFile` and re-introduces the wipe (the guard only saves `AcoustIDFingerprint`, not diagnostics). Require field-scoped update or full hydrate.**

## Full per-caller classification
13 LIGHT, 3 HEAVY-READ, 4 HEAVY-WRITEBACK, 1 TEST-FALLBACK (of 21 external callers).
`tag_backfill` + `backfill_file_hashes` write back but via the GUARDED
`BatchUpsertBookFiles` / named-field `SetBookFileHash` respectively — safe.
