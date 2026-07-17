<!-- file: docs/agent-tasks/consultancy-roadmap/TASK-09-apikey-rotation-expiry.md -->
<!-- version: 1.0.0 -->
<!-- guid: 771afe2f-e822-48a4-884f-7422cdd498c0 -->
<!-- last-edited: 2026-07-03 -->

# TASK-09 — API-key rotation + expiry for bootstrap-issued keys (SEC-1 / PROC-6)

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Sonnet · go-backend subagent · **Depends on:** none

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/cr-09-apikey-rotation-expiry" -b agent/cr-09-apikey-rotation-expiry origin/main
cd "$REPO/.worktrees/cr-09-apikey-rotation-expiry"
git rebase origin/main
```

## Goal

Close SEC-1 / PROC-6: bootstrap-issued full-scope API keys currently never
expire and there is no rotation workflow. Add (1) a config-driven TTL applied
to bootstrap-issued keys, (2) a `POST /api/v1/auth/api-keys/:id/rotate`
endpoint that issues a new key and lets the old one keep working for a short
grace window instead of being killed instantly, and (3) a low-frequency
background sweep that logs a `slog.Warn` for keys approaching expiry (and a
one-time-per-process deprecation warning for legacy keys that have no
expiry at all). Treat existing prod keys with `ExpiresAt == nil` as
**legacy-valid — never lock them out**; the deprecation warning is
observability only, not enforcement.

## Background (verify before editing)

- **Bootstrap issuance has no expiry today.** `handleBootstrap` in
  `internal/server/bootstrap.go` builds the `database.APIKey{}` literal with
  `ID`/`UserID`/`Name`/`Description`/`TokenHash`/`Scopes`/`Status`/`CreatedAt`
  only — no `ExpiresAt` field is set, so every bootstrap exchange mints a
  permanent full-scope (`auth.All()`) key. Confirm before editing:
  ```bash
  grep -n "func (s \*Server) handleBootstrap" internal/server/bootstrap.go
  grep -n "scopes := auth.All()" -A 12 internal/server/bootstrap.go
  ```
  As of this writing that's around lines 277 (func start) and 338-349 (the
  `key := &database.APIKey{...}` literal) — **re-verify, don't trust these
  numbers.**

- **Expiry enforcement in the auth middleware ALREADY EXISTS** — this part of
  the consultancy finding's authoritative direction is already implemented,
  do not re-add it. `handleAPIKeyAuth` in
  `internal/server/middleware/auth.go` already checks:
  ```bash
  grep -n "key.ExpiresAt != nil && time.Now().After" internal/server/middleware/auth.go
  ```
  and responds `401` with body `"API key has expired"` via
  `httputil.RespondWithUnauthorized`. **Do not duplicate this check** — the
  only gap is that bootstrap-issued keys never get an `ExpiresAt` set in the
  first place, so the existing check never fires for them.

- **`database.APIKey` already has `ExpiresAt *time.Time`** (`internal/database/store.go`
  around line 618) and `handlers.Create` (`internal/server/handlers/apikeys.go`,
  `expires_in_days` request field, ~line 173-176) already shows the pattern for
  computing an expiry: `time.Now().Add(time.Duration(req.ExpiresInDays) * 24 * time.Hour)`.
  Reuse that exact pattern for the TTL config value.

- **No rotation endpoint exists.** `internal/server/handlers/apikeys.go` has
  `Create`, `List`, `Get`, `UpdateStatus`, `Revoke` (`DELETE /:id`, ~line 314)
  but nothing that atomically issues-new + retires-old. Routes are wired in
  `internal/server/wire_auth_routes.go`:
  ```bash
  grep -n "api-keys" internal/server/wire_auth_routes.go
  ```
  `RevokeAPIKey` (`internal/database/pebble_store.go`, ~line 6031) sets
  `Status = "revoked"` **immediately** — that's too abrupt for a grace window
  (the auth middleware aborts on `Status == "revoked"` right away, see
  `internal/server/middleware/auth.go` ~line 189). For the grace window, set
  the OLD key's `ExpiresAt` to `now + grace` instead of revoking it outright,
  so the existing (already-implemented) expiry check in the middleware
  retires it naturally. There is currently no store method that updates only
  `ExpiresAt` on an existing key — you will need to add one.

- **`APIKeyStore` interface** (`internal/database/iface_misc.go` ~line 73) —
  re-verify the current method set:
  ```bash
  grep -n "type APIKeyStore interface" -A 12 internal/database/iface_misc.go
  ```
  `PebbleStore.SetAPIKeyStatus` (`internal/database/pebble_store.go` ~line
  6035) is a read-modify-write you can pattern-match for the new
  `SetAPIKeyExpiry(id string, at time.Time) error` method.

- **No existing config field for a bootstrap-key TTL.** `internal/config/config.go`
  has `MetadataFetchCacheTTLDays` (~line 341) as the closest style precedent
  (comment explaining default + 0-means-never-expire convention). Add a
  sibling field, e.g. `BootstrapKeyTTLDays int` with default `30`, following
  the same viper-binding pattern used for `MetadataFetchCacheTTLDays`:
  ```bash
  grep -n "MetadataFetchCacheTTLDays" internal/config/config.go
  ```
  (appears in the struct, the viper-load block, and the defaults block —
  three sites to mirror.)

- **Background sweep placement.** `Server.Start` in
  `internal/server/server_lifecycle.go` already kicks off several
  fire-and-forget cache warmers as goroutines right after HNSW load:
  ```bash
  grep -n "go s.warmFacetsCache\|go s.warmAuthorsCache\|go s.warmSeriesCache" internal/server/server_lifecycle.go
  ```
  (around lines 266-278). Add the new sweep goroutine in the same block.
  `internal/server/library_list_warmer.go` is a good structural model for a
  ticker-driven background loop with `slog.Warn` logging
  (`runTrickleWarmer`, ~line 613, uses `time.NewTicker`).

- **Mocks.** `internal/database/mock_store.go` implements `APIKeyStore` for
  tests (`CreateAPIKey` ~1588, `RevokeAPIKey` ~1623, `SetAPIKeyStatus` ~1630).
  Any new interface method needs a mock implementation too, or `go build`
  breaks every package that uses `MockStore` as `database.Store`.

## Step-by-step

1. **Config TTL.** In `internal/config/config.go`, add `BootstrapKeyTTLDays int
   json:"bootstrap_key_ttl_days"` next to `MetadataFetchCacheTTLDays`, with a
   doc comment stating default 30, and `0` or negative falls back to 30 (never
   "never expire" — bootstrap keys are full-scope, they must always expire).
   Wire it through the viper-load block and the defaults block exactly where
   `MetadataFetchCacheTTLDays` appears (grep above), defaulting to `30`.

2. **Apply TTL at bootstrap issuance.** In `internal/server/bootstrap.go`,
   inside `handleBootstrap`, compute
   `ttlDays := config.AppConfig.BootstrapKeyTTLDays; if ttlDays <= 0 { ttlDays = 30 }`
   and set `ExpiresAt: &expiresAt` on the `database.APIKey{}` literal, where
   `expiresAt := time.Now().Add(time.Duration(ttlDays) * 24 * time.Hour)`.
   Also add `expires_at` to the `bootstrapResp` JSON so callers (including the
   `server-bootstrap` skill) can see it. Do not change any other field on the
   literal.

3. **Add `SetAPIKeyExpiry` to the store layer.**
   - `internal/database/iface_misc.go`: add `SetAPIKeyExpiry(id string, at
     time.Time) error` to `APIKeyStore`.
   - `internal/database/pebble_store.go`: implement it as a read-modify-write
     next to `SetAPIKeyStatus` — load the key, set `k.ExpiresAt = &at`, marshal,
     write back with the same `apikey:` key (do not touch the `idx:` entries).
   - `internal/database/mock_store.go`: add a matching mock implementation
     (store the value in the mock's in-memory map, mirroring how
     `SetAPIKeyStatus` is mocked there).

4. **Rotation endpoint.**
   - `internal/server/handlers/apikeys.go`: add `Rotate(c *gin.Context)`
     handling `POST /auth/api-keys/:id/rotate`. Logic:
     - Require caller auth (`servermiddleware.CurrentUser`), same ownership
       check as `Revoke` (owner or `isAdminUser`).
     - Load the old key via `h.store.GetAPIKey(id)`; 404 if missing.
     - Generate a new token (`database.GenerateAPIKeyToken()`), build a new
       `database.APIKey` copying `UserID`, `Name` (suffix e.g. `" (rotated)"`
       optional — keep simple, just copy `Name`), `Description`, `Scopes`
       from the old key, `Status: "active"`, and a fresh `ExpiresAt` computed
       the same way as bootstrap issuance (reuse the `BootstrapKeyTTLDays`
       config value if the old key has no custom expiry pattern — simplest:
       always use `BootstrapKeyTTLDays`-based expiry for the new key).
       Persist via `h.store.CreateAPIKey`.
     - Set the OLD key's `ExpiresAt` to `time.Now().Add(graceWindow)` via the
       new `SetAPIKeyExpiry` store method — **do not call `RevokeAPIKey`**,
       that would kill it immediately instead of giving a grace window. Use a
       package-level const `const apiKeyRotationGraceWindow = 1 * time.Hour`.
     - Respond `201`/`200` with the new raw token (shape similar to
       `CreateAPIKeyResponse`), and log at `slog.Info` (id, user, old key id,
       grace window) mirroring the existing `"apikey created"` / `"apikey
       revoked"` log lines in this file.
   - `internal/server/wire_auth_routes.go`: add
     `authProtected.POST("/api-keys/:id/rotate", apiKeyH.Rotate)` next to the
     existing `api-keys` routes.

