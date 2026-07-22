<!-- file: docs/specs/2026-07-22-itunes-2way-sync-writeback-design.md -->
<!-- version: 0.1.0 -->
<!-- guid: 193a875e-d0ca-4bc5-b34f-6461e03a0edb -->
<!-- last-edited: 2026-07-22 -->

# iTunes 2-Way Sync Writeback — Design Spec (DRAFT)

> **STATUS: DRAFT for owner review. Design only — no code, no prod actions.**
> Shaped in the 2026-07-22 session after the `itl-diff` comparison exposed that the
> deployed writeback **regenerates** the library (lossy) instead of **editing** it.
> Open decisions are called out inline and collected at the end.

## 1. Problem

The deployed `rebuild-full` writeback (shipped 2026-07-22, PR #2032) **regenerates** the
iTunes library from our DB. Compared to the real library it is catastrophically lossy — not
corrupt (both pass all 10 `ITLSafetyContract` guards), but a different, much smaller library:

| | Real library | Our `rebuild-full` output | Δ |
|---|---|---|---|
| Tracks | **97,782** | 12,193 | −85,589 |
| Playlists | **356** | 14 | −342 |
| Track-list (decompressed) | 206 MB | 9.6 MB | −196 MB |
| Playlist-list (decompressed) | 27.7 MB | 0.16 MB | −27.5 MB |
| Persistent-ID overlap | — | **0** | disjoint |
| Safety audit | ✅ all pass | ✅ all pass | — |

Two independent losses stack:

1. **Scope collapse (8× fewer tracks).** We emit one track per DB-known iTunes audiobook
   (~12K). The real library holds every part/chapter file, plus music, podcasts, dupes, and
   years of accumulation (~85K of which we never emit).
2. **Field collapse (~2.6× leaner per track).** `ITLNewTrack` carries only 7 string fields
   (name/album/artist/genre/kind/location) + a few numerics. The real per-track record also
   holds **play count, rating, playback-position bookmark, date-added, last-played, skip
   count, sort fields, comments** — we emit none of them. Zero PID overlap means iTunes would
   treat our output as an entirely *new* library, not "your library, relocated."

### Requirement (owner, 2026-07-22)

> "Full 2-way sync for all playlists and non-audiobooks. For audiobooks we can rewrite the
> library so it matches the audiobook-organizer's audiobooks, but keep all fields where
> possible. Knowing if something has been played is the backbone of every playlist, so be
> careful not to remove those fields."

Decomposed:

- **Non-audiobook tracks (music/podcasts) + all 356 playlists:** preserved verbatim. A 2-way
  sync — whatever the user changed in iTunes stays and flows back.
- **Audiobook tracks:** reconciled to AO's canonical audiobook set (AO's dedup/organization
  wins), **carrying forward every existing field** — above all the play-state fields, because
  smart-playlist membership is computed from them.

## 2. Key insight — edit in place, never regenerate

The correct mechanism is the **inverse** of `rebuild-full`. Instead of building tracks from a
field struct (which drops everything not in the struct), take the **real library as the base**
and **surgically rewrite only what must change**, leaving every other byte intact. Fields we
don't even model (the audiobook playback bookmark) are preserved *because we never rebuild the
record*.

## 3. Primitives that already exist (feasibility is high)

| Primitive | Location | What it gives us |
|---|---|---|
| `UpdateITLLocations(inputPath, outputPath, []ITLLocationUpdate)` | `internal/itunes/itl.go:699` | Surgical in-place relocate. Matches tracks by persistent ID, rewrites **only** `0x0D`/`0x0B` location mhods, routes through `SafeWriteITL` (backup→validate→rollback), reports which PIDs actually matched (DL-5). Everything else — other mhods, other tracks, all playlists — untouched. |
| `IsAudiobook(track)` | `internal/itunes/parser.go:107` | Classifies audiobook vs music/podcast, so we scope edits to audiobooks only. |
| `ITLTrack` play-state parse | `internal/itunes/itl.go:61` | We already read `PlayCount`, `Rating`, `DateAdded`, `LastPlayDate`, `DateModified` — enough to reason about played-state and to verify preservation. |
| `SafeWriteITL` + `ITLSafetyContract` | `internal/itunes/` | Atomic write with the 10-guard contract incl. `no-new-dangling-refs`, `bounded-delta`, `library-identity`. |
| `AddTracksLE` | `internal/itunes/itl_le_mutate.go` | Fresh-track builder — used **only** for the never-in-iTunes add case. Now writes both `0x0D`+`0x0B` (fixed 2026-07-21). |
| `itl-diff` | `cmd/itl-diff` | Decrypt+inflate structural diff (track/playlist counts, per-container size inventory, per-track field diff, `--audit`). The verification harness for every phase. |

The **relocate cycle is therefore mostly a wiring job** on top of `UpdateITLLocations`. The
real design work is topology changes and playlist-ref integrity (§5).

## 4. Architecture — three operation classes

Base = the **current** `.itunes-writeback/iTunes Library.itl` (the library iTunes is actively
using). Each sync cycle reads it, applies only the audiobook deltas, and `SafeWriteITL`s it
back. Because the base is the live library, iTunes-side changes (new play counts, new
playlists, new music) are already present and survive untouched — that is the "2-way" property.

For each AO canonical audiobook, classify against the current library:

1. **Relocate (common case, lossless).** The book already exists as an iTunes audiobook track
   and only its file location changed (now points at the AO copy, `W:\audiobook-organizer\…`).
   → one `ITLLocationUpdate{PID, newLoc}`; batched through `UpdateITLLocations`. All fields,
   including the bookmark, preserved automatically.
