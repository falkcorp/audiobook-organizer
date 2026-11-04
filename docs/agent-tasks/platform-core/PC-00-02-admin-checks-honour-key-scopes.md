<!-- file: docs/agent-tasks/platform-core/PC-00-02-admin-checks-honour-key-scopes.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4d2ef2cf-ee03-495b-9ca0-7d5eedcfedf4 -->
<!-- last-edited: 2026-11-04 -->

# PC-00-02 — Early fix 4: `RequireAdmin`, `adminRole`, the in-handler role checks and `mergeActor` honour API-key scopes

| Field | Value |
|---|---|
| ID | PC-00-02 |
| Tier | **T2 (Sonnet/high)**: one rule applied at four call sites, fully specified; an authorization change, so every site is tested |
| Wave | W00 (S0, release `S0`); live security hotfix |
| Depends on | PC-00-01 (merged first: both edit `internal/server/middleware/auth.go`) |
| Priority / effort | P0 / M |
| Branch | `fix/pc-00-02-admin-key-scopes` |
| Trailer on every commit | `Stage-Release: S0 1` |
| Digest | `docs/plans/platform-core/digests/S0.md` §1 (user-visible change 2) |
| Owning spec | `docs/specs/platform-core/05-authn.md` §11.1 item 4; `05-rbac.md` "Out-of-scope defects found" item 1 and §12 row `TestUserMerge_ScopedAdminKeyRefused`; `09-abo-on-core.md` S0 "Hotfixes" |

