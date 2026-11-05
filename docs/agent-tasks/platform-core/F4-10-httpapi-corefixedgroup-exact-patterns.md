<!-- file: docs/agent-tasks/platform-core/F4-10-httpapi-corefixedgroup-exact-patterns.md -->
<!-- version: 1.0.1 -->
<!-- guid: 0398de9d-2bb5-4cdc-bd80-084cd51c0a37 -->
<!-- last-edited: 2026-11-05 -->

# F4-10 — `httpapi.Router.CoreFixedGroup` and `CoreExactPatterns`: the core group at a kept HEAD path, the declared exception list, and `Build`'s refusal of a fixed-group route outside it

| Field | Value |
|---|---|
| ID | F4-10 |
| Release / flip | R2, identity flip F4 (dark PR: no app registers a fixed-group route before F4-05 builds `authhttp` and F4-08 mounts it) |
| Tier | **T2** (Sonnet, high): two router members and one build-time refusal, with exact patterns the spec fixes |
| Wave | W24 |
| Depends on | OWNER-03 (NOT AGENT WORK: R1 released and deployed) |
| Owns gate tests | none of 09's list; `TestCoreExactPatterns_TwoEntries` and the router cells of `TestCoreFixedGroup_OIDCCallback` (05-http-api §12) |
| Gate | Plan, then execute autonomously in your own worktree and PR. Do not merge with `--admin` past a required check. |

## ⛔ START HERE

```bash
REPO=<your clone>
SLUG=platform-core-f4-10-corefixedgroup
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/$SLUG" -b "agent/$SLUG" origin/main
cd "$REPO/.worktrees/$SLUG"
git rebase origin/main
make web-deps
git rev-parse HEAD       # BASE
```

Confirm in the tracking issue that OWNER-03 recorded the R1 release and deploy. If not, stop.

## Goal

Add to core's router (`core/httpapi`, built in R1 by R1-13, R1-14 and R1-17) the two members of 05-http-api §3.5:

1. `func (r *Router) CoreFixedGroup(base string) *Group`: a core group rooted at a kept HEAD path. Its routes run the
   whole core chain exactly like `CoreGroup()`'s and record `RouteInfo.Owner` = `"core"`. Only
   `core/internal/authhttp` uses it (F4-05), with the base ABO supplies as `app.Options.Auth.OIDCCallbackBase`
   (`/api/v1/auth/oauth`); an app that sets no base gets no fixed group.
2. `func CoreExactPatterns() []string`: the declared list of exact core patterns allowed inside another owner's claim
   (05-http-api §4.9). It returns **exactly two** entries:
   - `GET /api/v1/auth/oauth/{provider}/callback`
   - `GET /api/v1/auth/oauth/{provider}/start`

and the build rules of §3.5 and §4.9:

- `Build` fails with `ErrPrefixCollision` when a `CoreFixedGroup` route's full pattern is not an entry of
  `CoreExactPatterns()`.
- For those two patterns alone, `Build` raises neither `ErrPrefixCollision` against books' `/api/v1/` claim nor
  `ErrShadow` against books' routes.
- `Build` still fails with `ErrPrefixCollision` when a legacy manifest route matches a path either entry matches (so
  F4-08 must delete HEAD's gin callback and start handlers in the same PR that mounts core's).

## Spec (read before coding)

- 05-http-api §3.5 (the `Router` block: `CoreFixedGroup`, `CoreExactPatterns` comments; "Two core paths outside
  `/api/core/v1`"), §4.9 (prefix claims, `ErrOutsideClaim`, `ErrShadow`, "The one exception: exact core patterns inside
  a module claim"), §5 (the error rows: `ErrPrefixCollision` covers a manifest route over a `CoreExactPatterns()`
  entry), §12 row `TestCoreFixedGroup_OIDCCallback` / `TestCoreExactPatterns_TwoEntries`.
- 05-authn §3.2 (`auth.Options.OIDCCallbackBase`), §4.13 (the callback and start served by `authhttp` on the fixed
  group).