5. **Expiry-approaching WARN sweep.**
   - Add a new file `internal/server/apikey_expiry_sweep.go` (or a function
     in `server_lifecycle.go` if you prefer — pick one and keep the file
     header correct either way) with `func (s *Server)
     warnExpiringAPIKeys()`. Pattern it after `runTrickleWarmer`'s
     ticker loop in `internal/server/library_list_warmer.go`:
     - Ticker interval: something coarse like 6h (this is observability, not
       a hot path).
     - Each tick: `keys, err := s.Store().ListAllAPIKeys()`; for each key with
       `Status == "active"`:
       - If `ExpiresAt == nil`: log `slog.Warn` **once per key ID per process**
         (keep a `map[string]bool` of already-warned IDs in the sweep's
         closure state, guarded by a mutex or just owned by the single
         goroutine) — message like `"api key has no expiry (legacy) — set one via rotate"`,
         fields `key_id`, `user_id`, `name`. This is the deprecation warning;
         it must NEVER cause the key to be rejected — enforcement stays solely
         in the untouched middleware `ExpiresAt != nil` branch.
       - If `ExpiresAt != nil` and within a threshold window (e.g. 7 days) of
         now and not yet expired: log `slog.Warn` with fields `key_id`,
         `user_id`, `name`, `expires_at`. Track "already warned this window"
         similarly to avoid spamming every 6h.
   - Wire `go s.warnExpiringAPIKeys()` into `Server.Start` in
     `internal/server/server_lifecycle.go`, next to the other `go
     s.warm*Cache()` calls (grep above for the exact insertion point).

