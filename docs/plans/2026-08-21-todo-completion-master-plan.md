<!-- file: docs/plans/2026-08-21-todo-completion-master-plan.md -->
<!-- version: 1.1.0 -->
<!-- guid: b53623ab-e690-413e-bb0a-f40a493ed31f -->
<!-- last-edited: 2026-08-21 -->

# TODO completion — master plan and Fable handoff

**Status:** PROPOSED. Planning only; no planned work was executed.
**Audience:** the Fable instance that will refine this into subtasks and dispatch subagents.
**Companion:** [`2026-08-21-todo-completion-inventory.tsv`](2026-08-21-todo-completion-inventory.tsv)
— all 385 parsed rows (≈378 distinct; see §1), tiered, with `TODO.md` line anchors
and collision domains.

---

## 1. What the corpus actually is

`TODO.md` is 10,849 lines and encodes tasks **two different ways**. A census that sees
only one of them is wrong, and the first pass of this analysis was wrong for exactly
that reason.

| Encoding | Where | Open | Done |
|---|---|---|---|
| Checkbox bullets `- [ ]` | body, lines 8–10,484 | **335** | 63 |
| Numbered backlog `N.` (done = `~~struck~~`) | tail, lines 10,486–10,849 | **50** | 4 |
| | raw sum | 385 | 67 |
| | **distinct open, after dedup** | **≈378** | |

The two encodings **overlap**: the tail preamble describes itself as a curated
index of "items confirmed ACTIVE by the 2026-07-17 docs audit", so some numbered
entries re-describe work the body already carries. Fuzzy-matching the 50 numbered
titles against the 335 checkbox bodies (bold-ID match, then ≥0.55 token overlap)
finds **7 overlaps**, giving **378 distinct open items**.

> ⚠️ The 7 is a *heuristic* match, not a verified one — treat 378 as ±7 and
> re-derive it during Wave 2, when every item has a resolved anchor. Do not report
> progress against 385: that number double-counts.

Plus three registers that are not in `TODO.md` at all:

- **54 open GitHub issues**, every one carrying a `todo-id:` label (`todo-sync`).
- **10 rows** in [`DECISIONS-PENDING.md`](DECISIONS-PENDING.md) — the stop-for-human queue.
- **2 unfolded fragments** in `todo.d/` (`dual-path-settings-panel`, `condition-based-op-resume`).

### 1.1 Instrument caveats — read before trusting any number here

Two measurement errors were made and corrected while producing this plan. Both are
recorded because the same traps will catch the refinement pass.

1. **A checkbox grep is not a census.** `grep -c '^\s*- \[ \]'` returns 342. The
   structural parse returns 335 top-level open checkboxes + 7 *nested* subtasks. The
   grep silently conflates task and subtask.
