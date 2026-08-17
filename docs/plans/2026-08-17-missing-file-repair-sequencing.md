<!-- file: docs/plans/2026-08-17-missing-file-repair-sequencing.md -->
<!-- version: 1.1.0 -->
<!-- guid: 3c9e5a71-8d24-4b16-9f03-6ae2b7d5c481 -->
<!-- last-edited: 2026-08-17 -->

# Missing-file repair — recommended sequencing

Evidence base: [`docs/audits/2026-08-17-missing-file-audit-full-population.md`](../audits/2026-08-17-missing-file-audit-full-population.md).
Nothing here has been executed. The delete apply remains blocked.

## The shape of the problem, in one table

| population | rows | books | disposition |
|---|---|---|---|
| missing rows, total | 71,954 | — | — |
| …in **repairable** books (the delete plan) | 25,677 | 1,386 | mostly recoverable — do NOT delete yet |
| …in **fully-broken** books (skipped by the repair) | 46,277 | 16,265 | the books that will not load |

The delete op addresses the smaller share and completes **zero** unloadable books.

## Principle: never write a repoint you have not verified

The existing `filename` method proposes 8,128 relinks on the strength of "this
basename was unique in the index" — uniqueness is not identity. Every phase below
writes only after a candidate passes the same gate.

**The verification gate** (all CPU, no GPU, in cost order):

1. `os.Stat` the candidate — must exist.
2. `file_size` **within tolerance** of the stored value. NOT equality: measured
   deltas of 1,066 B and 65 B on files of 110–413 MB are tag rewrites after the
   size was recorded. Equality rejects correct matches.
3. `duration` via ffprobe — corroborates, and a tag write does not change it.
4. `file_hash` / fingerprint / transcript **only when present** — decisive but
   rare (2.2% / 0% / 0.4%), so a bonus, never the gate.

Signals 1–3 are available on ~100% of rows. That is the whole reason this is
buildable today.

### 🔻 Correction: fingerprint coverage is UNMEASURED, not 0%

An earlier draft reported AcoustID coverage as 0% on broken rows. **That was an
instrument error.** `GET /audiobooks/:id/files`
(`internal/server/handlers/audiobooks/handler_files.go:146`) serialises
`acoustid_seg0..6` but **never** emits `AcoustIDFingerprint` — the whole-file
chromaprint that `store.go:38-42` calls "preferred over the segment fields". The
probe was blind by construction. (Control: `AcoustIDSeg0` *is* emitted, so the
grep discriminates.) `/dedup/stats` shows a live `acoustid` candidate layer, so
fingerprints demonstrably exist.

**Library-wide baseline** (150 books across 6 offsets, 2,395 rows) for the
signals the API *does* expose:

| signal | coverage |
|---|---|
| `file_size` | 99.4% |
| `duration` | 93.4% |
| `itunes_persistent_id` | 8.5% |
| `file_hash` (SHA-256) | 7.3% |
| `original_file_hash` | 7.3% |
| `intro_transcription` | 4.1% |
| `transcribed_title` | 2.8% |
| `post_metadata_hash` | 0.1% |
| `acoustid_fingerprint` | **not exposed — unmeasured** |

These are the *right* signals: where present, a SHA-256 or a chromaprint settles
identity outright. At under 10% they cannot be the gate, but they are a decisive
top tier when they fire — hence their position in the ladder above.

### Repointing must keep derived data honest

A repoint changes which file a row points at. Every generated signal on that row
— `file_hash`, `original_file_hash`, `post_metadata_hash`, `AcoustIDFingerprint`,
`AcoustIDSeg0..6`, `IntroTranscription` and the parsed `Transcribed*` fields —
was computed from the **old** path. After a repoint each is in one of two states:

- **Corroborating** — it matches the candidate, which is the strongest possible
  proof the repoint is right. Use it as the gate when present.
- **Stale** — it describes a file this row no longer points at. It must be
  cleared or recomputed, never silently carried forward.

Carrying a stale hash forward is worse than having none: it looks like evidence.
Any repoint implementation must decide this per field, explicitly.

### Fold the coverage census into Phase 1

