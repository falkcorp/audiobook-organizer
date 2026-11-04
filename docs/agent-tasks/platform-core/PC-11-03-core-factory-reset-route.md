<!-- file: docs/agent-tasks/platform-core/PC-11-03-core-factory-reset-route.md -->
<!-- version: 1.0.0 -->
<!-- guid: 294b4dac-15f6-408b-a7bb-0adaf089b337 -->
<!-- last-edited: 2026-11-04 -->

# PC-11-03 — Core factory-reset route in internal/storagehttp with storage.ResetRequest; both legacy reset paths become its aliases; legacy.Maintenance deleted

**ID:** PC-11-03 · **Wave:** W11 (S4 r2) · **Priority:** P1 · **Effort:** L
**Tier:** T3 Opus/high — a destructive route with a confirmation contract (A-17): the native route must never delete
library files without an explicit, freshly confirmed opt-in, and the legacy aliases must keep HEAD's behaviour.
**Depends on:** PC-11-02 (the flip). It merges right after the flip in S4 r2; a build between the two keeps S2b's
staging path behind the bridge (`legacy.Maintenance`), which is safe.
**Stage-Release trailer:** `Stage-Release: S4 2` on every commit.
**Normative digest:** `docs/plans/platform-core/digests/S4.md` (Behaviour rule 9; Contracts: `Stager`, `ResetRequest`).
**Owning spec:** 09 S4 Moves ("In the r2 PR the core factory-reset route lands"); 05-storage §5S.3.15 (`ResetRequest`,
`ResetStaged`, `LibraryConfirmError`, the sentinels, `kernel.stageReset`, `Stager`); 05-rbac §6.2 (the confirmation
contract), §12 (the three tests); 05-config §1.12.2 ("A core build's factory reset"); 05-http-api §3.1 (the two reset
aliases' `Request` and `Selector`), §5; review disposition A-17.

## ⛔ START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-pc-11-03" -b agent/pc-11-03-reset-route origin/main
cd "$REPO/../audiobook-organizer-pc-11-03"
git rebase origin/main
git worktree list
npm ci --prefix web
```

Destructive paths are exercised on fixtures only; never stage a reset against a store you did not create.

## Goal

- `core/internal/storagehttp` (L6, imported only by `kernel`): `POST /api/core/v1/system/factory-reset`, `core:admin` +
  `Guards.Interactive` + `Guards.Owner`, receiving the kernel's `kernel.stageReset` as `storagehttp.Stager` (never
  `*storage.Admin`). Body decoded strictly into `storage.ResetRequest`; `kind` defaults to `factory` (`store` allowed); a
  missing `confirm` → 422 `confirm_required`; `delete_library_files: true` without a `library_root` and
  `library_file_count` that match a fresh preflight → 422 `library_files_confirm_required` carrying the current root and
  count, nothing staged; success → 200 `{reset_id, message}`; a dangerous `RootDir` → 400 only when the wipe is staged.
  The route audits the request.
- The two legacy paths become its `Alias`es keeping HEAD's `{"data": {"message": ...}}` shape and answering 422 where
  HEAD answered 400: `POST /api/v1/system/factory-reset` sets path values `delete_library_files=true` and
  `library_confirm=legacy` (HEAD's wipe, no count; the route sets `LegacyConfirm`), and `POST /api/v1/system/reset` sets
  `reset_kind=store`; each lists `kind`, `delete_library_files`, `library_root`, `library_file_count` in `Selector`, so
  no body can steer them.
- Delete `legacy.Maintenance`, `module.StorageHandles.Maintenance` and the two gin handlers (their only callers).
- Ledger rows for the two aliases; `core.json` regenerated with the route.

## Exact files

Create: `core/internal/storagehttp/`.
Modify: `core/legacy/maintenance.go` (deleted), `core/module/` (`StorageHandles.Maintenance` removed),
`internal/server/handlers/system/handler.go` (the two handlers removed), `internal/books/module.go`,
`docs/compat-surfaces.md`, `internal/books/compat-surfaces.md` (regenerated).
Also (Planner notes): `internal/server/wire_system_routes.go` (the two gin registrations), `core/kernel/` (mounting the
route), `core/openapi/core.json`.

## Re-verify (expected at the plan checkout `f07f516be` unless marked; rerun at base)

```bash
grep -n '"/system/reset"\|"/system/factory-reset"' internal/server/wire_system_routes.go   # 4 hits (:45-48): two credRoute registrations
grep -n 'func (h \*Handler) ResetSystem\|func (h \*Handler) FactoryReset' internal/server/handlers/system/handler.go   # 2 hits
test -f core/legacy/maintenance.go && grep -n 'StageFactoryReset' core/legacy/maintenance.go   # >= 1 hit (PC-07-07)
grep -rn 'StorageHandles' core/module/ | grep -n Maintenance                                  # >= 1 hit
grep -rn 'func (r ResetRequest) Validate() error\|type LibraryConfirmError struct\|ErrLibraryConfirmRequired' core/storage/ | wc -l   # 3 (PC-07-07)
grep -rn 'func.*stageReset' core/kernel/                                                      # 1 hit
grep -rn 'legacy.Maintenance\|StageFactoryReset' --include=*.go internal | grep -v _test      # the callers you delete (the two handlers)
test -d core/internal/storagehttp && echo EXISTS-STOP || echo absent                          # absent
grep -n 'shipped_stage: S4' ci/stage.txt                                                      # 1 hit (the flip merged)
```

## Steps

1. Tests first (below), red.
2. `storagehttp`: `Register` per the existing `internal/*http` shape; the handler decodes strictly, fills `requestedBy`
   from the principal, calls `Stager.StageReset`, maps `ErrResetConfirmRequired` → 422 `confirm_required`,
   `ErrResetRequestInvalid` → 422, `*LibraryConfirmError` → 422 `library_files_confirm_required` with `{root, files}`,
   `ErrResetRefused` → 400, `ErrSwapPending` → 409, success → 200 `{reset_id, message}`.
3. Aliases per 05-http-api §3.1 (`Targets` = the route; `Request` sets only the path values listed above; `Selector` as
   above; `LegacyError` per HEAD's error shape; `Shape` → `{"data": {"message": ...}}`).
4. Mount the route and the aliases from the kernel; delete the gin registrations and handlers, `legacy.Maintenance`,
   `module.StorageHandles.Maintenance`; remove books' module wiring of the handle.
5. Ledger rows and `core.json`.
6. Headers, `changelog.d/` fragment, acceptance. Release note (with PC-11-02's notes): "A missing or wrong confirmation
   now answers 422 instead of 400."

## Code contract (05-storage §5S.3.15)

```go
package storagehttp
type Stager interface {
	StageReset(ctx context.Context, requestedBy string, req storage.ResetRequest) (storage.ResetStaged, error)
}
// storage.ResetRequest{Kind, Confirm, DeleteLibraryFiles, LibraryRoot, LibraryFileCount, LegacyConfirm}
// storage.ResetStaged{ID `json:"reset_id"`, LibraryRoot, LibraryFileCount}
```

## TDD — names fixed by 09 S4 and 05-rbac §12

`TestFactoryResetCoreRouteRecordsHookArgs` (the intent staged through the native route and through the
`/api/v1/system/factory-reset` alias carries `kind: factory` and `hook_args`, written through the kernel's `Stager` from
one `storage.ResetRequest`; the alias carries HEAD's wipe with no count check), `TestFactoryResetAliasKeepsConfirmAndShape`
(both aliases: missing or wrong `confirm` → 422, nothing staged; success → HEAD's `{"data": {"message": ...}}`; the
`/api/v1/system/reset` alias stages `kind: store` and no body field steers it to `factory`),
`TestFactoryResetNativeRouteKeepsLibraryFilesByDefault` (without `delete_library_files` the intent keeps every library
file and a dangerous `RootDir` is not refused; with it but without, or with stale, `library_root`/`library_file_count` →
422 `library_files_confirm_required` with the current root and count and no `restore/pending.json`; with matching values
200). Also keep green: `TestFactoryResetRejectedLeavesStoreAndSettings`, `TestFactoryResetClearsGapFillSecrets`,
`TestFactoryReset_StaleEnableAuthFalseKeepsAuthOn`.

## Invariants (STOP conditions)

- The request only stages; nothing destructive runs before the restart; no setting is written in the request.
- The native route never stages a library wipe without a matching fresh preflight.
- The aliases' path values cannot be overridden by a body.
- If `kernel.stageReset` does not already implement the preflight compare, STOP (it is PC-07-07's; do not re-implement it
  in the route).

## Acceptance

```bash
(cd core && go build ./... && go test ./internal/storagehttp/... ./kernel/... -race -count=1)   # ok
go test ./... -run 'TestFactoryResetCoreRouteRecordsHookArgs|TestFactoryResetAliasKeepsConfirmAndShape|TestFactoryResetNativeRouteKeepsLibraryFilesByDefault' -count=1   # ok
test -f core/legacy/maintenance.go || echo deleted                                       # deleted
grep -rn 'StorageHandles.*Maintenance\|legacy.Maintenance' --include=*.go . | grep -v _test   # no output
grep -n '"/system/reset"\|"/system/factory-reset"' internal/server/wire_system_routes.go  # no output
make compat-ledger                                                                       # exit 0
make -C core ci                                                                          # exit 0
make ci                                                                                  # exit 0
```

## Out of scope

The swap mechanics, preflight and post-reset hook (S2b, PC-07-07); the UI's reset dialog changes (none required: the
legacy SPA calls the aliased paths).

## Rollback

Binary swap: the staged intent is the same record either build writes; an older build's gin handlers stage through its
own path.

## Repo rules

Headers; `changelog.d/` fragment. Conventional commits with `Stage-Release: S4 2` and
`Co-Authored-By: Claude <noreply@anthropic.com>`. `gh pr merge <n> --rebase`.

## Planner notes

- **Missing files:** `internal/server/wire_system_routes.go` (registers the two gin routes being deleted, :45-48 at the
  plan checkout), `core/kernel/` (mounts the new route) and `core/openapi/core.json` (every regeneration lands in the PR
  that adds the route, 09 S4 Endpoints) are not in the row's file list.
