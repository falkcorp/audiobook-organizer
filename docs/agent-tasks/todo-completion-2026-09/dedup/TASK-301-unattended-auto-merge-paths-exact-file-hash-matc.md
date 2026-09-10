<!-- file: docs/agent-tasks/todo-completion-2026-09/dedup/TASK-301-unattended-auto-merge-paths-exact-file-hash-matc.md -->
<!-- version: 1.7.0 -->
<!-- guid: c87b0753-6bb1-45c0-a73c-e02034d8c02a -->
<!-- last-edited: 2026-09-10 -->

# TASK-301 — Unattended auto-merge paths (exact file-hash match, LLM high-confidence verdict) and several bulk/manual HTTP merge endpoints bypass MergeJournaled, so they write no reversal journal -- broader than the tracked MERGE-UNDO scope (DA-02)

> **Status 2026-09-10:** 🆕 NEW — Wave 3 audit finding `DA-02` (audit_dedup_activity.json) · adversarial re-check 2026-09-10: **CONFIRMED**
> **Design fit 2026-09-10 (`audiobook-organizer:expert`, `state/final/design_fit_rows_*.json`): RESHAPE** — 5 direct mergeService.MergeBooks sites (engine.go:1373,4047; handler.go:940,1125,1196) bypass journaling; MergeJournaled(candidateID, aID, bID, keepID, tag) is strictly pairwise and candidate-keyed. handler.go:940 and :1196 merge N-ary clusters with no candidate row and cannot route through it as written.
> **Reshape to:** Generalize undo-ledger journaling into an N-ary, candidate-optional helper (bulk MergeJournaled variant) that the two cluster-merge handlers and engine.go can all call.
**Priority:** P1 · **Effort:** M · **Recommended subagent:** Opus-class · dedup subagent · **Depends on:** none · **Wave:** per ../orchestration.md (collision-aware) · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: Wave 3 audit finding `DA-02` (audit_dedup_activity.json) · adversarial re-check 2026-09-10: **CONFIRMED**. Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # the primary checkout (same path convention as every carried brief)
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/dedup-301" -b agent/dedup-301-unattended-auto-merge-paths-exact-file-h origin/main
cd "$REPO/.worktrees/dedup-301"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

**Reshaped by the design-fit review (2026-09-10) — build THIS, not the original suggestion:** Generalize undo-ledger journaling into an N-ary, candidate-optional helper (bulk MergeJournaled variant) that the two cluster-merge handlers and engine.go can all call.

Route handleFileHashMatch, ApplyVerdicts' auto-merge, and the three HTTP bulk/manual merge handlers through Engine.MergeJournaled (or extend MergeJournaled/expose an equivalent on merge.Service) instead of calling mergeService.MergeBooks directly; update TODO.md's MERGE-UNDO item to reflect the current (larger) scope.

