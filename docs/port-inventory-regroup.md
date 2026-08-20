<!-- file: docs/port-inventory-regroup.md -->
<!-- version: 1.1.0 -->
<!-- guid: 667eb751-3728-4f3d-91ab-e0a677537268 -->
<!-- last-edited: 2026-08-20 -->

# Port inventory — the regroup lane

Written before any code, as `docs/port-inventory.md` and
`docs/port-inventory-dupes.md` were. That gate has found every real defect in
both completed ports; the mechanical version of this port would have carried
three of them forward.

Source: `web/src/pages/ReviewQueue.tsx` (821 lines) plus
`web/src/stores/useReviewStore.ts` (87 lines).
Target: a `regroup` lane inside `ReviewWorkspace`.

---

## 1. How this port differs in shape from the previous two

| | metadata | dupes | **regroup** |
|---|---|---|---|
| Data owner | local hook | local hook | **global Zustand store** |
| Pagination | client-side over a full fetch | server-side | server-side, but the page never asks |
| Source deletable at Phase 7 | yes | yes | **only the page, not the store** |
| Bulk scope | selected ids | filter | **kind — a filter the UI cannot see the extent of** |
| Renderer | book vs. proposal | book vs. book | grouping proposal — a **third** shape |

### The store survives; the page does not

`useReviewStore` has three consumers besides `ReviewQueue`:

- `components/ReviewBanner.tsx:22` — `count`
- `components/layout/Sidebar.tsx:113` — `count`
- `App.tsx:121` — `getState()`, and it owns `startPolling`

Both prior ports deleted their source outright. **This one must not.** At Phase 7,
delete `pages/ReviewQueue.tsx` and leave `stores/useReviewStore.ts` in place. The
instinct at deletion time will be to take both, and taking both silently kills the
sidebar badge and the review banner.

### Decision: the lane fetches its own items

`items` / `itemsLoading` / `loadItems` are read by exactly one consumer
(`ReviewQueue`), so they are lane-private state living in a global store by
accident of history. The lane will own its own fetch, with the same
`AbortController` and `active` gating the dupes lane uses.

The deciding reason is the gating. A lane whose data lives in a globally polled
store cannot be switched off when the reviewer moves to another lane — `App.tsx`
starts the polling and the store has no notion of who is looking. The dupes lane
already established that leaving fetches in flight for an unmounted lane is a
defect, not a nicety.

`count` and `byKind` stay in the store and the lane READS them. Those are
genuinely shared, badge-shaped data, and §3.1 depends on `byKind` being there.

---

## 2. Behaviour checklist — what the port must reproduce

- [ ] Bucket pending items by `kind`, sorted by label, with a stable per-bucket count
- [ ] Per-item Approve / Reject, with the button disabled while that item's request is in flight
- [ ] `actionFor` resolution order: explicit reviewer pick → the hold's own recommendation → `""`, which **disables** Approve rather than guessing
- [ ] Always send the resolved action explicitly on approve, so what ran and what was displayed cannot disagree
- [ ] Surface the backend's own error message verbatim (see §3.2)
- [ ] Bulk approve/reject per kind, disabled while that kind is in flight
- [ ] Report skips in the same breath as successes — a bucket is not cleared just because the call returned
- [ ] `MemberFilesDetail` lazy-loads member books per item
- [ ] `RecommendationPanel` / `ActionSelector` / `MemberRow` render the proposal
- [ ] Refresh both the queue and the badge count after any action

---

## 3. Defects found while reading — fix, do not port

### 3.1 Bulk approve acts on far more than the reviewer can see

**The bug.** The store loads with `limit: 500` (`useReviewStore.ts:60`) and
**discards `page.total`** — `set({ items: page.items })`. The page buckets those
items by kind and shows a count per bucket. `handleBulkAction` then sends
`{ action, kind }` with no ids, and the handler
(`internal/server/handlers/review/handler.go:531-546`) acts on **every pending
item of that kind**, up to `bulkScanLimit = 100_000`.

The reviewer has no signal that a divergence exists, because the one number that
would reveal it is discarded. The API layer already parses `total` (`api.ts:6196`,
`ReviewItemsPage.total`); the store drops it six lines later. It is computed on
every single load and thrown away, which is what makes the fix cheap.

**Confirmed live against the server on 2026-08-20, not inferred:**

| | |
|---|---|
| `GET /review/count` → `byKind["regroup.ambiguous"]` | **714** |
| `GET /review/items?status=pending&limit=500` → `total` | 730 (discarded) |
| items actually returned | 500 |
| `regroup.ambiguous` among them — what the bucket displays | **484** |

