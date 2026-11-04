<!-- file: docs/agent-tasks/platform-core/PC-04-01-route-manifest-collision-rule.md -->
<!-- version: 1.0.0 -->
<!-- guid: cc0c946a-de38-43af-8520-d6794c19ac93 -->
<!-- last-edited: 2026-11-04 -->

# PC-04-01 — Route manifest generator, the ServeMux collision rule and the MountLegacy wrapper, tested against the real gin engine

| Field | Value |
|---|---|
| ID | PC-04-01 |
| Tier | **T4 Opus/xhigh–max** — without the collision rule S2 r1 cannot boot (gin accepts route pairs that `net/http.ServeMux` refuses with a panic); a wrong fold or a missing post-match check lets a request be judged on one route and served by another |
| Wave | W04 (stage S2, release `S2r1`), serialized step 1; it is the first content of S2 r1 and its real-engine test gates the flip PC-04-07 |
| Depends on | OWNER-03 (the `S1` soak row) |
| Trailer | `Stage-Release: S2 1` |
| Spec | 05-http-api §4.8 (the `ServeMux` table, the route collision rule, "The operations component"), §8.1 S2 row, §8.2 (`MountLegacy` contract, admission, wrapper), §3.5 (`MatchedPattern`), §12 (test names); 05-rbac §3.1 and the §3 "Landing" paragraph; 05-authn §3 "Landing" and §3.1; 04 §8.1 S2 row (rbac and auth leaf types); 09 §9.4 S2 "HTTP" and "Done when" |
| Digest | `docs/plans/platform-core/digests/S2.md` §4.3, §5 rule 3, §8 |

**Gate.** Open and make green any time; merge only after OWNER-03's `S1` row is in `ci/soak.txt`.