2. **Add (only fresh-track case).** AO has an audiobook with no corresponding iTunes track
   (newly organized, never imported). → `AddTracksLE`. No play-state to preserve (there is
   none). Must also add it to the appropriate playlist(s) if AO models that.
3. **Remove + reconcile refs (topology change).** AO says an iTunes audiobook track is a
   duplicate/superseded/reassembled-away. → remove the track **and** repair every playlist that
   referenced it (remap to the surviving canonical track, or drop the entry) so
   `no-new-dangling-refs` passes.

Non-audiobook tracks are **never** in any of these sets — `IsAudiobook` gates membership, so
music/podcasts are structurally excluded from all three ops.

## 5. Hard problems (what the spec must actually solve)

### 5.1 Matching AO books ↔ existing iTunes audiobook tracks
Relocate/remove both require a reliable join from an AO book to the exact iTunes track record.
Candidate keys, in preference order:
- **Stored persistent ID** — if a book_file already carries the iTunes PID from import, this is
  exact. **[OPEN: do we persist the source track PID at import time? verify.]**
- **Current location** — match on the pre-relocate path the track still points at.
- **Fingerprint** — fall back to `AcoustIDFingerprint` / the reconciliation spec's signals for
  books whose PID/location drifted. Ties into
  [`2026-07-19-fingerprint-driven-reconciliation-design.md`](2026-07-19-fingerprint-driven-reconciliation-design.md).
A book that cannot be matched with confidence must **not** be force-relocated — it lands in
review (same fail-safe posture as the reconciliation loop).

### 5.2 Topology changes + playlist integrity
AO frequently changes audiobook *shape*: 39 shattered `Aces Abroad - Part NN` records → one
book; a reassembled anthology; a merged multi-part book. When the iTunes track set for a book
changes cardinality, any playlist (incl. smart-playlist static membership) referencing the old
track IDs must be remapped to the survivor or dropped. This is the one place we touch playlists
— and only to keep them valid, never to restructure them. The `no-new-dangling-refs` guard is
the backstop; the design must actively remap, not rely on the guard to catch mistakes.

### 5.3 Bookmark field preservation — verify, don't assume
`UpdateITLLocations` preserves unmodeled mhods by construction (it rewrites only location
bytes). But we must **prove** the audiobook playback-position bookmark survives a real
relocate: relocate a known-bookmarked track on a **sandbox clone**, re-parse, and confirm the
bookmark mhod is byte-identical. **[OPEN: identify the bookmark mhod type; extend `itl-diff` to
surface it.]**

### 5.4 2-way direction: iTunes → AO
"2-way" also implies iTunes-side facts flowing **back** into AO where relevant (e.g. play
counts / last-played informing AO, new user playlists noted). Minimum viable: preserve them in
the library (achieved by edit-in-place). Fuller sync (ingesting play-state into the DB) is a
**[OPEN: is read-back into AO in scope for v1, or preserve-only?]**

## 6. Phasing

- **P0 — Verify primitives (read-only + sandbox).** Confirm PID persistence at import (§5.1);
  identify + diff the bookmark mhod (§5.3); prove a single-track relocate on a sandbox clone
  preserves all fields via `itl-diff`. Gate: no field loss on a relocate.
- **P1 — Relocate-only sync (no topology).** Build the audiobook match + `UpdateITLLocations`
  batch for books whose only change is location. Ship behind a flag; validate on sandbox with
  `itl-diff --audit` (expect: tracks Δ0, playlists Δ0, only Location fields changed).
- **P2 — Add-path.** Never-in-iTunes AO books via `AddTracksLE` + playlist insertion.
- **P3 — Topology + playlist-ref remap.** Remove/merge with playlist integrity (§5.2). Highest
  risk; most adversarial testing.
- **P4 — iTunes→AO read-back** (if in scope per §5.4).

Each phase is sandbox-proven before prod, using the existing ZFS-clone sandbox (rebuild scripts
in infra-docs) and `itl-diff` as the acceptance oracle.

## 7. Safety / rollback

- Every write goes through `SafeWriteITL` (backup + full contract + auto-rollback). Never a
  direct `writeITLFile`.
- **Never touch `books/itunes/**`** — hands-off active library. The writeback target is only
  `.itunes-writeback/`.
- Blast-radius: the relocate path must **not** need `ForceContractConfig()` — a bounded relocate
  should pass `bounded-delta` on its own. If it doesn't, that's a signal the change set is
  wrong, not a reason to force.
- Acceptance oracle per cycle: `itl-diff --audit <pre> <post>` must show non-audiobook tracks
  Δ0, playlists Δ0 (except intentional ref remaps), and audiobook changes matching the intended
  op set exactly.

## 8. Open decisions (collected)

1. **§5.1** Do we persist the source iTunes persistent ID on `book_file` at import? (Determines
   whether matching is exact or fingerprint-fallback.)
2. **§5.3** Which mhod type carries the audiobook playback bookmark? Extend `itl-diff` to show
   it before P1.
3. **§5.4** Is iTunes→AO read-back (play-state into the DB) in v1 scope, or preserve-only?
4. **Base selection**: seed the first real cycle from the current `.itunes-writeback` (already
   iTunes-pointed) or re-seed from a fresh copy of the real 32 MB library? The 2 MB prototype
   must be discarded either way.
5. **Cadence/trigger**: manual op, or on-change after AO reconciliation settles?

## 9. Immediate consequence for the current prod state

The deployed 2 MB `.itunes-writeback/iTunes Library.itl` is a **lossy prototype** — audiobooks
only, no play-state, no music/podcasts, no user playlists. It is fine for confirming audiobook
paths resolve in the iTunes GUI, but it is **not** the target library and must not be treated as
authoritative. The real operation is P0–P3 above.
