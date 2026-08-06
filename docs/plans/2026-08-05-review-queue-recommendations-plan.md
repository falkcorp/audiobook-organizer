<!-- file: docs/plans/2026-08-05-review-queue-recommendations-plan.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3d5b9e07-84f1-4c62-a0d9-7b1e2f6c8a34 -->
<!-- last-edited: 2026-08-05 -->

> ⚠️ **UNVERIFIED DRAFT — symbols not grep-checked.** This document was authored by
> an agent on 2026-08-05 but the adversarial verification pass (which grep-verifies
> that every cited function, struct field, op ID and file path actually exists) did
> NOT run — the workflow was halted by API rate limiting before stage 2.
>
> **Treat every code citation as a claim, not a fact.** The most common failure mode
> in generated plans is a confidently-cited symbol that does not exist. Verify before
> executing. The design reasoning and the measured production numbers are still sound
> and were drawn from real observations; the code references are what needs checking.


# Review-queue recommendations + per-hold override actions — IMPLEMENTATION PLAN

Design: [`docs/specs/2026-08-05-review-queue-recommendations-design.md`](../specs/2026-08-05-review-queue-recommendations-design.md)
Fragment: `todo.d/20260805_220000_review_queue_recommendations_and_overrides.md`

**Prerequisite already satisfied:** `maintenance.relink-unlinked-books` (PR #2147)
has been applied; member duration coverage is 92.2%. Do not start this
workstream on a library where that is not true — the recommender's decisive
signal is `ShatterBook.DurationSec`.

**Worktree.** All work happens in a dedicated worktree, never the primary
checkout:

```bash
cd /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer
git worktree add .worktrees/review-recommendations -b feat/review-queue-recommendations
cd /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer/.worktrees/review-recommendations
```

Every file created or modified gets/keeps a version header (path, version, guid,
last-edited) and a version bump.

---

## Step order and why

Steps 1-3 are backend-pure and land with **zero behaviour change** to the apply
path — they only enrich the payload. Step 4-6 change dispatch. Step 7 is the
frontend. Splitting it this way means a bisect that lands on step 3 has a richer
queue and identical apply semantics.

Each step is one commit and one reviewable diff.

---

## Step 1 — Action vocabulary + `groupSignals` extraction (no behaviour change)

### Files

| File | Intent |
|---|---|
| `internal/itunes/service/regroup_actions.go` | **NEW.** `Action*` constants, `ValidChosenAction`, `DefaultActionForKind`. Pure, no imports beyond stdlib. |
| `internal/itunes/service/fs_regroup_shape.go` | Extract the block from line 578 (`fpVotes`) through line 648 (`folderNamedAfterBook`) into `func collectSignals(members []memberInfo) groupSignals`, returning a struct. `classifyGroup`'s switch (lines 668-770) reads `sig.X` instead of the local variables. **Do not change one branch condition.** |
| `internal/itunes/service/regroup_actions_test.go` | **NEW.** Table test over `DefaultActionForKind` for all four Kinds + an unknown kind → `""`; `ValidChosenAction` accepts the four choosable actions and rejects `insufficient-evidence`, `""`, `"COMBINE"`, `"delete"`. |

### `DefaultActionForKind` table (must match `wire_handlers.go:604-607`)

`regroup.multidisc`→`combine`, `regroup.anthology`→`combine`,
`regroup.version-group`→`version-group`, `regroup.ambiguous`→`""`, unknown→`""`.

### Tests

```bash
go test ./internal/itunes/service/... -race -run 'TestClassify|TestDefaultActionForKind|TestValidChosenAction' -count=1
```

**Green means:** every pre-existing test in `fs_regroup_shape_test.go`,
`fs_regroup_booklength_test.go`, `fs_regroup_folderref_test.go` passes
unmodified. If any of them needed editing, the refactor was not
behaviour-preserving — revert and redo.

---

## Step 2 — The recommender (payload-invisible; pure function + tests)

### Files

| File | Intent |
|---|---|
| `internal/itunes/service/regroup_recommend.go` | **NEW.** `RecommendationEvidence` struct (design §4.3) and `recommend(sig groupSignals, members []memberInfo) (action, reason string, ev RecommendationEvidence)` implementing rules R1-R11 in order. Reuses `bookLengthSec` (`fs_regroup_shape.go:123`) and `flatMultitrackMin` (`fs_regroup_shape.go:220`) — no new magic numbers. Every reason string interpolates the numbers that fired it. |
| `internal/itunes/service/fs_regroup_shape.go` | `RegroupGroup` (lines 162-181) gains `RecommendedAction`, `RecommendationReason`, `Evidence`, `SurvivorTitleSource`. The `build` closure (lines 651-666) populates the first three from `recommend(sig, members)`. |
| `internal/itunes/service/regroup_recommend_test.go` | **NEW.** See below. |

### Required tests (name them exactly; these are the acceptance criteria)

1. **`TestRecommend_AllDurationsUnknown_NeverCombines`** — 6 members,
   `DurationSec == 0` on every one, flat structure, filenames `01.mp3`…`06.mp3`
   (so `numberedCount == 6`), one shared stem (so `manyDistinctTitles` is
   false). Assert `action == ActionInsufficientEvidence` and
   `ev.DurationsMissing == 6`.
   **This is the load-bearing test.** `membersAreBookLength` counts unknown
   durations as *not* book-length (`fs_regroup_shape.go:148-159`), so with
   durations missing the series guard is silently false and the flat branch
   falls straight through to a confident collapse — the 41-of-43 shape
   documented at `fs_regroup_shape.go:128-148`. R1 running before every combine
   rule is the only thing preventing it. Do not merge without this test green.
2. **`TestRecommend_BookLengthMembers_Separate`** — 5 members at 7200s each →
   `separate`, reason contains `5 of 5` and `≥90 min`.
3. **`TestRecommend_ChapterLengthMembers_Combine`** — 8 members at 900s, flat,
   numbered, one stem → `combine`, `ev.MedianKnownSec == 900`.
4. **`TestRecommend_MixedDurations_MajorityLong_Separate`** — 3× 7200s + 2× 600s
   → `separate` (strict majority per R3).
5. **`TestRecommend_MergedViaITunes_InsufficientEvidence`** — members spanning
   two FilePath folders glued by a shared `ITunesPath` → `insufficient-evidence`.
6. **`TestRecommend_AbridgedUnabridged_VersionGroup`**.
7. **`TestRecommend_BoxedSet_Separate`** — folder `The Foundation Trilogy Boxed Set`,
   3 distinct stems → `separate` (R4 beats R8).
8. **`TestRecommend_Anthology_Combine`** — folder `… Anthology`, 5 distinct
   stems, all short → `combine`.
9. **`TestRecommend_DiscStructure_CombinesDespiteMissingDurations`** — the R1
   exception: disc subfolders + `folderNamedAfterBook` → `combine` even with
   durations unknown; assert `ev.DurationsMissing > 0` so the human sees it.
10. **`TestRecommend_ReasonAlwaysCarriesNumbers`** — run every fixture group
    through `recommend` and assert each reason matches `\d` at least once. A
    reason without a number is a nicer generic string, which is the bug.
11. **`TestRecommend_EvidenceReconciles`** — for every fixture:
    `ev.DurationsKnown + ev.DurationsMissing == ev.Members`,
    `ev.DiscMembers + ev.ChapterMembers + ev.FlatMembers == ev.Members`,
    `len(ev.MemberDurationsSec) == ev.Members`. No silent filtering.

```bash
go test ./internal/itunes/service/... -race -run TestRecommend -count=1
go test ./internal/itunes/service/... -race -count=1
```

**Green means:** all of the above pass **and** every pre-existing test in the
package still passes untouched.

---

## Step 3 — Survivor title fix + payload emission

### Files

| File | Intent |
|---|---|
| `internal/itunes/service/fs_regroup_shape.go` | Re-signature `deriveSurvivorTitle` (line 840) to `(folderName string, folderNamedAfterBook bool, dominantStem, dominantAuthor string) (title, source string)` per design §6. Add `bareOrdinalRe = regexp.MustCompile(`(?i)^(vol(?:ume)?\|bk\|book\|part\|pt\|disc\|cd)\s*\.?\s*\d+$`)` beside the other cleaning regexes (lines 254-258). Add `dominantAuthor` to `groupSignals` (majority `ShatterBook.Author`, `""` when no majority). Update the call site at line 660 to set both `SurvivorTitle` and `SurvivorTitleSource`. |
| `internal/itunes/service/fs_regroup_survivortitle_test.go` | **NEW.** See below. |
| `internal/plugins/maintenance/regroup_shattered_ai.go` | `regroupPayload` (lines 64-81) gains the four `omitempty` fields from design §4.4. `buildRegroupPayload` (lines 374-411) populates them. Add a `RECOMMEND:` histogram log line next to the existing `RECONCILE:` line (lines 220-223) — counts per action, plus `combine-with-missing-durations`, plus `survivor-title-empty` and `survivor-title-source-*`. |
| `internal/plugins/maintenance/regroup_shattered_ai_test.go` | Extend: assert `buildRegroupPayload` round-trips the new fields, that `proposedAction` is still present and unchanged, and that a payload with the new fields still decodes into the *old* struct shape (forward-compat is free with `encoding/json`, but assert it so a future rename is caught). |

### `deriveSurvivorTitle` tests (exact cases from the fragment)

- `folderNamedAfterBook=false`, `dominantAuthor="C. T. Phipps"`,
  `folderName="C. T. Phipps"`, `dominantStem=""` → `("", "none")`.
  (The author guard fires; without a members-derived title there is nothing
  trustworthy to emit.)
- `folderName="Volume 1"`, `folderNamedAfterBook=true` → `("", "none")`
  (bare-ordinal guard).
- `folderName="The Wandering Inn Vol. 01"`, `folderNamedAfterBook=false`,
  `dominantStem="The Wandering Inn Vol 9"` → `("The Wandering Inn Vol 9", "members")`.
- `folderName="Dune (Unabridged) (2019)"`, `folderNamedAfterBook=true` →
  `("Dune", "folder")`.
- `folderName="03 - Cage of Souls - 2"`, `folderNamedAfterBook=true` →
  `("Cage of Souls", "folder")` (existing cleaning behaviour preserved).
- Neither signal trustworthy → `("", "none")` — **empty, never a wrong title.**

```bash
go test ./internal/itunes/service/... -race -run 'TestDeriveSurvivorTitle' -count=1
go test ./internal/plugins/maintenance/... -race -run 'TestRegroup|TestBuildRegroupPayload' -count=1
```

**Green means:** the six cases above pass and `regroup_shattered_ai_test.go`'s
existing assertions are unmodified.

**Checkpoint.** After step 3 the queue payload is richer and the apply path is
byte-identical. This is the first deployable-and-useful state, and the first
half of the prod dry-run gate (§Gate A) can run here.

---

## Step 4 — Persist the chosen action (store layer)

### Files

| File | Intent |
|---|---|
| `internal/database/review_store.go` | `ReviewItem` (lines 60-70) gains `ChosenAction string \`json:"chosen_action,omitempty"\``. Add `SetReviewItemDecision(id, status, action string) (*ReviewItem, error)`: the body of the current `SetReviewItemStatus` (lines 423-459) plus `if action != "" { item.ChosenAction = action }`. Re-implement `SetReviewItemStatus(id, status)` as `SetReviewItemDecision(id, status, "")`. **Do not construct a fresh `ReviewItem`** — the existing code already re-fetches and mutates in place (lines 427-436); keep that shape. |
| `internal/database/iface_review.go` | Add `SetReviewItemDecision` to the `ReviewStore` interface with a doc comment stating that `action == ""` means "leave `ChosenAction` unchanged". |
| `internal/database/mock_store.go` | Add the method to the hand-written `MockStore` (siblings at lines 2721-2728). Without this the compile-time `Store` assertion at the top of the file breaks the build. Bump the file header version. |
| `internal/database/mocks/mock_store.go` | **Regenerate only.** `database.Store` embeds `ReviewStore` (`internal/database/store.go:57`), so the mockery mock needs the new method. |
| `internal/database/review_store_test.go` (or nearest existing) | Tests below. |

### Mock regeneration — scoped

```bash
make mocks
git diff --stat internal/database/mocks/mock_store.go
```

The diff must touch **only** the `SetReviewItemDecision` addition. Mockery is
pinned to v3.7.1 (Makefile:191-197); a diff that rewrites unrelated mocks means
the wrong binary is on PATH — run `scripts/setup-mockery.sh` and redo. Never
commit an unscoped regen.

### Tests

- **`TestSetReviewItemDecision_RecordsAction`** — approve with `combine`, read
  back, `ChosenAction == "combine"`.
- **`TestSetReviewItemStatus_PreservesChosenAction`** — set decision
  `(approved, "separate")`, then `SetReviewItemStatus(id, applied)`;
  `ChosenAction` must still be `"separate"`.
- **`TestUpsertReviewItem_PreservesChosenActionOnDecidedRow`** — set decision,
  then re-upsert the same DedupKey with a new Summary/Payload; the row must be a
  full no-op (`review_store.go:239-242`) and `ChosenAction` intact.
- **`TestUpsertReviewItem_PendingUpdatePreservesChosenAction`** — belt and
  braces on the patch-in-place path (`review_store.go:245-250`).

```bash
make mocks && git diff --stat internal/database/mocks/
go test ./internal/database/... -race -run 'TestSetReviewItem|TestUpsertReviewItem' -count=1
go build ./...
```

**Green means:** those four tests pass, `go build ./...` succeeds (proving both
mocks satisfy `Store`), and the mocks diff is one method.

---

## Step 5 — Action-keyed dispatch in the review handler

### Files

| File | Intent |
|---|---|
| `internal/plugins/maintenance/regroup_apply.go` | **NEW func** `ApplySeparate() func(context.Context, database.ReviewItem) error` — decodes the payload for logging (`decodeRegroupPayload`, line 322), logs folder + member count + "members are already separate books; no library change", returns `nil`. It must perform **zero writes**; add a comment saying so. |
| `internal/server/handlers/review/handler.go` | Rename `RegisterApplyHandler`→`RegisterActionHandler` and `applyHandlerFor`→`actionHandlerFor` (lines 77-88); the map is now keyed by action. `approveOne` (lines 179-208) takes a `chosenAction string` param and resolves per design §7.1 (explicit → payload `recommendedAction` → `DefaultActionForKind(item.Kind)`), dispatches on the resolved action, and calls `SetReviewItemDecision(id, status, resolved)`. `ApproveReviewItem` (lines 154-174) binds the optional body with `_ = c.ShouldBindJSON(&req)` — **the pattern at `replay.go:58`, not a hard bind**; returns 400 `INVALID_REVIEW_ACTION` when `req.Action != "" && !ValidChosenAction(req.Action)`; adds `"action": resolved` to the response. `bulkRequest` (lines 232-236) gains `ItemAction string \`json:"item_action,omitempty"\``; `bulkResult` gains `ByAction map[string]int \`json:"by_action,omitempty"\``. |
| `internal/server/handlers/review/replay.go` | Lines 80 and 116: dispatch via `actionFor(it)` (`it.ChosenAction` → payload `recommendedAction` → `DefaultActionForKind(it.Kind)`) instead of `it.Kind`. Add `would_replay_by_action` / `applied_by_action` to the two responses. |
| `internal/server/wire_handlers.go` | Lines 603-607 → three `RegisterActionHandler` calls (`combine`, `version-group`, `separate`) per design §7.4. Rewrite the comment block above it (lines 590-602) to explain action-keyed dispatch. |
| `internal/server/handlers/review/handler_test.go` | Extend (real `PebbleStore`, per the file's existing approach at lines 30-38). |
| `internal/server/handlers/review/replay_test.go` | Extend for the action-aware replay. |

### Required tests

- **`TestApprove_EmptyBody_NoContentType_Still200`** — the F7 regression. Use
  `doReq(..., nil, ...)` (helper at line 55), which sends no body and no
  `Content-Type`.
- **`TestApprove_LegacyAmbiguousPayload_NoDispatch`** — a `regroup.ambiguous`
  item whose payload has no `recommendedAction`: status `approved`, note
  mentions no handler, **no apply function invoked**. Byte-identical to today.
- **`TestApprove_UsesRecommendationWhenNoBody`** — ambiguous item with
  `recommendedAction: "separate"`, `separate` handler registered, switch ON →
  handler ran, status `applied`, `chosen_action == "separate"`.
- **`TestApprove_ExplicitActionOverridesRecommendation`** — payload recommends
  `combine`, body says `{"action":"separate"}` → the **separate** handler ran,
  the combine handler did not. Assert with two closures recording invocation.
- **`TestApprove_InvalidAction_400`** — `{"action":"insufficient-evidence"}` and
  `{"action":"delete"}` both 400, and the item's status is **unchanged**.
- **`TestApprove_SwitchOff_RecordsChosenAction`** — switch OFF, body
  `{"action":"separate"}` → status `approved`, `chosen_action == "separate"`,
  no handler invoked.
- **`TestReplayApproved_DispatchesStoredChosenAction`** — the F3 regression, and
  the reason D5 exists. Seed the item from the previous test's end state, flip
  the switch on, `POST /review/replay-approved {"apply":true}` → the
  **separate** handler runs, not the Kind default. Assert `applied_by_action`
  is `{"separate":1}`.
- **`TestBulk_NoItemAction_UsesKindDefault`** — bulk approve over a kind whose
  items recommend `combine`, with no `item_action`: dispatch must use
  `DefaultActionForKind`, i.e. today's behaviour (D6).
- **`TestBulk_ItemAction_AppliedToAll`** + **`TestBulk_InvalidItemAction_400`**.
- **`TestApplySeparate_WritesNothing`** (in `regroup_apply_test.go`) — run
  `ApplySeparate()` against a store seeded with N books; assert every book row
  is byte-identical afterwards and no `book_file` row changed.

```bash
go test ./internal/server/handlers/review/... -race -count=1
go test ./internal/plugins/maintenance/... -race -run 'TestApplySeparate|TestApplyMultidisc|TestApplyVersionGroup' -count=1
go test ./internal/server/... -race -count=1
```

**Green means:** all of the above pass **and** the pre-existing approve/bulk
tests (`handler_test.go:137-344`) pass with at most a mechanical rename of
`RegisterApplyHandler` — if any of their **assertions** had to change, dispatch
semantics regressed for an existing Kind.

---

## Step 6 — API client + payload types (frontend, no UI yet)

### Files

| File | Intent |
|---|---|
| `web/src/lib/reviewActions.ts` | **NEW.** Mirrors `reviewKinds.ts:8-13`. `REVIEW_ACTIONS` (the five strings), `CHOOSABLE_ACTIONS` (four), `ACTION_LABELS` (`combine`→"Combine into one book", `separate`→"Keep separate", `version-group`→"Link as versions", `duplicate-of`→"Duplicate of another book", `insufficient-evidence`→"Not enough evidence"), `labelForAction`. |
| `web/src/lib/reviewPayload.ts` | Add `recommendedAction?`, `recommendationReason?`, `recommendationEvidence?: RecommendationEvidence`, `survivorTitleSource?` to `ReviewPayload` (lines 17-32). Add `RecommendationEvidence` interface mirroring design §4.3 with every field optional (an old hold has none). Add `recommendationOf(payload)` returning `{action, reason, evidence} | null`. Update the file's header comment, which currently enumerates the producer's keys. |
| `web/src/services/api.ts` | `approveReviewItem(id, action?)` (line 5899): when `action` is given, send `headers: {'Content-Type': 'application/json'}` and `body: JSON.stringify({action})` — the header matters; the current call sends neither (lines 5900-5902). `ReviewBulkRequest` gains `item_action?`; `ReviewBulkResult` gains `by_action?`. `ReviewItem` gains `chosen_action?: string`. |
| `web/src/lib/__tests__/reviewPayload.test.ts` | Extend: an old payload (the existing fixture at line 20) yields `recommendationOf() === null`; a new payload yields the parsed triple; a malformed `recommendationEvidence` (string instead of object) does not throw. |
| `web/src/lib/__tests__/reviewActions.test.ts` | **NEW.** `labelForAction` covers all five and falls back readably for an unknown string. |

```bash
cd web && npx vitest run src/lib/__tests__/reviewPayload.test.ts src/lib/__tests__/reviewActions.test.ts
cd web && npx tsc --noEmit
```

**Green means:** both suites pass and `tsc` is clean.

---

## Step 7 — Review-queue UI

### Files

| File | Intent |
|---|---|
| `web/src/pages/ReviewQueue.tsx` | In `MemberFilesDetail` (lines 160-242): above the existing "Proposed action" row (lines 205-210), render a **Recommendation** block — a Chip with `labelForAction(recommendedAction)` (colour: `combine`→primary, `separate`→info, `insufficient-evidence`→warning), the reason sentence, and a compact evidence grid (members, durations known/missing, book-length members, distinct titles, numbered, structure, folder-named-after-book). Keep `proposedAction` rendered below as legacy context. Show `survivorTitle` only when `survivorTitleSource !== 'none'`; when it is `'none'`, render "Proposed title: (not derivable)" rather than a blank. In `ItemActions` (lines 329-360): replace the single Approve button with a split button — primary action = the recommendation (label `Approve: <action label>`), dropdown = the other three choosable actions; `handleItemAction` (line 277) passes the chosen action to `api.approveReviewItem`. When `recommendedAction === 'insufficient-evidence'`, the primary button is **disabled** with a tooltip "not enough evidence yet — leave pending"; Reject stays available but is **not** styled as the suggested path (D4/F5). Bucket header (lines 405-426): "Approve all" opens a menu requiring an explicit action, sent as `item_action` (D6). |
| `web/src/lib/reviewKinds.ts` | Unchanged — the four Kind labels stay verbatim (D1). Listed here so a reviewer knows the omission is deliberate. |
| `web/src/pages/__tests__/ReviewQueue.test.tsx` | **NEW** if absent. Render a hold with a recommendation → the chip, reason and evidence numbers appear; clicking `Approve: Keep separate` calls `approveReviewItem(id, 'separate')`; an `insufficient-evidence` hold has a disabled primary button; a legacy payload (no recommendation) renders exactly the old layout. |

```bash
cd web && npx vitest run src/pages/__tests__/ReviewQueue.test.tsx
make web-test
make test-all-short
```

**Green means:** the new suite passes, `make web-test` is clean, and
`make test-all-short` is green.

---

## Step 8 — Docs, changelog, TODO

| File | Intent |
|---|---|
| `changelog.d/<scriv-fragment>.md` | Required by `changelog-check.yml`. Run `scriv create`. |
| `todo.d/20260805_220000_review_queue_recommendations_and_overrides.md` | Leave it — `TODO.md` fragments are add-only; the checkbox is ticked in `TODO.md` directly once merged. |
| `docs/executive-summaries/2026-08.md` | Update. This qualifies: multi-file, wide blast radius (it changes what the Approve button does), and it closes owner items 1+2. Plain language, no jargon. |
| `.claude/notes/2026-08-05-first-aid-architecture.md` | Add a line under "Shipped so far". |

---

## Full pre-merge verification

```bash
cd /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer/.worktrees/review-recommendations
go build ./...
go test ./internal/itunes/service/... -race -count=1
go test ./internal/plugins/maintenance/... -race -count=1
go test ./internal/server/handlers/review/... -race -count=1
go test ./internal/database/... -race -run 'TestReview|TestUpsertReviewItem|TestSetReviewItem' -count=1
make mocks-check
make ci
```

`make ci` = `mocks-check check-mock-fresh staticcheck sdkguard test-all-short
coverage-check-short` (Makefile:350). Note that local `staticcheck` is red on
`main` itself; the authoritative gate is GitHub's **Minimal CI**. Compare
`staticcheck` output against a `main` baseline rather than treating any output
as a blocker.

---

## Deployment

Deploy from the **primary checkout**, not the worktree — `Makefile.local`'s
`LOCAL_ROOT` is hardcoded to the main checkout, so deploying from a worktree
ships main's binary.

```bash
cd /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer
git checkout main && git pull --ff-only
make deploy
```

`review_apply_enabled` stays **OFF** for the whole of this workstream.

---

## Dry-run acceptance gate

Two gates. **Gate A** can run after step 3 (payload-only). **Gate B** runs after
step 7 and is the one that must pass before anyone considers flipping
`review_apply_enabled` (which is owner item 3's canary, not this workstream).

### Before: capture the baseline

From the **current** prod binary, before deploying, save the last
`maintenance.regroup-shattered-ai` run's `RECONCILE:` and result lines
(`regroup_shattered_ai.go:220-223, 274-279`). In particular record the exact
`bykind=map[...]` string and `groups=`.

Reference numbers already measured on 2026-08-05:

- **Pre-relink:** 777 holds, 762 (98.1%) carrying the single generic string
  `review: flat folder shares a title but ordering is unclear`.
- **Post-relink (current):** queue 357 → **356**; member duration coverage 2.5%
  → **92.2%**.

Do not conflate the two: 762/777 measures the *problem*; **356** is what the
post-change dry-run must reproduce.

### Gate A — payload enrichment (after step 3)

Run the dry-run (no apply flag exists; the op is dry-run only by construction —
`regroup_shattered_ai.go:83-104` declares only `CapLibraryRead`):

```bash
TOKEN=$(cat /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer/.claude/.api-token)
# Enqueue: POST /api/v1/operations/v2 {def_id, params} -> {"op_id": "..."}
#   (internal/server/wire_operations_routes.go:27, internal/server/handlers/operations_v2.go:149-164)
OP=$(curl -sS -X POST https://<host>/api/v1/operations/v2 \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"def_id":"maintenance.regroup-shattered-ai","params":{}}' | python3 -c 'import json,sys;print(json.load(sys.stdin)["op_id"])')
# Logs: GET /api/v1/operations/:id/logs
curl -sS "https://<host>/api/v1/operations/$OP/logs?tail=200" -H "Authorization: Bearer $TOKEN"
```

Then read the op log for the `RECONCILE:`, `RECOMMEND:` and result lines.
**All six must hold:**

| # | Assertion | Threshold |
|---|---|---|
| A1 | `bykind=` map is **identical** to the pre-deploy baseline capture | exact string equality, no tolerance |
| A2 | `groups=` equals the **pre-deploy baseline capture**, exactly | Compare against the number you captured minutes earlier, **not** a literal. 356 is the 2026-08-05 reference, not the gate value — the library changes between runs, and an engineer who captures 361 and sees 361 must pass, not halt (and must never "fix" the classifier to hit 356). Any drift from *your own* baseline means the `groupSignals` refactor perturbed the Kind switch — stop. |
| A3 | New `RECOMMEND:` histogram counts sum to `groups=` | exact: `combine + separate + version-group + insufficient-evidence == groups` |
| A4 | Holds recommending `combine` while `durationsMissing*2 > members` | **exactly 0** — this is the direct guard on the 41-of-43 incident |
| A5 | Holds whose `survivorTitle` equals their dominant member author | **exactly 0** |
| A6 | Every hold carries a non-empty `recommendationReason` | **100%** (this one cannot fail after step 3 — it is a smoke check that the field is wired, not a quality bar) |
| A7 | Distinct `(recommendedAction, recommendationReason)` pairs across all holds | **≥ 6**. This is the gate with teeth and the actual restatement of the problem: the pre-relink queue had **1** distinct string on 762 of 777 holds (98.1%). If R11 (`insufficient-evidence`, the catch-all) absorbs everything you get 1-2 pairs and this fires. |

Plus one distributional sanity check that is informative, not blocking:
`insufficient-evidence` should be a **minority** of the 356 now that duration
coverage is 92.2%. If it is >60%, R1's majority threshold is mis-tuned against
real data — investigate before proceeding, do not just relax the rule.

Verification query for A3-A7 (read-only, run against the live API):

```bash
curl -sS "https://<host>/api/v1/review/items?status=pending&limit=1000" \
  -H "Authorization: Bearer $(cat .claude/.api-token)" \
| python3 -c '
import json,sys,collections
items=json.load(sys.stdin)["items"]
hist=collections.Counter(); bad_combine=0; empty_title=0; src=collections.Counter()
for it in items:
    p=json.loads(it["payload"] or "{}")
    a=p.get("recommendedAction",""); hist[a]+=1
    e=p.get("recommendationEvidence") or {}
    if a=="combine" and e.get("durationsMissing",0)*2 > e.get("members",0): bad_combine+=1
    if not p.get("survivorTitle"): empty_title+=1
    src[p.get("survivorTitleSource","")]+=1
print("total",len(items)); print("hist",dict(hist))
print("A4 combine-with-missing-durations",bad_combine)
print("empty survivorTitle",empty_title,"sources",dict(src))
'
```

A4 must print `0`.

### Gate B — dispatch + UI (after step 7)

With `review_apply_enabled` still **OFF**:

| # | Assertion | How |
|---|---|---|
| B1 | Approving a hold with no body still returns 200 and does not merge | approve one `regroup.multidisc` hold; assert status `approved`, note mentions review-only mode, and the member Book IDs still resolve via `GET /api/v1/audiobooks/<id>` |
| B2 | An explicit override is recorded | approve one `regroup.ambiguous` hold with `{"action":"separate"}`; `GET /review/items?status=approved` shows `chosen_action: "separate"` |
| B3 | Replay dry-run sees the stored action | `POST /review/replay-approved` (no body) → `would_replay_by_action` includes `separate: 1`; `dry_run: true`; **nothing applied** |
| B4 | Nothing was written to the library | `GET /api/v1/audiobooks?limit=1` `total` unchanged from before Gate B |
| B5 | Human agreement sample | the owner opens 20 holds spanning all four Kinds and agrees with **≥18** recommendations. Below 18 → retune the rules, do not proceed |
| B6 | No hold under `books/itunes/` | `skipped-frozen-itunes=` in the result line is >0 and no pending hold's `folder_ref` contains `books/itunes/` |

**Only after B1-B6 pass** may `review_apply_enabled` be considered — and that
decision belongs to owner item 3's multidisc canary
(`todo.d/20260805_220100_multidisc_apply_canary.md`), with its own before/after
snapshot. This workstream never flips it.

---

## Rollback

| Situation | Action |
|---|---|
| Gate A fails A1 (bykind drift) | The step-1 `groupSignals` extraction was not behaviour-preserving. `git revert` the step-1 commit and redo mechanically, one local variable at a time, re-running `go test ./internal/itunes/service/... -race` after each. |
| Gate A fails A4 (combine with missing durations) | Rule ordering bug in `recommend`. Revert step 2's commit only — steps 1 and 3 are independent — fix R1's placement, re-run. No prod state to undo: the op writes only review rows. |
| Recommendations are wrong but harmless | Nothing to roll back on the data side. Deploy the previous binary and re-run the dry-run: `UpsertReviewItem` patches pending rows in place (`review_store.go:245-250`), so the old payloads simply overwrite the new ones. **Decided rows are untouched** (`review_store.go:239-242`). |
| Dispatch regression after step 5 | Revert the step-5 commit. `ChosenAction` (step 4) is additive and harmless on its own — an unused field. |
| A wrong merge actually happened | Only possible if someone flipped `review_apply_enabled`, which this plan forbids. `CombineBooks` hard-deletes absorbed Book rows (`regroup_apply.go:28`); **the files are not deleted**, so the recovery path is a rescan, which regenerates a Book for any file no `book_file` row claims. Do **not** attempt to reconstruct rows by hand. |
| Mocks diff is larger than one method | Wrong mockery binary. `bash scripts/setup-mockery.sh`, `git checkout internal/database/mocks/`, `make mocks` again. |

**What is genuinely irreversible here:** nothing, as long as
`review_apply_enabled` stays OFF. That is the whole reason the switch exists and
the reason this plan does not touch it.

---

## Ship

```bash
git push -u origin feat/review-queue-recommendations
gh pr create --title "feat(review): structured recommendations + per-hold override actions" --body "..."
gh pr merge <n> --rebase        # this repo is rebase/FF only
git checkout main && git pull --ff-only
make deploy                     # from the PRIMARY checkout
git worktree remove .worktrees/review-recommendations && git worktree prune
```