- 03 R5 (no exported package-level slice: `CoreExactPatterns` is a function returning a fresh slice).

## Files

| Action | Path |
|---|---|
| create | `core/httpapi/corefixed.go` (`CoreFixedGroup`, `CoreExactPatterns`, the fixed-group check and the exemption predicate `Build` calls) |
| create | `core/httpapi/corefixed_test.go` |
| modify (not in the inventory row; see Planner notes) | the R1-13 file that defines `func (r *Router) Build` (and, if separate, the file holding its prefix-collision and shadow checks): **one call** into `corefixed.go`'s check and **one call** to its exemption predicate, nothing else |

## Re-verify at BASE (record every count)

```bash
ls core/httpapi/corefixed.go core/httpapi/corefixed_test.go 2>/dev/null | wc -l     # expect 0
grep -rn 'func (r \*Router) CoreGroup' core/httpapi/*.go | wc -l                    # expect 1 (R1-13)
grep -rn 'func (r \*Router) Build' core/httpapi/*.go                                # expect 1 line: note its file (BUILD_FILE)
grep -rn 'ErrPrefixCollision\s*=' core/httpapi/*.go | wc -l                         # expect 1
grep -rn 'ErrShadow\s*=' core/httpapi/*.go | wc -l                                  # expect 1
grep -rn 'func (r \*Router) Claim' core/httpapi/*.go | wc -l                        # expect 1
grep -rn 'Owner ' core/httpapi/*.go | grep -c 'RouteInfo\|string'                  # >= 1 (RouteInfo.Owner)
grep -rn 'CoreFixedGroup\|CoreExactPatterns' core/ | wc -l                          # expect 0
# where the prefix-collision and shadow checks live (expect them in BUILD_FILE or one sibling file)
grep -rln 'ErrPrefixCollision\|ErrShadow' core/httpapi/*.go | grep -v _test
```

## STOP conditions

1. Any re-verify count differs (a missing `Build`, `CoreGroup`, `Claim` or error sentinel means R1 is not where this
   brief assumes).
