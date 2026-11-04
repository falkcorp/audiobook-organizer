<!-- file: docs/agent-tasks/platform-core/PC-10-04-internal-cfaccess-extension.md -->
<!-- version: 1.0.0 -->
<!-- guid: 376a85a7-bf89-4dc9-8f46-844a57e3c914 -->
<!-- last-edited: 2026-11-04 -->

# PC-10-04 — internal/cfaccess: owner proof and CF Access glue as an auth.Extension; client_trust's Access branch moves in

**ID:** PC-10-04 · **Wave:** W10 (S4 r1, dark) · **Priority:** P1 · **Effort:** M
**Tier:** T2 Sonnet/high — a package consolidation with security-relevant glue (owner proof, Access assertion
verification, the fail-open binder); gin keeps calling it until the r2 flip, so behaviour must not change.
**Depends on:** PC-10-03 (`internal/oauth` now holds only `resolve.go` and `cfaccess.go`).
**Stage-Release trailer:** `Stage-Release: S4 1` on every commit.
**Normative digest:** `docs/plans/platform-core/digests/S4.md` (moves table rows `internal/cfaccess`; the consolidation
PR's injected interfaces).
**Owning spec:** 04 §2.4 (`internal/cfaccess` row: sources, `layerOf` = 5, the injected interface for `internal/config`
and the injected func for `internal/plugins/maintenance`), 04 §8.6 (gate E importer cuts); 05-authn §3.3 (`Extension`),
§4.8 (owner assurance), §11 table; 05-http-api §8.3 (Cloudflare Access binder row), §8.4 (`client_trust.go` row).

## ⛔ START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-pc-10-04" -b agent/pc-10-04-cfaccess origin/main
cd "$REPO/../audiobook-organizer-pc-10-04"
git rebase origin/main
git worktree list
npm ci --prefix web
```

## Goal

Create the ABO package `internal/cfaccess` (layer 5) holding, unchanged in behaviour:

- the Access JWT verifier from `internal/oauth/cfaccess.go` (`CFAccessVerifier`, `NewCFAccessVerifier`,
  `CFAccessHeader`, `ErrNonIdentityAssertion`, `NonIdentityAssertionError`, `CFAssertionCommonName`);
- the `CFAssertionVerifier` interface from `internal/server/middleware/absauth.go`;
- the gin binder and authenticator from `middleware/cfaccess.go`, the service-token bouncer from `middleware/cfbounce.go`,
  the owner guard from `middleware/owner_guard.go`;
- the owner proof from `internal/auth/owner.go` (`WithAccessEmail`, `AccessEmailFromContext`, `OwnerProofWhyNot`,
  `IsOwnerEmail`);
- `client_trust.go`'s Access-assertion branch (05-http-api §8.4: "at S4 its Access-assertion branch moves into ABO's
  `internal/cfaccess` and the rest is deleted" — the deletion is the r2 flip's; here only the branch moves);
- an `auth.Extension` value (05-authn §3.3) wrapping the authenticator, **built but not registered**: the flip
  (PC-11-02) passes it through `app.Options.Authenticators`; until then gin keeps calling the binder.

Lower-layer importers stop importing the owner proof directly: `internal/config`'s update service takes the owner-proof
check and the access-email reader as one injected interface, and `internal/plugins/maintenance` takes its owner check as a
func injected by `internal/books` (04 §2.4, MOD4-18, MOD7-4).

## Exact files

Create: `internal/cfaccess/` (`verifier.go`, `binder.go`, `bounce.go`, `owner.go`, `owner_guard.go`, `trust.go`,
`extension.go`, tests moved with their code).
Modify: `internal/auth/owner.go` (deleted after the move, or left as a thin forwarder only if a lower-layer importer
cannot be cut in this PR — then STOP and report instead), `internal/server/middleware/cfaccess.go`,
`internal/server/middleware/cfbounce.go`, `internal/server/middleware/owner_guard.go`, `internal/server/client_trust.go`,
`internal/arch/layering_test.go` (`layerOf["internal/cfaccess"] = 5`).
Also required (Planner notes): `internal/oauth/cfaccess.go` (moves), `internal/server/middleware/absauth.go`
(`CFAssertionVerifier`), `internal/config/update_service.go`, `internal/plugins/maintenance/repairs_ops.go`,
`internal/books/module.go` (the injected func), and every importer the listing below prints.

## Re-verify (expected at the plan checkout `f07f516be`; rerun at base)

```bash
grep -n '^func WithAccessEmail\|^func AccessEmailFromContext\|^func OwnerProofWhyNot\|^func IsOwnerEmail' internal/auth/owner.go   # 4 hits
grep -n '^type CFAssertionVerifier interface' internal/server/middleware/absauth.go    # 1 hit (:53)
grep -n '^func CloudflareAccessAuth\|^func NewCFAccessAuthenticator\|^func NewCFAccessAuthenticatorWithVerifier\|^func CFAccessVerified\|^func MarkCFAccessVerified' internal/server/middleware/cfaccess.go   # 5 hits
grep -n '^func RequireOwner\|^func OwnerPublicHost' internal/server/middleware/owner_guard.go   # 2 hits
grep -n 'servermiddleware.CFAccessVerified(c) && local' internal/server/client_trust.go         # 1 hit (the Access branch, ~:812)
grep -rln 'auth\.\(WithAccessEmail\|AccessEmailFromContext\|OwnerProofWhyNot\|IsOwnerEmail\)(' --include=*.go internal cmd | wc -l   # 10 files (config, plugins/maintenance, server/handlers/repairs, server/middleware)
ls internal/oauth/                                         # cfaccess.go resolve.go resolve_test.go resolve_verified_email_test.go (after PC-10-03)
grep -n 'layerOf\[' internal/arch/layering_test.go | head -3   # the table you extend
test -d internal/cfaccess && echo EXISTS-STOP || echo absent   # absent
grep -n 'type Extension struct' -r core/auth/                  # 1 hit (PC-10-02)
```

## Steps

1. Move the code with `git mv` where a whole file moves, so history follows; split files where only part moves
   (`absauth.go`'s interface, `client_trust.go`'s branch). Keep every exported name; callers switch to the
   `cfaccess.` qualifier.
2. Layer cuts (04 §2.4): define in `internal/config` an interface (name coined here: `OwnerProof` with
   `WhyNot(ctx, ownerEmail, host string) string` and `AccessEmail(ctx) string`) that `internal/books` sets at
   construction from `internal/cfaccess`; define in `internal/plugins/maintenance` a func field (`IsOwnerEmail func(claim,
   owner string) bool`, name coined here) that `internal/books` injects. No package at layer < 5 imports
   `internal/cfaccess` (`internal/arch/layering_test.go` proves it).
3. `extension.go`: an `auth.Extension{Authenticator, Spec, Order, OwnerSet}` built from the authenticator; Method spec
   per 05-authn §4.8 (owner verifier); exported constructor (name coined here: `cfaccess.Extension(...)`). Not wired.
4. `internal/oauth`: after `cfaccess.go` leaves, the package holds only the resolver; keep the path (PC-10-03's notes).
5. Run the moved tests from their new package; no test assertion changes.
6. Headers, `changelog.d/` fragment, acceptance.

## Code contract

Exported names keep their HEAD signatures, re-homed: `cfaccess.CFAccessVerifier`, `cfaccess.NewCFAccessVerifier(ctx,
teamDomain, aud string) (*CFAccessVerifier, error)`, `cfaccess.CFAssertionVerifier`, `cfaccess.CloudflareAccessAuth(a
*CFAccessAuthenticator) gin.HandlerFunc`, `cfaccess.RequireOwner(...)` (HEAD's parameters), `cfaccess.OwnerProofWhyNot(ctx
context.Context, ownerEmail, host string) string`, `cfaccess.IsOwnerEmail(claim, ownerEmail string) bool`. The extension
satisfies 05-authn §3.3 `Extension`.

## TDD

Done-check (the row): the existing CF Access and owner-guard tests pass from the new package — at the plan checkout
these include `internal/server/middleware/cfaccess_verified_test.go`, `cfbounce_test.go`, the owner-guard tests,
`internal/server/owner_routes_test.go`, `cf_recovery_test.go` and `internal/server/handlers/repairs/owner_apply_test.go`.
Add `TestCFAccessExtensionSpec` (name coined here): the extension's `Spec` marks it an owner verifier and its
`Authenticate` returns the same identity the gin binder binds for a fixture assertion.

## Invariants (STOP conditions)

- Behaviour-identical: the fail-open binder still binds nothing on an invalid assertion and never blocks a request.
- `layerOf` stays acyclic; if any importer below layer 5 cannot be cut with an injected interface or func, STOP.
- Nothing registers the extension with core auth in this PR.

## Acceptance

```bash
go build ./... && (cd core && go build ./...)                      # exit 0
go test ./internal/cfaccess/... ./internal/server/... ./internal/config/... ./internal/plugins/maintenance/... ./internal/arch/... -count=1   # ok
grep -rn '"github.com/falkcorp/audiobook-organizer/internal/cfaccess"' --include=*.go internal/config internal/plugins internal/repairs   # no output
test -f internal/oauth/cfaccess.go || echo moved                   # moved
test -f internal/auth/owner.go || echo moved                       # moved
make ci                                                            # exit 0
```

## Out of scope

Registering the extension and removing the gin binder (PC-11-02); deleting the rest of `client_trust.go` (PC-11-02);
the CF route files that stay in `internal/server` (`cf_recovery.go`, `guest_service_tokens.go`,
`invite_guest_access.go`, `owner_routes.go`, 04 §2.4).

## Rollback

Revert: code moves only.

## Repo rules

Headers; `changelog.d/` fragment. Conventional commits with `Stage-Release: S4 1` and
`Co-Authored-By: Claude <noreply@anthropic.com>`. `gh pr merge <n> --rebase`.

## Planner notes

- **Missing files in the row:** `internal/oauth/cfaccess.go` (the verifier this package is built from),
  `internal/server/middleware/absauth.go` (`CFAssertionVerifier`, 04 §2.4), `internal/config/update_service.go` and
  `internal/plugins/maintenance/repairs_ops.go` (the importers 04 §2.4 says the consolidation PR cuts) and
  `internal/books/module.go` (which injects the func). `internal/books/module.go` is also edited in W10 by PC-10-05;
  the plan's serialized order puts PC-10-05 (step 3) before PC-10-04 (step 4), but this row's depends-on lists only
  PC-10-03, so the order on that file is not encoded; add PC-10-05 to this row's depends-on.
