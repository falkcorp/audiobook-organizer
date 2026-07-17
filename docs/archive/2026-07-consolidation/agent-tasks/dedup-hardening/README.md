<!-- file: docs/agent-tasks/dedup-hardening/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: 90c14f61-0651-48a6-9377-9adad3e37b25 -->
<!-- last-edited: 2026-07-01 -->

# Workstream — Dedup hardening

Backend dedup-correctness tasks: close the residual exact-layer false-positive
leak and add defensive guards. From DEDUP-INTRO-1 residual, CONS-15, and
CONS-FRAG-2.

| Task | TODO id | Title | Priority | Effort | Tier | Wave |
|------|---------|-------|----------|--------|------|------|
| TASK-01 | dedup-residual | Boilerplate-title + min-duration guard at `upsertExactCandidate` chokepoint | P1 | M | Sonnet | 1 |
| TASK-02 | CONS-15 | Part-vs-whole defense-in-depth guard in the exact emitter | P2 | S | Sonnet | 2 |
| TASK-03 | CONS-FRAG-2 | Route `BookFiles>1` books to `OrganizeBookDirectory` in `organizeOneBook` | P2 | S | Sonnet | 1 |

## Ground rules

- Go code under `internal/`.
- Build + test gate for every task in this workstream:
  ```bash
  go build ./... && go test ./internal/dedup/... ./internal/itunes/... -count=1
  ```
- **Verify every file:line anchor with `grep` before editing** — the codebase
  moves fast and the line numbers in each task brief are a starting point, not
  a guarantee.

## Collision / wave note

**WS1/T01 and WS1/T02 both edit `internal/dedup/engine.go`.** They MUST run in
different waves (T01 in wave 1, T02 in wave 2, serialized after T01 merges) —
running them in parallel would produce a same-file merge conflict on every
rebase cycle. **WS1/T03** edits `internal/itunes/service/importer.go` and
`internal/organizer/organizer.go`, which neither T01 nor T02 touch, so it is
independent and runs in wave 1 alongside T01.

See [ORCHESTRATION.md](../ORCHESTRATION.md) (one level up) for the coordinator
+ worker protocol.
