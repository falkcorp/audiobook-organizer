<!-- file: docs/agent-tasks/platform-core/F7-06-core-library-completed-dark.md -->
<!-- version: 1.1.0 -->
<!-- guid: 3c40ae0e-a05f-40b2-b19d-4fc0f2dca4b2 -->
<!-- last-edited: 2026-11-05 -->

# F7-06 — Core library completed, dark: Manager, Root, RootFS, Transfer, Watch, Quarantine, PathMapper, the browse route under core:library.browse, core/internal/libfs

| Field | Value |
|---|---|
| ID | F7-06 |
| Release | R2, flip label F7 (dark PR) |
| Tier | **T4** (Opus / xhigh–max): filesystem transfers and renames over user media; a wrong move loses files |
| Wave | W28 (beside F2-16, F4-05, F5-15, F6-01, F6-02, F7-02, F7-03, F8-03; file-disjoint; this row is W28's one owner of `core/go.mod`, `core/go.sum` and `go.sum`). F3-07 is in W29: if it has merged before this PR, rebase and re-run `go mod tidy` in `core/` |
| Depends on | F3-01 (core config: the roots are `core.library.roots` config entries), F2-05 (`internal/taskhttp` and `coretest` on tasks, as the inventory row lists) |
| Spec | 05-library §5L.0 (D-L1 … D-L9), §5L.3 (all: §5L.3.1 roots, paths, registry; §5L.3.2 `RootFS`; §5L.3.3 transfers; §5L.3.4 hashing; §5L.3.5 quarantine; §5L.3.6 path mapping; §5L.3.7 watchers; §5L.3.8 ownership and sharing), §5L.4–§5L.8, §5L.9 (tests), §5L.10 (code moves; the roots cutover is F7-08's); 04 §8.1 F7 |

**Gate:** plan, then execute autonomously in a worktree with its own PR. **Dark:** no ABO code calls `library` beyond
what R1-08's leaf already provides; `import_path:` rows, `internal/fileops`, `internal/pathutil`, `internal/watcher`,
`internal/quarantine` and the organizer keep serving until F7-09. The package runs on `hello` and `coretest`.

## START HERE (do this first, exactly)

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/pc-f7-06-library" -b agent/platform-core-f7-06-library origin/main
cd "$REPO/.worktrees/pc-f7-06-library"
git rebase origin/main
```

## Goal

Complete core `library` over R1-08's leaf (`RootID`, `Role`, `RootSpec`, `Path` and whatever R1 landed): `Manager`
(`New(Options)`), `Root` (`ID`, `Spec`, `FS`, `Usage`, `Mounted`, `AppDir`), `Scope` (the module handle), `RootFS`
over `os.Root` with the `*at`-syscall operations `os.Root` cannot express (`RenameNoReplace`, `Link`, `Reflink`,
`CreateExclusive`, `SyncDir`, `Walk`), the **one** transfer engine (`Transfer`, `TransferIn`, `TransferOptions` via
`NewTransferOptions`, `PermPolicy` with no default, journal in `c/library/transfer/`, free-space floor, verify,
`ProtectionOracle` fail-closed), `Hash` (`Digest`), quarantine primitives (`Quarantine`, `Release`), `PathMapper`
(separator-boundary longest prefix, byte-exact, `Fold` for Windows remotes), watchers (debounce, overflow → rescan,
watch-limit → polling), the sentinel rule (D-L5, `SentinelBootstrap` for migrated roots), roots as config entries
(`core.library.roots`, `mappings`, `protected_paths`) applied without a restart, and the browse route under
`core:library.browse` in `core/internal/libraryhttp` (unmounted in ABO). The byte-level code (copy, reflink, fsync
seams) moves from `internal/fileops` into `core/internal/libfs` as a re-implementation with HEAD's rules.

## Invariants (each is a §5L.9 test)

- **I1. Confinement.** No `..` or symlink escapes a root, including a symlink swapped mid-walk
  (`TestRootFSRefusesDotDotAndSymlinkEscape`).
- **I2. Never clobber.** Renames and copy publishes never replace an existing name
  (`TestRenameNoReplaceNeverClobbers`, `TestTransferCopyPublishIsNoClobber`).
- **I3. Move order.** A cross-device move removes the source only after destination fsync and verify, at every
  injected crash point (`TestTransferCrossDeviceMoveOrder`); every journal state recovers to done or rolled back
  (`TestTransferRecoveryFromJournal`).
- **I4. Fail closed on seeding.** An oracle with `loaded=false` blocks a move and allows a copy
  (`TestMoveRefusedWhenSeedingUnknown`); free space is checked before any write (`TestTransferChecksFreeSpaceFirst`).
- **I5. Sentinel.** An unmounted (empty) root allows reads and refuses remove/move-out
  (`TestSentinelBlocksDestructiveOps`, `TestSentinelBootstrapMigratedRoot`).
- **I6. Ownership.** Roots of different owners never overlap (`TestRootOverlapAcrossOwnersRefused`); a module's
  `Scope` cannot act as another owner (`TestScopeCannotActAsAnotherOwner`); download roots are move-out only
  (`TestDownloadRootSharedMoveOutOnly`).
- **I7.** `TransferIn` commits with the caller's batch (`TestTransferInCommitsWithCallerBatch`).
- **I8.** No ABO key name in core (`TestLibraryErrorsPassKeyNameGate`, a case of `TestCoreHoldsNoAppKeyNames`).
- **I9. Dark.** ABO's file operations are unchanged in this PR.

## Files

| Action | Path |
|---|---|
| create | `core/library/manager.go` (`Manager`, `Scope`, `Options`, `New`, roots as config entries, sentinel) |
| create | `core/library/rootfs.go` (`RootFS`, `File`, `WalkOptions`, `Entry`) |
| create | `core/library/transfer.go` (`TransferMode`, `PermPolicy`, `VerifyLevel`, `TransferOptions`, `NewTransferOptions`, `TransferResult`, `ProtectionOracle`, journal, recovery, `Hash`/`Digest`, quarantine) |
| create | `core/library/watch.go` (watchers, debounce, overflow, polling fallback) |
| create | `core/internal/libfs/` (copy, reflink per OS, `*at` syscalls, fsync seams) |
| create | `core/internal/libraryhttp/` (the browse route under `core:library.browse`) |
| modify | `core/go.mod`, `core/go.sum`, `go.sum` (`fsnotify`, `golang.org/x/sys` if not yet in core) |
| create | one `changelog.d/` fragment (headerless) |

The path mapper (§5L.3.6) and quarantine (§5L.3.5) may live in their own files under `core/library/`; the row's file
list names the four main files, and new files in the same package are within its scope.

## Re-verify before editing (expected results at your base)

```bash
# R1-08's leaf. Expected: RootID, Role, RootSpec, Path; no Manager yet
grep -n 'type RootID\|type Role \|type RootSpec\|type Path struct\|type Manager interface' core/library/*.go
# HEAD code this re-implements. [evidence: internal/fileops/copy.go, reflink.go, reflink_linux.go, hash.go;
#  internal/organizer/saferename.go; internal/versions/rename_noreplace.go; internal/quarantine/service.go;
#  internal/watcher/watcher.go; internal/diskstats/diskstats_unix.go]
ls internal/fileops/copy.go internal/fileops/reflink.go internal/fileops/hash.go internal/organizer/saferename.go \
   internal/versions/rename_noreplace.go internal/watcher/watcher.go internal/diskstats/diskstats_unix.go
# Config entries API (F3-01). Expected: a way to declare list-valued config entries and OnChange
grep -rn 'OnChange\|func .*Entries' core/config/*.go | head -5
# fsnotify already a root dependency. Expected: 1 line
grep -n 'fsnotify' go.mod
```

STOP if `library.Manager` already exists, or if core config offers no list-entry and change-notification API for
`core.library.roots`.

## Steps

1. Read 05-library §5L.0–§5L.10 in full; copy every type and signature of §5L.3 exactly.
2. Write the tests of §5L.9 first (below), with fault injection seams for every fsync and rename.
3. `core/internal/libfs`: re-implement HEAD's byte copy rules (`CopyFile*`: no clobber, ingest mode `LibraryFileMode`
   0664, file and parent fsync, partial cleanup), reflink (FICLONE on Linux, `clonefile` on darwin, unsupported
   elsewhere, refusing an existing destination), `renameat2 RENAME_NOREPLACE` / `renamex_np` with a checked fallback,
   all through descriptors opened through the root.
4. `RootFS` over `os.Root`; `Walk` with the skip rules that replace `internal/pathutil/hidden.go` and
   `internal/appdirs`.
5. The transfer engine: modes `Auto` (reflink → hardlink → copy, `Did` reports reality), `Copy`, `Hardlink`, `Reflink`,
   `Symlink`, move; journal in `c/library/transfer/`; recovery; `TransferIn` in the caller's batch;
   `PendingTransfers`.
6. Hash, quarantine/release (no clobber on release), path mapper, watchers, `Root.Usage` (Bavail), `Mounted` with the
   sentinel and the bootstrap heuristic of HEAD's `libroot.Mounted`.
7. Roots as config entries with live apply (`TestConfigEntryRoots_RoundTrip`).
8. The browse route in `core/internal/libraryhttp`, `core:library.browse`, allow-list = the caller's readable roots.
9. Wire on `hello`/`coretest`. Headers, fragment, acceptance.

## Code contract (05-library §5L.3, verbatim excerpts; copy the rest from the spec)

```go
package library
type Manager interface {
	Root(id RootID) (*Root, error)
	Roots(filter RootFilter) []*Root
	Resolve(abs string) (Path, error)
	Abs(p Path) (string, error)
	Transfer(ctx context.Context, caller storage.Owner, src, dst Path, opts TransferOptions) (TransferResult, error)
	TransferIn(ctx context.Context, b *storage.Batch, caller storage.Owner, src, dst Path, opts TransferOptions) (TransferResult, error)
	Hash(ctx context.Context, p Path) (Digest, error)
	Quarantine(ctx context.Context, caller storage.Owner, p Path, reason string) (QuarantineID, error)
	Release(ctx context.Context, caller storage.Owner, id QuarantineID, to *Path) (Path, error)
	Watch(ctx context.Context, id RootID, opts WatchOptions) (iter.Seq[[]Event], error)
	Mapper() PathMapper
	// … exactly as §5L.3.1
}
func New(opts Options) (Manager, error)
func NewTransferOptions(mode TransferMode, perm PermPolicy, verify VerifyLevel) TransferOptions
type ProtectionOracle interface {
	Protected(ctx context.Context, p Path) (protected, loaded bool, err error)
}
var ErrReflinkUnsupported = errors.New("library: reflink not supported on this filesystem")
```

## Tests (write first; spec names copied exactly, 05-library §5L.9)

`TestRootFSRefusesDotDotAndSymlinkEscape`, `TestRenameNoReplaceNeverClobbers`, `TestTransferCopyPublishIsNoClobber`,
`TestTransferCrossDeviceMoveOrder`, `TestTransferRecoveryFromJournal`, `TestTransferPermPolicyHasNoDefault`,
`TestTransferAutoFallbackChain`, `TestMoveRefusedWhenSeedingUnknown`, `TestTransferChecksFreeSpaceFirst`,
`TestSentinelBlocksDestructiveOps`, `TestSentinelBootstrapMigratedRoot`, `TestRootOverlapAcrossOwnersRefused`,
`TestDownloadRootSharedMoveOutOnly`, `TestPathMapperLongestPrefixBoundary`, `TestPathMapperNoUnicodeFolding`,
`TestWatchDebounceCoalesces` (synctest), `TestWatchOverflowYieldsRescan`, `TestWatchFallsBackToPolling`,
`TestQuarantineReleaseRoundTrip`, `TestHashSameDescriptorSizeAndDigest`, `TestDisabledModuleRootsInert`,
`TestTransferInCommitsWithCallerBatch`, `TestScopeCannotActAsAnotherOwner`, `TestConfigEntryRoots_RoundTrip`,
`TestLibraryErrorsPassKeyNameGate`. Not here: `TestRootsCutover_FromHEADFixture` (F7-08).

## Acceptance (commands and expected output)

```bash
# Library with the race detector, on Linux and with GOOS=darwin vet. Expected: ok / exit 0
(cd core && go test -count=1 -race ./library/... ./internal/libfs/... ./internal/libraryhttp/...)
(cd core && GOOS=darwin CGO_ENABLED=0 go vet ./library/... ./internal/libfs/...)

# Key-name gate. Expected: ok
(cd core && go test -count=1 -run 'TestCoreHoldsNoAppKeyNames' ./archtest/...)
go test -count=1 -run 'TestLibraryErrorsPassKeyNameGate' ./...

# Dark: ABO file operations unchanged. Expected: ok
go test -count=1 ./internal/fileops/... ./internal/organizer/... ./internal/watcher/... ./internal/quarantine/...

# Gates. Expected last lines: ✅ All CI checks passed! / exit 0
make ci
make -C core ci
```

## Out of scope

- The roots cutover M6 (F7-08) and deleting `ImportPathStore` (F7-09).
- Moving organizer, versions, quarantine, Deluge import and iTunes transfer onto `Transfer` (after R2, plan §(j)).
- Read shares for a second module (deferred-build, §5L.13).

## Rollback

Revert the PR; nothing in ABO uses it and no stored data is written (dark; the transfer journal exists only on hello
and test stores).

## Repo rules

- Worktree off fresh `origin/main`; headers on every file; no `go work init` (CLAUDE.md).
- Conventional commit, for example `feat(library): core roots, confined filesystem and transfer engine (dark)`, ending
  with `Co-Authored-By: Claude <noreply@anthropic.com>`; `gh pr merge <n> --rebase`; never squash; never `--admin`.
- Concurrency (CLAUDE.md): watcher rescans and `Walk` over library-scale trees stream; any per-file work you add
  (hashing, verify) runs on a bounded pool, never unbounded goroutines.
- `core/go.mod` changes run `make -C core replace-check toolchain-check`.

