<!-- file: docs/specs/2026-07-23-itunes-2way-p0-findings.md -->
<!-- version: 1.4.0 -->
<!-- guid: 2c9e5a71-8b04-4d36-9f18-7a3c1e6b0d52 -->
<!-- last-edited: 2026-07-24 -->

# iTunes 2-Way-Sync — P0 Findings (read-only)

Executes P0 of `2026-07-23-itunes-2way-sync-system-design.md`. Verified against HEAD
`c5c311a6`. Read-only; nothing applied.

## F1 — K13 library-identity is NOT `LibraryPID`-only (RESOLVES §10b.2, the P1 blocker)

`guardLibraryIdentity` (`internal/itunes/itl_safety_contract.go:749`) checks **both**:
1. `LibraryPID` equality (via `ExtractLibraryPIDHex(hdr)` vs `ExpectedIdentity.LibraryPID`).
2. **track-PID sample overlap**: `ExpectedIdentity.SampleOverlapPct(after)` must be
   ≥ `IdentityMinOverlapPct` (**default 90**, `itl_safety_contract.go:1357`). The sidecar
   stores `PIDSample` = up to **1024** evenly-spaced track PIDs in payload order
   (`itl_identity.go:44,64`); overlap = how many still exist in the written library
   (`SampleOverlapPct`, `itl_identity.go`).

**Consequences for the design (§5 correction):**
- Normal iTunes activity is SAFE: adds don't remove sampled PIDs; play-state changes
  don't touch PIDs. Overlap stays ~100%.
- **Deletions/replacements of sampled tracks erode overlap.** At 90%, up to ~102 of the
  1024 sampled PIDs may vanish before K13 rejects.
- Therefore the steady-state count-auto-refresh (`RefreshLibraryTrackCount`, §5.3) MUST
  **re-derive the PID sample** from the current on-disk library each cycle (keeping
  `LibraryPID` pinned) — not merely refresh the count — or accumulated legitimate churn
  eventually false-rejects a valid relocate.
- **Drift-vs-reseed boundary is now concrete:** `LibraryPID` change = reseed (needs
  `adopt-base`); a large single-step sampled-PID loss = suspicious → the §5.2 drift-ceiling
  + §3.4 settle/quiescence gate catch it. K13 arming is well-defined; no blocker remains.

## F2 — Rebuild-caller audit CLEAN

`RebuildITLFromDB` and `ComputeITLDiff` are called ONLY from the two guarded handlers
(`internal/server/itl_rebuild.go:76` and `:171`); `rebuild.go:352` is the internal slog,
not a caller. Both handlers now carry `GuardRebuildTarget`. No unguarded rebuild path exists.

## F3 — `ProtectedPaths` is EMPTY on prod (safety gap to close)

Prod `/config` reports `protected_paths` empty/unset. So `books/itunes/**` is currently
NOT in the in-process protected set; the scanner avoids it only because it is not a
configured scan root (config-based, not a hard skip). **Action:** populate `ProtectedPaths`
with `books/itunes/**` on prod and adopt the config-load assertion (below) before any
`itunes.libraries`-driven op runs. The assertion is inert until `itunes.libraries` is
populated, so it does not affect the current deployment.

## Config scaffold landed (this PR)

`internal/config/itunes_libraries.go` — the 4-state model (`LibraryRef`/`LibrarySet` with
`PointedAt`/`ImportSource`), `ITunesConfig.Resolve()` (derives the legacy
`LibraryReadPath`/`LibraryWritePath` shims), and `ValidateLibraries()` (the four
fail-closed §2.4 assertions). Wired into `Config.Validate()` and viper load. **Inert until
`itunes.libraries` is populated** — empty `Libraries` → legacy behavior byte-for-byte.
Unit-tested (Resolve for both `import_source` values; all four assertions; back-compat).

## F4 — Cleanup provenance census: P3 is MEASURE-AND-STOP (provable removable set = 0)

**Measured 2026-07-24** against a consistent read-only copy of prod: ZFS snapshot of
`rpool/ROOT/ubuntu_0nm86n/var/lib` → the Pebble dir copied out of `.zfs/snapshot/` (13G,
1487 files, crash-consistent) + a copy of the live AO writeback `.itl`
(`.itunes-writeback/iTunes Library.itl`, 32,103,033 bytes). Ran
`pid-census --merge-provenance` (new mode, `internal/itunes/pid_integrity.go`
`ComputeMergeOrphanCensus`). All artifacts + the snapshot were destroyed afterward; the
`books@itunes-ao-fallback-2026-07-23` snapshot is intact.

