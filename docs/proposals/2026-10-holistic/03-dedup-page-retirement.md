<!-- file: docs/proposals/2026-10-holistic/03-dedup-page-retirement.md -->
<!-- version: 1.2.1 -->
<!-- guid: 3b8e1f52-6c0d-4a97-9e2b-7f14d5a0c863 -->
<!-- last-edited: 2026-10-09 -->

# 03: Retire the old /dedup page

Analyst: `dedup`. Measured at HEAD `f7211eb39`, in the `aorg-holistic` worktree. This is a planning document only: no code was changed.

> **Coordinator note (08, 2026-10-08).** (1) PR 11 no longer deletes `BulkActionBar.tsx` and its test, `FingerprintVisualsColumn.tsx`, or the four dead `api.ts` wrappers: 01 P7 owns them, so no file is deleted twice. (2) The dead `dedup` category C cluster (`MergeBooks`, `guardKeeperAudioRoute`, `retireMergedLoser` and 12 more funcs, 705 lines), which 01 had marked "see 03", was named nowhere in this doc. It is now 01 P73, with the guard-parity check as its precondition. (3) PR 9's three fixers are written as v2 `repairs.Fixer`s and port to v3 in 05 wave 12D. See `08-integrated-roadmap.md` §3.

### Round-2 review (r1)

Re-checked at HEAD `ebda30d47` on 2026-10-09; the code is identical to the measured HEAD `f7211eb39`, so the anchors hold. Owner decisions D19–D22 (and D2) are treated as fixed. Changes:

- **Parity matrix re-checked against the Review code.** `useDupesLane.ts:106-112` filters are still `band`, `status`, `bothUnmatched`, `entityId`, `search`; `layer` appears only as a read at `:531`; `entity_type: 'book'` is hardcoded at `:362,667`. The old tabs' control labels (`grep -o 'label="…"'` over the five tabs) surface nothing the 78 rows miss: `rowsPerPage` is C09, "Book A ID / Book B ID" is C65, the undo-dismiss banner is C59, the "iTunes" chip is C08. No row added; the count stays 78.
- **G9 redirect gains a default row.** A `/dedup` link with no `?tab=` or an unknown one (the dead `FingerprintVisualsColumn.tsx:86` comment documents a legacy `tab=unified`) must land on `/review?lane=dupes` and keep `book`/`band`; `initialLaneFrom` already falls to `dupes` on either param (`ReviewWorkspace.tsx:165-170`). Deep links therefore survive the redirect.
- **New risk for the Mac fingerprint workers (G5, D2).** "Reset all audio fingerprints" enqueues `acoustid.reset-all` **then** `acoustid.fingerprint-rescan` (`handlers/dedup/handler.go:2286-2292`). Under D2 that rescan is whole-library work for the Mac workers, not the server. The confirm dialog must say so, and the command is hidden while no worker has checked in.
- **D21 applied**: split-book is ported as a fixer and measured against the fragment fixer (C67, G8, PR 9c); the "only if the owner keeps it" wording is gone.
- **PR 7 contradicted appendix B**: it ported `BookDedup.validation.test.tsx`, which appendix B drops because it tests a local copy of `validateBookID`. The port is removed. PR 7 is split into **7a** (lane scaffolding + Authors sub-view) and **7b** (Series sub-view), each with its own E2E block; PR 9 is split into **9a/9b/9c**, one fixer each, since they are independent Go changes with independent rollbacks.
- **PR 11's E2E line fixed**: the "Scheduler Tasks for Dedup" block **moves** to an operations spec (appendix B already said so); 04 keeps the `dedup_refresh` task pointing at `dedup.author-scan`, so nothing in 04 makes it deletable.
- **D27 touches G8**: `reconcile.scan` (UI) and `maintenance.reconcile-scan` (nightly) are a 04 near-duplicate pair; PR 9b's plan source reads `/operations/reconcile/scan/latest` (`server/reconcile.go:68`, `recentReconcileScans`), so PR 9b must be written against whichever op 04's D27 PR keeps, and that PR must keep writing the saved results the fixer reads (§6).
- §7 questions now carry their decision. No change to the endpoint inventory or the delete list in appendix A.

Appendices are in [`03-dedup-page-retirement/`](03-dedup-page-retirement/):

- `A-endpoint-inventory.md`: every route the old page calls, with all of its callers and a verdict.
- `B-test-disposition.md`: every test that targets the old page, and where it goes.

## 1. Summary

- **The old page is two routes, not one.**
  - `/dedup` (`web/src/pages/BookDedup.tsx`) has **9 tabs**.
  - `/dedup/labels` (`web/src/pages/DedupLabels.tsx`) is the gold-label review page.
  - Behind them sit **19 non-test component files** in `web/src/components/dedup/` (9,898 lines by `wc -l`), on top of the 912 lines of the two pages and **78 capabilities** (parity matrix, §3.1).
- **Only one of the 9 tabs was ever ported.** The Review page's Dupes lane absorbed the pair-candidate tab (`UnifiedDedupTab`, deleted in Phase 7). `docs/port-inventory-phase7.md` §2.2 kept the other eight on purpose, because they do different jobs. Retiring the page therefore means a **destination per capability**, not "parity with the Dupes lane". Each row in §3.1 names one of these destinations:
  - Dupes lane
  - new Authors lane
  - Repairs fixer
  - Settings
  - command bar
  - existing page
  - obsolete
- **Count from the matrix:**
  - **78** capabilities;
  - **21** already covered, in Review or on another page;
  - **5** obsolete;
  - **52** need work: **45 MISSING and useful**, 4 partial (the verb exists on `Authors.tsx` or `Series.tsx`, but without the duplicate-group context), and 3 navigation links.
  - The work groups into **9 gap packages**, G1 to G9 (§3.1.1, §3.2).
- **The largest gaps** are three whole domains that Review does not touch at all:
  - author duplicates, including the AI author scans;
  - series duplicates and prune;
  - reconcile apply.
- **Smaller gaps:**
  - The Dupes lane cannot filter by layer.
  - It cannot keep one side in bulk (`keep_side`).
  - It cannot export, although the server route `GET /dedup/candidates/export` exists.
  - It has no cluster (N-way) view.
  - It hides **author-entity candidates** that the engine writes (`internal/dedup/engine.go:2667`). `useDupesLane.ts:362,667` hardcodes `entity_type: 'book'`.
- **Without the page, three server outputs would have no reviewer:**
  - the nightly `ai-dedup-batch` AI author scans, viewable only through `DedupAIReviewTab.tsx`;
  - reconcile scan matches, applied only from `DedupReconcileTab.tsx`;
  - the AcoustID API key, which can only be entered on `DedupAcousticTab.tsx:856`.
- **Links into the page that must be repointed:**
  - `Sidebar.tsx:94,96`;
  - `App.tsx:296-297` (redirects from `/authors/dedup` and `/books/dedup`);
  - the Review command bar: `ReviewWorkspace.tsx:375` names "the Dedup page under Reconcile", and `:387` calls `navigate('/dedup/labels')`;
  - **a backend announcement**: `internal/server/handlers/system/handler.go:243` emits `Link: "/dedup?tab=authors"`;
  - `FingerprintVisualsColumn.tsx:94`, which is itself dead code (no importer).
- **Most endpoints stay.** Some have callers outside the browser:
  - `tools/cmd/merge-split-books` calls the split-book routes;
  - `scripts/dedup_bench_apply.py` calls the author merge, rename, split and reclassify routes;
  - a runbook curls `/dedup/labels/stats` and `/dedup/labels/export`.
  - **AudioBooth calls no `/api/v1` route at all.** Its route manifest lists `/api/...` ABS paths only (`tests/audiobooth-decode/manifest.json`, 0 matches for `api/v1`). Confidence: high.
  - **Exactly 5 routes (plus 2 deprecated aliases) become dead** when the page goes, without being reused by a gap package (Appendix A §A.3).
- **Plan: 16 PRs in 4 phases** (§4; round 2 split PR 7 into 7a/7b and PR 9 into 9a/9b/9c):
  - Phase 0, 1 PR: relocate the shared compare-drawer closure.
  - Phase 1, 11 PRs: close the gaps.
  - Phase 2, 1 PR: repoint links and add a `?tab=`-aware redirect.
  - Phase 3, 3 PRs: delete the frontend, then the backend routes, then the docs.
- **The six owner decisions are answered** (D19–D22, D2; §7): a new **Authors & series lane** in Review (G6, G7), Gold Labels as a Dupes-lane sub-view (G1), split-book ported as a fixer and measured (G8), the per-page cluster view now with a server-side "recommended" keep later (G3, G2), and fingerprinting on the Mac workers only.

## 2. Findings

