- [ ] **TRASHED-VERSIONS-NO-ROUTE** The Trashed Versions page cannot load.
      `web/src/pages/TrashedVersions.tsx:125` sends
      `GET /api/v1/audiobooks/trashed-versions` and `/audiobooks/purged-versions`,
      but the server registers neither list route. The only matching route is
      `DELETE /purged-versions/:vid` (`internal/server/version_lifecycle.go:157`).
      Both GETs fall through to `GET /audiobooks/:id`
      (`internal/server/wire_audiobooks_routes.go:45`) with
      `id="trashed-versions"` and `id="purged-versions"`, so the page always
      shows its load error. This predates #3915, which only changed how the
      response is read; the #3915 re-review (2026-10-10) confirmed it.
      Fix: either add the two list endpoints (enveloped `{data:{versions}}`),
      or remove the page and its nav entry. Owner call.