2. **A bold-ID reconciliation produced 24 false orphans.** Cross-referencing the 54
   issues against `- [ ] **ID**` bullets suggested 24 issues had no live TODO item and
   were therefore closeable. Spot-checking 18 of them found **17 present** in
   `TODO.md` — they live in the numbered tail backlog, which uses `N. **ID**`, not a
   checkbox. Only `DOCS-1` (#1276) was genuinely absent. **Do not close an issue on
   an ID-match miss; grep the bare ID across the whole file first.**

The corrected issue reconciliation:

- 30 issues map to an open checkbox task.
- 23 issues map into the numbered tail backlog.
- **1 issue (`DOCS-1` #1276) has no live `TODO.md` item** — candidate close, verify first.
- 2 open TODO items have **no** issue: `MERGE-UNDO`, `TODO-MUI-4`.

---

## 2. Tiers

Counts are exact and mechanical, from the inventory TSV.

| Tier | Meaning | Count |
|---|---|---|
| **A** | Sits under a `✅`/`FIXED`/`ANSWERED`/`SHIPPED` heading, or is self-marked done — **closeable or already done** | **37** |
| **B** | Gated on an owner decision — **must not be dispatched to an agent** | **29** |
| **C** | Actionable work | **319** |

> Tier A/B classification is *keyword-derived* and is a triage hypothesis, not a verdict.
> Every Tier A item needs one confirming grep before its box is checked. Two numbered
> backlog items (`TODO.md:10622` CPU busy-loop, `TODO.md:10641` op-progress metric) are
> annotated `✅ DONE` inline yet were never struck through — proof the file carries stale
> entries in both encodings.

### 2.1 Tier B — the blocking list (do NOT let an agent decide these)

This is the highest-stakes part of the handoff. Several Tier B items sit on **production
data-loss paths**. An agent that treats one as actionable will make a unilateral call.

Hard blockers already formally queued in `DECISIONS-PENDING.md`:

| # | Decision | Blocks |
|---|---|---|
| 1 | `REPO-SIZE-1` — how to shrink 1.69 GB of history | INIT-9 close-out, `TOOL-1` (#2673) |
| 2 | `INIT-5 T2` — Deluge spike sign-off | whole torrent-relocation track |
| 3 | `INIT-6` — workflow-system spec review | `WF-2/3/4/5`, PR #1935 |
| 4 | `INIT-8` — community fingerprint index | entire community-index track |
| 5 | `INIT-7` — Responses-API greenlight | `AI-RESP-A/B/E/F` (#1260–#1265) |
| 6 | `internal/server` test-package structure | `TODO-SRVTIMEOUT` (#2112), CI-stall class |
| 7 | Product rename (1.17) | branding sweep |
| 8 | Flip `review_apply_enabled` in prod | review-queue apply doing real work |
| 9 | PH-2b purge-wave scope | draining the exact-pending backlog |
| 10 | `ComposeScore` confidence clamping | whether calibration can move auto-merge scores |

Additional decision-gated items found in `TODO.md` that are **not** yet in that queue —
these need adding:

- `TODO.md:2945` — explicitly labelled `🔴 OWNER DECISION — do not pick this unilaterally`.
- **The 16,265 fully-broken books** (missing-file lane) — every file entry dead; needs a
  human call. Related: missing-file repair is **REPORT-ONLY** per #2614 — the repair must
  **REPOINT**, never delete.
- **41.8% of `book_file` rows have no bytes** (#2515/#2516) — repair approach undecided.
- **E08 write-back** — deferred pending measurement, not pending implementation.
- ABS play-counts/listening-history surface — `TODO.md:9909` "Decision needed before building".
- Chapters backfill E02 run decision — `TODO.md:10018`.

**Rule for the refinement pass: a Tier B item becomes an `AskUserQuestion`, never a brief.**

### 2.2 Tier C — actionable, but only 125 of 319 are brief-ready

This is the finding that most shapes the execution plan.

| | Count |
|---|---|
| Tier C items citing ≥1 concrete file path | **125** |
| Tier C items citing **no** file path | **194** |
| Tier C items spanning **>1** collision domain | 40 |

The 194 pathless items are *not* prose noise — median body length is 350 characters and
they carry real specifications. They cite **symbols, fields and behaviours** instead of
paths (`positionSyncStore`, `Book.FilePath`, "the primary-version filter"). They are
briefable, but only after an anchor-resolution pass turns each into `file:line` +
re-verify grep.

**Consequence: you cannot dispatch subagents against Tier C directly.** The first real
wave of work is a scoping wave, not an implementation wave.

Collision domains for the 125 path-cited items (primary domain):

| Domain | Items |
|---|---|
| `web` (frontend) | 22 |
| `docs` | 22 |
| `internal/database` | 15 |
| `internal/dedup` | 8 |
| `internal/server/handlers` | 5 |
| `ci/scripts` | 4 |
| `internal/operations` | 4 |
| `internal/metafetch` | 4 |
| `internal/audiobooks` | 4 |
| `internal/itunes` | 4 |
| `internal/plugins/maintenance` | 4 |
| 18 further domains | 1–3 each |

> **Primary domain = most-specific code domain; `docs` only when it is the sole
> domain.** An earlier cut of this table assigned the primary domain
> alphabetically, which silently filed **16 code tasks under `docs`** (because
> TODO items routinely cite a `docs/…` evidence link alongside the code they
> describe) and understated `web` by 6. If you regenerate the TSV, keep the
> priority rule — the naive `sorted()[0]` reintroduces the bug.
>
> 3 items cite both `web/` and `internal/`; they are cross-cutting, not frontend.

---

## 3. Territory that is already claimed

Do not plan work into these — they are live.

| Worktree | Branch | State |
|---|---|---|
| `abo-env-consolidation` | `refactor/env-var-consolidation` | 1 commit ahead, clean, **no PR** |
| `abo-mobile` | `fix/mobile-table-overflow` | 1 commit ahead, clean, **no PR** |
| `abo-unified-review` | `fix/reap-report-records-outcome` | 1 commit ahead, clean, **no PR** |
| `audiobook-organizer-summarize-by-day` | `fix/summarize-by-day` | 1 commit ahead, clean — **live session (pid 16904)** |

As of 2026-08-21 there were **0 open PRs** on the repo (this plan's own branch aside).
Four finished commits are sitting unshipped.

Also live per the handoff: `feat/persist-missing-file-verdict` (`9b43f598`, missing-file
Phase 1a) — committed and pushed, **no PR, not mutation-tested**.

---

## 4. Execution plan

### Wave 0 — Ship what is already built (serial, ~1 session, no agents)

Cheapest possible progress: four completed commits and one pushed branch have no PRs.

1. Open + merge PRs for `abo-env-consolidation`, `abo-mobile`, `abo-unified-review`
   (confirm with the owner of `fix/summarize-by-day` before touching it — that session is live).
2. Mutation-test and open the PR for `feat/persist-missing-file-verdict`.
3. `git worktree remove` each one after its PR merges.

**Execution mode:** `SINGLE-AGENT (strong model)` — merge-order and rebase judgment.
**Exit criteria:** `git worktree list` is back to the main checkout + the plan worktree.

### Wave 1 — Tier A close-out (parallel-safe, mechanical)

37 items. Each needs *one* confirming grep, then a direct edit checking the box (checking
off is an explicit exception to the fragment rule — it is a normal direct edit).

Also in this wave: verify and close `DOCS-1` (#1276), and reconcile the 23 tail-backlog
issues against their numbered entries.

**Execution mode:** `/parallel-sweep` — ≥3 mechanically similar tasks, and the only file
touched is `TODO.md`. ⚠️ **Every task in this wave collides on `TODO.md`.** Either run it
serially, or have one agent verify all 37 and a single commit apply all the check-offs.
The second shape is strongly preferred.

**Exit criteria:** distinct open-item count drops from ≈378 to ≈341, with a one-line evidence note
per closed item.

### Wave 2 — Anchor-resolution / scoping pass (parallel, read-only)

The gate to everything downstream. 194 pathless Tier C items → scoped tasks.

Dispatch **read-only** scouts (this is exactly the `plan-op:repo-scout` contract), one per
`TODO.md` section, each returning for every item: `exact_files`, a verified `file:line`
anchor, a re-verify grep that resolves at HEAD, and a size estimate. Items whose anchors
no longer resolve are re-classified as Tier A (stale) — expect a meaningful number.

**Execution mode:** `/parallel-sweep` over disjoint read-only scopes; **no agent writes code
in this wave.**
**Exit criteria:** every Tier C item has either a resolvable anchor or a stale verdict.
Only then is the collision matrix real.

### Wave 3 — Implementation waves, ordered by collision domain

Build the wave table **from the Wave 2 output**, not from this document — the domain
counts in §2.2 cover only the 125 items whose paths were already citable.

Sequencing rules that hold regardless:

- **`internal/database` is the chokepoint.** The store-decoupling lane is largely closed
  (refs 172→8), but 13 path-cited items plus most interface-width work still land there.
  Serialize it. Do not run it alongside `internal/server/handlers`.
- **`docs` (22 items) is parallel-safe** and shares no files with code work. Run it
  concurrently with any code wave as filler. This is 22, not 37 — the other 15 cite a
  `docs/` evidence link but edit code, and belong to their code domain.
- **`web` (22 items) is disjoint from all Go work.** Free parallelism — but the MUI
  upgrade chain (`TODO-MUI-1/2/3/4`, #2216–#2218) is strictly serial within it, and
  `MuiMenu`/`theme.ts:186` is a known regression surface.
- **The 40 cross-cutting items are `SINGLE-AGENT` work.** They span domains by
  construction; parallelizing them is what produces rebase conflicts.
- **Any item on a prod-data path is `SINGLE-AGENT (strong model)` and never weak-tier** —
  missing-file repair, dedup merge/apply, `book_file` repair, organize/rename.

### Wave 4 — Prod runs and verification

A distinct class: several items are not code at all but **operations awaiting a run**
(`PH-2`, chapters backfill E02, `SLOG-PROD-VERIFY`, `I1`/`I6` pprof, `ROWCOUNT-REVERIFY`,
iTunes PID repair apply). These are `NOT AGENT WORK`. They route to
[`pending-prod-actions.md`](../operations/pending-prod-actions.md), and several are gated
on Tier B decisions.

⚠️ **A running scan clobbers applied metadata — never apply during a scan.** This
constraint binds most of Wave 4.

---

## 5. Standing constraints the refinement pass must not relax

- **Worktree per task; never edit or commit to `main`.** Every worktree needs
  `npm ci --prefix web`. **Never `go work init .`** — it breaks the build (`ambiguous
  import` on the split `genproto` modules).
- **New tasks go in `todo.d/` fragments, never straight into the `TODO.md` inbox.**
  Fragments carry **no** file-version header. Checking a box off *is* a normal direct edit.
- **`CHANGELOG.md` is assembled from `changelog.d/`** — never hand-edited. CI enforces a
  fragment per PR.
- **Every changed file bumps its version header** (`file`/`version`/`guid`/`last-edited`).
- **Concurrency is mandatory** for any whole-library loop doing per-item work — bounded
  worker pool sized to `runtime.NumCPU()`, never unbounded fan-out. A `dedup.full-scan`
  went silent for 3+ hours on one core for want of this.
- **Status reports carry exact counts** — `COMPLETED: n` / `REMAINING: n` / `BLOCKED: n`.
  Never "all done" without a number.
- **Never launch overlapping waves on related files.**

---

## 6. Handoff instructions — for the Fable instance

Run these in order. Do not skip 1 or 2.

1. **Resolve Tier B first.** Present all 10 `DECISIONS-PENDING.md` rows plus the 6
   additional gated items in §2.1 to the user as `AskUserQuestion` batches. Nothing in
   Tier B may be briefed. Several unblock large Tier C tracks, so answers here change
   the wave plan.
2. **Execute Wave 0.** It is pure profit and needs no planning.
3. **Run Wave 1 as a single verify-then-one-commit task**, not as a parallel sweep —
   all 37 items collide on `TODO.md`.
4. **Run Wave 2 (scoping) before writing any implementation brief.** Briefs written
   against the current corpus will cite anchors that do not resolve; the repo's own
   audit tooling (`plan-op:plan-auditor`) fails a brief whose re-verify grep returns zero
   hits, which is the correct behaviour and will reject them.
5. **Then generate briefs**, one per scoped Tier C item, and run
   `plan-op:brief-verifier` over each (it role-plays a cold weak model with zero context —
   which is exactly what your subagents will be), followed by `plan-op:plan-auditor` for
   the mechanical pass: re-verify greps at HEAD, recomputed collision matrix, header lint.
6. **Only Bucket-1 quality briefs get weak-model subagents.** Anything design-adjacent,
   schema-touching, or on an irreversible write path is `SINGLE-AGENT (strong model)`.

The inventory TSV is the working set. Sort by `tier`, then `domains`, then `todo_line`.

---

## 7. Rollback

This plan produces documents only. To roll back: delete the two files and remove the
plan worktree. No code path is touched, no data is migrated, nothing is deployed.

For the waves themselves, rollback is per-PR: the repo uses rebase/FF merges with no
branch protection (deliberately removed 2026-08-20 — auto-revert replaces it), so a bad
merge is reverted forward, not force-pushed away.

---

## 8. Status

```
COMPLETED: 2 — this plan; the tiered inventory (TSV, 385 rows / ≈378 distinct)
REMAINING: 5 — Wave 0 (ship 4 branches), Wave 1 (37 Tier A close-outs),
               Wave 2 (194-item scoping pass), Wave 3 (implementation waves),
               Wave 4 (prod runs)
BLOCKED:  29 — Tier B, decision-gated; 10 formally queued in DECISIONS-PENDING.md
               plus 6 unqueued gated items identified in §2.1
```

**NO implementation was executed — planning only.**
