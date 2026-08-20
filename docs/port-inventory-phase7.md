<!-- file: docs/port-inventory-phase7.md -->
<!-- version: 2.0.0 -->
<!-- guid: b0d4917c-52ae-4f38-9c61-7e2038ab5d19 -->
<!-- last-edited: 2026-08-20 -->

# Phase 7 inventory — what actually gets deleted

Written before the deletions, like the three port inventories before it. It exists
because **PLAN.md's Phase 7 list is wrong in two places**, and following it
literally would remove working features that nothing replaces.

The rule applied throughout: a surface is deleted when the workspace does *its
job*, not when it is merely *about the same subject*.

---

## 1. Delete — genuinely superseded

| Surface | Replaced by | Evidence |
|---|---|---|
| `components/audiobooks/MetadataReviewDialog.tsx` (2,110 lines) + 3 test files | metadata lane | Both call the cached-review path; the lane's data layer was lifted from it |
| `pages/ReviewQueue.tsx` (374 lines) + its test | regroup lane | **Already unrouted** — `App.tsx` mentions it only in a comment |
| `components/dedup/UnifiedDedupTab.tsx` | dupes lane | Only dedup tab whose API surface the workspace covers (below) |
| `dedup_show_legacy` toggle (`BookDedup.tsx:65,74`) | — | Exists only to switch between UnifiedDedupTab and the rest |

### UnifiedDedupTab's API surface vs. the workspace

Every call it makes has a home: `getDedupCandidates`, `mergeDedupCandidate`,
`dismissDedupCandidate`, `bulkMergeDedupCandidates`, `rescoreDedupCandidates`,
`triggerDedupScan`, `triggerEmbedScan`, `triggerDedupLLM`, `triggerDedupAcoustID`.

The one exception is `triggerFingerprintBackfill`, which the workspace does not
offer — but it is also reachable from `DedupAcousticTab` and `pages/Library.tsx`,
so deleting this tab loses no reachable function.

---

## 2. KEEP — PLAN.md is wrong

### 2.1 The metadata search dialogs are not superseded

PLAN.md:451 says to delete "`MetadataSearchDialog` / `BulkMetadataSearchDialog`
entry points superseded by the Metadata lane".

They are not superseded. They do the opposite job:

| | calls | role |
|---|---|---|
| `MetadataSearchDialog`, `BulkMetadataSearchDialog` | `searchMetadataForBook` | **populate** candidates |
| `MetadataReviewDialog` → metadata lane | `batchApplyFromCache` | **review** cached candidates |

These two files are the **only** callers of `searchMetadataForBook` in the entire
frontend, and nothing under `components/review/` calls it at all. Deleting them
would remove the only way to fetch metadata for a book — the very thing that fills
the cache the metadata lane exists to review. The lane would slowly empty and
there would be no way to refill it.

**Kept.** Wiring a search action into the workspace is a real follow-up, but it is
a feature, not a deletion, and it is not this phase.

### 2.2 "Collapse BookDedup's nine legacy tabs" is wrong by eight

PLAN.md:453. Only `UnifiedDedupTab` overlaps the dupes lane. The others are
different domains entirely, and nothing in `/review` touches their endpoints:

| Tab | Does what the lane does not |
|---|---|
| `DedupAuthorTab` | merge/rename authors, split composites, reclassify narrator |
| `DedupSeriesTab` | series dedup, prune + preview, rename |
| `DedupSplitBookTab` | split-book candidates |
| `DedupReconcileTab` | reconcile scans |
| `DedupAIReviewTab` | AI scan lifecycle (start/cancel/apply) |
| `DedupEmbeddingTab` | **clusters** and series-level dedup — `mergeDedupCluster`, `listDedupCandidateSeries` |
| `DedupAcousticTab` | AcoustID compare, fingerprint reset, online lookup, config |
| `DedupBookTab`, `DedupAdvancedScanTab` | `getBookDuplicates`/`mergeBooks`, version-group merging |

The dupes lane handles **book-pair duplicate candidates**. It does not do author
dedup, series dedup, clustering, or AcoustID administration.

**Kept.** What goes is the *legacy framing* — with the toggle gone these are not
"the legacy view", they are the dedup tools, and the page should stop calling them
legacy.

---

## 3. Also in scope

- `BookDetailStatusAlerts.tsx:104` links to `/dedup/candidates`, which is **not a
  registered route** — a dead link today, unrelated to any deletion. Point it at
  the dupes lane.
- `unified-dedup-tab.spec.ts`: its legacy-toggle test dies with
  `dedup_show_legacy`. Its other assertions describe behaviour the lane
  reproduces and should move rather than be deleted.

---

## 4. Left owing after this phase

- **No metadata search in the workspace.** §2.1 — the dialogs remain the only
  entry point. The honest next feature.
- The `MetadataPanel` lift that would make the metadata lane structurally match
  dupes and regroup.

---

## 5. What executing it actually turned up

Written after the fact. Three things were not visible from reading the code.

### 5.1 The `?book=` deep link could never have worked

`ReviewWorkspace` opened on `metadata` unconditionally, and each lane gates its
fetch on being the visible one. So a `?book=` link opened the metadata lane and
left `useDupesLane` inactive — the server-side entity filter shipped for exactly
this link never ran. Every lane test clicked its way in first, so none of them
exercised arrival.

Fixed by seeding the lane from the URL at mount. Deliberately seeded, not
mirrored: writing `?lane=` on a tab click would make lane state derive from a
param the click sets, firing every gated fetch twice per switch — the defect
removed from `DupesPanel` one phase earlier.

### 5.2 Rescore was announcing a dry run as the real thing

The workspace command bar labelled a command plain "Rescore", called
`rescoreDedupCandidates(false)`, and toasted "Rescore started". It wrote nothing.
`UnifiedDedupTab` was the only other caller and the only route to `apply=true`,
so deleting it would have made the apply path unreachable from the whole
frontend while the remaining button kept claiming to do the job.

Now two commands: `Rescore (dry run)` and `Rescore and apply…`, the latter behind
a confirmation, which is what the deleted dialog required.

### 5.3 `/review` had no end-to-end coverage at all

`unified-dedup-tab.spec.ts` had five tests against the surface being deleted;
the surface replacing it had none. Deleting the spec would have traded five
passing tests for a blind spot on the screen this project exists to build.

Rewritten as `review-dupes-lane.spec.ts` against `/review?lane=dupes`. The mocks
moved untouched — both surfaces call the same `/api/v1/dedup/*` endpoints and
share the comparison drawer component. Only the legacy-toggle test was genuinely
deleted, replaced by a deep-link test covering §5.1.

### 5.4 The `?reviewOp` chain

`OperationsIndicator` sent `window.location.href = '/library?reviewOp=<id>'` —
a full SPA reload — and `Library` read the param as a boolean to open the dialog.
The op id was never used for anything but its own presence. The whole chain
(param, ref, URL-preservation) is gone; the button navigates to `/review`.
