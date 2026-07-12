<!-- file: docs/architecture.md -->
<!-- version: 1.0.1 -->
<!-- guid: 1a9b8c7d-6e5f-4a3b-92c1-d0e9f8a7b6c5 -->
<!-- last-edited: 2026-07-11 -->

# Architecture

> **See also:** the deeper, per-subsystem system-documentation set lives in
> [`docs/system/`](system/README.md) — architecture, pipelines, storage, API,
> runbooks, components, incidents, and deploy/GPU ops, with process/data-flow diagrams.

## Overview

Audiobook Organizer is a single-binary Go application with an embedded React frontend.

- Backend: Go HTTP API using Gin
- Frontend: React + TypeScript + Material UI
- Data: Pebble (default) or SQLite
- Realtime: SSE event stream (`/api/events`)
- Background execution: Priority operation queue

## Runtime Components

- `cmd/root.go`: CLI entrypoint and server wiring
- `internal/server`: API routes, middleware, handlers
- `internal/database`: Store abstraction + Pebble/SQLite implementations
- `internal/scanner`: File discovery and metadata extraction
- `internal/organizer`: File placement and organization strategy execution
- `internal/operations`: Queue orchestration and operation lifecycle
- `web/src`: UI routes, API client, state, and views

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

- Queue supports prioritized jobs (`scan`, `organize`, etc.)
- Jobs transition through queued/running/completed/failed/canceled
- Stale operations are detected by age and marked failed
- Operation timeout is configurable and enforced per execution context

## Deployment Model

- Local binary (`audiobook-organizer serve ...`)
- Docker image with embedded web assets
- Optional service definitions:
  - systemd unit: `deploy/audiobook-organizer.service`
  - launchd plist: `deploy/com.audiobook-organizer.plist`
