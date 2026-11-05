<!-- file: docs/agent-tasks/platform-core/F4-02-core-auth-store-service-middleware.md -->
<!-- version: 1.0.0 -->
<!-- guid: 17675dfd-c1d9-454e-bb6e-545401beffad -->
<!-- last-edited: 2026-11-05 -->

# F4-02 — Core auth, dark: `auth.Store` native over `c/auth/`, `auth.Importer`, `authtest`, `Service.Middleware` (stage 12), hashed sessions and one-time tokens, `mck_` keys with `abk_` verification, throttle, bootstrap, `AuthCompat`, `auth/password`; the five ported early-fix property tests

| Field | Value |
|---|---|
| ID | F4-02 |
| Release / flip | R2, identity flip F4 (dark PR) |
| Tier | **T4** (Opus, xhigh–max): the authentication engine, hashed credentials and five ported security properties |
| Wave | W24 (runs inside an announced **move window**: no other PR of the wave touches the moved files or the shared `layerOf` and shard files) |
| Depends on | F4-01, F4-04, F3-01 |
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

Confirm F4-01, F4-04 and F3-01 are merged and the move window naming the moved files is announced. If not, stop.

## Goal

Complete `github.com/falkcorp/mediacore/auth` on R1's leaf (R1-12: `MethodSpec`, `Principal`, `User`, `UserID`,
`UserStatus`, `Assurance`) as 05-authn §3–§4 specify, dark, and move into core the HEAD files 05-authn §11 assigns to
it, re-pointing ABO's gin code at core's public API so **ABO's behaviour is unchanged**: the gin auth chain stays HEAD's
until F4-08.

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

## Files

| Action | Path |
|---|---|
| create | `core/auth/store.go`, `core/auth/service.go` (and sibling files of `core/auth/` for formats, throttle, bootstrap, compat, middleware) |
| create | `core/auth/authtest/`, `core/auth/password/`, `core/internal/authstore/` |
| move (delete in ABO, re-point callers) | `internal/server/absauth/password.go`, `internal/server/absauth/throttle.go`, `internal/server/absauth/audit.go`, `internal/server/login_ceiling.go`, `internal/server/bootstrap_rate_limit.go`, `internal/server/invite_rate_limit.go`, `internal/util/email.go`, `internal/config/bootstrap_key_ttl.go`, `internal/config/invite_config.go` |
| create | `core/audit/auth_types.go` |

The row lists only these ABO files. The callers of the moved symbols must be re-pointed; see STOP 2.

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
grep -rln 'absauth\.HashPassword\|absauth\.VerifyPassword\|util\.NormalizeEmail' --include=*.go . | grep -v _test | wc -l   # HEAD 17 caller files
grep -rn 'type MethodSpec struct' core/auth/ | wc -l          # expect 1 (R1-12)
grep -rn 'func (s \*Service) Compile' core/rbac/ | wc -l      # expect 1 (F4-01)
grep -rn 'type RevocationJournal interface' core/backup/journal/ | wc -l   # expect 1 (F4-04)
grep -rn 'type Bus struct' core/events/ | wc -l               # expect 1 (F4-04)
grep -rn 'TestLegacy' internal/server/handlers/ internal/server/middleware/ internal/database/ internal/books/store/ 2>/dev/null | grep -c 'func Test'   # the six R0 tests (R0-02 … R0-05) present
```

## STOP conditions

1. ABO's behaviour changes before F4-08: every ABO test, the ABS conformance suite (`internal/server/handlers/abs/`,
   `internal/server/absauth/`) and the six `TestLegacy…` tests of R0 stay green **unchanged**.
2. Re-pointing a moved symbol's caller needs a file outside the row (the 17 caller files at HEAD are spread across
   `internal/server/`, `internal/server/middleware/`, `internal/server/handlers/` and `internal/usermerge/`, which other
   rows of nearby waves own). Report the list before editing; the lead amends the row or keeps a one-line forwarding
   shim in the old file for this PR (a shim is acceptable only if the 04 key's move is then completed by F4-08, which
   owns `internal/server/`).
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
   (§3.2, §4.14; write nothing in `New` or a constructor).
8. **Moves:** move each listed HEAD file's code to its core home, re-point its callers at core's public API, keep HEAD's
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
(F4-08) are written there.

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
for f in internal/server/absauth/password.go internal/server/absauth/throttle.go internal/server/absauth/audit.go internal/server/login_ceiling.go internal/server/bootstrap_rate_limit.go internal/server/invite_rate_limit.go internal/util/email.go internal/config/bootstrap_key_ttl.go internal/config/invite_config.go; do test -e $f && echo "still present: $f"; done   # no output (or only agreed shims, STOP 2)
go test -count=1 ./internal/arch/...                                                    # ok
make -C core ci                                                                         # exit 0
make ci                                                                                 # exit 0
```

## Out of scope

OIDC and Cloudflare Access (F4-03); the routes (F4-05); the identity cutover M3 (F4-06); replacing the gin auth chain,
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

## Planner notes

- **Callers outside the row.** The moved symbols have 17 non-test caller files at HEAD; several live in
  `internal/server/` and `internal/usermerge/` paths that F4-03 (W26) and F4-08 (W31) own as whole directories. The row
  should either list the caller files or allow forwarding shims until F4-08.
- **Ordering with F3-06.** This row moves `internal/config/invite_config.go` and `bootstrap_key_ttl.go` (W24) before
  F3-06 (W25) generates the rename table from HEAD's config code; F3-06 must read their `BindEnv` names from the
  frozen source of `748fd787a`, not from BASE's `internal/config`.
