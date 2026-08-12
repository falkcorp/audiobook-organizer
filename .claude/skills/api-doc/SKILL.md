---
name: api-doc
description: Update docs/api/openapi.json when adding or modifying API endpoints
disable-model-invocation: true
---

# Update API Documentation

Keep `docs/api/openapi.json` (OpenAPI 3.0.3) in sync when adding or modifying API endpoints.

> **This skill used to target `docs/openapi.yaml`.** That file was a second,
> independently hand-maintained spec; the two drifted until each held endpoints the
> other lacked. They were union-merged into `docs/api/openapi.json` on 2026-08-12 and
> the YAML was archived. **There is exactly one spec now — keep it that way.** If you
> find yourself creating a second spec file, don't.

## Arguments

- First argument: description of the API change (e.g., "added GET /api/v1/works endpoint")

## Steps

1. **Read the current OpenAPI spec:**
   ```
   Read docs/api/openapi.json
   ```

2. **Identify the endpoint in Go source.** Routes are registered in the
   `internal/server/wire_*_routes.go` files (and some in `server_lifecycle.go`) using
   Gin, usually on a **group**:
   ```go
   itunesG := protected.Group("/itunes")
   itunesG.POST("/sync", s.perm(auth.PermLibraryEditMetadata), itunesH.Sync)
   ```

   ⚠️ **Write the full path, not the group-relative fragment.** The example above is
   `/itunes/sync`, *not* `/sync`. The previous spec was built by something that missed
   group prefixes, leaving ~20 bogus root-level paths (`/login`, `/books`, `/compare`)
   that had to be untangled by hand.

   If you are unsure of a full path, ask the router instead of guessing — a temporary
   test calling `s.router.Routes()` prints every registered method and path.

3. **Read the handler function** to understand request/response types.

4. **Update `docs/api/openapi.json`** with:
   - Path definition under `paths:`, keyed **without** the `/api/v1` prefix —
     `servers[0].url` is already `/api/v1`, so a path of `/api/v1/foo` would resolve to
     `/api/v1/api/v1/foo`.
   - Request body schema (if POST/PUT/PATCH)
   - Response schemas (200, 400, 404, 500)
   - Proper tag assignment (match existing tags)
   - Parameter definitions. **Every `{name}` in the path needs a declaration** with
     `in: path` and `required: true`, or the spec fails validation. Declare them once at
     the path-item level so all operations on that path share them.

5. **Bump `info.version`.**

6. **Validate before you finish** — the spec is valid OpenAPI 3.0.3 today and should
   stay that way:
   ```bash
   python3 -m pip install --user openapi-spec-validator
   python3 -c "import json;from openapi_spec_validator import validate;validate(json.load(open('docs/api/openapi.json')))"
   ```

## Template for a new endpoint

```json
  "/endpoint/{id}": {
    "parameters": [
      {
        "name": "id",
        "in": "path",
        "required": true,
        "description": "Unique identifier.",
        "schema": { "type": "string" }
      }
    ],
    "get": {
      "tags": ["TagName"],
      "summary": "Short description",
      "description": "Longer description",
      "operationId": "operationName",
      "responses": {
        "200": {
          "description": "Success",
          "content": {
            "application/json": {
              "schema": {
                "type": "object",
                "properties": { "field": { "type": "string" } }
              }
            }
          }
        },
        "404": { "description": "Not found" }
      },
      "security": [{ "bearerAuth": [] }]
    }
  }
```

## Rules

- Follow OpenAPI 3.0.3.
- **Document only endpoints that actually exist.** Do not add a path speculatively or
  leave one behind after deleting a route. The spec previously advertised
  `/audiobooks/search` and 16 `/maintenance/*` endpoints that no router serves — a
  client that trusts the spec and gets a 404 is worse off than one with no spec.
- Use existing `$ref` components where possible (parameters, schemas).
- All endpoints require `security: [{"bearerAuth": []}]` except Health and Events.
- Use existing tags — only add new tags if truly a new domain.
- Keep response schemas consistent with the actual Go struct JSON output.
- `operationId` must be unique across the whole document.