| ID | Finding | Evidence | Confidence | Impact |
|---|---|---|---|---|
| F1 | `/dedup` is 9 tabs. `/dedup/labels` is a separate page. Both are lazy routes. | `web/src/App.tsx:33-34,296-312`; `BookDedup.tsx:29-39,156-164` | high | Scope of the retirement |
| F2 | Only the pair-candidate job moved to Review. The other 8 tabs were kept on purpose as separate domains. | `BookDedup.tsx:59-64`; `docs/port-inventory-phase7.md` §2.2 | high | A parity matrix against the Dupes lane alone would miss 8 domains |
| F3 | Review imports two components from `components/dedup/`. The drawer pulls in a closure of five more files. | `DupesPanel.tsx:37` imports `CandidateCompareDrawer`; `DupesSpine.tsx:47` imports `FolderFilesChip`. The drawer imports `ScoreBadgeRow` (which imports `BandFilterBar`), `FingerprintCanvas`, `FileInfoCompare`, and `AudioSamplePair` (which imports `../AudioSampleCompare`). Command: `python3 importers.py` (scratch script; output in Appendix A §A.5) | high | Must be relocated before `components/dedup/` can be deleted |
| F4 | `BulkActionBar.tsx` and `FingerprintVisualsColumn.tsx` have no production importer. Only `__tests__/BulkActionBar.test.tsx` imports the first. | `grep -rn "FingerprintVisualsColumn\|BulkActionBar\b" web/src` | high | Already dead; handed to 01. The dead column still carries a `/dedup?book=` link (`:94`). |
| F5 | The Dupes lane shows **book** candidates only. The engine also writes `entity_type: "author"` embedding candidates, which appear only on the Embedding tab. That tab has no entity filter (`DedupEmbeddingTab.tsx:249-253`) and calls `getBook` on author ids. | `useDupesLane.ts:362,667`; `internal/dedup/engine.go:2667,3048` | high | Author-pair candidates have no correct reviewer anywhere |
| F6 | The Dupes lane has no `layer` filter and no bulk `keep_side`, though the wire filter already supports both. | `services/api.ts:6545-6562` (`layer`, `keep_side`); `useDupesLane.ts:106-125` (filters: band, status, bothUnmatched, entityId, search) | high | The Acoustic tab's candidate table and the Version Groups tab cannot be replaced until these exist |
| F7 | A server-side export route exists but Review does not use it. The workspace comment saying there is no export route is stale. | `wire_dedup_routes.go:29`; used only by `DedupEmbeddingTab.tsx:348-355`; `ReviewWorkspace.tsx:111-118` ("There is no server-side export route"), whose CSV export covers metadata rows only (`:524-533`) | high | S-sized gap |
| F8 | The AI author-scan lifecycle has no other UI. Its endpoints are `/ai/scans`, `/ai/scans/:id`, `/results`, `/apply` and `/cancel`. The nightly `ai-dedup-batch` writes scans (the "superseded" status) that only this tab lists. | `DedupAIReviewTab.tsx:77-232,475-530`; `wire_media_routes.go:52-62`; `internal/plugins/maintenance/dedup_ops.go` (ai-dedup-batch) | high | Removing the tab without a port leaves a nightly job writing output nobody can review or apply |
| F9 | Reconcile is half-ported. Review can *start* a scan (`ReviewWorkspace.tsx:371-376`), but its description sends the user to "the Dedup page under Reconcile". Viewing and applying matches exists only in `DedupReconcileTab.tsx:64,125,287,413`. | as cited | high | Live link into the page |
| F10 | The AcoustID API key can be set only on the Acoustic tab. Settings has the AcoustID toggles but no key field. | `DedupAcousticTab.tsx:837-862`; `Settings.tsx:424-425` (`acoustid_online_lookup`, `acoustid_nightly_limit`); `grep acoustid_api_key web/src` finds `api.ts:1028` only, outside `components/dedup` | high | Settings gap |
| F11 | The backend emits a notification link to the old page. | `internal/server/handlers/system/handler.go:243` (`Link: "/dedup?tab=authors"`) | high | Needs a backend edit in the repoint PR. That file belongs to the Go side; it is listed here so it is not missed. |
| F12 | Version Groups and the Dupes lane exact layer use different hash sources. (Note: `layer` on a unified candidate is the label of its *dominant* signal, mapped by `layerNameForKind` to `exact`, `acoustid` or `embedding`; `internal/dedup/engine.go:1260-1303`. An exact file-hash signal has confidence 1.0, so it always dominates.) `GetDuplicateBooks` groups by the **book-level** `organized_file_hash`/`file_hash`. The exact collector pairs books that share a whole-file hash on **any BookFile**. The second is a superset for multi-file books, so the tab's groups reach the lane as exact pairs. | `internal/database/pebble_store.go:1784-1815`; `internal/dedup/collectors_exact.go:16-18` | medium | Version Groups is obsolete once G2 (layer filter) and G3 (clusters) land |
| F13 | The Duplicate Scan tab (`dedup.book-scan`) is a different pipeline whose results never reach the queue. Review's "Force full rescan" was repointed away from it on purpose. | `web/src/components/review/dedupPipeline.ts:16-21`; `duplicates/handler.go:165-175,239-241` (30-minute in-memory cache) | high | Obsolete. The op is passed to 04 as a candidate to retire. |
| F14 | The `duplicate-copies` Repairs fixer is **not** a replacement for Version Groups. It *retires* proven duplicate copies under stricter gates: H ≥ 90 %, equal titles, no ASIN conflict, no `not_dup` label, iTunes copies excluded. Version Groups *links* the copies as versions, and both stay visible. | `internal/plugins/maintenance/duplicate_copies_fixer.go:15-45,438-442` | high | Two different verbs. Both survive; neither replaces the tab by itself. |
| F15 | Split Books overlaps the `fragment-consolidation` fixer's "no parent" case (3+ short same-key chapters from one folder). The split-book detector also handles a grandparent-directory shape the fixer does not describe. | `internal/dedup/split_book_detector.go:5-30`; `fragment_consolidation_fixer.go:403-410` | medium | Owner question Q3: retire it, or port it as a fixer |
| F16 | The author-tab actions partly overlap two fixers. Neither fixer finds *fuzzy-duplicate* author groups or merges them. | `combined-author-credits` removes combined credits only where the separate authors are already credited (`combined_author_fixer.go:181-185`). `junk-authors` relinks non-person authors, including studios and narrator credits (`junk_author_fixer.go:255-258`). | high | The author-dedup list and merge need a home in Review (G6) |
| F17 | Already handled in Review: the rescore apply path, server-side search `q`, the `?book=` deep link, the `BookDetailStatusAlerts` link, and deletion of `UnifiedDedupTab`. Several port-inventory lines are therefore stale (§2.1). | `ReviewWorkspace.tsx:315-333`; `useDupesLane.ts:115-124,371`; `ReviewWorkspace.tsx:165-170`; `BookDetailStatusAlerts.tsx:106` | high | Use this document, not the inventories |
| F18 | Eight dedup-family routes have no frontend caller today, independent of this retirement. The frontend wrappers `triggerDedupRefresh`, `requestAIAuthorReview`, `applyAIAuthorReview` and `getReconcilePreview` exist but have no caller. | Routes: `wire_dedup_routes.go:34,70-73,76-78` and `:117-118` (`/series/normalize`). Command: `grep -c <path> web/src/services/api.ts` returns 0 for `scan-book-signature`, `purge-legacy-fp`, `embed-async`, `lsh-index`, `emb-reencode`, `purge-acoustid-conflicts` and `series/normalize`. | high (no web caller); low (no caller at all: CLI, ops and runbooks not fully checked) | Passed to 01 and 04 (§6). **Not** on this document's delete list. |
| F19 | AudioBooth calls only ABS-shaped `/api/...` routes. The ABS group is mounted at the root (`router.Group("")`), separate from `/api/v1`. | `tests/audiobooth-decode/manifest.json`: 40 distinct paths, `grep -c api/v1` = 0; `internal/server/wire_abs_routes.go:596`; `server_lifecycle.go:1422` (`/api/v1` group) | high | No dedup route is an AudioBooth dependency |
| F20 | Some routes have callers outside the browser. | `tools/cmd/merge-split-books/main.go` (split-book scan and candidates); `scripts/dedup_bench_apply.py:17-21` (`/authors/merge`, `/:id/name`, `/:id/split`, `/:id/reclassify-as-narrator`, `/:id/aliases`); `docs/plans/2026-07-12-dedup-clean-remeasurement-runbook.md:37-42` (`/dedup/labels/stats`, `/dedup/labels/export`); `internal/config/update_service.go:39,802` (`/dedup/rescore` named in an error remedy string) | high | These routes stay |

### 2.1 Port inventories re-verified at HEAD

`docs/port-inventory-dupes.md` and `docs/port-inventory-phase7.md` are dated 2026-08-20. Lines that are now stale:

| Inventory line | Status at HEAD | Evidence |
|---|---|---|
| dupes: "`handleRescore(apply)` still only on the legacy page" | **Done.** "Preview new scores" and "Recalculate scores…" (behind a confirm dialog) are in the command bar. | `ReviewWorkspace.tsx:315-333` |
| dupes Defect 1: "`?book=` filters client-side" | **Fixed.** It is a server-side `entity_id` filter, and the lane is seeded from the URL. | `useDupesLane.ts:371`; `ReviewWorkspace.tsx:165-170,187-194` |
| dupes Defect 2: "Search is scoped to the loaded page" | **Fixed.** Search is server-side `q`, debounced. | `useDupesLane.ts:115-124,678` |
| dupes Defect 3 and phase7 §3: the `BookDetailStatusAlerts` link to `/dedup/candidates` | **Fixed.** The comment records the repoint. | `BookDetailStatusAlerts.tsx:106`; `.test.tsx:42-43` |
| phase7 §1: delete `UnifiedDedupTab` and `dedup_show_legacy` | **Done.** Neither exists. | `ls web/src/components/dedup`; `BookDedup.tsx:59-64` |
| phase7 §1: `triggerFingerprintBackfill` is "reachable from `DedupAcousticTab` and `pages/Library.tsx`" | Still true. After this retirement **only** `Library.tsx` reaches it. | `callers.py`: `PROD=pages/Library.tsx` |
| AI-REFERENCE.md:339: "BookDedup … Tabs: Author dedup (AI scan pipeline), Series dedup, Book dedup" | Stale (it describes 3 tabs; there are 9). Rewrite it in PR 13. | `docs/AI-REFERENCE.md:339` |

