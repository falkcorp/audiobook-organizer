<!-- file: docs/agent-tasks/dedup-pipeline-hardening/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: 689ea92f-ac45-440a-bef8-75b4bf08228d -->
<!-- last-edited: 2026-07-10 -->

# Workstream — Deduplication Pipeline Hardening (INIT-2)

Revive the two dead book-dedup tiers (stub store getters), bound the exact-layer candidate
explosion (#1512) and drain its ~387k backlog under a human gate, and remove the two hotspots
(full-table candidate scan, single-mutex emit()). From INIT-2 in
`.claude/notes/2026-07-10-remaining-work-master-plan.md` and
`docs/specs/2026-07-10-dedup-pipeline-hardening-design.md`; plan/taskboard:
`docs/plans/2026-07-10-dedup-pipeline-hardening.md`.

**Gate (verbatim, every task):** PLAN -> EXECUTE AUTONOMOUSLY (worktree/PR/CI per task).
EXCEPTIONS: T3's 387k-backlog drain and T6's CONS-10 prod drain are prod-data mutations ->
dry-run FIRST, then a real AskUserQuestion apply gate.

**File-ownership (verbatim):** INIT-2 OWNS all structural edits to `internal/dedup/engine.go`
and `internal/database/embedding_store.go`. INIT-1 rebases its single engine.go touch on top
AFTER INIT-2's engine.go waves merge. Never schedule INIT-1+INIT-2 waves touching engine.go
concurrently.

| Task | Source id | Title | Priority | Effort | Tier | Wave |
|------|-----------|-------|----------|--------|------|------|
| TASK-01 | INIT-2 T1 | GetFolderDuplicatesCore on both backends; revive tier 2 | P2 | M | Sonnet-class | 1 |
| TASK-02 | INIT-2 T2 | GetDuplicateBooksByMetadataCore on both backends; revive tier 3 ⚠ | P2 | L | Sonnet-class | 2 |
| TASK-03 | INIT-2 T3 / #1512 | Exact-layer emission-gate audit + drain parity + flag v2 ⚠ | P1 | L | Sonnet-class | 1 |
| TASK-04 | INIT-2 T4 | Status secondary index over candidates; de-magic the both_unmatched limit (ceiling kept) | P2 | L | Sonnet-class | 1 |
| TASK-05 | INIT-2 T5 / CONC-3 | Shard emit() mutex; -race proof ⚠ | P2 | M | Opus/strong-class | 2 |
| TASK-06 | INIT-2 T6 / #1512 | Prod drain: dry-run → AskUserQuestion → apply → verify | P1 | S | human + coordinator (NOT AGENT WORK) | 3 |

## Ground rules

- Go backend only; directories: `internal/database`, `internal/dedup`,
  `internal/plugins/dedup`, `internal/server/handlers/dedup`.
- Brief mode: **standalone** — each brief carries its own worktree + branch + PR +
  `gh pr merge <n> --rebase`. Conventional commits; version headers bumped on every touched
  file; never commit to main.
- Build + test gate for every task in this workstream:
  ```bash
  make ci
  ```
  Caveat (verbatim): staticcheck is red on main (pre-existing backlog #1796) — scope
  staticcheck to files you changed; the merge gate is Minimal CI green.
- Store-getter tasks (TASK-01, TASK-02) additionally run the FULL `go test ./... -short` —
  never a package subset (mocks in unexpected consumer packages fail vacuously otherwise).
- TASK-05 additionally runs `go test -race ./internal/dedup/... -short`.
- **Verify every file:line anchor with `grep` before editing** — line numbers in each brief
  are a starting point, not a guarantee. Zero hits on an edit-target grep = STOP and report.

## Collision / wave note

Collision matrix (computed from the briefs' Exact-files lists):

| Shared file | Tasks that touch it | Resolution |
|---|---|---|
| `internal/database/pebble_store.go` | TASK-01, TASK-02 | serialize: wave1=T01, wave2=T02 |
| `internal/database/memdb_reads.go` | TASK-01, TASK-02 | serialize: wave1=T01, wave2=T02 |
| `internal/database/mock_store.go` | TASK-01, TASK-02 | serialize: wave1=T01, wave2=T02 |
| `internal/dedup/engine.go` | TASK-03, TASK-05 | serialize: wave1=T03, wave2=T05 |

| Wave | Tasks | Prereq | Parallel-safe because |
|---|---|---|---|
| 1 | TASK-01, TASK-03, TASK-04 | none | disjoint file sets (see collision matrix). Execution mode: /parallel-sweep — trigger: 3 independent tasks (≥3 threshold), disjoint files, gate = `make ci`. Invocation: TASK-01,03,04. |
| 2 | TASK-02, TASK-05 | wave 1 merged + siblings rebased | T02∥T05 disjoint; each serialized behind its wave-1 collision partner. Execution mode: SERIAL WAVES (coordinator-driven) — trigger: TASK-02 shares `pebble_store.go`/`memdb_reads.go`/`mock_store.go` with TASK-01; TASK-05 shares `engine.go` with TASK-03. |
| 3 | TASK-06 | TASK-03 merged + deployed (only HARD prereq; prefer ONE `make deploy` after wave 2, but a delayed T05 must not stall this P1 drain — deploy on T03 alone and accept a second deploy) | Execution mode: NOT AGENT WORK — trigger: prod-data mutation behind a mandatory AskUserQuestion gate; 0 code files. |

**TASK-01 and TASK-02 both edit the three `internal/database` store files** — they MUST run in
different waves (TASK-02 serialized after TASK-01 merges); running them in parallel would
produce a same-file merge conflict on every rebase cycle. Same for **TASK-03 → TASK-05 on
`internal/dedup/engine.go`**. Cross-initiative: engine.go and embedding_store.go are INIT-2
property until wave 2 merges — INIT-1's engine.go touch rebases afterward. **Dispatch
precondition (enforced, not just documented):** before dispatching wave 1 or wave 2, confirm
no INIT-1 PR/worktree touching `engine.go` or `embedding_store.go` is live
(`gh pr list` + `git worktree list`); hold T03/T04/T05 until any such wave lands or is parked.

Coordinator + worker protocol: embedded verbatim in
`docs/plans/2026-07-10-dedup-pipeline-hardening.md` §Coordinator protocol (briefs are
standalone-mode; the protocol governs coordinated multi-task waves).
