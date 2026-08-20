<!-- file: docs/port-inventory-dupes.md -->
<!-- version: 1.0.0 -->
<!-- guid: c93b313e-8cfd-4ecc-8f28-443b071cd080 -->
<!-- last-edited: 2026-08-20 -->

# Port inventory — `UnifiedDedupTab` → the dupes lane

Companion to `docs/port-inventory.md`, which covered `MetadataReviewDialog`. That
inventory is the reason the metadata port did not lose anything; this surface is
1437 lines and had no equivalent until now.

`[ ]` not yet ported · `[x]` ported and covered by a test · `[~]` deliberately not
ported, with the reason recorded.

---

## The shape difference, stated once

The dupes lane is **not** `useMetadataLane` with different nouns. Four differences
change the architecture, and every one of them was a place this port could have
quietly gone wrong:

| | metadata lane | dupes lane |
|---|---|---|
| Pagination | fetches all (`limit=0`), slices client-side | **server-side** (`limit`/`offset`, `total` from response) |
| Stale responses | fetch-id ref, discard late results | **`AbortController`**, cancel in flight |
| Row id | `string` (book id) | **`number`** (candidate id) |
| Comparison | book vs. proposed metadata | **book vs. book** |

Consequences that must not be papered over:

- **The derived page clamp does not transfer.** In the metadata lane `page` is
  derived as `Math.min(requestedPage, totalPages)` because `totalPages` comes from
  a filtered array already in memory. Here `total` comes from the server, so
  clamping means refetching. `page` stays plain state in this lane.
- **`AbortController` is stronger than the fetch-id ref and stays.** Discarding a
  late response still pays for it; aborting does not. Do not "unify" these.
- **Ids stay numeric.** `SpineContext` is `(id: string)` throughout. Stringifying
  a `DedupCandidate.id` to fit that signature is how `mergeDedupCandidate` ends up
  called with `"12"`.
- **Filters split across the client/server boundary.** `band`, `status`, and
  `both_unmatched` are server params — changing one is a refetch. `searchQuery`
  and the `?book=` deep-link are client filters over the loaded page. They look
  symmetric in the UI and are not. See the two defects below.

### The spine

`CompareSpine`'s header reserved this decision for the moment a second real case
arrived: *"a type parameter here would be an abstraction over one real case and
two guesses."* The case has now arrived, and the answer is **no type parameter**.
`CandidateResult` is `{book, candidate, status}`; `DedupCandidate` is
`{id, book_a, book_b, band, score_breakdown}`. They share a *page layout*, not a
row shape. A `DupesSpine` sibling renders book-vs-book; `ReviewWorkspace` keeps
the lane switch. `CompareSpine`'s header comment is updated to record this rather
than left pointing at a generalisation that was examined and rejected.

---

## Behaviour to carry over

### Data layer
- [ ] Server-paginated fetch — `limit`/`offset`/`total`, `include_breakdown=true`,
      `include_books=true`
- [ ] `AbortController` cancellation of the in-flight request
- [ ] `getDedupStats` → `deriveBandCounts`, feeding the band filter's counts
- [ ] Raw `fetch` is used because `api.getDedupCandidates` takes no `AbortSignal`.
      Fix the API signature rather than carrying the raw call across.

### The keep decision — highest loss risk
- [ ] `metadataQuality(book)` — weighted completeness score. Treats `"TITLE"` and
      bare ULIDs as garbage titles, which is why it is not simply a field count.
- [ ] `qualityChip(score)` — Rich ≥6 / Partial ≥3 / Poor
- [ ] `recommendedKeepSide(candidate)` — returns `{keepId, label}` or `null` on a
      tie. **Its whole purpose is that the ★ chip and the `m` shortcut call the
      same function and cannot drift.** Porting the chip without the shortcut
      breaks nothing visibly and turns `m` into a coin flip.
- [ ] Tie behaviour: `m` defaults to keeping A, matching the button order.

### Keyboard navigation — second-highest loss risk
Nothing in `ReviewWorkspace` has focus management today, and shortcuts are the
classic port casualty: no test goes red when they disappear.

- [ ] `j` / `k` — move focus, clamped at both ends
- [ ] `m` — merge focused (pending only), via `recommendedKeepSide`
- [ ] `d` — dismiss focused (pending only)
- [ ] `s` — toggle select focused
- [ ] `Enter` — open compare drawer · `Esc` — close it
- [ ] `Shift+A` — select all on page · `?` — toggle shortcut help
- [ ] `isKeyboardShortcutSuppressed()` — blocks shortcuts in inputs, textareas,
      selects, contenteditable, and anything inside `[role="dialog"]`.
      **The queue rail is full of inputs, so this guard is load-bearing here.**