So today, on the real queue, "Approve all" under a bucket reading **484** acts on
**714** — **230 holds the reviewer has never seen and could not have scrolled
past.** This is not a large-library hypothetical; it is the current state.

This is the third instance of this class in this rewrite, after the dupes band
filter and the `?book=` deep link. It is the same shape every time: a list
endpoint and a bulk endpoint that do not agree on scope.

**Severity is "unannounced until done", not "silent".** `result.Processed`
(`handler.go:574`, `:588`) counts every item actually acted on, so the toast says
"Approved 4300 items" afterwards. The reviewer does learn the true scope — after
an irreversible bulk action on a queue they believed held 500.

**The fix, and why it needs no product decision.** Two options existed:

- **(a)** Send explicit `ids` for exactly the rows displayed. The endpoint already
  accepts `IDs` (`handler.go:530`), so this is available. Fail-closed, but it
  quietly redefines "Approve all Regroup" as "approve the 500 I happened to load",
  which is not what the button means.
- **(b)** Keep the kind-scoped call — it expresses the reviewer's actual intent —
  and make the scope **visible** before they commit to it.

Take **(b)**. It looked like it needed a store change and a user decision, and it
needs neither: `byKind` already holds the true pending count per kind
(`useReviewStore.ts:48`, from `GET /review/count`), is already polled, and is
already read by the sidebar badge. The honest number is in memory already.

Concretely: the bucket header shows the true pending total for that kind, marked
when it exceeds what is loaded, and the bulk button names its real scope —
**"Approve all 4,300"**, not "Approve all". A reviewer who wants the narrower
thing still has per-item actions.

Keep `total` from the list response too, so "showing 500 of 4,300" is honest
independently of the polled count.

### 3.2 A comment describing a 501 that no longer happens

`ReviewQueue.tsx:566` explains that the backend's message is surfaced verbatim
because `duplicate-of` "answers 501 with an explanation of who owns it".

`duplicate-of` **has had an apply path since 2026-08-19**
(`handler.go:386-387`: "It was refused with a 501 until 2026-08-19"). The comment
documents behaviour that was removed the day before this port.

The *code* is still right — surfacing the backend's message verbatim is correct
regardless — so only the justification is stale. Port the behaviour, rewrite the
reason. This is the `deriveBandCounts` failure mode exactly: carrying a name or a
rationale across without reading what it now describes.

### 3.3 One kind's skip list silently erases another's

`bulkSkips` is a single `{ kind, skipped }` object (`ReviewQueue.tsx:516`), and
`handleBulkAction` overwrites it wholesale on every bulk call. It renders only
under the matching bucket (`bulkSkips?.kind === bucket.kind`).

So: bulk-approve kind A, see "12 skipped, listed below", start reading them, then
bulk-approve kind B — and A's list is gone, while A's twelve items are still
sitting in the queue undecided. Nothing was lost destructively, but the one
report telling the reviewer what still needs their attention disappears without
a trace.

Key it by kind (`Record<string, ReviewBulkSkip[]>`) so each bucket keeps its own.

---

## 4. Not defects — deliberate, keep

- **Per-item refusals skip and continue.** Aborting a batch on one
  insufficient-evidence hold would make bulk approve useless on a queue where a
  large fraction are exactly that (`handler.go:555-559`).
- **No batch-wide action override.** Bulk approve runs each item's *own*
  recommendation on purpose; a single action applied across a heterogeneous
  bucket is what the skip mechanism exists to avoid.
- **`""` disables Approve** rather than falling back to a guess.

---

## 5. E2E disposition

To determine when the lane renders. `ReviewQueue.recommendations.test.tsx` (188
lines) asserts recommendation rendering that the lane must reproduce; it should
move with the lane rather than die with the page.

---

## 6. Carried debts this port does NOT take on

- The metadata lane's rail/spine/action-bar trio is still inline in
  `ReviewWorkspace` while dupes lives in `DupesPanel`. Lifting it into a
  `MetadataPanel` is the matching change; still owed.
- `handleRescore` is unwired.
- `BookDetailStatusAlerts.tsx:104` links to `/dedup/candidates`, not a registered
  route. Phase 7 dependency.
- The legacy `/dedup` page keeps its client-side `?book=` bug and
  `UnifiedDedupTab.tsx:141` keeps the stale "Select all on the current page"
  label — both surfaces Phase 7 deletes.
