<!-- file: docs/agent-tasks/todo-completion/server/TASK-138-exempt-the-abs-router-group-from-the-global-basi.md -->
<!-- version: 1.0.0 -->
<!-- guid: 35c6082c-b812-497b-8a05-ddb8fd6f5c0c -->
<!-- last-edited: 2026-08-21 -->

# TASK-138 — Exempt the ABS router group from the global BasicAuth() middleware (ABS-SYNC)

**Priority:** P1 · **Effort:** S · **Recommended subagent:** Sonnet-class · server subagent · **Why:** Small, surgical middleware change, but it is a security-boundary edit (auth exemption) so it needs careful precision on the path-prefix match and a test proving BOTH the exemption and that non-ABS paths still enforce basic auth · **Depends on:** none · **Wave:** 1 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 10290 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**ABS-SYNC: exempt the ABS surface from `BasicAuth" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-13.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-138-exempt-the-abs-router-group-from-the-global-basi" -b agent/server-138-exempt-the-abs-router-group-from-the-global-basi origin/main
cd "$REPO/.worktrees/server-138-exempt-the-abs-router-group-from-the-global-basi"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

In internal/server/middleware/basicauth.go's BasicAuth(), add an ABS-path exemption alongside the existing health/static-asset exemptions so that when config.AppConfig.BasicAuthEnabled is true, requests under the ABS surface (paths registered on absGroup: /api/authorize, /api/me*, /api/libraries*, /api/items*, /api/session*, /public/session*, /api/playlists/:id, /login, /logout, /auth/*, /status, /ping — see internal/server/handlers/abs/handler.go:432 Register for the full list) skip HTTP Basic Auth entirely, since ABS clients authenticate via their own bearer/session/CF-assertion scheme (internal/server/middleware/absauth.go ABSRequireAuth) and cannot also send Authorization: Basic on the same header.

## Background (verify before editing)

- internal/server/middleware/basicauth.go's BasicAuth() exempts only /api/health, /api/v1/health (L28-32) and static assets (L34-45); everything else on s.router is gated.
- internal/server/handlers/abs/handler.go:432 Register mounts the ABS surface. CRITICAL: the ABSRequireAuth middleware is not constructed until L446 — /ping (L433), /status (L434), /login (L436), /auth/refresh (L437), /auth/openid (L442) and /auth/openid/callback (L443) are registered with NO app-layer auth, and /api/items/:id/cover is deliberately auth-free. For those paths BasicAuth is currently the only gate, so they MUST NOT be added to the exemption list.
- Only the routes that carry the `auth` middleware may be exempted: /logout, /api/authorize, /api/me*, /api/libraries*, /api/collections*, /api/playlists/:id, /api/items/:id (but NOT /api/items/:id/cover), /api/items/:id/play, /api/items/:id/file*.
- internal/server/server.go:469 applies servermiddleware.BasicAuth() globally; internal/server/wire_abs_routes.go:504 mounts absGroup := s.router.Group("") as a child, so it inherits BasicAuth with no override.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'router.Use(servermiddleware.BasicAuth())' internal/server/server.go   # 1 hit ~L469 — BasicAuth() is applied globally to s.router
  grep -n 'absGroup := s.router.Group' internal/server/wire_abs_routes.go   # 1 hit ~L504 — the ABS group is a child of s.router with no BasicAuth exemption
  grep -n 'Exempt' internal/server/middleware/basicauth.go   # 2 hits ~L28, L34 — BasicAuth()'s current exemption list is health endpoints + static assets only
  grep -n 'BasicAuth()' internal/server/server.go   # 1 hit at L469: router.Use(servermiddleware.BasicAuth()) — BasicAuth is applied globally to s.router
  grep -n 'absGroup := s.router.Group' internal/server/wire_abs_routes.go   # 1 hit at L504 — the ABS group inherits it with no exemption
  grep -n 'r.GET("/ping"\|r.GET("/status"\|r.POST("/login"\|r.GET("/auth/openid"\|ABSRequireAuth(h.resolver)' internal/server/handlers/abs/handler.go   # /ping L433, /status L434, /login L436, /auth/openid L443 — all registered BEFORE `auth := servermiddleware.ABSRequireAuth(h.resolver)` at L446, so BasicAuth is their only gate — six ABS routes have NO ABS auth middleware and rely on BasicAuth as their only gate
  grep -n 'Exempt\|HasPrefix' internal/server/middleware/basicauth.go   # comments at L28 and L34, strings.HasPrefix(path, "/assets/") at L35 — the existing exemption style to mirror
  ```