## 3. Proposed specification

### 3.1 Parity matrix

Legend:

- **Review** gives a `file:line` in `web/src/components/review/` (or another page), or **MISSING**.
- **Dest** is where the capability should end up:
  - `DL` = Dupes lane
  - `AL` = new Authors lane
  - `RF` = Repairs fixer
  - `CB` = command bar
  - `ST` = Settings
  - `EX` = existing page elsewhere
  - `OB` = obsolete
- **Size** is the work to port that one row: S, M or L, with its gap package in brackets. A covered or obsolete row is `0`, with the reason.
- Old-page paths are relative to `web/src/components/dedup/` unless they begin with `pages/` or another directory.

#### Shell and navigation

| # | Capability | Old page | Review | Useful? | Dest | Size |
|---|---|---|---|---|---|---|
| C01 | Tab per domain, deep-linkable with `?tab=` | `pages/BookDedup.tsx:29-51` | Lanes `ReviewWorkspace.tsx:165-170` (`?lane=`, `?book=`, `?band=`) | Yes: bookmarks and the backend announcement use `?tab=` | redirect (G9) | S |
| C02 | Sidebar entries "Dedup" and "Gold Labels" | `layout/Sidebar.tsx:94,96` | "Review" `Sidebar.tsx:95` | Yes, as navigation | repoint (G9) | S |
| C03 | Old-path redirects `/authors/dedup` and `/books/dedup` | `App.tsx:296-297` | — | Yes, for bookmarks | repoint (G9) | S |

#### Version Groups tab (`DedupBookTab.tsx`, `GET /audiobooks/duplicates`)

| # | Capability | Old page | Review | Useful? | Dest | Size |
|---|---|---|---|---|---|---|
| C04 | List identical-hash book groups | `DedupBookTab.tsx:120` | Exact-layer pairs in the Dupes lane (`useDupesLane.ts:358-371`; layer chip `spine/DupesSpine.tsx:339`) | The job is useful; the separate list is not (F12) | DL | 0 (pairwise covered; N-way is C05) |
| C05 | Pick the primary copy of an N-copy group | `DedupBookTab.tsx:399-409` | Pairs only: Keep A/B `spine/DupesSpine.tsx:391-418`. N-way: **MISSING** | Yes | DL (G3) | M (G3) |
| C06 | Link one group as versions (`dedup.book-merge` op, polled) | `DedupBookTab.tsx:140-153,195-197` | Pairwise merge `useDupesLane.ts:892-913` | Yes | DL (G3 for N-way) | 0 (pairwise covered; N-way is C05) |
| C07 | Link selected groups / link all groups | `DedupBookTab.tsx:221-273` | `mergeSelected` / `mergeAllFiltered` `useDupesLane.ts:931,941`, but no way to scope them to the exact layer: **MISSING** | Yes | DL (G2) | S (G2) |
| C08 | iTunes and format chips per copy | `DedupBookTab.tsx:416-423` | Format is in the drawer (`FileInfoCompare`). The iTunes marker on the row is **MISSING** | Yes: iTunes copies are hands-off and the reviewer must see which they are | DL (G2) | S (G2) |
| C09 | Client-side pagination and select-all-matching | `DedupBookTab.tsx:99-108,361` | Server pagination `DupesPanel.tsx:246`; `SelectAllMatchingBanner` `DupesPanel.tsx:322` | — | covered | 0 (covered) |

#### Duplicate Scan tab (`DedupAdvancedScanTab.tsx`, op `dedup.book-scan`)

| # | Capability | Old page | Review | Useful? | Dest | Size |
|---|---|---|---|---|---|---|
| C10 | Run the hash/folder/fuzzy-title group scan | `:68-71` | "Force full rescan" (`dedup.full-scan`) `ReviewWorkspace.tsx:281-295` | No. Its results go to a 30-minute cache and never reach the queue (F13) | OB | 0 (obsolete) |
| C11 | Confidence tabs high/medium/low, with counts | `:221-226` | Band chips `DupesPanel.tsx:178-190` | — | covered | 0 (covered) |
| C12 | Link a group as versions / reject a group | `:85-104` | merge/dismiss `useDupesLane.ts:892-930` | — | covered | 0 (covered) |
| C13 | Refresh cached results | `:170-171` | Lane refetch | No (the cache is obsolete) | OB | 0 (obsolete) |

#### Authors tab (`DedupAuthorTab.tsx`)

| # | Capability | Old page | Review | Useful? | Dest | Size |
|---|---|---|---|---|---|---|
| C14 | List fuzzy duplicate-author groups; refresh (`dedup.author-scan`) | `:359-370` | **MISSING** | Yes. The backend announces these groups (`system/handler.go:237-244`). | AL (G6) | M (G6) |
| C15 | Edit the canonical name inline; one-click "use suggested name" | `:395-398,729-760` | **MISSING** (`pages/Authors.tsx:493` renames, but outside the group context) | Yes | AL (G6) | S (G6) |
| C16 | Per-variant Author/Narrator role toggle, which reclassifies before the merge | `:453-469,806,934` | **MISSING**. The `junk-authors` fixer relinks narrator credits library-wide (F16). | Yes | AL (G6) | S (G6) |
| C17 | Remove one variant from the merge | `:950` | **MISSING** | Yes | AL (G6) | S (G6) |
| C18 | Validate a name against the providers (`POST /dedup/validate`) | `:438-441,967` | **MISSING** | Yes (it is the only evidence shown for a group) | AL (G6) | S (G6) |
| C19 | "Find Real Author" for production-company groups (`resolve-production`) | `:776,1005-1030` | **MISSING** | Yes | AL (G6) | S (G6) |
| C20 | Split a composite author, then reclassify narrators | `:413-425` | `pages/Authors.tsx:640` (manual, with no narrator step) | Yes | AL (G6) | S (G6) |
| C21 | Merge one group / merge selected / merge all (polled op) | `:453-547,599-618` | `pages/Authors.tsx:570` (manual multi-select only) | Yes | AL (G6) | M (G6) |
| C22 | Popover listing each author's books | `:140-317` | **MISSING** | Yes (evidence; the clickable-count rule) | AL (G6) | S (G6) |

#### AI Review tab (`DedupAIReviewTab.tsx`, `/ai/scans`)

| # | Capability | Old page | Review | Useful? | Dest | Size |
|---|---|---|---|---|---|---|
| C23 | Start an AI author scan, realtime or batch | `:46,77` | **MISSING**. Review's "AI review of unclear pairs" is the *book* `dedup.llm-review` (`ReviewWorkspace.tsx:357-363`). | Yes | AL (G7) | S (G7) |
| C24 | Poll progress; cancel a scan | `:105-115,231-232` | **MISSING** | Yes | AL (G7) | S (G7) |
| C25 | Scan history drawer, including superseded nightly scans | `:475-530` | **MISSING** | Yes (F8) | AL (G7) | M (G7) |
| C26 | Agreement filter (All / Agreed / …) with counts | `:48-54,295-300` | **MISSING** | Yes | AL (G7) | S (G7) |
| C27 | Select results and apply | `:58-69,146-147` | **MISSING** | Yes | AL (G7) | S (G7) |
| C28 | "Books" sub-tab | `:561,564-573` | — | No: it is a "coming soon" placeholder | OB | 0 (obsolete) |

#### Series tab (`DedupSeriesTab.tsx`)

| # | Capability | Old page | Review | Useful? | Dest | Size |
|---|---|---|---|---|---|---|
| C29 | List same-name series groups; refresh (`dedup.series-scan`) | `:150-156` | **MISSING** | Yes | AL (G6, series sub-view) | M (G6) |
| C30 | Edit a series name inline (`PATCH /series/:id`) | `:117-120,580` | `pages/Series.tsx:522` (rename with undo, outside the group context) | Yes | AL (G6) | S (G6) |
| C31 | Per-author narrator flag before a series merge | `:86,190-194,603` | **MISSING** | Yes | AL (G6) | S (G6) |
| C32 | Validate a series name | `:104-107` | **MISSING** | Yes | AL (G6) | S (G6) |
| C33 | Merge one group / merge selected / merge all (`/series/merge`, `/series/deduplicate`) | `:179-281,811-820` | `pages/Series.tsx:589` (manual only) | Yes | AL (G6) | M (G6) |
| C34 | Prune empty series: preview, confirm, prune | `:87-89,301-325,964-965` | **MISSING** | Yes, and it fits the Repairs trial/apply shape exactly | RF (G8) | S (G8) |

#### Reconcile tab (`DedupReconcileTab.tsx`)

| # | Capability | Old page | Review | Useful? | Dest | Size |
|---|---|---|---|---|---|---|
| C35 | Start a reconcile scan | `:109` | "Match missing files" `ReviewWorkspace.tsx:371-376` | — | covered | 0 (covered) |
| C36 | Show the latest scan's matches: book → file, match type, confidence | `:64,87,287-330` | **MISSING** | Yes | RF (G8) | M (G8) |
| C37 | Select matches and apply (`reconcile.apply` op) | `:119-136` | **MISSING** | Yes | RF (G8) | S (G8) |
| C38 | List missing-file books with no candidate file | `:413-430` | **MISSING** | Yes (clickable-count rule) | RF (G8) | S (G8) |

#### Embedding tab (`DedupEmbeddingTab.tsx`)

