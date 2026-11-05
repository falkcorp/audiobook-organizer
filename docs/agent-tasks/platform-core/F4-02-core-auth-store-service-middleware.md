<!-- file: docs/agent-tasks/platform-core/F4-02-core-auth-store-service-middleware.md -->
<!-- version: 1.1.2 -->
<!-- guid: 17675dfd-c1d9-454e-bb6e-545401beffad -->
<!-- last-edited: 2026-11-05 -->

# F4-02 — Core auth, dark: `auth.Store` native over `c/auth/`, `auth.Importer`, `authtest`, `Service.Middleware` (stage 12), hashed sessions and one-time tokens, `mck_` keys with `abk_` verification, throttle, bootstrap, `AuthCompat`, `auth/password`; the five ported early-fix property tests

| Field | Value |
|---|---|
| ID | F4-02 |
| Release / flip | R2, identity flip F4 (dark PR) |
| Tier | **T4** (Opus, xhigh–max): the authentication engine, hashed credentials and five ported security properties |
| Wave | W27 (runs inside an announced **move window**: no other PR of the wave touches the moved files, their callers listed below, or the shared `layerOf` and shard files) |
| Depends on | F4-01, F4-04, F3-01, F4-09 |
| Owns gate tests | `TestSessions_ListNeverReturnsToken`; `TestAPIKey_EmptyEffectiveKeyCannotMint`; `TestAPIKey_RevokedIsTerminal`; `TestAdminChecks_HonourKeyScopes`; `TestRevokeRacesTouch_NeverUnrevokes` |
| Gate | Plan, then execute autonomously in your own worktree and PR. |

## ⛔ START HERE

```bash
REPO=<your clone>
SLUG=platform-core-f4-02-core-auth
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/$SLUG" -b "agent/$SLUG" origin/main
cd "$REPO/.worktrees/$SLUG"
git rebase origin/main
make web-deps
git rev-parse HEAD       # BASE
```

Confirm F4-01, F4-04, F3-01 and F4-09 are merged and the move window naming the moved files and their callers is
announced. If not, stop.

## Goal

Complete `github.com/falkcorp/mediacore/auth` on R1's leaf (R1-12: `MethodSpec`, `Principal`, `User`, `UserID`,
`UserStatus`, `Assurance`) as 05-authn §3–§4 specify, dark, and move into core the HEAD files 05-authn §11 assigns to
it, re-pointing ABO's gin code at core's public API so **ABO's behaviour is unchanged**: the gin auth chain stays HEAD's
until F4-08.

- **Built on `core/internal/authz`** (F4-09): the auth-disabled principal's checker is `authz.AuthDisabled(catalog)`
  and the `_system` principal's is `authz.ForSystem(perms)`; `auth` is one of the three packages
  `archtest.NoSystemPrincipalOutsideKernel` allows to call them (05-rbac §3.4). `auth` reaches the compiled checker of
  a user or key only through `rbac.Service.Compile`.
- **Core, new:** `Service` (users, password sign-in, sessions, API keys, bootstrap, `PrincipalFor`,
  `CheckCredentialFor`, `Middleware`, `Revalidate`, `PostRestore`, `BootWrites`), authenticators and `Extension`,
  `Store` and `Importer`, the native `core/internal/authstore` over `c/auth/`, `auth/authtest` (`StoreSuite` with the
  importer cases), `auth/password`, the instance throttle, the `Compat` handle, credential formats with checksum.
- **Moved from HEAD** (04 keys, F4): `internal/server/absauth/password.go` → `core/auth/password`;
  `internal/server/absauth/throttle.go`, `internal/server/login_ceiling.go`, `bootstrap_rate_limit.go`,
  `invite_rate_limit.go` → core auth's throttle; `internal/server/absauth/audit.go` → `core/audit/auth_types.go`;
  `internal/util/email.go` → core auth (e-mail normalization, 04 §2.3 P3); `internal/config/bootstrap_key_ttl.go`,
  `internal/config/invite_config.go` → `core.auth.*` keys.

## Spec

