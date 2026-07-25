<!-- file: docs/specs/2026-07-25-itunes-2way-sync-phase2-metadata-design.md -->
<!-- version: 1.0.0 -->
<!-- guid: ac62187b-29cd-4132-9ea0-a8fec4725e6c -->
<!-- last-edited: 2026-07-25 -->

# iTunes 2-Way Sync — Phase 2: Bidirectional Metadata Sync (Design)

**Status:** DESIGN — all owner decisions RESOLVED 2026-07-25 (§2, §5.5). Design-first,
dry-run-first. NO write path is built until an F6-style byte-proof passes on the real
library; NO broad "AO-wins" apply runs until the read-back watcher exists (so the
overwrite is lossless).

**Builds on:** the shipped relocate MVP (P0–P2, PRs #2041–#2050 — location-only sync
cycle with quiescence gate, contract, oracle, `.bak`/auto-rollback, playlist
preservation). This phase (a) expands the write from location-only to **all AO-owned
audiobook metadata**, and (b) adds an **iTunes→AO read-back watcher** so AO can stay
authoritative without destroying iTunes-side edits.

**Related:** `docs/specs/2026-07-23-itunes-2way-sync-system-design.md`;
`internal/itunes/relocate_sync_cycle.go`, `relocate_oracle.go`,
`itl_le_metadata_update.go`, `rebuild.go`, `import.go`.

---

## 1. What already exists (mechanism is done + proven safe)

- **`UpdateMetadataLE`** (`itl_le_metadata_update.go`) already writes every field: Name
  (0x02), Album (0x03), Artist (0x04), Genre (0x05), Kind (0x06), Composer (0x0C),
  Location (0x0D/0x0B). It copies the **mith header verbatim** (only patches totalLen at
  offset 8), leaves every **non-targeted mhod** untouched, and treats **empty string as
  "leave unchanged"** (can set/overwrite, cannot blank).
- **Play-state lives entirely in the mith header** (verified in `itl_le.go`): PlayCount
  `base+76`, Rating `base+108`, DateAdded `base+120`, resume bookmark (unparsed header
  field). None are mhods → preserving the header verbatim preserves ALL play-state.
- **`buildNewTrackFromBook`** (`rebuild.go:201`) is the canonical DB→iTunes field mapping.
- **`import.go:387`+** is the canonical iTunes→AO field mapping (used by the importer) —
  reused by the read-back path.

**New code this phase adds:** `ComputeMetadataOps` (planner), a **metadata oracle**
(generalized `relocateTrackDelta`), a metadata read-back path (iTunes→AO), a persistent
**settle-gated watcher**, cmd modes (`--metadata-dry-run` / apply), and an F6-style
byte-proof.

---

## 2. Field-ownership model (RESOLVED)

Per book_file / track:

