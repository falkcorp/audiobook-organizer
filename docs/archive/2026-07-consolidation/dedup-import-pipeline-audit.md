<!-- file: docs/dedup-import-pipeline-audit.md -->
<!-- version: 1.0.0 -->
<!-- guid: d95f6a1c-45d5-4c5d-b9c7-0b479ae25c0a -->
<!-- last-edited: 2026-06-21 -->

# Dedup + Import Pipeline Audit — Recurrence Prevention & Optimization

**Date:** 2026-06-21 · **Author:** agent-driven audit (post PR #1548 shattered-book heal)
**Scope:** `internal/dedup/`, `internal/plugins/dedup/`, `internal/scanner/`,
`internal/itunes/service/`, plus the `maintenance.fs-regroup-xml` apply path.

> Companion to `.claude/notes/shattered-books-inventory.md` (the pre-heal root-cause
> inventory) and memory `project_itunes_grouping_key`.

---

## 0. Executive summary

The shattered-book heal (PR #1548) removed 20,247 chapter-junk records and cut the
exact-layer dedup backlog from 380,515 → 10,859 candidates. This audit answers the two
**recurrence-prevention** questions and surfaces the scaling problems that made the
explosion expensive.

**The two prevention findings (load-bearing):**

1. **Import side — the shatter is _cross-directory_.** `groupFilesIntoBooks` runs
   per-leaf-directory, so a per-chapter-subdir layout yields one 1-file book per chapter.
   **Changing its grouping key to "album" fixes nothing** — the album-equal files live in
   separate sibling dirs and never enter the same `files` slice. The fix must live one
   level up, in `ScanDirectoryParallel`, as a sibling-coalescing post-pass.

2. **Dedup side — the exact emitters have _no same-dir gate at emission_.** `checkExactTitle`
   and `checkDurationMatch` can re-create chapter cross-pairs at any time; the only
   folder-aware suppression today is reactive (`PairEligibility` skips re-scoring but does
   not delete; `PurgeStaleCandidates` deletes but is a manual cleanup op). A future
   shatter (or the still-unfixed title-leak importer bug) regenerates the explosion.

**The residual (10,859) is four populations, not one** — a blanket purge would destroy the
genuine-duplicate review signal. See §3.

---

## 1. Import pipeline (scanner + iTunes importer)

### 1.1 ROOT CAUSE of the shatter — cross-directory (P0)

`internal/scanner/scanner.go`:
- `ScanDirectoryParallel` (`:427-468`) walks the tree, collects each directory into `dirs`
  (`:364-419`), then for **each** directory spawns a goroutine that `os.ReadDir`s only the
  files **directly in that dir** (subdirs skipped, `:443-445`) and calls
  `groupFilesIntoBooks(audioFiles)` **once per directory** (`:460`).
- `groupFilesIntoBooks` (`:1514`) early-returns `if len(files) <= 1` (`:1515`).

For the shattered layout `Author/<Book>/<Book> - N/47.mp3`, each `<Book> - N/` dir holds
exactly **one** file → 1-element slice → early return → **one standalone Book per chapter**.

`groupFilesIntoBooks`'s internal album grouping (`:1561-1647`) only fires on multiple files
**in one folder** — it solves the opposite problem ("mixed albums in one folder"), so
album-keying it is a **no-op for this bug**.

### 1.2 PREVENTION — sibling-coalescing post-pass in `ScanDirectoryParallel` (P0)

PR #1548 made this newly feasible: the `album` tag + real track/disc are now captured per
file, giving a cross-directory merge key.

**Recommended algorithm (post-pass, smaller blast radius than re-architecting the walk):**

```
After per-dir goroutines produce `books`, run a serial post-pass:
1. Candidates = single-file Books whose parent dir is a child of a common grandparent
   (sibling chapter dirs).
2. Group candidates by (grandparent_dir, normalized album).
   - album empty/missing → DO NOT merge by album. Fall back to filename-sequence
     detection only (a sibling-dir variant of DetectMultiFileGroup; the `<Book> - N`
     dir names already match extractSeqNumber patterns). NEVER derive the key from the
     parent-folder name alone (recreates the importer CONS-17b "every GRRM book →
     'George R. R. Martin'" bug).
3. Over-merge guard: require a ≥75% album quorum (matches DetectMultiFileGroup's
   TagQuorum) AND the importer's trackNumbersCleanDistinct check (importer.go:930):
   merge only when per-file track numbers form a clean DISTINCT sequence. Repeated/zero
   track numbers ⇒ leave split (signature of distinct books sharing a generic album).
4. Merge survivors into one Book{ SegmentFiles: [...sorted by disc,track...] }.
5. Gate behind a config flag + dry-run (like other risky scanner behaviors).
```

**Reuse, don't reinvent:** the importer already solved the identical over-merge risk —
`groupTracksByAlbum` (`importer.go:867`), `albumGroupKey` (`:913`),
`trackNumbersCleanDistinct` (`:930`), `splitOverMergedGroup` (`:950`). Lift that pattern.

> **Doc correction:** memory's "(Album, Artist) ONLY" identity key is **stale** for the
> importer. Post-CONS-FRAG the importer keys **album-only** when album is present
> (`albumGroupKey:914-916`), keeping artist only as a guard for the no-album fallback.
> Update memory `project_itunes_grouping_key` accordingly.

### 1.3 Per-file tag read I/O cost (#1548) (P1)

`createBookFilesForBook` (`scanner.go:1229-1331`) now does, per segment file:
`metadata.ExtractMetadata` (`:1294`, full open+parse) **plus** `ComputeFileHash`
(`:1309`, open+read up to 20 MB). The grouping phase also opens each file
(`quickReadMultiFileInfo :1534`, `quickReadAlbum :1564/:1591`) → **~3 opens/file** across a
full scan. On a 35K-file scan the hash reads dominate (hundreds of GB read).

**Fix — deduplicate opens, not batch:** route segment reads through `ProcessFile`
(`process_file.go:41`, one open → meta+mediainfo+hash) and reuse grouping-phase tags in
`createBookFilesForBook`. Net ~3 → ~1 open/file. Directory-level parallelism already exists.

### 1.4 Grouping gates (P2)

- `DetectMultiFileGroup` (`scanner.go:1531`): N≥3, sequential naming, ≥75% tag quorum.
  No duration gate.
- `consolidateChapterGroups` (`chapter_consolidation.go:44-143`, no-album fallback): has a
  duration gate (`:108-128`) — only consolidates short files.
- These gates are filename/duration-based and **per-directory**; the cross-dir post-pass
  should add the importer's track-number-distinctness gate.

---

## 2. Dedup pipeline

### 2.1 PREVENTION — same-dir gate at emission (P0)

`internal/dedup/engine.go`:
- All exact emitters persist via `upsertExactCandidate` (`:1160`); its only guard is
  `isNonPrimaryVersion` (`:1161`). **No same-folder suppression.**
- `checkExactTitle` (`:902`) + `checkDurationMatch` (`:1014`) are the two that cross-pair
  chapters (shared leaked title / similar duration). The same-dir signal is available at
  emit time (`book.FilePath` → `filepath.Dir`) and is already used elsewhere
  (`findSimilarBooks :1394`, `eligibility.go:106`, `PurgeStaleCandidates :2280`).
- The unified re-score pass loads pending pairs (`:407`), and the suppressible branch at
  **`:452-457` is `slog.Debug` + `continue` — no delete.** So same-dir pairs persist.

**Fix (two complementary):**
- **(a) Preventive, targeted (recommended):** add a same-parent-dir guard to
  `checkExactTitle` and `checkDurationMatch` specifically. **Do NOT** add a blanket guard
  in `upsertExactCandidate` — it also routes file-hash/ISBN/metadata-hash, where a same-dir
  pair can be a *legitimate* duplicate (two identical files in one folder). If a single
  chokepoint is preferred, make it **layer-aware** (suppress same-dir only for exact
  title/duration).
- **(b) Backstop:** at `:452-457`, change `continue` → `DeleteCandidate(c.ID)` then
  `continue`, so the unified pass deletes any suppressible pair from any emitter (catches
  future emitters + LSH/AcoustID chapter pairs too).

### 2.2 O(N·P) — unified pass reloads the full pending table per book (P1)

`runUnifiedScoringForBook` (`:407-410`) loads **all** pending candidates per book (no
per-book/entity filter), then linearly scans them (`:415-427`). Full scan = O(N·P);
at N=29,308, P≈10,859 ≈ **318M row-scans/scan**; during an explosion P grows → effectively
quadratic. **Fix:** add an entity-ID predicate to `CandidateFilter` (or a
`book:dedup:cand:<id>` index) so each book loads only its own pairs → O(N·K).
*(Verify the candidate-store API supports an entity filter before implementing.)*

### 2.3 N+1 — book-only collectors re-run inside the per-candidate loop (P1)

`engine.go:444-554`: `CollectExactFileHash` (`:463`), `CollectISBNASIN` (`:473`),
`CollectMetaSrcHash` (`:482`), `CollectDuration` (`:521`) depend only on `book` but run
**inside** the `for candID` loop. `CollectISBNASIN` (`collectors_exact.go:148`) does a full
O(N) `GetAllBooks` batch scan → O(K·N) per book. **Fix:** hoist all four out of the loop,
build `map[otherID][]Signal` once per book.

### 2.4 Per-author O(M²) emitters (P1/P2)

`checkExactTitle` (`:913` `GetBooksByAuthorID` + `:920` inner loop) and `checkDurationMatch`
(`:1025`+`:1034`) are O(M²) per author — pathological for a synthetic author aggregating
thousands of chapter-books. `checkExactISBN` already has an indexed fast path (`:770-811`).
The same-dir guard from §2.1(a) short-circuits the dominant pathological pairs cheaply
(compare `filepath.Dir` before Levenshtein). Severity depends on whether the heal
redistributed the synthetic authors.

### 2.5 purge-stale ≤100K/pass cap (P2)

`engine.go:2195-2199`: `ListCandidates{Status:"pending", Limit:100000}`, single page, no
offset loop. Cap is real; bounds memory/runtime for the interactive op (10-min timeout,
`purge_stale.go:28-46`). Currently latent (P≈10,859 < 100K). During the 380K explosion a
single pass cleaned only the first 100K (why the heal needed 4 passes). **Fix:** either
paginate-read accumulating stale IDs then batch-delete (a naive `offset += 100000` loop
re-fetches survivors forever — wrong), **or** raise the cap to 1M (precedent:
`dataset_backfill.go:100` uses `Limit: 1_000_000`) with chunked deletes. Prefer raising.

### 2.6 Signature oracle + LSH do NOT suppress chapter pairs today

`book_signature_scan.go` is a separate manual op (not wired into `dedup.full-scan`) and
emits its own `book_signature` layer with no folder awareness. LSH/AcoustID collectors
(`collectors_acoustid.go`) operate on per-file fingerprints with no same-folder awareness.
The §2.1(b) delete-on-suppress backstop would also neutralize any chapter pairs these raise.

---

## 3. Residual characterization — the 10,859 exact-pending (fresh, post-heal)

Read-only sample of n=151 pairs spread across the full range (both books fetched per pair;
`.claude/notes/characterize_residual.py`). The composition **flipped** vs the pre-heal 380K:

| Bucket | Pre-heal (380K) | Post-heal (10,859) |
|---|---|---|
| Chapter-sibling (same grandparent) | 69% | **17%** |
| Different-tree (cross-location) | ~17% | **83%** |
| Fragment-vs-full (duration ratio < 0.5) | 8% | **14%** |

Reading the actual pairs, the residual is **four distinct populations**:

1. **Genuine duplicate editions** — e.g. `Equilibrium` and `Darkness Beyond` (ratio 1.0,
   byte-identical filesize, different folders), two real copies of `The Bands of Mourning`.
   → **KEEP for the review UI. Do NOT purge.** This is the dedup feature working.
2. **Fragment-vs-full leftovers (~14%)** — full `.m4b` vs a single `Chapter 67.mp3`
   (`Disquiet Gods`, `Sun Eater`). → needs an absolute fragment-floor rule (purge the
   stray chapter candidate, or attach it).
3. **Title-leak FALSE pairs** — the `"Opening Credits"` / `"Intro"` / `"Big Finish Ident"`
   cluster (dur=0): *different* books (`Backyard Dungeon 4` vs `Made in Hell 3`) sharing a
   junk leaked title. → purgeable; **root cause = the title-leak importer bug (CONS-17,
   memory `project_duration_ms_and_title_leak`)** — fix upstream so it can't regenerate.
4. **Stub / empty records** — `fs=11` / `fs=91` bytes paired with a real book
   (`Kushiel's Mercy`, `The Finder Chronicles [02]`). → cleanup (these never had real audio).

**Implication:** differentiated handling, not a blanket purge. Populations #3 and #4 trace
to upstream importer bugs the prevention work should also close.

---

## 4. The delete-skipped edge case = a real data-completeness bug

`maintenance.fs-regroup-xml` apply (`internal/plugins/maintenance/fs_regroup_xml.go`):
`applyFSRegroup` (`:203`). The delete-guard (`:277-281`) refuses to delete a shell that
still owns ≥1 BookFile — **correct** defensive behavior. The bug is upstream in the attach
step (`:231-251`): it calls `UpsertBookFile` with `BookID: survivor.ID` but
`FilePath: m.FilePath`. `UpsertBookFile` (`pebble_store.go:9799-9827`) **matches by path**
(`:9812-9817`) and on match **preserves the existing row's BookID** (`:9824-9826`) — it does
NOT reassign.

- Shell with FileCount==0 (path only on `Book.FilePath`, no row) → `CreateBookFile` →
  attaches to survivor → shell empty → deleted. **(the 595 healed)**
- Shell with FileCount==1 (already had a materialized BookFile row) → path-match keeps the
  old BookID → file **stays on the shell** → guard sees non-empty → **delete-skipped.**
  **(the 3)** — and the **survivor is silently missing that chapter's audio.**

**Verdict: apply bug, not benign.** Fix: explicit reattach/move (set `BookFile.BookID =
survivor.ID` for the matched row) instead of relying on `UpsertBookFile`. The WS2
store-mocked test must replicate `UpsertBookFile`'s path-match-preserves-BookID semantics or
it proves nothing (see §5).

---

## 5. Prioritized work plan

| Pri | Item | Where | Track |
|---|---|---|---|
| **P0** | Same-dir gate on title/duration emitters + delete-on-suppress backstop | `engine.go:902,1014,452` | code |
| **P0** | Cross-dir sibling-coalescing post-pass (shatter prevention) | `scanner.go:427` | code |
| **P0** | Fix `applyFSRegroup` attach (explicit reattach) + heal the 1 residual | `fs_regroup_xml.go:231` | code+op |
| **P1** | `applyFSRegroup` store-mocked unit test + E2E shattered fixture | new tests | code |
| **P1** | Per-book candidate filter (kill O(N·P)) | `engine.go:407` | code |
| **P1** | Hoist book-only collectors out of per-candidate loop | `engine.go:444-554` | code |
| **P1** | Dedup #1548 read: route `createBookFilesForBook` through `ProcessFile` | `scanner.go:1294` | code |
| **P1** | tag-backfill apply (lossless whole library) | op | op |
| **P2** | Differentiated residual disposition (fragment-floor; purge title-leak/stub) | new op | op |
| **P2** | purge-stale: raise cap to 1M w/ chunked delete | `engine.go:2195` | code |
| **P2** | Per-author O(M²) short-circuit via same-dir | `engine.go:913,1025` | code |

---

## 6. Open verification items (flagged by the auditors)

- Confirm `CandidateFilter` / candidate-store supports an entity-ID predicate before the
  §2.2 fix.
- Confirm `DeleteCandidate` is safe to call mid-scan inside `runUnifiedScoringForBook`
  (candidates already materialized into a slice → appears safe).
- The "why exactly 3" provenance of the one-file shells is a hypothesis; the FileCount 0/1
  split is forced by the prod numbers and is certain.
- Empirical scan open-count (3 is a static worst case; scan-cache `shouldSkipFile:289` may
  skip unchanged files on rescans).