6. **Tests.**
   - `internal/server/bootstrap_test.go` (or wherever bootstrap tests live —
     `grep -rl "handleBootstrap\|TestBootstrap" internal/server/*_test.go`):
     assert the created key has a non-nil `ExpiresAt` roughly `TTLDays` days
     in the future.
   - `internal/database/*_test.go`: test `SetAPIKeyExpiry` round-trips
     through `PebbleStore` (create key, set expiry, `GetAPIKey`, assert
     `ExpiresAt` matches).
   - `internal/server/handlers/apikeys_test.go` (check it exists: `ls
     internal/server/handlers/apikeys_test.go`): test `Rotate` — old key
     still has `Status == "active"` but `ExpiresAt` is now within the grace
     window (not nil, not far-future); new key exists with fresh `ExpiresAt`
     and inherited scopes.
   - Confirm the existing auth-middleware expiry test still passes unchanged
     (do not modify `internal/server/middleware/auth_test.go` unless a new
     legacy-nil-expiry case is worth adding there — optional, not required).

7. **Skill doc.** `.claude/skills/server-bootstrap/SKILL.md`'s example
   bootstrap response (the `Bootstrap Token Exchange` section) does not show
   `expires_at`. Add it to the example JSON response so the doc matches the
   new field. Do not change the 8-hour client-side `.claude/.api-token`
   cleanup convention — that is unrelated to the new server-side key TTL
   (client cleanup is much shorter than the 30-day server TTL, so no
   conflict).

