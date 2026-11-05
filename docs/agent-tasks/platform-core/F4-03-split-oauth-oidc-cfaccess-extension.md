<!-- file: docs/agent-tasks/platform-core/F4-03-split-oauth-oidc-cfaccess-extension.md -->
<!-- version: 1.1.0 -->
<!-- guid: 6d906762-e7f8-4bbc-b7e3-b7cbceb0b389 -->
<!-- last-edited: 2026-11-05 -->

# F4-03 — Split `internal/oauth`: OIDC/OAuth into `core/auth/oidc`, and Cloudflare Access into the new ABO package `internal/cfaccess` as an `auth.Extension`

| Field | Value |
|---|---|
| ID | F4-03 |
| Release / flip | R2, identity flip F4 (dark PR: ABO's gin chain and routes keep HEAD's behaviour until F4-08) |
| Tier | **T3** (Opus, high): an OIDC move with the native-app hand-off and an owner-proof extension |
| Wave | W27 (runs inside an announced **move window**: no other PR of the wave touches `internal/oauth/`, the `internal/server/` files listed below, `internal/auth/owner.go`, `go.mod`, `core/go.mod` or the shared `layerOf` and shard files) |
| Depends on | F4-02 |
| Gate | Plan, then execute autonomously in your own worktree and PR. |

## ⛔ START HERE

```bash
REPO=<your clone>
SLUG=platform-core-f4-03-oauth-split
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/$SLUG" -b "agent/$SLUG" origin/main
cd "$REPO/.worktrees/$SLUG"
git rebase origin/main
make web-deps
git rev-parse HEAD       # BASE
```

Confirm F4-02 is merged and the move window is announced. If not, stop.

## Goal

1. Move `internal/oauth/{oauth,providers,resolve,state}.go` (with their tests) into `github.com/falkcorp/mediacore/auth/oidc`
   and make it 05-authn §4.7's generic OIDC: discovery through `github.com/coreos/go-oidc/v3` v3.21.0 and
   `golang.org/x/oauth2` v0.37.0 (both added to `core/go.mod`), a `github` kind, PKCE S256 for every provider, the signed
   state codec (`state.go:55-132` at HEAD) keyed by `secrets.DeriveKey("mediacore/auth/oidc-state/v1")` with a 10 min
   TTL and carrying the native-app hand-off fields, linking by `(provider, subject)` with the email-link guard, the
   verified-email displacement rule under the same guard and audited `core.identity.displace`, admission by allowlist and
   the `ExtraAllowed` invite hook, and the callback's `AppHandoff.Mint` branch. `resolve.go` stops importing
   `internal/database` and `internal/logger`: it uses `auth.Store` and a passed `*slog.Logger` (04 §8.5 F4 rows).
2. Create the ABO package `internal/cfaccess` (04 §2.4) consolidating `internal/oauth/cfaccess.go`,
   `internal/server/middleware/{cfaccess,cfbounce,owner_guard}.go`, `internal/auth/owner.go` and the
   `CFAssertionVerifier` interface from `internal/server/middleware/absauth.go`; expose it as an `auth.Extension`
   (`Spec: MethodSpec{Method: "ext:cfaccess", Interactive: true, OwnerVerifier: true}`, `OwnerSet` = an owner e-mail is
   configured; 05-authn §3.3) for F4-08 to register in `app.Options.Authenticators`, and keep the gin binder HEAD's gin
   chain still uses working until F4-08.
3. Re-point every importer of `internal/oauth` (all inside `internal/server/`, at HEAD) and delete `internal/oauth`.
   `internal/server`'s CF Access route files (`cf_recovery.go`, `guest_service_tokens.go`, `invite_guest_access.go`,
   `owner_routes.go`) **stay** in `internal/server` (04 §2.4: moving them would close a cycle).

## Spec

- 05-authn §3.3 (`Authenticator`, `Assertion`, `MethodSpec`, `OIDCAppHandoff`, `Extension`; "Core ships no identity
  extension" paragraph), §4.7 (all), §4.8 (owner assurance), §8 (the `core.auth.oidc.*` keys:
  `link_existing_by_email`, `default_role`, `redirect_base_url`, client ids and secrets, allowlists), §10 (OIDC and CF
  rows), §11 (the HEAD → destination table).
