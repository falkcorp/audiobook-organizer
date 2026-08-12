## Docs / API

- [ ] **The OpenAPI spec still documents 48 endpoints that no router serves.** After the
      2026-08-12 union merge, `docs/api/openapi.json` was diffed against the **real** route
      table (obtained by calling `s.router.Routes()` on the actual router, not by grepping),
      and 48 documented operations have no matching route. They fall into three groups:

      1. **Group-relative artifacts** — the spec's generator missed Gin group prefixes, so it
         recorded `/login` instead of `/auth/login`, `/books` instead of `/itunes/books`,
         `/{id}` instead of `/ai/scans/{id}`, and so on. The correctly-prefixed paths are now
         present (they came from the YAML), so these are duplicates of real endpoints.
      2. **Removed maintenance endpoints** — 16 `POST /maintenance/*` paths. Only
         `/maintenance/wipe` still exists as a POST; the rest became registry operations
         (`maintenance.dedup-books` etc.) dispatched through the ops API.
      3. **`/torrents`** — group-relative fragment of the Deluge integration group.

      Two more (`/compare`, `/path`) were already removed in the merge because duplicate
      `operationId: "unknown"` made the spec fail validation. `/path` is the sharpest
      illustration of the whole problem: it was scraped out of a **code comment** at
      `internal/server/server.go:988`.

      This matters for the same reason as the `/auth/openid` and `/socket.io` probes: a
      client that trusts the spec and gets a 404 is worse off than one with no spec at all.

      Not removed in the merge PR because each deserves individual confirmation, and because
      a test-server route table may omit conditionally-registered routes (integrations behind
      a flag). The group-relative ones are safe to delete on sight; the maintenance ones
      should be checked against whether an ops-API equivalent should be documented instead.

      Full list:

  - `DELETE /invites/{token}`
  - `DELETE /sessions/{id}`
  - `DELETE /{id}`
  - `GET /books`
  - `GET /import-status/{id}`
  - `GET /invites`
  - `GET /library-status`
  - `GET /me`
  - `GET /sessions`
  - `GET /status`
  - `GET /torrents`
  - `GET /{id}`
  - `GET /{id}/results`
  - `POST /accept-invite`
  - `POST /import`
  - `POST /import-status/bulk`
  - `POST /invite`
  - `POST /login`
  - `POST /logout`
  - `POST /maintenance/backfill-book-files`
  - `POST /maintenance/cleanup-backups`
  - `POST /maintenance/cleanup-empty-folders`
  - `POST /maintenance/cleanup-organize-mess`
  - `POST /maintenance/cleanup-series`
  - `POST /maintenance/dedup-books`
  - `POST /maintenance/enrich-book-files`
  - `POST /maintenance/fix-author-narrator-swap`
  - `POST /maintenance/fix-book-file-paths`
  - `POST /maintenance/fix-library-states`
  - `POST /maintenance/fix-read-by-narrator`
  - `POST /maintenance/fix-version-groups`
  - `POST /maintenance/generate-itl-tests`
  - `POST /maintenance/recompute-itunes-paths`
  - `POST /maintenance/refetch-missing-authors`
  - `POST /rebuild`
  - `POST /setup`
  - `POST /sync`
  - `POST /test-connection`
  - `POST /test-mapping`
  - `POST /validate`
  - `POST /write-back`
  - `POST /write-back-all`
  - `POST /write-back/preview`
  - `POST /{id}/apply`
  - `POST /{id}/cancel`
  - `POST /{id}/deactivate`
  - `POST /{id}/reactivate`
  - `POST /{id}/reset-password`
