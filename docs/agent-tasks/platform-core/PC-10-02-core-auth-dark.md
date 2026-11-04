<!-- file: docs/agent-tasks/platform-core/PC-10-02-core-auth-dark.md -->
<!-- version: 1.0.0 -->
<!-- guid: ae10dd37-49e2-4a03-85ce-b1c74c6b2a26 -->
<!-- last-edited: 2026-11-04 -->

# PC-10-02 — core/auth (dark): Service, principal resolution, sessions, keys, one-time tokens, bootstrap, throttle, password, PostRestore and BootWrites hooks, native store

**ID:** PC-10-02 · **Wave:** W10 (S4 r1, dark) · **Priority:** P1 · **Effort:** XL
**Tier:** T4 Opus/xhigh-max — the identity engine every surface will use from the flip; its hooks run on restore
boots; a lock released before the commit reopens early fix 6 (a touch un-revoking a key) or a single-use token.
**Depends on:** PC-10-01 (`rbac`, `audit`, `journal`, `events`).
**Stage-Release trailer:** `Stage-Release: S4 1` on every commit.
**Normative digest:** `docs/plans/platform-core/digests/S4.md` (Contracts: 05-authn §3.2, §3.4; Behaviour rules 14-18).
**Owning spec:** 05-authn §3 (§3.1 identities and principals, §3.2 service, §3.3 authenticators, §3.4 store), §4
(§4.1-§4.12 behaviour; §4.14 post-restore hook), §5 lifecycle, §6 errors, §7 concurrency, §8 config keys, §9 metrics,
§10 tests, §11 (the native store); 05-backup B7 and §5B.3.6; 05-storage §5S.3.2 (`CommitLocked`, `LocksHeld`).

