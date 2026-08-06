<!-- file: docs/architecture/server-plugin-registry-handoff.md -->
<!-- version: 1.2.0 -->
<!-- last-edited: 2026-05-13 -->

# SERVER-PLUGIN-REG — Handoff Brief

> **Use this as the opening prompt for a fresh AI session continuing the
> plugin migration.** It is intentionally short — read the three companion
> docs only as you reach the tickets that need them.

## Repo context

Go + React audiobook organizer. The host (`internal/server`) historically
built every service in a 968-line `NewServer`. We're partway through a
migration to a per-instance service container (`internal/serviceregistry`)
where domain packages self-register via `init()` and `NewServer` just
asks the container to build them.

Production data lives on `<server>` (PebbleDB at
`/var/lib/audiobook-organizer/audiobooks.pebble`). **Always use
`make deploy` after merging server-side changes** — `Makefile.local`
builds, scp's, and restarts via systemd.

## State as of 2026-05-13 (PM2 update)

- All 7 waves of the original SERVER-PLUGIN-REG plan landed.
- **SERVER-LIFECYCLE-FLIP done** (PR #901): `Container.Start` /
  `Container.Stop` drive lifecycle. Fixed previously-leaked
  `updateScheduler.Stop`. One holdout: `searchindex` (see ticket #3).
- **~45 services** registered across the 5 named groups: `core`, `ai`,
  `scheduler`, `plugins`, `activity`.
- `NewServer` is **388 lines** (target: ≤50). Down from 968 historic /
  462 at this morning's session start. 11 PRs (#890-#901) shipped today.
- 0 open PRs. `go vet ./...` clean. `go build` clean.
- **PRs #890 — #899 (10 PRs)** were shipped today moving inline blocks
  into the container + PostInit hooks:
  - #890 itunesservice container-built
  - #891 writebackbatcher stub promoted
  - #892 iTunes plugin stub promoted (PostInit registers ops)
  - #893 activitywriter pulled from container
  - #894 updater + scheduler container-built (appversion Override)
  - #895 activity Set wiring moved into 3 services' PostInit hooks
  - #896 WriteBackBatcher.Start/Stop match Starter/Stopper; fan-out
    into metafetch/merge/quarantine/audiobook PostInit
  - #897 olservice + ISBN enrichment via metafetch.PostInit
  - #898 aiscanstore + pipelinemanager via container
  - #899 metricsstore via container
  - #901 SERVER-LIFECYCLE-FLIP — Container.Start/Stop drive lifecycle;
    search.IndexService exported
- Pre-existing test failures to ignore: **SERVER-THIN-8** —
  `TestITunesImport_*`, `TestE2E_ITunesImportOrganizeWriteBack`,
  `TestOrganizeService_ViaHTTP`, `TestAddImportPathAutoScan`,
  `TestStartScanOperation`, `TestStartOrganizeOperation`. These timeout
  at 15s and have been broken on `main` since before the migration started.

## Key design decisions (don't relitigate)

1. **Event bus dispatch is async + panic-isolated + decoupled contexts.**
   Subscribers get the bus's lifecycle ctx, not the publisher's.
   `plugin.EventBus.Publish` already wraps each subscriber goroutine in
   `recover()`.
2. **NewServer uses explicit phases**: `Resolve → Build → PostInit` happen
   in `NewServer`; `Start/Stop` belong to `Server.Start`/`Server.Shutdown`.
3. **`database.GetGlobalStore` → `GetGlobalStoreForTest` rename** is the
   bridge state; full deletion (`TEST-GLOBAL-STORE-MIGRATION`) waits on
   `SERVER-GLOBAL-STORE-AUDIT`.
4. **`Container.IncludeGroup(names ...string)`** is the production
   inclusion API. Each `ServiceDef` has a `Groups []string` field. Explicit
   `Container.Include(name)` stays for tests and ad-hoc additions.

## Companion docs (in `docs/architecture/`)

- `server-plugin-registry-design.md` — original design rationale
- `server-plugin-registry-plan.md` — original 7-wave plan
- **`server-plugin-registry-deferred-work.md`** — the live work plan;
  read this before starting any ticket

## What's left

In dependency order. Estimated sizes assume one focused engineer.

### 1. ~~PLUGIN-DECOUPLE-SERVER-CLOSURES (remaining work)~~ — DONE (PR #890)

### 2. ~~PROMOTE-STUB-REGISTRATIONS~~ — partially DONE
- writebackbatcher: PR #891 (promoted to real adapter), then PR #896
  (adapter removed; *WriteBackBatcher registered directly after Start/Stop
  signature align).
- iTunes plugin: PR #892 (PostInit registers ops).
- Maintenance plugin: **STILL DEFERRED**. `ServerDeps` is a 25-method
  interface delegating to `*Server` closures over `bgCtx`/`bgWG`/
  package-private helpers. Needs a multi-PR wave decomposing the `runX`
  helpers into standalone services before this can build for real.

### 3. ~~SERVER-LIFECYCLE-FLIP~~ — DONE (PR #901)

`Container.Start(s.bgCtx)` runs at the top of `Server.Start`;
`Container.Stop(...)` is a tail-call at the end of `Server.Shutdown`.
Five inline lifecycle calls removed:

| Removed | Driven by |
|---|---|
| `s.itunesSvc.Start(s.bgCtx)` | Container.Start |
| `s.opRegistry.Start(s.bgCtx)` | Container.Start |
| `server.updateScheduler.Start()` | Container.Start |
| `aw.Start(context.Background())` | Container.Start |
| `s.activityWriter.Stop(...)` | Container.Stop |

Fixed a previously-leaked `updateScheduler.Stop` (was never called →
goroutine leak per restart cycle). `TestServerStartGracefulShutdown`
now logs `"Auto-update scheduler stopped"` proving it fires.

**One service intentionally still uses an inline path:** `searchindex`.
`search.IndexService.Start/Stop` are no-ops; `OpenInline()` is called
from `server_lifecycle.go` and `s.searchIndex.Close()` stays inline.
Reason: folding the Bleve open into `Container.Start` exposes a
pre-existing race between the `stripMovementAtoms` /
`remuxMalformedM4BFiles` goroutines (which read `s.store`) and the
`indexedStore` decorator install at `server_lifecycle.go:396`. Documented
in `IndexService.Start` docstring. Fixing the underlying race (move
`s.store = wrapped` to before the goroutines launch) unblocks the
collapse — it's a separate, small PR.

**Other inline Stops stay inline** because they need specific ordering
relative to `bgCancel` / store close: `writeBackBatcher.Stop`,
`opRegistry.Shutdown`, `itunesSvc.Shutdown`, `searchIndex.Close`,
plugin teardown. Each is idempotent; the `Container.Stop` tail-call
double-calls them safely.

### 4. W7.1 NEWSERVER-TRIM — M (in progress)
**Status**: NewServer 462 → 390 lines (-72 net across PRs #890-#899).

Remaining inline blocks all have documented coupling reasons:
- **Auto-organize closure** (`server.scanService.AutoOrganizeFn`, ~40
  lines) — captures `server.store` which the search-index decorator
  rewrites in `Server.Start`. Moving the closure into
  `scanner.PostInit` with container's `store` would skip the indexed
  wrapper and break search reindex on auto-organize writes.
- **Organize collision hook** (~5 lines) — `serverOrganizeHooks`
  captures `*Server` (`bgCtx` + `bgWG` + cross-service helpers).
- **Embedding backfill goroutine** (~7 lines) — wants `bgWG.Add(1)` for
  shutdown coordination; couldn't move to a Starter without exposing
  the wait-group to the dedup package.
- **Activity-log residual** (~25 lines) — `log.SetOutput`,
  `scanner.SetScanHooks`, `server.itunesActivityFn` closure, startup
  ActivityEntry. All process-global or server-internal.
- **ProtectedPathCache block** (~30 lines) — deluge.GetClient() global,
  `metadata.SetSafeWriteDeps` global, `LibraryImporterAdapter` wraps
  resolvedStore.
- **AI scan store back-fill** (~3 lines) — extraOpsRegistrar isn't
  itself a container service.
- **Server-package services** (`OrganizeService.ScanEnqueuer`,
  `ImportService.SetTrackProvisioner`, etc.) — closures over server
  fields or in the `server` package directly.

Hitting ≤50 lines requires:
- Either moving extraOpsRegistrar + OrganizeService into the container
  (~50 lines saved between them), or
- Folding the auto-organize closure into a scanner-package method that
  takes the *currently-active store* via accessor (refactor of
  ScanService.AutoOrganizeFn signature), or
- LIFECYCLE-FLIP (item #3) which deletes the inline Start/Stop calls.

### 5. SERVER-GLOBAL-STORE-AUDIT — XL (parallel-safe)
~120 production callers of `database.GetGlobalStore()`. Migrate
per-package, one PR at a time. Order (smallest first):
1. `cmd/seed.go`, `cmd/dedup_bench.go`, `cmd/diagnostics.go`
2. `internal/itunes/rebuild.go`, `internal/backup/backup.go`,
   `internal/database/store.go`
3. `internal/server/server_*.go` (~30 across files)
4. `internal/metafetch/helpers.go`, `internal/organizer/organizer.go`
5. `internal/audiobooks/helpers.go`
6. `internal/scanner/scanner.go` (35 — biggest, save for last)

Pattern: change function signature to accept `store database.Store`
explicitly; propagate to callers. Tests keep working via the existing
`GetGlobalStore`/`SetGlobalStore` (rename to `*ForTest` after the audit
is done).

### 6. TEST-GLOBAL-STORE-MIGRATION — M
After #5: migrate the ~289 test sites from `SetGlobalStoreForTest(mock)`
to per-test container construction
(`serviceregistry.NewContainer().Override("store", mockStore)`). Then
delete the test globals entirely.

## How to ship work in this repo

```bash
# Branch + make changes
git checkout -b refactor/<ticket-slug>
# ... edits + tests ...
go vet ./...
go build ./...
go test ./internal/<affected>/... -short -race -timeout=60s

# Commit + push + PR + admin-merge
git push -u origin refactor/<ticket-slug>
gh pr create --base main --head refactor/<ticket-slug> --title "..." --body "..."
gh pr merge <N> --rebase --admin --delete-branch
git checkout main && git pull --ff-only origin main
make deploy  # if server-side changes; runs from primary checkout
```

This repo enforces **rebase/FF only** — never use `--squash`. The user
admin-merges with `--admin` regularly; you may do the same.

## User preferences (learned this session)

- **Terse > verbose.** Don't pad responses with insights/recaps unless
  asked. Short status updates between tool calls only.
- **No flattery or affirmations.** Skip "great choice!", "perfect!", etc.
- **Honest about deferred work.** Don't claim "feature complete" if real
  blockers remain — document the blocker, propose a path, move on.
- **Push forward.** When given a goal like "until done", keep working
  through tickets in dependency order. Don't pause for confirmation on
  each one.
- **PR descriptions earn their length.** Spell out per-file changes, why
  each is safe (often a grep that confirms zero callers), what was kept
  inline and why. The user reviews these.
- **Stub-registrations are documented bridges**, not failures. If a real
  blocker exists, note it in the file header + open a follow-up ticket.

## Gotchas

1. **Test-mode `MockStore` doesn't expect `UpsertOpDefinitionV2`.** If you
   move a `Plugin.Register(opRegistry)` into PostInit, make sure the
   plugin's Build returns nil when `cfg.RootDir == ""` (test path) —
   otherwise PostInit triggers the unexpected mock call. PR #888 has
   the canonical guard pattern.
2. **macOS `t.TempDir()` returns `/var/folders/...` which is a symlink
   to `/private/var/folders/...`.** Any path-validation that uses
   `filepath.EvalSymlinks` on one side and not the other will fail.
   `internal/security/pathvalidation/SecureJoinResolved` has the fix
   (PR #863) — reuse the `resolveExistingPrefix` pattern.
3. **`internal/database` cannot import `internal/config`** (cycle via
   `persistence.go`). Services that need both — like the AI cluster
   services that need `config.Config.OpenAIAPIKey` and want to live near
   `database` types — get registered from `internal/server/registry_wire.go`
   instead. See the `embeddingstore`/`chromemstore`/`aijobsstore`/`dedup`
   registrations there.
4. **A child agent in a `/parallel-sweep` can wreck a worktree** with
   `git add -A`. Every dispatch must spell out: "stage ONLY <explicit
   paths>; verify `git status --short` shows only those before commit."
   PR #851 was a 260-file disaster from this exact oversight.
5. **`make ci` runs full server tests** which include SERVER-THIN-8
   pre-existing failures. For per-PR validation,
   `go test ./internal/server/ -short -race -timeout=180s -run "TestHandler_|TestNewServer|TestRegister|TestServer"`
   is the green subset.

## Quick verification commands

```bash
# All-in-one health check
go vet ./... && go build ./... && staticcheck ./...

# Service registry sanity
go test ./internal/serviceregistry/... -count=1 -race -timeout=60s

# What's in each group right now
for g in core ai scheduler plugins activity; do
  echo "=== $g ==="
  grep -rn "Groups.*\"$g\"" internal/ --include="*.go"
done

# NewServer size (target: <=50)
awk '/^func NewServer/,/^}/' internal/server/server.go | wc -l

# GetGlobalStore audit (target: 0 production callers)
grep -rn "GetGlobalStore()" --include="*.go" internal/ cmd/ | grep -v "_test\." | wc -l
```

## Active goal

Continue through the deferred-work tickets in dependency order until the
migration is operationally complete: itunesservice + maintenance plugin
fully container-built, lifecycle flip done, NewServer trimmed, GlobalStore
audit complete. Pick up at PLUGIN-DECOUPLE-SERVER-CLOSURES (remaining
work) per the order above.
