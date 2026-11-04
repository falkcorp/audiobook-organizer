<!-- file: docs/agent-tasks/platform-core/PC-00-01-api-key-writers-revocation-fixes.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3dfb0b7b-c66d-4e6d-ac55-c84404fe5da5 -->
<!-- last-edited: 2026-11-04 -->

# PC-00-01 — Early fixes 2, 3 and 6: API-key writers under `apiKeyMu`, revoked keys stay revoked, a scoped key cannot mint a wider key

| Field | Value |
|---|---|
| ID | PC-00-01 |
| Tier | **T3 (Opus/high)**: fix 6 is a lock-scope race on the path every key-authenticated request takes; a wrong lock scope either reopens the race or deadlocks a request |
| Wave | W00 (S0, release `S0`); live security hotfix, ships first |
| Depends on | none |
| Priority / effort | P0 / L |
| Branch | `fix/pc-00-01-apikey-revocation` |
| Trailer on every commit | `Stage-Release: S0 1` |
| Digest | `docs/plans/platform-core/digests/S0.md` §5 rule 8 |
| Owning spec | `docs/specs/platform-core/05-authn.md` §11.1 items 2, 3 and 6; "Out-of-scope defects found" items 2, 3, 4; `09-abo-on-core.md` S0 "Hotfixes" and §9.1 |