| iTunes atom      | DB source                                      | Ownership |
|------------------|------------------------------------------------|-----------|
| Name (0x02)      | **`BookFile.Title`, fallback `Book.Title`** (D2) | AO-authoritative |
| Album (0x03)     | **`Book.Title`** (D1 — groups a book's chapters) | AO-authoritative |
| Artist (0x04)    | resolved author name                           | AO-authoritative |
| Genre (0x05)     | `Book.Genre` or `"Audiobook"`                  | AO-authoritative |
| Composer (0x0C)  | narrator                                       | AO-authoritative |
| Kind (0x06)      | file-format-derived                            | **LEAVE ALONE** |
| Location (0x0D/0x0B) | canonical WinPath                          | AO (shipped, P2) |
| **mith header**  | PlayCount / Rating / Dates / **resume bookmark** | **iTunes-owned — NEVER written** |
| any other mhod   | sort keys, comments, grouping                  | **iTunes-owned — untouched** |

**Owner decisions (RESOLVED 2026-07-25):**
- **D1 — Album = `Book.Title`.** Keep rebuild semantics; a book's chapters group as one
  album; Series is NOT mapped to Album; no library-wide regroup.
- **D2 — Name = `BookFile.Title`, fallback `Book.Title`.** Preserves/owns per-chapter
  labels; single-file books ≡ `Book.Title`.
- **D3 — Bidirectional (read-back-then-AO-wins).** AO stays authoritative and overwrites,
  but a read-back watcher captures iTunes-side edits into AO first (10-min settle window),
  so the overwrite is lossless. Owner accepts the residual race (edit in iTunes, then
  immediately make a conflicting AO edit inside the window → AO wins). Design in §5.
- **D4 — Empty DB fields never blank iTunes.** `UpdateMetadataLE` leaves iTunes unchanged
  when the DB value is empty; no way to intentionally clear a field in v1.

---

## 3. The metadata oracle (safety redefinition)

The relocate oracle asserts *"only 0x0D/0x0B changed."* The metadata oracle generalizes to
a **per-track allowlist**. For each planned track, given `allowedTypes` = the atom types the
plan intended to change on THAT track (⊆ {0x02,0x03,0x04,0x05,0x0C,0x0D,0x0B}):

1. **mith header byte-identical** — pins PlayCount / Rating / Dates / resume bookmark.
2. **Only allowlisted mhods may differ** — every mhod whose type ∉ `allowedTypes` is
   byte-identical (catches sort keys, comments, grouping, and any unparsed atom).
3. **Every non-planned track byte-identical** (0 adds / 0 removes).
4. **Playlist-list section (msdh type 2) byte-identical** — reuse the #2049 assertion.

Any violation → auto-rollback from the `.bak`. New `metadataTrackDelta(before, after,
allowedTypes)` replaces the fixed 0x0D/0x0B set with the per-track allowlist; relocate
becomes the special case `allowedTypes = {0x0D,0x0B}`.

**F6-style byte-proof (gate before ANY apply):** on the real 97,999-track library, plan →
in-memory apply → assert per-track header-identity, only-allowlisted-mhods-changed, all other
tracks/atoms identical, playlists identical. Empirical proof play-state survives; mirrors
`itl_preserve_proof_test.go`.

---

## 4. Write-back plan (DB→iTunes)

Same shape as the shipped relocate cycle (guards/quiescence/rollback reused):

1. `ComputeMetadataOps(store, itl, mappings, ownership)` → `ITLOperationSet.MetadataUpdates`
   (+ per-track allowlist), mirroring `buildNewTrackFromBook` + the D1/D2/D4 rules.
2. `VerifyMetadataWrite` = generalized `VerifyRelocateWrite` (§3).
3. Cmd: `--metadata-dry-run` (plan + in-memory oracle, NO write), then a gated apply mode
   (Apply=true, same SafeWriteITL contract + quiescence gate + `.bak` + auto-rollback).
4. Byte-proof (§3) green on the real library BEFORE first apply.
5. Live dry-run → review per-field diff counts → gated apply, starting small-batch (like the
   relocate first-write).

**Reused unchanged:** quiescence gate, single-flight lock, SafeWriteITL contract (identity +
magnitude + F7 scope + pre-rename SHA re-verify), post-commit re-verify + auto-rollback,
playlist-preservation assertion, ZFS snapshot + `.bak`.

---

## 5. Read-back watcher (D3 — the scope expansion)

**Goal:** make "AO wins" lossless. Before AO overwrites an AO-owned field in iTunes, capture
any iTunes-side edit to that field back into the AO DB, so the write-back re-asserts a value
AO already agrees with (a no-op), never destroying a change.

### 5.1 Flow (per cycle)
```
persistent watcher on .itunes-writeback/iTunes Library.itl  (mtime + SHA)
  │  change detected
  ▼
settle gate: SHA stable for 10 min  (reuse FileActivityLibraryCheck)
  │  AND the change is NOT AO's own write (§5.3)  ← HARD REQUIREMENT
  ▼
READ-BACK: parse AO-owned metadata from the .itl, update AO DB for matching PIDs
  │  (reuse import.go:387+ mapping, scoped to AO-owned metadata — NOT play-state)
  ▼
WRITE-BACK: the normal DB→iTunes metadata sync (§4). Now mostly no-ops; AO re-asserts.
```

### 5.2 Read-back scope (SAFETY)
- Read back **only AO-owned metadata**: Name→`BookFile.Title`, Album/Title, Artist→author,
  Genre, Composer→narrator.
- **NEVER read iTunes play-state INTO an AO-owned field.** Play count / rating / bookmark /
  dates keep their own `ITunes*` columns (populated by the existing importer) and are not
  part of this read-back (preserves locked system-design decision #2: play-state = preserve-only).
- Read-back is an **UPDATE of existing books by PID**, never a create (new-book import is P4,
  blocked on dedup-on-import). PIDs with no matching AO book are left for P4.

### 5.3 Oscillation / attribution — the critical safeguard (owner-emphasized)
Every AO write bumps the `.itl` SHA, so a naive watcher would treat AO's OWN write as an
"iTunes edit" and ping-pong forever. **The watcher must never count AO's own writes, even if
something else forces the write.** Mechanism:

- After every AO write, record the **exact SHA (and mtime) AO produced** in a durable
  state file (or the identity sidecar).
- The watcher **ignores any observed SHA that matches an AO-produced SHA.** Only a SHA AO did
  NOT produce is a genuine iTunes edit.
- Belt-and-suspenders: read-back never runs while the AO single-flight write-lock is held,
  and AO keeps a short ring of its recent written-SHAs (so a delayed filesystem event for an
  older AO write is still attributed to AO, not mistaken for an iTunes edit).

### 5.4 Source: `.itl` (not `.xml`)
Metadata fields are parseable from the binary `.itl` via `parseMithLE`, and the `.itl` is
always present. `.xml` is only written when iTunes shares it and matters mainly for playlist
classification — treat as an optional future cross-check.

### 5.5 Owner decisions (RESOLVED 2026-07-25)
- **Settle window N = 10 minutes.**
- **Trigger = persistent background watcher** inside the running service (auto-fires the
  cycle whenever the `.itl` settles after a non-AO change).
- **Conflict logging = yes** — when read-back changes an AO field (iTunes disagreed), log it
  (start/skip/complete per project logging standard) so drift is visible, not silent.

---

## 6. Phasing (design-first, dry-run-first)

1. **P2a — write-back (dry-run only).** `ComputeMetadataOps` + `VerifyMetadataWrite` +
   `--metadata-dry-run` + byte-proof. No write path armed. Reviewable per-field diff on the
   live library. (Dry-run can't clobber anything, so this is safe to build first.)
2. **P2b — read-back watcher.** Persistent settle-gated watcher + iTunes→AO metadata read-back
   (reusing import mapping) + the SHA-attribution safeguard (§5.3) + conflict logging.
3. **P2c — gated write-back apply.** Only after P2a byte-proof is green AND P2b read-back
   exists (so AO-wins is lossless): the `--metadata-apply` cmd mode + a small-batch live apply,
   like the relocate first-write.

---

## 7. Risks / non-goals

- **Not** touching play-state — enforced by header-identity + byte-proof + read-back scope.
- **Not** touching playlists — enforced by the #2049 assertion.
- **Not** blanking fields — `UpdateMetadataLE` can't (empty = no-op).
- **Not** an import path — updates only tracks that already exist (have a PID); never-imported
  books are P4 (blocked on dedup-on-import).
- **Main hazard = oscillation** (§5.3) — the SHA-attribution safeguard is mandatory, not
  optional; without it the watcher ping-pongs on its own writes.
