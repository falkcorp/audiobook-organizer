<!-- file: docs/port-inventory.md -->
<!-- version: 1.3.0 -->
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

`[~]` marks a row that was **deliberately not ported**, with the reasoning recorded. That
is a different thing from an unticked row, and collapsing the two is how a considered
decision turns into an unnoticed regression six months later.

### Status

| Section | State |
|---|---|
| 1 — State | in `lanes/useMetadataLane.ts`; the two derivations lifted to `spine/rowState.ts` |
| 2 — Handlers | done, except `handleClose`, which is a deliberate drop (`[~]`) |
| 3 — Persistence | done, all three loaders lifted verbatim |
| 4 — Renderers | done, including the three-position view toggle |
| 5 — API surface | done |
| 6 — Copy | done |
| 7 — New in the port | done |

**Every section now passes.** The precondition PLAN.md sets for the Phase 7 deletions is
met: `MetadataReviewDialog.tsx`, the superseded search dialogs, and the legacy dedup
surfaces can be removed. That deletion is its own change, not a rider on this one.

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

All of these now live in `lanes/useMetadataLane.ts` as `dispatch` cases over the
`MetadataAction` union. The switch has no `default`, so an action added to the union
and not handled is a compile error rather than a button that silently does nothing.

- [x] `handleApplyOne` — apply a single candidate (optimistic, then debounced)
- [x] `handleBulkApply` — apply all selected
- [x] `handleReject` / `handleUnreject` — reject is **undoable**; the undo rides on
      the toast, asserted in `useMetadataLane.test.ts`
- [x] `handleSkip` — split into `skip` / `unskip`, so the Skip button and the
      "Skipped" chip no longer call one function meaning opposite things
- [x] `handleRejectGroup` — reject an entire multi-book group (a dispatched
      `rejectGroup` carrying only the still-actionable ids)
- [x] `handleSkipAllUnmatched` — bulk skip; tested to touch `no_match`/`error` only
- [x] `handleUngroup` — "Separate from group"; now scoped to the page it was
      performed on rather than reset by an effect
- [x] `toggleSelected` — selection, via `spineCtx.onToggleSelect`
- [x] `handleApplyError` — the auth-bounce path that must NOT claim "nothing was
      applied" (see the comment; measured against a 2-minute production apply)
- [x] `runApplyOp` / `flushApplyQueue` — the debounced batch pipeline, tested to
      coalesce rapid single applies into one call
- [~] `handleClose` — **deliberately dropped, not ported.** It exists because a
      dialog closes and has exactly one moment to tell the library to refresh.
      A route does not close. The workspace refreshes when the apply operation
      actually finishes, which is strictly more accurate: the dialog's version
      fired on close whether or not the background op had done anything yet.
      `hasChangesRef` goes with it, for the same reason.

---

## 3. Persistence (`STORAGE_KEYS`, localStorage)

All three loaders were **lifted verbatim** into `lanes/useMetadataLane.ts`.

- [x] `METADATA_REVIEW_LANGUAGE_FILTER` (:79)
- [x] `METADATA_REVIEW_STRICT_PRESET` (:164) — the preset sets its three members
      together; tested both directions, including that switching off returns the
      threshold to `DEFAULT_CONFIDENCE` rather than leaving it at 190
- [x] `METADATA_REVIEW_PAGE_SIZE` (:178) — **loads with correction**: an out-of-range stored
      value is clamped and rewritten. Comment at :141 records why: a bad value could not be
      fixed from the UI, and the only escape was clearing localStorage by hand. Porting the
      read without the correction reintroduces a bug that was already fixed once.
      Tested directly, including that the correction is *persisted* and not merely
      applied for the session.

---

## 4. Renderers (become `CompareSpine` view modes)

Ported into `spine/CompareSpine.tsx` by **mechanical substitution** of closure
references, not by rewriting: `rowStates.get(id)` → `ctx.rowState(id)`,
`handleApplyOne(id)` → a dispatched action, and so on. The JSX is otherwise untouched.
Line numbers below are PLAN.md's; the actual bodies are :739-974, :976-1318, :1320-1587.

- [x] `renderGroupedCard` → `GroupedCard` — multi-book grouping, "Skip All", per-book
      "Separate from group", All Applied / All Rejected states
- [x] `renderCompactRow` → `CompactRow` — dense single-line, plus its expanded
      current-vs-proposed detail
- [x] `renderTwoColumnCard` → `TwoColumnCard` — current vs proposed
- [x] dispatch between them — now `CompareSpine`'s `viewMode` switch
- [x] `<ToggleButtonGroup>` — now in the workspace toolbar, with **three** positions:
      the dialog's Compact and Two-Column, plus `auto`. Tested to actually drive the
      spine's `data-view-mode`, which nothing did while the shell was missing.
- [x] **New:** `auto` mode on `container-type: inline-size`, declared on the spine itself
      so the query measures the spine and not the row. jsdom cannot evaluate container
      queries — the declaration is asserted, the reflow is a visual-harness question.

**One semantic change, deliberate:** the dialog's `handleSkip` (:592) toggled
`'skipped' ↔ 'pending'`, so the Skip button and the "Skipped" chip called the same
function and meant opposite things. The port dispatches `skip` and `unskip`. Net
behaviour identical; intent now readable at the call site.

---

## 5. API surface

- [x] `api.batchApplyFromCache` — via `runApplyOp`; asserted to be called once for a
      debounced pair rather than once per row
- [x] `api.markNoMatch` — the reject path
- [x] `api.clearMetadataNoMatch` — the undo path, asserted through the toast action

---

## 6. Copy worth preserving verbatim

These tooltips explain non-obvious semantics and took real thought; rewriting them from
scratch during a port loses information that is not recoverable from the code:

- [x] `hideMultiBook`: "Hide any book that shares a match with another book … Turning this
      on leaves only the straightforward one-book-one-match rows, and takes the hidden books
      out of Apply Selected too." — note the second clause, which is a *behaviour*, not a
      description.
- [x] `matchLanguage`: "Books without a language set still show all candidates." The
      *behaviour* behind it is tested too: an unknown language on either side shows the
      row rather than hiding it.
- [x] `onlyWithTranscription` vs `onlyTranscriptionMatched`: has transcription data vs the
      score was boosted by it. Both carried, with the distinction stated in the tooltip.

---

## 7. New in the port (not carried, added)

- [x] `EvidencePanel` on the metadata lane — `metadataEvidence(candidate)` renders the
      recorded scoring waterfall. The dialog never had this; it is the reason the backend
      instrumentation in `5fb7f5f6` / `94440de4` exists.

      Wired in `spine/CompareSpine.tsx` as `EvidenceSection`, in both places a reviewer
      judges a candidate: the compact row's expanded detail, and the two-column card
      (where it needs no expand, since that card already shows everything). Tested
      through the workspace, including the case where a candidate predates the
      instrumentation and has no breakdown — the panel says so rather than rendering
      blank, because a blank panel reads as "no signals fired".
