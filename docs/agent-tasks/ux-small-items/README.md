<!-- file: docs/agent-tasks/ux-small-items/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: be3ba324-0237-4822-af55-3646e16fbb80 -->
<!-- last-edited: 2026-07-10 -->

# Workstream — INIT-10 Small Open UX/Feature Items

Closes the eight small open UX/feature items from the remaining-work master plan (INIT-10): three verified closeouts (ratings, Audible runtime-mismatch, category ladders), the hash-chain detail view (#1270), the human-gated C8 auto-bug-filing op (#1447), the DOCS-1 documentation set (#1276), the SLOG-W13 op-context logging sweep (#1254), and the SLOG-PROD-VERIFY prod smoke-test (#1255). From `.claude/notes/2026-07-10-remaining-work-master-plan.md` §INIT-10 and `docs/specs/2026-07-10-ux-small-items-design.md`; taskboard: `docs/plans/2026-07-10-ux-small-items.md` (the plan's task-skeleton table is authoritative — this README is a projection of it).

**Gate (initiative, verbatim):** PLAN -> EXECUTE AUTONOMOUSLY where cheap (worktree/PR/CI per item); defer with a written note where not. SLOG-PROD-VERIFY (#1255) is read-only prod verification — allowed autonomously via SSH per house rules, but any prod file/data change discovered as needed -> AskUserQuestion.

| Task | TODO id / issue | Title | Priority | Effort | Tier | Wave |
|------|-----------------|-------|----------|--------|------|------|
| TASK-01 | RATE-5 / TODO.md:1857 | Verify RATE-5 shipped; fix stale User Ratings header | P2 | S | Haiku-class | 1 |
| TASK-02 | HASH-CHAIN-2 / #1270 | Hash chain in book-file detail view | P2 | M | Sonnet-class | 2 |
| TASK-03 | DUR-* / TODO.md:1876 | Audible runtime-mismatch closeout + read-only prod scan | P2 | S | Sonnet-class | 3 |
| TASK-04 | CAT-* / TODO.md:1940 | Category-ladders confirm-no-residual (report-only) | P3 | S | Haiku-class | 1 |
| TASK-05 | C8 / #1447 | Auto-file issue per not_dup cluster (dry-run + human-gated) ⚠ | P3 | M | Sonnet-class | 6 (BLOCKED on INIT-1) |
| TASK-06 | DOCS-1 / #1276 | Comprehensive system docs | P2 | L | SINGLE-AGENT strong (Opus-class) | 1 |
| TASK-07 | SLOG-W13 / #1254 | Op-context slog → logging.* sweep | P2 | L | /parallel-sweep (Sonnet coord + Haiku shards) | 5 |
| TASK-08 | SLOG-PROD-VERIFY / #1255 | Prod op-ID chain smoke-test (read-only Lane A; optional metadata-fetch Lane B is a prod WRITE, AskUserQuestion-gated) | P2 | S | NOT AGENT WORK (coordinator) | 4 |

## Ground rules

- Go backend (`internal/`), React/TS frontend (`web/src/`), docs (`docs/`), and `TODO.md` closeouts; brief mode is **standalone** — each brief is its own worktree + branch + PR + `gh pr merge --rebase` (rebase/FF only; never commit to main).
- Build + test gate for every task in this workstream:
  ```bash
  make ci
  ```
  staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck to files you changed; the merge gate is Minimal CI green.
- **Verify every file:line anchor with `grep` before editing** — line numbers in each brief are a starting point, not a guarantee; every brief carries its anchors' greps verbatim.
- File version headers bumped on every touched file; conventional commits; prod interactions in TASK-03 and TASK-08 Lane A are READ-ONLY (any prospective prod change → AskUserQuestion). CAUTION: the `docs/slog-prod-verify.md` procedure's `metadata-fetch` op is fetch+APPLY (a prod write) — TASK-08 substitutes the read-only `scan-duration-mismatch` maintenance job on its autonomous lane; the metadata-fetch lane requires an explicit AskUserQuestion approval.

## Collision / wave note

**TASK-01, TASK-02, TASK-03, TASK-08, TASK-07, and TASK-05 all edit `TODO.md`.** They MUST run in different waves, serialized in exactly that order (`serialize: wave1=T01, wave2=T02, wave3=T03, wave4=T08, wave5=T07, wave6=T05`) — running any two in parallel would produce a same-file merge conflict on every rebase cycle. TASK-04 (report-only, zero files) and TASK-06 (NEW files under `docs/system/` + one `docs/architecture.md` cross-link) touch disjoint file sets and run in wave 1 concurrently with TASK-01.

| Wave | Tasks | Prereq | Parallel-safe because |
|---|---|---|---|
| 1 | TASK-01, TASK-04, TASK-06 | none | disjoint file sets (T04 has none; T06 is docs/system/ only) |
| 2 | TASK-02 | wave 1 merged + siblings rebased | shares TODO.md with TASK-01 |
| 3 | TASK-03 | wave 2 merged | shares TODO.md with TASK-02 |
| 4 | TASK-08 | wave 3 merged | shares TODO.md with TASK-03; NOT AGENT WORK (coordinator-run) |
| 5 | TASK-07 | wave 4 merged + INIT-3 ownership check on `internal/server/metadata_ops.go` clears + INIT-1/INIT-2 dedup-partition check (`internal/dedup/**`, `internal/plugins/dedup/**`, `internal/database/embedding_store.go` excluded until both merge) | shares TODO.md with TASK-08; /parallel-sweep shards internally disjoint |
| 6 | TASK-05 | wave 5 merged + EXTERNAL: INIT-1 mining-rule PR merged AND re-mine applied on prod AND explicit human go-ahead | shares TODO.md with TASK-07; ⚠ outward action, dry-run + AskUserQuestion |

Additional ownership rules (from the plan): (1) TASK-07's candidate set includes `internal/server/metadata_ops.go` (INIT-3 territory) — excluded from all shards until the INIT-3 check clears, then covered by a trailing shard. (2) INIT-1/INIT-2 dedup partition (master plan §INIT-1): `internal/dedup/**` (engine.go alone has 76 slog sites and is INIT-2-owned for structural edits), `internal/plugins/dedup/**`, and `internal/database/embedding_store.go` are excluded from ALL TASK-07 shards until BOTH INIT-1 and INIT-2 merge, then covered by a trailing shard. (TASK-05's NEW files under `internal/plugins/dedup/` are new paths and do not collide.)

See `docs/plans/2026-07-10-ux-small-items.md` for the full coordinator + worker protocol (embedded verbatim there).
