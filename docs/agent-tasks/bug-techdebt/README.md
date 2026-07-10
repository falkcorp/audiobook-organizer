<!-- file: docs/agent-tasks/bug-techdebt/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5da65c60-ea12-46fa-9dce-5c2d896b431e -->
<!-- last-edited: 2026-07-10 -->

# Workstream — Bug + Tech-Debt Cluster (INIT-9)

Seven small, well-specified debt items: retire the CFG-2 flat-key shim, drain the
staticcheck backlog, break the two sdkguard dependency chains, fix the mock-freshness
CI glob, enroll the cache warmers in bgWG, plan (NOT execute) the repo-size history
rewrite, and verify the W5d-1 Author/Series write-back. From the 2026-07-10
remaining-work master plan (INIT-9 section) and GitHub issues #1536, #1796, #1795,
#1797, #1794, #1650 + TODO.md:62 (STOREFID W5d-1). Spec:
`docs/specs/2026-07-10-bug-techdebt-design.md`; taskboard:
`docs/plans/2026-07-10-bug-techdebt.md` (the authoritative skeleton).

**Initiative gate (verbatim, applies to every task):** PLAN -> EXECUTE AUTONOMOUSLY
per item (worktree/PR/CI) EXCEPT REPO-SIZE-1 (#1650) which is STOP-FOR-HUMAN: a
git-history rewrite is destructive and invalidates every clone/worktree — produce the
migration plan (BFG/filter-repo vs LFS options, coordination checklist, backup
strategy) as a TASK brief whose ONLY deliverable is the plan document, then STOP.

| Task | Item id | Title | Priority | Effort | Tier | Wave |
|------|---------|-------|----------|--------|------|------|
| TASK-01 | CFG-2-D (#1536/CONS-13) | Retire the flat-key compat shim | P2 | M | Sonnet-class | 1 |
| TASK-02 | STATICCHECK (#1796) | Drain staticcheck backlog to green | P2 | L | Sonnet-class | 2 |
| TASK-03 ⚠ | SDKGUARD (#1795) | Break the sdkguard dependency violations | P1 | M | Sonnet-class | 1 |
| TASK-04 | MOCK-GLOB (#1797) | Fix mock-freshness recursive glob | P1 | S | Haiku-class | 1 |
| TASK-05 | WARMERS (#1794) | Enroll 4 cache warmers in bgWG | P1 | S | Sonnet-class | 1 |
| TASK-06 ⛔ | REPO-SIZE-1 (#1650) | History-rewrite migration plan, then STOP | P2 | M | Sonnet-class | 1 |
| TASK-07 | W5D1-VERIFY (TODO.md:62) | Verify Author/Series write-back survival | P2 | M | Sonnet-class | 1 |

## Ground rules

- Go backend + one CI workflow file + docs; no frontend code; no prod-data mutations
  anywhere in this workstream.
- Brief mode: **standalone** — each task is its own worktree + branch + PR +
  `gh pr merge <n> --rebase`. Conventional commits; version headers bumped on every
  touched file.
- Build + test gate for every task in this workstream:
  ```bash
  make ci
  ```
  staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck to files
  you changed; the merge gate is Minimal CI green. The `sdkguard` step of `make ci` is
  ALSO red on main (#1795) until TASK-03 merges — a failure listing only
  `internal/logger` + `internal/dedup/unified` is pre-existing.
- **Verify every file:line anchor with `grep` before editing** — line numbers in each
  brief are a starting point, not a guarantee. Zero-hit at execution time = STOP and report.
- Cross-initiative ownership: the two dedup stub getters (`GetFolderDuplicatesCore` /
  `GetDuplicateBooksByMetadataCore`) are **done in INIT-2 T1/T2 — no task here**;
  `internal/database/embedding_store.go` and `internal/dedup/engine.go` are
  INIT-2-owned for structural edits (TASK-03/TASK-02 carry the coordination notes).
  **Coordinator hard gate:** before dispatching TASK-03 or TASK-02, confirm no INIT-2
  wave is ACTIVE (session/state-level check — the briefs' `gh pr list` grep is only a
  point-in-time fallback for solo runs; see the plan's cross-initiative section).

## Collision / wave note

**Zero code-file collisions**: every code file appears in exactly one task's
exact_files list (matrix computed in the plan doc). Wave 1 = TASK-01, 03, 04, 05, 06,
07 in parallel — Execution mode: /parallel-sweep — trigger: 6 independent tasks (≥3
threshold), disjoint code files per collision matrix. Wave 2 = TASK-02 alone —
Execution mode: SINGLE-AGENT (Sonnet-class) — trigger: 1 task whose file set is
derived from a fresh `staticcheck ./...` run and therefore MUST wait until TASK-01,
TASK-03, TASK-05 and TASK-07 have merged (their deletions/moves change the findings,
and their code files may overlap the run-time-derived file set). TASK-04 does NOT
block wave 2 (ci.yml cannot alter Go findings), and TASK-06's STOP-FOR-HUMAN does NOT
block wave 2 — its PR (the plan document) merges; only the rewrite itself awaits the
human. TODO.md/CHANGELOG.md are shared docs-ledgers exempt from the matrix: rebase
keep-both-sides before every merge.

Coordinator + worker protocol: embedded verbatim in
`docs/plans/2026-07-10-bug-techdebt.md` §"Coordinator protocol" (briefs are standalone
— the protocol governs coordinated /parallel-sweep runs).
