<!-- file: .github/prompts/abs-implementation-completion.prompt.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6b41f0a8-93de-4c57-8e12-a0d75c3fb914 -->
<!-- last-edited: 2026-08-13 -->

# Prompt: finish the AudiobookShelf server implementation

Paste the section below into a fresh session. Everything after the horizontal rule is the
prompt; the preamble here is for whoever is deciding whether to run it.

**Why this prompt is shaped the way it is.** The obvious framing — "implement collections,
playlists, read status, position and bookmarks" — is wrong in a way that wastes a session:
**three of those five are already implemented.** Bookmarks have full CRUD, position and read
status live in `progress.go`. A session told to "implement bookmarks" will either rebuild
them or spend an hour discovering it shouldn't. So Phase 0 is a mandatory ground-truth pass
that produces a status document, and the build phases are scoped from *that*, not from
anyone's memory.

The second reason: `docs/audits/2026-08-11-abs-coverage-gap-audit.md` is the closest thing
we have to a status doc and it is **stale in at least four places** — N-1, N-2, N-3 and N-4
all changed on 2026-08-12, one of them retracted outright. Acting on it as written would
reintroduce a bug that broke 46 live routes twice.

---

## PROMPT

You are finishing the AudiobookShelf-compatible API in this repo. The protocol is now fully
mapped; the work is to close the gap between what is mapped and what is implemented.

### Phase 0 — establish ground truth, and write it down (do this first, do not skip)

Do not trust this prompt's summary of what exists, and do not trust the existing audit.
Derive the current state yourself, then write
`docs/reference/abs-implementation-status.md` recording it. That document is the
deliverable of Phase 0 and the input to every later phase.

Sources, in order of authority:

1. **`router.Routes()`** — the only complete list of what we serve. `absRouteList()` in
   `internal/server/wire_abs_routes.go` is a hand-maintained list and has been wrong before.
   Grep **cannot** see these paths: gin composes them from group prefixes, so `/api/v1/users`
   appears as a literal in no file. Derive from the router or you will under-count.
2. **`testdata/abs-fixtures/`** (28 JSON captures) — what a real client actually asked the
   real server, and what it answered. `request.path` is the client's demand; the reply is the
   contract.
3. **`docs/reference/abs-upstream-api-reference.md`** — the unified upstream surface. It
   documents ~48 entries that do not exist on our side; that is known and recorded, not a
   discovery.
4. **`docs/reference/abs-target-client-contract.md`** — what the target client requires,
   including §6.1 (paginated-envelope trap), §6.6 (`Page<T>` needs non-optional `total` and
   `page` or Dart throws) and §11 (which surfaces are safe to stub).
5. **`docs/audits/2026-08-11-abs-coverage-gap-audit.md`** — useful for structure, **stale on
   N-1/N-2/N-3/N-4**. N-3 is RETRACTED; do not act on it. Reconcile it and say in your status
   doc which items you re-verified and which you found already closed.

The status doc must classify **every** ABS route into exactly one of: `implemented +
conformance-asserted`, `implemented, not asserted`, `stub` (answers, but with empty or
fabricated data), `404 by design`, `301 into the app API`, `absent`. A route counted as
"implemented" on the strength of a handler existing is not good enough — say whether a test
asserts its **values**.

### Phase 1 — the two real gaps

**Collections** and **playlists** are today both `h.EmptyPage` at
`internal/server/handlers/abs/handler.go:386-387`. They are *not* the same size of job, and
scoping them as one bucket is the main way this goes wrong:

- **Playlists already exist in the domain.** `internal/database` has `Playlist`,
  `PlaylistItem`, `CreatePlaylist(name, seriesID, filePath)`, `GetPlaylistByID`,
  `GetPlaylistBySeriesID`, `GetPlaylistItems`, the app API serves full CRUD at
  `/api/v1/playlists`, and there is an iTunes playlist importer. This is a **mapping** job:
  translate the existing model into ABS's shape. Do not create a second playlist model.
  Note the collision — `/api/playlists` currently **301s** into the app-API twin, which the
  ABS namespace work deliberately left in place; changing that is a routing decision, not a
  side effect.
- **Collections do not exist anywhere.** No domain model, no store, no routes. This is a new
  entity end to end: storage, CRUD, ownership, and whatever ordering ABS expects. Cost it
  honestly and say so before building.

