<!-- file: docs/agent-tasks/README.md -->
<!-- version: 3.1.0 -->
<!-- guid: 7a1e0c44-9d2b-4f08-bc31-2e5a6b7c8d90 -->
<!-- last-edited: 2026-08-12 -->

# Agent Task Package

This folder is a **self-contained, manually-run work queue**. Each `TASK-*.md`
file is a complete, copy-pasteable brief that a single AI agent — even a weak one
(Haiku, an older GPT) — can execute end to end without any other context.

You (the human) drive it: pick a task, paste it into an AI agent, let it work,
review, merge. The `ORCHESTRATION.md` + `run-sweep.sh` show how to run several
tasks in parallel on isolated git worktrees.

> This is **not** the automated burndown bot (TODO.md → GitHub issues). These are
> in-repo markdown briefs for hands-on runs.

## Live workstreams (verified against HEAD, 2026-08-11)

**These ten folders are what actually lives in this directory.** Every row was verified by
locating the concrete code artifact each brief promised and grepping for it at HEAD — a ✅ in
a brief is not evidence. Full per-brief evidence with `file:line` citations is in
[`docs/audits/2026-08-11-docs-inventory.md`](../audits/2026-08-11-docs-inventory.md).

**Nothing here is archive-ready.** All ten are ACTIVE or PARTIAL.

| Folder | State | Briefs done | Blocked on |
|---|---|---|---|
| [`abs-sync/`](abs-sync/) | ACTIVE | **10 / 10 written** (index lists 12; TASK-11 and TASK-12 have no brief) | 9 live TODO items; TASK-12's three identity gaps are absent at HEAD |
| [`bug-techdebt/`](bug-techdebt/) | ACTIVE | 5 / 7 | TASK-01 warn-log absent; TASK-02 `staticcheck` exits 1 (5 test-file findings, post-completion drift); TODO #33/#34 |
| [`dedup-pipeline-hardening/`](dedup-pipeline-hardening/) | **PARTIAL — closest to archivable** | 5 / 5 code + 1 operational | ~~contradictory bookkeeping~~ **resolved 2026-08-12**: the prod drain *did* run (prod journal, `apply=true dismissed=7891`); `TODO.md` was stale and is corrected. Now blocked only on T03 (the **sandbox** purge wave, still unrun) and T13 |
| [`error-correction-2026-07/`](error-correction-2026-07/) | ACTIVE | 10 / 13 (inline in `TASKS.md`, no `TASK-*.md` files) | T03 sandbox purge, T04 prod deploy, T13 docs truth-up — the only genuine unchecked boxes in the whole directory |
| [`ux-small-items/`](ux-small-items/) | ACTIVE | 4 present + 1 partial + 1 N/A-by-design / 8 | TASK-05 and TASK-08 have zero implementation; TODO #5/#28/#29 |
| [`torrent-relocation/`](torrent-relocation/) | ACTIVE — **human-gated** | 1 / 7 | TASK-02's STOP-FOR-HUMAN Deluge spike never opened, blocking T03–T07 |
| [`ai-responses-migration/`](ai-responses-migration/) | ACTIVE — **on hold** | 0 / 5 | explicit do-not-start-without-greenlight hold, never lifted |
| [`responses-api-migration/`](responses-api-migration/) | ACTIVE — **on hold** | n/a (`HOLD-STATUS.md` only) | hold never lifted; the doc trails the 2026-07-16 C/D closures |
| [`community-fingerprint-index/`](community-fingerprint-index/) | ACTIVE — **awaiting approval** | n/a (`AWAIT-APPROVAL.md` only) | design spec still `Draft — STOP-FOR-HUMAN`; TODO #13 |
| [`workflow-system/`](workflow-system/) | ACTIVE — **stale gate** | n/a (`AWAIT-APPROVAL.md` only) | superseded 2026-07-18 by an owner-approved plan (`docs/plans/2026-07-13-workflow-system-implementation-plan.md`) that this gate file was never updated to point at |

