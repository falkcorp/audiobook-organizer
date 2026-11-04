<!-- file: docs/agent-tasks/platform-core/PC-00-03-sessions-list-derived-handle.md -->
<!-- version: 1.0.0 -->
<!-- guid: 1c0a2a1b-5f8d-4f86-92ee-1e7e33deda0c -->
<!-- last-edited: 2026-11-04 -->

# PC-00-03 — Early fix 1: the sessions list returns a view without the bearer token and revokes by a derived handle

| Field | Value |
|---|---|
| ID | PC-00-03 |
| Tier | **T1 (Sonnet/medium)**: one handler pair and one route; the view, the handle rule and every edit are given below |
| Wave | W00 (S0, release `S0`); live security hotfix |
| Depends on | none |
| Priority / effort | P0 / S |
| Branch | `fix/pc-00-03-session-handle` |
| Trailer on every commit | `Stage-Release: S0 1` |
| Owning spec | `docs/specs/platform-core/05-authn.md` §11.1 item 1 and "Out-of-scope defects found" item 1; `09-abo-on-core.md` S0 "Hotfixes" |

**The defect (verified at HEAD).** `ListMySessions` (`internal/server/handlers/auth.go:513-537`) returns
`sessionView{database.Session; Current bool}`, which embeds `database.Session`, whose JSON field `id` **is the bearer
session token** (`internal/database/store.go:1044-1045`). Any credential of a user, an API key included (the route
has no permission and no credential guard), can read the user's live session tokens and replay one as
`Authorization: Bearer`, which yields `MethodSession` and defeats the key's scopes and the credential guard.
**The fix (05-authn §11.1 item 1):** return a view without `id`, with a derived handle, and revoke by that handle.

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-pc-00-03" -b fix/pc-00-03-session-handle origin/main
cd "$REPO/../audiobook-organizer-pc-00-03"
git worktree list
npm ci --prefix web          # or make web-deps once PC-01-06 has merged
```

Never run `go work init` in the checkout or the worktree.

## Exact files

Create:

- `internal/server/handlers/auth_sessions_view_test.go`

Modify:

- `internal/server/handlers/auth.go`
- `internal/server/wire_auth_routes.go`
- `internal/server/credential_routes_test.go` (one map key; not in the inventory row, see Planner notes)
- `docs/AI-REFERENCE.md` (one table row; see Planner notes)
- `TODO.md` or `todo.d/2026-11-04_platform_core_followups.md` (check off one entry)

Add one `changelog.d/` fragment.

## Re-verify greps (expected counts at `ac4368bdb`, origin/main when this brief was written)

```bash
grep -n 'func (h \*AuthHandler) ListMySessions\|func (h \*AuthHandler) RevokeMySession' internal/server/handlers/auth.go | wc -l   # 2 (:513, :540)
grep -n '^		database.Session$' internal/server/handlers/auth.go | wc -l                       # 1 (:526, the embedded field)
grep -n 'sessionID := strings.TrimSpace(c.Param("id"))' internal/server/handlers/auth.go | wc -l   # 1 (:547)
grep -n 'authProtected.DELETE("/sessions/:id", authH.RevokeMySession)' internal/server/wire_auth_routes.go | wc -l   # 1 (:62)
grep -n '"DELETE /api/v1/auth/sessions/:id"' internal/server/credential_routes_test.go | wc -l   # 1 (:48)
grep -n '/auth/sessions/:id' docs/AI-REFERENCE.md | wc -l                                       # 1 (:335)
grep -rn 'auth/sessions' web/src | wc -l                                                         # 0 (no SPA caller)
grep -rn 'func SessionHandle' internal | wc -l                                                   # 0
```

A different count means STOP and report.

## The handle rule (fixed here)

05-authn §11.1 says "a derived handle (e.g. SHA-256 prefix)". This brief fixes it, and the core era reuses it
(05-authn §3.4 `SessionRecord.PublicID`: "ABO: early fix 1's derived handle"):

**handle = the first 32 lowercase hex characters (16 bytes) of SHA-256 over the session ID string's bytes.**

## Steps

1. Run the re-verify greps.
2. In `internal/server/handlers/auth.go` add `"crypto/sha256"` and `"encoding/hex"` to the imports and add, above
   `ListMySessions`:
   ```go
   // SessionHandle is the public handle of a session: the first 32 hex characters of
   // SHA-256 over its ID. The ID is the bearer token and is never returned by an API;
   // the handle names the session for display and revoke (05-authn §11.1 item 1).
   func SessionHandle(sessionID string) string {
       sum := sha256.Sum256([]byte(sessionID))
       return hex.EncodeToString(sum[:16])
   }

   // SessionView is one entry of GET /auth/sessions. It lists every field of
   // database.Session except ID, by name, so a field added to Session later is not
   // exposed by accident.
   type SessionView struct {
       Handle          string    `json:"handle"`
       UserID          string    `json:"user_id"`
       CreatedAt       time.Time `json:"created_at"`
       ExpiresAt       time.Time `json:"expires_at"`
       IP              string    `json:"ip"`
       UserAgent       string    `json:"user_agent"`
       Revoked         bool      `json:"revoked"`
       Version         int       `json:"version"`
       Origin          string    `json:"origin,omitempty"`
       CredentialEpoch int64     `json:"credential_epoch,omitempty"`
       Current         bool      `json:"current"`
   }
   ```
3. Replace the body of `ListMySessions` from `type sessionView struct {` through the `response` loop with:
   ```go
   response := make([]SessionView, 0, len(sessions))
   for _, s := range sessions {
       response = append(response, SessionView{
           Handle: SessionHandle(s.ID), UserID: s.UserID, CreatedAt: s.CreatedAt,
           ExpiresAt: s.ExpiresAt, IP: s.IP, UserAgent: s.UserAgent, Revoked: s.Revoked,
           Version: s.Version, Origin: s.Origin, CredentialEpoch: s.CredentialEpoch,
           Current: currentSession != nil && s.ID == currentSession.ID,
       })
   }
   ```
   Keep `httputil.RespondWithOK(c, gin.H{"sessions": response})`. Update the doc comment: "GET /auth/sessions; the
   bearer token (`id`) is never returned".
4. Rewrite `RevokeMySession` (doc comment: "DELETE /auth/sessions/:handle"):
   ```go
   handle := strings.ToLower(strings.TrimSpace(c.Param("handle")))
   if handle == "" {
       httputil.RespondWithBadRequest(c, "session handle required")
       return
   }
   sessions, err := h.store.ListUserSessions(user.ID)
   if err != nil {
       httputil.RespondWithInternalError(c, "failed to list sessions")
       return
   }
   var target *database.Session
   for i := range sessions {
       if SessionHandle(sessions[i].ID) == handle {
           target = &sessions[i]
           break
       }
   }
   if target == nil {
       httputil.RespondWithNotFound(c, "session", handle)
       return
   }
   if err := h.store.RevokeSession(target.ID); err != nil {
       httputil.RespondWithInternalError(c, "failed to revoke session")
       return
   }
   if currentSession != nil && currentSession.ID == target.ID {
       clearSessionCookie(c)
   }
   httputil.RespondWithNoContent(c)
   ```
   Only the caller's own sessions are searched, so another user's session is never found (404, never 403, so the
   route no longer confirms that a token exists). A raw session ID in the path is not accepted. `GetSession` is no
   longer called by this handler; leave it in the `AuthSessionStore` interface (other handlers use it).
5. In `internal/server/wire_auth_routes.go:62` change `"/sessions/:id"` to `"/sessions/:handle"`. Bump the header.
6. In `internal/server/credential_routes_test.go:48` change the key `"DELETE /api/v1/auth/sessions/:id"` to
   `"DELETE /api/v1/auth/sessions/:handle"` (same reason text). Bump the header.
7. In `docs/AI-REFERENCE.md:335` change `` `/auth/sessions/:id` `` to `` `/auth/sessions/:handle` ``. Bump its header.
8. Check off the TODO entry: `grep -n 'hotfix, early fix 1)' TODO.md todo.d/*.md`; change `- [ ]` to `- [x]`.
9. Changelog fragment `changelog.d/<YYYY-MM-DD>_pc-00-03-session-handle.md` under `### Security`: "the session list
   no longer shows session tokens" (09 S0 release-note text); state that `GET /api/v1/auth/sessions` entries carry
   `handle` instead of `id` and that `DELETE /api/v1/auth/sessions/:handle` takes that handle.
10. Bump `auth.go`'s header (minor) and give the new test file a header with a fresh guid.

## Code contract

```go
// package handlers (internal/server/handlers); names coined here
func SessionHandle(sessionID string) string // hex(sha256(id))[:32]
type SessionView struct { Handle string `json:"handle"`; /* every other Session field except ID */; Current bool `json:"current"` }
```

Routes: `GET /api/v1/auth/sessions` (unchanged path; entries are `SessionView`), `DELETE /api/v1/auth/sessions/:handle`.

## TDD: tests to write first

`internal/server/handlers/auth_sessions_view_test.go` (package `handlers_test`). Use a real store:
`store, err := database.NewPebbleStoreInMemory("db")`, `t.Cleanup(func(){ store.Close() })`;
`h := handlers.NewAuthHandler(store, true)`; create a user with `store.CreateUser("sess-owner", "owner@example.com",
"bcrypt", "h", []string{"viewer"}, "active")` and two sessions with
`store.CreateSession(user.ID, "127.0.0.1", "ua", time.Hour, 0)`; call the handlers with `newAuthCtx`, `setAuthUser`
and `setAuthSession` (`auth_test.go:37-63`).

- **`TestListMySessions_NoSessionTokenInResponse`** (name coined here; the inventory's "GET /api/v1/auth/sessions
  returns no id equal to a session token"): the raw response body contains neither session's ID as a substring;
  `data.sessions[*]` has no `id` key; each `handle` equals `handlers.SessionHandle(<that session's ID>)` and is 32
  lowercase hex characters; exactly one entry has `"current": true` (the one passed to `setAuthSession`).
- **`TestRevokeMySession_ByHandle`** (name coined here; the inventory's "DELETE /api/v1/auth/sessions/:handle
  revokes"): `c.Params = gin.Params{{Key: "handle", Value: SessionHandle(s2.ID)}}` → 204, and
  `store.GetSession(s2.ID)` returns `Revoked == true` while `s1` is not revoked;
  - the raw session ID as the handle → 404 and nothing revoked;
  - a handle of another user's session → 404 and that session not revoked;
  - an uppercase copy of a valid handle → 204 (the handler lower-cases);
  - revoking the current session clears the cookie (the response carries a `Set-Cookie` for
    `servermiddleware.SessionCookieName` with `Max-Age=0`, which `clearSessionCookie`'s `MaxAge: -1` produces).
- **`TestSessionHandle_Rule`** (name coined here): `SessionHandle("abc")` equals the literal
  `"ba7816bf8f01cfea414140de5dae2223"` (the first 32 hex characters of SHA-256("abc")).

## Acceptance

```bash
go build ./...                                                                                   # exit 0
go test -race -count=1 -run 'TestListMySessions|TestRevokeMySession|TestSessionHandle' ./internal/server/handlers/   # ok
go test -race -count=1 -run 'TestCredentialRoutes' ./internal/server/                             # ok
grep -n 'database.Session$' internal/server/handlers/auth.go | wc -l                              # 0
grep -n '"/sessions/:handle"' internal/server/wire_auth_routes.go | wc -l                         # 1
make ci                                                                                         # last line: ✅ All CI checks passed!
```

## Out of scope

- Hashing sessions at rest (H1, 05-authn §11.2). Session rows stay HEAD's `sess:<raw>` rows.
- The ABS surface's own sessions (`/api/me/sessions`), a different family.
- Any other route that might embed `database.Session` (report one if you see it; do not change it here).

## Rollback

Revert the PR; binary swap. No stored state changes. A client written against the new `handle` field gets 404 from
an older build's `DELETE /sessions/:id` with a handle, which is harmless.

## Repo rules

- Headers bumped on every changed file; the fragment has none.
- Conventional commit `fix(auth): session list hides the bearer token; revoke by derived handle (early fix 1)`,
  ending:
  ```
  Stage-Release: S0 1
  Co-Authored-By: Claude <noreply@anthropic.com>
  ```
- `gh pr create`, wait for the required checks, `gh pr merge <n> --rebase`; never squash, never `--admin`.
- Remove the worktree after merge.

## Planner notes

- The inventory row omits `internal/server/credential_routes_test.go`: its `exemptRoutes` map keys the route as
  `"DELETE /api/v1/auth/sessions/:id"` (`:48`) and the test fails on a key that names no real route, so the rename to
  `:handle` must change it in the same PR.
- `docs/AI-REFERENCE.md:335` lists the old route; updated in the same PR so the reference does not go stale.
- The spec gives the handle only as "e.g. SHA-256 prefix"; this brief fixes it at 32 hex characters of SHA-256 over
  the ID. The S4 native session's `PublicID` for ABO is "early fix 1's derived handle" (05-authn §3.4), so the rule
  should be recorded in the S0 stage record for S4 to cite.
