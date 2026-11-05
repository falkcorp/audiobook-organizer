<!-- file: docs/agent-tasks/platform-core/R1-14-mount-legacy-manifest-redirect.md -->
<!-- version: 1.1.2 -->
<!-- guid: 48b212d2-c9e7-4980-9fb5-ec04de724c03 -->
<!-- last-edited: 2026-11-05 -->

# R1-14 — httpapi.MountLegacy with the same-owner collision rule, books' legacy route manifest (every gin route a mux pattern), and the /api/* redirect with the native-prefix exclusion

| Field | Value |
|---|---|
| ID | `R1-14` |
| Tier | **T3** (Opus / high): the ServeMux collision rule over 572 real routes, ABS on and off |
| Release / wave | R1 / W11 |
| Depends on | R1-13 (each done before you start: a task merged on `main`, an OWNER checkpoint recorded by the owner) |
| Spec | 05-http-api §4.8 (collision rule), §8.1 R1 row, §8.2; 09 §9.2.4; 09 §9.4 R1 Content: endpoints |
| Branch | `agent/platform-core-r1-14-mount-legacy-manifest-redirect` |

**Gate:** plan, then execute autonomously in a worktree with its own PR. Every merge passes `make ci`, e2e and the named required checks of 09 §9.2.1 that exist at your base. The R1 freeze is on from R1-35 (unless OWNER-01 overrode it) until OWNER-03 cuts R1 (09 §9.2.1): nothing in this task changes that.

