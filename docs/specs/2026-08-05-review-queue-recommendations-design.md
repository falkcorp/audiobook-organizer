<!-- file: docs/specs/2026-08-05-review-queue-recommendations-design.md -->
<!-- version: 1.0.0 -->
<!-- guid: 0a7c4f19-52d6-4b83-9e10-c6b7a3f8d245 -->
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


# Review-queue recommendations + per-hold override actions — DESIGN

Owner items 1 and 2 (2026-08-05). Fragment:
`todo.d/20260805_220000_review_queue_recommendations_and_overrides.md`.

Companion plan: [`docs/plans/2026-08-05-review-queue-recommendations-plan.md`](../plans/2026-08-05-review-queue-recommendations-plan.md)

---

## 1. Problem statement

### 1.1 The queue says the same thing on almost every row

`maintenance.regroup-shattered-ai` writes one review hold per candidate book
folder. Every hold carries a `proposedAction` string produced by
`classifyGroup` (`internal/itunes/service/fs_regroup_shape.go:570-771`), which
is a hard-coded literal per branch.

**Measured 2026-08-05 (pre-relink population, 777 holds): 762 of 777 holds
(98.1%) carried the single string**

> `review: flat folder shares a title but ordering is unclear`

emitted by the flat-ambiguous branch at
`internal/itunes/service/fs_regroup_shape.go:760-764`. A queue where 98% of
rows say the same sentence is a queue nobody can work: there is no way to tell
"six three-minute chapters of one novel" from "six two-hour novels by one
author" without opening each hold and reading file paths by hand.

### 1.2 This is NOT a missing-data problem — that was already fixed

The obvious hypothesis was that the classifier was starved of runtime data.
`ShatterBook.DurationSec` (`fs_regroup_shape.go:97-102`) is summed by the
producer from `store.GetBookFiles(id)`
(`internal/plugins/maintenance/regroup_shattered_ai.go:151-154`), and 97.5% of
review-queue members had zero `book_file` rows
(`.claude/notes/2026-08-05-unlinked-books-investigation.md`), so `DurationSec`
was 0 and `membersAreBookLength` (`fs_regroup_shape.go:149-160`) could not fire.

