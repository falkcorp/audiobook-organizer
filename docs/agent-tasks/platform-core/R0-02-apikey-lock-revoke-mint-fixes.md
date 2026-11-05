<!-- file: docs/agent-tasks/platform-core/R0-02-apikey-lock-revoke-mint-fixes.md -->
<!-- version: 1.1.0 -->
<!-- guid: db4a0394-774e-4f4a-8a93-e57fc75b4bc1 -->
<!-- last-edited: 2026-11-05 -->

# R0-02 — Early fixes 2, 3 and 6: every `apikey:` writer under `apiKeyMu`, a revoked key stays revoked on every path, a key with an empty effective set mints no key

| Field | Value |
|---|---|
| ID | R0-02 |
| Release | R0 (09 §9.4 R0 Content (1)); a live security hotfix in HEAD's code |
| Tier | **T3** (Opus / high): fix 6 is a lock-scope race on every key-authenticated request, plus two authorization rules |
| Wave | W01 |
| Depends on | none |
| Branch | `fix/r0-02-apikey-revoke-mint` |
| Owner text | 05-authn §11.1 items 2, 3 and 6, and its test table (the single list of early fixes; 09 §9.1 "Live access-widening defects") |

**Gate:** plan, then execute autonomously in your own worktree and PR. This is security work on HEAD's legacy
code, not core: implement 05-authn §11.1 items 2, 3 and 6 **exactly**, and nothing narrower or wider.

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/r0-02-apikey" -b fix/r0-02-apikey-revoke-mint origin/main
cd "$REPO/.worktrees/r0-02-apikey"
git rebase origin/main
npm ci --prefix web
```

Never edit the primary checkout or `main`; never run `go work init` (CLAUDE.md).

## Goal (the three rules, verbatim in substance from 05-authn §11.1)

- **Fix 6 — a request with a key can undo that key's revocation.** `TouchAPIKeyLastUsed` holds `apiKeyMu` across
  its whole-row read-modify-write, but `SetAPIKeyStatus` (behind DELETE and PATCH) and `SetAPIKeyExpiry` do their
  own whole-row read-modify-write **without** it, so a revoke that commits between a touch's read and its write is
  overwritten with the pre-revoke row. Hotfix: `SetAPIKeyStatus`, `SetAPIKeyExpiry` **and every other `apikey:`
  writer** take `apiKeyMu`, so every writer of the family is serialized by one lock (the lock's one other user,
  user merge, already takes it).
- **Fix 3 — a revoked API key can be re-activated.** `PATCH /api/v1/auth/api-keys/:id` answers **409** for a key
  with `status == "revoked"` or a non-nil `revoked_at`; `SetAPIKeyStatus` refuses any transition to `active` for
  such a row, so no other caller bypasses the handler; **both** key-auth paths refuse a row with `revoked_at` set
  whatever its `status`: the web path and the ABS path (`ResolveAPIKey`, its own `switch key.Status`).
- **Fix 2 — a scoped API key can mint an unscoped key.** When the caller is an API key, require **non-empty**
  scopes ⊆ the key's effective set, and fail closed on a nil or empty caller permission set: the hotfix treats nil
  as the empty set **for every caller**, so a key whose effective set is empty mints no key.

## Files

| Action | Path |
|---|---|
| modify | `internal/database/pebble_store_auth.go` (fixes 6 and 3, store side) |
| create | `internal/database/pebble_store_auth_race_test.go` (package `database`) |
| modify | `internal/server/handlers/apikeys.go` (fixes 2 and 3, handler side) |
| create | `internal/server/handlers/apikeys_earlyfix_test.go` (package `handlers_test`) |
| modify | `internal/server/middleware/auth.go` (fix 3, web key path only) |
| modify | `internal/server/middleware/absauth.go` (fix 3, ABS key path only) |
| create | `internal/server/middleware/auth_revoked_test.go` (package `middleware`) |
| create | `changelog.d/<date>_r0_apikey_early_fixes.md` (no header) |
| tick | the three SECURITY entries "early fix 2", "early fix 3", "early fix 6" (step 9) |

`internal/server/middleware/auth.go` is also edited by R0-03 (wave W02), which waits for this PR. Keep your
change there to `handleAPIKeyAuth`; do not touch `RequireAdmin`.

## Re-verify before editing (expected at the base commit)

The spec anchors at 748fd787a; none of these files changed between it and HEAD, so the spec's line numbers hold.
Any count that differs: STOP and report.

```bash
# Store writers of the apikey: family (non-test): expect 4 lines: 1122, 1247, 1267, 1302
grep -n 'Set(\[\]byte("apikey:"' internal/database/pebble_store_auth.go
# The only lock holders today: expect 2 (TouchAPIKeyLastUsed :1285, user merge user_merge.go:528)
grep -rn 'apiKeyMu.Lock()' internal/database/*.go | grep -v _test.go | wc -l
# Writers to lock: expect 1 hit each (1100, 1226, 1254, 1284)
grep -n 'func (p \*PebbleStore) CreateAPIKey\|func (p \*PebbleStore) SetAPIKeyStatus\|func (p \*PebbleStore) SetAPIKeyExpiry\|func (p \*PebbleStore) TouchAPIKeyLastUsed' internal/database/pebble_store_auth.go
# No other package writes apikey: rows directly: expect 0
grep -rn 'Set(\[\]byte("apikey:' internal --include=*.go | grep -v _test.go | grep -v 'internal/database/pebble_store_auth.go' | wc -l
# Fix 2 edit target: expect "199:	callerPermSet := ..." and "205:		if callerPermSet != nil {"
grep -n 'callerPermSet := auth.PermissionsFromContext\|if callerPermSet != nil' internal/server/handlers/apikeys.go
# Fix 3 handler target: expect 1 hit at 337
grep -n 'func (h \*APIKeyHandler) UpdateStatus' internal/server/handlers/apikeys.go
# Fix 3 web path: expect 1 hit at 335
grep -n 'if key.Status == "revoked" {' internal/server/middleware/auth.go
# Fix 3 ABS path: expect 1 hit at 459
grep -n 'switch key.Status {' internal/server/middleware/absauth.go
# WithPermissions drops an empty slice: expect "if len(perms) == 0 {" at context.go:143
grep -n 'if len(perms) == 0 {' internal/auth/context.go
```

## Steps

1. **Tests first** (below). Run them on the unmodified base; the three named tests must fail (fix 6's race test
   must fail at least once in `go test -race -run TestLegacyRevokeRacesTouch_NeverUnrevokes -count=20`). Record
   the failing output in the PR description.
2. **Fix 6, store.** In `internal/database/pebble_store_auth.go`, take `p.apiKeyMu.Lock(); defer p.apiKeyMu.Unlock()`
   at the top of `CreateAPIKey`, `SetAPIKeyStatus` and `SetAPIKeyExpiry` (as `TouchAPIKeyLastUsed` already does).
   `RevokeAPIKey` calls `SetAPIKeyStatus`, so it must **not** take the lock itself (`sync.Mutex` is not
   re-entrant). Do not edit the `apiKeyMu` field comment in `internal/database/pebble_store.go:231` (that file
   is not in this PR's list); state the lock's new scope in a comment on `SetAPIKeyStatus` instead ("every
   `apikey:` writer takes apiKeyMu; 05-authn §11.1 item 6"). Lock order: `apiKeyMu` is never held while taking another
   store lock (user merge's comment at `user_merge.go:517` records the order); keep it so.
   Cost, stated so a reviewer can check it: `SetAPIKeyStatus` and `SetAPIKeyExpiry` write with `pebble.Sync`, so a
   revoke now holds `apiKeyMu` across one fsync, and key-authenticated requests touching keys wait behind it for
   that fsync. Revokes are rare, the lock is per-family, and the spec requires one lock for every writer. Do not
   downgrade those writes to `NoSync`.
3. **Fix 3, store.** In `SetAPIKeyStatus`, after reading the row and before writing: when `status == "active"` and
   the row has `Status == "revoked"` or `RevokedAt != nil`, return a new exported sentinel
   `database.ErrAPIKeyRevoked` (name coined here; `errors.New("api key is revoked")`, declared next to the other
   auth sentinels in the database package) and write nothing. Every other transition keeps HEAD's behaviour
   (a revoke of a revoked key stays revoked; `RevokedAt` is never cleared by any transition).
4. **Fix 3, handler.** In `UpdateStatus` (`handlers/apikeys.go:337`), after the ownership check and before binding
   the body: when `key.Status == "revoked" || key.RevokedAt != nil`, answer
   `httputil.RespondWithConflict(c, "api key is revoked")` (409) and return. Also map a
   `database.ErrAPIKeyRevoked` returned by `SetAPIKeyStatus` (a revoke that raced the PATCH) to the same 409 with
   `errors.Is`, never to 500.
5. **Fix 3, web key path.** In `handleAPIKeyAuth` (`middleware/auth.go:335`) change the revoked test to
   `if key.Status == "revoked" || key.RevokedAt != nil {` with the same 401 "API key has been revoked". It must run
   before `TouchAPIKeyLastUsed` (it already does), so a revoked key's request writes nothing.
6. **Fix 3, ABS key path.** In `ResolveAPIKey` (`middleware/absauth.go`, before `switch key.Status` at :459) add:
   `if key.RevokedAt != nil { return nil, absErr(http.StatusUnauthorized, "apikey-revoked", "API key has been revoked", ABSModeAPIKey) }`.
7. **Fix 2, handler.** In `Create` (`handlers/apikeys.go:199-209`):
   - drop the `if callerPermSet != nil` guard: a nil set is the empty set for every caller, so every requested scope
     must be in `callerPermSet` (a lookup in a nil map returns "absent", so removing the guard is exactly that);
   - when the caller authenticated with an API key
     (`auth.MethodFromContext(c.Request.Context()) == auth.MethodAPIKey`) and `len(req.Scopes) == 0`, answer 403
     ("an API key must name the scopes of the key it creates") before minting anything. An empty scope list means
     "all of the owner's role permissions" (`middleware/auth.go:405-408`), which a key may never hand out.
   - an interactive (session) caller sending empty scopes keeps HEAD's behaviour.
8. **Run the gate** (Acceptance). If an existing test elsewhere asserted HEAD's defective behaviour (a PATCH that
   re-activates a revoked key, an empty-scope key minted by a key), STOP and report it; do not edit tests outside
   this PR's file list without the lead's approval.
9. **TODO bookkeeping.** The three entries are lines of the fragment
   `todo.d/2026-11-04_platform_core_followups.md` beginning `- [ ] **SECURITY (hotfix, early fix 2):**`,
   `… early fix 3):**`, `… early fix 6):**`. First `grep -n 'SECURITY (hotfix, early fix [236])' TODO.md`: if they
   are there (the daily assembly folded the fragment in), tick them in `TODO.md`; otherwise tick them in the
   fragment. Grep both files before merging (CLAUDE.md "Post-Task Hygiene").
10. **Changelog fragment** (no header): "Security: a revoked API key can no longer be re-activated; a limited API key
    can no longer create an unlimited one; a revocation can no longer be undone by a request that was in flight."
    (the matching lines of 09 §9.4 R0's release note).

## Code contract

```go
// internal/database (name coined here)
var ErrAPIKeyRevoked = errors.New("api key is revoked")

// unchanged signatures; new behaviour:
func (p *PebbleStore) CreateAPIKey(key *APIKey) (*APIKey, error)          // takes apiKeyMu
func (p *PebbleStore) SetAPIKeyStatus(id, status string, at time.Time) error // takes apiKeyMu; ErrAPIKeyRevoked on revoked → active
func (p *PebbleStore) SetAPIKeyExpiry(id string, at time.Time) error      // takes apiKeyMu
```

No interface in `internal/database/iface_auth.go` or `internal/server/handlers/apikeys.go:120-121` changes, so no
mock regeneration is needed.

## Tests to write first (spec names copied exactly)

| Test | File | Proves |
|---|---|---|
| `TestLegacyRevokeRacesTouch_NeverUnrevokes` | `internal/database/pebble_store_auth_race_test.go` | Real `NewPebbleStoreInMemory("db")` store, one key. Repeat ≥ 200 rounds: start N ≥ 8 goroutines calling `TouchAPIKeyLastUsed` in a tight loop, call `RevokeAPIKey` concurrently, stop the touchers, read the row: `Status == "revoked"` and `RevokedAt != nil` every round. Also one round with `SetAPIKeyExpiry` racing the touchers: the expiry written is the one read back. Must pass under `-race`. |
| `TestLegacyAPIKey_RevokedNotReactivated` | `internal/server/handlers/apikeys_earlyfix_test.go` | Subtests over a real in-memory store: (a) handler: PATCH `{"status":"active"}` and `{"status":"inactive"}` on a key with `status=revoked`, and on one with `status=active` but `revoked_at` set, answer 409 and leave the row unchanged; (b) store: `SetAPIKeyStatus(id,"active",…)` on both rows returns `database.ErrAPIKeyRevoked` and writes nothing; (c) web path: `middleware.RequireAuth(store)` with `Authorization: Bearer <abk_ token>` of the `revoked_at`-set row answers 401 and the row's `use_count` is unchanged; (d) ABS path: a resolver built as `internal/server/middleware/absauth_apikey_test.go:71` builds it (`NewABSIdentityResolver(cfg, verifier, nil, store)`, ABS enabled with its JWT mode on), `ResolveAPIKey(c)` returns an `*ABSAuthError` with `Reason == "apikey-revoked"` for the same row. |
| `TestLegacyAPIKeyCreate_EmptyEffectiveKeyCannotMint` | `internal/server/handlers/apikeys_earlyfix_test.go` | A caller authenticated by API key (`auth.MethodAPIKey` on the context) whose effective permission set is empty (no `WithPermissions` call, so `PermissionsFromContext` is nil): POST `/auth/api-keys` with `"scopes": []` → 403 and no key created; with `"scopes": ["library.view"]` → 403 and no key created. Control: a key caller holding `library.view` with `"scopes": ["library.view"]` → 201; a key caller with `"scopes": []` → 403 even when its set is non-empty; a session caller with `"scopes": []` → 201 (HEAD behaviour kept). |
| `TestLegacyAPIKeyAuth_RevokedAtRefusedWhateverStatus` (name coined here) | `internal/server/middleware/auth_revoked_test.go` | Unit level, with the package's fake key store: web path and ABS path each refuse a row with `Status: "active"`, `RevokedAt` set (401; ABS `Reason == "apikey-revoked"`), and accept the same row with `RevokedAt` nil. |

Use the handler test helpers already in `package handlers_test` (`newAuthCtx`, `setAuthUser` in
`internal/server/handlers/auth_test.go`) where they fit; set `auth.WithMethod(ctx, auth.MethodAPIKey)` yourself for
the key-caller cases.

## Invariants (a reviewer checks each)

1. Every write of an `apikey:` row in `internal/database` happens with `apiKeyMu` held (grep: each of the four
   `Set([]byte("apikey:"` sites is inside a function that locks `apiKeyMu`, or inside user merge's locked block).
2. No transition makes a row with `RevokedAt != nil` usable on any path (store, PATCH, web auth, ABS auth).
3. No key-authenticated request mints a key whose scopes are empty or not ⊆ the caller's effective set; a nil caller
   set never skips the subset check for any caller.
4. No session-caller behaviour changes except that a nil permission set no longer skips the subset check.

STOP conditions: an edit-target grep returns 0 hits or a different line; a pre-existing test asserts the defective
behaviour; the race test cannot be made to fail on the base (report the attempts; do not ship an unproven test).

## Acceptance

```bash
go build ./...                                                                    # exit 0
go vet ./internal/database/ ./internal/server/...                                 # exit 0
go test -race -count=1 ./internal/database/ -run 'TestLegacyRevokeRacesTouch_NeverUnrevokes'           # ok
go test -race -count=1 ./internal/server/handlers/ -run 'TestLegacyAPIKey_RevokedNotReactivated|TestLegacyAPIKeyCreate_EmptyEffectiveKeyCannotMint'   # ok
go test -race -count=1 ./internal/server/middleware/ -run 'TestLegacyAPIKeyAuth_RevokedAtRefusedWhateverStatus'  # ok
go test -race -count=1 ./internal/database/ ./internal/server/handlers/ ./internal/server/middleware/ ./internal/server/   # ok (all packages)
grep -c 'apiKeyMu.Lock()' internal/database/pebble_store_auth.go                  # expect 4
grep -c 'if callerPermSet != nil' internal/server/handlers/apikeys.go             # expect 0
make ci; echo "exit $?"                                                           # expect exit 0
```

The e2e suite and every required check of 09 §9.2.1 are green on the PR on an up-to-date branch.

## Out of scope

- Early fixes 1, 4, 5 (R0-04, R0-03, R0-05); `RequireAdmin` (R0-03).
- Core auth, `Expect`, `mck_` keys and the ported core tests `TestRevokeRacesTouch_NeverUnrevokes`,
  `TestAPIKey_RevokedIsTerminal`, `TestAPIKey_EmptyEffectiveKeyCannotMint` (R2, F4).
- The revocation journal (05-authn §11.1 last paragraph: it ships with core's keyring in R2).
- Changing the `pebble.Sync`/`NoSync` choice of any write.

## Rollback

Revert the commit in a new PR. No stored data changes shape; a row refused by the new checks was already revoked.

## Repo rules

- File headers bumped (version and last-edited) on every modified file; new Go files get the 4-line header with a
  fresh guid; `changelog.d/`/`todo.d/` fragments carry no header.
- Conventional commit, e.g. `fix(auth): serialize apikey writers, keep revoked keys revoked, no key from an empty set`,
  ending `Co-Authored-By: Claude <noreply@anthropic.com>`. No release or flip trailer (09 §9.2.1 names none).
- `gh pr merge <n> --rebase` after every required check is green; never squash, never `--admin`.
- CLAUDE.md concurrency: this PR adds no loop over a library-scale collection; the race test's goroutine count is
  fixed and bounded.
- Remove the worktree after merge.
