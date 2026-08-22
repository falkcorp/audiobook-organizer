### Changed

#### `setupMockApi` dispatcher audited: none of the 10 remaining `startsWith()` catch-alls shadow a more-specific branch

Audited every one of the 10 `pathname.startsWith(...)` prefix catch-alls in the
E2E mock dispatcher (`web/tests/e2e/utils/test-helpers.ts`) for the hazard that
was fixed earlier at `/api/v1/audiobooks/batch`: a specific branch placed *below*
a prefix catch-all that also matches it, making the specific branch unreachable
so the request silently returns the generic response instead. The audit covered
both branch forms in the dispatcher — all 67 `pathname === '...'` exact matches
and all 24 `pathname.match(/.../)` regex branches — and found **zero** shadowed
branches, so no ordering change was needed and no runtime behaviour changed. Two
latent (not currently shadowing) observations are recorded for follow-up: the
`/api/v1/works` catch-all has no trailing slash, so it would also swallow a
future `/api/v1/workspaces...` path, and the `/api/v1/backup/list` branch above
it carries no HTTP-method guard.