> ⚠️ **`run-sweep.sh` cannot drive four of these ten.** It discovers work with
> `find -maxdepth 1 -name 'TASK-*.md'`, but `community-fingerprint-index`, `workflow-system`,
> `responses-api-migration` and `error-correction-2026-07` contain no `TASK-*.md` files —
> they use `AWAIT-APPROVAL.md`, `HOLD-STATUS.md`, and `TASKS.md` respectively.
>
> **It now says so.** As of v1.4.0 the script exits **2** with a diagnostic naming what the
> package contains instead and what the gate means, rather than creating no worktrees and
> printing "Next steps" as though it had worked. (Exit **1** remains "no such workstream", so
> a typo and an unparseable package are distinguishable.) `set -e` could never have caught the
> old behaviour: iterating an empty list is not a command failure.

## Completed workstreams (archived)

Planning + cost/efficiency rationale for the 2026-07-01 set is in
[`BREAKDOWN-2026-07-01.md`](../archive/2026-07-consolidation/agent-tasks/BREAKDOWN-2026-07-01.md) (three buckets: authored as
briefs / needs-brainstorm / operational-no-task, plus per-task model tier and the
same-file collision→wave table).

| Folder | What | Priority | Tasks |
|--------|------|----------|-------|
| [`consultancy-roadmap/`](../archive/2026-07-consolidation/agent-tasks/consultancy-roadmap/) | **2026-07-02 consultancy evaluation** implementation tasks — Tier-0 live-defect fixes (CONSULT-1..8), backend-mode toggle, dedup drain/recalibration/auto-resolve, shutdown correctness, ops hardening ([roadmap](../consultancy/00-ROADMAP.md)) | **P0–P3** | 31 |
| [`dedup-hardening/`](../archive/2026-07-consolidation/agent-tasks/dedup-hardening/) | Close the residual exact-layer false-positive leak + defensive guards (DEDUP-INTRO-1 residual, CONS-15, CONS-FRAG-2) | **P1** | 3 |
| [`ci-flaky-fixes/`](../archive/2026-07-consolidation/agent-tasks/ci-flaky-fixes/) | Make the mock-freshness + 2 flaky-test gates trustworthy | **P1** | 3 |
| [`library-ui/`](../archive/2026-07-consolidation/agent-tasks/library-ui/) | Saved filter presets, tag search, Ollama link, Library stale-cache bugfix | P2 | 4 |
| [`dedup-dataset/`](../archive/2026-07-consolidation/agent-tasks/dedup-dataset/) | Labeled-dataset follow-ups: relation classifiers, live-capture, JSONL export (C5, C5-sig, C5-folder, C7, C8) | P2 | 5 |
| [`provenance-hash-chain/`](../archive/2026-07-consolidation/agent-tasks/provenance-hash-chain/) | Download-hash field + integrity alert (HASH-CHAIN-1/3) | P2 | 2 |
| [`perf-cleanup/`](../archive/2026-07-consolidation/agent-tasks/perf-cleanup/) | RunItems migration, caching fast-paths, config-shim retire (ARCH-4b, MAYDEPLOY-H5/H7, NUTSDB, CONS-13) | P3 | 5 |
| [`logging-slog/`](../archive/2026-07-consolidation/agent-tasks/logging-slog/) | Wire `logging.Info(ctx)` into the remaining raw-slog op paths (SLOG-W13 residual) | P3 | 3 |

Each workstream folder has its own `README.md` (overview + wave table),
numbered `TASK-NN-*.md` briefs, an `orchestration.md`, and a `run.sh`.

> **Archived (shipped) workstreams** live in
> [`../archive/agent-tasks/`](../archive/agent-tasks/): `transcription-matching/`,
> `dedup-intro-falsepositive/`, `dedup-ui/`, `system-docs/` — all verified
> complete on 2026-07-01 (see BREAKDOWN doc).

## How to run ONE task (the simple path)

1. Open the `TASK-*.md` file.
2. Paste its **entire contents** into a fresh AI agent session, prefixed with:
   *"You are an autonomous coding agent. Execute this task exactly. Do not skip
   the START HERE setup. Stop and report if any acceptance criterion fails."*
3. When the agent reports done, review its PR and merge it.

