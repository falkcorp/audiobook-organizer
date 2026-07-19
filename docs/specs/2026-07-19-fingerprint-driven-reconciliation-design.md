<!-- file: docs/specs/2026-07-19-fingerprint-driven-reconciliation-design.md -->
<!-- version: 0.1.0 -->
<!-- guid: 3d2e23d9-f003-4d47-8c11-1b9e3ac3333b -->
<!-- last-edited: 2026-07-19 -->

# Fingerprint-Driven Library Reconciliation — Design Spec (DRAFT)

> **STATUS: DRAFT for owner review. Design only — no code, no prod actions.** Everything
> below was shaped in the 2026-07-19 design session; the "Verified facts" appendix is the
> read-only prod evidence gathered that day. Open decisions are called out inline and
> collected at the end. This supersedes the narrower TODO 50 bullets.

## 1. Problem

The library has three overlapping identity problems that today are handled by separate,
title/metadata-centric heuristics that misfire:

1. **Shattered books.** One audiobook exists as many separate one-file "book" records.
   Live example (verified): *Wild Cards IV: Aces Abroad* is **39 separate book records**,
   each a single `Aces Abroad - Part NN.mp3` in one on-disk folder, each with its own
   `version_group_id` — nothing groups them. Two flavors exist:
   - **DB-only splits** — files are correctly co-located on disk; only the DB grouping is
     wrong (the Aces Abroad case; root cause class = the iTunes `artist|album` grouping
     bug, fix #1528).
   - **Mis-imported copies** — some books were physically imported incorrectly into the
     organized (primary) library folder, so the primary copy itself is shattered/wrong.
2. **Duplicates.** The same book is present multiple times (e.g. an iTunes-imported copy
   plus an organized copy plus an un-imported source copy), with inconsistent
   title/author attribution, so title-only dedup leaves them split.
3. **Ambiguous identity.** Title/metadata is unreliable — near-titles ("the house" vs
   "the mouse"), editor-vs-author attribution, title-leak residue ("same file, one extra
   character"). Pure string matching cannot resolve these safely.

Underlying all three: **titles lie; the audio and the original assembled folders do not.**

## 2. Approach — three cross-constraining signals

Replace title-centric guessing with three independent identity signals, each covering the
others' blind spots:

| Signal | Answers | Source | Coverage today |
|---|---|---|---|
| **Acoustic fingerprint** | "Is this the *same audio*?" | per-file Chromaprint (`AcoustIDFingerprint`), compared via `WholeFileSimilarity` (edge-trimmed Hamming), scaled via the `fpidx` LSH index | **94% of files** (verified); `complete` even on the shattered fragments |
| **Original source folder** | "What is the *complete set* / ground truth?" | the assembled, unmodified source-download folders (each book = one folder with all its tracks) | on disk; **not yet scanned into the DB** (the one real gap) |
| **Whisper transcript** | "*Which title / identity*?" | existing transcripts, esp. the intro (usually states title/author/narrator); `intro_transcribe` op; transcription-match landed in #1734 | ~96.5% transcribed (~40% low-quality tail = TODO 48) |

Key property: **each signal degrades gracefully.** A book missing a signal simply won't
reach auto-resolve confidence — it lands in review, never a forced merge.

## 3. Orchestration — the convergence loop

The signals are run in an **iterative mutual-constraint refinement loop**, not as
one-shot independent tests. Each round, every signal's output tightens the others' inputs:

```
fingerprint set-match ─► tighter candidate grouping
        │                          │
        ▼                          ▼
  smaller Whisper            cleaner metadata
  title-candidate set ─────► match ─────► tighter next-round grouping
        │
        ▼
  sharper transcript answer (candidate set fed to Whisper as a decoding prior)
```

- **Why it converges:** a 2-way title choice is a far easier ASR problem than
  open-vocabulary transcription, so feeding the narrowed candidate set back into Whisper
  raises its accuracy, which narrows the set further — confidence climbs each pass.
- **Stopping rule:** iterate until confidence crosses a **near-certainty** threshold
  (→ auto-resolve) or stops improving (→ human review). No fixed pass count.
- **Safe by construction:** the loop cannot manufacture certainty from missing signals;
  under-determined books fall out to review.

## 4. Use-cases the loop serves

1. **Reassemble shattered books** — collapse the N fragment records into one book.
   Fingerprint-set containment against the assembled source folder (`fragments ⊆
   source_folder`) confirms the group is complete and pure; must fix **both** DB-only
   splits and mis-imported AO copies.
2. **Dedup on import** — the source-download roots are *valid import + organize sources*.
   On import, fingerprint-match each incoming book against the library and **merge into
   the existing primary** instead of creating a new duplicate.
3. **iTunes decommission** — owner invariant: *every book in the iTunes library matches a
   book somewhere else*. Fingerprint-reconcile each iTunes entry to its real source/AO
   copy so the iTunes folder can retire (iTunes then reads from
   `audiobook-organizer/.itunes-writeback`).
4. **Near-dupe confirm** — the "same file, one character different" title-leak dupes,
   auto-merged once fingerprints agree.

## 5. Where it slots into the existing engine

Reuse, don't rebuild (from the 2026-07-19 code investigation):

- Per-file acoustic signals already feed scoring (`exact_acoustid`, `lsh_acoustid` in
  `internal/dedup/collectors_acoustid.go`); the acoustic-confirm test extends them.
- Auto-resolve already exists and already accepts a whole-book-signature match as
  justification (`internal/dedup/unified/auto_resolve.go`), gated behind the
  `AutoResolveEnabled` kill-switch — the convergence loop's "near-certainty → resolve"
  hangs here.
- Split detection lives in `internal/dedup/split_book_detector.go` (today: same folder /
  same author / sequential `Part NN` — metadata only). Add the fingerprint-set-containment
  qualifier here; it's the natural home for reassembly.
- `WholeFileSimilarity` + the `fpidx` LSH index are the matching primitives; the
  source-folder reference is the new corpus they run against.

## 6. Safety, gates, and constraints

- **Keep the organized (AO) copy PRIMARY** — but repair mis-imported AO shattering.
- **Never mutate the active iTunes tree.** Read-only at most. It's transitional (retiring
  to `.itunes-writeback`), but until then it's live third-party state. **Blocker:** the
  `ProtectedPaths` guard (`config.go:631`, "iTunes media paths belong here") could **not
  be confirmed populated on prod** on 2026-07-19 — the scanner also only avoids iTunes
  because iTunes isn't a configured scan root (config-based, not a hard skip).
  **Action: verify/populate `ProtectedPaths` before any write op that could reach iTunes.**
- **Scanning a source root auto-organizes by default** (`AutoOrganize` defaults `true`;
  scanner fires the organize hook on non-root folders). Since sources are import-valid,
  this is acceptable *if* dedup-on-import (use-case 2) is in place first; otherwise it mass-
  duplicates. **Order matters: dedup-on-import before bulk source ingest.**
- **All auto-resolve stays human-gated / dry-run-first**, consistent with the prod-apply
  review gate. The review queue is where non-certain outcomes land.
- **Rollback:** every phase is independently revertible; merges go through the existing
  merge path (soft-delete losers, reversible), nothing hard-deletes files.

## 7. Phased plan (each phase independently shippable + gated)

- **P0 — Measure & verify (read-only).** Confirm `ProtectedPaths`; corrected coverage
  already done (94%); enumerate the shattered-book population and the DB-vs-source gap.
- **P1 — Acoustic-confirm signal.** Add whole-book/per-file fingerprint closeness as a
  *confirmer* on existing candidates; strengthen the auto-resolve gate. Reach = wherever
  both sides are fingerprinted (most of the 94%).
- **P2 — Whisper candidate-prior.** Feed the narrowed candidate set into transcription
  matching as a decoding prior; use the intro transcript to disambiguate near-titles.
- **P3 — Source-ground-truth reassembly.** Scan the source-download roots as an indexed
  corpus; fingerprint-set-containment reassembly of shattered books (incl. mis-imported
  AO). This is the reassembly capability that's entirely absent today.
- **P4 — Dedup on import.** Fingerprint-match incoming books → merge into existing primary
  rather than duplicating. Prerequisite for safe bulk source ingest.
- **P5 — iTunes decommission.** Reconcile every iTunes entry to its match elsewhere; retire
  the folder to `.itunes-writeback`.

The convergence loop (§3) is the control layer that runs across P1–P3 signals; P4/P5 are
applications of the same machinery.

## 8. Open decisions (for owner)

1. **Near-certainty threshold** for auto-resolve vs review — and does it differ per
   use-case (reassembly vs cross-copy dedup)?
2. **Bulk source ingest ordering** — confirm P4 (dedup-on-import) ships before pointing a
   scan at the source roots, to avoid mass-duplication.
3. **iTunes decommission trigger** — manual per-batch, or automatic once a match is
   confirmed at ≥ threshold?
4. **The `duration≈4` anomaly** on the Aces Abroad fragment books — real data bug to fix
   in parallel, or display artifact? (unconfirmed)

## 9. Verified facts (2026-07-19, read-only prod)

- File-level raw-fingerprint coverage: **296,010 / 315,013 = 94%**; zero-duration
  fingerprinted rows = 0 (the deprecated-Seg0 over-count worry is moot on prod).
- *Wild Cards IV: Aces Abroad* = 39 separate 1-file book records, one on-disk folder,
  39 distinct version-groups, all `fingerprint_status: complete`.
- Assembled source folders exist on disk (e.g. *Wild Cards I* = 23 tracks incl.
  `17 Strings.mp3` in one folder) but are **not** a configured scan path (≈71 files tied
  to the source root in the DB; source-title search = 0 hits).
- `fpcalc` runs on the server and produces usable fingerprints for source files.
- Configured scan/library roots today: the organized library root + the iTunes media dir;
  the source-download roots are not among them.