**Files the inventory assigns to this task** (a path ending in `/` is a whole directory; touch nothing outside this list without recording it under the PR's "Scope notes"):

- `core/httpapi/legacy.go`
- `core/httpapi/legacy_test.go`
- `internal/server/legacy_manifest.go`
- `internal/server/legacy_manifest_test.go`
- `internal/server/server_lifecycle.go`
- `internal/server/testdata/`

## START HERE

```bash
REPO=<your clone>   # the audiobook-organizer checkout you work from
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/platform-core-r1-14" -b agent/platform-core-r1-14-mount-legacy-manifest-redirect origin/main
cd "$REPO/.worktrees/platform-core-r1-14"
git rebase origin/main
npm ci --prefix web        # CLAUDE.md: Playwright and vitest from this worktree
```

Never run `go work init` in the checkout or the worktree (CLAUDE.md; 03 §3.2 item 1): verify with `go build ./...` and `go -C core build ./...` (once `core/` exists). Line numbers below were read at the planning base `f7ac5b11e`; the spec's anchors are at `748fd787a`. Where they differ the brief says so. Re-run every grep at your base before editing; a count that differs only because a listed predecessor merged is expected, and the brief says what to do with it.

## Goal

Give core the permanent legacy mount and give books its reviewed route manifest (05-http-api §8.1 R1 row, §8.2
`MountLegacy` contract, §4.8 the `ServeMux` table and the route collision rule; 09 §9.2.4; 09 §9.4 R1 "Content:
endpoints"; 03 R6):

1. **`httpapi.MountLegacy`** in `core/httpapi/legacy.go` per 05-http-api §8.2: mounts a legacy `http.Handler`
   (ABO's gin engine) behind core's router; each manifest route registers as its own `ServeMux` pattern, except
   that each connected set of **same-owner** routes that conflict as patterns registers as **one generalised
   pattern**, and only when every member carries the same `Perm`, `Guards`, `Public`, `RateClass`, `OwnAuth` with
   its `AuthMethod`, `BasicExempt` and `NoCompress` (03 R6; 05-http-api §4.8 rule 4); any other conflicting set,
   and any set holding routes of two owners, fails manifest generation. The legacy handler wrapper carries the
   matched pattern into gin (`httpapi.MatchedPattern(ctx)`), and after gin routed, a request whose `c.FullPath()` is
   not that pattern's manifest entry (for a generalised pattern, one of its members) gets 404 in the legacy error
   shape before any gin handler runs (§4.8 last row). The mount carries HEAD's `/api/*` → `/api/v1/*` 301 through
   its `Redirects`.
2. **Books' legacy route manifest** in `internal/server/legacy_manifest.go`: every gin route at your base, with
   its translated pattern (`:name` → `{name}`, `*name` → `{name...}`, `/` → `/{$}`), owner, and permission:
   `rbac.LegacyAtom(atom)` for every `s.perm(atom)` route (R1-15 resolves it through books' atom table), and
   `rbac.AnyAuthenticated` for the permission-less authenticated routes (36 at `748fd787a`; 05-http-api §8.1 R1:
   "R6 must hold from R1"), `rbac.Public` for the public ones. Count routes at your base (572 at `748fd787a`), ABS on
   and off.
3. **The redirect exclusion**: HEAD's redirect (`internal/server/server_lifecycle.go:1790-1822` at `f7ac5b11e`,
   the same lines 09 §9.1 cites at `748fd787a`) additionally excludes
   `^/api/(core|[a-z][a-z0-9]{1,15})/v[0-9]+(/|$)` (09 §9.2.4; 09 §9.4 R1). Every existing exclusion stays.

## Tests this task owns (spec names)

- `TestLegacyManifest_RegistersAsMuxPatterns`: on the **real engine** built by `internal/server`, ABS on and off,
  every gin route of the engine is in the manifest and every manifest route registers in a `ServeMux` through
  `MountLegacy` without conflict (09 §9.4 R1 Verification).
- `TestNativePrefixesNotRedirected`: `/api/core/v1/x`, `/api/hello/v1/x`, `/api/books/v1/x` are not 301-redirected.
- `TestCollidingNamespacesStillRedirect`: a path that only looks versioned but fails the pattern (for example
  `/api/v1x/...`, `/api/A/v1/...`, `/api/toolongnamespacexx/v1/...`, `/api/core/vx/...`) still redirects exactly
  as HEAD does; the ABS-reserved and other existing exclusions behave unchanged.
- Also from 05-http-api §12 for the mount: `TestCollisionRule_RefusesUnmergeableSets`,
  `TestLegacyMount_GeneralisedPatternAcceptsAnyMember`, `TestLegacyMount_MuxAndGinAgreeOnEncodedSlash`,
  `TestLegacyManifest_MatchesGinRoutes`, `TestOwnAuthRoutesGolden`, `TestPublicRoutesGolden` (golden files under
  `internal/server/testdata/`). `TestManifest_SPermRoutesCarryAtomRequirement` is R1-15's (it needs the atom table).
  `archtest.RoutesHavePermissions` over the legacy manifest is R1-37's (it reads the manifest this row writes); it is
  not a test of this row.
- Build-time refusals of the mount (05-http-api §12; each one line from the spec row):
  `TestOutsideClaim_Refused` (a legacy route outside its mount's `LegacyPrefixes`, for example at
  `GET /api/core/v1/auth/oidc/google/callback`, gives `ErrOutsideClaim`), `TestBuild_CrossOwnerShadowRefused` (one
  owner's pattern strictly more specific than another's gives `ErrShadow`; SEC3-8),
  `TestBuild_PublicNotInManifestRefused` (a module native route with `rbac.Public` that is absent from
  `Manifest.PublicRoutes` gives `ErrPublicNotAllowed`; its pair `TestPublicRoutesGolden` is above; COR3-3) and
  `TestLegacyAuthGin_RefusedWithoutOption` (`LegacyAuthGin` without `Options.LegacyAuthGinUntil`, and an `OwnAuth`
  route without a registered auth method, are boot errors; its pair `TestOwnAuthRoutesGolden` is above; SEC2-17).
- Kernel-side rules of the mount (06 §6.10; each one line from the spec row): `TestSPAOwner` (a `ServesSPA` legacy
  mount owns `/` and `ui.Handler` is absent; two owners fail boot; FE-1; `TestSPA_SingleOwnerOfRoot` for `Router.SPA`
  is R1-13's) and `TestLegacyAuthGinOnlyNamedModule` (`LegacyAuthGin` from a module other than the one named by
  `Options.LegacyAuthGinUntil` fails boot; SEC2-17; F4-08 deletes the option and this test with it).

## Dependency check

R1-13 merged: `test -d core/httpapi && test -f core/openapi/core.json && echo ok`.

## Re-verify (expected at `f7ac5b11e`)

```bash
sed -n 1790,1791p internal/server/server_lifecycle.go       # expect the "Redirect /api/* to /api/v1/*" comment and s.router.Use(
grep -n 'absReservedPath(path)\|absCollisionDetailReserved' internal/server/server_lifecycle.go   # expect the two existing ABS exclusions
grep -n 'RedirectTrailingSlash\|RedirectFixedPath\|UseRawPath' internal/server/server.go   # expect the three gin settings 05-http-api §4.8 cites (server.go:637-641)
grep -rn 's\.perm(' internal/server/*.go | wc -l             # record the count: every one must appear as LegacyAtom in the manifest
ls internal/server/testdata/                                 # expect op_ids.golden series_prune.golden write_op_modes.golden
```

## Steps

1. Tests first (red), with goldens generated from the real engine at your base.
2. `core/httpapi/legacy.go`: the mount, the collision rule, the pattern hand-off, the post-gin `FullPath` check.
   Keep `LegacyAuth` an option whose only R1 value is `LegacyAuthGin` (05-http-api §8.1: allowed until F4 through
   `app.Options.LegacyAuthGinUntil`); stage 12/13 enforcement is F4's and must not run for the mount in R1.
3. `internal/server/legacy_manifest.go`: generated or hand-written, reviewed; the test is what keeps it equal to the
   engine. A route whose permission you cannot determine from the code: STOP and list it (R6: every route names
   its permission; nothing is guessed).
4. The redirect exclusion in `server_lifecycle.go` (one added condition with a comment citing 09 §9.2.4).
5. Header bumps; fragment `changelog.d/<today>_legacy_mount_manifest.md` (`### Changed`: "Paths of the form
   `/api/<name>/v<N>/…` are reserved for versioned APIs and are no longer redirected to `/api/v1/`." — the R1
   release-note line of 09 §9.4 R1). Commit: `feat(httpapi): MountLegacy, books' legacy route manifest, versioned-prefix redirect exclusion`.

## Code contract

`httpapi.MountLegacy` and `LegacyRoute` exactly as 05-http-api §8.2 defines them; `httpapi.MatchedPattern(ctx) string`.

## Acceptance

```bash
go -C core test ./httpapi/ -run 'Legacy|Collision' -count=1                     # expect ok
go test ./internal/server/ -run 'TestLegacyManifest_RegistersAsMuxPatterns|TestNativePrefixesNotRedirected|TestCollidingNamespacesStillRedirect|TestLegacyManifest_MatchesGinRoutes|TestOwnAuthRoutesGolden|TestPublicRoutesGolden' -count=1 -v   # expect every one PASS
make test-e2e                                                                    # expect green (the mount is not yet serving; nothing changes for the SPA)
make ci && make -C core ci                                                       # expect exit 0
```

## STOP conditions

- A conflict set that is not mergeable under rule 4 (the advisor found five conflicting pairs at `748fd787a`, all
  same-owner; a new one at your base that fails the rule is a lead decision, not a manifest edit).
- Any route that would need a weaker permission in the manifest than gin enforces today.

## Out of scope

Wiring the mount into ABO's `main` (R1-15); the perimeter (R1-17); the atom table (R1-15); stage 13 enforcement (F4).

## Rollback

Revert the PR; the redirect returns to HEAD's exclusions.

## Repo rules (every PR of this workstream)

- **File headers.** Every new file gets the four-line header (`file`, `version: 1.0.0`, a fresh `guid` from `python3 -c 'import uuid;print(uuid.uuid4())'`, `last-edited`); every modified file gets its patch version bumped and `last-edited` set to today, keeping its `guid`. A moved file keeps its `guid` and gets its `file:` line set to the new path. `changelog.d/` and `todo.d/` fragments carry no header (CLAUDE.md).
- **Changelog.** Add one headerless fragment under `changelog.d/` (`scriv create`, or by hand); `changelog-check.yml` requires it. Do not edit `CHANGELOG.md`.
- **Commits.** Conventional commits (`type(scope): summary`). End every commit body with
  `Co-Authored-By: Claude <noreply@anthropic.com>`. 09 names no release or flip trailer (09 §9.2.1: there is no release-gating commit trailer), so add none.
- **PR and merge.** `git push -u origin <branch>`, `gh pr create`, then after every required check is green on an up-to-date branch `gh pr merge <n> --rebase`. Never squash. Never merge with `--admin` past a required check (14 Q-I as OWNER-01 answered it).
- **Concurrency (CLAUDE.md).** A loop over a library-scale collection with per-item I/O, hashing or subprocess work uses a bounded worker pool (`errgroup` + `SetLimit`); never unbounded goroutines.
- **No `go.work`.** Never run `go work init` in a checkout or worktree; the genproto probe runs only in a throwaway copy under `mktemp -d` (03 §3.2 item 1).
- **Status honesty.** The PR description ends with `COMPLETED: <n> — <list>`, `REMAINING: <n> — <list>`, `BLOCKED: <n> — <list>` over this brief's numbered steps.