```
tracks_in_itl        = 97999
  healthy            = 77211   (owned by a live PRIMARY book_file)
  stale_owner        =  7324   (owned by a soft-deleted / non-primary book_file)
  no_live_owner      = 13464   (NO book_file carries the PID — unattributable)
automerge_journal_entries = 0      ← the loser journal is EMPTY on prod
merged_into_losers        = 1
residual_duplicate_pids   = 3      (matches the 8,987→3 pid-repair; PIDs are unique)
>>> PROVABLE_MERGE_ORPHANS = 1   (sha_gated_removable = 0)
```

**Decision: P3 retires the unsafe `cleanup_merged.go` handler as a guarded no-op and
builds NO removal machinery.** The provable, SHA-gated, safely-removable orphan set is
**0** (1 candidate — "Stay of Execution", via `MergedIntoBookID` — with no live-primary
FileHash match, so not even SHA-confirmed). This is the measure-and-stop branch §6.5
names as the default expectation.

**Why the number is a floor, and why that STRENGTHENS the stop (advisor fork, confirmed by
code):** there is no durable, mutation-immune record of "what PIDs a loser owned at merge
time." `merge.Service.MergeBooks` (`internal/merge/service.go:228`) **reassigns** the
loser's `ext_id:itunes:*` mappings to the winner and soft-deletes the loser, but writes
**neither** the `AutoMergeJournalEntry` journal **nor** `MergedIntoBookID`. The journal is
written ONLY by `dedup/auto_resolve.go` (Tier-1 auto-resolve → 0 entries on prod);
`MergedIntoBookID` ONLY by `FlagMetadataHashDuplicate` (metafetch dedup +
`pebble_store.go:4030` → 1 on prod). And the PID-uniqueness repair already cleared
duplicate `book_file.ITunesPersistentID` off non-canonical rows. So a track that WAS a
loser's, whose PID→loser link was severed, lands in `no_live_owner` (unattributable), not
in the provable set. **`~0` here means "the provenance link is gone," not "no orphans
exist" — and you cannot build a provenance-anchored bulk remover from provenance that
isn't durably recorded.**

