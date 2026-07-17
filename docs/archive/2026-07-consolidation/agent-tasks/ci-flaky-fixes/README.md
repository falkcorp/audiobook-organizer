<!-- file: docs/agent-tasks/ci-flaky-fixes/README.md -->
<!-- version: 1.0.1 -->
<!-- guid: 16675f4f-7abf-409f-9867-764bea57be79 -->
<!-- last-edited: 2026-07-03 -->

# Workstream — CI flaky-test + mock-freshness fixes

Fix the known CI noise so the coverage/mock gates are trustworthy. From the
"Known flaky CI" TODO block (TODO.md, "🐛 Known flaky CI (pre-existing,
capture-and-fix later)").

| Task | TODO id | Title | Priority | Effort | Tier | Wave |
|------|---------|-------|----------|--------|------|------|
| **TASK-01** | **mock-freshness** | **✅ DONE (#1718, 2026-07-01)** — Resolve mockery v2/v3 pin drift; regenerate + commit scoped mocks | P1 | M | Sonnet | 1 (run alone/first) |
| TASK-02 | flaky-backup | Root-cause + fix TestBackupEndpointsErrors | P2 | M | Sonnet | 1 |
| TASK-03 | flaky-scan | Root-cause + fix TestScanService_MultiChapterAudiobook | P2 | M | Sonnet | 1 |

## Ground rules (all tasks)

- Go + tooling only (no frontend changes expected).
- Diagnose root cause; do **not** rerun-and-ignore a flaky test. Prove
  determinism with repeated runs (`go test -count=20`).
- For mockery specifically: the local `mockery` binary on developer machines
  (Homebrew) drifts from the version CI pins
  (`go install github.com/vektra/mockery/v3@v3.7.1`, see
  `.github/workflows/ci.yml`). Running the wrong version regenerates **all**
  mocks repo-wide (`interface{}` → `any` and other formatting churn) — this is
  a known footgun (see project memory: "Mockery version drift"). Always invoke
  mockery pinned to the CI version, scope the diff to what you intend to
  change, and hand-fix or discard incidental noise. Never commit an unscoped
  repo-wide regen.

## Collision / wave note

Three independent tasks — conceptually **one wave**. However **TASK-01
touches `internal/*/mocks/` broadly** (regenerating mocks) — run it **alone or
first** so its mock-file churn doesn't collide with any unrelated diffs TASK-02
or TASK-03 might incidentally produce (neither should touch mocks, but keep
them serialized after TASK-01 lands to avoid a stale-mock rebase surprise).
TASK-02 and TASK-03 touch disjoint files (`internal/server/server_extra_test.go`
+ `internal/server/handlers/system/handler.go` vs
`internal/server/scan_edge_cases_test.go` + `internal/scanner/*.go`) and can run
fully in parallel with each other.

See ORCHESTRATION.md (one level up) for the coordinator + worker protocol.
