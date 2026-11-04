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
