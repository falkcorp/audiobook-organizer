<!-- file: docs/HANDOFF-2026-05-13-night.md -->
<!-- version: 1.0.0 -->

# Audiobook Organizer — Night-of-2026-05-13 Handoff

The user (jdfalk) went to bed asking the next session to keep shipping the
TODO list autonomously. This file captures exactly what was finished tonight,
what's wired up but unshipped, and the specific next-step instructions —
detailed enough that a Haiku-tier agent can pick up without asking questions.

**Working tree:** `/Users/jdfalk/.worktrees/audiobook-organizer-next/`
on branch `fix/server-tests-followup`. **Main checkout:**
`/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer/` (has the
`Makefile.local` with `make deploy`). Deploy from main checkout (the worktree's
musl-cross build env is missing `-lz`).

**Deployed prod:** https://<your-server-ip>:8484/ — verify after every deploy with
`curl -skIo /dev/null -w "%{http_code}\n" https://<your-server-ip>:8484/` (expect 200).

## Operating rules — read first

1. **Worktree per PR.** Every new task: `git worktree add /Users/jdfalk/.worktrees/audiobook-organizer-<slug> -b <branch> origin/main`. Don't commit on main.
2. **Workflow per PR**: write code → `go build ./...` → commit (conventional + Claude co-author) → `git push -u origin <branch>` → `gh pr create` → `gh pr merge --rebase --admin` → `git fetch && git checkout main && git pull --ff-only` → `cd /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer && make deploy` → verify HTTPS 200.
3. **Repo enforces rebase/FF only.** No squash.
4. **Co-author trailer:** end every commit message body with `Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>`.
5. **Skip pre-existing-flaky tests** during verification. The full list is in `docs/architecture/metadata-cached-matcher-plan.md`; the iTunes/Organize/Scan ones from SERVER-THIN-8 are *already fixed* (PRs #919, #920). Don't re-investigate them.
6. **Pebble is `cockroachdb/pebble/v2`.** Always. If you import `cockroachdb/pebble` without `/v2`, fix it before commit.
7. **Always `make deploy` after server-side merges.** Skill at `.claude/skills/deploy/*.md`.
8. **Don't ask the user questions.** Make the reasonable call. Even on ambiguous spec details, pick the option that matches existing patterns and ship.
9. **Update CHANGELOG.md `## [Unreleased]` and TODO.md as you go** — same PR as the change, not at the end.
10. **No flattery, no "honest", no affirmations.** Just do the work.

## What shipped tonight (chronological)

| PR | Branch | Subject | Status |
|---|---|---|---|
| #915 | (merged) | ctx-aware itunes BackfillExternalIDs + Registry.shuttingDown flag; pebble:closed panic fix | merged + deployed |
| #916 | (merged) | EnqueueOp dedupe on ConcurrencyKey — kills Purge×2 / Temp×2 | merged + deployed |
| #917 | (merged) | ActivityLog: status chip + cancel-on-terminal hide; opLogsLoaded flag; static bar for completed ops | merged + deployed |
| #918 | (merged) | slog.SetDefault → MultiWriter(stderr, activityWriter) | merged + deployed |
| #919 | (merged) | SERVER-THIN-8 test pass: opRegistry.Start in test setUp; v2 op lookup in waiters; v2→v1 status bridge | merged + deployed |
| #920 | (merged) | Activity Log: split Active Ops into Pending/Active/Completed; getOperationLogs reads op_logs_v2 first | merged + deployed |
| #921 | (merged) | PERF-VERSIONS: book:versiongroup:<gid>:<id> Pebble index + backfill goroutine | merged + deployed |
| #922 | (merged) | fingerprint: stamp FingerprintFailedAt; skip 7-day retry window | merged + deployed |
| #923 | (merged) | chore(logging+scheduler): silence per-pass spam; ISBN every 6h not on startup; slog duplicate-line fix | merged + deployed |
| #924 | (merged) | feat(database): MetadataCacheStore interface, Pebble impl, SQLite/Mock stubs | merged + deployed |
| #925 | (merged) | feat(server): metadata cache-first fetch + list endpoint + apply invalidation | merged + deployed |
| #927 | (merged) | feat(web): listCachedCandidates() API client | merged + deployed |
| #928 | (merged) | feat(matcher): batch fetch writes cache; Resume Review consults cache first | merged + deployed |
| #929 | (merged) | feat(web): MetadataSearchDialog Refresh button bypasses cache | merged + deployed |
| #931 | (merged) | feat(web): rename Fetch & Review → Fetch Selected; Resume Review → Review | merged + deployed |
| #932 | (merged) | docs: handoff update through matcher Task 11 | merged + deployed |
| #933 | (merged) | fix(activity): tier-by-level so Activity Log shows info/warn/error entries | merged + deployed |
| #934 | (merged) | chore(metadata): silence per-fetch Hardcover no-token log | merged + deployed |
| #935 | (merged) | chore(plugins): delete empty maintenance plugin container stub | merged + deployed |
| #936 | (merged) | docs: handoff update for #933-#935 | merged + deployed |
| #937 | (merged) | feat(web): cache provenance badge in MetadataSearchDialog | merged + deployed |
| #938 | (merged) | docs: CHANGELOG + HANDOFF refresh through PR #937 | merged + deployed |
| #939 | (merged) | fix(test): TestHandler_GetOperationLogs expects v2 lookup before v1 | merged + deployed |
| #940 | (merged) | docs(handoff): record PRs #938 + #939 | merged + deployed |
| #941 | (merged) | feat(matcher): invalidate cache on fetch-apply + revert | merged + deployed |
| #942 | (merged) | feat(matcher): invalidate cache on undoLastApply + undoMetadataChange | merged + deployed |
| #943 | (merged) | docs(handoff): record PRs #940-#942 | merged + deployed |
| #944 | (merged) | feat(pebble): invalidate metadata_cache when identity fields change | merged + deployed |
| #945 | (merged) | docs: CHANGELOG + handoff through PR #944 | merged + deployed |
| #946 | (merged) | feat(activity): parse slog TextHandler format so summaries are clean | merged + deployed |

CHANGELOG `## [Unreleased]` is up to date through PR #944. PR #946 (slog parser) is in the changelog under Fixes section.

**Activity Log status (verified 2026-05-14 00:02):** API responding, 21k+ entries indexed, summaries now show clean message text (e.g. "Token expires in 10 minutes...") instead of raw slog format. Tier-by-level fix (#933) + slog parser (#946) together make the feed actually readable.

## Outstanding work, ranked by ROI

### A. Add PRs #921/#922 to CHANGELOG (5 min, do first)
Edit `/Users/jdfalk/.worktrees/audiobook-organizer-next/CHANGELOG.md`. Under
`## [Unreleased]` → `### Fixes`, append two bullets describing the version-group
index and fingerprint retry skip. Commit alongside whatever else you ship.

### B. METADATA-CACHED-MATCHER frontend (#16, **backend done**, frontend remaining)

**Spec:** `docs/architecture/metadata-cached-matcher-design.md`.
**Plan:** `docs/architecture/metadata-cached-matcher-plan.md`.

**Status:** Backend Tasks 1-8 shipped (PRs #924, #925). Cache works end-to-end:
- `metafetch.Service.GetCachedCandidates(bookID)` / `FetchAndCache(...)` / `ListCachedSummaries(ctx)` / `InvalidateCachedCandidates(bookID)`.
- `POST /audiobooks/:id/search-metadata` is cache-first when no alt-query is passed; `?refresh=true` forces fresh.
- `GET /audiobooks/metadata/cached?status=pending|matched` returns the list for the Review popup.
- `POST /audiobooks/:id/apply-metadata` calls `InvalidateCachedCandidates(id)`.

**Frontend work — current state:**
- ✅ Task 9 (PR #927): `api.listCachedCandidates()` typed wrapper added.
- ✅ Task 10 partial (PR #929): `MetadataSearchDialog` got a Refresh icon next to Search that posts `?refresh=true`. Badge with `from_cache`/`is_fresh` flags in BookDetail is NOT yet done — the dialog has the controls; BookDetail itself doesn't visualize cache-freshness in any inline UI element.
- ✅ Task 11 partial (PR #931): toolbar labels renamed to "Fetch Selected" + "Review", tooltips updated, toast copy refreshed. Auto-open removal is already correct (`handleFetchReview` only sets the opId, never `setMetadataReviewOpen(true)`).
- ⚠️ Task 12 NOT DONE: `MetadataReviewDialog.tsx` still gets its data via `metadataReviewOpId` (operation-scoped). The Review button works because `handleResumeReview` falls back to `api.getPendingReview()` for the operation id when the cache has entries.
  - To finish: change the dialog to accept a `bookIds: string[]` prop (or fetch via `listCachedCandidates('pending')` internally) and remove the `operation_id` requirement. After that switch lands, delete the server-side `handleGetPendingReview` handler and its route registration in `server_lifecycle.go`. The legacy route and `api.getPendingReview()` client wrapper remain in place to keep Review functional during this gap.

The legacy `handleGetPendingReview` server route is **still in place** intentionally. Delete it as part of the Task 12 refactor, not separately.

Follow the plan task-by-task. The plan was written by the same author as the current session — trust it.

Skip-test list during `go test ./internal/server -short`: see plan section "Repo conventions to know" (line ~22-32).

### C. PLUGIN-DECOUPLE-SERVER-CLOSURES — promote maintenance plugin stub (#3)

**Status:** Still a stub at `internal/plugins/maintenance/register.go:33`. The plugin needs `ServerDeps` which is a 25-method interface implemented by `*Server`. The inline construction at `internal/server/server.go:402` is the production path:

```go
if err := maintenanceplugin.New(server).Register(server.opRegistry); err != nil { ... }
```

**Cleanest promotion path:**

1. Override `*Server` itself into the container under the name `"server"` immediately AFTER NewServer constructs `server` and BEFORE the inline `maintenanceplugin.New(server).Register(...)` call. The container's PostInit has already run by then so the maintenance plugin's PostInit can't pull it. Solution: introduce a `Container.LatePostInit(ctx)` phase that runs after Override calls, OR (simpler) keep the maintenance registration inline but delete the `register.go` stub and document the inline path as canonical. Pick the simpler one — it's a documentation change, not a refactor.

2. Update `register.go`:
   - Delete the stub Build that returns `(*Plugin)(nil)`.
   - Replace with a comment explaining that `internal/server/server.go:~402` is the canonical registration site because `ServerDeps` is implemented by `*Server` and cannot be decomposed without a 25-service container split.
   - Remove the `serviceregistry.Register(...)` call entirely — there's nothing to register.

3. Update `TODO.md` to mark `PLUGIN-DECOUPLE-SERVER-CLOSURES` as "documented as inline-only; full decoupling deferred to post-matcher work" and move it to a `Deferred` subsection.

The actual decoupling (event-bus for `OnBookCreated`, explicit container deps for each of the 25 methods) is at least a 5-PR effort. The user knows this — the TODO entry calls it out. Just stop pretending the stub is in flight.

### D. Activity Log entries — root cause found and fixed (PR #933)

Root cause: every line `activity.Writer.Write` parsed was assigned
`Tier: "debug"`. The Activity Log UI default has `debug` unchecked
under tier filters, so `exclude_tiers=debug` got sent to the server
and every persisted entry was filtered out before render.

Fix (PR #933): derive tier from level — `info`/`warn`/`error` → tier
`change` (visible by default), `debug` stays `debug`. Slog and stdlib
log lines should now appear in the feed without the user touching
filters. **Verify after deploy:** open Activity Log on prod, expect
non-empty entry list with registry/scanner/backfill records.

### E. Logs-takes-forever (USER REPORTED — partially fixed)

The 14.7s `/audiobooks/:id/versions` is fixed by PR #921. Other slow endpoints to check (server log shows `/api/v1/file-ops/pending` polling every 3-4s):

- `/api/v1/file-ops/pending` polled at 3s interval — fine but verify it's cheap (<50ms).
- Library facet/tags load on every page render — could be cached.

User mentioned "recording rules" pattern (Prometheus): pre-compute hot
aggregates into a Pebble cache with TTL + invalidation on writes. Worth doing
for: facet counts, library stats, version-group sizes. **Don't start this
without a design doc** — too much surface area.

### F. TECHDEBT-1 — React Router v6 future-flag warnings (10 min)

Easiest "shippable nothing" PR. Open `web/src/main.tsx` (or wherever `<BrowserRouter>` lives — grep `BrowserRouter`), add:

```tsx
<BrowserRouter future={{ v7_startTransition: true, v7_relativeSplatPath: true }}>
```

Same for any `MemoryRouter` in tests. PR title: `chore(web): opt into react-router v7 future flags`.

### G. Misc small wins

- **CHANGELOG/TODO maintenance:** TODO.md still lists SERVER-THIN-8 as in-progress (I started updating but the file's long). Confirm the `[ ] **SERVER-THIN-8**` line is now `[x]` and the body reads "PRs #919, #920".
- **Maintenance plugin task #3** in the in-conversation task list is marked `pending`. Per item C above, change to `completed (documented inline)` once you ship the doc-only PR.

## Verification commands cheatsheet

```bash
# build worktree
cd /Users/jdfalk/.worktrees/audiobook-organizer-next && go build ./...

# server-side tests (skip pre-existing flakies — they're now passing but slow)
go test ./internal/server -short -timeout=120s

# all package tests
go test ./... -short -timeout=180s

# frontend typecheck
cd web && npx tsc --noEmit

# frontend tests
npx vitest --run

# deploy from main checkout
cd /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer
git pull --ff-only origin main
make deploy
curl -skIo /dev/null -w "%{http_code}\n" https://<your-server-ip>:8484/

# tail prod logs (user reads these to find performance hotspots)
ssh <your-username>@<your-server-ip> 'sudo journalctl -u audiobook-organizer -f --no-pager'

# pebble store on prod
ssh <your-username>@<your-server-ip> 'sudo ls -la /var/lib/audiobook-organizer/audiobooks.pebble/'
```

## Files you'll touch (cheatsheet)

- **Pebble store:** `internal/database/pebble_store.go` (8000+ lines, modular by domain). Look for prefix patterns like `book:author:` for examples of secondary indexes.
- **SQLite store:** `internal/database/sqlite_store_*.go` — almost never the production path, but tests use it. Mirror Pebble changes here when feasible; the mock store in `internal/database/mocks/mock_store.go` is autogenerated by mockery — regenerate with `make mocks` after interface changes.
- **HTTP handlers:** `internal/server/<feature>_handlers.go`. New routes go in `internal/server/server_lifecycle.go` (~line 1040 has the routing tree).
- **UOS v2 ops:** `internal/operations/registry/` for the registry itself; plugin op-defs live in `internal/plugins/<plugin>/` or `internal/server/<feature>_ops.go`. Always `Isolate: false` unless you genuinely need subprocess isolation.
- **Activity Log UI:** `web/src/pages/ActivityLog.tsx` (1500 lines, just take what you need).
- **Library page:** `web/src/pages/Library.tsx`.

## Pitfalls

- **`s.Store()` returns the same instance regardless of test setup**, but `database.GetGlobalStore()` lookups may diverge in tests. Always prefer `s.Store()` / explicit params (SERVER-GLOBAL-STORE-AUDIT is done; new code shouldn't reintroduce globals).
- **Pebble `book:0`..`book:;` bound trick:** the trailing `;` is the next ASCII char after `:`. New secondary indexes follow the same `book:<index>:<key>` shape so the existing `strings.Contains(key, ":path:")` skip-filter keeps working. Add a new skip-filter check to `GetAllBooks`-style scans if you introduce a new index prefix.
- **`go test ./internal/server` is slow** (~120s on this machine). Use `-run` to scope to your changes.
- **Frontend tests** sometimes hang on Vitest if `@testing-library/react` not installed in worktree — run `npm install` in `web/` first.
- **Pre-commit hook** runs Prettier; commit messages must use HEREDOC to preserve newlines.

## When you hit your own usage cap

Write a successor handoff to `docs/HANDOFF-<date>-<part>.md` and commit it
on its own branch. Update this file with a link. The user is sleeping — the
goal is uninterrupted forward progress, not a perfect endpoint.

— Opus 4.7, 2026-05-13 ~22:50 ET
