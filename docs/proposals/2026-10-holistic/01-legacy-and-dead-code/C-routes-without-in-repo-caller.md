<!-- file: docs/proposals/2026-10-holistic/01-legacy-and-dead-code/C-routes-without-in-repo-caller.md -->
<!-- version: 1.2.0 -->
<!-- guid: cf3c3044-cbe2-474c-a6b1-851a99f8650f -->
<!-- last-edited: 2026-10-09 -->

# Appendix C: API routes with no in-repo caller

**Round-2 (r1, 2026-10-09), applying D9.** The owner marks the routes their scripts use; the rest are retired per handler group with a 410 stub for one release (01 P81). Three things to know before marking:

- **`POST /api/v1/maintenance/wipe` is retired regardless** (D9). It is the first P81 PR: `server_lifecycle.go:1589`, `maintenance_fixups.go` (`handleWipe`, `prefixWiper`), `maintenance_wipe_prefixes_test.go`, `credential_routes_test.go:58`.
- **The 410 pattern already exists**: `server_lifecycle.go:1495-1501` answers `/operations/active` and `/operations/recent` with `http.StatusGone` and a `message` naming the replacement (UOS-14). P81 lifts it into one helper rather than adding a second shape.
- **Routes tied to other decisions**: `GET /maintenance/repair-missing-files/:id` belongs to the `maintenance.repair-missing-files` job that 04 D27 retires in favour of `missing-file-repoint` / `recover-missing-files`, so it goes with that 04 PR, not here. `GET /series/normalize/preview` goes with 01 P72. `GET /books/:id/position` and `POST /books/:id/position` are the ABS-style reading-position pair; AudioBooth uses the root-mounted `/api/...` ABS group, not `/api/v1` (03 F19), so they are only a curl risk, but the owner should confirm no Mac-side script syncs positions through them before marking.

Method: the Gin route table was dumped from a test server built with `setupCredGuardServer` (a scratch copy; no repo edit): 467 routes. Each `/api/v1` path was turned into a regex (params and `${...}` interpolations as wildcards, per-file `API_BASE` resolved) and searched in non-test `web/src`, then in `cmd/`, `scripts/`, `.claude/`. 356 matched the frontend, 12 matched only scripts/skills, 93 matched nothing.

This is a **floor and a triage list, not a dead list**: routes behind config/build tags are missing from the dump, and AudioBooth (Swift, external) and ad-hoc curl are invisible. Confidence: medium per route.

## Excluded from deletion (known external or tooling callers): 13

- `GET /api/v1/admin/debug/book-files/:id`
- `GET /api/v1/admin/debug/books/:id`
- `GET /api/v1/admin/debug/edits`
- `GET /api/v1/admin/debug/lookup`
- `GET /api/v1/auth/oauth/:provider/callback`
- `GET /api/v1/fingerprint/worker/hello`
- `PATCH /api/v1/admin/debug/book-files/:id`
- `PATCH /api/v1/admin/debug/books/:id`
- `POST /api/v1/admin/debug/edits/:edit_id/undo`
- `POST /api/v1/auth/accept-invite`
- `POST /api/v1/fingerprint/worker/lease/:id/release`
- `POST /api/v1/fingerprint/worker/lease/:id/renew`
- `POST /api/v1/fingerprint/worker/results`

## Taken from 03 §6, deleted by P72 (gated on Q6): 8

- `POST /api/v1/dedup/candidates/:id/dismiss`
- `POST /api/v1/dedup/candidates/:id/merge`
- `POST /api/v1/dedup/emb-reencode`
- `POST /api/v1/dedup/embed-async`
- `POST /api/v1/dedup/lsh-index`
- `POST /api/v1/dedup/purge-acoustid-conflicts`
- `POST /api/v1/dedup/purge-legacy-fp`
- `POST /api/v1/dedup/scan-book-signature`

P72 also removes the verb aliases and non-`/dedup` routes 03 listed (`/audiobooks/merge`, `/series/normalize*`, `/authors/duplicates/ai-review*`), some of which matched a frontend string in an unused `api.ts` wrapper and so are not in the 93.

## For owner triage (Q6): 72

