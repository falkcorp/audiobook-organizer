<!-- file: docs/proposals/2026-10-holistic/03-dedup-page-retirement/A-endpoint-inventory.md -->
<!-- version: 1.1.0 -->
<!-- guid: 5e2c7a90-1d4b-4f63-a8e7-c03b9d6f1a24 -->
<!-- last-edited: 2026-10-08 -->

# Appendix A: endpoint inventory of the old /dedup page

Parent: [`../03-dedup-page-retirement.md`](../03-dedup-page-retirement.md). Measured at HEAD `f7211eb39`.

## A.1 Method

1. **API wrappers.** Every `api.<fn>(` call in `web/src/pages/BookDedup.tsx`, `web/src/pages/DedupLabels.tsx` and `web/src/components/dedup/*.tsx` was collected. `DedupLabels.tsx` uses raw `apiFetch` at `:325,335,365,381,511`.
2. **Other callers.** For each wrapper, every other file in `web/src/` that names it was listed, split into production files and tests. Script: `callers.py` in the session scratchpad. It is a word-boundary regex over `.ts`/`.tsx`.
3. **Route mapping.** Each wrapper was mapped to its route by reading its body in `web/src/services/api.ts` (`routes.py`).
4. **Non-browser callers.** Each route was grepped across `scripts/`, `tools/`, `cmd/`, `internal/`, `docs/` (excluding `docs/archive`) and `tests/audiobooth-decode/`.
5. **AudioBooth.** `tests/audiobooth-decode/manifest.json` lists 40 distinct `/api/...` paths and contains `api/v1` **0** times. The ABS routes mount on `s.router.Group("")` (`internal/server/wire_abs_routes.go:596`); every route below mounts under `/api/v1` (`internal/server/server_lifecycle.go:1422`). **No route here is an AudioBooth dependency.** Confidence: high.

## A.2 Every route the old page calls

Column meanings:

- **Other web caller**: a production file outside the old page.
- **Non-web caller**: a script, CLI tool or runbook.
- **Verdict**:
  - **keep**: still used elsewhere;
  - **reuse**: a gap package needs it;
  - **dead**: delete in PR 12;
  - **dead after Gn**: no web caller once that gap package lands, if it calls internals directly.

