<!-- file: docs/architecture.md -->
<!-- version: 1.1.1 -->
<!-- guid: 1a9b8c7d-6e5f-4a3b-92c1-d0e9f8a7b6c5 -->
<!-- last-edited: 2026-10-09 -->

# Architecture

> **See also:** the deeper, per-subsystem system-documentation set lives in
> [`docs/system/`](system/README.md) — architecture, pipelines, storage, API,
> runbooks, components, incidents, and deploy/GPU ops, with process/data-flow diagrams.

## Overview

Audiobook Organizer is a single-binary Go application with an embedded React frontend.

- Backend: Go HTTP API using Gin
- Frontend: React + TypeScript + Material UI
- Data: Pebble (default) or SQLite for the main store (`database_type`); memdb in-memory query layer; activity log on its own backend (`activity_backend`: Pebble by default, SQLite legacy opt-in until P75)
- Realtime: SSE event stream (`/api/events`)
- Background execution: Operations v2 registry (`internal/operations/registry`) — every long-running job is an `OperationDef` registered by a plugin and launched/polled via `/api/v1/operations/v2`

## Runtime Components

- `cmd/root.go`: CLI entrypoint and server wiring
- `internal/server`: API routes, middleware, handlers
- `internal/database`: Store abstraction + Pebble/SQLite implementations
- `internal/scanner`: File discovery and metadata extraction
- `internal/organizer`: File placement and organization strategy execution
- `internal/operations`: Operations v2 registry (`registry/`), def_ids, run state, resume/retry
- `internal/plugin` + `internal/plugins/*`: plugin SDK and the built-in plugins (`acoustid`, `dedup`, `deluge`, `itunes`, `maintenance`, `metafetch`, `webhook`) that register the `OperationDef`s
- `internal/repairs`: the Repairs lane framework — one `Fixer` contract and one engine that plans, pages and applies every library fixer with the same guards
- `internal/server/handlers/*`: per-domain HTTP handler packages (`abs`, `review`, `repairs`, …)
- `web/src`: UI routes, API client, state, and views; `/review` is the unified review workspace

## API Flow

1. UI issues request to `/api/v1/*`
2. Middleware applies:
   - rate limit (auth + general)
   - body size limits
   - auth guard (if enabled and users exist)
3. Handler calls service/store layer
4. Operation and system updates publish over SSE as needed

## Authentication Flow

1. UI checks `/api/v1/auth/status`
2. First run (`bootstrap_ready=true`) allows `POST /api/v1/auth/setup`
3. Login via `POST /api/v1/auth/login`
4. Server creates session and sets httpOnly cookie `session_id`
5. Protected routes resolve session and user from middleware

## Data Model Notes

- Core entities: books, authors, series, works, import paths, operations
- Auth entities: users, sessions
- Metadata provenance: per-field state with fetched/stored/override values
- Lifecycle fields support soft-delete, purge, and wanted state workflows

## Background Work Model

- Every job is an Operations v2 `OperationDef` (e.g. `library.scan`, `maintenance.window`, `repairs.plan`); plugins register defs at startup and `POST /api/v1/operations/v2 {"def_id": …}` launches one
- Runs transition through queued/running/completed/failed/canceled; state is persisted under `opv2:*` keys so a run survives a restart, can be resumed, retried (`/retry`) or discarded (`/record`)
- A renamed def keeps its old ID as an alias (`OperationDef.FormerIDs`)
- `OperationDef.Permissions` is enforced on the v2 trigger route (PR #2536)
- Stale operations are detected by age and marked failed; the operation timeout is configurable and enforced per execution context

## Review Page and Repairs Lane

`/review` in the UI is one workspace with four lanes: **Review queue** (the
producer-agnostic review queue — items a producer such as the regroup op flagged
for a human decision; `GET/POST /api/v1/review/*`), **Duplicates** (dedup
candidates), **Metadata** (candidate apply) and **Repairs**.

The Repairs lane is built on `internal/repairs`. A fixer decides what a row is
and how one row is written; the engine owns the rest: a plan runs as the
`repairs.plan` operation and stores every row in the op result, the rows page
over `GET /api/v1/repairs/:fixer/plan/:op_id/rows`, and `repairs.apply` takes
explicit row ids, re-plans each one and refuses a row whose fingerprint changed.
Rows under `books/itunes/**` or matching Doctor Who / Big Finish / Torchwood are
skipped at plan time and refused again at apply time; writes go through a
`Writer` that records a metadata-history row per changed field and has no
delete primitive; apply holds the library-scan stand-down. Fixers registered in
production (`internal/plugins/maintenance/*_fixer.go`): `duplicate-copies`,
`folder-books`, `fragment-consolidation`, `maintenance.normalize-letter-l-ordinals`,
`maintenance.repair-junk-authors`, `maintenance.repair-junk-titles`,
`version-group-primary-repair`. Routes: `GET /repairs`, `POST /repairs/:fixer/plan`,
`GET /repairs/:fixer/plan/:op_id/rows`, `POST /repairs/:fixer/apply`
(`internal/server/wire_repairs_routes.go`).

## Deployment Model

- Local binary (`audiobook-organizer serve ...`)
- Docker image with embedded web assets
- Optional service definitions:
  - systemd unit: `deploy/audiobook-organizer.service`
  - launchd plist: `deploy/com.audiobook-organizer.plist`