- `DELETE /api/v1/audiobooks/:id/alternative-titles`
- `DELETE /api/v1/books/:id/status`
- `DELETE /api/v1/books/:id/versions/:vid`
- `DELETE /api/v1/collections/:id`
- `DELETE /api/v1/metadata/providers/throttles`
- `DELETE /api/v1/metadata/providers/throttles/:id`
- `GET /api/v1/ai/capabilities`
- `GET /api/v1/ai/endpoints/status`
- `GET /api/v1/audiobooks/:id/alternative-titles`
- `GET /api/v1/audiobooks/:id/cover-history`
- `GET /api/v1/audiobooks/:id/narrators`
- `GET /api/v1/audiobooks/:id/path-history`
- `GET /api/v1/audiobooks/:id/similar`
- `GET /api/v1/authors/:id/tags`
- `GET /api/v1/books/:id/position`
- `GET /api/v1/books/:id/state`
- `GET /api/v1/cache/stats/history`
- `GET /api/v1/cache/stats/keys`
- `GET /api/v1/catalog/entries`
- `GET /api/v1/catalog/entries/:id`
- `GET /api/v1/collections/:id`
- `GET /api/v1/deluge/discover`
- `GET /api/v1/deluge/labels`
- `GET /api/v1/diagnostics/db-census`
- `GET /api/v1/diagnostics/fingerprint-failures`
- `GET /api/v1/itunes/library-stats`
- `GET /api/v1/itunes/pid-integrity`
- `GET /api/v1/maintenance/repair-missing-files/:id`
- `GET /api/v1/maintenance/scan-composer-tags/:id`
- `GET /api/v1/merge/combine-journal`
- `GET /api/v1/merge/sibling-journal`
- `GET /api/v1/metadata/bulk-apply-preview/:id`
- `GET /api/v1/metadata/fields`
- `GET /api/v1/metadata/providers/throttles`
- `GET /api/v1/narrators/count`
- `GET /api/v1/op-defs/:id`
- `GET /api/v1/playlists/:id/export.m3u`
- `GET /api/v1/policy/tags`
- `GET /api/v1/series/:id/tags`
- `GET /api/v1/series/normalize/preview`
- `GET /api/v1/signals/coverage`
- `GET /api/v1/system/activity-log`
- `GET /api/v1/tools/:name/status`
- `GET /api/v1/work/stats`
- `PATCH /api/v1/books/:id/status`
- `POST /api/v1/activity/clamp-summaries`
- `POST /api/v1/admin/recompact-digests`
- `POST /api/v1/audiobooks/:id/alternative-titles`
- `POST /api/v1/audiobooks/:id/cover-history/restore`
- `POST /api/v1/audiobooks/:id/rescan`
- `POST /api/v1/audiobooks/:id/status/repair`
- `POST /api/v1/audiobooks/duplicates/dismiss`
- `POST /api/v1/audiobooks/duplicates/merge`
- `POST /api/v1/auth/api-keys/:id/rotate`
- `POST /api/v1/authors/:id/tags`
- `POST /api/v1/books/:id/position`
- `POST /api/v1/books/:id/status/repair`
- `POST /api/v1/books/:id/versions/:vid/purge-now`
- `POST /api/v1/books/:id/versions/:vid/restore`
- `POST /api/v1/cache/invalidate`
- `POST /api/v1/collections/:id/materialize`
- `POST /api/v1/deluge/discover/import`
- `POST /api/v1/import/collision-preview`
- `POST /api/v1/itunes/pid-repair`
- `POST /api/v1/maintenance/wipe`
- `POST /api/v1/merge/sibling-undo/:journal_id`
- `POST /api/v1/merge/undo/:journal_id`
- `POST /api/v1/metadata/bulk-apply-preview`
- `POST /api/v1/review/replay-approved`
- `POST /api/v1/series/:id/tags`
- `PUT /api/v1/audiobooks/:id/narrators`
- `PUT /api/v1/collections/:id`

## Referenced only by scripts or skills (keep unless the script is retired)

- `DELETE /api/v1/works/:id  <- scripts/test-api-endpoints.py`
- `GET /api/v1/maintenance/transcribe-stats  <- scripts/transcribe_monitor.py`
- `GET /api/v1/metadata/export  <- scripts/TEST-README.md`
- `GET /api/v1/works/:id  <- scripts/test-api-endpoints.py`
- `GET /api/v1/works/:id/books  <- scripts/test-api-endpoints.py`
- `POST /api/v1/auth/bootstrap  <- .claude/skills/server-bootstrap/SKILL.md`
- `POST /api/v1/fingerprint/worker/lease  <- cmd/fp_worker_test.go`
- `POST /api/v1/metadata/batch-update  <- scripts/TEST-README.md`
- `POST /api/v1/metadata/import  <- scripts/TEST-README.md`
- `POST /api/v1/metadata/validate  <- scripts/TEST-README.md`
- `POST /api/v1/system/reset  <- scripts/record_demo.js`
- `PUT /api/v1/works/:id  <- scripts/test-api-endpoints.py`