## ⛔ START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-pc-10-02" -b agent/pc-10-02-core-auth origin/main
cd "$REPO/../audiobook-organizer-pc-10-02"
git rebase origin/main
git worktree list
npm ci --prefix web
```

Suggested split: a code-exploration pass over HEAD's identity code (`internal/auth`, `internal/server/absauth`'s
hashing/throttle/LRU, `internal/server/bootstrap.go`, `internal/database/pebble_store_auth.go` — moved to
`internal/books/store/` at PC-07-04) mapping each behaviour to its 05-authn section, then implementation, then a
code-auditing pass on lock order and token single-use.

## Goal

Land `mediacore/auth` (L4) complete and tested over its **native** store, called by nothing in ABO yet (gin still
authenticates every request through S4 r1):

- `auth.Service` and its methods (05-authn §3.2), principal resolution (`PrincipalFor`, `Middleware`), sessions, API
  keys (`KeySpec.Scopes` required, non-empty, wildcard-free; statuses `active`, `disabled`, `needs_confirmation`,
  `revoked`), one-time tokens (invites, temp-login, reset), bootstrap (`RedeemBootstrap`, §4.6), throttle and lockout
  (§4.9, from absauth's throttle/saturation/LRU), `auth/password` (hashing ported from absauth), credential epochs and
  revocation (§4.12), auth-off principal (§4.11), the extension point (`Authenticator`, `MethodSpec`, `Extension`, §3.3);
- `PostRestore` and `BootWrites` (§4.14, §4.6) and `RestoreDeps`;
- `auth.Store` (§3.4), the native store in `core/internal/authstore` over `c/auth/` (hashed sessions, `mck_` keys —
  native only; ABO's legacy store keeps HEAD's representation, PC-10-05), and `auth/authtest.StoreSuite`;
- email normalization moved from `internal/util/email.go` per 04's `internal/util` split row (needed by
  `auth/oidc` in PC-10-03; see step 7).

## Exact files

Create: `core/auth/`, `core/auth/authtest/`, `core/auth/password/`, `core/internal/authstore/`.
Also needed (Planner notes): `core/internal/authsys/` (the system-principal constructor, 05-authn §3.1).

## Re-verify (rerun at your base)

```bash
test -d core/rbac && test -d core/audit && test -d core/backup/journal && test -d core/events && echo ok   # PC-10-01
test -d core/auth && echo EXISTS-STOP || echo absent                                                       # absent
grep -n 'func NormalizeEmail(s string) string\|func SameEmail(a, b string) bool' internal/util/email.go   # 2 hits (copy source)
ls internal/server/absauth/ | head -20                                         # hashing, throttle, saturation, LRU sources (04 §8.1 S4)
grep -n 'apiKeyMu\|inviteMu\|userWriteMu\|sessionWriteMu\|bootstrapMu\|userMergeApplyMu' -r internal/books/store/ internal/database/ 2>/dev/null | grep 'sync\.' | head   # HEAD's six identity mutexes (names exact)
grep -n 'func CommitLocked\|func LocksHeld' -r core/storage/                   # 2 hits
```

## Steps

1. Tests first (below), red.
2. Types and `Service` exactly as 05-authn §3.1-§3.3 (contract below). `auth` never imports `legacy`, `httpapi` or
   `events`' internals; it receives `Deps.Events` and publishes.
3. **Every write takes the caller's batch.** Every batch with an identity write is built and committed inside
   `storage.CommitLocked(ctx, b, d, stage, locks...)` with the locks of `Deps.Locks` (`StoreLocks{Users, Sessions,
   APIKeys, Invites, Bootstrap, Merge}`) per the mapping of digest rule 16 (`PutUser` Users; session writes Sessions;
   key writes APIKeys; invites, identities, allow-list Invites, plus Users for an account-creating accept; bootstrap
   Bootstrap, plus Users and APIKeys for the redeem; `RevokeSignInsBefore` Sessions+Invites+Bootstrap; `StageMerge`
   Merge; temp-login none). `auth` never calls `WriteLocks`; the kernel resolves the locks at P4c (PC-10-05 wires it).
4. **Early fixes through core** (05-authn §11.1; digest rule 18): a coalesced key touch after a revoke leaves it revoked
   (fix 6); an empty effective key cannot mint (fix 2); a revoked key is terminal (fix 3); `Can(core:admin)` is
   key-scoped (fix 4); the session list carries no bearer token and revokes by a derived handle (fix 1).
5. **Hooks.** `PostRestore` and `BootWrites` per §4.14 and §4.6 with `RestoreDeps`; at S4 r1 the kernel does not call
   them (dark); they are complete and tested here over the native store.
6. **Native store** in `core/internal/authstore` over `c/auth/`, passing `authtest.StoreSuite`
   (`TestNativeStore_RoundTrip`). It implements none of package `legacy`'s capabilities.
7. **Email normalization.** Add `core/auth/email.go` with `NormalizeEmail(s string) string` and `SameEmail(a, b string)
   bool`, behaviour-identical to `internal/util/email.go` (copy its table tests). ABO's `internal/util/email.go` stays
   for its ABO callers in this PR (see Planner notes).
8. **Goroutines** (§5 item 6): one throttle sweeper and one `LastSeen` flusher per Service, bounded drain on shutdown
   (2 s); tests under `testing/synctest`.
9. Headers, `changelog.d/` fragment, acceptance.

## Code contract (05-authn §3.2, §3.4; digest "Contracts")

```go
func New(ctx context.Context, opts Options, deps Deps) (*Service, error)
type Deps struct { Store Store; RBAC *rbac.Service; Audit audit.Logger; Secrets secrets.Store; Events *events.Bus; Revocations journal.RevocationJournal
	Clock func() time.Time; Log *slog.Logger; Metrics *metrics.Scope; StateFiles StateFiles; Locks StoreLocks }
