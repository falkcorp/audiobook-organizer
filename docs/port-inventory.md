<!-- file: docs/port-inventory.md -->
<!-- version: 1.1.0 -->
<!-- guid: 3e7b2f18-64a0-4c95-b1d7-8f2e5c0a9b43 -->
<!-- last-edited: 2026-08-20 -->

# Port inventory — `MetadataReviewDialog` → `CompareSpine` + workspace

PLAN.md Phase 5 says of this component's feature list: **"Nothing in this list may be
dropped in the port."** That sentence is only enforceable if the list exists somewhere
other than in the head of whoever is doing the porting, across a build that spans many
commits. This file is that mechanism.

Source: `web/src/components/audiobooks/MetadataReviewDialog.tsx` (2110 lines, at
`94440de4`). Phase 7 deletes it — **this checklist passing is the precondition for that
deletion**, not a nice-to-have alongside it.

How to use it: tick a row when the behaviour exists in the new surface *and* has a test
covering it. A row ticked because "the code was copied across" is not ticked — the port
changes the containing layout, and layout is where these silently stop working.

---

## 1. State (24 hooks)

| # | State | Line | Behaviour that must survive |
|---|---|---|---|
| 1 | `results` | 216 | Candidate list for the current server page |
| 2 | `loading` | 217 | Spinner vs content; must not flash on refetch |
| 3 | `totalCount` | 218 | Drives pagination maths, not just a label |
| 4 | `selectedIds` | 219 | `Set`, survives page changes within the session |
| 5 | `rowStates` | 220 | Per-row applied/rejected/skipped/error, keyed by id |
| 6 | `sourceFilter` | 223 | Filter to one metadata provider |
| 7 | `strictPreset` | 226 | **Persisted** — see §3 |
| 8 | `confidenceThreshold` | 227 | Derived from preset; hides low-score candidates |
| 9 | `viewMode` | 230 | `compact` \| `two-column` — the toggle Phase 5 extends with `auto` |
| 10 | `expandedId` | 231 | Single-open accordion; not multi-expand |
| 11 | `applying` | 232 | Disables the action bar during a batch |
| 12 | `summary` | 233 | matched / no_match / errors / total for the current page |
| 13 | `totalSummary` | 234 | Corpus-wide counts; distinct from `summary` |
| 14 | `previewCover` | 237 | Cover lightbox |
| 15 | `serverPage` | 240 | 1-indexed |
| 16 | `reviewPageSize` | 241 | **Persisted**, with correction-on-load — see §3 |
| 17 | `hideApplied` | 242 | Default **on** |
| 18 | `hideRejected` | 243 | Default **on** |
| 19 | `hideNoMatch` | 244 | Default **on** |
| 20 | `hideSkipped` | 245 | Default from storage |
| 21 | `hideMultiBook` | 253 | Hides grouped/ambiguous cards **and excludes them from Apply Selected** |
| 22 | `matchLanguage` | 256 | **Persisted**; books with no language set show all candidates |
| 23 | `titleFilter` | 257 | Client-side title substring filter |
| 24 | `onlyWithTranscription` | 258 | Only books with Whisper intro data |
| 25 | `onlyTranscriptionMatched` | 259 | Only books whose score was *boosted* by the transcription — **not** the same as 24 |
| 26 | `refreshKey` | 260 | Forces refetch without changing query params |
| 27 | `ungroupedIds` | 277 | Books manually separated from their group |

> 24 is PLAN.md's count; the actual total is 27 `useState` calls. The plan undercounted —
> port against this table, not against the number.

### Refs (deliberate non-state)
| Ref | Line | Why it is a ref |
|---|---|---|
| `applyWatchAliveRef` | 268 | Guards async completion after unmount |
| `fetchIdRef` | 291 | Discards out-of-order responses — a stale page must not overwrite a newer one |
| `hasChangesRef` | 295 | Decides whether closing triggers a parent refresh |
| `applyQueueRef` | 472 | Batches apply calls |
| `applyTimerRef` | 473 | Debounce handle for the above |

`fetchIdRef` and `applyWatchAliveRef` are the two that look like dead weight and are not.
Both exist because the dialog fires overlapping async work; the workspace fires more of it,
not less.

