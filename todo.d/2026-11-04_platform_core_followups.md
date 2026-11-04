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
  - [ ] **SECURITY (hotfix, early fix 1):** session tokens exposed by `GET /api/v1/auth/sessions`. Implement 05-authn §11.1 item 1 exactly
        (`docs/specs/platform-core/05-authn.md`); it is the single list and this entry restates nothing.
  - [ ] **SECURITY (hotfix, early fix 2):** a scoped API key can mint an unscoped key (including the nil/empty caller set). Implement 05-authn §11.1 item 2 exactly
        (`docs/specs/platform-core/05-authn.md`); it is the single list and this entry restates nothing.
  - [ ] **SECURITY (hotfix, early fix 4):** `RequireAdmin` and the role-name checks ignore API-key scopes (including `adminRole`, `handlers/user.go` and `mergeActor`). Implement 05-authn §11.1 item 4 exactly
        (`docs/specs/platform-core/05-authn.md`); it is the single list and this entry restates nothing.
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
  - [ ] **SECURITY (hotfix, early fix 3):** a revoked API key can be re-activated (web and ABS key paths). Implement 05-authn §11.1 item 3 exactly
        (`docs/specs/platform-core/05-authn.md`); it is the single list and this entry restates nothing.
  - [ ] **SECURITY (hotfix, early fix 6):** a request with a key can undo that key's revocation (`apiKeyMu` on every `apikey:` writer). Implement 05-authn §11.1 item 6 exactly
        (`docs/specs/platform-core/05-authn.md`); it is the single list and this entry restates nothing.
  - [ ] **SECURITY (hotfix, early fix 5):** the Basic gate accepts an empty password (with the `BASIC_AUTH_PASSWORD` recovery). Implement 05-authn §11.1 item 5 exactly
        (`docs/specs/platform-core/05-authn.md`); it is the single list and this entry restates nothing.