Phase 1 already walks all 532,296 rows. Have the same pass tally which signals
each row carries. One sweep then answers, for the real population rather than a
sample: how many missing rows have a hash, a fingerprint, a transcript — and
therefore how much of the repair can be adjudicated outright instead of inferred.

## Phase 0 — do not delete (in force now)

No `{"apply": true}`. 24/24 sampled delete candidates have bytes on disk.

## Phase 1 — make the audit persist what it already computes ⭐ enabling step

`maintenance.missing-file-audit` stats all 532,296 paths in ~168 s and discards
the per-row verdict. Persist it.

Two things fall out at once:

- Fixes the stale `book_file.missing` / `file_exists` columns, which today report
  `false` for rows whose paths provably do not exist.
- Gives a **queryable set of missing rows**. Right now nothing can enumerate the
  71,954 rows without re-running a full stat sweep, which is why every measurement
  in the audit is a 60- or 200-row sample. Every phase below is easier with this.

Do this first even though it fixes nothing user-visible. It is the difference
between working from samples and working from the population.

## Phase 2 — the track-slash population (highest confidence)

Rows like `…/Zero History - 70/131.mp3` where the `/` in `{track}/{total_tracks}`
became a path separator. Derive the flat `{title} - {track:02d}.{ext}` name, run
the gate, write.

- Proven **101/101** in the audit sample and **24/24** on actual delete candidates.
- `repair-missing-files` misses this shape entirely — all five of its tiers fail
  (§4 of the audit). This needs building; it cannot be wired up.
- ⚠️ The naive transform does **not** work: `SanitizePathComponent` maps `/` → space,
  giving `Zero History - 70 131.mp3`. The target comes from the *new* naming
  default, not from collapsing the old string.

Ship dry-run-first, sample the plan by hand, then apply.

## Phase 3 — books whose own `file_path` already points at real audio

12 of 60 sampled fully-broken books record a correct path to an existing file;
only the `book_file` row disagrees. These are books that will not load where the
answer is already in the database.

Cheapest real user-visible win in this document. Same gate applies.

## Phase 4 — gate the existing `filename` method (or disable it)

8,128 proposed relinks currently write with no same-book check. Either put them
behind the Phase-1 gate or turn the tier off until it is gated. Do not run
`repair-missing-files` for real before this.

The asymmetry is the tell: the multi-match branch already narrows by parent dir
and author, so the code knows a bare basename is insufficient — it just skips
that knowledge when the match happens to be unique.

## Phase 5 — the iTunes author-directory population (riskiest, do last)

43 of 60 sampled fully-broken books resolve to an *author* folder holding
hundreds of files (Jim Butcher 1,033; Stephen King 1,555). Recoverable in
principle, but selecting the right file needs title matching, which is exactly
where a wrong pick is silent. Highest ratio of risk to reward — last, and only
with the full gate plus `duration`.

The iTunes tree is **hands-off for writes**; this phase changes DB rows only.

## Phase 6 — only now, delete what is left

Scoped to the set Phase 1 classified as genuinely dead (no candidate found by any
phase). Requirements:

- Read `CappedAt` explicitly. The default `max_deletes` is 20,000 against a true
  plan of 25,677 — a capped run reports success while leaving 22% behind.
- Re-run the audit immediately before, so the plan is not stale.

## Cross-cutting constraints

- 🔴 **Never apply during a `library.scan`** — a running scan clobbers applied
  metadata, and the scheduler fires unattended every 6 h
  (`scheduled.library_scan`, 360 min). Disable it for the duration or work well
  clear of the boundary.
- **Ownership is unassigned.** The code lands in `internal/plugins/maintenance/`,
  which neither session working this repo on 2026-08-17 owns.
- **Per-file Whisper transcription is explicitly LOW priority** and does not gate
  any phase here — fingerprint coverage on broken rows is 0% and transcription
  0.4%, so neither can adjudicate today. It improves *future* matching, which is
  an argument for doing it eventually, not now.

## Measure this before starting

**How many of the 25,677 delete candidates are also relinkable?** That is the
size of the data loss the blocked apply would have caused, and it decides how
urgent Phase 2 is relative to the rest. It is read-only and needs Phase 1's
queryable row set — or a one-off join of the two dry-run outputs.
