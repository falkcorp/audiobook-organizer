<!-- file: docs/agent-tasks/platform-core/R0-03-admin-checks-honour-key-scopes.md -->
<!-- version: 1.0.0 -->
<!-- guid: 876a356e-1741-4b99-8188-3d24d8a0e712 -->
<!-- last-edited: 2026-11-05 -->

# R0-03 — Early fix 4: `RequireAdmin`, `adminRole`, the two `handlers/user.go` checks and `mergeActor` honour API-key scopes

| Field | Value |
|---|---|
| ID | R0-03 |
| Release | R0 (09 §9.4 R0 Content (1)); a live security hotfix in HEAD's code |
| Tier | **T2** (Sonnet / high): one rule at four call sites; an authorization change, well specified |
| Wave | W02 |
| Depends on | R0-02 (both edit `internal/server/middleware/auth.go`; start from a base that contains R0-02's merge) |
| Branch | `fix/r0-03-admin-key-scopes` |
| Owner text | 05-authn §11.1 item 4; 05-rbac "Out-of-scope defects found" item 1 |

**Gate:** plan, then execute autonomously in your own worktree and PR. If R0-02 is not merged on `origin/main`,
STOP: this PR must be cut after it.

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" log --oneline origin/main | grep -m1 -i 'apikey' # confirm R0-02 merged (its commit names apikey writers)
git -C "$REPO" worktree add "$REPO/.worktrees/r0-03-admin-scopes" -b fix/r0-03-admin-key-scopes origin/main
cd "$REPO/.worktrees/r0-03-admin-scopes"
git rebase origin/main
npm ci --prefix web
```

## Goal

The admin checks decide by role **name** (`slices.Contains(user.Roles, "admin")`) and ignore the scopes of the API
key that authenticated the request, so a key an admin scoped to one atom passes every admin-gated route not also
behind the credential guard (7 `admin/debug` routes, including `PATCH …/books/:id` and `…/edits/:edit_id/undo`),
and a key scoped to `users.manage` passes `mergeActor` on the two merge reads that are not credential routes,
`GET /users/:id/footprint` and `GET /users/merges` (05-rbac "Out-of-scope defects found" item 1).

**The rule (05-authn §11.1 item 4):** refuse an `api_key` method whose **non-empty** scopes do not include
**every atom**, in `RequireAdmin`, `adminRole` and the in-handler checks `handlers/user.go:285,453` and the
user-merge actor check `mergeActor` (`internal/server/handlers/user_merge.go:62-75`, its `adminRole` test at `:67`).
Requiring `auth.Can(users.manage)` instead is **not** enough: an admin's key scoped to `users.manage` passes it and
keeps the debug-edit bypass. A key with **empty** scopes holds all of its owner's role permissions
(`middleware/auth.go:405-408`) and so still passes for an admin owner; a session caller is unchanged.

"Every atom" is `auth.All()` (`internal/auth/permissions.go:91-106`, 13 atoms at HEAD).

## Files

| Action | Path |
|---|---|
| modify | `internal/server/middleware/auth.go` (new helper; `RequireAdmin`) |
| create | `internal/server/middleware/auth_admin_scope_test.go` (package `middleware`) |
| modify | `internal/server/handlers/user.go` (the checks at `:285` and `:453`) |
| modify | `internal/server/handlers/user_merge.go` (`mergeActor`, `:67`) |
| create | `internal/server/handlers/user_merge_scope_test.go` (package `handlers_test`) |
| create | `changelog.d/<date>_r0_admin_key_scopes.md` (no header) |
| tick | the entry "SECURITY (hotfix, early fix 4)" (step 7) |

`adminRole` (`internal/server/wire_admin_role.go:76-81`) returns `servermiddleware.RequireAdmin()` whenever auth is
on, and `adminGate` (`:85-90`) does the same, so fixing `RequireAdmin` fixes `adminRole` without editing that file;
the test proves it on an `adminRole` route.

## Re-verify before editing (expected at the base commit, after R0-02)

R0-02 edits only `handleAPIKeyAuth` in `middleware/auth.go`, so the `RequireAdmin` lines may have moved by a few
lines; the hit counts must match.

```bash
grep -n '^func RequireAdmin() gin.HandlerFunc' internal/server/middleware/auth.go        # expect 1 hit (522 at 748fd787a)
grep -n 'slices.Contains(user.Roles, "admin")' internal/server/middleware/auth.go        # expect 1 hit
grep -n 'slices.Contains(actor.Roles, adminRole)' internal/server/handlers/user.go        # expect 2 hits: 285, 453
grep -n 'slices.Contains(actor.Roles, adminRole)' internal/server/handlers/user_merge.go  # expect 1 hit: 67
grep -n 'servermiddleware.RequireAdmin()' internal/server/wire_admin_role.go              # expect 2 hits: 80, 88
grep -n '^func CurrentAPIKey\|^func CurrentAuthMethod' internal/server/middleware/auth.go  # expect 2 hits
grep -n '^func All() \[\]Permission' internal/auth/permissions.go                         # expect 1 hit: 91
grep -rn 'RequireAdmin()' internal/server --include=*.go | grep -v _test.go | wc -l       # expect 5: middleware/auth.go (definition), admindebug/handler.go:135, wire_system_routes.go:33 (a comment), wire_admin_role.go:80 and :88
```

## Steps

1. **Tests first** (below); both named tests must fail on the base (except the one `:285` assertion noted in the table).
2. **Helper** in `internal/server/middleware/auth.go`, next to `RequireAdmin` (name coined here):

   ```go
   // APIKeyMayActAsAdmin reports whether the request's credential may use the
   // admin role. A request authenticated by an API key whose scopes are non-empty
   // must hold every atom (auth.All()); a key with empty scopes holds all of its
   // owner's role permissions and passes; any other method passes. It fails
   // closed: an api_key request with no key on the context is refused
   // (05-authn §11.1 item 4).
   func APIKeyMayActAsAdmin(c *gin.Context) bool {
   	if CurrentAuthMethod(c) != auth.MethodAPIKey {
   		return true
   	}
   	key, ok := CurrentAPIKey(c)
   	if !ok {
   		return false
   	}
   	if len(key.Scopes) == 0 {
   		return true
   	}
   	for _, p := range auth.All() {
   		if !slices.Contains(key.Scopes, string(p)) {
   			return false
   		}
   	}
   	return true
   }
   ```

   (`auth.Permission` is a string type; keep the conversion as written.)
3. **`RequireAdmin`.** Pass only when `slices.Contains(user.Roles, "admin") && APIKeyMayActAsAdmin(c)`. A key that
   fails the scope rule gets 403 with the message `"admin role required: this API key's scopes do not include every permission"`;
   keep the existing 401 (no user) and 403 (`"admin role required"`) branches unchanged. Update the doc comment.
4. **`handlers/user.go`.** At `:285` (`inviteTokenVisible`) the expression becomes
   `ok && actor != nil && actor.Status == "active" && slices.Contains(actor.Roles, adminRole) && servermiddleware.APIKeyMayActAsAdmin(c)`.
   At `:453` (`SetUserEmail`) add `|| !servermiddleware.APIKeyMayActAsAdmin(c)` to the refusal condition, same 403
   message.
5. **`mergeActor`** (`user_merge.go:67`): add `|| !servermiddleware.APIKeyMayActAsAdmin(c)` to the refusal condition,
   same 403 message ("merging accounts needs the admin role"). Update the function comment: the role **and** the
   credential's scopes decide.
6. **Run the gate.** If an existing test in `internal/server/` relied on a scoped key passing an admin route, STOP and
   report it (it is the defect).
7. **TODO bookkeeping.** `grep -n 'SECURITY (hotfix, early fix 4)' TODO.md todo.d/2026-11-04_platform_core_followups.md`;
   tick that entry in `TODO.md` if it is there, otherwise in the fragment.
8. **Changelog fragment** (no header): "Security: admin-only routes honour API-key limits."
9. **Headers:** bump version and `last-edited` on the three modified Go files; new test files get a fresh header.

## Code contract

```go
// internal/server/middleware (name coined here)
func APIKeyMayActAsAdmin(c *gin.Context) bool
func RequireAdmin() gin.HandlerFunc // unchanged signature; also refuses a narrowly scoped key
```

## Tests to write first (spec names copied exactly)

| Test | File | Proves |
|---|---|---|
| `TestLegacyRequireAdmin_HonoursKeyScopes` | `internal/server/middleware/auth_admin_scope_test.go` | An admin user (`Roles: ["admin"]`) authenticated by an API key scoped to one atom (`["library.view"]`) gets **403** on a `RequireAdmin` route; the same key gets 403 through the gate `adminRole` hands out (build it as `wire_admin_role.go:80` does: `RequireAdmin()` with auth on); a key whose scopes list every atom of `auth.All()` passes both; a key with empty scopes passes; a session caller passes; a non-admin with an every-atom key gets 403. Set the gin context the way `handleAPIKeyAuth` does: `c.Set("auth_user", user)`, `c.Set("auth_api_key", key)`, and `auth.WithMethod(ctx, auth.MethodAPIKey)` on the request context. Also cover both `handlers/user.go` sites through their exported entry points if they can be reached from package `middleware` without an import cycle; if they cannot (handlers imports middleware), cover them in `user_merge_scope_test.go` below with the same four credentials and say so in the PR. |
| `TestUserMerge_ScopedAdminKeyRefused` | `internal/server/handlers/user_merge_scope_test.go` | The HEAD-code half of the test 05-authn §11.1 names (F4 re-points it onto core). Reuse `newMergeRig` (`user_merge_test.go:70`) for the store, users and handler, and register `GET /users/merges` and `GET /users/:id/footprint` on a new `gin.New()` router with a test middleware that binds `r.admin` with an API key: a key scoped to `["users.manage"]` → 403 on both reads; an every-atom key → not 403 (200, or the handler's normal answer); also `PUT /users/:id/email` (`SetUserEmail`, `user.go:453`) → 403 with the `users.manage` key, and the invite-token visibility at `user.go:285` hides an emailed invite's token from that key. Note: at HEAD the `:285` site already hides tokens from every API key through `CredentialChangeAllowed` (`credential_guard.go:28-30`), so that one assertion passes on the base too; the scope check is still added there because 05-authn §11.1 item 4 names the site. Every other assertion must fail on the base. |

## Invariants

1. Every role-name admin check named in 05-authn §11.1 item 4 calls `APIKeyMayActAsAdmin`
   (grep: `grep -c 'APIKeyMayActAsAdmin' internal/server/handlers/user.go` = 2, `user_merge.go` = 1,
   `middleware/auth.go` ≥ 2).
2. A key that lacks any atom never passes an admin check; an empty-scope key and a session behave as at HEAD.
3. With auth disabled, `adminRole` still passes everything (unchanged `wire_admin_role.go:77-79`).

## Acceptance

```bash
go build ./...                                                                          # exit 0
go test -race -count=1 ./internal/server/middleware/ -run 'TestLegacyRequireAdmin_HonoursKeyScopes'   # ok
go test -race -count=1 ./internal/server/handlers/ -run 'TestUserMerge_ScopedAdminKeyRefused'         # ok
go test -race -count=1 ./internal/server/middleware/ ./internal/server/handlers/ ./internal/server/ ./internal/server/handlers/admindebug/   # ok
grep -c 'APIKeyMayActAsAdmin' internal/server/handlers/user.go                         # expect 2
grep -c 'APIKeyMayActAsAdmin' internal/server/handlers/user_merge.go                   # expect 1
make ci; echo "exit $?"                                                                # expect exit 0
```

The e2e suite and every required check of 09 §9.2.1 are green on the PR on an up-to-date branch.

## Out of scope

- `Can(core:admin)` with key scoping and the ported `TestAdminChecks_HonourKeyScopes` (R2, F4; 05-rbac §4.6).
- Invite-role bounding and the `users.manage` credential reach (05-rbac "Out-of-scope defects found" items 2–3).
- `internal/server/wire_admin_role.go` (no edit needed, see Files).

## Rollback

Revert the commit in a new PR. No stored data changes.

## Repo rules

- File headers bumped on every modified file; new files get a header; fragments carry none.
- Conventional commit, e.g. `fix(auth): admin checks honour API-key scopes`, ending
  `Co-Authored-By: Claude <noreply@anthropic.com>`. No release or flip trailer.
- `gh pr merge <n> --rebase` after every required check is green; never squash, never `--admin`.
- Remove the worktree after merge.
