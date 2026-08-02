<!-- file: todo.d/2026-08-02-abs-progress-mutation-endpoints.md -->
<!-- version: 1.0.0 -->
<!-- guid: b57d2409-8e13-4c6a-9f25-30ab8e17c4d2 -->
<!-- last-edited: 2026-08-02 -->

## MISSING: ABS progress-mutation endpoints — "reset progress" and "remove from continue listening" do nothing

**Severity:** user-visible feature gap, not a regression. Reported from AudioBooth
on 2026-08-02 immediately after the client reached a fully working state (SSO login,
library browse, and playback all confirmed the same night).

Observed in production:

```
01:13:17  GET /api/me/progress/44669fab-6544-4414-ae2d-fa8eba7c52f3  → 404
```

`remove-from-continue-listening` was reported as also not working. Its call does not
appear in the log window that was checked, so it is recorded here from the spec
rather than from an observation — confirm the exact path and method against
AudioBooth before implementing.

### This is planned work, not a defect

`docs/specs/2026-07-29-abs-sync-api-design.md:839` puts all of it in **Phase 6**:

> Progress + bookmarks: adapt playback store, `/api/me`, `PATCH /api/me/progress/:id`,
> `/api/me/progress`, bookmarks CRUD (new), remove-from-continue-listening; §5 merge
> policy

Phase 6's read half shipped — `/api/me` and `POST /api/authorize` both serve the
complete `mediaProgress` list from `UserDataProvider`. The **write** half was never
built, so every client-side progress mutation 404s.

### Endpoints to add

- `PATCH /api/me/progress/:id` — update progress for one item
- `GET`/`DELETE` on `/api/me/progress/:id` — AudioBooth issued a `GET`; check whether
  reset is a `DELETE` and the `GET` is only a pre-read
- `/api/me/progress` — batch
- `…/remove-from-continue-listening`
- bookmarks CRUD

### Constraints that already apply

- **`absReservedPaths`.** `/api/me/` is already a reserved *prefix*, so these inherit
  the exclusion and will not 301 into `/api/v1`. No new reservation needed — unlike
  `/api/authorize`, which needed an exact-path entry (see PR #2100).
- **§1.8.1 still governs the read side.** Any handler that returns a user payload must
  return the COMPLETE `mediaProgress` list or a 5xx. A mutation endpoint that responds
  with a truncated user object destroys local progress exactly as `/api/me` would.
- **`…/remove-from-continue-listening` needs a non-empty body** — `{}` suffices
  (spec:318). An empty `200` is fatal to these decoders (§1.8.6).
- **§5 merge policy** applies to writes: device↔device sync is explicitly out of scope
  for the phase, but the merge rules for a single device's updates are specified.

### Not a bug, do not "fix"

`GET /api/me/listening-stats` → 404 and `GET /api/me/item/listening-sessions/:id` →
404 are **correct**. The spec prefers 404 for the stats endpoints (~12 non-optional
fields; callers use `try?`), and a half-correct body is worse than none.