### Derivations (behaviour, not state — the two most likely to be silently rewritten)

Both are pure functions of `rowStates` and were **lifted** into
`web/src/components/review/spine/rowState.ts`, not reimplemented. Each contains an
asymmetry that reads as an oversight and is not:

- [x] `getRowSx` (:720) — `applied` and `skipped` get a background; **`rejected`
      deliberately does not**. A rejected row carries a "Rejected — click to undo" chip,
      and dimming the row would bury the undo affordance.
- [x] `isRowActionable` (:734) — **`skipped` stays actionable**. Skip means "not now", so
      the reviewer can return to it in the same session. The simplifying rewrite
      (`state !== undefined`) compiles, reads better, and makes every skip permanent with
      no error anywhere.

Covered by `spine/rowState.test.ts`, which asserts both asymmetries directly so that
changing either is a deliberate act rather than a tidy-up.

---

## 2. Handlers

- [ ] `handleApplyOne` — apply a single candidate
- [ ] `handleBulkApply` — apply all selected
- [ ] `handleReject` / `handleUnreject` — reject is **undoable** (see the "click to undo" chip)
- [ ] `handleSkip` — skip is **undoable** (same pattern)
- [ ] `handleRejectGroup` — reject an entire multi-book group
- [ ] `handleSkipAllUnmatched` — bulk skip
- [ ] `handleUngroup` — "Separate from group"; feeds `ungroupedIds`
- [ ] `toggleSelected` — selection
- [ ] `handleClose` — refreshes the parent only when `hasChangesRef` is set
- [ ] `handleApplyError` — per-row error state, not a global toast
- [ ] `runApplyOp` / `flushApplyQueue` — the debounced batch pipeline

---

## 3. Persistence (`STORAGE_KEYS`, localStorage)

- [ ] `METADATA_REVIEW_LANGUAGE_FILTER` (:79)
- [ ] `METADATA_REVIEW_STRICT_PRESET` (:164)
- [ ] `METADATA_REVIEW_PAGE_SIZE` (:178) — **loads with correction**: an out-of-range stored
      value is clamped and rewritten. Comment at :141 records why: a bad value could not be
      fixed from the UI, and the only escape was clearing localStorage by hand. Porting the
      read without the correction reintroduces a bug that was already fixed once.

---

## 4. Renderers (become `CompareSpine` view modes)

- [ ] `renderGroupedCard` (:719) — multi-book grouping, "Skip All"
- [ ] `renderCompactRow` (:862) — dense single-line
- [ ] `renderTwoColumnCard` (:1157) — current vs proposed
- [ ] dispatch between them (:1755)
- [ ] `<ToggleButtonGroup>` (:1536-1545) — the user's explicit choice, **preserved**
- [ ] **New:** `auto` mode collapsing on `container-type: inline-size`. The existing
      `renderTwoColumnCard` uses `Stack direction="row"` with `flex: 1 / flex: 1` and has
      *no* responsive collapse — it squishes. This is the one fix the plan authorises.

---

## 5. API surface

- [ ] `api.batchApplyFromCache`
- [ ] `api.markNoMatch`
- [ ] `api.clearMetadataNoMatch`

---

## 6. Copy worth preserving verbatim

These tooltips explain non-obvious semantics and took real thought; rewriting them from
scratch during a port loses information that is not recoverable from the code:

- [ ] `hideMultiBook`: "Hide any book that shares a match with another book … Turning this
      on leaves only the straightforward one-book-one-match rows, and takes the hidden books
      out of Apply Selected too." — note the second clause, which is a *behaviour*, not a
      description.
- [ ] `matchLanguage`: "Books without a language set still show all candidates."
- [ ] `onlyWithTranscription` vs `onlyTranscriptionMatched`: has transcription data vs the
      score was boosted by it.

---

## 7. New in the port (not carried, added)

- [ ] `EvidencePanel` on the metadata lane — `metadataEvidence(candidate)` renders the
      recorded scoring waterfall. The dialog never had this; it is the reason the backend
      instrumentation in `5fb7f5f6` / `94440de4` exists.
