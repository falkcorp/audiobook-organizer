- [ ] **PLATFORM-CORE-FOLLOWUPS** Defects found during the 2026-11-04 platform-core planning scouts
      (evidence in `docs/plans/platform-core/raw/`; out of scope for the docs-only planning PRs):
  - [ ] Add `exclude google.golang.org/genproto v0.0.0-20230410155749-daa745c078e1` to the root
        `go.mod` and rewrite the CLAUDE.md "Do NOT run `go work init .`" note: the only requirer of the
        stale monolithic genproto is `github.com/cockroachdb/errors@v1.14.0`; with the exclude,
        `go work init .` builds and vets green and `go.sum` is unchanged (`raw/00-baseline-build.md` E7–E9).
  - [ ] Backup copies only the Pebble checkpoint (not bleve, hnsw, or the storage-format sidecar) and
        the restore handler never closes or reopens the live store (`raw/05-storage.md` §5).
  - [ ] `PluginRouter` is mounted on `s.router.Group("/api/v1/plugins")`, outside the authenticated
        `protected` group (`internal/server/plugins_init.go:42`); latent because no plugin registers
        routes yet (`raw/10-auth-rbac.md` G8).
  - [ ] `OperationDef.Schedule` cron strings are never evaluated, so `deluge.protected-paths-sync`
        never runs unattended; `OperationDef.Cancellable` is never consulted (`raw/06-tasks-ops.md` §5).
  - [ ] Several secrets (acoustid key, OAuth client secrets, Deluge password, metadata-source
        credentials) appear to be stored in the plaintext config blob; sessions are stored unhashed
        (`raw/10-auth-rbac.md` §7, G12).
  - [ ] Stale numbers/references: `database.Store` has 445 methods (CLAUDE.md and code comments say
        398); the storage-format guard references a nonexistent `RunCutover`; `--db-type` help still
        offers "sqlite" (`raw/04-database-store.md` §1, `raw/05-storage.md` §4.2).
  - [ ] `RunItems` doc promises the first error under `ErrModeFail` but parallel mode returns
        `errors.Join` (`raw/06-tasks-ops.md` §2).
  - [ ] **SECURITY (hotfix, do not wait for the extraction):** `GET /api/v1/auth/sessions` returns
        `database.Session` including `id`, which is the bearer session token
        (`internal/server/handlers/auth.go:525-536`, `internal/database/store.go:1044-1045`); the route
        has no permission or credential guard, so any API key of the user can read and replay the
        user's session tokens and gain session-level credential rights. Return a view without `id`.
  - [ ] **SECURITY (hotfix):** a scoped API key can mint an unscoped key: `POST /api/v1/auth/api-keys`
        with `"scopes": []` skips the subset check (`internal/server/handlers/apikeys.go:199-210`) and
        empty scopes mean "all role permissions" (`internal/server/middleware/auth.go:405-408`).
        Refuse empty scopes when the caller is an API key with non-empty scopes.
  - [ ] **SECURITY (hotfix):** `RequireAdmin` checks role names and ignores API-key scopes
        (`internal/server/middleware/auth.go:522-537`), so a key scoped to `library.view` on an admin
        account passes admin-gated routes not also behind `credGuard`.
  - [ ] **SECURITY:** 3 of the 4 encrypted config secrets are also written in plaintext to a second
        `config.yaml` (see `docs/specs/platform-core/05-config.md`, "Out-of-scope defects found").
  - [ ] Invite role not bounded by the creator's permissions (`internal/server/handlers/user.go:155-190`);
        latent while only `admin` holds `users.manage`.
  - [ ] Deluge path-update trigger never fires; shutdown phases can total 45 s against a 30 s stop
        limit; updater's expected asset name does not match the goreleaser archives
        (`docs/specs/platform-core/05-tasks.md`, `05-updater.md` "Out-of-scope defects found").
  - [ ] 8 writes to `config.AppConfig` bypass `Mutate`; in `serve` the log file stops receiving output
        after `NewServer`; `log_level` is never applied to the handler (`05-config.md`, `05-logging.md`).
  - [ ] The shipped systemd unit sets `AUDIOBOOK_ROOT_DIR`, which nothing reads (`09-abo-on-core.md`).
  - [ ] **SECURITY (hotfix):** a revoked API key can be re-activated: `PATCH /api/v1/auth/api-keys/:id`
        with `{"status":"active"}` (`internal/server/handlers/apikeys.go:336-372`) calls
        `SetAPIKeyStatus`, which overwrites `Status` to `active` without checking `RevokedAt`
        (`internal/database/pebble_store_auth.go:1226-1247`). Refuse any status change on a revoked key
        (and confirm the auth path also checks `RevokedAt`).
  - [ ] **SECURITY (hotfix):** a revocation can be silently undone by a concurrent request using the
        key: `TouchAPIKeyLastUsed` reads and rewrites the whole `apikey:` row under `apiKeyMu`
        (`internal/database/pebble_store_auth.go:1284-1302`), but `SetAPIKeyStatus` writes the row
        without taking that lock (`:1226-1247`), so an in-flight touch can write back the pre-revocation
        row. Take `apiKeyMu` in every `apikey:` writer (or write `last_used` to a separate key).
  - [ ] **SECURITY (hotfix):** an enabled Basic gate with an empty stored password accepts the configured
        username with an empty password (`internal/server/middleware/basicauth.go:81-87` compares `""` to
        `""`). Refuse all Basic credentials while the gate is enabled and no password is set (fail closed).
  - [ ] **Security (low):** a scope-restricted API key whose owner has the admin role can read
        `GET /users/:id/footprint` and `GET /users/merges` (`internal/server/handlers/user_merge.go:97-99,190-192`):
        the handlers check the owner's role name, not the key's scopes. The merge POSTs are already refused to API
        keys (credential routes). Check `Can(core:admin)` on the request principal (or the key's scopes) instead.