Why it matters: TODO.md:6523 (MERGE-UNDO) already tracks that UnmergeAuto has no production caller and that 'only the auto-resolve path journals', but that description describes the pre-merge_journaled.go state and only names 'the review lane' as the gap. merge_journaled.go was since added specifically to close that gap for human-initiated merges, and handler.go:1486 was updated to require it (refusing to merge at all if the engine is unavailable, rather than merge unjournaled). That fix was not propagated to the two automatic/unattended auto-merge triggers (which are arguably the MOST in need of an undo trail, since no human reviewed the pair before it merged) or to the three bulk/manual HTTP endpoints that can merge many books in one call. The current state is inconsistent: the codebase clearly decided unjournaled merges are unacceptable (handler.go:1486's explicit refusal-over-silent-fallback design) for one endpoint, while five other paths -- including the two that run with zero human review -- still perform the exact unjournaled merge that design was built to prevent.

## Background (verify before editing)

- internal/dedup/merge_journaled.go's doc comment (lines 17-26) states the design goal explicitly: 'This sequence used to live only inside autoMergeCertain ... Centralising it here is what makes "every merge is reversible" a property of the engine rather than a habit of one caller.' Grep for mergeService.MergeBooks vs .MergeJournaled( across the codebase shows only TWO call sites use MergeJournaled: auto_resolve.go:299 (Tier-1 CERTAIN auto-resolve) and internal/server/handlers/dedup/handler.go:1486 (the single-candidate manual-apply endpoint, whose own comment at handler.go:936-941 says 'Refuse rather than merge irreversibly ... a merge a human dispatched by keystroke is precisely the one most likely to be a mistake'). Five other live merge-shaped call sites call mergeService.MergeBooks directly with NO journal entry: engine.go:1373 (checkExactFileHash/handleFileHashMatch -- fires automatically and unattended on every FullScan Layer-1 pass whenever config.Dedup.AutoMergeEnabled is on, with no CERTAIN-band or corroboration gate beyond same-author+same-title+file-hash match), engine.go:4047 (ApplyVerdicts' LLM high-confidence auto-merge path), and internal/server/handlers/dedup/handler.go:940 (bulk cluster-merge), :1125 (bulk candidate-merge -- its own adjacent comment literally calls this 'the hardest write in the system to undo', applycap.Refuse comment ~line 1105-1111), and :1196 (the generic POST /audiobooks/merge endpoint with an explicit body.BookIDs list).
- Anchor: `internal/dedup/engine.go:1373` (audit `DA-02`, confidence high, severity high).
- Related tracking: TODO.md:6523 MERGE-UNDO (tracks a narrower/stale version of this gap -- pre-dates merge_journaled.go and only names the review lane, not the two automatic auto-merge triggers or the three bulk/manual HTTP endpoints)
- **Adversarial re-check (2026-09-10, `state/final/adversarial_top11.json`): CONFIRMED** — Exactly the five unjournaled sites: engine.go:1373 (handleFileHashMatch auto), engine.go:4047 (ApplyVerdicts LLM high-confidence), handler.go:940/:1125/:1196 (bulk/manual HTTP). Only auto_resolve.go:299 and handler.go:1486 call MergeJournaled.
  - Blast radius: FullScan Layer-1 auto-merge, ApplyVerdicts, 3 HTTP endpoints. handler.go uses h.mergeService (narrower than Engine); routing through MergeJournaled needs an interface-shape decision, not a one-line swap.
  - Existing tests to extend: none for journal emission at these 5 sites
  - Standing-ban contact: none directly; core dedup/merge — validate on the dedup sandbox
  - Note: TODO.md:6523 MERGE-UNDO does not already track these gaps.

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  test -e internal/dedup/engine.go   # the file the finding is anchored to still exists (-e: a directory anchor is valid too)
  sed -n '11,32p' internal/dedup/engine.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  sed -n '1099,1117p' internal/dedup/engine.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  sed -n '1367,1379p' internal/dedup/engine.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  sed -n '4041,4053p' internal/dedup/engine.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  sed -n '934,946p' internal/server/handlers/dedup/handler.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  sed -n '1480,1492p' internal/server/handlers/dedup/handler.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  ```

## Step-by-step

1. Re-run the re-verify anchors; read the surrounding function end-to-end and confirm the finding still holds at HEAD (if it does not, STOP and report).
2. Implement the fix described under Goal in `internal/dedup/engine.go` (and any sibling that shares the same shape — grep for the pattern before assuming there is one copy).
3. Write the regression test first (it must fail against the pre-fix code), then make it pass.
4. Run the gate; add the changelog fragment; bump headers; report with exact counts.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_dedup_301.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave. In your final report, state the exact `TODO.md` line text (or `todo.d` fragment) to check off.

## Tests

- A regression test that reproduces the defect described in Background and fails on the pre-fix code.
- Existing package tests stay green (`-count=1`).
- ONLY if the fix adds or changes a write/apply/repair path (see Idempotency / Rollback): a test proving the dry-run / guard path writes nothing (fail-closed on error). A pure code change (lock, bound, check, propagated error) does not need this — do not add a dry-run surface to satisfy it.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/dedup/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched.

## Acceptance criteria

- [ ] Every re-verify anchor above still hits (or the report says which moved and where).
- [ ] The tests listed above exist and pass; a regression test reproduces the original defect and fails on the pre-fix code.
- [ ] Gate green: the command in **How to test** exits 0; `go vet`/lint clean on touched packages.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-09-10" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260910_dedup_301.md`.

## Commit message

```
fix(dedup): Unattended auto-merge paths (exact file-hash match, LLM high-confidenc (DA-02)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

Decide this FIRST and write the answer in your report: **does the fix add or change a path that writes, moves, or deletes persisted data or files** (an apply/repair/delete/migration path)?

- **NO** — the fix is a lock, a bound, a check, an error propagated, a header, a config value: pure code change. Rollback = `git revert` the commit. Already-done check = the re-verify anchors above show the new code (add the exact `grep -n '<new symbol or string>' <file>` you used to your report). Do NOT invent a dry-run/`apply` parameter that the Goal did not ask for.
- **YES** — **`git revert` does NOT restore data.** Mandatory: the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; the apply path journals enough to undo; a test proves the dry-run writes nothing; the PR is held for the owner.

## Coordinator notes

review_critical=true: prod-data path per CLAUDE.md's review-critical definition — hold the PR for the owner.
