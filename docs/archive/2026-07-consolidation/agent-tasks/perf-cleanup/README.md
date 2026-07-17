&lt;!-- file: docs/agent-tasks/perf-cleanup/README.md --&gt;
&lt;!-- version: 1.0.0 --&gt;
&lt;!-- guid: 9feb30a9-2a8a-4ebf-a5f4-2297d76fb165 --&gt;
&lt;!-- last-edited: 2026-07-01 --&gt;

# Workstream — Perf cleanup + small backend debt

Low-priority backend cleanups: a `RunItems` migration, two caching fast-paths,
a benign goroutine-leak tidy, and retiring a config compat shim. Sourced from
ARCH-4b, MAYDEPLOY-H5, MAYDEPLOY-H7, NUTSDB-CLOSE, and CONS-13/CFG-2-D in
`TODO.md`.

## Tasks

| Task | TODO id | Title | Priority | Effort | Tier | Wave |
|------|---------|-------|----------|--------|------|------|
| TASK-01 | ARCH-4b | Migrate `acoustid/reset_all.go` to `registry.RunItems` | P3 | M | Sonnet | 1 |
| TASK-02 | MAYDEPLOY-H5 | Per-book `GetAuthorByID` fast path when `len(bookIDs)<100` | P3 | S | Sonnet | 1 |
| TASK-03 | MAYDEPLOY-H7 | TTL-cache `isProtectedPath` / `GetAllImportPaths` at hot sites | P3 | S | Sonnet | 1 |
| TASK-04 | NUTSDB-CLOSE | Investigate/document `NutsActivityStore.Close()` goroutine note (benign, optional) | P4 | XS | Haiku | 1 |
| TASK-05 | CONS-13/CFG-2-D | Retire flat-to-nested config compat shim (**GATED**) | P3 | S | Sonnet | 1 |

## Ground rules

Go. Build+test the changed package only. Verify file:line anchors with `grep`
before editing — every anchor in these briefs was checked against the repo at
authoring time but line numbers drift as other PRs land.

## Collision / wave note

All five tasks touch **different files** — this is a single wave, dispatched
up to 5-wide in parallel:

- TASK-01 → `internal/plugins/acoustid/reset_all.go`
- TASK-02 → `internal/server/metadata_ops.go`
- TASK-03 → `internal/server/server_middleware.go` + `internal/audiobooks/helpers.go`
- TASK-04 → `internal/database/nuts_activity_store.go`
- TASK-05 → `internal/config/update_service.go` (**GATED** — do not dispatch
  until the nested config has ~1 week of confirmed prod stability; see the
  banner at the top of that task brief)

See ORCHESTRATION.md (one level up) for the coordinator + worker protocol.
