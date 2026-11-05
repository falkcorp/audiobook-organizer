<!-- file: docs/agent-tasks/platform-core/R0-04-sessions-list-without-token.md -->
<!-- version: 1.1.1 -->
<!-- guid: 648e9591-d8d7-4347-a426-28fd4281ad1e -->
<!-- last-edited: 2026-11-05 -->

# R0-04 — Early fix 1: the sessions list returns a view without the bearer token and revokes by a derived handle

| Field | Value |
|---|---|
| ID | R0-04 |
| Release | R0 (09 §9.4 R0 Content (1)); a live security hotfix in HEAD's code |
| Tier | **T1** (Sonnet / medium): one handler and one route; the handle rule is given |
| Wave | W01 |
| Depends on | none |
| Branch | `fix/r0-04-sessions-view` |
| Owner text | 05-authn §11.1 item 1 and "Out-of-scope defects found" item 1 |

**Gate:** plan, then execute autonomously in your own worktree and PR. Follow the steps exactly; every name and
rule you need is below. If a grep below does not return what it says, STOP and report.

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/r0-04-sessions" -b fix/r0-04-sessions-view origin/main
cd "$REPO/.worktrees/r0-04-sessions"
git rebase origin/main
npm ci --prefix web
```

## Goal

`GET /api/v1/auth/sessions` embeds `database.Session`, whose `id` **is the bearer token**, so any credential of a
user (an API key included; the route has no permission and no credential guard) can read the user's live session
tokens and replay one as `Authorization: Bearer` (05-authn "Out-of-scope defects found" item 1). The hotfix
(05-authn §11.1 item 1): return a view **without `id`** and **with a derived handle** for revoke; revoke by the
derived handle deletes exactly that session.

**The handle rule.** The spec says "a derived handle (e.g. SHA-256 prefix)". This brief fixes it as: the first 16
lowercase hex characters of `hex.EncodeToString(sha256.Sum256([]byte(session.ID)))` (rule fixed here). It is
derived, never stored, and does not equal the token.

## Files

| Action | Path |
|---|---|
| modify | `internal/server/handlers/auth.go` (`ListMySessions`, `RevokeMySession`, one new helper) |
| modify | `internal/server/wire_auth_routes.go` (comment only, see step 4) |
| create | `internal/server/handlers/auth_sessions_view_test.go` (package `handlers_test`) |
| create | `changelog.d/<date>_r0_sessions_view.md` (no header) |
| tick | the entry "SECURITY (hotfix, early fix 1)" (step 6) |

## Re-verify before editing (expected at the base commit)

The spec anchors at 748fd787a; these files are unchanged at HEAD, so the line numbers hold.

```bash
grep -n 'func (h \*AuthHandler) ListMySessions' internal/server/handlers/auth.go      # expect 1 hit: 513
grep -n 'func (h \*AuthHandler) RevokeMySession' internal/server/handlers/auth.go     # expect 1 hit: 540
grep -n '		database.Session$' internal/server/handlers/auth.go                    # expect 1 hit: 526 (the embed)
grep -n 'authProtected.GET("/sessions", authH.ListMySessions)' internal/server/wire_auth_routes.go       # expect 1 hit: 61
grep -n 'authProtected.DELETE("/sessions/:id", authH.RevokeMySession)' internal/server/wire_auth_routes.go # expect 1 hit: 62
grep -n '"DELETE /api/v1/auth/sessions/:id"' internal/server/credential_routes_test.go # expect 1 hit: 48 (must keep matching)
grep -rn 'ListMySessions\|RevokeMySession' internal --include=*_test.go | wc -l        # expect 0 (no existing test to update)
grep -rln 'auth/sessions' web/src | wc -l                                             # expect 0 (no SPA caller)
grep -n 'ListUserSessions(userID string)' internal/server/handlers/auth.go            # expect 1 hit: 70 (in AuthSessionStore)
```

## Steps

1. **Write the test first** (below) and run it: it must fail on the base (the response carries `id`).
2. **Add the helper** in `internal/server/handlers/auth.go`, next to `ListMySessions` (add `crypto/sha256` and
   `encoding/hex` to the imports if missing):

   ```go
   // sessionHandle is the revoke handle the sessions list shows instead of the
   // session ID, which is the bearer token (05-authn §11.1 item 1): the first 16
   // hex characters of SHA-256(id). Derived, never stored, never equal to the token.
   func sessionHandle(id string) string {
   	sum := sha256.Sum256([]byte(id))
   	return hex.EncodeToString(sum[:])[:16]
   }
   ```

3. **Replace the view in `ListMySessions`.** Delete the `sessionView` struct that embeds `database.Session` and use
   this one (field order and JSON names exactly as written; `id` and `credential_epoch` are absent on purpose):

   ```go
   type sessionView struct {
   	Handle    string    `json:"handle"`
   	UserID    string    `json:"user_id"`
   	CreatedAt time.Time `json:"created_at"`
   	ExpiresAt time.Time `json:"expires_at"`
   	IP        string    `json:"ip"`
   	UserAgent string    `json:"user_agent"`
   	Revoked   bool      `json:"revoked"`
   	Origin    string    `json:"origin,omitempty"`
   	Current   bool      `json:"current"`
   }
   ```

   Fill it from each `s` of `ListUserSessions(user.ID)`: `Handle: sessionHandle(s.ID)`, the other fields copied
   from `s`, `Current: currentSession != nil && s.ID == currentSession.ID`. The response stays
   `gin.H{"sessions": response}`.
4. **Revoke by handle in `RevokeMySession`.** The route pattern stays `DELETE /sessions/:id` (so
   `credential_routes_test.go:48` keeps matching); the `:id` path parameter now carries the handle. Replace the
   lookup:
   - `handle := strings.TrimSpace(c.Param("id"))`; empty → `RespondWithBadRequest(c, "session handle required")`.
   - `sessions, err := h.store.ListUserSessions(user.ID)`; on error → `RespondWithInternalError(c, "failed to list sessions")`.
   - find the one `s` with `sessionHandle(s.ID) == handle`; none → `RespondWithNotFound(c, "session", handle)`.
   - `h.store.RevokeSession(s.ID)`; error → `RespondWithInternalError(c, "failed to revoke session")`.
   - when `currentSession != nil && currentSession.ID == s.ID`, `clearSessionCookie(c)`.
   - `RespondWithNoContent(c)`.

   A raw session ID sent as `:id` now matches nothing and answers 404. The "another user's session" 403 branch goes
   away, because only the caller's own sessions are searched. Update the two handler doc comments to say the list
   returns handles and revoke takes a handle.
5. **Route comment.** In `internal/server/wire_auth_routes.go` add, above line 61, the comment
   `// The sessions list returns revoke handles, never session IDs (the ID is the bearer token; 05-authn §11.1 item 1); DELETE /sessions/:id takes a handle.`
   Change no code in that file.