| # | Capability | Old page | Review | Useful? | Dest | Size |
|---|---|---|---|---|---|---|
| C39 | Per-layer stat cards (exact / embedding / llm) | `:655-660` | Only `pendingTotal` `DupesPanel.tsx:242` | Yes. Each count must be clickable and set the layer filter. | DL (G2) | S (G2) |
| C40 | Layer filter | `:1318-1340` | **MISSING** | Yes | DL (G2) | S (G2) |
| C41 | Status filter: pending / merged / dismissed | `:1285-1307` | `DupesPanel.tsx:158-172` | — | covered | 0 (covered) |
| C42 | Text search | `:1260-1300` (page-scoped) | Server-side `q` `DupesPanel.tsx:211-222` | — | covered (better) | 0 (covered) |
| C43 | Cluster view: connected components of pairs | `:119-180,558,1462` | **MISSING** | Yes (series boxsets, multi-copy books) | DL (G3) | L (G3) |
| C44 | Link a cluster with a chosen primary | `:361-364,854,1558,1646` | **MISSING** | Yes | DL (G3) | M (G3) |
| C45 | Reject a whole cluster | `:374-377,1567` | **MISSING** | Yes | DL (G3) | S (G3) |
| C46 | Remove one or more books from a cluster | `:390-434,1597` | **MISSING** | Yes | DL (G3) | S (G3) |
| C47 | "Merge page": link every cluster on the page | `:612-622,1166` | Select page + `mergeSelected` `DupesPanel.tsx:370-380` | No as a separate verb | OB | 0 (obsolete) |
| C48 | Bulk-link everything under the filter | `:584-589` | `mergeAllFiltered` `useDupesLane.ts:941` | — | covered | 0 (covered) |
| C49 | Series-level link (`series-summary`, `link-series`) | `:311-338,1059,1173` | **MISSING** | Yes (it is a cluster scope) | DL (G3) | M (G3) |
| C50 | Export the filtered candidates as CSV or JSON (`GET /dedup/candidates/export`) | `:344-355,1069-1095` | **MISSING** (`ReviewWorkspace.tsx:524-533` exports metadata only) | Yes | DL (G4) | S (G4) |
| C51 | Listen to audio samples for a cluster pair | `:450,1635` | Drawer `AudioSamplePair` (pairwise) | — | covered | 0 (covered) |
| C52 | Scan triggers: full-scan, LLM, AcoustID, embed | `:477-560,949-975` | `ReviewWorkspace.tsx:305-363` | — | covered | 0 (covered) |
| C53 | Hover to see each side's file list | `:72-80` | `FolderFilesChip` `DupesSpine.tsx:248` | — | covered | 0 (covered) |
| C54 | Author-entity candidates (shown here by accident, through the missing entity filter) | `:249-253` | **MISSING** (`useDupesLane.ts:362,667`) | Yes, but in the right lane (F5) | AL (G6) | M (G6) |
| C78 | "Partial fp N%" chip when a book's signature came from partial audio (`book_sig_coverage_pct`) | `:786-791` | **MISSING**: `grep -rn book_sig_coverage web/src` finds only this tab and `api.ts:220` | Yes. It tells the reviewer a similarity score is weaker evidence. | DL (G2) | S (G2) |

#### Acoustic tab (`DedupAcousticTab.tsx`)

| # | Capability | Old page | Review | Useful? | Dest | Size |
|---|---|---|---|---|---|---|
| C55 | "Fingerprint Books" (`acoustid.fingerprint-rescan` mode `missing`) | `:709-713` | Not in Review. Also on `pages/Library.tsx`. | Keep where it is. Fingerprinting decodes audio, so whether the server should run it at all is 04's question. | EX | 0 (stays on Library) |
| C56 | "Find Acoustic Duplicates" (`acoustid.scan`) | `:722-726` | `ReviewWorkspace.tsx:348-355` | — | covered | 0 (covered) |
| C57 | Table of acoustid-layer candidates: Keep A / Keep B / Compare / Dismiss | `:56,616,741-772` | The Dupes lane does this once a layer filter exists | Yes | DL (G2) | S (G2) |
| C58 | Bulk Keep A / Keep B over the filter (`keep_side`) | `:59-62,915-917,957-966` | **MISSING** | Yes | DL (G2) | S (G2) |
| C59 | Bulk dismiss over the filter, with undo | `:957,999` | `useDupesLane.ts:973-1000` | — | covered | 0 (covered) |
| C60 | Re-check the pending count before a bulk action (`FILTER_CHANGED` refusal) | `:1026,978` | `useDupesLane.ts:870-883` (`bulkFailure`) | — | covered | 0 (covered) |
| C61 | Purge stale candidates | `:790-794` | Queue → "Purge stale" `ReviewWorkspace.tsx:537-541` | — | covered | 0 (covered) |
| C62 | Reset all AcoustID fingerprints, then rescan | `:873-883` | **MISSING** | Yes, rarely, and it is destructive | CB (G5, Advanced → Maintenance, with confirm) | S (G5) |
| C63 | AcoustID online lookup (`acoustid.lookup-online`) | `:821-825` | **MISSING** | Yes | CB (G5) | S (G5) |
| C64 | AcoustID.org API key field | `:837-862` | **MISSING** (F10) | Yes | ST (G5) | S (G5) |
| C65 | Compare two arbitrary books by id (`compare-acoustid`) | `:200-276,1493,1582` | **MISSING** | Yes, as a diagnostic for the pair already open | DL (G5, drawer section) | S (G5) |
| C66 | "Recommended keep" and metadata-quality chips | `:1280-1300` | `DupesSpine.tsx:153-158,226-230` | — | covered | 0 (covered) |

#### Split Books tab (`DedupSplitBookTab.tsx`)

| # | Capability | Old page | Review | Useful? | Dest | Size |
|---|---|---|---|---|---|---|
| C67 | Scan for split-book clusters (`dedup.split-book-scan`) | `:204-210` | **MISSING**. It partly overlaps the `fragment-consolidation` fixer (F15). | Yes. D21: port it as a fixer, measure it against the fragment fixer on prod trials, then decide whether it stays | RF (G8) | M (G8) |
| C68 | List and expand clusters, with a suggested keep | `:76,152,183` | **MISSING** | Yes | RF (G8) | S (G8) |
| C69 | Merge one cluster | `:237-247` | **MISSING** | Yes | RF (G8) | S (G8) |
| C70 | Bulk merge from an imported candidate file | `:259-289` | **MISSING**. The CLI `tools/cmd/merge-split-books` does the same. | No in the UI: the Repairs "approve all" replaces it | OB | 0 (obsolete) |

#### Gold Labels page (`pages/DedupLabels.tsx`)