**The 7,324 + 13,464 "leftover-looking" tracks are NOT safely removable.** Samples confirm
`stale_owner` is dominated by legitimate version-group alternates
("Warbreaker … Part 1 of 2 (Dramatized Adaptation)", "Legion – Skin Deep") — the editions
§6.5 forbids removing (reject `version_group_id` basis). `no_live_owner` is unattributable
(user's direct non-audiobook imports OR severed orphans) and hands-off. Removing from
either bucket violates the spec's fail-closed rules.

**Two follow-ons (NOT this PR, NOT blocking the stop):**
1. **No durable merge-provenance trail on prod.** If a future release wants provenance-
   anchored cleanup, it must FIRST make `merge.Service.MergeBooks` record losers (write the
   journal at merge time), then re-run this census. Until then, cleanup is un-buildable
   safely. **Second consequence of the same gap:** `UnmergeAuto` reads the same journal, so
   the "undo merge" capability is effectively **inert for every production merge** — the big
   9,074→1,311 dedup drain went through `MergeBooks`, which wrote nothing to unwind. Any
   real unmerge/audit recovery needs the same durable-loser-recording fix.
2. **`no_live_owner` composition** (13,464 = 13.7% of tracks): classify by audiobook genre
   to separate the user's non-AO music/podcasts (expected, hands-off) from severed
   audiobook orphans. Does not change the P3 decision (both are un-removable); informs any
   future re-attribution effort.

## F5 — Cross-type PID-collision backstop: disjointness invariant HOLDS (0 real collisions)

**Measured 2026-07-24** (`pid-census --cross-type`, `internal/itunes/cross_type.go`
`ComputeCrossTypeCollisions`), same consistent prod copy as F4. Classifies every AO-`.itl`
track (`isAudiobookITL`: Kind/Genre/Location) and cross-tabs it against AO book_file
ownership. The disjointness invariant for the relocate op: an AO book_file PID must resolve
to an **audiobook** track, never a music/podcast one — else a relocate rewrites a
non-audiobook track's location.

```
tracks_in_itl = 97999   audiobook = 92807   non_audiobook = 5192
  ab_owned = 81099   ab_unowned = 11708   non_ab_owned = 3436   non_ab_unowned = 1756
CROSS_TYPE_COLLISIONS = 3436  (all live-primary owner)
```

**All 3,436 "collisions" are audiobooks that `isAudiobookITL` under-classifies — NOT real
music.** Proof: AO's DB stores only audiobooks, so any AO-owned track is definitionally an
audiobook; and the genre histogram over all 3,436 is 100% book-shaped — `Audio Book` (653) +
`audio book` (52), `(none)` (1130), `The First Law Book Two` (230, a Joe Abercrombie
audiobook), `Science Fiction`/`Sci Fi`/`SciFi`/`Sci-Fi` (~660), `Suspense`, `Fantasy`,
`Comedy`, `Speech`, various `LGBT …` literary tags — **zero** music genres (no Pop/Rock/
Classical/Podcast); kinds are ordinary `MPEG audio file` (3194) / `AAC audio file` (236) /
`Apple Lossless` (6). The 1,756 `non_ab_unowned` are the user's actual non-audiobook tracks,
correctly hands-off.

**Decision: the relocate disjointness backstop PASSES.** The relocate op targets tracks by
AO book_file PID (already correct), and AO owns no music/podcast track, so a relocate can
never make a cross-type write. No blocker for arming relocate (P2) on this ground.

**Secondary finding (fail-safe, not fixed here): `isAudiobookITL` is unreliable as a genre
gate.** It misses `Audio Book`/`audio book` (checks the substring `"audiobook"` with no
space — 705 tracks) and every literary-genre audiobook (Science Fiction, Fantasy, Suspense,
…). For `GuardRebuildTarget` this is **fail-safe** — under-classifying audiobooks *inflates*
the non-audiobook count, making the "looks real" guard *more* likely to block, never less.
But it must **not** be used as a relocate/cleanup targeting filter, and "fixing" it (e.g.
adding the space variant) would *lower* the non-audiobook count and could weaken the rebuild
guard's threshold — so any such change must re-derive `GuardRebuildTarget`'s thresholds in
the same PR. Left as a documented follow-on.

## F6 — Bookmark / field-preservation BYTE-PROOF: relocate + remove preserve everything else

**Proven 2026-07-24** (`internal/itunes/itl_preserve_proof_test.go`
`TestITLPreservationByteProof`, env-gated `ITL_PRESERVE_PROOF_PATH`, run against a copy of
the real 32MB AO `.itl`). The concern (design §INV-F2 + memory): the audiobook resume
**bookmark is NOT parsed by the binary LE parser**, so it survives a write only if the write
path copies it through byte-for-byte — is that true, and does relocate/remove ever mutate any
other field of any other track? Proven the strongest way: a per-track **raw-byte** comparison
of the decompressed payload before vs after a real relocate + remove (raw bytes catch every
unparsed atom, not just parsed fields).

Ran a real relocate of 300 tracks (location changed to a longer path, exercising the
length-change writer path) + a real remove of 30 disjoint tracks, via the production
`UpdateMetadataLE` and `RemoveTracksByPIDLE` on the decompressed payload:

```
library: 97999 tracks parsed, 97999 mith blocks split
PROOF: relocated=300 (all non-location atoms byte-identical), removed=30, untouched-identical=97669
   (300 + 30 + 97669 == 97999 — exact partition, ZERO collateral mutation)
non-basic atoms preserved across relocated tracks: 0x08 Comment (209), 0x1B Sort Name (75),
   0x1E Content Advisory (47), 0x12 Sort Artist (1), 0x15 Content Rating (1),
   0x1F Content Description (1), 0x36 audio-data blob (300)
```

Per relocated track: the mith **header is byte-identical except the 4-byte totalLen** (offset
8), and **every mhod atom except the location pair (0x0D/0x0B) is byte-identical, in order**.
The audiobook **bookmark/resume position lives in the mith header** alongside play count,
rating, and dates — all covered by the header-identity assertion. Untouched tracks are whole-
block byte-identical; removed tracks are absent and no other track changed.

**Decision: the preservation claim is PROVEN across both layers that can touch track
records.** Two env-gated tests, run against the real library:
- `TestITLPreservationByteProof` — the **mutation layer** (`UpdateMetadataLE` +
  `RemoveTracksByPIDLE`): relocate changes ONLY the location pair, remove changes ONLY the
  targeted tracks; 97,669 untouched tracks byte-identical.
- `TestITLPreservationThroughEncode` — the **encode layer** (`WriteITLBytes` →
  `writeITLFile`: CRIT-3 header regeneration + recompress + re-encrypt): after a real
  relocate encoded through the full production encode path, 97,699 untouched tracks are still
  byte-identical and relocated tracks changed location-only. This proves header regeneration
  does not touch any track record.

Together these are the only two layers that transform bytes; the third SafeWriteITL layer (the
safety **contract**) is a read-only gate that never rewrites track records — and is separately
covered by F7. No field of any other track — parsed or unparsed, header or atom, including the
mith-header resume bookmark — is ever mutated. INV-F2 holds. Tests skip in CI (a synthetic
fixture would not exercise real bookmark/comment/advisory atoms).

## F7 — 🚧 P2 BLOCKER: the location-form guard rejects the entire live AO library

**Discovered 2026-07-24** while running the preservation proof through the full contract path
(`UpdateITLLocations → SafeWriteITL`, `TestITLRelocateContractStatus`). The write was
**REJECTED** by the `location-form` guard (`itl_safety_contract.go:562`) on **82,976 tracks**
whose 0x0B URL contains `.itunes-writeback/`, e.g.
`file://localhost/W:/audiobook-organizer/.itunes-writeback/iTunes%20Media/Audiobooks/…`.

**These are NOT leaks — they are the AO library's real media paths.** The AO writeback library
physically lives at `W:\audiobook-organizer\.itunes-writeback\iTunes Library.itl`, so iTunes'
media folder is `…\.itunes-writeback\iTunes Media\`, and every track legitimately points
there. The guard was written to catch a staging-dir path **leaking into the hands-off Original
library** (the "damaged-4" incident — `location_pair.go:24`, iTunes marks such a library
"(Damaged)"). But in the locked **hard-cutover** design (iTunes pointed AT the AO library,
whose own root is under `.itunes-writeback/`), that substring is unavoidable and correct.

**Consequence: the relocate op (P2's core write) cannot write the live library at all.** Every
`SafeWriteITL` call fails location-form; `Force` does not override it (it only relaxes the
bounded-delta guard, `itl_safe_write.go:138`). This is a hard P2 blocker, not a preservation
issue.

**Reconciliation options for P2 (owner decision needed):**
1. **Scope the staging-marker check to the write TARGET.** Reject `.itunes-writeback/` only
   when writing the **Original** library (`LibrarySet.PointedAt`/`ImportSource`), or only when
   the path's `.itunes-writeback/` root differs from the AO library's own root. When writing
   the AO library whose root legitimately contains it, the check must not fire. (Preferred —
   the 4-state config from P0 already carries the mode facts to gate this.)
2. Move the AO library + media out from under a `.itunes-writeback/`-named directory (physical
   relocation of ~all media — invasive, and re-triggers iTunes' own relocation bookkeeping).

Until reconciled, P2 relocate is **blocked**. See [[project_itunes_writeback_pathnorm_bug]].

## P0 status — DONE; F7 RESOLVED; all P2 primitives merged; only the cycle remains

P0 (F1-F6) complete; F7 uncovered **and resolved**. Merged 2026-07-24:

| Piece | PR | What it gives P2 |
|---|---|---|
| 4-state `LibrarySet` config | #2040 | Original/AO × .itl/.xml + PointedAt/ImportSource mode facts |
| Cleanup census → P3 measure-and-stop | #2041 | P3 is a guarded no-op; no bulk-removal machinery |
| Cross-type (F5) + preservation byte-proof (F6) | #2042 | Disjointness holds; relocate/remove proven to touch nothing else |
| Relocate oracle `VerifyRelocateWrite` | #2043 | Per-track raw-byte auto-rollback trigger |
| P1 `RefreshLibraryIdentity` + `PartitionedTrackCount` | #2044 | Delta-aware K13/K14 refresh; K14 partition count |
| F7 guard scope `ContractConfig.AllowedWritebackRoot` | #2045 | `SafeWriteITL` can now write the AO library (owner chose option 1) |

**P2 — relocate-only sync cycle (MVP end) — READY TO BUILD.** All prerequisites are in
`main`; nothing re-opens the safety questions. Compose:

1. **Read** the AO `.itl`; `RefreshLibraryIdentity` (pin PID + drift ceiling, #2044) →
   ExpectedIdentity for K13.
2. **Plan** the relocate: DB `book_file` locations vs the `.itl`'s 0x0D (the existing
   relocate op computes `[]ITLLocationUpdate`); 0 adds / 0 removes by construction.
3. **Write** via `SafeWriteITL` with
   `ContractConfig{AllowedWritebackRoot: <AO media root>, ExpectedIdentity: <refreshed>,
   ExpectedTrackCount: PartitionedTrackCount → planAudiobook + liveNonAudiobook (#2044),
   Force:false}` + `.bak` + tight bounded-delta capped at `len(LocationUpdates)`.
4. **Verify** with `VerifyRelocateWrite(before, after, relocatedPIDs)` (#2043) BEFORE the
   atomic rename commits.
5. **Commit or roll back:** oracle OK → atomic rename; any violation → restore `.bak` + alert.
   Single-flight lock; never concurrent with a manual relocate/pid-repair/cleanup.

Then P3 stays retired (measure-and-stop), P4 (import) waits on dedup-on-import, P5
(cutover/fallback tooling), P6 (2-way read-back preserve-only).