- 05-authn §3.1–§3.4 (copy the `Service`, `Deps`, `StateFiles`, `Options`, `IssuedSession`, `SessionView`, `KeySpec`,
  `KeyStatus`, `RestoreDeps`, `Authenticator`, `Assertion`, `MethodSpec`, `OIDCAppHandoff`, `Extension`, error,
  `Store`, `SessionRecord`, `Importer` blocks exactly), §4.1–§4.6, §4.8–§4.12, §4.14 (post-restore hook), §5, §6, §7,
  §8 (keys), §9, §10 (tests), §11 ("Native store" key table; the HEAD → core table), §11.1 items 1–6 (the early fixes
  ported).
- 05-rbac §4.4–§4.7, §4.10–§4.11, §12 (`TestAdminChecks_HonourKeyScopes`).
- 05-http-api §4.2 stage 12 (stop at the first authenticator returning anything other than `ErrNoCredential`; 503
  `auth_unavailable` only).
- 05-backup §5B.3.2 (`journal.RevocationJournal`, `RestoreContext`), §5B.3.6.
- 04 §8.1 F4, §8.5 F4 (the absauth `audit.go` cut), §8.2 (rewired importers of `mediacore/auth`).
- 06 §6.4.4 (`Deps.Auth`, `AuthAdmin`, `AuthCompat` land at F4), §6.5.1 P4d steps 3–4 (`PostRestore`, `BootWrites`).
- 05-storage §5S.10 (`TestUserMergeAtomicAcrossOwners`); 05-metrics §5 item 5, §12 (`metricstest.AssertSeries` per scope).

## Files

| Action | Path |
|---|---|
| create | `core/auth/store.go`, `core/auth/service.go` (and sibling files of `core/auth/` for formats, throttle, bootstrap, compat, middleware) |
| create | `core/auth/authtest/`, `core/auth/password/`, `core/internal/authstore/` |
| move (delete in ABO, re-point callers) | `internal/server/absauth/password.go`, `internal/server/absauth/throttle.go`, `internal/server/absauth/audit.go`, `internal/server/login_ceiling.go`, `internal/server/bootstrap_rate_limit.go`, `internal/server/invite_rate_limit.go`, `internal/util/email.go`, `internal/config/bootstrap_key_ttl.go`, `internal/config/invite_config.go` |
| create | `core/audit/auth_types.go` |
| modify (re-point callers) | `internal/books/store/`: `oauth_identity.go`, `pebble_store_auth.go`, `pebble_store_invite_delivery.go`, `pebble_store_verified_email_backfill.go`, `user_merge.go` |
| modify (re-point callers) | `internal/cfaccesspolicy/`: `client.go` |
| modify (re-point callers) | `internal/oauth/`: `oauth.go`, `resolve.go` |
| modify (re-point callers) | `internal/server/absauth/`: `password_guard_test.go` |
| modify (re-point callers) | `internal/server/`: `auth_accept_invite.go`, `bootstrap.go`, `bootstrap_rate_key_test.go`, `bootstrap_security_test.go`, `client_trust.go`, `csp.go`, `csp_test.go`, `email_normalize_parity_test.go`, `invite_guest_access.go`, `login_budget_isolation_test.go`, `main_test.go`, `password_writers_algo_test.go`, `peer_allowlist.go`, `peer_allowlist_test.go`, `server.go`, `server_lifecycle.go`, `wire_abs_routes.go`, `wire_auth_routes.go`, `wire_oauth.go` |
| modify (re-point callers) | `internal/server/handlers/abs/`: `abs_test.go`, `authorize.go`, `handler.go`, `handler_dummy_warm_test.go`, `login.go`, `login_throttle_key_test.go`, `main_test.go`, `me.go`, `openid.go`, `refresh.go` |
| modify (re-point callers) | `internal/server/handlers/`: `abs_session_revoke.go`, `abs_session_revoke_test.go`, `auth.go`, `auth_dummy_warm_test.go`, `auth_login_timing_test.go`, `auth_password_algo_test.go`, `auth_saturation_test.go`, `auth_test.go`, `invite_delivery.go`, `user.go` |
| modify (re-point callers) | `internal/server/middleware/`: `absauth.go`, `absauth_test.go` |
| modify (re-point callers) | `internal/usermerge/`: `footprint.go`, `plan.go` |

These 52 caller files are every non-moved file that references a moved symbol at the plan's base (the five
`internal/books/store/` files are HEAD's `internal/database/` files after R1-26). Re-point each at core's public API in
this PR; **no forwarding shim** stays in a moved file's old location (04 §2.1: the staying side's references change in
the same PR).

## Re-verify at BASE (HEAD `f7ac5b11e` counts shown)