| # | Capability | Old page | Review | Useful? | Dest | Size |
|---|---|---|---|---|---|---|
| C71 | Label stats header | `:323-330` | **MISSING** | Yes | DL (G1) | S (G1) |
| C72 | All-labels table: filter by label, source and band; configurable columns; pagination | `:497-521,576-622` | **MISSING** | Yes (the owner's feedback dataset) | DL (G1) | M (G1) |
| C73 | One-click human override (`/dedup/labels/:id/override`) | `:332-345` | **MISSING** | Yes | DL (G1) | S (G1) |
| C74 | Suspicious-label queue, with "why suspicious" reasons and override | `:161-266,361-390` | **MISSING** | Yes | DL (G1) | S (G1) |

#### Cross-cutting

| # | Capability | Old page | Review | Useful? | Dest | Size |
|---|---|---|---|---|---|---|
| C75 | Keyboard shortcuts | none: `grep -l keydown components/dedup/*.tsx pages/DedupLabels.tsx` is empty | `j k m d s A Enter Esc ?` `useDupesLane.ts:1054-1140` | — | covered (better) | 0 (covered) |
| C76 | Inline operation progress (`OperationProgress`, `runOperationWithPolling`) | `dedupHelpers.tsx` | Notification bell, plus the `dedupPipeline` banner `ReviewWorkspace.tsx:162-163` | — | covered | 0 (covered) |
| C77 | Distinct loading / error / empty / populated states | per tab, e.g. `DedupAuthorTab.tsx:215,628-640` | Dupes: `DupesPanel.tsx:262-288,339`. New lanes and fixers must meet the same bar. | — | covered for Dupes | 0 (covered) |

**Categories the brief named that have no row of their own:**

- **Keep both**: this is "Not a duplicate" (dismiss). It is in both surfaces: C12, C59, `DupesSpine.tsx:419`.
- **Unmerge or undo**:
  - Neither surface can undo a link.
  - The only undo on either side is for bulk *dismiss* (C59).
  - The server's `/merge/undo/:journal_id` and `/merge/sibling-undo/:journal_id` (`wire_dedup_routes.go:105-110`) have **no UI anywhere**: `grep -rn "merge/undo\|combine-journal" web/src` finds only a comment at `api.ts:2182`.
  - No parity gap, but a real product gap. It is listed in §6 for 07.

#### 3.1.1 Auditable partition

Each row is in exactly one bucket. Checked by script: 78 rows, 78 unique ids, every id in exactly one bucket.

| Bucket | Rows | n |
|---|---|---|
| **Covered** (no work) | C09 C11 C12 C35 C41 C42 C48 C51 C52 C53 C56 C59 C60 C61 C66 C75 C76 C77 | 18 |
| **Covered pairwise** (the N-way part is a separate MISSING row, in G3) | C04 C06 | 2 |
| **Covered on another page; stays there** | C55 (`pages/Library.tsx`) | 1 |
| **Obsolete** | C10 C13 C28 C47 C70 | 5 |
| **Partial**: the verb exists on `Authors.tsx` / `Series.tsx` without the duplicate-group context; G6 adds the context | C20 C21 C30 C33 | 4 |
| **Navigation**: a link or route to repoint (G9) | C01 C02 C03 | 3 |
| **MISSING and useful** | C05 C07 C08 C14 C15 C16 C17 C18 C19 C22 C23 C24 C25 C26 C27 C29 C31 C32 C34 C36 C37 C38 C39 C40 C43 C44 C45 C46 C49 C50 C54 C57 C58 C62 C63 C64 C65 C67 C68 C69 C71 C72 C73 C74 C78 | 45 |

**Totals: 78 capabilities.**

- 21 are covered (18 + 2 + 1).
- 5 are obsolete.
- 52 need work: 45 MISSING, 4 partial and 3 navigation.

The 45 MISSING rows fall into gap packages G1 to G8. G9 covers the 3 navigation rows. Per package:

| Package | MISSING rows | n |
|---|---|---|
| G1 Labels | C71 C72 C73 C74 | 4 |
| G2 Layer filter, keep-side and row chips | C07 C08 C39 C40 C57 C58 C78 | 7 |
| G3 Clusters | C05 C43 C44 C45 C46 C49 | 6 |
| G4 Export | C50 | 1 |
| G5 AcoustID tools | C62 C63 C64 C65 | 4 |
| G6 Authors lane | C14 C15 C16 C17 C18 C19 C22 C29 C31 C32 C54 (plus partial C20 C21 C30 C33) | 11 |
| G7 AI scans | C23 C24 C25 C26 C27 | 5 |
| G8 Repairs fixers | C34 C36 C37 C38 C67 C68 C69 | 7 |
| | **total** | **45** |

### 3.2 Gap-closing specification

Every gap package follows the patterns already in the workspace:

- **One lane = one descriptor plus one hook.**
  - A `LaneDescriptor` in `lanes/<x>.ts`, with verbs total over its action type (`lanes/types.ts:22-51`).
  - A `useXLane(toast, active, urlFilters)` hook that fetches only while active (`ReviewWorkspace.tsx:180-217`).
  - A `XPanel` component that renders the rail, the spine and the action bar.
- **The view-mode toggle is the existing three-position `SpineViewMode`** (`spine/viewMode.ts:11`).
  - A lane relabels the positions through `viewModeLabel()` (`ReviewWorkspace.tsx:103-108`). The Repairs lane already does this, mapping them to Compact / Details / Grouped.
  - Do not add a fourth position.
- **Library-scope jobs are `CommandMenu` items** with `startJob`. Destructive ones open a confirm dialog first, as `rescore-apply` does (`ReviewWorkspace.tsx:323-333`).
- **Library-wide fix lists are Repairs fixers**: trial, per-row preview, approve or pick, apply. This follows the owner rule in memory `feedback_fixers_live_in_review_repairs_tab.md`.
- **Every count is clickable** and opens its rows (owner rule `feedback_every_count_must_be_clickable.md`).
- **Every fetch has four distinct states** (loading, error, empty, populated) and an `AbortController`, as `useDupesLane` already has (`port-inventory-dupes.md`, "AbortController is stronger…").

#### G1: Labels inside the Dupes lane (C71-C74). Size M.

- Add a lane-local segmented control to `DupesPanel`: **Queue | Labels | Suspicious**. Default: Queue.
  - It is a *sub-view*, not a `SpineViewMode`. The view-mode toggle chooses how rows are laid out, and stays available on Queue.
  - URL: seeded once from `?lane=dupes&view=labels` or `view=suspicious`, and not mirrored back. This is the same rule `initialLaneFrom` documents (`ReviewWorkspace.tsx:22-26`).
- **Labels**:
  - a stats strip from `/dedup/labels/stats`, where each count sets the matching filter;
  - a server-paged table using the existing `ConfigurableTable`, filtered by label, source and band;
  - override implemented with `LabelToggle`.
  - The logic moves from `pages/DedupLabels.tsx` into `lanes/useDupesLabels.ts`. The rendering moves into `DupesLabelsView.tsx`.
- **Suspicious**: the `SuspiciousQueue` from `DedupLabels.tsx:161-266`, unchanged apart from moving.
- **Command bar**: "Manage labels" (`ReviewWorkspace.tsx:378-389`) switches to `lane=dupes`, view Labels. It stops navigating away.
- **Fix while moving it**: the local `LabeledExample` interface leaves out `Score`/`ScoreBreakdown` (audit D11, `docs/audits/2026-09-02-dedup-review-matching-path-audit.md:85`). Type them, and show the breakdown with the existing `EvidencePanel` / `dedupEvidence` adapter.

#### G2: Dupes lane filters, bulk keep-side and row chips (C07, C08, C39, C40, C57, C58, C78). Size M.

- **Filter**: add `layer: DedupLayer | null` to `DupesFilters` (`useDupesLane.ts:106`).
  - It is a server-side param on both the list and `bulkFilter` (`:666-680`); `BulkDedupCandidateFilter.layer` already exists (`api.ts:6548`).
  - Changing it resets the page and the selection, like `band`.
  - URL-seeded from `?layer=`, so the redirect `/dedup?tab=acoustic` → `/review?lane=dupes&layer=acoustid` works.
- **Layer chips**: render them beside the band chips (`DupesPanel.tsx:176-190`). Each shows its pending count and sets the filter when clicked.
  - The counts come from the `stats` the hook already fetches, which are grouped by layer and status (`useDupesLane.ts:271,624-628`). This adds no request.
- **Bulk keep-side**: A and B are not random. `UpsertCandidateNew` canonicalises the pair so that A is the smaller id (`internal/database/embedding_store.go:719`), and ULIDs sort by creation time, so A is the **older record**. Label the verbs that way ("Keep the older record" / "Keep the newer record"), not "Keep A". A bulk "keep the recommended side" would be the more useful verb; it needs server support and is Q6. Add two lane actions, `keepAAllFiltered` and `keepBAllFiltered`.
  - Each is `{lane:'dupes'; type; expectedTotal}`, sent as `bulkLinkDedupCandidates({...bulkFilter, keep_side:'a'|'b', expected_total})`.
  - Add them to `reviewActions.ts:46-61` and to the `verbs` in `lanes/dupes.ts:26-36`. The total-map type forces both.
  - Same pending-only refusal and confirm dialog as `mergeAllFiltered` (`DupesPanel.tsx:397-450`).
- **iTunes marker** (C08): in `BookSide` (`DupesSpine.tsx:~191-248`), show an "iTunes" chip when the book has an iTunes path or PID. This uses the same predicate the `duplicate-copies` fixer uses to exclude iTunes copies (`duplicate_copies_fixer.go:41-45`). The marker is read-only.
- **Partial-signature chip** (C78): in the same `BookSide`, show "partial fp N%" with the explanatory tooltip from `DedupEmbeddingTab.tsx:786-791` when `book_sig_coverage_pct < 100`.

#### G3: Cluster view and cluster verbs (C05, C43-C46, C49). Size L.

- **View**: the third `SpineViewMode` position, `candidates`, is currently rendered as compact on this lane (`ReviewWorkspace.tsx:88-91`). Relabel it **"Clusters"** for the dupes lane in `viewModeLabel()`.
- **New spine** `spine/DupesClusterSpine.tsx`:
  - It groups the loaded page into connected components. Lift `buildClusters` (`DedupEmbeddingTab.tsx:119-180`) into `lanes/dupesClusters.ts` and unit-test it.
  - Each card shows the members: cover, path, `FolderFilesChip`, quality chip, and a radio to choose the primary.
- **Scope note**: clusters are built from one server page. A cluster that crosses a page boundary is split. Say so on the card ("N more pairs for these books are on other pages"), using a `total`-aware count from `entity_id` filtering.
  - A server-side clustering endpoint is a follow-up. It is the correct long-term shape.
- **Verbs**, as new lane actions over `ids: string[]` (book ids, not candidate ids; keep the type distinct):
  - `linkCluster` with `primaryId` → `api.linkDedupCluster`;
  - `rejectCluster` → `api.rejectDedupCluster`;
  - `removeFromCluster` → `api.removeFromDedupCluster`.
- **Series scope** (C49): a "By series" toggle in the Clusters view lists `series-summary` rows. Each row's action is "Link all pending pairs in this series", which calls `link-series`, behind a confirm dialog showing the count.

#### G4: Export from the Dupes lane (C50). Size S.

- Add a Queue-menu command "Export duplicates (CSV/JSON)". It is enabled only while the dupes lane is active.
- It builds the same query string as `DedupEmbeddingTab.tsx:348-355` from the lane's current server filters, including `layer`, `band`, `status`, `q` and `entity_id`.
- Delete the stale comment at `ReviewWorkspace.tsx:111-117`, and rename the metadata command to "Export metadata rows (CSV)".

#### G5: AcoustID tools (C62-C65). Size S.

- **Settings → Dedup** (`components/settings/DedupSettingsSection.tsx`): add an AcoustID.org API key field, shown masked, saved with `updateConfig({acoustid_api_key})`. This is the same call `DedupAcousticTab.tsx:856-862` makes.
- **Dedup menu → Advanced → Maintenance**: add two commands.
  - "Look up fingerprints online" (`triggerAcoustIDOnlineLookup`).
  - "Reset all audio fingerprints…" (`resetAcoustIDFingerprints`), behind a confirm dialog that says it deletes every stored fingerprint and queues a rescan. The handler enqueues `acoustid.reset-all` and then `acoustid.fingerprint-rescan` (`handlers/dedup/handler.go:2286-2292`). Under D2 the rescan runs on the Mac fingerprint workers, so the dialog must state that it queues a whole-library re-fingerprint for the workers (days, not minutes), and the command is shown only while a worker has checked in (`GET /fingerprint/worker/hello` lease state; reuse whatever indicator `pages/Library.tsx` uses for "Fingerprint Books" after D2 lands). The server must never decode audio for it.
- **Compare drawer**: add an "Audio match" section to `CandidateCompareDrawer`. It calls `compareAcoustID(book_a, book_b)` on demand and renders the response the way `AcousticComparePanel` does (`DedupAcousticTab.tsx:200-276`). This replaces the panel where you type in two ids.

#### G6: Authors lane (C14-C22, C29-C33, C54). Size L.

- **New lane `authors`**, labelled "Authors & series":
  - `ReviewLane` gains `'authors'` (`reviewActions.ts:32`);
  - `LANES` and `LANE_ORDER` add it after `regroup` (`lanes/index.ts:23-37`);
  - files: `lanes/authors.ts`, `lanes/useAuthorsLane.ts`, `AuthorsPanel.tsx`.
- **Sub-views**, through the lane's own segmented control: **Authors | Series | AI scans** (the last is G7).
- **Authors sub-view**:
  - rail: duplicate-author groups from `getAuthorDuplicates()`, with a refresh command → `refreshAuthorDuplicates()`, plus a second section listing `entity_type: 'author'` candidates from `getDedupCandidates({entity_type:'author'})`. That second section closes F5.
  - spine: one card per group with the canonical name (inline edit, suggested-name chip), variants with an Author/Narrator toggle and remove-from-merge, a book-count chip that opens a popover from `getBooksByAuthor`, a Validate button, and "Find real author" (production companies) or "Split" (composites) where they apply.
  - Behaviour is carried over verbatim from `DedupAuthorTab.tsx:395-547`.
- **Series sub-view**: the same shape over `getSeriesDuplicates()`, with inline rename, a narrator flag, Validate and Merge. Behaviour from `DedupSeriesTab.tsx:104-281`.
- **Actions** (total verbs): `mergeGroup`, `mergeSelected`, `mergeAllGroups` (confirm), `renameCanonical`, `splitComposite`, `resolveProduction`, `toggleRole`, `excludeVariant`.
  - Each action is typed with its entity kind (`author` or `series`), so a series id can never reach `mergeAuthors`.
- **Keyboard**: the same contract as dupes (`j`/`k`/`m`/`Enter`/`Esc`/`?`), gated on the lane being active.
- **Backend**: no new routes. Everything in Appendix A §A.2 rows "AL" is reused.

#### G7: AI author scans inside the Authors lane (C23-C27). Size M.

- **Sub-view "AI scans"**: the current scan's results, with agreement-filter chips that show counts and are clickable, a selection, and "Apply selected".
- **Scan picker**: a history list. It is not a drawer, so superseded nightly scans are visible without hunting.
- **Commands** in the lane's menu: "Start AI author scan" (realtime, or batch while the switch is on), and "Cancel scan".
- **Progress** goes through the notification bell (`startJob`). The page-local poll loop at `DedupAIReviewTab.tsx:100-120` is dropped: the bell already follows ops.
- Drop the Books placeholder (C28).

#### G8: Three Repairs fixers (C34, C36-C38, C67-C69). Size L in total.

These are Go and the Repairs registry, so they are backend work. They are listed here because they are the precondition for deleting the tabs. Each one ships a `repairs.Fixer` (trial → rows → apply) and needs **no new frontend code**: `RepairsPanel` renders any fixer generically.

| Fixer | Plan source | Apply | Replaces |
|---|---|---|---|
| `dedup.series-prune` | `SeriesPrunePreview` (`duplicates/handler.go:738`) | `dedup.series-prune` op | C34 |
| `reconcile.missing-files` | latest `reconcile.scan` preview (`/operations/reconcile/scan/latest`) | `reconcile.apply` with the picked rows | C36-C38. The "no candidate file" books become skipped rows that carry a reason, so they stay countable and clickable. |
| `dedup.split-books` | `ListSplitBookCandidates` | `dedup.split-book-merge` per row | C67-C69. **D21: port it, then measure.** Run its trial and the fragment fixer's trial on prod; if every split-book row is also a fragment "no parent" row, retire it and the CLI in a follow-up. |

**D27 dependency for `reconcile.missing-files`.** The plan source `/operations/reconcile/scan/latest` is served by `server/reconcile.go:68` (`latestReconcileScan` → `recentReconcileScans(store, 200)`), which reads saved scan results. 04's D27 pair "`maintenance.reconcile-scan` (nightly, saves) vs `reconcile.scan` (UI, previews)" keeps the newer `reconcile.scan`; that 04 PR must make the survivor persist its results the way the nightly one does, or PR 9b has no plan source. PR 9b is written after that 04 PR, against the survivor's op type.

After G8, the reconcile command's description (`ReviewWorkspace.tsx:375`) points to Repairs → "Missing files".

#### G9: Link repoint and redirect. Size S.

A new `DedupRedirect` element replaces the `/dedup` route. It maps `?tab=` and keeps `?book=` and `?band=`:

| Old | New |
|---|---|
| `/dedup`, `?tab=books`, `?tab=book-duplicates` | `/review?lane=dupes&layer=exact` |
| `?tab=embedding` | `/review?lane=dupes` (the Clusters layout is a view-mode toggle and is not seeded from the URL) |
| `?tab=acoustic` | `/review?lane=dupes&layer=acoustid` |
| `?tab=authors` | `/review?lane=authors` |
| `?tab=series` | `/review?lane=authors&view=series` |
| `?tab=ai` | `/review?lane=authors&view=ai` |
| `?tab=reconcile`, `?tab=split-books` | `/review?lane=repairs&fixer=<id>` |
| `/dedup/labels` | `/review?lane=dupes&view=labels` |
| `/authors/dedup`, `/books/dedup` | through the same element |
| no `?tab=`, or an unknown value (the legacy `tab=unified` that `FingerprintVisualsColumn.tsx:86` documents) | `/review?lane=dupes`, keeping `book` and `band`; `initialLaneFrom` (`ReviewWorkspace.tsx:165-170`) already lands on the dupes lane for either param, so a `?book=` deep link keeps working |

Every row keeps `?book=` and `?band=` verbatim. Unknown extra params are dropped, not forwarded, so a stale bookmark cannot seed a lane filter it never had.

Also in G9:

- Sidebar: drop "Dedup" and "Gold Labels" (`Sidebar.tsx:94,96`).
- Backend announcement (`system/handler.go:243`): change `Link` to `/review?lane=authors`. This is the one Go line in this package; it goes to the Go owner.
- `ReviewWorkspace.tsx:375` and `:387`: point them inside Review.
- Prerequisites:
  - `initialLaneFrom` (`ReviewWorkspace.tsx:165-170`) must accept `layer` and `view` as seeds.
  - Nothing reads a `fixer` URL param today: `grep -n searchParams` finds nothing in `lanes/useRepairsLane.ts` or `RepairsPanel.tsx`. PR 10 adds a `?fixer=<id>` seed that preselects the fixer in the Repairs list.

### 3.3 Compare-drawer closure to relocate

These files must move, not be deleted. Target: `web/src/components/review/compare/`.

| File | Why it stays |
|---|---|
| `CandidateCompareDrawer.tsx` | `DupesPanel.tsx:37` |
| `ScoreBadgeRow.tsx`, `BandFilterBar.tsx` | imported by the drawer (`BandFilterBar` for its band colour helper) |
| `FingerprintCanvas.tsx`, `FileInfoCompare.tsx`, `AudioSamplePair.tsx` | imported by the drawer |
| `FolderFilesChip.tsx` | `spine/DupesSpine.tsx:47` |
| `LabelToggle.tsx` | G1 |
| `__tests__/CandidateCompareDrawer.test.tsx`, `__tests__/BandFilterBar.test.tsx`, `__tests__/FolderFilesChip.test.tsx`, `FolderFilesChip.test.tsx`, `__tests__/LabelToggle.test.tsx` | move with their subjects |

`../AudioSampleCompare.tsx` (outside the folder) stays where it is.

## 4. Implementation plan

The gap PRs (1 to 9c) are independent of each other except where a "Needs" line says otherwise. Deletions (11-13) wait for **all** of them. Every frontend PR runs `npx --prefix web tsc --noEmit` and the Vitest suite. It follows the red-then-green rule: revert the fix, see the test fail, restore it, see the test pass.

### Phase 0: prepare

**PR 1. Move the compare-drawer closure.** Size S. `refactor(review): move compare drawer and chips under review/compare`.

- Files:
  - `git mv` the 8 component files and 5 tests in §3.3 to `web/src/components/review/compare/`;
  - edit `web/src/components/review/DupesPanel.tsx` and `web/src/components/review/spine/DupesSpine.tsx` (imports);
  - edit `web/src/components/dedup/DedupAcousticTab.tsx` and `DedupEmbeddingTab.tsx` (imports, until deletion);
  - bump every header.
- Tests: the existing tests move; `tsc`.
- Rollback: revert. Pure move, no behaviour change.

### Phase 1: close the gaps

**PR 2. G2: layer filter, layer chips, bulk keep-side, iTunes marker.** Size M.

- Files: `web/src/components/review/lanes/useDupesLane.ts`, `lanes/dupes.ts`, `reviewActions.ts`, `DupesPanel.tsx`, `spine/DupesSpine.tsx`, `ReviewWorkspace.tsx` (URL seed `layer`).
- Tests: `lanes/useDupesLane.test.ts` (layer param on list and bulk filter; page reset); `useDupesLane.selection.test.tsx` (keep-side refused off Pending); `ReviewWorkspace.test.tsx` (`?layer=` seed); `spine/DupesSpine.test.tsx` (iTunes chip).
- E2E: `web/tests/e2e/review-dupes-lane.spec.ts`, plus a layer-filter case.
- Rollback: revert. The new params are optional on the server.

**PR 3. G4: export.** Size S.

- Files: `web/src/components/review/ReviewWorkspace.tsx`; `services/api.ts` (a `dedupCandidatesExportUrl(filters)` helper lifted from `DedupEmbeddingTab.tsx:348-355`).
- Tests: `ReviewWorkspace.test.tsx` (the command is disabled outside dupes; the URL carries every filter).
- Rollback: revert.

**PR 4. G5: AcoustID key, online lookup, reset, drawer audio match.** Size S. Needs PR 1.

- Files: `web/src/components/settings/DedupSettingsSection.tsx`, `pages/Settings.tsx` (if its state shape needs the key), `components/review/ReviewWorkspace.tsx` (2 commands plus a confirm dialog), `components/review/compare/CandidateCompareDrawer.tsx`.
- Tests: `DedupSettingsSection` test (masked key saved); `ReviewWorkspace.test.tsx` (reset asks first); `compare/__tests__/CandidateCompareDrawer.test.tsx` (on-demand compare; error state rendered).
- Rollback: revert.

**PR 5. G1: Labels and Suspicious inside the Dupes lane.** Size M. Needs PR 1.

- Files: new `web/src/components/review/lanes/useDupesLabels.ts` and `DupesLabelsView.tsx`; edit `DupesPanel.tsx` and `ReviewWorkspace.tsx` (`view` seed; "Manage labels" switches the view instead of navigating). `pages/DedupLabels.tsx` stays until PR 11.
- Tests: port `web/src/pages/__tests__/DedupLabels.test.tsx` → `components/review/lanes/useDupesLabels.test.tsx` and `DupesLabelsView.test.tsx`, covering all 4 states and override.
- Rollback: revert. The old page is still routed.

**PR 6. G3: cluster view and cluster verbs.** Size L. Needs PR 2.

- Files: new `lanes/dupesClusters.ts` and `spine/DupesClusterSpine.tsx`; edit `lanes/useDupesLane.ts`, `lanes/dupes.ts`, `reviewActions.ts`, `DupesPanel.tsx`, `ReviewWorkspace.tsx` (`viewModeLabel`).
- Tests: `dupesClusters.test.ts` (components, primary choice, a cluster split across a page boundary); `DupesClusterSpine.test.tsx`; port the cluster cases from `components/dedup/__tests__/DedupEmbeddingTab.test.tsx`.
- Rollback: revert.

**PR 7a. G6: Authors lane scaffolding and the Authors sub-view.** Size M. (Was half of an L-sized PR 7; split in round 2 because the Series sub-view is a separate endpoint family with its own E2E block, and the lane must be reviewable before the second sub-view lands.)

- Files: new `lanes/authors.ts`, `lanes/useAuthorsLane.ts`, `AuthorsPanel.tsx` and `spine/AuthorGroupSpine.tsx`; edit `lanes/index.ts`, `reviewActions.ts`, `ReviewWorkspace.tsx`.
- Tests: port `components/dedup/__tests__/DedupAuthorTab.test.tsx` and the author part of `dedupTabs.selectAll.test.tsx` → `lanes/useAuthorsLane.test.ts` and `AuthorsPanel.test.tsx`. `pages/__tests__/BookDedup.validation.test.tsx` is **not** ported: appendix B drops it because it tests a local copy of `validateBookID`, not the product.
- E2E: rewrite `web/tests/e2e/dedup.spec.ts` "Author Dedup", "Book Preview Popover", "Dedup Refresh Operations", "Dedup Pagination" and "Dedup Bulk Actions", and `dedup-operations.spec.ts` "Production Company Resolution", "Dedup Operation Progress" and "Dedup Error Handling", against `/review?lane=authors` (Appendix B).
- Rollback: revert. The lane is additive.

**PR 7b. G6: Series sub-view.** Size S. Needs PR 7a.

- Files: `lanes/useAuthorsLane.ts` (series fetch and verbs, typed `series`), `AuthorsPanel.tsx` (segmented control gains "Series"), `spine/AuthorGroupSpine.tsx` (series card variant), `ReviewWorkspace.tsx` (`view=series` seed).
- Tests: series cases in `useAuthorsLane.test.ts` and `AuthorsPanel.test.tsx`; the series part of `dedupTabs.selectAll.test.tsx`.
- E2E: `dedup.spec.ts` "Series Dedup" against `/review?lane=authors&view=series`.
- Rollback: revert.

**PR 8. G7: AI scans sub-view.** Size M. Needs PR 7a.

- Files: `lanes/useAuthorsLane.ts` (or a new `lanes/useAIScans.ts`), `AuthorsPanel.tsx`.
- Tests: port `components/dedup/__tests__/DedupAIReviewTab.test.tsx`.
- Rollback: revert.

**PR 9a, 9b, 9c. G8: one Repairs fixer per PR.** Go side; owned outside `web/`. (Was one L-sized PR 9; split in round 2 because the three fixers share no code, each has its own rollback, and 9b is gated on a 04 PR that 9a and 9c are not.) They are v2 `repairs.Fixer`s and port to v3 in 05 wave 12D.

| PR | Fixer | Files | Tests | Gate | Size |
|---|---|---|---|---|---|
| 9a | `dedup.series-prune` | new `internal/plugins/maintenance/series_prune_fixer.go` + `_test.go`; registration in `plugin.go` | trial rows match `SeriesPrunePreview` (`duplicates/handler.go:738`); apply runs `dedup.series-prune`; a series referenced by any book is never a row | — | S |
| 9b | `reconcile.missing-files` | new `internal/plugins/maintenance/reconcile_fixer.go` + `_test.go`; `internal/plugins/maintenance/plugin.go`; `web/src/components/review/ReviewWorkspace.tsx:375` (the description text) | trial rows come from the latest saved scan; "no candidate file" books are skipped rows with a reason; apply runs `reconcile.apply` with the picked rows only; iTunes rows never written | **after 04's D27 PR for the reconcile pair** (§3.2 G8 note) | M |
| 9c | `dedup.split-books` | new `internal/plugins/maintenance/split_books_fixer.go` + `_test.go`; `internal/plugins/maintenance/plugin.go` | trial rows match `ListSplitBookCandidates`; apply runs `dedup.split-book-merge` per row; iTunes rows excluded the way `duplicate_copies_fixer.go:41-45` does | D21: ship, then run both trials on prod and record the overlap before deciding to retire | M |

- Shared: `RepairsPanel.test.tsx` fixture gains each new id in its own PR.
- Rollback: unregister the one fixer.

### Phase 2: repoint

**PR 10. G9: redirect, sidebar, announcement link.** Size S. Needs PRs 2, 5, 7a, 7b, 8, 9a, 9b and 9c.

- Files: `web/src/App.tsx` (new `DedupRedirect` replacing the `/dedup`, `/dedup/labels`, `/authors/dedup` and `/books/dedup` routes); new `web/src/pages/DedupRedirect.tsx` plus a test; `components/layout/Sidebar.tsx`; `components/review/ReviewWorkspace.tsx` (`initialLaneFrom` accepts `layer` and `view`); `components/review/lanes/useRepairsLane.ts` and `RepairsPanel.tsx` (`?fixer=` seed); `internal/server/handlers/system/handler.go:243` (Go owner).
- Tests: `DedupRedirect.test.tsx`, one case per row of the G9 table; `Sidebar` test; Go test asserting the announcement link.
- Rollback: revert. The old components are still in the tree.

### Phase 3: delete

**PR 11. Delete the frontend of the old page.** Size M.

- Files deleted:
  - `web/src/pages/BookDedup.tsx`, `web/src/pages/DedupLabels.tsx`;
  - `web/src/pages/__tests__/BookDedup.validation.test.tsx`, `web/src/pages/__tests__/DedupLabels.test.tsx` (already ported in PRs 5 and 7);
  - `web/src/components/dedup/` remainder: `DedupAcousticTab.tsx`, `DedupAdvancedScanTab.tsx`, `DedupAIReviewTab.tsx`, `DedupAuthorTab.tsx`, `DedupBookTab.tsx`, `DedupEmbeddingTab.tsx`, `DedupReconcileTab.tsx`, `DedupSeriesTab.tsx`, `DedupSplitBookTab.tsx`, `dedupHelpers.tsx` (BulkActionBar is deleted earlier by 01 P7);
  - tests: `DedupAcousticTab.selectAll.test.tsx`, `DedupAIReviewTab.test.tsx`, `DedupAuthorTab.test.tsx`, `DedupBookTab.test.tsx`, `DedupBookTab.selectAll.test.tsx`, `DedupEmbeddingTab.test.tsx`, `dedupTabs.selectAll.test.tsx` (each ported or dropped per Appendix B);
  - `web/src/components/common/BulkConfirmDialog.tsx`, if `grep` still shows no importer outside the deleted tabs;
  - the `services/api.ts` wrappers whose only callers were deleted: `getBookDuplicates`, `getBookDedupScanResults`, `scanBookDuplicates`, `linkBookDuplicatesAsVersions`, `rejectBookDuplicateGroup`, and `queueBulkSplitBookMerge` (if G8 does not use it). The four dead wrappers `triggerDedupRefresh`, `requestAIAuthorReview`, `applyAIAuthorReview` and `getReconcilePreview` are deleted by 01 P7, not here.
- E2E: delete `web/tests/e2e/dedup.spec.ts` "Dedup Tab Navigation"; its replacement redirect assertions landed in `review-dupes-lane.spec.ts` in PR 10. The `dedup-operations.spec.ts` "Scheduler Tasks for Dedup" block **moves** to an operations spec in PR 10 (appendix B.2); 04 keeps the `dedup_refresh` task on `dedup.author-scan` (`scheduler/maintenance.go:166`), so the block stays valid and is not deletable. After both moves, `dedup.spec.ts` and `dedup-operations.spec.ts` are empty and are deleted here.
- Rollback: revert. The redirect is already live, so nothing user-visible breaks.

**PR 12. Delete the backend routes that are now dead.** Size S. Go side.

- Files:
  - `internal/server/wire_dedup_routes.go:86-88,94-97` (the five routes and two aliases in Appendix A §A.3);
  - `internal/server/handlers/duplicates/handler.go` (`ListDuplicateAudiobooks`, `ListBookDuplicateScanResults`, `ScanBookDuplicates`, `LinkBookDuplicatesAsVersions`, `RejectBookDuplicateGroup`, plus their tests);
  - `internal/audiobooks/service_single.go:276` (`GetDuplicateBooks`) and the store method `GetDuplicateBooks` (`pebble_store.go:1786`, `iface_book.go:154`, mocks), **only if** no other caller remains (re-check with gopls `findReferences`).
- `scripts/api_examples.sh:136-145` ("Example 10: List duplicate audiobooks" curls GET `/audiobooks/duplicates`). Delete the example, or point it at GET `/dedup/candidates?layer=exact`.
- The op `dedup.book-scan` is left in place for 04 to decide.
- Rollback: revert.

**PR 13. Docs.** Size S.

- Files: `docs/AI-REFERENCE.md:339`; `docs/port-inventory-dupes.md` and `docs/port-inventory-phase7.md` (add a "superseded by 03-dedup-page-retirement" banner); a `changelog.d/` fragment; a `docs/executive-summaries/2026-10-…-dedup-page-retired-executive-summary.md`, since this qualifies as a multi-PR change.
- Rollback: revert.

## 5. Risks and what must not break

- **iTunes.**
  - No gap package writes to iTunes or removes anything there.
  - The G2 iTunes marker is read-only.
  - G8's split-books fixer must exclude iTunes rows the same way `duplicate-copies` does (`duplicate_copies_fixer.go:41-45`).
- **Irreversible links.**
  - Link-cluster and the bulk keep-side actions write version links that the lane cannot undo (`useDupesLane.ts` comment near `:737`).
  - Keep the `expected_total` / `FILTER_CHANGED` guard on every filter-scoped verb.
  - Keep sequential dispatch for selected rows (`runSequential`), so two merges can never touch one book.
- **Duplicate fetches.**
  - New lanes must follow the "seed once from the URL, never mirror" rule (`ReviewWorkspace.tsx:22-26`).
  - Writing `?view=` back on a click would double every gated fetch. This exact defect has shipped before.
- **Infinite spinner.** Every new fetch needs an `AbortController` and the four states. The author and series lists come from endpoints with no pagination today. On a big library the response size is the risk, not latency, so measure it before G6.
- **The cluster page boundary (G3)** can mislead: a cluster may look smaller than it is. The card must say so (§3.2 G3).
- **Removal order.** PR 11 must not merge before PR 10 is live. Otherwise the backend announcement and bookmarks land on a 404 inside the SPA.
- **Not deleting a route that has a non-browser caller.** Check every route in PR 12 against Appendix A §A.4 again at merge time.
- **Mac fingerprint workers (D2).** The G5 reset command queues `acoustid.fingerprint-rescan` for the whole library after wiping every fingerprint. Under D2 that is worker-side work; the server's decoders are guarded. The command must not be reachable while no worker is connected, and its dialog must name the cost. Nothing else in this plan enqueues fingerprinting: the Dupes lane's "Find acoustic duplicates" (C56) compares stored fingerprints only.
- **Hard bans respected:**
  - no `book_file` deletion;
  - no audio decoding added: G5 only moves buttons that queue existing ops, and C55 stays where it is;
  - no scan ConcurrencyKey change;
  - no `fix-library-states`.

## 6. Dependencies on other workstreams

- **04 (operations census).** Input, not decisions. Dedup ops reachable *only* from the old page:
  - `dedup.book-scan` (F13, obsolete);
  - `dedup.author-scan`, `dedup.series-scan`, `dedup.series-dedup`, `dedup.series-prune` and `ai.author-scan` (all kept, via G6, G7 and G8);
  - `dedup.split-book-scan` and `dedup.split-book-bulk-merge` (Q3; the CLI also uses the routes).
  - Also for 04: whether `acoustid.fingerprint-rescan` (C55) may run on the server at all, given the no-decode rule. **Answered by D2:** the server decoders are guarded and Fingerprint Books goes to the Mac workers.
  - Assumption: 04 keeps every op that G6, G7 and G8 reuse. **Round 2:** 04's D27 pairs touch two of them. `dedup.author-scan` survives its pair (it is the newer op and the `dedup_refresh` task target), so G6 is unaffected. `reconcile.scan` survives its pair, but the nightly `maintenance.reconcile-scan` is the op that saves the results PR 9b reads; 04's PR for that pair must carry the save over before PR 9b is written.
- **01 (dead code).**
  - `web/src/components/FingerprintVisualsColumn.tsx` and `web/src/components/dedup/BulkActionBar.tsx` (F4).
  - Frontend wrappers with no caller: `triggerDedupRefresh`, `requestAIAuthorReview`, `applyAIAuthorReview`, `getReconcilePreview`.
  - Routes with no web caller (F18): `/dedup/scan-book-signature`, `/dedup/refresh`, `/dedup/purge-legacy-fp`, `/dedup/embed-async`, `/dedup/lsh-index`, `/dedup/emb-reencode`, `/dedup/purge-acoustid-conflicts`, `/series/normalize` and its preview, and `/authors/duplicates/ai-review` and its apply.
  - All the deprecated verb aliases (`/merge`, `/dismiss`, `/bulk-merge`, `/merge-cluster`, `/dismiss-cluster`, `/merge-series`, `/audiobooks/merge`). Grep of `scripts/`, `tools/` and `cmd/` found no caller of the alias spellings; only audit docs mention them.
  - Assumption: 01 deletes these; this plan does not. **Confirmed by the coordinator:** 01 P7 (components and wrappers), 01 P72 (routes and aliases) and 01 P73 (the `MergeBooks` category C cluster).
- **02 (filters and search).** G2 adds a `layer` filter and G6 adds new lists. If 02 unifies the filter grammar (RE2 search, owner decision 2026-10-06), these lanes should use it. Assumption: the G2 layer filter is a plain enum param and is compatible.
- **05 (operations v3).** G7 drops a page-local poll loop in favour of the bell. Nothing else depends on it.
- **07 (design).** There is no UI for the merge and combine undo journals (§3.1, under "Categories the brief named").
- **07 (modularity).** Adding a fifth lane exercises the `LaneDescriptor` design (`lanes/types.ts:6-17`). If 07 proposes a lane registry or plugin shape, G6 should be built on it.

## 7. Open questions for the owner

All six are answered in `09-owner-decisions.md`; the text is kept for the record with the decision beside it.

| # | Question | Recommended answer | Decision |
|---|---|---|---|
| Q1 | Where do author and series dedup go: a new Review lane, or Repairs fixers? | **A new "Authors & series" lane (G6, G7).** These are judgement calls per group (canonical name, role, exclusions), not trial-then-approve fixes. Repairs suits mechanical fixes. | **D19: yes.** PRs 7a, 7b, 8. |
| Q2 | Should Gold Labels stay its own page (moved to `/review/labels`), or become a sub-view of the Dupes lane? | **A sub-view of the Dupes lane (G1).** It is the history of the decisions that lane makes, and "Manage labels" already sits in its menu. | **D20: yes.** PR 5. |
| Q3 | Keep the split-book detector as a Repairs fixer, or retire it in favour of the `fragment-consolidation` fixer? | **Port it as a fixer (G8) first, then measure.** Run both trials on prod. If every split-book row also appears in the fragment fixer's "no parent" rows, retire split-book and its CLI in a follow-up. The detector also covers a grandparent-folder shape that the fixer does not claim to cover (F15). | **D21: port, measure, then decide.** PR 9c. |
| Q4 | The cluster view is built per page. Is that acceptable for the first version, with a server-side clustering endpoint later? | **Yes**, provided each card states when pairs for its books are on other pages (§3.2 G3). | **D22: yes.** PR 6. |
| Q5 | Remove the "Fingerprint Books" button from dedup tooling entirely, since fingerprinting decodes audio? | **Yes, from the dedup surfaces.** Leave the existing Library button. Defer the server-decode question to 04. | **D2** settles the decode question: the server decoders are guarded and Fingerprint Books goes to the Mac workers. The button stays on Library only (C55). |
| Q6 | Bulk "keep A/B" means "keep the older/newer record" (§3.2 G2). Add a server-side `keep_side: "recommended"` that applies the lane's keep rule (`lanes/keepDecision.ts`) and refuses ties? | **Yes, as a follow-up to G2.** Ship older/newer labels first for parity, then add the recommended side, which is the verb a reviewer actually wants at scale. Server change: `handlers/dedup/handler.go:1281-1297`. | **D22: yes, as a follow-up.** |