- 04 §2.4 row `internal/cfaccess`; §8.2 row `mediacore/auth/oidc`; §8.5 F4 rows (the `resolve.go` cuts).
- 05-http-api §8.3 row "Cloudflare Access fail-open binder" (from F4 the authenticator runs in stage 12; this PR only
  prepares it).

## Files (the row's list)

`core/auth/oidc/`, `core/go.mod`, `core/go.sum`, `go.mod`, `go.sum`, `internal/oauth/` (deleted),
`internal/cfaccess/` (created), `internal/server/middleware/cfaccess.go`, `internal/server/middleware/cfbounce.go`,
`internal/server/middleware/owner_guard.go`, `internal/auth/owner.go` (each moved),
`internal/arch/layering_test.go` (`internal/cfaccess` enters `layerOf`; `internal/oauth` leaves it),
`scripts/ci/short_test_weights.json`, and these 25 `internal/oauth` importers (re-pointed, not the whole
`internal/server/` directory): `internal/server/`: `cf_recovery_test.go`, `invite_guest_access.go`, `invite_guest_access_test.go`, `oauth_app_code_epoch_integration_test.go`, `owner_routes_test.go`, `static_embed_recovery_test.go`, `wire_abs_routes.go`, `wire_oauth.go`; `internal/server/handlers/abs/`: `abs_test.go`, `authorize_apikey_test.go`, `openid.go`, `openid_flow_test.go`, `sliding_session_test.go`; `internal/server/handlers/`: `oauth_login.go`, `oauth_login_deeplink_test.go`; `internal/server/middleware/`: `abs_caller_key_test.go`, `absauth.go`, `absauth_apikey_test.go`, `absauth_nonidentity_test.go`, `absauth_servicetoken_test.go`, `absauth_test.go`, `absauthprobe.go`, `auth_method_test.go`, `cfaccess_verified_test.go`, `cfbounce_test.go`.

At HEAD 29 files import `internal/oauth`; `internal/server/login_ceiling.go` and its test leave with F4-02 (W25), and
`cfaccess.go`/`cfbounce.go` move here, so at BASE the importer set is exactly the files above.

## Re-verify at BASE (HEAD `f7ac5b11e` values shown)

```bash
ls internal/oauth/                                         # HEAD: cfaccess.go oauth.go oauth_test.go providers.go resolve.go resolve_test.go resolve_verified_email_test.go state.go
grep -rln '"github.com/falkcorp/audiobook-organizer/internal/oauth"' --include=*.go . | grep -v '^./internal/oauth' | wc -l   # HEAD 29 files, all under internal/server/; BASE 27 (F4-02 moved login_ceiling.go and its test)
grep -rln '"github.com/falkcorp/audiobook-organizer/internal/oauth"' --include=*.go . | grep -v '^./internal/oauth' | grep -vc '^./internal/server/'   # HEAD 0 (else STOP 1)
ls internal/server/middleware/cfaccess.go internal/server/middleware/cfbounce.go internal/server/middleware/owner_guard.go internal/auth/owner.go | wc -l   # 4
grep -c 'CFAssertionVerifier' internal/server/middleware/absauth.go    # >= 1
grep -c 'coreos/go-oidc/v3' go.mod                                      # 1
grep -c 'coreos/go-oidc/v3' core/go.mod                                 # 0 now; 1 after
grep -rn 'type Extension struct' core/auth/ | wc -l                     # 1 (F4-02)
grep -rn 'type OIDCAppHandoff interface' core/auth/ | wc -l             # 1 (F4-02)
ls -d internal/cfaccess 2>/dev/null | wc -l                             # 0
```

## STOP conditions

1. An importer of `internal/oauth` at BASE that is not in the Files list above (a new file since the plan's base, or
   one outside `internal/server/`): report before editing it.