| Route | Registered at | Old-page caller | Other web caller | Non-web caller | Verdict |
|---|---|---|---|---|---|
| GET `/audiobooks/duplicates` | `wire_dedup_routes.go:86` | `DedupBookTab.tsx:120` | — | — | **dead** |
| GET `/audiobooks/duplicates/scan-results` | `:87` | `DedupAdvancedScanTab.tsx:56` | — | — | **dead** |
| POST `/audiobooks/duplicates/scan` (op `dedup.book-scan`) | `:88` | `DedupAdvancedScanTab.tsx:71` | — | — | **dead** (the op goes to 04) |
| POST `/audiobooks/duplicates/link`, plus alias `/merge` | `:94-95` | `DedupAdvancedScanTab.tsx:90` | — | — (only audit docs name the alias) | **dead** |
| POST `/audiobooks/duplicates/reject`, plus alias `/dismiss` | `:96-97` | `DedupAdvancedScanTab.tsx:101` | — | — | **dead** |
| POST `/audiobooks/link` | `:100` | `DedupBookTab.tsx:153,195` | `pages/Library.tsx` (`linkBooks`) | — | keep |
| GET `/authors/duplicates`, POST `/authors/duplicates/refresh` | `:98-99` | `DedupAuthorTab.tsx:363,367` | — | — | reuse (G6) |
| POST `/authors/merge` | `wire_entities_routes.go:29` | `DedupAuthorTab.tsx:481,507,542` | `pages/Authors.tsx:570` | `scripts/dedup_bench_apply.py:17` | keep |
| PUT `/authors/:id/name` | `wire_entities_routes.go:31` | `DedupAuthorTab.tsx:398,903` | `pages/Authors.tsx:493` | `scripts/dedup_bench_apply.py:18` | keep |
| POST `/authors/:id/split` | `wire_entities_routes.go:32` | `DedupAuthorTab.tsx:419` | `pages/Authors.tsx:640` | `scripts/dedup_bench_apply.py:19` | keep |
| POST `/authors/:id/reclassify-as-narrator` | `wire_entities_routes.go:30` | `DedupAuthorTab.tsx:425,469`; `DedupSeriesTab.tsx:194` | — | `scripts/dedup_bench_apply.py:20` | keep (script) and reuse (G6) |
| POST `/authors/:id/resolve-production` | `wire_entities_routes.go:33` | `DedupAuthorTab.tsx:1014` | — | — | reuse (G6) |
| GET `/audiobooks?author_id=` (`getBooksByAuthor`) | generic list | `DedupAuthorTab.tsx:164` | (generic route) | — | keep |
| POST `/dedup/validate` | `wire_dedup_routes.go:119` | `DedupAuthorTab.tsx:441`; `DedupSeriesTab.tsx:107` | — | — | reuse (G6) |
| GET `/series/duplicates`, POST `/series/duplicates/refresh` | `:111-112` | `DedupSeriesTab.tsx:150,153` | — | — | reuse (G6) |
| POST `/series/deduplicate` | `:113` | `DedupSeriesTab.tsx:281` | — | — | reuse (G6, "merge all") |
| POST `/series/merge` | `:114` | `DedupSeriesTab.tsx:213,245` | `pages/Series.tsx:589` | — | keep |
| GET `/series/prune/preview`, POST `/series/prune` | `:115-116` | `DedupSeriesTab.tsx:305,325` | — | — | **dead after G8** |
| PATCH `/series/:id` (`updateSeriesName`) | `wire_entities_routes.go:52` | `DedupSeriesTab.tsx:120` | — (`Series.tsx` uses the undoable PUT `/series/:id/name` instead, `api.ts:2349-2354`) | — | **dead after G6**, if G6 uses `renameSeries` (recommended: it is undoable) |
| `/ai/scans` (POST, GET), `/ai/scans/:id`, `/:id/results`, `/:id/apply`, `/:id/cancel` | `wire_media_routes.go:52-62` | `DedupAIReviewTab.tsx:77-232` | — | — | reuse (G7) |
| POST `/operations/reconcile/scan` | `server_lifecycle.go:1511` | `DedupReconcileTab.tsx:109` | `ReviewWorkspace.tsx:376` | — | keep |
| GET `/operations/reconcile/scan/latest`, POST `/operations/reconcile` | `server_lifecycle.go:1510,1512` | `DedupReconcileTab.tsx:64,87,125` | — | — | **dead after G8** |
| GET `/dedup/candidates` | `wire_dedup_routes.go:26` | `DedupEmbeddingTab.tsx:255`; `DedupAcousticTab.tsx:616` | `lanes/useDupesLane.ts` | — | keep |
| GET `/dedup/stats` | `:30` | `DedupEmbeddingTab.tsx:237` | `lanes/useDupesLane.ts` | runbook `docs/specs/2026-09-20-…:37` | keep |
| POST `/dedup/candidates/:id/link`, `/:id/reject` | `:41,43` | `CandidateCompareDrawer.tsx`; `DedupAcousticTab.tsx:755,772` | `lanes/useDupesLane.ts` | — | keep |
| POST `/dedup/candidates/bulk-link`, `/bulk-reject`, `/bulk-reject/revert`, `/bulk-count` | `:45,48-49,51` | `DedupAcousticTab.tsx:957-1026`; `DedupEmbeddingTab.tsx:589` | `lanes/useDupesLane.ts` | — | keep |
| POST `/dedup/candidates/link-cluster`, `/reject-cluster`, `/remove-from-cluster` | `:52,54,56` | `DedupEmbeddingTab.tsx:364,377,393,434` | — | — | reuse (G3) |
| GET `/dedup/candidates/series-summary`, POST `/link-series` | `:57,64` | `DedupEmbeddingTab.tsx:315,328` | — | — | reuse (G3) |
| GET `/dedup/candidates/export` | `:29` | `DedupEmbeddingTab.tsx:348-355` (download link) | — | — | reuse (G4) |
| POST `/dedup/scan`, `/scan-llm`, `/scan-acoustid`, `/embed` | `:66-68,75` | `DedupEmbeddingTab.tsx:481-547`; `DedupAcousticTab.tsx:726` | `ReviewWorkspace.tsx:310-362` | — | keep |
| POST `/dedup/purge-stale` | `:72` | `DedupAcousticTab.tsx:794` | `ReviewWorkspace.tsx:541` | — | keep |
| POST `/dedup/reset-acoustid` | `:74` | `DedupAcousticTab.tsx:883` | — | — | reuse (G5) |
| POST `/audiobooks/:id/compare-acoustid` | `:69` | `DedupAcousticTab.tsx:250` | — | — | reuse (G5) |
| POST `/dedup/fingerprint-rescan` | `server_lifecycle.go:1479` | `DedupAcousticTab.tsx:713` | `pages/Library.tsx` | — | keep |
| POST `/operations/v2` (def `acoustid.lookup-online`) | generic | `DedupAcousticTab.tsx:825` | generic | — | keep; reuse (G5) |
| PUT `/config` (`acoustid_api_key`) | generic | `DedupAcousticTab.tsx:861` | Settings and others | — | keep; reuse (G5) |
| GET `/dedup/split-book-candidates`, POST `/split-book-scan`, `/:id/merge`, `/bulk-merge` | `wire_library_routes.go:49-52` | `DedupSplitBookTab.tsx:183,208,247,288` | — | `tools/cmd/merge-split-books/main.go` (candidates, scan) | keep (CLI); reuse (G8 if kept) |
| GET `/dedup/labels`, `/labels/stats`, `/labels/suspicious`; POST `/labels/:id/override` | `wire_dedup_routes.go:59-63` | `pages/DedupLabels.tsx:325-511` | — | runbook `docs/plans/2026-07-12-dedup-clean-remeasurement-runbook.md:37-42` (stats, export) | reuse (G1) |
| GET `/audiobooks/:id`, `/audiobooks/:id/files` | generic | `DedupEmbeddingTab.tsx:64,77`; `FolderFilesChip.tsx` | many | — | keep |