- [ ] `Escape` is deliberately exempt from that guard — MUI gives the drawer's
      paper `role="dialog"` and moves focus into it, so guarding Escape would
      suppress the one shortcut whose job is to close that drawer.
- [ ] `focusedRowIndex` resets appropriately when the filtered set changes

### Selection and bulk actions
- [ ] `Set<number>` selection, shift-click range via `lastClickedIdxRef`
- [ ] `handleMergeSelected` / `handleDismissSelected`
- [ ] `handleMergeAllFiltered` — library-wide scope; belongs in the command bar's
      `library` scope, not a row control
- [ ] `handleRescore(apply)` and `handleScan` with `trackOp` progress
- [ ] `bulkBusy` gating so a second dispatch cannot overlap the first

### URL state — not in the original brief, found while reading
- [ ] `?band=` — deep-link into a band filter, written back with `{replace: true}`
- [ ] `?book=` — **inbound link from `FingerprintVisualsColumn.tsx:94`**, plus its
      dismissable "Showing candidates for book X" banner. If the lane replaces
      `/dedup`, that navigate target must move with it or the link dead-ends.

### Reusable as-is
- [x] `CandidateCompareDrawer` is already a standalone component — it needs
      wiring, not porting.
- [x] `EvidencePanel` already handles `evidenceKind: 'weighted'`. Dedup is the one
      lane where a stacked share bar is arithmetically honest, and `lanes/dupes.ts`
      already declares it.
- [ ] `renderBookCard` — cover, garbage-title styling, path tooltip,
      `FolderFilesChip`. Ports mechanically into `DupesSpine`.

---

## Defects found while reading, not yet fixed

**1. `?book=` deep-link filters client-side over a server-paginated page.**
`filteredCandidates` (`:490`) filters `candidates` — one page — by
`entity_a_id`/`entity_b_id`. Arriving from `FingerprintVisualsColumn` for a book
whose candidate is not on page 1 shows an **empty list under a banner naming the
book**. The deep-link's entire purpose is to land on that book's candidates, so
this is a defect rather than a limitation.

**Fix, sized against the backend rather than guessed.** There is no `entity_id`
param today. But both read paths in `ListCandidates` funnel through one
predicate and one paginator, so the correct fix is small *and* complete:

- `CandidateFilter` gains `EntityID string` (`embedding_store.go:169`)
- `matchesCandidateFilter` gains one clause matching either side of the pair
  (`:968`) — this covers the full-scan path *and* the status-index path, and
  because `paginateCandidates` runs after it, **pagination totals stay accurate**
- the handler reads `c.Query("entity_id")` into it (`handler.go:~168`)

This is deliberately *not* modelled on `both_unmatched`, which cannot pre-filter
(its signal lives on the Book, not the candidate) and so fetches 1,000,000 rows
and paginates in-handler. `entity_id` lives on the candidate, so it filters at
scan level like `band` does. Copying the `both_unmatched` shape here would work
and would be wrong.

**2. Search is scoped to the loaded page and does not say so.** Same mechanism,
milder: the source comments it as intentional. Either scope it server-side or
label the control, but silently searching one page of N reads as "no results".

**3. `BookDetailStatusAlerts.tsx:104` links to `/dedup/candidates`, which is not
a route.** `App.tsx` registers only `/dedup` and `/dedup/labels`. Pre-existing
and out of this lane's scope, but it is a Phase 7 dependency: those deletions
repoint dedup routes, and this link has to be repointed with them.

---

## E2E disposition

Three specs drive this surface, 1061 lines total:

- `unified-dedup-tab.spec.ts` (296) — band filter bar, CERTAIN filtering, compare
  drawer, score breakdown, **and the legacy toggle**. Phase 7 deletes
  `dedup_show_legacy`, so that last test is deleted with it, not ported. The
  other four assert behaviour the lane must reproduce and **move with the lane**.
- `dedup.spec.ts` (466) and `dedup-operations.spec.ts` (299) — audit before
  Phase 7 deletes their target. E2E is where the last silent breakage surfaced.

---

## Success criteria

1. Every `[ ]` above is ticked or has a recorded `[~]` reason.
2. `ReviewWorkspace`'s `UNPORTED` map no longer contains `dupes`.
3. The lane reaches a merge and a dismiss decision end-to-end against a real
   backend (PLAN.md Phase 7's precondition — still blocked on auth).