8. Bump the file header (version + `last-edited`) on every file touched, per
   `.standards/instructions/file-headers.md`.

## How to test

```bash
go build ./...
go vet ./internal/server/... ./internal/database/... ./internal/config/...
go test ./internal/server/... ./internal/database/... ./internal/config/... -count=1
```

## Acceptance criteria

- [ ] Bootstrap-issued keys (`handleBootstrap`) get a non-nil `ExpiresAt`
      derived from `config.AppConfig.BootstrapKeyTTLDays` (default 30d if
      unset/non-positive).
- [ ] `bootstrapResp` JSON includes `expires_at`.
- [ ] `POST /api/v1/auth/api-keys/:id/rotate` exists, requires auth, enforces
      the same ownership rule as `Revoke` (owner or admin), issues a new key
      with inherited scopes, and puts the OLD key on a short grace-window
      expiry (via `SetAPIKeyExpiry`) rather than immediate revocation.
- [ ] `SetAPIKeyExpiry` is added to `APIKeyStore`, implemented in
      `PebbleStore`, and mocked in `MockStore` — `go build ./...` is clean.
- [ ] A background sweep logs `slog.Warn` for active keys approaching expiry
      and for legacy active keys with `ExpiresAt == nil`, without rejecting or
      modifying those keys (enforcement remains only in the pre-existing
      middleware `ExpiresAt != nil && time.Now().After(...)` check — verify
      that check is unchanged: `git diff origin/main -- internal/server/middleware/auth.go`
      should be empty for this task).
- [ ] Existing keys with `ExpiresAt == nil` (legacy prod keys) are NOT
      rejected by anything added in this task — confirmed by test and by the
      unchanged middleware diff above.
- [ ] `.claude/skills/server-bootstrap/SKILL.md` example response shows
      `expires_at`.
- [ ] All new/updated tests pass; `go build ./...`, `go vet`, and `go test`
      (scoped packages above) are green.
- [ ] File headers bumped on every changed file.

## Commit message

```
feat(auth): add TTL + rotation for bootstrap-issued API keys (SEC-1/PROC-6)

Bootstrap-issued full-scope keys never expired and had no rotation path.
Add a config-driven TTL applied at bootstrap issuance, a rotate endpoint
that grace-windows the old key instead of an abrupt revoke, and a
background sweep that WARN-logs keys nearing expiry or lacking one
(legacy keys stay valid — this is observability only). The auth
middleware's expiry enforcement already existed and is unchanged.

Co-Authored-By: Claude <model> <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/cr-09-apikey-rotation-expiry
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

Before starting, re-verify the premise — if `handleBootstrap` already sets
`ExpiresAt` on the issued key (`grep -n "ExpiresAt" internal/server/bootstrap.go`
shows a hit inside the `database.APIKey{}` literal, not just in
`ConsumeBootstrapToken`'s own struct), SEC-1's core gap is already closed;
only the rotation endpoint and WARN sweep would remain to build. If a
`/rotate` endpoint already exists (`grep -n "rotate" internal/server/wire_auth_routes.go
internal/server/handlers/apikeys.go`), skip step 4. If a `warnExpiring*`
sweep already exists (`grep -rn "warnExpiring\|ExpiresAt == nil" internal/server/*.go`),
skip step 5.

**Migration safety:** this task must never cause existing prod keys (which
have `ExpiresAt == nil`) to be rejected — the middleware's enforcement branch
(`internal/server/middleware/auth.go`, `key.ExpiresAt != nil && ...`) already
treats `nil` as "never expires" and this task does not change that branch.
Do not add any code path that treats a `nil` `ExpiresAt` as "expired" or
otherwise locks out legacy keys — the WARN sweep is log-only.

Rollback = revert the commit. `BootstrapKeyTTLDays` config addition is
additive (defaults preserve prior behavior only if you also revert the
bootstrap.go change — reverting the whole commit restores true
never-expiring bootstrap keys). `SetAPIKeyExpiry` and the `/rotate` route are
new additions with no other callers, safe to remove wholesale.
