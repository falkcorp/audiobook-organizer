<!-- file: docs/agent-tasks/ux-small-items/TASK-06-docs-1-system-docs.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4edcf540-ea65-48de-abf7-144e4e2cc8c2 -->
<!-- last-edited: 2026-07-10 -->

# TASK-06 — DOCS-1: comprehensive system documentation (#1276) — SINGLE-AGENT (strong model)

**Gate:** PLAN -> EXECUTE AUTONOMOUSLY where cheap (worktree/PR/CI per item); defer with a written note where not. SLOG-PROD-VERIFY (#1255) is read-only prod verification — allowed autonomously via SSH per house rules, but any prod file/data change discovered as needed -> AskUserQuestion.
**File-ownership:** none — writes only NEW files under `docs/system/` plus one cross-link edit in `docs/architecture.md`; no `internal/server/` contact; no TODO.md edit (issue-tracked item), so parallel-safe in wave 1.

**Priority:** P2 · **Effort:** L · **Recommended subagent:** ⛔ NOT A WEAK-MODEL BRIEF — SINGLE-AGENT, Opus/strong-class · docs-synthesis subagent · **Why:** whole-system synthesis across dedup/matching/ops/stores/frontend exceeds what a cold weak model can hold; dispatching this to Haiku/Sonnet produces confident wrong documentation, which is worse than none · **Depends on:** none

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/ux-small-items-docs-1-system-docs" -b agent/ux-small-items-docs-1-system-docs origin/main
cd "$REPO/.worktrees/ux-small-items-docs-1-system-docs"
git rebase origin/main
```

## Goal

Close GitHub issue #1276 (DOCS-1, verified OPEN 2026-07-10): ensure a comprehensive, current system documentation set exists under `docs/system/` meeting the scope quoted verbatim in Background below, then close the issue. ⚠ **KNOWN STATE DISCREPANCY (measured at HEAD 2026-07-10):** `docs/system/` ALREADY contains a 9-page set (`README.md`, `architecture.md`, `pipelines.md`, `storage.md`, `api.md`, `runbooks.md`, `components.md`, `incidents.md`, `deploy-and-gpu-ops.md`) totaling 12 Mermaid diagrams, and its README states "Status: DOCS-1 workstream complete" — yet issue #1276 (an auto-synced burndown item) is still OPEN. Your job is therefore an **audit-and-gap-fill, not fresh authoring**: verify the existing set against the quoted scope, fill only genuine gaps, refresh stale content (the set was last edited 2026-06-29 — check it against post-June work like the STOREFID unification and the #19 dedup-scoring fixes), and close #1276. EXTEND and cross-link the existing partial docs rather than duplicating them; where an existing doc is current, the page links to it and summarizes; where it is stale, note the staleness inline rather than silently contradicting it. Quality bar: the burndown-tasks docs are the repo's gold standard — more is better.

## Background (verify before writing)

- **Issue #1276's requested scope, quoted verbatim from the issue body (2026-07-10) so this brief is self-contained — this inlined text is the requirement source:**
  > Write comprehensive system documentation for `jdfalk/audiobook-organizer` into `docs/` — full process graphs, architecture diagrams, data flow, component inventory, operations runbooks, incident history. Target: ≥9 files, ≥7 Mermaid diagrams (flowchart, sequence, state machine, Gantt). Model after `jdfalk/burndown-tasks/docs/` (PR #73, 2216 lines).
  Coverage checklist derived from that body — each item must have a page (or a section plus README pointer): (1) process graphs / data flow, (2) architecture diagrams, (3) component inventory, (4) operations runbooks, (5) incident history, (6) API/storage detail as needed to support the above. Numeric floor: ≥9 files under `docs/system/`, ≥7 Mermaid diagrams total.
- **`docs/system/` already exists and largely satisfies the floor** (measured 2026-07-10 at HEAD: 9 `.md` files, 12 mermaid code fences; README claims "DOCS-1 workstream complete", last-edited 2026-06-29). Do NOT re-author from scratch — audit for scope gaps and staleness, then gap-fill. One gap is already known at dispatch: `docs/system/deploy-and-gpu-ops.md` exists but is NOT linked from `docs/system/README.md` — adding that index link is in scope.
- Existing partial docs OUTSIDE `docs/system/` to inventory (do not duplicate — link/summarize instead): `docs/architecture.md` (top-level architecture), `docs/database-pebble-schema.md` (PebbleDB key schema), `docs/database-architecture.md` (store layering), `docs/developer-guide.md` (contributor setup/workflow), `docs/archive/implementation-guide.md` (feature implementation walkthroughs), `docs/archive/technical_design.md` (original design doc, oldest and most staleness-prone). You only need to open the ones relevant to a gap you are actually filling.
- Doc content must be grounded in code at YOUR HEAD — cite files by path + symbol (never bare line numbers) so the docs survive drift.

- **Re-verify these anchors before writing**:
  ```bash
  ls docs/system/                                     # expect 9 files at dispatch time (set exists — audit, don't re-author)
  grep -c '```mermaid' docs/system/*.md               # expect 12 total at dispatch time (floor is >=7)
  ls docs/*.md | head -30                             # existing doc inventory outside docs/system/
  gh issue view 1276 --repo falkcorp/audiobook-organizer --json state,body   # confirm still OPEN and body still matches the verbatim quote above; if the body changed, the LIVE body wins — note the delta in PLAN.md
  ```

## Step-by-step

1. Run the anchors above; scope = the verbatim issue-body quote in Background (re-verified against the live issue by the last anchor).
2. Write `PLAN.md` at the worktree root (house rule): gap-audit table (each of the 6 coverage-checklist items → which existing `docs/system/` page covers it, or GAP), pages you will create/extend, per-page source-of-truth files in the code, order, and the cross-link plan. For a >300-line deliverable set this plan is mandatory before authoring. If the audit finds ZERO gaps and zero staleness, the remaining work is just Steps 4–5 + closing #1276 — say so in PLAN.md and the PR body.
3. Fill the gaps under `docs/system/`: NEW pages get the mandatory 4-line header (`file:` repo-relative, `version: 1.0.0`, fresh guid via `uuidgen | tr 'A-Z' 'a-z'`, `last-edited:` today); EXTENDED pages get a version bump + `last-edited` refresh, guid unchanged. Keep the index (`docs/system/README.md`) linking every page.
4. Add one cross-link from `docs/architecture.md` to the new index; bump its header version.
5. Purely additive elsewhere: do NOT rewrite existing docs wholesale; do NOT touch code, TODO.md, or CHANGELOG beyond the standard CHANGELOG entry for the PR.
6. Anti-over-suppression: N/A (no filter/guard/veto/skip/dedupe path added).

## How to test

```bash
make ci
```
staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck to files you changed; the merge gate is Minimal CI green. (Docs-only PR; Minimal CI green is the merge condition. Additionally verify every new file has its 4-line header: `head -4 docs/system/*.md`.)

## Acceptance criteria

- [ ] `test -f docs/system/README.md` (index exists) and it links every page: `ls docs/system/*.md | wc -l` ≥ 9 AND every non-README filename appears in `grep -o '[a-z-]*\.md' docs/system/README.md`.
- [ ] Numeric floor from the issue body met: ``cat docs/system/*.md | grep -c '```mermaid'`` ≥ 7 (12 at dispatch time).
- [ ] Each of the 6 coverage-checklist items in Background (process graphs/data flow, architecture diagrams, component inventory, runbooks, incident history, API/storage) is mapped in PLAN.md's gap-audit table to a `docs/system/` page — or explicitly deferred with a written reason in `docs/system/README.md` (checkable: `grep -in 'deferred' docs/system/README.md` lists each deferral).
- [ ] `head -4` of every new file shows the 4-line header with a unique guid.
- [ ] Zero bare `file:line` citations in the new docs (`grep -rn ":[0-9]\{2,\}" docs/system/ | grep -v http` reviewed — symbol-based citations only).
- [ ] Anti-over-suppression: N/A
- [ ] Minimal CI green; `docs/architecture.md` header bumped.
- [ ] PR body contains `Closes #1276`.

## Commit message

```
docs(system): comprehensive system documentation set (DOCS-1, #1276)

Index + per-subsystem pages grounded in code symbols at HEAD; extends and
cross-links existing partial docs instead of duplicating them.

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/ux-small-items-docs-1-system-docs
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

`test -f docs/system/README.md` WILL succeed at dispatch time — that alone does NOT mean the task is done (the set predates this brief and issue #1276 is still open). The task is applied only when the acceptance checks above ALL pass against the 6-item coverage checklist AND issue #1276 is closed; otherwise run the gap-audit (Step 2) and fill what is missing. If the audit truly finds zero gaps/staleness, the residual deliverable is the cross-link (Step 4) + a PR whose body says `Closes #1276` with the audit table as evidence. Rollback = revert the commit; docs-only, nothing else affected.