### Reuse — don't invent

- Use `path-prefix exemption pattern already used for /assets/` in `internal/server/middleware/basicauth.go` (verify: `grep -n 'strings.HasPrefix(path, "/assets/")' internal/server/middleware/basicauth.go`) — do NOT write a parallel helper.

## Step-by-step

1. Read internal/server/handlers/abs/handler.go's Register method (func (h *Handler) Register(r gin.IRouter), ~L432) to enumerate every literal path prefix it registers, so the exemption is precise rather than a blanket '/api' prefix (which would also exempt the real /api/v1 app surface — NOT wanted).
2. In internal/server/middleware/basicauth.go's BasicAuth(), add a new exemption block after the existing static-asset check (after line ~44) that returns c.Next() early when the request path matches one of the ABS route prefixes gathered in step 1. Prefer an explicit prefix list (e.g. '/api/me', '/api/libraries', '/api/items', '/api/session', '/public/session', '/api/authorize', '/api/playlists', '/auth/openid', '/status', '/ping') defined as a package-level []string or via strings.HasPrefix checks, matching the existing code style (see the /assets/ check for the pattern).
3. Bump the file version header per file-headers.md (this is a Go file: file/version/guid/last-edited comment block at the top).
4. Document the mutual-exclusion caveat: add a one-line comment above the new exemption noting that BasicAuth and ABS bearer auth cannot both gate the same Authorization header, so ABS paths are exempt from BasicAuth by design, not by oversight.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_server_138.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A path like /api/libraries that ALSO happens to prefix-match an app-API route must not be exempted if app-API and ABS ever share a literal path — verify by diffing against internal/server (app API) route registrations, not just ABS's.
- BasicAuthEnabled=false (the default) must remain a full no-op — the exemption branch should never even be reached in that case, matching the existing early-return at the top of BasicAuth().

## Tests

- internal/server/middleware/basicauth_test.go: add TestBasicAuth_ABSPathsExempt — set config.AppConfig.BasicAuthEnabled=true, hit an ABS path (e.g. GET /api/me with no Basic Auth header, no ABS credential either) through the middleware alone, and assert it is NOT aborted with 401 by BasicAuth specifically (i.e. c.Next() was reached) — this test should stub/skip the downstream ABS auth check or assert only on the BasicAuth layer's behavior.
- internal/server/middleware/basicauth_test.go: keep/extend the existing non-exempt-path test (a plain /api/v1/... path) to prove BasicAuth STILL enforces basic auth outside the new ABS exemption — this is the anti-over-suppression check: the fix must not accidentally exempt the whole /api prefix.

Anti-over-suppression test: `TestBasicAuth_UnauthenticatedABSRoutesStillEnforced — with BasicAuthEnabled=true, GET /ping, GET /status, POST /login and GET /auth/openid must STILL be challenged with 401 by BasicAuth, because ABSRequireAuth is not on those routes (abs/handler.go:433-443). Plus TestBasicAuth_NonABSPathStillEnforced for a plain /api/v1/... path.` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/server/middleware/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `go test ./internal/server/middleware/... -run TestBasicAuth` passes, including the new ABS-exemption test and the existing non-ABS enforcement test
- [ ] `grep -n 'ABS' internal/server/middleware/basicauth.go` shows the new exemption block with a comment explaining the mutual-exclusion rationale
- [ ] Anti-over-suppression test: `TestBasicAuth_UnauthenticatedABSRoutesStillEnforced — with BasicAuthEnabled=true, GET /ping, GET /status, POST /login and GET /auth/openid must STILL be challenged with 401 by BasicAuth, because ABSRequireAuth is not on those routes (abs/handler.go:433-443). Plus TestBasicAuth_NonABSPathStillEnforced for a plain /api/v1/... path.` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/server/middleware/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_server_138.md`.

## Commit message

```
refactor(server): Exempt the ABS router group from the global BasicAuth() midd (ABS-SYNC)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

basic_auth_enabled is off by default in prod per the item text, so this is a latent bug, not an active incident — still worth fixing before anyone turns basic auth on.
