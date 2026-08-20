<!-- file: docs/port-inventory-dupes.md -->
<!-- version: 1.1.0 -->
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
- [x] Server-paginated fetch — `limit`/`offset`/`total`, `include_breakdown=true`,
      `include_books=true`
- [x] `AbortController` cancellation of the in-flight request
- [x] Raw `fetch` no longer needed: `apiFetch` always accepted a `signal`,
      `getDedupCandidates` simply never forwarded one. Fixed at the client.
- [~] `deriveBandCounts` **deliberately not ported, because it did not work.**
      The stats endpoint groups by entity_type/layer/status — there is no band
      dimension in the schema — so the source returned a hardcoded `0` for every
      band and only its `total` was real. Reproducing the shape would ship a map
      whose name promises counts it cannot contain. Replaced by `pendingTotal`,
      which is the one honest number derivable from that endpoint.

### The keep decision
- [x] `metadataQuality(book)` — moved to `lanes/keepDecision.ts`
- [x] `qualityChip(score)` — now `qualityBand()` + a `QualityChip` component, so
      the thresholds are testable without rendering
- [x] `recommendedKeepSide(candidate)` — shared module, imported by both the
      chip and the `m` shortcut, which is what makes the original's "can never
      drift" promise survive being split across a hook and a view
- [x] Tie behaviour: `keepIdForMerge` resolves a tie to A, matching button
      order, while `recommendedKeepSide` still returns `null` so the chip does
      not claim a recommendation exists

### Keyboard navigation
- [x] `j` / `k`, clamped at both ends
- [x] `m` (pending only, via the shared keep decision)
- [x] `d` (pending only)
- [x] `s`, `Shift+A`, `Enter`, `Esc`, `?`
- [x] `isKeyboardShortcutSuppressed()` — inputs, textareas, selects,
      contenteditable, and `[role="dialog"]`
- [x] `Escape` exempt from that guard, with the reason recorded at the call site
- [x] Focus resets across page and filter changes. Stronger than the source's
      clamp: with server-side pagination the previous page is still loaded while
      the next is in flight, so a stale focus index would aim `m` at a row from
      the page the reviewer just left.
- [x] Shortcuts unbind when the lane is not active — a window listener that
      outlives the lane moves a focus ring nobody can see

### Selection and bulk actions
- [x] `Set<number>` selection with shift-click range extension
- [x] `mergeSelected` / `dismissSelected`, sequential rather than concurrent so
      two merges cannot touch the same book
- [x] `mergeAllFiltered`, **with the refusal** described below
- [x] `busy` gating so a second destructive dispatch cannot overlap the first
- [ ] `handleRescore(apply)` — still only on the legacy page. It is a
      library-wide maintenance job rather than a review action, so it belongs in
      the command bar's `library` scope beside "Find duplicates"; not wired yet.

### URL state
- [x] `?band=` — round-trips, written back with `{replace: true}`
- [x] `?book=` — inbound from `FingerprintVisualsColumn.tsx:94`, now a
      **server-side** filter, with a dismissable banner and an empty state that
      is finally truthful

### Reused rather than ported
- [x] `CandidateCompareDrawer` — wired, not rewritten
- [x] `EvidencePanel` via `dedupEvidence` — the stacked share bar the
      `weighted` evidence kind exists for
- [x] `renderBookCard` → `BookSide`. Promoted from a function returning JSX to a
      real component; as a bare function the cover image re-mounted on every
      parent render.

### Symmetry this leaves owing
- [ ] The metadata lane's rail/spine/action-bar trio is still inline in
      `ReviewWorkspace`, while dupes lives in `DupesPanel`. The shell's own
      types.ts says "adding a fourth lane is a new descriptor rather than a new
      branch in five components", and it now carries one branch. Lifting
      metadata into a `MetadataPanel` is the matching change — deliberately not
      done here, because it refactors a lane this work was not asked to touch.

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