```bash
grep -c '^func ' internal/server/absauth/password.go          # HEAD 11 (HashPassword :131, VerifyPassword :165)
grep -c '^func ' internal/server/absauth/throttle.go          # HEAD 12
grep -c '^func ' internal/server/absauth/audit.go             # HEAD 1
grep -c '^func ' internal/server/login_ceiling.go             # HEAD 24
grep -c '^func ' internal/server/bootstrap_rate_limit.go      # HEAD 10
grep -c '^func ' internal/server/invite_rate_limit.go         # HEAD 9
grep -c '^func ' internal/util/email.go                       # HEAD 2 (NormalizeEmail, SameEmail)
grep -c '^func ' internal/config/bootstrap_key_ttl.go         # HEAD 1
grep -c '^func ' internal/config/invite_config.go             # HEAD 15
grep -rln 'absauth\.HashPassword\|absauth\.VerifyPassword\|util\.NormalizeEmail' --include=*.go . | grep -v _test | wc -l   # HEAD 17 non-test caller files (a subset of the 52 listed)
ls internal/books/store/oauth_identity.go internal/books/store/pebble_store_auth.go internal/books/store/user_merge.go | wc -l   # expect 3 (R1-26 moved them from internal/database/)
grep -rn 'func AuthDisabled\|func ForSystem\|func Compile' core/internal/authz/ | wc -l   # expect 3 (F4-09)
grep -rn 'type MethodSpec struct' core/auth/ | wc -l          # expect 1 (R1-12)
grep -rn 'func (s \*Service) Compile' core/rbac/ | wc -l      # expect 1 (F4-01)
grep -rn 'type RevocationJournal interface' core/backup/journal/ | wc -l   # expect 1 (F4-04)
grep -rn 'type Bus struct' core/events/ | wc -l               # expect 1 (F4-04)
grep -rn 'TestLegacy' internal/server/handlers/ internal/server/middleware/ internal/database/ internal/books/store/ 2>/dev/null | grep -c 'func Test'   # the six R0 tests (R0-02 … R0-05) present
```

## STOP conditions

1. ABO's behaviour changes before F4-08: every ABO test, the ABS conformance suite (`internal/server/handlers/abs/`,
   `internal/server/absauth/`) and the six `TestLegacy…` tests of R0 stay green **unchanged**.
2. Step 8's caller census at BASE names a file that is **not** in the Files table (a caller added since the plan's base):
   report it before editing; do not add a forwarding shim instead.
3. A credential or its hash appears in a log, a span, an error body or any `SessionView`.
4. Core would hold an ABO name (`abk_` is passed by ABO as `Options.ImportedKeyPrefixes`, never hard-coded).

## Steps

1. Write the tests below first.
2. **Credential formats** (§4.1): core mints `mcs_` (sessions), `mck_` (keys), `mcb_` (bootstrap), `mci_` (invites),
   `mct_` (temp-login) with prefix, length and CRC; stores only SHA-256; `ImportedKeyPrefixes` recognised by the key
   authenticator; malformed → `ErrNoCredential` vs `ErrInvalidCredentials` exactly as §4.1.
3. **Store and native store** (§3.4, §11 "Native store"): the key table of §11 (`c/auth/user/…`, `apikey/…`,
   `identity/…`, `inviteallow/…`, `session/…` `Serialized`, `onetime/…` `Serialized`); every write on the caller's
   batch; `Expect(key, seen)` + `Delete` for every `Take*`, `Expect` on the read bytes for every derived write;
   `TouchSession` and `TouchAPIKey` change only their fields and stage nothing for an absent, revoked, disabled,
   expired or epoch-stale record. `Importer` keeps every ID, the stored password hash as is and the epoch, and stores
   an imported key under the SHA-256 the app's rows hold, refusing an invariant break with `ErrImportRejected`.
4. **Service** (§3.2): every method acting on another user enforces dominance; `ChangeOwnPassword` self only,
   Interactive; status changes bump the epoch; `Login` throttled with equal timing (dummy-hash derivation kept from
   `password.go`); sessions hashed; `CreateAPIKey` requires non-empty scopes ⊆ the actor's effective set at that moment
   (early fix 2), no wildcards; `SetAPIKeyStatus` monotone toward `revoked` (early fix 3); `needs_confirmation` only to
   `active` (admin, interactive) or `revoked`; revocations append `journal.Revocation` lines into the revoking batch
   before commit; `PrincipalFor` applies status, epoch, `SignInRestricted`, role resolution and scope intersection;
   `CheckCredentialFor` refuses a `Confined()` principal outside its scopes.
