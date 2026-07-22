<!-- file: docs/specs/2026-07-22-itunes-2way-sync-writeback-design.md -->
<!-- version: 0.2.0 -->
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

### 5.1 Matching AO books ↔ existing iTunes audiobook tracks — **per-FILE PID** (RESOLVED)
iTunes stores one track per audio file; a multi-part book = many tracks, each with its own
persistent ID. **We persist PID at both levels** (verified 2026-07-22): `Book.ITunesPersistentID`
(`internal/database/bookcore.go:54`) *and* `BookFile.ITunesPersistentID`
(`internal/database/bookfilecore.go:38`). The correct join is **per-file**: iterate a book's
`book_files` and match each file's iTunes track by that file's own PID.

> **⚠️ Bug in the current `rebuild`:** `ComputeITLDiff` (`rebuild.go:107`) matches only on the
> **book-level** `book.ITunesPersistentID` — one PID per book. For a 14-part book it would
> relocate one track and mark the other 13 part-tracks as "not in DB → remove". The safe
> relocate MUST operate at `book_file` granularity, keyed on `BookFile.ITunesPersistentID`.
> **[VERIFY: were per-file PIDs written *uniquely* per file, or the same book PID copied to all?
> owner unsure — audit a known multi-file book before P1.]**

Match key order per file:
1. **`BookFile.ITunesPersistentID`** — exact, primary key.
2. **Current location** — fallback for a file whose PID is missing/blank.
3. **Fingerprint** — `AcoustIDFingerprint` / reconciliation-spec signals for drifted files. Ties
   into [`2026-07-19-fingerprint-driven-reconciliation-design.md`](2026-07-19-fingerprint-driven-reconciliation-design.md).

A file that cannot be matched with confidence must **not** be force-relocated — it lands in
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

### 5.4 2-way direction: iTunes → AO — **preserve-only in v1** (RESOLVED)
"2-way" also implies iTunes-side facts flowing **back** into AO. **v1 is preserve-only** (owner,
2026-07-22): edit-in-place already keeps every iTunes-side field in the library for free; that is
the whole v1 requirement. Ingesting play-state (counts / last-played / bookmarks / new playlists)
back into the DB is deferred to a later phase (P4), not v1.

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
- **🚫 NEVER WRITE TO THE REAL iTunes LIBRARY (`books/itunes/**`) — hands-off, absolute (owner,
  2026-07-22).** The writeback target is *only* `.itunes-writeback/`. Reading from
  `books/itunes/` (e.g. to reseed) is fine; writing to it is never permitted. Recovery from a bad
  writeback is via ZFS snapshot, which is why bookmark preservation is trusted rather than
  proven up-front (§5.3) — but that trust applies only because the blast radius is confined to the
  disposable `.itunes-writeback` copy.
- Blast-radius: the relocate path must **not** need `ForceContractConfig()` — a bounded relocate
  should pass `bounded-delta` on its own. If it doesn't, that's a signal the change set is
  wrong, not a reason to force.
- Acceptance oracle per cycle: `itl-diff --audit <pre> <post>` must show non-audiobook tracks
  Δ0, playlists Δ0 (except intentional ref remaps), and audiobook changes matching the intended
  op set exactly.

## 8. Decisions (owner, 2026-07-22)

1. **§5.1 PID matching — RESOLVED: per-FILE PID.** Both levels are persisted;
   `BookFile.ITunesPersistentID` is the correct join. The current book-level match is a bug for
   multi-file books. Remaining verify: were per-file PIDs written uniquely per file? (owner
   unsure — audit a multi-file book in P0).
2. **§5.3 Bookmark preservation — RESOLVED: trust the primitive.** `UpdateITLLocations` rewrites
   only location bytes; recovery is via ZFS snapshot. No up-front byte-proof required — *conditional*
   on writes never leaving the disposable `.itunes-writeback` copy (§7).
3. **§5.4 Read-back — RESOLVED: preserve-only in v1.** DB ingestion of play-state deferred to P4.
4. **Base selection — RESOLVED (done 2026-07-22):** `.itunes-writeback` reseeded from the latest
   real library (`books/itunes/iTunes Library.itl`, 32 MB, 97,782 tracks / 356 playlists; read-only
   copy). The 2 MB prototype is backed up (`.prototype-2mb-bak-20260722`) and discarded. Future
   cycles edit this reseeded base in place.
5. **Cadence/trigger — RESOLVED: both.** Manual op first; auto-trigger after AO reconciliation
   settles, added behind a flag once the relocate path is proven safe on the sandbox.

### Critical caveat discovered while resolving these
Running the **existing** `rebuild` against the reseeded full library is **destructive**: it is
DB-authoritative (`rebuild.go:126`), so it would mark ~85,589 non-audiobook / unmatched tracks for
**removal** and shatter the 356 playlists. It must **not** be run against a full library. The safe
edit-in-place relocate (this spec) is the only writeback allowed post-reseed; until it exists, the
reseeded library is left as-is (iTunes shows the full library).

## 9. Immediate consequence for the current prod state

The deployed 2 MB `.itunes-writeback/iTunes Library.itl` is a **lossy prototype** — audiobooks
only, no play-state, no music/podcasts, no user playlists. It is fine for confirming audiobook
paths resolve in the iTunes GUI, but it is **not** the target library and must not be treated as
authoritative. The real operation is P0–P3 above.
