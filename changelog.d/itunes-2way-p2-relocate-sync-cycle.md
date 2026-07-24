<!-- file: changelog.d/itunes-2way-p2-relocate-sync-cycle.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2d8f1c93-7b64-4d50-9e18-3c7b5a0e1d72 -->
<!-- last-edited: 2026-07-24 -->

### Added

#### iTunes 2-way-sync P2 — relocate-only sync cycle (dry-run; the MVP write path)

`internal/itunes/relocate_sync_cycle.go` adds `RunRelocateSyncCycle`, composing the
P0–P1 primitives into the MVP sync cycle: **plan** (`ComputeRelocateOps` — location-only,
0 adds/0 removes) → **guard** (`SafeWriteITL` contract armed for the AO library: F7
`AllowedWritebackRoot`, K13 identity, K14 magnitude via `PartitionedTrackCount`, and a
pre-rename SHA re-verify) → **verify** in memory (`VerifyRelocateWrite` raw-byte oracle,
NO write) → **commit** (gated behind `Apply`, with post-commit re-verify and auto-rollback
from the `.bak`). A `cmd/pid-census --sync-dry-run` mode runs it read-only.

**Dry-run proven on the real library:** `planned=1, already_correct=77,210, unmatched=0,
unmappable=0, ORACLE_OK=true`. Two things this confirms: the relocate plan **preserves** the
AO library's `.itunes-writeback/iTunes Media/` paths (77,210/77,211 tracks already correct —
it does not strip the media root and break links), and the F7 guard scope lets the write
through (the in-memory contract pass was not rejected by `location-form`). The library is
essentially already in sync (1 drifted track).

**Dry-run by default; `Apply=true` is a gated production decision** — writing the live iTunes
library needs explicit owner authorization + the dry-run review (the oracle proves "only the
planned tracks changed," not that the new location is semantically correct — that is what the
dry-run is for). Unit-tested helpers; env-gated real-library dry-run via the cmd.