type StoreLocks struct{ Users, Sessions, APIKeys, Invites, Bootstrap, Merge []storage.Lock }
type Options struct { LegacyKeyPrefixes []string; InviteLinkForm InviteLinkForm; DefaultCookieName string; DefaultCookieNoHostPrefix, DefaultDisabledAllowRemote bool }
func (s *Service) CreateUser(ctx context.Context, actor *Principal, in NewUser) (*User, error)
func (s *Service) SetStatus(ctx context.Context, actor *Principal, id UserID, st UserStatus) error
func (s *Service) SetRoles(ctx context.Context, actor *Principal, id UserID, roles []rbac.RoleName) error
func (s *Service) ChangeOwnPassword(ctx context.Context, actor *Principal, current, next string) error
func (s *Service) MintTempLogin(ctx context.Context, actor *Principal, target UserID) (link string, err error)
func (s *Service) Login(ctx context.Context, in LoginInput) (*IssuedSession, error)
func (s *Service) CreateAPIKey(ctx context.Context, actor *Principal, spec KeySpec) (raw string, key *APIKey, err error)
func (s *Service) SetAPIKeyStatus(ctx context.Context, actor *Principal, id string, st KeyStatus) error
func (s *Service) RedeemBootstrap(ctx context.Context, token string, client ClientInfo) (*BootstrapResult, error)
func (s *Service) PrincipalFor(ctx context.Context, u *User, m Method, scopes []rbac.Permission, epoch int64) (*Principal, error)
func (s *Service) CheckCredentialFor(p *Principal, perm rbac.Requirement) error
func (s *Service) Middleware() func(http.Handler) http.Handler
func PostRestore(ctx context.Context, d RestoreDeps, rc journal.RestoreContext) (RestoreCounts, error)
func BootWrites(ctx context.Context, d RestoreDeps) error
type RestoreDeps struct { Store, Previous Store; Revocations journal.RevocationJournal; StateFiles StateFiles; Locks StoreLocks; Clock func() time.Time }
const ( InviteLinkFragment InviteLinkForm = iota; InviteLinkPath )
```

Other `Service` methods: `DeleteUser`, `MintResetLink`, `IssueSession`, `RevokeSession`, `RevokeAllSessions`,
`RevokeAPIKey`, `Revalidate`. The `Store` interface is 05-authn §3.4 verbatim (digest reproduces it). `Deps.Secrets`:
05-config §2.11 item 4 says auth should take a `secrets.Scope` (prefix `core.auth.`) or `secrets.Resolver`; follow
05-authn's text as written and record the open alignment in the PR.

## TDD — names from 05-authn §10 (the native-store and service half)

`TestNativeStore_RoundTrip` with `authtest.StoreSuite`; inside the suite, `TestKeyLastUseNeverRevertsRevoke` and
`TestLastSeenFlushNeverResurrectsRevokedSession` (`synctest`); `TestLegacyAPIKeyCreate_EmptyEffectiveKeyCannotMint` at
service level over a fake legacy-style store; `TestPostRestoreSurvivesRestart`; `TestRestore_PreSwapRevocationsWin`;
`TestBootWrites_RunInP4d` (service half: idempotent, writes only what §4.6 lists); `TestExpiryStamp_OneShot` (service
half). Run every other 05-authn §10 test that does not need ABO's legacy store or a native route; list in the PR which
ones are deferred to PC-10-05, PC-11-01 and PC-11-02.

## Invariants (STOP conditions)

- No staging method takes a mutex: each checks `storage.LocksHeld(b, l)` and refuses with `storage.ErrNotLocked`.
- A one-time token is redeemed at most once under concurrent redeems (`TakeOneTime` inside the hold).
- `auth` imports no ABO package and no `legacy`; `go list -deps ./core/auth` shows neither.
- Dark: nothing in the root module constructs `auth.Service` in this PR.

## Acceptance

```bash
(cd core && go build ./... && go test ./auth/... ./internal/authstore/... ./internal/authsys/... -race -count=1)   # ok
(cd core && go list -deps ./auth | grep -E 'audiobook-organizer|mediacore/legacy')       # no output
grep -rn 'mediacore/auth"' --include=*.go internal cmd main.go | grep -v _test           # no output (dark)
make -C core ci                                                                          # exit 0
make ci                                                                                  # exit 0
```

## Out of scope

`auth/oidc` (PC-10-03); `internal/cfaccess` (PC-10-04); books' legacy stores and the kernel's P4c/P4d wiring
(PC-10-05); routes (PC-11-01); the flip, `seed.go`/`bootstrap.go` moves, P7 seeding (PC-11-02).

## Rollback

Binary swap; nothing writes through this code at r1.

## Repo rules

Headers; `changelog.d/` fragment. Conventional commits with `Stage-Release: S4 1` and
`Co-Authored-By: Claude <noreply@anthropic.com>`. `gh pr merge <n> --rebase`. CLAUDE.md concurrency: the sweeper and
flusher are single goroutines; no unbounded fan-out.

## Planner notes

- **Missing path:** `core/internal/authsys` (the only constructor of the system principal, 05-authn §3.1, SEC-26) is in
  no inventory row; it belongs here.
- **Implied move not in any row:** PC-10-03 needs core-side email normalization (04's `internal/util` row splits
  `email.go` into `mediacore/auth`); this brief adds `core/auth/email.go`. Retiring ABO's `internal/util/email.go` and
  rewiring its ABO importers is not in any row.
