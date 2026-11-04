<!-- file: docs/agent-tasks/platform-core/PC-10-06-s4-head-reach-oracles.md -->
<!-- version: 1.0.0 -->
<!-- guid: eca2b5d4-0e46-455f-9c02-cee61baceb7b -->
<!-- last-edited: 2026-11-04 -->

# PC-10-06 — S4 HEAD oracles, test first: TestLegacyRoleReachUnchanged as a permanent gate, the role-name inventory and the settings-atom check

**ID:** PC-10-06 · **Wave:** W10 (S4 r1, dark) · **Priority:** P2 · **Effort:** L
**Tier:** T3 Opus/high — the generated reach matrix is the strongest guard of S4 (advisor G1 risk 6); it must be
recorded from gin's real router before the engine changes, and it stays a permanent CI gate.
**Depends on:** PC-10-01 (`rbac` types for the catalog-facing half), PC-10-03 (the oauth move; this PR adds files in
`internal/server`, ordered after it by the collision matrix).
**Stage-Release trailer:** `Stage-Release: S4 1` on every commit.
**Normative digest:** `docs/plans/platform-core/digests/S4.md` (Behaviour rules 5, 13, 20, 24; Done-when).
**Owning spec:** 09 §9.2.6 (the invariant, the recorded changes, the custom-role half, scoped-key cells, auth-off
cells), 09 S4 "Done when"; 05-rbac §4.6 (the role-name inventory), §6.2 (seed roles and admin-gated routes), §6.3 (reach
snapshot, row rule, in-handler list with `read_side` and `auth_off`), §12; 05-http-api §8.1, §12
(`TestLegacyRoute_SettingsAtomNeverWiderThanSuccessor`); 09 §9.2.1 (`extraction-gates`).