**What this task delivers (dark).** Nothing in this PR changes what ABO serves: ABO still runs `server.NewServer`
and gin is still the root handler. The PR adds (a) the `rbac` and `auth` S2 leaf types to core, (b) the legacy-mount
half of `core/httpapi` — the manifest types, the collision rule and the wrapper with its post-match check — and (c) in
ABO, the recorded route manifest of the real gin engine plus the tests that register it into a real `ServeMux`.

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add ../audiobook-organizer-pc-04-01 -b feat/pc-04-01-legacy-manifest origin/main
cd ../audiobook-organizer-pc-04-01 && git rebase origin/main && git worktree list
make web-deps
```

Never run `go work init` (CLAUDE.md).

## Exact files

| Action | Path | Holds |
|---|---|---|
| create | `core/rbac/requirement.go` | the S2 leaf of 05-rbac §3.1/§3.3/§3.4: `Permission`, `ParsePermission`, `ParsePattern`, its three methods, `CoreAdmin`, `Requirement` (+`IsZero`, `Permissions`, `String`), `Public`, `AnyAuthenticated`, `Need`, `AllOf`, `AnyOf`, `Delegated`, `LegacyAtom`, `LegacyCheck`, `RoleName`, the `Checker` interface |
| create | `core/auth/principal.go` | the S2 leaf of 05-authn §3.1: `UserID`, `UserStatus` (+4 consts), `User`, `Method` (+5 consts, `Interactive`, `MayChangeCredentials`), `Assurance` (+consts), `Principal`, `MethodSpec` (05-authn §3.3 struct) |
| create | `core/httpapi/legacy.go` | `LegacyMount`, `LegacyRoute`, `LegacyAuth` (`LegacyAuthGin`, `LegacyAuthCore`), `LegacyRedirect`, `Guards`, `RateClass` (+4 consts), `MatchedPattern`, the wrapper and the post-match helper |
| create | `core/httpapi/manifest.go` | gin path → mux pattern translation, manifest validation (`ErrLegacyManifest` input checks), registration into a `*http.ServeMux` |
| create | `core/httpapi/collision.go` | conflict graph, kept/folded split, generalised pattern, rules 1–5 of §4.8, `ErrPatternConflict` |
| create | `core/httpapi/collision_test.go` | `TestCollisionRule_RefusesUnmergeableSets` and unit tests |
| create | `core/httpapi/legacy_test.go` | wrapper and post-match unit tests on a fixture `http.Handler` |
| create | `internal/server/legacy_manifest.go` | ABO's recorder: `s.perm`, the ABS auth middleware, the API rate limiter, `recordCredRoute`/`recordAdminRoute` data, `BasicExempt`/`NoCompress`/`Public` tables → `iter.Seq[httpapi.LegacyRoute]`; the gin post-match middleware; two exported accessors (names coined here) `func (s *Server) Engine() http.Handler` (the gin engine) and `func (s *Server) LegacyRoutes() (iter.Seq[httpapi.LegacyRoute], error)` (the manifest, or `ErrLegacyManifest`), which `books.Module` (PC-04-05) and the series contract test (PC-04-06) call |
| create | `internal/server/legacy_manifest_test.go` | the four real-engine tests below |
| modify | `internal/server/server_lifecycle.go` | `func (s *Server) perm(...)` records its atom (also when auth is off) |
| modify | `internal/server/server.go` | one line in `NewServer` installing the post-match gin middleware first (see Planner notes) |
| create | `changelog.d/<date>_pc_04_01_legacy_manifest.md` | headerless fragment |

## Re-verify (counts at `f07f516be`; rerun at your base)

```bash
grep -n 'func (s \*Server) perm(' internal/server/server_lifecycle.go                    # 1 (~1693)
grep -rn 's\.perm(' --include=*.go internal/server | grep -v _test | wc -l              # 417
grep -rln 's\.perm(' --include=*.go internal/server | grep -v _test | wc -l             # 21 files
grep -n 'func (s \*Server) recordCredRoute' internal/server/credential_routes.go       # 1 (~47)
grep -n 'func (s \*Server) recordAdminRoute' internal/server/wire_admin_role.go        # 1 (~63)
grep -n 'RedirectTrailingSlash = false\|RedirectFixedPath = false' internal/server/server.go   # 2 (~639-640)
grep -n 'protected.GET("/operations/v2/:id"' internal/server/wire_operations_routes.go # 1 (~27)
grep -n 'protected.GET("/operations/:id/\(result\|changes\)"' internal/server/wire_operations_routes.go  # 2 (~103,110)
grep -n 'protected.GET("/operations/:id/activity"' internal/server/wire_library_routes.go           # 1 (~48)
grep -n 'protected.GET("/operations/:id/results"' internal/server/server_lifecycle.go               # 1 (~1952)
grep -n 'r.GET("/api/me/item/:id/remove-from-continue-listening"\|r.GET("/api/me/item/listening-sessions/:id"' internal/server/handlers/abs/handler.go  # 2 (~871,885)
grep -n 'gzip.WithExcludedPaths\|gzip.WithExcludedPathsRegexs' internal/server/server.go          # 2 (~1604-1605)
grep -n 'func setupCredGuardServerWith\|func setupTestServer(' internal/server/*_test.go            # >= 2
ls core/rbac core/auth core/httpapi 2>&1 | head                                                    # absent before this PR (or httpapi/ holds only PC-04-02's files)
head -1 core/go.mod                                                                                 # module github.com/falkcorp/mediacore (Q-H)
```

**STOP and report** if any edit-target grep returns 0, if the five conflict-pair routes are not all present, or if
`core/rbac` or `core/auth` already exist with different contents.

## Steps

1. **Leaf types.** Write `core/rbac/requirement.go` and `core/auth/principal.go` with exactly the signatures of
   05-rbac §3.1 (and `RoleName` from §3.3, `Checker` from §3.4) and 05-authn §3.1 (plus `MethodSpec` from §3.3),
   restricted to the S2 list of 05-rbac §3 "Landing" and 05-authn §3 "Landing". Nothing else of those packages lands:
   no catalog, `Store`, `Service`, `WithChecker`, `Require`, `Identities`, `Admin`, `Failure`, `WithPrincipal`.
   `LegacyAtom(atom)` and `LegacyCheck(atom, readSide)` only **record** (05-rbac §3.1: "The constructor only records
   the atom and is an S2 leaf"). `Requirement.String()` renders as the spec's comment shows (`"legacy(settings.manage)"`).
   Both packages import only the standard library (`auth` also imports `rbac`). `auth.User` and `auth.Principal`
   name `rbac.RoleName`/`rbac.Checker`; keep every field the spec lists.
2. **Translation (`manifest.go`).** gin → mux per §4.8: `:name` → `{name}`, `*name` → `{name...}`, `/` → `/{$}`;
   method prefixed (`"GET /api/v1/x/{id}"`). Exact patterns only for manifest routes (gin runs with
   `RedirectTrailingSlash=false`, `RedirectFixedPath=false`).
3. **Collision rule (`collision.go`), §4.8 rules 1–5, per build variant:**
   - Rule 1: register every route pair-wise (or detect conflicts with the same semantics Go uses; the test asserts
     agreement with real `ServeMux` registration), take connected components of "conflicts with"; a component with
     more than one owner → `ErrPatternConflict` (at every stage, no exemption).
   - Rule 2: a route is **kept** when gin routes every overlap with each partner to it (static segment beats
     parameter at the same depth); all others are **folded** (members).
   - Rule 3: members register as **one** pattern: their shared path with each differing segment replaced by a
     wildcard named by its 1-based segment position (`GET /api/v1/operations/{p4}/{p5}`,
     `GET /api/me/item/{p4}/{p5}`); every kept route must be strictly more specific than it; it must conflict with
     no other route; different segment counts or a `{name...}` member → `ErrPatternConflict`.
   - Rule 4: members must agree in `Perm` (same `rbac.LegacyAtom` atom), `Guards`, `Public`, `RateClass`, `OwnAuth`
     with `AuthMethod`, `BasicExempt`, `NoCompress`; any difference → `ErrPatternConflict`.
   - Every `ErrPatternConflict` names the routes involved.
   - Conflicts the rule does not resolve are converted from the `ServeMux` panic text with `recover()` into
     `ErrPatternConflict`; nothing panics out of registration (§4.8 first row).
4. **Wrapper and post-match (`legacy.go`), §8.2 and §4.8 rule 5:** each registered pattern's handler is a wrapper
   that (a) stores the matched pattern (as registered, method included) in the request context, readable through
   `MatchedPattern(ctx) (string, bool)`; (b) clears `r.Pattern` before calling the mount's handler (otelgin v0.72.0
   falls back to `r.Pattern` for `http_route` when gin matched nothing, §8.2); (c) calls the mount handler. The
   post-match decision is a pure helper the ABO gin middleware calls after gin routed (name coined here:
   `func LegacyMemberMatch(ctx context.Context, method, fullPath string) (ok bool, inMount bool)`): outside a mount
   request it reports `inMount=false`; inside, `ok` is true only when `fullPath` (gin's `c.FullPath()`, gin form)
   is the pattern's own manifest entry or one of its members. `core/httpapi` imports no gin package.
   Stage 2's route carrier and its "mark before gin" step are PC-05-01's; leave a `// stage 2 marks the carrier here
   (PC-05-01)` comment at the call.
5. **Mount entry point.** If PC-04-02's `core/httpapi/router.go` is at your base, add
   `func (r *Router) MountLegacy(m LegacyMount) error` (05-http-api §3.5) that validates and records the mount
   (collision rule, `ErrLegacyManifest`, `ErrPrefixCollision` per §3.5 last paragraph) for `Build`. If it is not,
   expose the unexported `registerLegacy(mux *http.ServeMux, m LegacyMount) error` with the same behaviour and leave
   the method to PC-04-02 (its brief has the mirror instruction). Do not create `router.go`.
6. **ABO recorder (`internal/server/legacy_manifest.go`).** Produce one `httpapi.LegacyRoute` per gin route:
   - `Perm`: `rbac.LegacyAtom(atom)` for every route behind `s.perm(atom)` (record in `perm` **before** the
     auth-off early return, so the manifest is the same with `EnableAuth` false); `rbac.AnyAuthenticated` for the
     permission-less authenticated routes (36 at the evidence commit; the test counts them at your base); `rbac.Public`
     for the reviewed public routes (`Public: true`); `rbac.Delegated(...)` with `OwnAuth: true` and
     `AuthMethod: "books.abs"` for routes behind `ABSRequireAuth`.
   - `Guards`: `Interactive` from `credRoute`/`recordCredRoute`, `Owner` from the `RequireOwner` routes.
   - `RateClass`: `RateDefault` iff the route's gin chain includes the `/api/v1` rate limiter, else `RateExempt`.
   - `BasicExempt`: exactly `/api/health` and `/api/v1/health` (05-http-api §8.2).
   - `NoCompress`: exactly HEAD's `compressionMiddleware` exclusions that are routes (`/api/events`, `/metrics`,
     `/api/v1/auth/accept-invite`, the `^/api/v1/operations/v2/[^/]+/logs/download$` route).
   - Recording mechanism is your design, constrained by §8.2: extend the existing recording pattern and compare with
     `(*gin.Engine).Routes()`; a route without an entry, or an entry without a route, is `ErrLegacyManifest`. It
     runs at registration time, never by probing requests at boot.
   - The gin post-match middleware (installed first in `NewServer`): when `LegacyMemberMatch` says
     `inMount && !ok`, abort with 404 in the legacy error shape (ABO's existing `internal/httputil` error envelope),
     before any handler. Outside a mount it is a pass-through, so HEAD's behaviour is unchanged.
   - If PC-04-06 has merged a stand-alone `internal/server/engine_export.go` with the same `Engine()` accessor,
     delete that file in this PR and keep `Engine()` in `legacy_manifest.go` (same name and signature).
7. Write the tests first (next section), then implement until green. Bump headers; add the changelog fragment.

## Code contract (exact, 05-http-api §8.2 and §3.5)

```go
type LegacyMount struct {
	Owner      string
	Handler    http.Handler
	Routes     iter.Seq[LegacyRoute]
	Auth       LegacyAuth
	ServesSPA  bool
	DefaultCSP func(*http.Request) string
	CSPReport  string
	Redirects  []LegacyRedirect
	TopSegments []string
	SPAShared  []string
	BuildID    string
	BasicExemptAssets func(path string) bool
}

type LegacyRoute struct {
	Method, GinPath string
	Perm            rbac.Requirement
	Guards          Guards
	Public          bool
	OwnAuth         bool
	BasicExempt     bool
	AuthMethod      string
	RateClass       RateClass
	NoCompress      bool
}

type Guards struct{ Interactive, Owner bool }            // §3.1
type RateClass uint8                                     // RateDefault, RateAuth, RateExpensive, RateExempt (§3.1)
func MatchedPattern(ctx context.Context) (pattern string, ok bool)
func (r *Router) MountLegacy(m LegacyMount) error        // only if router.go exists at your base (step 5)
```

Errors: `ErrPatternConflict`, `ErrLegacyManifest` (names fixed by §4.8, §8.2). `LegacyRedirect`'s fields are not
fixed by the spec; give it `From, To string` (coined here) and keep it unused by this PR.

## TDD tests to write first (names fixed by 05-http-api §12)

- `TestLegacyManifest_RegistersAsMuxPatterns` (`internal/server`) — over `srv.router.Routes()` of the real engine,
  built twice: with `setupTestServer` (ABS off) and with `setupCredGuardServerWith` setting `ABSAPIEnabled = true`
  (and a synthetic `ABSJWTSecret`). Never a fixture. Asserts: every gin route is a mux pattern or a member of one
  generalised pattern; registration raises no conflict; `GET /api/v1/operations/{p4}/{p5}` is present (and, ABS on,
  `GET /api/me/item/{p4}/{p5}`); the kept routes `GET /api/v1/operations/v2/{id}` and
  `GET /api/me/item/listening-sessions/{id}` are their own patterns; for each conflicting pair, Go's overlap example
  (for example `/api/v1/operations/v2/activity`) is routed by the mux to a pattern holding gin's route. Also the
  `ErrLegacyManifest` drift half (`TestLegacyManifest_MatchesGinRoutes`): add a gin route without a record → error.
- `TestCollisionRule_RefusesUnmergeableSets` (`core/httpapi`) — fixtures: a component spanning two owners; members
  differing in each of `Perm`, `Guards`, `RateClass`, `OwnAuth`, `BasicExempt`, `NoCompress`; members of different
  segment counts; a kept route not strictly more specific than the generalised pattern; a generalised pattern that
  conflicts with another route → each `ErrPatternConflict`, naming the routes.
- `TestLegacyMount_GeneralisedPatternAcceptsAnyMember` (`internal/server`, books' real engine) — each member of
  `GET /api/v1/operations/{p4}/{p5}` reaches its gin handler; `/api/v1/operations/x/y` → 404 legacy shape with no
  handler run; `/api/v1/operations/v2/activity` reaches `GetOperationV2`.
- `TestS2Manifest_SPermRoutesCarryLegacyAtom` (`internal/server`) — every entry recorded from an `s.perm(atom)` route
  has `Perm == rbac.LegacyAtom(atom)`, built with no catalog, with auth on and with auth off; the manifest is accepted
  for a `LegacyAuthGin` mount. Its half "`Handle` refuses a `LegacyAtom`/`LegacyCheck` requirement" needs PC-04-02's
  `Handle`: include it if `Handle` is at your base, else leave a `t.Skip("Handle lands in PC-04-02")` that PC-04-02
  removes (its brief says so).
- `TestLegacyMount_MuxAndGinAgreeOnEncodedSlash` (`internal/server`) — a request with `%2F` in a segment whose gin
  match differs from its mux pattern → 404, no gin handler runs.
- Unit tests in `core/httpapi` (names coined here): `TestMatchedPattern_SetInsideWrapperAndPatternCleared` (inside
  the mount handler `MatchedPattern` is set and `r.Pattern == ""`), `TestTranslateGinPath` (table of the three rules).

## Acceptance

```bash
(cd core && go build ./... && go vet ./rbac/ ./auth/ ./httpapi/)               # exit 0
(cd core && go test ./rbac/ ./auth/ ./httpapi/ -count=1)                         # ok for each package
go test ./internal/server/ -run 'TestLegacyManifest_|TestCollisionRule_|TestLegacyMount_|TestS2Manifest_' -count=1 -v 2>&1 | grep -E '^(--- |ok|FAIL)'
#   expect: --- PASS lines for TestLegacyManifest_RegistersAsMuxPatterns (both variants as subtests),
#           TestLegacyManifest_MatchesGinRoutes, TestLegacyMount_GeneralisedPatternAcceptsAnyMember,
#           TestS2Manifest_SPermRoutesCarryLegacyAtom, TestLegacyMount_MuxAndGinAgreeOnEncodedSlash; final "ok"
go test ./internal/server/ -count=1                                              # ok (nothing served changed)
grep -rn 'gin-gonic' core/httpapi core/rbac core/auth | wc -l                    # 0
make ci                                                                          # exit 0
make -C core ci                                                                  # exit 0
```

## Invariants and STOP conditions

- No ABO behaviour change: `server.NewServer` stays the root handler; the gin post-match middleware is a
  pass-through outside a mount request. Any existing `internal/server` test that changes result → STOP.
- The manifest is identical with auth on and off (perm recording precedes the auth-off return).
- A conflict set never spans two owners (rule 1). If the real engine shows a conflicting pair other than the five of
  §4.8 that the rule cannot fold (members disagree in a rule-4 field), STOP and report the pair: the spec's fix is
  a reviewed rule change, not a local exemption.
- `core/httpapi`, `core/rbac` and `core/auth` import nothing from ABO (`TestCoreImportsOnlyCore`, S0).
- CLAUDE.md concurrency: the conflict graph is pairwise over about 572 routes per build at the evidence commit, once
  per boot; a serial O(n²) pass over that size is well under a second, so no worker pool is wanted here. If you
  measure more than one second at your base, shard the outer loop with a bounded pool and say so in a comment.

## Out of scope

`Router`, `Build`, `Handle`, `Infra`, problem rendering (PC-04-02); the perimeter stages and stage 2's carrier
(PC-05-01); mounting the engine through `books.Module()` and the `LegacyAuthGinUntil` admission (PC-04-05, PC-04-07);
`TestOwnAuthRoutesGolden`, `TestLegacyAuthGin_RefusedWithoutOption` (admission lands with the kernel).

## Rollback

Revert the PR. Nothing is persisted; ABO's serving path is unchanged.

## Repo rules

Worktree per task off fresh `origin/main`; 4-line headers on every new or changed file (bump version, keep guid);
headerless `changelog.d/` fragment; conventional commit (`feat(httpapi): legacy route manifest and ServeMux
collision rule`) whose body ends with `Stage-Release: S2 1` and `Co-Authored-By: Claude <noreply@anthropic.com>`;
`gh pr merge <n> --rebase` after the required checks pass on an up-to-date branch and the gate is open; never squash;
never `--admin` past a required check; CLAUDE.md concurrency rules as above.

## Planner notes

- Same-package parallelism: PC-04-01 and PC-04-02 run in the same serialized step but both create files in
  `core/httpapi` (types such as `Guards`, `RateClass` and the method `(*Router).MountLegacy` sit on the seam). This
  brief assigns `Guards`, `RateClass`, the legacy types and the collision code to PC-04-01, and `Router`, `Route`,
  `Build`, `Handle` to PC-04-02, with a conditional for `MountLegacy` (step 5). Serializing PC-04-01 before PC-04-02
  would remove the conditional.
- PC-04-02's `core/module` (`Registrar.AuthMethod(spec auth.MethodSpec)`) and `core/httpapi` `Route.Perm`
  (`rbac.Requirement`) need this PR's `core/auth` and `core/rbac` leaves, so PC-04-02 effectively depends on
  PC-04-01; the row lists both as depending only on OWNER-03.
- The row omits `internal/server/server.go`, which this brief edits by one line to install the post-match gin
  middleware before any route registers (gin applies `engine.Use` only to routes registered after it).