5. **Middleware** (stage 12, §4.2): ordered authenticators (key 100, session 200, extensions by `Order`); stop at the
   first non-`ErrNoCredential` result; bind the principal (`rbac.WithChecker`, `audit.WithActor`) or record
   `auth.Failure`; never write a response except 503 `auth_unavailable`.
6. **Throttle** (§4.9): instance state (no globals), source and account buckets, soft delays, 429, LRU pinning; the
   login ceiling, bootstrap and invite limiters move here keeping HEAD's limits (read from `core.auth.*` keys).
7. **Bootstrap** (§4.6), **Compat** (§4.10: the method-bound handle; `VerifyPassword`, `AuthenticateKey`,
   `PrincipalFor`, `CheckCredentialFor`), **auth-off** (§4.11), **epochs** (§4.12), **PostRestore** and **BootWrites**
   (§3.2, §4.14; write nothing in `New` or a constructor). `PostRestore` is the hook body only: F4-11 builds the P4d
   step 3 runner that calls it and F4-08 registers it there.
   **Metrics** (§9): this adds the core `auth` metrics scope (`mediacore.auth`); add its `testdata/series.golden` under
   `core/auth/`, asserted by `metricstest.AssertSeries` (R1-10; 05-metrics §5 item 5).
8. **Moves:** first take the caller census at BASE: for each moved file, list its exported symbols and grep every
   `.go` file outside the moved set that references them (`grep -rlw` per symbol with the package qualifier); the union
   must equal the 52 caller files of the Files table (STOP 2 otherwise; record the census in the PR). Then move each
   listed HEAD file's code to its core home, re-point every caller at core's public API (no shim), keep HEAD's
   behaviour (limits, timings, hash parameters, error strings the ABS suite checks). Move PR rules apply (description
   lists `old ==> new`; every commit builds; `go fix` on moved files).
9. Headers, one `changelog.d/` fragment, Acceptance.

## Tests first (05-authn §10, 05-rbac §12)

The five gate tests, each a **property test** over random inputs as the spec states: `TestSessions_ListNeverReturnsToken`,
`TestAPIKey_EmptyEffectiveKeyCannotMint`, `TestAPIKey_RevokedIsTerminal`, `TestAdminChecks_HonourKeyScopes`,
`TestRevokeRacesTouch_NeverUnrevokes` (`synctest`). Also: `TestSession_TokenNeverStored`,
`TestStage12_InvalidKeyPlusValidSessionIs401`, `TestCredentialFormat_ChecksumAndPrefix` + `FuzzParseCredential`,
`TestAPIKey_PresentedKeyWins`, `TestAPIKey_CreateRequiresScopes` / `TestAPIKey_KeyCannotWiden`,
`TestKeyLastUseNeverRevertsRevoke` / `TestLastSeenFlushNeverResurrectsRevokedSession`, `TestImportedKey_AbkVerifies`,
`TestSession_NativeOnlyRecord`, `TestStage12_ExtensionPermsIgnored` / `TestModuleAuthMethod_CannotBeInteractive`,
`TestConfining_WorkerKeyRefusedOnABS`, `TestFirstRun_OnlyPublicAndSetupReachable`,
`TestSetup_RequiresBootstrapToken` / `TestSetup_LoopbackExceptionRefusesForwarded`, `TestBootstrapRedeemedOnce`,
`TestBootstrap_AdminIsCoreAdminHolder`, `TestCredentialEpoch_KillsSessionsAndOneTimeTokens`,
`TestThrottle_SourceAndAccount`, `TestPasswordHash_BoundedConcurrency`, `TestAuthDisabled_AuthRequiredScopesStill401`,
`TestAuthDisabled_InteractiveGuardRefuses` / `TestAuthDisabled_PresentedKeyIgnored`,
`TestAuthDisabled_RefusesNonLoopbackOrProxyWithoutOptIn`, `TestPrincipalFor_CompatAppliesEpochAndScopes`,
`TestLastAdmin_CannotBeDisabledOrDemoted`, `TestRestrictedLastAdmin_SSORefused`, the post-restore rows
(`TestPostRestore_RevokedKeyStaysRevoked` / `TestPostRestore_NoJournalKeysNeedConfirmation` /
`TestRestoreNilCoverageKeepsRevokedTerminal`, `TestRestore_PreSwapRevocationsWin`, `TestPostRestoreSurvivesRestart`,
`TestReplacedStore_RunsPostRestoreHook` / `TestReplacedStore_TruncatedJournalKeysNeedConfirmation`,
`TestRestore_PostArchivePasswordChangeNotReverted` / `TestRevocationCommittedAfterCheckpointReapplied`),
`TestInviteRedeemedOnce` / `TestUserMergeDeletesSourceInvites`, `TestInviteAcceptRechecksCanAssign`, `TestNoGlobals`
(auth), `TestNativeStore_RoundTrip` / `authtest.StoreSuite`. Rows needing the HTTP routes (F4-05) or ABO's live mount
(F4-08) are written there. Also:

