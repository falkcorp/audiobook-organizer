<!-- file: docs/agent-tasks/filtering-search/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2cb13fb3-db3f-4dda-962e-452f98423d8d -->
<!-- last-edited: 2026-07-10 -->

# Workstream — Filtering & Search Pipeline (INIT-4)

Fixes the two user-visible search correctness bugs (dead field boosts; silently dropped
per-user filters), consolidates per-hit hydration behind a batch getter, parity-locks the
ALREADY-SHIPPED heavy-filter pushdown (T6 was rescoped in review — the pushdown exists at
HEAD; see spec §C6/Decision 9), and makes facets and the dedup boilerplate blocklist
real/configurable. From
INIT-4 of `.claude/notes/2026-07-10-remaining-work-master-plan.md`; spec
`docs/specs/2026-07-10-filtering-search-design.md`; plan
`docs/plans/2026-07-10-filtering-search.md`.

**Gate (verbatim, applies to every task):** PLAN -> EXECUTE AUTONOMOUSLY (worktree/PR/CI per
task). T1/T2 are user-visible correctness fixes — ship first.

| Task | TODO id | Title | Priority | Effort | Tier | Wave |
|------|---------|-------|----------|--------|------|------|
| TASK-01 | INIT-4 T1 | Apply free-text field boosts at query time | P1 | M | Sonnet-class | 1 |
| TASK-02 | INIT-4 T2 | Restore per-user filter application in searchWithBleve ⚠ | P0 | M | Sonnet-class | 1 |
| TASK-03 | INIT-4 T3 | Batch-hydrate Bleve hits via GetBooksByIDs | P1 | M | Sonnet-class | 2 |
| TASK-04 | INIT-4 T4 | Bleve facet counts with DB-distinct fallback | P2 | L | Sonnet-class | 2 |
| TASK-05 | INIT-4 T5 | Boilerplate blocklist → config-extendable module | P2 | M | Sonnet-class | 3* |
| TASK-06 | INIT-4 T6 | Parity-lock the shipped heavy-filter pushdown ⚠ | P2 | L | Sonnet-class | 3 |

\* TASK-05 is additionally gated EXTERNALLY on INIT-2's `internal/dedup/engine.go` waves
merging first (engine.go is INIT-2-OWNED; rebase on top — same partition rule as INIT-1). It
has no internal collisions and may start earlier than wave 3 if INIT-2 merges earlier.

## Ground rules

- Go backend only: `internal/search`, `internal/audiobooks`, `internal/playlist`,
  `internal/database`, `internal/server`, `internal/dedup`, `internal/config`. No frontend.
- Briefs are **standalone**: each task = its own worktree + branch + PR +
  `gh pr merge <n> --rebase`. Never commit to main. Conventional commits. Version headers
  bumped on every touched file.
- Build + test gate for every task in this workstream:
  ```bash
  make ci
  ```
  staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck to files you
  changed; the merge gate is Minimal CI green. TASK-02/03/06 additionally require the FULL
  `go test ./... -short` (cross-package moves / Store interface change).
- **Verify every file:line anchor with `grep` before editing** — line numbers in each brief
  are a starting point, not a guarantee. Zero hits = STOP and report.

## Collision / wave note

**TASK-02, TASK-03, and TASK-06 all edit `internal/audiobooks/service_query.go`**, and
**TASK-01 and TASK-04 both edit `internal/search/bleve_index.go`.** (TASK-02 and TASK-05
also both touch `internal/config/config.go` — already serialized by waves 1 vs 3.) They MUST run in
different waves (serialize: wave1=T02, wave2=T03, wave3=T06; and wave1=T01, wave2=T04) —
running them in parallel would produce a same-file merge conflict on every rebase cycle.
Within a wave the pairs (T01+T02, T03+T04, T05+T06) touch disjoint files and may run
concurrently. A wave starts only after the previous wave's PRs are merged and any open
sibling worktrees are rebased on `origin/main`.

Wave table:

| Wave | Tasks | Prereq | Parallel-safe because |
|---|---|---|---|
| 1 | TASK-01, TASK-02 | none | disjoint file sets (see collision table in the plan) |
| 2 | TASK-03, TASK-04 | wave 1 merged + siblings rebased | T03 shares service_query.go with T02; T04 shares bleve_index.go with T01; T03∥T04 disjoint |
| 3 | TASK-05, TASK-06 | wave 2 merged; T05 also: INIT-2 engine.go waves merged | T06 shares service_query.go with T02/T03; T05 files (dedup/config) disjoint from T06 |

Full collision matrix, dependency graph, and the verbatim coordinator protocol live in the
plan: `docs/plans/2026-07-10-filtering-search.md` (briefs are self-contained; the plan is the
dispatch reference).