## A.3 Delete list for PR 12

Only routes the old page called **exclusively**, that no gap package reuses, and that have no non-web caller:

1. GET `/audiobooks/duplicates`: handler `duplicates.ListDuplicateAudiobooks` (`internal/server/handlers/duplicates/handler.go:135`).
2. GET `/audiobooks/duplicates/scan-results`: handler `ListBookDuplicateScanResults` (`:167`).
3. POST `/audiobooks/duplicates/scan`: handler `ScanBookDuplicates` (`:239`).
4. POST `/audiobooks/duplicates/link`, plus the DEPRECATED alias `/merge`: handler `LinkBookDuplicatesAsVersions` (`:405`).
5. POST `/audiobooks/duplicates/reject`, plus the DEPRECATED alias `/dismiss`: handler `RejectBookDuplicateGroup` (`:479`).

That is **5 routes plus 2 aliases**.

One non-browser reference exists: `scripts/api_examples.sh:140` (Example 10) curls GET `/audiobooks/duplicates`. It is a stale example script, not a client. PR 12 removes or repoints the example.

The search behind this list grepped the prefix-less paths (`audiobooks/duplicates`, `series/prune`, `operations/reconcile`, `reconcile/scan/latest`) across `scripts/`, `tools/`, `cmd/`, `.claude/`, `.github/`, `AGENTS.md` and `Makefile*`. That catches scripts that build URLs as `f"{server}/api/v1{endpoint}"`. It found that one hit and nothing else.

Possible follow-on deletions, each to be confirmed with gopls `findReferences` at merge time:

- `AudiobookService.GetDuplicateBooks` (`internal/audiobooks/service_single.go:276`);
- `Store.GetDuplicateBooks` (`internal/database/pebble_store.go:1786`, `iface_book.go:154`, `mock_store.go:1179`, `mocks/mock_store.go:17076`);
- the `dedupCache` keys `book-duplicates` and `book-dedup-scan`.

**Conditional**: these are dead only once their gap package lands *and* that package calls internals rather than the route:

- `/series/prune/preview`, `/series/prune` (after G8);
- GET `/operations/reconcile/scan/latest`, POST `/operations/reconcile` (after G8);
- PATCH `/series/:id` (after G6, if it uses `renameSeries`).

## A.4 Routes kept because something outside the browser calls them

| Route | Caller |
|---|---|
| `/dedup/split-book-candidates`, `/dedup/split-book-scan` | `tools/cmd/merge-split-books/main.go` |
| `/authors/merge`, `/authors/:id/name`, `/authors/:id/split`, `/authors/:id/reclassify-as-narrator`, `/authors/:id/aliases` | `scripts/dedup_bench_apply.py:17-21` |
| `/dedup/labels/stats`, `/dedup/labels/export` | `docs/plans/2026-07-12-dedup-clean-remeasurement-runbook.md:37-42` |
| GET `/audiobooks/duplicates` | `scripts/api_examples.sh:140` (an example only; removed in PR 12) |
| `/dedup/rescore` | named as the manual remedy in `internal/config/update_service.go:39,802` |

## A.5 Frontend component closure (who imports what)

Command: the scratch script `importers.py`. It is a regex over `from '…/<Module>'` across `web/src/`.

| Module | Production importers | Disposition |
|---|---|---|
| `CandidateCompareDrawer` | `review/DupesPanel.tsx` | move (PR 1) |
| `ScoreBadgeRow` | `CandidateCompareDrawer` | move |
| `BandFilterBar` | `ScoreBadgeRow` | move |
| `FingerprintCanvas`, `FileInfoCompare`, `AudioSamplePair` | `CandidateCompareDrawer` | move |
| `FolderFilesChip` | `review/spine/DupesSpine.tsx` | move |
| `LabelToggle` | `pages/DedupLabels.tsx` | move (G1) |
| `dedupHelpers` | 5 dedup tabs only | delete |
| `BulkActionBar` | **none** (test only) | delete (already dead; 01) |
| `FingerprintVisualsColumn` (outside the folder) | **none** | delete (already dead; 01) |
| `BulkConfirmDialog` (common) | 3 dedup tabs only | delete if still unimported after PR 11 |
| `SelectAllMatchingBanner`, `useServerMatchingCount` | dedup tabs plus `review/DupesPanel.tsx`, `review/lanes/useDupesLane.ts` | keep |
| `FilterTagBar` | `DedupEmbeddingTab` plus `pages/Library.tsx` | keep |
| `AudioSampleCompare` | `AudioSamplePair`, `DedupEmbeddingTab` | keep (it follows `AudioSamplePair`) |