These are live defects at HEAD. 05-authn §11.1 is the single list; implement items 2, 3 and 6 exactly as written
there. They are ordinary hotfixes, **not** a rollback floor: nothing in the extraction depends on them and no level or
`Floor` refers to them (05-authn §11.1).

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-pc-00-01" -b fix/pc-00-01-apikey-revocation origin/main
cd "$REPO/../audiobook-organizer-pc-00-01"
git worktree list            # confirm you are in the new worktree, not the primary checkout
npm ci --prefix web          # make ci runs the web tests; once PC-01-06 has merged use: make web-deps
```

Never run `go work init` in the checkout or the worktree (CLAUDE.md). Never edit on `main`.

## Exact files

Create:

- `internal/database/pebble_store_auth_race_test.go`
- `internal/server/handlers/apikeys_earlyfix_test.go`
- `internal/server/middleware/apikey_revoked_at_test.go` (not in the inventory row; see Planner notes)

Modify:

- `internal/database/pebble_store_auth.go`
- `internal/database/pebble_store.go` (one comment line only, the `apiKeyMu` field comment; see Planner notes)
- `internal/server/handlers/apikeys.go`
- `internal/server/handlers/apikeys_expiry_test.go` (one existing test's request body; step 7)
- `internal/server/middleware/auth.go`
- `internal/server/middleware/absauth.go`
- `TODO.md` or `todo.d/2026-11-04_platform_core_followups.md` (check off three entries; step 9)

Add one `changelog.d/` fragment (no file header).

## Re-verify greps (run first; expected counts are at `ac4368bdb`, origin/main when this brief was written; these files are identical at `748fd787a`)

```bash
grep -n 'apiKeyMu' internal/database/pebble_store_auth.go | wc -l                 # 3  (comment :1273, Lock :1285, Unlock :1286)
grep -rn 'apiKeyMu.Lock()' internal/database/*.go | grep -v _test | wc -l         # 2  (pebble_store_auth.go:1285, user_merge.go:528)
grep -c 'func (p \*PebbleStore) \(CreateAPIKey\|SetAPIKeyStatus\|SetAPIKeyExpiry\|TouchAPIKeyLastUsed\|RevokeAPIKey\)(' internal/database/pebble_store_auth.go   # 5
grep -n 'Set(\[\]byte("apikey:"' internal/database/*.go | grep -v _test | wc -l    # 4  (:1122 CreateAPIKey batch, :1247 SetAPIKeyStatus, :1267 SetAPIKeyExpiry, :1302 Touch)
grep -n 'callerPermSet' internal/server/handlers/apikeys.go | wc -l               # 3  (:199, :205 the guard, :206)
grep -n 'req.Status == "revoked"\|h.store.SetAPIKeyStatus' internal/server/handlers/apikeys.go | wc -l   # 2 (:364, :372)
grep -n 'key.Status == "revoked"\|key.Status == "inactive"' internal/server/middleware/auth.go | wc -l    # 2 (:335, :340)
grep -n 'RevokedAt' internal/server/middleware/auth.go internal/server/middleware/absauth.go | wc -l     # 0
grep -n 'switch key.Status' internal/server/middleware/absauth.go | wc -l          # 1 (:459)
grep -n 'TestLegacyRevokeRacesTouch_NeverUnrevokes\|TestLegacyAPIKeyCreate_EmptyEffectiveKeyCannotMint' -r internal | wc -l   # 0
```

A zero on an edit target, or any count that differs, means STOP and report; do not patch around it.

## What is wrong at HEAD (verified)

- **Fix 6.** `TouchAPIKeyLastUsed` (`pebble_store_auth.go:1284-1302`) holds `p.apiKeyMu` across a whole-row
  read-modify-write and is called synchronously on every key-authenticated request (`middleware/auth.go:382`).
  `SetAPIKeyStatus` (`:1226-1247`, behind PATCH and, through `RevokeAPIKey` `:1222`, behind DELETE) and
  `SetAPIKeyExpiry` (`:1254-1267`) do their own whole-row read-modify-write **without** the lock. A revoke that
  commits between a touch's read and its write is overwritten with the pre-revoke row (`status: active`, no
  `revoked_at`). The lock's one other user, `ApplyUserMergeOps` (`user_merge.go:527-530`), already takes it.
- **Fix 3.** `UpdateStatus` (`handlers/apikeys.go:336-383`) never looks at the current status, so
  `PATCH /api/v1/auth/api-keys/:id {"status":"active"}` re-activates a revoked key; `SetAPIKeyStatus` sets
  `Status = "active"` from any state; both auth paths check only `Status`, never `RevokedAt`
  (`middleware/auth.go:335-344`; `middleware/absauth.go` `ResolveAPIKey`, `switch key.Status` at `:459`).
- **Fix 2.** `Create` (`handlers/apikeys.go:199-210`) checks `callerPermSet[scope]` only `if callerPermSet != nil`,
  and `auth.WithPermissions` stores nothing for an empty slice (`internal/auth/context.go:143-146`), so a key whose
  effective set is empty skips the check. An empty `scopes` list means "all role permissions"
  (`middleware/auth.go:405-408`), so a scoped key can also mint an unscoped one by sending `"scopes": []`.

## Steps

1. Run the re-verify greps.
2. **Fix 6, `internal/database/pebble_store_auth.go`.** Make every `apikey:` writer take `p.apiKeyMu` for the whole
   read-modify-write (read, change, write), exactly as `TouchAPIKeyLastUsed` does:
   - `SetAPIKeyStatus`: `p.apiKeyMu.Lock(); defer p.apiKeyMu.Unlock()` as the first two statements.
   - `SetAPIKeyExpiry`: the same.
   - `CreateAPIKey`: take the lock around the batch build and commit (from `b := p.db.NewBatch()` to the
     `b.Commit` call) so the family has one writer lock without exception (05-authn §11.1 item 6, "every other
     `apikey:` writer").
   - `RevokeAPIKey` keeps calling `SetAPIKeyStatus` and takes **no** lock itself (`sync.Mutex` is not re-entrant).
   - Do not touch `ApplyUserMergeOps`; it already locks (`user_merge.go:528`).
   - No writer may call another writer while holding the lock. `GetAPIKey` takes no lock and stays lock-free.
   - Add the test hook (name coined here) used by the race test, next to the other hooks of this file
     (pattern: `markEmailVerifiedLockedHook`, `pebble_store_auth.go:507`):
     ```go
     // touchAPIKeyAfterReadHook, when set (tests only), runs inside
     // TouchAPIKeyLastUsed after the key row is read and before it is written back.
     var touchAPIKeyAfterReadHook func()
     ```
     and call it in `TouchAPIKeyLastUsed` right after the `k == nil` check:
     `if touchAPIKeyAfterReadHook != nil { touchAPIKeyAfterReadHook() }`.
   - Rewrite the doc comment above `TouchAPIKeyLastUsed` so it states the rule: every writer of an `apikey:` row
     holds `apiKeyMu` across its read and its write, so a touch can never write back a pre-revoke row.
3. **Fix 3, store half.** In `SetAPIKeyStatus`, after reading `k` (inside the lock), refuse a transition to `active`
   for a revoked row:
   ```go
   if status == "active" && (k.Status == "revoked" || k.RevokedAt != nil) {
       return ErrAPIKeyRevoked
   }
   ```
   Declare the sentinel (name coined here) beside the other sentinels of this file:
   `var ErrAPIKeyRevoked = errors.New("database: api key is revoked")` with a doc comment citing 05-authn §11.1 item 3.
   No other transition changes (05-authn §11.1 item 3 refuses only "any transition to `active`").
4. **Fix 3, handler half, `internal/server/handlers/apikeys.go` `UpdateStatus`.** After the ownership check
   (`key.UserID != caller.ID && !isAdminUser(c)` → 403, so a stranger never learns the key's state) and **before**
   binding the body, add:
   ```go
   if key.Status == "revoked" || key.RevokedAt != nil {
       httputil.RespondWithConflict(c, "a revoked API key cannot change status")
       return
   }
   ```
   and map the store's refusal (a revoke that lands between the handler's read and its write):
   `if errors.Is(err, database.ErrAPIKeyRevoked) { httputil.RespondWithConflict(c, "a revoked API key cannot change status"); return }`
   before the existing `InternalError`. Add `"errors"` to the imports. Do **not** change the `APIKeyHandlerStore`
   interface (its mockery mock must stay valid).
5. **Fix 3, both auth paths, `internal/server/middleware`.** Add one helper (name coined here) in `auth.go` beside
   `APIKeyExpiryRefusal`:
   ```go
   // APIKeyRevoked reports whether key must be refused as revoked: its status is
   // "revoked" or its RevokedAt is set, whatever its status (05-authn §11.1 item 3).
   func APIKeyRevoked(key *database.APIKey) bool {
       return key.Status == "revoked" || key.RevokedAt != nil
   }
   ```
   - Web path `handleAPIKeyAuth`: replace `if key.Status == "revoked" {` with `if APIKeyRevoked(key) {` (same 401
     body "API key has been revoked").
   - ABS path `ResolveAPIKey`: before `switch key.Status`, add
     `if APIKeyRevoked(key) { return nil, absErr(http.StatusUnauthorized, "apikey-revoked", "API key has been revoked", ABSModeAPIKey) }`
     and delete the now-unreachable `case "revoked":` arm, keeping `case "inactive":`.
6. **Fix 2, `internal/server/handlers/apikeys.go` `Create`.** Replace the scope block (`:199-210`) with:
   ```go
   // A nil permission set is the empty set for every caller (05-authn §11.1 item 2).
   callerPermSet := auth.PermissionsFromContext(c.Request.Context())
   if servermiddleware.CurrentAuthMethod(c) == auth.MethodAPIKey && len(req.Scopes) == 0 {
       httputil.RespondWithForbidden(c, "an API key can only create a key with explicit scopes it holds")
       return
   }
   for _, scope := range req.Scopes {
       if !auth.IsKnown(scope) {
           httputil.RespondWithBadRequest(c, "unknown scope: "+scope)
           return
       }
       if _, has := callerPermSet[auth.Permission(scope)]; !has { // nil map: has == false
           httputil.RespondWithForbidden(c, "cannot grant scope you don't have: "+scope)
           return
       }
   }
   ```
   The `if callerPermSet != nil` guard is gone. Everything after the loop is unchanged; a session caller with
   `"scopes": []` still creates an all-role-permissions key (unchanged behaviour, the spec restricts the empty list
   only for an API-key caller).
7. **Existing test that this changes on purpose.** `TestAPIKeyCreate_ByAPIKeyClampedToCallingKey`
   (`internal/server/handlers/apikeys_expiry_test.go:91`) creates a key **from an API key with no scopes**, which
   fix 2 now refuses. Change only its request body to
   `map[string]any{"name": "child", "expires_in_days": 365, "scopes": []string{"library.view"}}`; its clamp
   assertions stay. No other existing test should change; if another one fails, STOP and report it.
8. **`internal/database/pebble_store.go`**: rewrite the trailing comment of the `apiKeyMu` field (`:231`) to
   "serializes every apikey: row write (create, status, expiry, last-use touch, user-merge owner rewrite) so a
   touch cannot write back a pre-revoke row (pebble_store_auth.go)". Comment only.
9. **TODO.** Check off the three entries "SECURITY (hotfix, early fix 2)", "(hotfix, early fix 3)" and
   "(hotfix, early fix 6)": run `grep -n 'hotfix, early fix [236])' TODO.md todo.d/*.md` and change `- [ ]` to
   `- [x]` on each of the three lines, in whichever file holds them (the daily collector may already have moved
   the fragment into `TODO.md`; CLAUDE.md "Post-Task Hygiene").
10. **Changelog fragment** `changelog.d/<YYYY-MM-DD>_pc-00-01-apikey-revocation.md` under `### Security`, with one
    `####` entry per fix, carrying the S0 release-note text of 09 S0 for these three: "a revoked API key can no
    longer be re-activated; a limited API key can no longer create an unlimited one; a revocation can no longer be
    undone by a request that was in flight."
11. Bump the version header (minor) and `last-edited` of every modified Go file; new files get a 4-line header with
    a fresh guid (`python3 -c 'import uuid;print(uuid.uuid4())'`).

## Code contract

```go
// internal/database (new)
var ErrAPIKeyRevoked = errors.New("database: api key is revoked") // name coined here
var touchAPIKeyAfterReadHook func()                                // tests only; name coined here

// internal/server/middleware (new)
func APIKeyRevoked(key *database.APIKey) bool // name coined here
```

Unchanged signatures: `SetAPIKeyStatus(id, status string, at time.Time) error`,
`SetAPIKeyExpiry(id string, at time.Time) error`, `TouchAPIKeyLastUsed(id string, at time.Time, ip string) error`,
`CreateAPIKey(key *APIKey) (*APIKey, error)`, and the `APIKeyHandlerStore` interface.

Invariants the tests prove:

- I1: a revoke and a touch on one key, in any interleaving, end with `Status == "revoked"` and `RevokedAt != nil`.
- I2: no code path moves a row with `RevokedAt != nil` or `Status == "revoked"` to `active`.
- I3: neither key-auth path authenticates a row with `RevokedAt != nil`, whatever its `Status`.
- I4: an API-key caller never creates a key with empty scopes or a scope outside its effective set; a nil effective
  set is the empty set for every caller.
- I5: no deadlock: no `apikey:` writer calls another writer while holding `apiKeyMu`.

## TDD: tests to write first (they must fail on HEAD, pass after)

`internal/database/pebble_store_auth_race_test.go` (package `database`):

- **`TestLegacyRevokeRacesTouch_NeverUnrevokes`** (the spec's name, 05-authn §10 and §11.1 item 6). Deterministic, no
  sleeps that decide the verdict:
  1. `store, _ := NewPebbleStoreInMemory("db")`; create a key (`UserID: "user-1"`); `t.Cleanup(store.Close)`.
  2. Set `touchAPIKeyAfterReadHook` to a func that closes a `read` channel (once) and blocks on a `release`
     channel; `t.Cleanup` resets it to nil.
  3. `go store.TouchAPIKeyLastUsed(key.ID, now, "127.0.0.1")`; wait for `read`.
  4. `go func(){ store.RevokeAPIKey(key.ID); close(revokeDone) }()`.
  5. `select { case <-revokeDone: case <-time.After(200 * time.Millisecond): }` — on HEAD the revoke finishes while
     the touch holds its stale read; with the fix it waits on the lock and the timeout path is taken.
  6. Close `release`; wait for the touch and the revoke (`waitGroupOrFatal` from `apikey_touch_concurrent_test.go`).
  7. Assert the stored row has `Status == "revoked"` and `RevokedAt != nil`. On HEAD it ends `active` (fails).
  - Subtest `expiry`: the same shape with `SetAPIKeyExpiry(key.ID, t1)` instead of the revoke; assert the stored
    `ExpiresAt` equals `t1` (fails on HEAD: the touch writes back the old expiry).
  - Subtest `stress` (run under `-race`): 100 concurrent touches and one revoke started midway; assert `revoked`
    and `UseCount` equal to the number of touches that ran.
- **`TestSetAPIKeyStatus_RevokedRowNeverActive`** (name coined here): revoke a key, then
  `SetAPIKeyStatus(id, "active", now)` returns `errors.Is(err, ErrAPIKeyRevoked)` and the row is still `revoked`;
  a row written with `Status: "inactive"` and `RevokedAt` set is refused the same way; an `inactive` row with no
  `RevokedAt` can still go `active` (unchanged).

`internal/server/handlers/apikeys_earlyfix_test.go` (package `handlers_test`; reuse `newAuthCtx`, `asCaller`,
`echoCreatedKey` and `handlersmocks.NewMockAPIKeyHandlerStore`, which fails on an unexpected call):

- **`TestLegacyAPIKeyCreate_EmptyEffectiveKeyCannotMint`** (the spec's name). Table, each a subtest:
  - API-key caller, **no** permissions (`asCaller(c, user, auth.MethodAPIKey, keyWithExpiry)` with no perms → nil
    set), `"scopes": ["library.view"]` → 403, no `CreateAPIKey` call;
  - same caller, `"scopes": []` → 403, no call;
  - API-key caller with perms `library.view, users.manage`, `"scopes": []` → 403 (empty scopes refused for a key);
  - API-key caller with perms `library.view, users.manage`, `"scopes": ["library.view"]` → 201;
  - API-key caller with perms `library.view`, `"scopes": ["users.manage"]` → 403;
  - session caller with **no** permissions, `"scopes": ["library.view"]` → 403 (nil is empty for every caller);
  - session caller with no permissions, no scopes → 201 (unchanged).
- **`TestAPIKeyUpdateStatus_RevokedKeyAnswers409`** (name coined here): owner session; `GetAPIKey` returns a key
  with `Status: "revoked"` → PATCH `{"status":"active"}` → 409 and no `SetAPIKeyStatus` call; a key with
  `Status: "inactive"` and `RevokedAt` set → 409; `{"status":"inactive"}` on a revoked key → 409; a store that
  returns `database.ErrAPIKeyRevoked` from `SetAPIKeyStatus` (key read as `inactive`) → 409, not 500.

`internal/server/middleware/apikey_revoked_at_test.go` (package `middleware`):

- **`TestAPIKeyAuth_RevokedAtRefusedOnBothPaths`** (name coined here): a key with `Status: "active"`,
  `RevokedAt` set and a future `ExpiresAt`:
  - web path: `authMethodStore()` (from `auth_method_test.go`) with `GetAPIKeyByHashFunc` returning that key;
    `gin.New()`, `r.Use(RequireAuth(store))`, `Authorization: Bearer abk_secret` → 401, body contains
    "revoked", and `TouchAPIKeyLastUsedFunc` is never called (set it to `t.Fatal`);
  - ABS path: `newAPIKeyHarness(t)` (from `absauth_apikey_test.go`), `h.store.addUser(...)` active owner,
    `h.store.addKey("abk_gone", key)` → `h.get("abk_gone")` is 401.

## Acceptance (commands and expected output)

```bash
go build ./...                                                               # exit 0, no output
go vet ./internal/database/ ./internal/server/...                            # exit 0
go test -race -count=1 -run 'TestLegacyRevokeRacesTouch_NeverUnrevokes|TestSetAPIKeyStatus_RevokedRowNeverActive|TestTouchAPIKeyLastUsed_ConcurrentTouchesDoNotLoseIncrements' ./internal/database/
#   ok  github.com/falkcorp/audiobook-organizer/internal/database
go test -race -count=1 -run 'TestLegacyAPIKeyCreate_EmptyEffectiveKeyCannotMint|TestAPIKeyUpdateStatus_RevokedKeyAnswers409|TestAPIKey' ./internal/server/handlers/
#   ok  github.com/falkcorp/audiobook-organizer/internal/server/handlers
go test -race -count=1 -run 'TestAPIKeyAuth_RevokedAtRefusedOnBothPaths|TestABSAPIKey|TestRequireAuth' ./internal/server/middleware/
#   ok  github.com/falkcorp/audiobook-organizer/internal/server/middleware
grep -n 'apiKeyMu.Lock()' internal/database/pebble_store_auth.go | wc -l    # 4 (CreateAPIKey, SetAPIKeyStatus, SetAPIKeyExpiry, Touch)
grep -n 'if callerPermSet != nil' internal/server/handlers/apikeys.go | wc -l  # 0
grep -rn 'RevokedAt != nil' internal/server/middleware/auth.go | wc -l        # 1 (APIKeyRevoked)
make ci                                                                      # last line: ✅ All CI checks passed!
```

Before writing the fix, run the three new test files on the unfixed tree and confirm each named test fails; record
that in the PR description.

## Out of scope

- The revocation journal (needs the S3 keyring; 05-authn §11.1 last paragraph).
- Any `CheckedWrites`/`Expect` mechanism: ABO keeps HEAD's mutexes (resolution A-3, 05-authn §3.4).
- Early fixes 1, 4 and 5 (PC-00-03, PC-00-02, PC-00-04). `RequireAdmin` is PC-00-02's.
- Re-revoking an already revoked key (it rewrites `RevokedAt`); not on the list.
- Any parallelisation: none of these paths is a library-scale loop.

## Rollback

Revert the PR (binary swap on deploy). Nothing is written in a new format; a revoked row keeps HEAD's shape. After a
rollback the three defects are live again; the release notes name the first release that carries the fixes as the
oldest safe rollback target (05-authn §11.1).

## Repo rules

- File headers on every new or changed file, version bumped (`.standards/instructions/file-headers.md`);
  `changelog.d/` and `todo.d/` fragments carry no header.
- Conventional commits, e.g. `fix(auth): serialize every apikey: writer on apiKeyMu (early fix 6)`. Every commit ends
  with:
  ```
  Stage-Release: S0 1
  Co-Authored-By: Claude <noreply@anthropic.com>
  ```
  (09 §9.2.1: every commit of a PR that touches a non-doc file carries the trailer.)
- `git push -u origin fix/pc-00-01-apikey-revocation`, `gh pr create`, wait for every required check, then
  `gh pr merge <n> --rebase`. Never squash, never `--admin` past a required check (14 Q-I).
- CLAUDE.md concurrency rules: no new goroutines in production code; the lock is held only across a memtable read
  and one write, never across a network call.
- After merge: `git worktree remove "$REPO/../audiobook-organizer-pc-00-01" && git worktree prune`.

## Planner notes

- The inventory row lists no middleware test file, but its done-check "both key-auth paths refuse a row with
  `revoked_at` set" can only be tested in package `middleware`; this brief adds
  `internal/server/middleware/apikey_revoked_at_test.go`. No other W00 row creates that file.
- The inventory row omits `internal/database/pebble_store.go`: the `apiKeyMu` field comment (`:231`) states the
  lock covers only the last-used touch, which this fix makes false (CLAUDE.md "verify the claim"); one comment line.
- The inventory row omits `internal/server/handlers/apikeys_expiry_test.go`: fix 2 deliberately changes the
  outcome of `TestAPIKeyCreate_ByAPIKeyClampedToCallingKey` (a key caller with empty scopes), whose body must name
  a scope.
- The inventory omits the `TODO.md` / `todo.d` check-off; the row's note asks for one entry per fix.