## How to run MANY tasks in parallel

See [`ORCHESTRATION.md`](ORCHESTRATION.md) and run [`run-sweep.sh`](run-sweep.sh).
It creates one git worktree per task and emits a ready-to-paste prompt file for
each, so you can fan several agents out at once without them colliding.

## Generic subagent roster (portable — no project-specific agent names)

Every task names a **recommended subagent by capability**, so it works with any
AI tool. Map these to whatever your tool calls them:

| Role | Use for | Typical model tier |
|------|---------|--------------------|
| **code-exploration subagent** | read-only: find files, trace call paths, map a subsystem before editing | cheap/fast |
| **go-backend subagent** | implement Go changes in `internal/**` | mid |
| **frontend subagent** | implement React/TypeScript in `web/src/**` | mid |
| **test-writing subagent** | author Go `_test.go` / Vitest / Playwright tests | mid |
| **code-auditing subagent** | review a diff for bugs, security, convention violations | strong |
| **documentation subagent** | write/restructure markdown docs + Mermaid diagrams | mid |
| **conflict-resolution subagent** | resolve git rebase conflicts, preserving both intents | strong |

A task may suggest splitting itself: e.g. *"first a code-exploration subagent to
confirm the insertion point, then a go-backend subagent to implement, then a
test-writing subagent."* Follow that split when the task is large.

## Universal protocol — EVERY task obeys these

Each task repeats the critical bits inline (weak models don't follow links), but
here is the canonical version.

### 1. Worktree + fresh base (NON-NEGOTIABLE)

Never edit on `main`. Never edit in the primary checkout. Always:

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
SLUG=<task-slug>                                                 # from the task file
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/$SLUG" -b "<branch-from-task>" origin/main
cd "$REPO/.worktrees/$SLUG"
git rebase origin/main      # ensure a fresh base before any edits
```

When finished, clean up: `git -C "$REPO" worktree remove "$REPO/.worktrees/$SLUG"`.

### 2. File version headers (MANDATORY)

Every file you create or modify must carry an updated header. Bump the version
and `last-edited` on **every** change.

- **Go files** (first lines, before `package`):
  ```go
  // file: internal/pkg/name.go
  // version: 1.2.3
  // guid: xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx
  // last-edited: YYYY-MM-DD
  ```
- **All other files** (md/yaml/json/ts/tsx/sh) use HTML/line comments with the
  same four fields. Keep an existing file's `guid`; only generate a new one for a
  brand-new file.

### 3. Commit + PR + merge

- Conventional commit: `type(scope): summary` (feat/fix/refactor/test/docs/chore/perf/ci).
- End every commit body with:
  ```
  Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
  ```
- `gh pr create` then `gh pr merge <n> --rebase` (this repo uses rebase/FF, never squash).

### 4. Build + test gate (before opening a PR)

```bash
go build ./...
go test ./<changed-packages>/ -count=1     # exact packages named in the task
# frontend tasks also: cd web && npm run build && npm test
```

> **Known CI noise (not your fault, do not chase):** the **Mock Freshness** check
> fails on every branch due to a `mockery` version drift (`interface{}`→`any`);
> and `TestBackupEndpointsErrors` / `TestScanService_MultiChapterAudiobook` are
> pre-existing flaky tests. Your gate is your changed packages passing locally.
> See [`dedup-intro-falsepositive/`](../archive/agent-tasks/dedup-intro-falsepositive/) sibling note and
> the `flaky-tests` follow-up in this repo's TODO.md.

## Definition of Done (every task)

- [ ] Worked in a worktree branched off fresh `origin/main`.
- [ ] All acceptance criteria in the task file are checked.
- [ ] Changed-package tests pass locally; new behavior has a test.
- [ ] File headers bumped on every touched file.
- [ ] Conventional commit with the Co-Authored-By trailer.
- [ ] PR opened and rebase-merged.
- [ ] Worktree removed.

## Coding standards

Org standards live in the `.standards/` submodule and `.github/instructions/`.
Key Go/TS rules and the file-header spec are referenced from each task. When in
doubt, match the surrounding code's style.