`maintenance.relink-unlinked-books` (PR #2147) has since been applied.

**Measured after relink: member duration coverage went 2.5% → 92.2%, and the
review queue moved 357 → 356 holds.**

One hold. The classifier saw real runtimes for 92.2% of members and changed its
mind about exactly one folder. That measurement is what makes this workstream a
**classifier-output and UX problem, not a data problem**, and it is why the
design below adds a *reporting* layer over the existing signals rather than
retuning the classification branches.

### 1.3 The human cannot override the machine

`approveOne` (`internal/server/handlers/review/handler.go:179-208`) looks up the
apply function by `item.Kind`:

```go
fn, hasHandler := h.applyHandlerFor(item.Kind)   // handler.go:187
```

Wiring registers three Kinds (`internal/server/wire_handlers.go:604-607`):
`regroup.multidisc` → `ApplyMultidisc`, `regroup.anthology` → `ApplyMultidisc`,
`regroup.version-group` → `ApplyVersionGroup`. `regroup.ambiguous` is
deliberately handler-less.

So the human's only expressible decisions are "do whatever this Kind means" and
"reject". A reviewer who reads a `regroup.ambiguous` hold, concludes "these are
six separate novels", and clicks Approve gets… nothing: status `approved`, a
note, no action, and `ReplayApprovedItems` will list it forever as
`no apply handler registered for this kind` (`replay.go:84`).

### 1.4 `deriveSurvivorTitle` returns wrong titles

`deriveSurvivorTitle` (`fs_regroup_shape.go:840-853`) takes **only the folder
name** and strips a leading ordinal / trailing parenthetical / trailing `- N`.
It is called at `fs_regroup_shape.go:660` with `folderName` alone, discarding
the two signals computed 15 lines earlier — `dominantPrefix`
(`fs_regroup_shape.go:627`) and `folderNamedAfterBook`
(`fs_regroup_shape.go:645-647`).

Observed outputs: author names (`C. T. Phipps`), bare ordinals (`Volume 1`),
and wrong volume numbers (a folder named `… Vol. 01` whose member files all say
`Vol. 9`). `SurvivorTitle` is display-only today — the apply path calls
`CombineBooks(present, primaryID, nil)` with a **nil** override
(`internal/plugins/maintenance/regroup_apply.go:100`), so the survivor's
metadata is never rewritten — but a title that names the author is exactly the
kind of label that makes a reviewer reject a good regroup
(`fs_regroup_shape.go:534-552` documents the identical failure for `FolderRef`).

---

## 2. Scope

**In scope**

1. A structured recommendation (`recommendedAction`, `recommendationReason`,
   `recommendationEvidence`) computed from signals `classifyGroup` already has,
   emitted on every hold.
2. A closed action vocabulary and an action-keyed apply-handler registry, so
   `approveOne` dispatches on the **chosen** action.
3. `POST /api/v1/review/items/:id/approve` accepting an optional
   `{"action": "..."}` body.
4. Persisting the chosen action on the `ReviewItem` so `ReplayApprovedItems`
   replays the human's decision, not the Kind's default.
5. Fixing `deriveSurvivorTitle` and reporting its provenance.
6. Frontend: render the recommendation + evidence; let the reviewer pick.

**Out of scope** — see §8 Non-goals.

---

## 3. Locked decisions

### D1 — The four `Kind` strings do not change, and neither does the Kind switch

`KindMultidisc` / `KindVersionGroup` / `KindAnthology` / `KindAmbiguous`
(`fs_regroup_shape.go:70-75`) stay exactly as they are, and **not one branch
condition in `classifyGroup`'s switch (`fs_regroup_shape.go:668-770`) is
touched.**

*Why.* The Kind strings are load-bearing: the frontend maps them verbatim
(`web/src/lib/reviewKinds.ts:8-13`), `regroupDedupKey` hashes
`(Kind, FolderRef)` (`regroup_shattered_ai.go:340-343`) so changing a Kind
orphans every existing hold, and `reconcileStaleHolds` keys off the
`regroup.` prefix (`regroup_shattered_ai.go:318`). Leaving the switch untouched
also gives the acceptance gate a **free exact regression test**: the
`bykind=` map in the RECONCILE line (`regroup_shattered_ai.go:220-223`) must be
byte-identical to the pre-change run. Any drift means the classifier was
perturbed by accident.

The recommendation is computed **alongside** the Kind, from the same signals,
and stored in the payload. The fragment says it explicitly: *"put the decision
in the payload, do not add Kinds."*

### D2 — Action vocabulary (closed set), and the fifth action

The fragment names four: `combine`, `separate`, `duplicate-of`,
`insufficient-evidence`. This design adds a **fifth**, `version-group`.

*Why the addition is required.* Once dispatch keys off the chosen action rather
than `item.Kind`, `ApplyVersionGroup` (`regroup_apply.go:188`) — today
registered under `KindVersionGroup` at `wire_handlers.go:606-607` — becomes
**unreachable** unless an action names it. Dropping it would silently disable
the Abridged/Unabridged linking path. Flagged here for owner review rather than
buried in the plan.

| Action | Meaning | Handler | Destructive? |
|---|---|---|---|
| `combine` | These files are one book. | `ApplyMultidisc` → `CombineBooks` | **Yes** — absorbed Book rows are hard-deleted (`regroup_apply.go:28`) |
| `separate` | These are already N separate books; leave them. | `ApplySeparate` (no-op, new) | No |
| `version-group` | Two editions of one work; link, keep both visible. | `ApplyVersionGroup` | No (locked decision #8: never soft-deletes) |
| `duplicate-of` | Debris of a book that already exists correctly. | **none in this workstream** | n/a |
| `insufficient-evidence` | The machine cannot tell. | **emit-only, not choosable** | n/a |

### D3 — `separate` gets a registered no-op handler, not "no handler"

The fragment is right that `separate` needs no *work*: every member is already
its own book. But "no handler" is not the same as "no work". With no handler,
`approveOne` records `approved` + a note (`handler.go:202`), and
`ReplayApprovedItems` will re-list that item as skipped on every future replay
(`replay.go:84`) — permanent noise that never drains.

So `separate` gets `ApplySeparate`: a registered `ApplyFunc` that logs the
decision and returns `nil`, which drives the item to `applied` — a terminal
state. `UpsertReviewItem` full-no-ops on non-pending rows
(`review_store.go:239-242`), so the decision survives every re-scan, exactly as
the fragment describes.

**Honest caveat:** while `review_apply_enabled` is OFF in prod (it is — see
`wire_handlers.go:588` and the memory note *review-apply switch OFF in prod*),
a `separate` approve still lands on `approved` + note, because
`applyGloballyEnabled()` gates *all* dispatch (`handler.go:188`). The benefit of
the no-op handler is dormant until the switch flips or `replay-approved` runs.

### D4 — `insufficient-evidence` is emit-only; overriding to it is a 400

A recommendation of `insufficient-evidence` is a statement *by the machine*.
There is no human action it maps to: the human either knows the answer (and
picks `combine`/`separate`/`version-group`) or does not (and leaves the hold
pending). `POST …/approve` with `{"action":"insufficient-evidence"}` returns
400.

**Critically, the UI must not steer these toward Reject.** `rejected` is
remembered forever — the dedup index is never deleted for a decided row
(`review_store.go:115-131`, "rejected is remembered"), and `reconcileStaleHolds`
only purges *pending* holds (`regroup_shattered_ai.go:314-329`). Rejecting an
`insufficient-evidence` hold permanently suppresses a folder that the tier-2
duration probe (First Aid task #3) could resolve next month. Leaving it pending
is the correct outcome.

### D5 — The chosen action is PERSISTED on the ReviewItem

New field `ReviewItem.ChosenAction` (`internal/database/review_store.go:60-70`)
plus `SetReviewItemDecision(id, status, action string)` on `ReviewStore`.

*Why this is non-negotiable.* `ReplayApprovedItems` exists precisely because
approved decisions were being silently discarded — read `replay.go:30-46`, which
documents that `ReviewStatusApproved` appeared in exactly two places in the
codebase before it was written: the constant, and the one line that sets it.
It dispatches with `h.applyHandlerFor(it.Kind)` (`replay.go:80` and
`replay.go:116`). If the chosen action lives only in the HTTP request, then a
human who overrides an ambiguous hold to `separate` while the switch is off has
their decision replayed later as *the Kind's default* — recreating the exact
class of bug `replay.go` was written to fix, one layer up.

Storage safety: `SetReviewItemStatus` already re-reads the record and mutates
in place (`review_store.go:427-436`), and the pending-upsert path likewise
patches the fetched row (`review_store.go:245-250`) — both are the safe
re-fetch-and-patch shape, so `ChosenAction` survives a producer re-scan with no
extra code. The one fresh-struct construction is the dangling-dedup-index
recovery branch (`review_store.go:207-221`), where the record is *gone* and
there is nothing to preserve; that branch is documented, not changed.

### D6 — Bulk approve NEVER uses per-item recommendations

`BulkReviewAction` (`handler.go:253-324`) calls `approveOne` per id
(`handler.go:294`). After this change:

- `bulkRequest` gains an optional `action` field. When set, it is validated once
  and applied to **every** target.
- When absent, bulk uses `defaultActionForKind(item.Kind)` — which reproduces
  today's dispatch exactly (multidisc/anthology → `combine`, version-group →
  `version-group`, ambiguous → `""` → no handler → `approved` + note).

*Why.* A recommendation is a per-item claim about a specific folder. Approving
300 of them from a bucket header without reading any is the precise failure mode
that produced the 41-of-43 near-miss documented at `fs_regroup_shape.go:128-148`.
Single-item approve honours the recommendation; bulk requires the human to name
one action for the whole batch.

### D7 — Combine recommendations are gated on positive duration evidence

`membersAreBookLength` counts unknown durations as **not** book-length
(`fs_regroup_shape.go:148-159`). That is correct for a *guard* — an absent value
must not fire a veto — but it means that when durations are missing the series
guard is silently false and the flat branch falls straight through to a
confident collapse. That is the 41-of-43 shape.

So the recommender applies rule **R1 before any combine rule**: if a majority of
members have `DurationSec == 0`, the recommendation is `insufficient-evidence`,
with the missing count in the evidence. **Absent evidence is never negative
evidence.** (Exception, narrowly: a `disc`- or `chapter`-structured group whose
folder is named after the book carries independent structural proof and may
still recommend `combine` — recorded as `durationsMissing` in the evidence so
the human sees it.)

### D8 — The recommender is a pure function, run in the existing single-threaded phase

No new whole-library loop. `runRegroupShatteredAI` fans the per-book *read* out
over `registry.RunItems` at `runtime.NumCPU()`
(`regroup_shattered_ai.go:124-137, 200-207`); grouping, classification and the
review-row writes are deliberately single-threaded afterwards
(`regroup_shattered_ai.go:128-130, 212-213`). The recommender is a pure function
over the already-materialised `memberInfo` slice, called once per group inside
`classifyGroup` — O(members) with no I/O. The CLAUDE.md concurrency mandate is
satisfied by construction; nothing new to parallelise.

### D9 — The frozen iTunes tree stays excluded, unchanged

`books/itunes/**` members are dropped at the source
(`regroup_shattered_ai.go:191-194`, via `config.UnderFrozenITunesTree`,
`internal/config/itunes_libraries.go:98`). No recommendation logic re-derives or
relaxes that policy.

---

## 4. Data model

### 4.1 Action vocabulary (Go)

New file `internal/itunes/service/regroup_actions.go`:

```go
const (
    ActionCombine              = "combine"
    ActionSeparate             = "separate"
    ActionVersionGroup         = "version-group"
    ActionDuplicateOf          = "duplicate-of"
    ActionInsufficientEvidence = "insufficient-evidence"
)
```

`ValidChosenAction(a string) bool` → true for `combine`, `separate`,
`version-group`, `duplicate-of`; **false** for `insufficient-evidence` and
anything else (D4).

`DefaultActionForKind(kind string) string`:

| Kind | Default action |
|---|---|
| `regroup.multidisc` | `combine` |
| `regroup.anthology` | `combine` |
| `regroup.version-group` | `version-group` |
| `regroup.ambiguous` | `""` (no dispatch) |

This table is a behaviour-preserving restatement of `wire_handlers.go:604-607`
and is what legacy holds (payloads written before `recommendedAction` existed)
fall back to. **`regroup.ambiguous` → `""` is load-bearing**: it guarantees a
pre-existing ambiguous hold cannot suddenly become a merge.

### 4.2 `RegroupGroup` additions

`internal/itunes/service/fs_regroup_shape.go`, struct at
`fs_regroup_shape.go:162-181`:

```go
    RecommendedAction   string                 // one of the Action* constants
    RecommendationReason string                // one sentence, quotes its own numbers
    Evidence            RecommendationEvidence
    SurvivorTitleSource string                 // "folder" | "members" | "none"
```

### 4.3 `RecommendationEvidence`

New type in `internal/itunes/service/regroup_recommend.go`. Every field is a
number `classifyGroup` already computes or trivially derives; nothing here
requires new I/O.

```go
type RecommendationEvidence struct {
    Members            int   `json:"members"`
    DurationsKnown     int   `json:"durationsKnown"`
    DurationsMissing   int   `json:"durationsMissing"`
    MemberDurationsSec []int `json:"memberDurationsSec"` // play order, 0 = unknown
    MedianKnownSec     int   `json:"medianKnownSec"`
    TotalKnownSec      int   `json:"totalKnownSec"`
    BookLengthMembers  int   `json:"bookLengthMembers"`  // DurationSec >= bookLengthSec (5400)

    DistinctStems      int    `json:"distinctStems"`      // == distinctPrefixes
    DominantStem       string `json:"dominantStem"`       // == dominantPrefix
    DominantStemCount  int    `json:"dominantStemCount"`  // == dominantCount

    NumberedMembers    int    `json:"numberedMembers"`
    DiscMembers        int    `json:"discMembers"`
    ChapterMembers     int    `json:"chapterMembers"`
    FlatMembers        int    `json:"flatMembers"`
    Structure          string `json:"structure"`          // "disc"|"chapter"|"flat"|"edition"

    FolderNamedAfterBook bool `json:"folderNamedAfterBook"`
    MergedViaITunes      bool `json:"mergedViaItunes"`
    SingleBookMarker     bool `json:"singleBookMarker"`   // anthology/omnibus/collection
    MultiBookMarker      bool `json:"multiBookMarker"`    // trilogy/quartet/boxed set
    HasUnabridged        bool `json:"hasUnabridged"`
    HasAbridged          bool `json:"hasAbridged"`
}
```

Source of each existing signal, so no one re-derives them:
`distinctPrefixes` / `dominantPrefix` / `dominantCount`
(`fs_regroup_shape.go:627-628`); `discCount` / `chapterCount` / `flatCount` /
`numberedCount` (`fs_regroup_shape.go:609-626`); `structure`
(`fs_regroup_shape.go:629`); `folderNamedAfterBook`
(`fs_regroup_shape.go:645-647`); `mergedViaItunes` (`fs_regroup_shape.go:581`);
markers (`fs_regroup_shape.go:604-606`); `bookLengthSec = 90*60`
(`fs_regroup_shape.go:123`).

### 4.4 `regroupPayload` additions

`internal/plugins/maintenance/regroup_shattered_ai.go:64-81`. Existing fields
are **unchanged** (`proposedAction` stays — the frontend reads it at
`web/src/pages/ReviewQueue.tsx:164` and old holds still carry it):

```go
    RecommendedAction    string                                `json:"recommendedAction,omitempty"`
    RecommendationReason string                                `json:"recommendationReason,omitempty"`
    Evidence             *itunesservice.RecommendationEvidence `json:"recommendationEvidence,omitempty"`
    SurvivorTitleSource  string                                `json:"survivorTitleSource,omitempty"`
```

`omitempty` everywhere: a hold written by the old binary decodes cleanly into
the new struct with zero values, and `DefaultActionForKind` covers it.

### 4.5 `ReviewItem` addition

`internal/database/review_store.go:60-70`:

```go
    ChosenAction string `json:"chosen_action,omitempty"` // action the human picked at approve time
```

Empty for every existing row and for `reject`.

---

## 5. The recommender

`func recommend(sig groupSignals, members []memberInfo) (action, reason string, ev RecommendationEvidence)`
in `internal/itunes/service/regroup_recommend.go`.

`groupSignals` is a new struct holding the values `classifyGroup` already
computes between `fs_regroup_shape.go:578` and `:648`; `classifyGroup` is
refactored to populate it once and use it in both the (unchanged) Kind switch
and the recommender. **This refactor must be behaviour-preserving for the Kind
switch** — that is what the identical-`bykind` gate proves.

Rules are evaluated **in order**. Safety asymmetry is deliberate: `combine`
hard-deletes absorbed Book rows (`regroup_apply.go:28`), `separate` does
nothing. So every guard that could yield `separate` or
`insufficient-evidence` is checked **before** any combine rule.

| # | Condition | Action | Reason template |
|---|---|---|---|
| R1 | `DurationsKnown*2 <= Members` **and not** (`Structure` ∈ {disc, chapter} ∧ `FolderNamedAfterBook`) | `insufficient-evidence` | "%d of %d members have no known runtime — cannot tell chapters from separate books" |
| R2 | `MergedViaITunes` | `insufficient-evidence` | "grouped only by a shared original iTunes album across %d different file-path folders" |
| R3 | `BookLengthMembers*2 > Members` | `separate` | "%d of %d members run ≥90 min — each is book-length, so this folder holds separate books" |
| R4 | `MultiBookMarker && DistinctStems >= 3 && DistinctStems*2 > Members` | `separate` | "folder is marked as a trilogy/boxed set and carries %d distinct titles" |
| R5 | `HasUnabridged && HasAbridged && DominantStemCount*2 >= Members` | `version-group` | "both Abridged and Unabridged markers with one dominant title (%d of %d members)" |
| R6 | `Structure == "disc" && DiscMembers*2 > Members` | `combine` | "%d of %d members sit in Disc/CD subfolders" |
| R7 | `Structure ∈ {chapter, edition} && FolderNamedAfterBook && DistinctStems <= 1` | `combine` | "chapter/edition shells all named after the folder's book, one title stem" |
| R8 | `SingleBookMarker && DistinctStems >= 3 && DistinctStems*2 > Members` | `combine` | "anthology/omnibus marker with %d distinct story titles — one published book" |
| R9 | `Structure == "flat" && NumberedMembers*2 >= Members && Members >= 4 && !manyDistinctTitles && MedianKnownSec < 5400` | `combine` | "%d of %d files numbered, median runtime %s — chapter-length, one book" |
| R10 | `DistinctStems >= 3 && DistinctStems*2 > Members && !FolderNamedAfterBook` | `separate` | "%d distinct titles and the folder is not named after any of them" |
| R11 | otherwise | `insufficient-evidence` | "no decisive signal: %d members, %d distinct titles, structure %s" |

Notes:

- `manyDistinctTitles` in R9 is the existing expression at
  `fs_regroup_shape.go:638` (`DistinctStems >= 3 && DistinctStems*2 > n`).
- R9's `MedianKnownSec < 5400` is a positive-evidence requirement, not a guard.
  It is reachable only after R1 established a duration majority.
- `4` in R9 is `flatMultitrackMin` (`fs_regroup_shape.go:220`); `5400` is
  `bookLengthSec` (`fs_regroup_shape.go:123`). Use the constants.
- Every reason string interpolates the numbers that fired the rule. A reason
  without numbers is a nicer generic string, which is the thing being fixed.

**The single most important test in the suite** (see plan step 2): N members,
all `DurationSec == 0`, flat structure, numbered, `n >= 4` → must yield
`insufficient-evidence`, **never** `combine`. That is the 41-of-43 shape with
its guard inert.

`duplicate-of` is never emitted by this recommender (§8).

---

## 6. `deriveSurvivorTitle`

New signature:

```go
func deriveSurvivorTitle(folderName string, folderNamedAfterBook bool,
    dominantStem string, dominantAuthor string) (title, source string)
```

Rules:

1. If `folderNamedAfterBook` → clean(`folderName`), source `"folder"`.
2. Else if `dominantStem != ""` → clean(original-case dominant prefix), source
   `"members"`.
3. Else → `""`, source `"none"`.

Then three rejection guards; a title failing any of them becomes `""` with
source `"none"`:

- **Author guard.** `normTitle(candidate) == normTitle(dominantAuthor)` →
  reject. Fixes `C. T. Phipps`. `dominantAuthor` is the majority
  `ShatterBook.Author` (`fs_regroup_shape.go:95`) among members; when no author
  has a majority it is `""` and the guard is inert.
- **Bare-ordinal guard.** New regex
  `^(?i)(vol(?:ume)?|bk|book|part|pt|disc|cd)\s*\.?\s*\d+$` on the cleaned
  candidate → reject. Fixes `Volume 1`.
- **Empty guard.** Cleaning left nothing → reject.

The existing cleaning helpers stay: `leadingNumRe`, `trailingParenRe`,
`trailingNumSuffixRe` (`fs_regroup_shape.go:254-258`).

Rule 2 uses **`dominantPrefix`, not `ShatterBook.Title`.** The package doc is
explicit that per-track tags are unreliable and that the library folder is the
only reliable identity signal (`fs_regroup_shape.go:12-15`, "decision #6:
regex-only v1"), and `Title` is documented as "may be empty"
(`fs_regroup_shape.go:94`). Preferring the tag would contradict the
classifier's own founding rationale. Fixing the *wrong-volume-number* case
(`Vol. 01` folder over `Vol. 9` files) falls out of rule 2: when the folder is
not named after the members' dominant stem, the members win.

`SurvivorTitle` remains display-only — nothing in `regroup_apply.go` reads it,
and `CombineBooks` is called with a nil override (`regroup_apply.go:100`), so
this change writes nothing to any Book row.

---

## 7. API surface

### 7.1 `POST /api/v1/review/items/:id/approve`

Optional body:

```json
{ "action": "combine" }
```

Binding **must** use the optional-body pattern from `replay.go:58`
(`_ = c.ShouldBindJSON(&req)`), not a hard bind — every existing caller,
including `web/src/services/api.ts:5899-5908`, POSTs with no body and no
`Content-Type`, and a hard bind would 400 all of them.

Resolution order inside `approveOne`:

1. `req.Action` if non-empty → must satisfy `ValidChosenAction`, else **400**
   `INVALID_REVIEW_ACTION`.
2. else `payload.recommendedAction` if it satisfies `ValidChosenAction`.
3. else `DefaultActionForKind(item.Kind)`.

Then `fn, ok := h.actionHandlerFor(resolvedAction)`. Same gate as today:
handler present **and** `applyGloballyEnabled()` → run, then
`SetReviewItemDecision(id, applied, resolvedAction)`. Otherwise
`SetReviewItemDecision(id, approved, resolvedAction)` + note.

Response adds `"action": "<resolved>"` alongside the existing `item` / `note`
keys, so the UI can confirm what it just did.

### 7.2 `POST /api/v1/review/bulk`

`bulkRequest` (`handler.go:232-236`) gains `Action string \`json:"item_action,omitempty"\``
— named `item_action` because `action` is already taken by
`approve`/`reject`. Validated once with `ValidChosenAction`; when empty, D6
applies (`DefaultActionForKind`, i.e. today's behaviour).

`bulkResult` gains `by_action map[string]int` so the operator can read
"applied 12 combine, 40 separate" instead of "applied 52".

### 7.3 `POST /api/v1/review/replay-approved`

`replay.go:80` and `replay.go:116` change from
`h.applyHandlerFor(it.Kind)` to `h.actionHandlerFor(actionFor(it))`, where
`actionFor` is: `it.ChosenAction` → else payload `recommendedAction` → else
`DefaultActionForKind(it.Kind)`.

The dry-run response gains `would_replay_by_action` and the apply response
gains `applied_by_action` (per D3's caveat: an operator must not read
`applied=300` as 300 merges when 250 of them are no-op `separate`s).

### 7.4 Handler registry

`RegisterApplyHandler(kind, fn)` → `RegisterActionHandler(action, fn)`
(`handler.go:77-81`); `applyHandlerFor` → `actionHandlerFor`
(`handler.go:83-88`). The map is now keyed by action, never by Kind.

Wiring (`wire_handlers.go:603-607`) becomes:

```go
combine := maintenanceplugin.ApplyMultidisc(s.Store(), mergeSvc)
reviewH.RegisterActionHandler(itunesservice.ActionCombine, combine)
reviewH.RegisterActionHandler(itunesservice.ActionVersionGroup,
    maintenanceplugin.ApplyVersionGroup(s.Store()))
reviewH.RegisterActionHandler(itunesservice.ActionSeparate,
    maintenanceplugin.ApplySeparate())
```

Note that `KindMultidisc` and `KindAnthology` collapsing to one `combine`
registration is behaviour-identical: both already registered the same closure
(`wire_handlers.go:604-605`).

---

## 8. Non-goals

- **No new `Kind` strings, no changes to `classifyGroup`'s switch conditions**
  (D1). If the recommender and the Kind disagree, that is information for the
  human, not a bug to fix here.
- **No `duplicate-of` detector.** Detecting "this group's member durations
  already appear as tracks of another book" (the Successors class,
  `.claude/notes/2026-08-05-unlinked-books-investigation.md`) needs cross-book
  comparison — First Aid tier 2, task #10. This workstream reserves the action
  string and validates it; no producer emits it and no handler is registered,
  so choosing it records `approved` + note.
- **No tier-2 duration probing.** `insufficient-evidence` holds stay pending
  until task #3 gives them real runtimes.
- **No deletion of anything, ever.** Rescan regenerates a book for any file no
  `book_file` row claims; duplicates are resolved by re-association, never
  deletion.
- **No writes to `books/itunes/**`** (D9).
- **No change to `review_apply_enabled`'s default (OFF).** Enabling it is
  owner item 3's canary, a separate workstream.
- **No auto-apply.** Every hold remains a human decision.
- **No retuning of thresholds** (`bookLengthSec`, `flatMultitrackMin`,
  `manyDistinctTitles`). The recommender reuses them verbatim.

---

## 9. Failure modes

| # | Failure | What happens | Mitigation |
|---|---|---|---|
| F1 | **Recommendation escalates an inert hold into a merge.** Today `regroup.ambiguous` has no handler, so Approve is a no-op. After this change an ambiguous hold recommending `combine` is one click from `CombineBooks`, which hard-deletes absorbed rows (`regroup_apply.go:28`). | Intended per the fragment — but the blast radius is real. | `review_apply_enabled` is OFF in prod, so nothing executes until it is deliberately flipped; single-item only (D6); `combine` gated on duration evidence (D7/R1); the acceptance gate below requires zero `combine` recommendations with a duration majority missing. |
| F2 | **Legacy hold with no `recommendedAction`.** | `DefaultActionForKind` → for `regroup.ambiguous` that is `""` → no handler → `approved` + note, i.e. **byte-identical to today**. | Explicit test. |
| F3 | **Human overrides, switch is off, decision evaporates.** | Prevented: `ChosenAction` is persisted (D5) and `replay-approved` reads it. | Test: approve with `{"action":"separate"}` while disabled → item is `approved` with `chosen_action=separate`; enable + replay → dispatches `separate`, not the Kind default. |
| F4 | **Producer re-scan wipes `ChosenAction`.** | Cannot happen: decided rows full-no-op (`review_store.go:239-242`), pending rows are patched in place (`review_store.go:245-250`). The one fresh-struct path is dangling-index recovery (`review_store.go:207-221`) where the record no longer exists. | Regression test that upserts over a decided row and asserts `ChosenAction` survives. |
| F5 | **`insufficient-evidence` holds get rejected en masse to clear the queue.** | `rejected` is remembered forever (`review_store.go:131`); the folder never resurfaces even after tier-2 probing gives it durations. | D4: UI never offers Reject as the recommended path for these; copy says "leave pending — more evidence is coming". Open question §10.1 asks whether a distinct status is warranted. |
| F6 | **Replay count conflation.** `applied=300` reads as 300 merges when 250 are no-op `separate`s. | Per-action counts in every response (§7.2, §7.3) and in the op log. |
| F7 | **Hard body-bind 400s every existing approve call.** | `_ = c.ShouldBindJSON(&req)` per `replay.go:58`; test with an empty body and no `Content-Type`. |
| F8 | **`groupSignals` refactor silently perturbs the Kind switch.** | The `bykind=` map in the RECONCILE line must be **identical** to the prior prod run (§11). Any drift → stop, do not apply. |
| F9 | **Evidence array bloat.** `memberDurationsSec` on a 133-member group adds ~700 bytes of JSON per hold. At ~360 holds that is ~250 KB total. | Acceptable. Payloads already carry parallel `files`/`discNumbers`/`trackNumbers` arrays of the same length (`regroup_shattered_ai.go:376-385`). |
| F10 | **Mock drift.** `database.Store` embeds `ReviewStore` (`internal/database/store.go:57`); adding an interface method breaks the hand-written `MockStore` compile assertion (`internal/database/mock_store.go:2721-2728`) and the mockery-generated mock. | Plan step 4 edits both; `make mocks` is scoped to the review diff (Makefile:191-197 — mockery v3.7.1 pinned; never commit an unscoped regen). |
| F11 | **An override targets an action whose handler errors** (e.g. `version-group` on members spanning two existing groups — `regroup_apply.go:236-245`). | Unchanged behaviour: the error propagates, `approveOne` returns it, the item stays in its prior status and the operator sees the reason. |

---

## 10. Open questions

1. **Does `insufficient-evidence` deserve its own `ReviewStatus`?** Today it is
   a payload value on a `pending` row. A distinct status (`needs-evidence`)
   would let the queue filter them out of the human's working set and let First
   Aid's missing-input triggering enqueue the producer op. Deferred — it
   touches the status index (`review_store.go:39-43`) and the frontend's status
   filter, and is only worth it once tier-2 exists.
2. **Should the item-level Approve button apply the recommendation, or require
   an explicit pick?** This design says "defaults to the recommendation" per the
   fragment (`ApproveReviewItem` takes an optional body *defaulting to*
   `recommendedAction`). If the owner prefers a forced explicit choice on
   `regroup.ambiguous` specifically, that is a one-line change in `approveOne`
   step 2.
3. **Should `recommendedAction` disagreeing with the hold's `Kind` be
   surfaced as a queue-level metric?** E.g. "38 multidisc holds recommend
   separate" would be a strong signal that the Kind switch needs retuning —
   which D1 explicitly defers.
4. **Does the reason string need i18n / structured form?** Currently a
   pre-formatted English sentence. The evidence object is the machine-readable
   half, so the reason can stay prose.
5. **Bulk `item_action` on a mixed-Kind selection** — should it be refused when
   the targeted ids span more than one Kind? Current design allows it (the
   human named one action explicitly). Flagged for owner review.

---

## 11. Acceptance gate (summary; full numbers in the plan)

Nothing is applied to production until a **dry-run** of
`maintenance.regroup-shattered-ai` on prod reports all of:

- `bykind=` **identical** to the pre-change run (D1/F8).
- New `RECOMMEND:` histogram whose counts **sum exactly** to `groups=`.
- Holds recommending `combine` while `durationsMissing*2 > members`: **exactly 0**
  (D7).
- Holds whose `survivorTitle` equals their dominant member author: **exactly 0**.
- Share of holds carrying the old generic flat-ambiguous string as their *only*
  guidance: **0%** (every hold now carries a reason with numbers), against the
  pre-relink baseline of 762/777 = 98.1%.

`review_apply_enabled` stays **OFF** throughout. Flipping it is owner item 3.