2. Wiring the check into `Build` needs more than the two one-line calls in the R1-13 file(s) the last grep names.
3. A third entry would be needed in `CoreExactPatterns()` (adding one is a spec change, 05-http-api §4.9).
4. Any change alters a route `Build` accepts or refuses today for a router that registers no `CoreFixedGroup` route
   (R1's router tests stay green unchanged).

## Steps

1. Write the tests below first (they fail).
2. In `corefixed.go`: `CoreExactPatterns()` returns a new `[]string` holding the two patterns above, in that order.
   `CoreFixedGroup(base)` returns a `*Group` built the way `CoreGroup()` builds its group (same chain, same `Owner`
   `"core"`, same route kind), rooted at `base` instead of `/api/core/v1`, and marks its routes as fixed-group routes
   (an unexported field or set on the router; name coined here). An empty `base` panics or returns an error the same
   way `ModuleGroup` treats an invalid ID at BASE (follow R1-13's convention; record which).
3. In `corefixed.go`, two unexported functions (names coined here): `checkCoreFixed(routes)` returns
   `ErrPrefixCollision` (wrapped with the offending pattern) for a fixed-group route whose `METHOD pattern` is not a
   `CoreExactPatterns()` entry, and for a legacy manifest route whose pattern matches a path an entry matches;
   `isCoreExact(route) bool` reports a fixed-group route that is an entry.
4. In BUILD_FILE (STOP 2): call `checkCoreFixed` from `Build` before the claim checks; in the prefix-collision and
   shadow checks, skip a route for which `isCoreExact` is true. Nothing else changes in that file. Bump its header.
5. Headers on both new files; one `changelog.d/` fragment (no header); Acceptance.

## Code contract (copy exactly; 05-http-api §3.5)

```go
func (r *Router) CoreFixedGroup(base string) *Group     // a core group at a HEAD path kept because that is cheaper; core/internal/authhttp only, from F4
func CoreExactPatterns() []string                       // exactly two entries, HEAD's OAuth callback and start
```

## Tests first (05-http-api §12 names; coined names marked)

- `TestCoreExactPatterns_TwoEntries`: `CoreExactPatterns()` equals exactly
  `[]string{"GET /api/v1/auth/oauth/{provider}/callback", "GET /api/v1/auth/oauth/{provider}/start"}`; mutating the
  returned slice does not change the next call's result (R5).
- `TestCoreFixedGroup_OIDCCallback` (the router cells; the real callback and start handlers and the `AppHandoff` cells
  arrive with F4-05's `authhttp` and F4-08's mount, whose `TestOAuthCallback_HEADPathServedByCore` and
  `TestOAuthStart_HEADPathServedByCore` cover them): a test router with a books-like legacy mount claiming `/api/v1/`
  and a stub handler registered on `CoreFixedGroup("/api/v1/auth/oauth")` at `GET /{provider}/callback` and
  `GET /{provider}/start`: `Build` succeeds with no `ErrPrefixCollision` or `ErrShadow`; a request to each path reaches
  the stub through the whole core chain with `RouteInfo.Owner` `"core"`; a fixed-group route `GET /{provider}/other` →
  `ErrPrefixCollision`; a legacy manifest route `GET /api/v1/auth/oauth/:provider/callback` beside the fixed-group
  route → `ErrPrefixCollision`.
- `TestCoreFixedGroup_NoBaseNoGroup` (name coined here): a router whose app registers no fixed-group route builds
  exactly as at BASE (same `Routes()` output as before this PR for R1's router fixtures).

## Invariants

- The only core routes outside `/api/core/v1` are the two `CoreExactPatterns()` entries.
- `CoreExactPatterns()` is a function, never an exported variable (R5).
- No exemption for any other pattern; a manifest route over an entry always fails `Build`.

## Acceptance (commands and expected output)

```bash
(cd core && go vet ./httpapi/...)                                                                  # exit 0
(cd core && go test -race -count=1 ./httpapi/...)                                                  # ok
(cd core && go test -count=1 -run 'TestCoreExactPatterns_TwoEntries|TestCoreFixedGroup_' -v ./httpapi/ 2>&1 | grep -c '^--- PASS')   # 3 (top-level tests)
(cd core && grep -rn 'CoreFixedGroup(' --include=*.go . | grep -v '_test.go' | grep -v 'httpapi/corefixed.go' | wc -l)   # 0: no caller yet
git diff --stat origin/main -- core/httpapi | tail -1                                              # 3 files changed (2 new + BUILD_FILE), or 4 if the claim checks live in a sibling file
make -C core ci                                                                                    # exit 0
make ci                                                                                            # exit 0
```

## Out of scope

The OIDC start and callback handlers (F4-05); mounting them, deleting HEAD's gin callback and start handlers and
setting `app.Options.Auth.OIDCCallbackBase` for ABO (F4-08).

## Rollback

Revert the PR. No app registers a fixed-group route before F4-05 and F4-08.

## Repo rules

- Worktree only; never edit `main` or the primary checkout; never run `go work init`.
- File header (file, version, guid, last-edited) on every created file; bump the version of BUILD_FILE and keep its
  guid. `changelog.d/` fragments carry no header.
- Conventional commits ending with `Co-Authored-By: Claude <noreply@anthropic.com>`. 09 names no release or flip
  trailer: add none.
- `gh pr merge <n> --rebase` once every required check is green on an up-to-date branch; never squash, never
  `--admin`.

```
feat(core/httpapi): CoreFixedGroup and CoreExactPatterns with Build's exact-pattern rule (dark)

Co-Authored-By: Claude <noreply@anthropic.com>
```

## Planner notes

- **`Build`'s file is not in the row.** The refusal and the two-pattern exemption are `Build` and claim-check rules
  (05-http-api §3.5, §4.9), and `Build` lives in an R1-13 file of `core/httpapi/` (name chosen by R1-13). The row lists
  only `corefixed.go` and its test, so this brief allows two one-line calls in that file (STOP 2). The row should list
  the file; no other W22 row touches `core/httpapi/`.