Upstream has ~10 collection routes and ~12 playlist routes. Implement the ones the target
client actually calls first — check the fixtures for evidence, and if a route appears in
**no** capture, say so explicitly rather than implying it was requested.

### Phase 2 — verify what is already built rather than rebuilding it

These exist. Your job is to confirm they are **correct and asserted**, then fix what isn't:

| area | where | status to verify |
|---|---|---|
| Bookmarks | `handlers/abs/bookmarks.go`, routes from `handler.go:478` | full CRUD; gated on a non-nil bookmark store — check what happens when it is nil |
| Current position | `handlers/abs/progress.go` | `currentTime`, single + batch update, delete |
| Read status | `progress.go` — `IsFinished`, merge semantics, `UserBookStatusFinished` | the merge path at ~L292–L343 is subtle; check the both-nil guard |
| Sessions (singular) | `play.go`, `/api/session/:id/sync`, `/close` | note upstream also has **plural** `/api/sessions*` history, which we do not serve |

If one of these is already right, say "already correct, verified by X" and move on. Do not
manufacture work to look busy.

### Phase 3 — decide, don't assume, on the deferred surface

Recorded as deliberately-not-done: podcasts, HLS, item writes (`PATCH /items/:id/media`,
covers, chapters, match), series/author detail, users/admin, and Socket.IO. Socket.IO is the
notable one — **`cancel_scan` has no REST equivalent in any upstream route**, so without a
socket a running scan cannot be cancelled at all. Raise these as decisions with costs; do not
silently build them and do not silently skip them.

### Hard constraints

- **Worktree + PR for everything.** Never edit or commit on `main`. One PR per coherent unit;
  do not let a branch accumulate three unrelated features.
- **`abs_api_enabled` defaults to FALSE and the server fails closed without it.** Any test
  that exercises new routes must turn it on explicitly, and you must state whether shipping
  changes the default (it should not, without an owner decision).
- **Do not touch engine-level routing to fix a namespace problem.** That has been attempted
  twice and broke **46 live app routes** both times. The guard is now derived from
  `router.Routes()` and from fixture `request.path`; keep it that way.
- **`Page<T>` needs non-optional `total` and `page`.** Returning `{}` or omitting either
  field red-screens the Dart client. This is why the stubs return an empty *page* rather than
  an empty object — see `dto_library.go:360` and contract §6.6. Any new list endpoint inherits
  this rule.
- **Conformance is value-checking, not shape-checking.** Use `assertConformant` /
  `assertConformantExcept` / `assertNonJSONConformant` with `CompareValues: true`. A
  divergence you cannot avoid goes in a **bounded** allowance stating the widest gap its cause
  can produce — never a blanket "may differ". An allowance that never fires fails the test,
  deliberately.
- **Verify every test by watching it go red.** Break the thing it guards, confirm the failure
  names the right cause, restore. A green test you have never seen fail is not evidence.
- **Fragments:** add a `changelog.d/` entry per PR; new tasks go in `todo.d/`. Both are
  **headerless** — no `file`/`version`/`guid`/`last-edited`. Every other file needs the header
  and a version bump.

### Traps this codebase has actually sprung

- Grep cannot see gin's composed routes. Derive from the router.
- A fixture bounds your coverage: the sessions capture holds 3 items against a page size of
  10, so pagination clamps were unreachable and shipped untested. Check what N your fixture
  contains before believing a green run.
- One row of a diff is one sample. A rule inferred from a single line was wrong; the other
  five lines said the opposite.
- Re-running a failed check replays the **original** event payload. Editing a PR title to add
  `[skip changelog]` does not re-fire `changelog-check.yml` — it has no `edited` trigger. Add
  the `skip-changelog` label instead.
- Before staging, re-run `git status --porcelain` and compare against what you reviewed. Other
  sessions edit sibling worktrees; `git add -A` on a stale survey has swept in unread files.

### Definition of done

1. `docs/reference/abs-implementation-status.md` exists, classifies every route, and states
   which claims were re-verified versus carried over.
2. Collections and playlists answer with real data for every route the target client calls,
   each covered by a value-asserting test.
3. Phase 2 areas are confirmed correct or fixed, with the evidence named.
4. Phase 3 items are written up as decisions with costs, not quietly dropped.
5. Report counts, never adjectives: `COMPLETED: <n> — <list>`, `REMAINING: <n> — <list>`,
   `BLOCKED: <n> — <list>`. "All done" without a number backing it is not a status.