2. A cycle `internal/server` → `internal/cfaccess` → `internal/server` (04 §2.4): keep the four CF route files in
   `internal/server`; `internal/config`'s update service takes the owner-proof check and the access-email reader as one
   injected interface. If that needs an `internal/config` edit, report (the row does not list `internal/config/`).
3. Any change in ABO's sign-in behaviour before F4-08 (the OAuth, deep-link, owner-guard and CF tests stay green
   unchanged).
4. `go list -m all` resolves different versions of go-oidc or oauth2 in the two modules (`scripts/ci/check_module_skew.py`).

## Steps

1. Run the re-verify block; write the `oidc` tests first.
2. Move the four OIDC files into `core/auth/oidc`, apply `go fix`, replace `internal/database` and `internal/logger` with
   `auth.Store` and `*slog.Logger`; add PKCE to GitHub; key the state codec off `secrets.DeriveKey`; add the
   hand-off fields to the state; implement the email-link guard and admission rules of §4.7.
3. Add go-oidc v3.21.0 and oauth2 v0.37.0 to `core/go.mod`; run `go mod tidy` in both modules; keep the versions equal.
4. Create `internal/cfaccess` from the five sources; provide `Extension()` (name coined here) returning the
   `auth.Extension`, and the gin binder ABO keeps until F4-08; `internal/server` imports `internal/cfaccess`.
5. Re-point every `internal/oauth` importer; delete `internal/oauth`.
6. Layering and shard weights; headers; one `changelog.d/` fragment; Acceptance.

## Tests first

05-authn §10: `TestOIDC_LinksBySubjectNotEmail`, `TestOIDC_PKCEForAllProviders`, `TestOIDC_UntrustedProviderCannotLinkByEmail`,
`TestCFAccess_PresentedInvalidIs401_AbsentPassesThrough`, `TestCFAccess_JWKSOutageFallsThrough`,
`TestOwnerGuard_RequiresOwnerAssurance`, `TestOwnerGate_AccessWithoutOwnerFallsBack`; the moved HEAD tests
(`oauth_test.go`, `resolve_test.go`, `resolve_verified_email_test.go`, the middleware CF tests, `owner_test.go`) pass in
their new homes; `TestOIDC_StateCarriesAppHandoffFields` (name coined here: a state minted with `redirect_uri` and an S256
`code_challenge` round-trips both fields, a tampered state is refused, a state older than 10 min is refused).
`TestOIDC_AppHandoffMintsABSCode` is F4-08's (it needs the routes mounted and the ABS redemption).

## Invariants

- Core ships no identity extension; Cloudflare Access is ABO code behind `auth.Extension`.
- An extension's assertion never raises its checker (stage 12 builds the principal).
- An unseen subject never takes an existing admin or password account by e-mail.

## Acceptance

```bash
(cd core && go vet ./auth/... && go test -race -count=1 ./auth/...)                 # ok
go test -count=1 ./internal/cfaccess/... ./internal/server/...                       # ok
ls -d internal/oauth 2>/dev/null | wc -l                                             # 0
grep -rln 'internal/oauth"' --include=*.go . | wc -l                                 # 0
python3 scripts/ci/check_module_skew.py                                              # exit 0
go test -count=1 ./internal/arch/...                                                 # ok
make -C core ci                                                                      # exit 0
make ci                                                                              # exit 0
```

## Out of scope

The core OIDC routes (F4-05); `TestOIDC_AppHandoffMintsABSCode` (F4-08); registering the extension and deleting the gin
binder and HEAD's OAuth routes (F4-08).

## Rollback

Revert the PR (a pure move plus new code; no stored data).

## Repo rules

Worktree only; file headers on every created, moved or modified file (fragments headerless); move PR rules (old ==> new
list in the description, every commit builds, `go fix`); conventional commit ending
`Co-Authored-By: Claude <noreply@anthropic.com>`, no release or flip trailer; `gh pr merge <n> --rebase`, never squash,
never `--admin`.

```
refactor(auth): move OIDC into core/auth/oidc; consolidate Cloudflare Access into internal/cfaccess

Co-Authored-By: Claude <noreply@anthropic.com>
```