6. **TODO bookkeeping.** `grep -n 'SECURITY (hotfix, early fix 1)' TODO.md todo.d/2026-11-04_platform_core_followups.md`:
   tick the `- [ ]` on that entry in `TODO.md` if it is there, otherwise in the fragment.
7. **Changelog fragment** (no header): "Security: the session list no longer shows session tokens; a session is
   revoked by the handle the list shows."
8. **Headers:** bump `version` (minor: 3.2.1 → 3.3.0 for `auth.go`, 1.8.0 → 1.8.1 for `wire_auth_routes.go`) and set
   `last-edited` on both; the new test file gets the 4-line header with a fresh guid
   (`python3 -c 'import uuid;print(uuid.uuid4())'`).

## Code contract

```go
func sessionHandle(id string) string            // unexported; rule above
func (h *AuthHandler) ListMySessions(c *gin.Context)   // GET /api/v1/auth/sessions: {"sessions":[sessionView…]}
func (h *AuthHandler) RevokeMySession(c *gin.Context)  // DELETE /api/v1/auth/sessions/:id, :id = handle
```

## Tests to write first

`TestLegacySessionsList_NoToken` (name from 05-authn §11.1's table) in
`internal/server/handlers/auth_sessions_view_test.go`. Build it like
`internal/server/handlers/auth_permissions_flow_test.go:33-55`: a real store
(`database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))`), `auth.SeedRoles(store)`, one user, then three
sessions for that user with `store.CreateSession(user.ID, "127.0.0.1", "ua", time.Hour, 0)`; a router with
`GET /auth/sessions` and `DELETE /auth/sessions/:id` behind `servermiddleware.RequireAuth(store)`, called with the
first session's token as `Authorization: Bearer <id>`. Assert:

1. status 200; decode the body generically (`map[string]any`) and walk **every** string value in it recursively:
   none equals any of the three session IDs (a live token), and no object has a key `id`.
2. each listed entry has a 16-character `handle`; exactly one has `current: true`.
3. `DELETE /auth/sessions/<handle of the second session>` → 204; afterwards `store.GetSession(second.ID)` shows it
   revoked (or gone, whichever `RevokeSession` does) while the first and third sessions are unchanged.
4. `DELETE /auth/sessions/<second session's raw ID>` → 404 (a token is not a handle), and
   `DELETE /auth/sessions/0000000000000000` → 404.

## Acceptance

```bash
go build ./...                                                                       # exit 0
go test -race -count=1 ./internal/server/handlers/ -run 'TestLegacySessionsList_NoToken'   # ok
go test -race -count=1 ./internal/server/handlers/ ./internal/server/                # ok (credential_routes_test included)
grep -c '		database.Session$' internal/server/handlers/auth.go                  # expect 0
grep -c 'func sessionHandle' internal/server/handlers/auth.go                       # expect 1
make ci; echo "exit $?"                                                             # expect exit 0
```

The e2e suite and every required check of 09 §9.2.1 are green on the PR and in the merge queue.

## Out of scope

- Core sessions with `PublicID` and `TestSessions_ListNeverReturnsToken` (R2, F4).
- Any other session route (`/users/:id/sessions` does not exist at HEAD), hashing sessions at rest (R2).
- Adding a permission or the credential guard to the route.

## Rollback

Revert the commit in a new PR. No stored data changes.

## Repo rules

- File headers bumped on every modified file; new files get a header; fragments carry none.
- Conventional commit, e.g. `fix(auth): sessions list shows revoke handles, never session tokens`, ending
  `Co-Authored-By: Claude <noreply@anthropic.com>`. No release or flip trailer.
- `gh pr merge <n> --rebase` after every required check is green; never squash, never `--admin`.
- Remove the worktree after merge.