## ⛔ START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-pc-10-06" -b agent/pc-10-06-reach-oracles origin/main
cd "$REPO/../audiobook-organizer-pc-10-06"
git rebase origin/main
git worktree list
npm ci --prefix web
```

## Goal

1. `TestLegacyRoleReachUnchanged` in `internal/server`: for every route of the legacy mount (gin's real router; 572 =
   497 + 75 ABS at the evidence commit, generated at your base and recorded with both values in the stage ledger) ×
   the four seed roles (`admin`, `editor`, `viewer`, `fp-worker`) + scoped API keys (HEAD's role ∩ key reach) + auth-off
   (with 09 §9.2.6's recorded changes) + a custom-role half, record allow/deny into a committed golden and assert it.
   **Green against gin** in this PR; from the flip (PC-11-02) it runs against `LegacyAuthCore` unchanged, permanently.
   The custom-role half and the native-principal cells need core enforcement: they are written now and enabled by
   PC-11-02 (skip with a reason naming PC-11-02 while `LegacyAuthGinUntil` is set).
2. The role-name inventory (05-rbac §4.6): `TestRoleNameChecksUseCoreAdmin` (`internal/server/role_name_checks_test.go`)
   and `TestNoRoleNameAuthz` (`internal/arch/no_role_name_authz_test.go`, AST) as **ratchets**: at r1 the ceiling is the
   inventory count at your base and may only go down; PC-11-02 sets it to 0.
3. `TestLegacyRoute_SettingsAtomNeverWiderThanSuccessor`: the manifest half now (every manifest route gated by
   `settings.manage` or `users.manage` carries `rbac.LegacyAtom(atom)`), the custom-role 403 cells enabled by PC-11-02.
4. Wire the reach test into `extraction-gates` as a permanent gate (09 §9.2.1).
5. The frozen reach snapshot (see Planner notes): `internal/books/rbacreach/legacy_reach_v<release>.json` from the
   same real-router dump plus the reviewed in-handler list.

## Exact files

Create: `internal/server/legacy_role_reach_test.go`, `internal/server/role_name_checks_test.go`,
`internal/arch/no_role_name_authz_test.go`; goldens under `internal/server/testdata/role_reach/` (path coined here).
Modify: `.github/workflows/extraction-gates.yml`.
Also (Planner notes): `internal/books/rbacreach/legacy_reach_v<release>.json`.

## Re-verify (expected at the plan checkout `f07f516be`; early fix 4, PC-00-02, rewrites some sites before S4)

```bash
grep -rnE 'slices\.Contains\([a-zA-Z_.]*Roles, *("admin"|adminRole|auth\.SeedRoleAdmin)\)' --include=*.go internal cmd | grep -v _test | wc -l   # 7:
#   internal/server/middleware/auth.go:530 (RequireAdmin)      internal/server/handlers/user.go:285, :453
#   internal/server/handlers/user_merge.go:67 (mergeActor)     internal/server/handlers/repairs/handler.go:529
#   internal/usermerge/plan.go:342 (survivor), :400 (ActorIsAdmin)
grep -n 'func (s \*Server) adminRole() gin.HandlerFunc' internal/server/wire_admin_role.go    # 1 hit (:76)
grep -n 'func RequireAdmin() gin.HandlerFunc' internal/server/middleware/auth.go             # 1 hit (:522)
grep -n 'func (s \*Server) perm(' internal/server/server_lifecycle.go                        # 1 hit (the s.perm adapter; 05-http-api §8.4 :1693-1698)
grep -rn 'auth\.Can(' --include=*.go internal | grep -v _test | wc -l                        # record N: the in-handler atom sites for the snapshot's in-handler list
grep -c 'Perm[A-Z][A-Za-z]* *Permission = "' internal/auth/permissions.go                    # 13 atoms
test -f internal/server/legacy_manifest.go && echo ok                                        # PC-04-01
test -f internal/server/legacy_role_reach_test.go && echo EXISTS-STOP || echo absent         # absent
```

## Steps

1. **Router dump.** Build the real `*gin.Engine` exactly as `serve` does (ABS on and off), with a fixture store seeded
   with HEAD's seed roles (`auth.SeedRoles`) and one user per seed role, plus: an admin-owned key scoped
   `["settings.manage"]`, an admin-owned key scoped `["library.view"]`, an admin-owned key scoped
   `["fingerprint.worker"]`, an `fp-worker` user's key, a key with an empty scope list, a viewer's key scoped
   `["settings.manage"]`. Walk `(*gin.Engine).Routes()`; for each route and principal send a request with a path built
   from the pattern and record `allow` (anything but 401/403 from the auth chain) or `deny` and the status. The cell's
   outcome is decided by the auth chain alone: stub handlers behind it (the gin chain up to the handler) so handler
   errors never count.
2. **Golden** sorted by method, path, principal; regenerate with `-update`; the test generates the route count and fails
   if a route has no row or a row has no route.
3. **Auth-off half.** With auth off, assert 09 §9.2.6's recorded outcomes: `settings.manage` legacy routes 200
   (expansion); `Guards.Interactive` routes (credential, key, temp-login, restore) refused **from the flip only** —
   record HEAD's pass-through now with the expected flip value beside it, and assert the HEAD value while gin decides;
   in-handler `deny` sites keep denying (response-content cells).
4. **Custom-role half** (09 §9.2.6): random native roles (seeded RNG, fixed seed recorded in the test); a legacy route
   admits only if the role holds `Catalog.LegacyReach(atom)`. Skipped until `LegacyAuthGinUntil` is removed; the skip
   message names PC-11-02.
5. **Role-name ratchet.** `TestRoleNameChecksUseCoreAdmin` lists the 05-rbac §4.6 sites by file and function (not by
   line) and asserts each still exists while gin decides; its post-flip assertions (custom every-atom role passes; an
   admin's key scoped `["users.manage"]` fails the actor checks; survivor and task-actor checks read the account's
   roles) are written now and enabled by PC-11-02. `TestNoRoleNameAuthz`: AST walk over every non-test `.go` file in the
   root module; counts `slices.Contains(<x>.Roles, <"admin" | adminRole | auth.SeedRoleAdmin>)`, `== "admin"` on a role
   value and `RoleName("admin")`; fails if the count exceeds the ceiling constant (the count at your base).
6. **Settings atom.** `TestLegacyRoute_SettingsAtomNeverWiderThanSuccessor`: manifest half now; the custom role holding
   the four `settings.manage` expansion scopes getting 403 on backup restore, backup create, logs, tool install and
   plugin settings is enabled by PC-11-02.
7. **Snapshot.** From the same dump write `internal/books/rbacreach/legacy_reach_v<release>.json` (`<release>` = the
   current production release tag at your base): every legacy route with its atom, plus the reviewed in-handler list
   (every `auth.Can` site and every §4.6 role-name site, each with `file:line`, atom, `read_side`, `auth_off`
   `pass|deny`). It is a data directory, not a Go package; never regenerated from later builds (05-rbac §6.3).
8. **Workflow.** Add the reach test to `extraction-gates.yml` (`timeout-minutes` ≤ 30; shard by route range if it
   exceeds 30 minutes, 09 §9.2.10).
9. Headers, `changelog.d/` fragment, acceptance.

## Code contract

Tests and data only. Test names fixed by the spec: `TestLegacyRoleReachUnchanged`, `TestRoleNameChecksUseCoreAdmin`,
`TestNoRoleNameAuthz`, `TestLegacyRoute_SettingsAtomNeverWiderThanSuccessor`. The snapshot is read through
`rbactest.ReachSnapshot` (PC-10-01's type); match its JSON shape.

## Invariants (STOP conditions)

- Every cell is decided by gin's real chain, never by a re-implementation of HEAD's rules.
- Route count generated, not hard-coded; a count far from 572 at your base is recorded, not "fixed".
- No production code changes in this PR; if a cell cannot be recorded without changing HEAD code, STOP.

## Acceptance

```bash
go test ./internal/server -run 'TestLegacyRoleReachUnchanged|TestRoleNameChecksUseCoreAdmin|TestLegacyRoute_SettingsAtomNeverWiderThanSuccessor' -count=1 -v 2>&1 | grep -E '^(--- |ok|FAIL)'   # PASS / SKIP lines only; SKIP reasons name PC-11-02
go test ./internal/arch -run TestNoRoleNameAuthz -count=1          # ok
ls internal/books/rbacreach/                                       # legacy_reach_v<release>.json
grep -n 'TestLegacyRoleReachUnchanged' .github/workflows/extraction-gates.yml   # >= 1 hit
make ci                                                            # exit 0
```

## Out of scope

Changing any authorization behaviour; enabling the post-flip cells (PC-11-02); native routes.

## Rollback

Revert: tests and data only.

## Repo rules

Headers (the JSON snapshot gets none if the repo's JSON files carry none; check a sibling JSON under `internal/`);
`changelog.d/` fragment. Conventional commits with `Stage-Release: S4 1` and
`Co-Authored-By: Claude <noreply@anthropic.com>`. `gh pr merge <n> --rebase`. CLAUDE.md concurrency: the matrix runs
thousands of requests; run cells through a bounded pool (`errgroup` with `SetLimit(runtime.NumCPU())`), each cell with
its own recorder.

## Planner notes

- **Missing file:** 05-rbac §6.3 says the frozen reach snapshot is "committed in the S4 r1 PR" at
  `internal/books/rbacreach/legacy_reach_v<release>.json` (name open), but no inventory row lists that path. This brief
  puts it here (it comes from the same real-router dump); PC-11-02's alias table and `TestRowRuleDominatesLegacyReach`
  read it.
