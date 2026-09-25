### Changed

#### Reading-progress and version-lifecycle routes moved under `/audiobooks`

`/books/:id/position`, `/books/:id/state`, `/books/:id/status`,
`/books/:id/status/repair`, and the version trash/restore/purge routes
(`/books/:id/versions/:vid`, `.../restore`, `.../purge-now`) used a different
top-level noun than every other per-book route in the API. They now live
under `/audiobooks/:id/...`, matching the other 60+ per-book routes. The old
`/books/:id/...` paths remain registered as deprecated aliases to the same
handlers, so existing callers keep working. The frontend (`readingApi.ts`,
`versionApi.ts`) now calls the new canonical paths. Part of the naming
consistency audit in `docs/audits/2026-09-25-interface-naming-consistency.md`
(class 1).