- `TestCookie_SiblingAppCookieDoesNotBlockLogin` (05-authn §10): two apps on one host get distinct default cookie
  names, and a foreign or stale cookie on a Public route yields an anonymous request with the cookie cleared.
- `TestUserMergeAtomicAcrossOwners` (05-storage §5S.10): books and the native auth participant merge in one batch; an
  identity row changed after the plan fails the commit with `ErrConflict` (reported as `auth.ErrMergeConflict`); a
  source user with an open invite merges and the invite is deleted; `CrashClone` at every point.
- `metricstest.AssertSeries` over the `auth` scope's `testdata/series.golden` (05-metrics §12).

## Invariants

- Verifiers are hashed; no raw session, key or one-time token is stored or logged.
- A presented-but-invalid credential never falls through to a weaker one.
- A revoked key stays revoked on every path; a touch never un-revokes.
- A key with an empty effective set mints nothing.
- Every admin check honours the presenting key's scopes.
- Auth holds no package-level state (temp-login and throttles are instance state).

## Acceptance

```bash
(cd core && go vet ./auth/... ./audit/... ./internal/authstore/...)                     # exit 0
(cd core && go test -race -count=1 ./auth/... ./audit/... ./internal/authstore/...)     # ok
(cd core && go test -count=1 -run 'TestSessions_ListNeverReturnsToken|TestAPIKey_EmptyEffectiveKeyCannotMint|TestAPIKey_RevokedIsTerminal|TestAdminChecks_HonourKeyScopes|TestRevokeRacesTouch_NeverUnrevokes' ./...)   # PASS
go test -count=1 ./internal/server/... ./internal/usermerge/...                         # ok, ABS suite included
go test -count=1 -run 'TestLegacy' ./...                                                # PASS (R0's six)
for f in internal/server/absauth/password.go internal/server/absauth/throttle.go internal/server/absauth/audit.go internal/server/login_ceiling.go internal/server/bootstrap_rate_limit.go internal/server/invite_rate_limit.go internal/util/email.go internal/config/bootstrap_key_ttl.go internal/config/invite_config.go; do test -e $f && echo "still present: $f"; done   # no output
go test -count=1 ./internal/arch/...                                                    # ok
make -C core ci                                                                         # exit 0
make ci                                                                                 # exit 0
```

## Out of scope

`core/internal/authz` (F4-09); OIDC and Cloudflare Access (F4-03); the routes (F4-05); the identity cutover M3 (F4-06);
the journal format, the P4d step 2 re-apply and the step 3 hook runner (F4-11); replacing the gin auth chain,
`legacyauth` and P7/P4d wiring for ABO (F4-08).

## Rollback

Revert the PR. The moved code returns to its HEAD files; nothing stored changes (the native store is unused by ABO).

## Repo rules

Worktree only; file headers on every created, moved or modified file (fragments headerless); conventional commit ending
`Co-Authored-By: Claude <noreply@anthropic.com>`, no release or flip trailer; move PR rules (old ==> new list, every
commit builds, `go fix`); `gh pr merge <n> --rebase`, never squash, never `--admin`. CLAUDE.md concurrency: password
verification is bounded to `NumCPU` in flight (`TestPasswordHash_BoundedConcurrency`).

```
feat(core/auth): native identity store, sessions, keys, throttle, bootstrap, stage-12 middleware (dark)

Co-Authored-By: Claude <noreply@anthropic.com>
```
