---
name: api-doc
description: Keep the API contract (Go DTO structs and generated TypeScript types) in sync when adding or modifying API endpoints
disable-model-invocation: true
---
<!-- file: .claude/skills/api-doc/SKILL.md -->
<!-- version: 2.0.0 -->
<!-- guid: 5b0e4d1c-8a37-4c52-9f6e-2d7a1c3b9e48 -->
<!-- last-edited: 2026-10-19 -->

# Update the API Contract

There is no OpenAPI file. `docs/api/openapi.json` was hand-maintained, drifted, and was
deleted (owner decision D45). **Do not create a replacement spec.** The contract is:

1. the Go DTO structs the handlers marshal, and
2. the TypeScript types generated from them into `web/src/types/generated/` (tygo, config in
   `tygo.yaml`).

## Arguments

- First argument: description of the API change (e.g., "added GET /api/v1/works endpoint")

## Steps

1. **Find the route.** Routes are registered in the `internal/server/wire_*_routes.go` files
   (and some in `server_lifecycle.go`), usually on a group. Write the full path, not the
   group-relative fragment: `itunesG.POST("/sync", ...)` on `protected.Group("/itunes")` is
   `/itunes/sync`.
2. **Change the DTO struct** the handler marshals or binds, with accurate `json` tags. Pointer
   or `omitempty` fields must match what the handler really sends.
3. **Regenerate the types:** `make generate-types`. Commit the changed files under
   `web/src/types/generated/`. CI runs `make types-check` and fails if they are stale.
4. **Update the contract goldens** if a covered type changed:
   `UPDATE_WIRE_GOLDEN=1 go test ./internal/wirecontract/`, then `make generate-types` again.
5. **Update the handler test** so it asserts the new request and response shapes.
6. **Run** `make types-check` and the affected Go and `web/` tests.

## Rules

- Document only what exists: remove DTO fields and routes together with their handlers.
- Keep response structs consistent with the JSON the handler actually writes.
- Never hand-edit files in `web/src/types/generated/`.