Implement 05-authn §11.1 item 4 exactly: **refuse an `api_key` method whose non-empty scopes do not include every
atom**, in `RequireAdmin`, in `adminRole` and in the in-handler admin-role checks (`handlers/user.go:285,453`) and
the user-merge actor check `mergeActor` (`handlers/user_merge.go:62-75`, its role test at `:67`). Requiring
`auth.Can(users.manage)` instead is **not** enough (an admin's key scoped to `users.manage` passes it).

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" log origin/main --oneline | grep -i 'pc-00-01\|early fix 6' | head -3   # PC-00-01 must be on main; else STOP
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-pc-00-02" -b fix/pc-00-02-admin-key-scopes origin/main
cd "$REPO/../audiobook-organizer-pc-00-02"
git worktree list
npm ci --prefix web          # or make web-deps once PC-01-06 has merged
```

Never run `go work init` in the checkout or the worktree (CLAUDE.md).

## Exact files

Create:

- `internal/server/middleware/auth_admin_scope_test.go`
- `internal/server/handlers/user_merge_scope_test.go` (not in the inventory row; see Planner notes)

Modify:

- `internal/server/middleware/auth.go`
- `internal/server/handlers/user.go`
- `internal/server/handlers/user_merge.go`
- `TODO.md` or `todo.d/2026-11-04_platform_core_followups.md` (check off one entry)

Not modified (see Planner notes): `internal/server/wire_master_list_routes.go`, `internal/server/wire_admin_role.go`.
`adminRole` (`wire_admin_role.go:76-81`) returns `servermiddleware.RequireAdmin()` when authentication is on and a
pass-through when it is off; the master-list group uses it (`wire_master_list_routes.go:42`). Fixing `RequireAdmin`
fixes `adminRole` and every route behind it, with the auth-off behaviour unchanged.

Add one `changelog.d/` fragment.

## Re-verify greps (expected counts at `ac4368bdb`, origin/main when this brief was written)

```bash
grep -n 'func RequireAdmin() gin.HandlerFunc' internal/server/middleware/auth.go | wc -l                 # 1 (:522)
grep -n 'slices.Contains(user.Roles, "admin")' internal/server/middleware/auth.go | wc -l                # 1 (:530)
grep -rn 'RequireAdmin()' internal --include=*.go | grep -v _test | wc -l                                # 5 (def; wire_admin_role.go:80,88; admindebug/handler.go:135; wire_system_routes.go:33 comment)
grep -n 'return servermiddleware.RequireAdmin()' internal/server/wire_admin_role.go | wc -l              # 2 (:80 adminRole, :88 adminGate)
grep -n 'adminOnly.Use(s.adminRole())' internal/server/wire_master_list_routes.go | wc -l                # 1 (:42)
grep -c 'slices.Contains(actor.Roles, adminRole)' internal/server/handlers/user.go                       # 2 (:285 inviteTokenVisible, :453 SetUserEmail)
grep -c 'slices.Contains(actor.Roles, adminRole)' internal/server/handlers/user_merge.go                 # 1 (:67 mergeActor)
grep -n 'func APIKeyRevoked' internal/server/middleware/auth.go | wc -l                                  # 1 (PC-00-01 landed)
grep -n 'func All() \[\]Permission' internal/auth/permissions.go | wc -l                                 # 1 (13 atoms)
```

A different count, or a zero on an edit target, means STOP and report.

## Steps

1. Run the re-verify greps.
2. In `internal/server/middleware/auth.go`, add the rule as one exported helper (name coined here), next to
   `CurrentAPIKey`:
   ```go
   // APIKeyScopedBelowAdmin reports whether the request is authenticated by an API key
   // whose non-empty scopes do not include every permission atom (auth.All()). Such a
   // key never passes an admin-role check, whatever its owner's roles
   // (05-authn §11.1 item 4). A key with empty scopes carries its owner's whole role set
   // (intersectPermissions) and is judged by the owner's roles, as today. An api_key
   // method with no key bound to the request fails closed.
   func APIKeyScopedBelowAdmin(c *gin.Context) bool {
       if CurrentAuthMethod(c) != auth.MethodAPIKey {
           return false
       }
       key, ok := CurrentAPIKey(c)
       if !ok {
           return true
       }
       if len(key.Scopes) == 0 {
           return false
       }
       for _, p := range auth.All() {
           if !slices.Contains(key.Scopes, string(p)) {
               return true
           }
       }
       return false
   }
   ```
3. In `RequireAdmin` (same file), after the `!ok || user == nil` 401 branch and **before** the role test, add:
   ```go
   if APIKeyScopedBelowAdmin(c) {
       httputil.RespondWithForbidden(c, "an API key scoped below admin cannot use an admin route")
       c.Abort()
       return
   }
   ```
   Update the doc comment above `RequireAdmin`: it checks the `admin` role **and** that an API key's non-empty
   scopes include every atom.
4. In `internal/server/handlers/user.go`:
   - `:285` (`inviteTokenVisible`): append `&& !servermiddleware.APIKeyScopedBelowAdmin(c)` to the returned
     expression.
   - `:453` (`SetUserEmail`): append `|| servermiddleware.APIKeyScopedBelowAdmin(c)` inside the refusal condition,
     keeping the same 403 message.
   - Update the comment on the `adminRole` constant (`:438-441`) to say an API key scoped below admin is refused too.
5. In `internal/server/handlers/user_merge.go` `mergeActor` (`:67`): append
   `|| servermiddleware.APIKeyScopedBelowAdmin(c)` inside the refusal condition, keeping the 403 message. Update the
   doc comment above `mergeActor` (`:62-64`) the same way.
6. Do not wire `RequireAdmin` anywhere new (`internal/server/admin_routes_test.go` fails when it is wired outside
   `adminGate`/`adminRole`).
7. Check off the TODO entry: `grep -n 'hotfix, early fix 4)' TODO.md todo.d/*.md`, change its `- [ ]` to `- [x]`.
8. Changelog fragment `changelog.d/<YYYY-MM-DD>_pc-00-02-admin-key-scopes.md` under `### Security`: "admin-only
   routes honour API-key limits" (09 S0 release-note text), naming `RequireAdmin`, `adminRole`, the two
   `handlers/user.go` checks and `mergeActor`.
9. Bump headers on every changed Go file; new files get a 4-line header with a fresh guid.

## Code contract

```go
// internal/server/middleware (new; name coined here)
func APIKeyScopedBelowAdmin(c *gin.Context) bool
```

Unchanged: `func RequireAdmin() gin.HandlerFunc`, `func (s *Server) adminRole() gin.HandlerFunc`, the handler
signatures. Invariants:

- I1: an `api_key` request whose key has non-empty scopes missing any atom of `auth.All()` gets 403 from
  `RequireAdmin` (so from every `adminRole`/`adminGate` route), from `mergeActor`, from `SetUserEmail`, and sees no
  invite token from `inviteTokenVisible`.
- I2: an admin's session, an admin's key with empty scopes and an admin's key whose scopes hold every atom pass as
  today; a non-admin is refused as today; auth-off behaviour of `adminRole` is unchanged.

## TDD: tests to write first

`internal/server/middleware/auth_admin_scope_test.go` (package `middleware`):

- **`TestRequireAdmin_ScopedKeyRefused`** (name coined here; the inventory's "a key without every atom is refused by
  `RequireAdmin`"). Router `gin.New()`, a setter middleware that does `c.Set(contextUserKey, user)`, optionally
  `c.Set(contextAPIKeyKey, key)`, and `c.Request = c.Request.WithContext(auth.WithMethod(ctx, method))`, then
  `RequireAdmin()`, then a 200 handler. Cases (all with an `admin` user unless stated):
  - session → 200;
  - api_key, scopes `[]` → 200;
  - api_key, scopes = every atom of `auth.All()` → 200;
  - api_key, scopes `["library.view"]` → 403;
  - api_key, scopes = `auth.All()` minus `fingerprint.worker` → 403;
  - api_key method with no key on the context → 403;
  - viewer user (`Roles: ["viewer"]`), session → 403 (unchanged);
  - no user → 401 (unchanged).
- **`TestAPIKeyScopedBelowAdmin`** (name coined here): unit table over the helper (same cases as above, plus
  `MethodABS` and `MethodSessionDelegated` → false).

`internal/server/handlers/user_merge_scope_test.go` (package `handlers_test`):

- **`TestUserMerge_ScopedAdminKeyRefused`** (the spec's name, legacy half; 05-rbac §12): build `r := newMergeRig(t)`
  (`user_merge_test.go:70`). Register two extra routes on `r.router` whose setter binds `r.admin` as `auth_user`,
  sets `auth_api_key` to `&database.APIKey{ID: "k", UserID: r.admin.ID, Scopes: []string{"users.manage"}}` and
  records `auth.MethodAPIKey` with `auth.WithPermissions(ctx, []auth.Permission{auth.PermUsersManage})`:
  `GET /k/users/:id/footprint` → `r.h.UserFootprint` and `GET /k/users/merges` → `r.h.ListUserMerges`. Assert:
  - both answer 403 for that key (on HEAD they answer 200: this is the reachable bypass);
  - the same routes with a key whose scopes are `auth.All()` answer 200;
  - the rig's own session routes (`/users/<absorbed>/footprint`, `/users/merges`) answer 200 for `r.admin`.
  (The merge POSTs are credential routes in production, `wire_library_routes.go:177-181`, and refuse any key there;
  that is covered by `internal/server/credential_routes_test.go`, not here.)
- **`TestSetUserEmail_ScopedAdminKeyRefused`** (name coined here): `r.h.SetUserEmail` on a gin context bound as the
  key above (`users.manage` only), body `{"email":"x@example.com"}` → 403 and the stored email unchanged; with an
  admin session → not 403.

## Acceptance

```bash
go build ./...                                                                              # exit 0
go test -race -count=1 -run 'TestRequireAdmin|TestAPIKeyScopedBelowAdmin' ./internal/server/middleware/   # ok
go test -race -count=1 -run 'TestUserMerge|TestSetUserEmail|TestUserMergeRoutes' ./internal/server/handlers/   # ok
go test -race -count=1 -run 'TestAdminRoutes|TestCredential' ./internal/server/                  # ok (route classification unchanged)
grep -rn 'APIKeyScopedBelowAdmin(c)' internal/server --include=*.go | grep -v _test | wc -l   # 4 (RequireAdmin, user.go x2, user_merge.go)
make ci                                                                                    # last line: ✅ All CI checks passed!
```

Run the two new handler/middleware test files on the unfixed tree first and record that the key cases fail.

## Out of scope

- From S4 the merge handler checks `Can(core:admin)` with key scoping (05-rbac §4.6); not here.
- Changing which routes are admin-gated, the auth-off rule of `wire_admin_role.go`, or `s.perm`.
- The invite role bound (05-rbac "Out-of-scope defects" item 2) and `users.manage` reach (item 3).

## Rollback

Revert the PR; binary swap. No stored state changes.

## Repo rules

- Headers bumped on every changed file; new files get fresh guids; fragments headerless.
- Conventional commit, e.g. `fix(auth): admin-role checks refuse an API key scoped below admin (early fix 4)`, ending:
  ```
  Stage-Release: S0 1
  Co-Authored-By: Claude <noreply@anthropic.com>
  ```
- `gh pr create`, wait for the required checks, `gh pr merge <n> --rebase`; never squash, never `--admin`.
- Remove the worktree after merge.

## Planner notes

- The inventory row lists `internal/server/wire_master_list_routes.go` as modified. At HEAD `adminRole` lives in
  `internal/server/wire_admin_role.go:76-81` and delegates to `servermiddleware.RequireAdmin()`; the master-list
  group only calls it (`wire_master_list_routes.go:42`; 05-rbac §6.3 cites that line). The fix in `RequireAdmin`
  covers it, so neither file needs an edit. The row's file list should drop it.
- The row's done-check `TestUserMerge_ScopedAdminKeyRefused` (legacy half) lives in package `handlers_test`; the
  row creates no handlers test file. This brief adds `internal/server/handlers/user_merge_scope_test.go`.
