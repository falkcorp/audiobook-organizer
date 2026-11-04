<!-- file: docs/specs/platform-core/05-library.md -->
<!-- version: 1.1.0 -->
<!-- guid: dafe380b-f703-41cb-99d0-2b2ba32f70e5 -->
<!-- last-edited: 2026-11-04 -->

# 05 — Library roots and safe filesystem access (`library`)

Evidence commit `748fd787a`; every `path:line` below is at that commit. Raw inputs:
[`raw/05-storage.md`](../../plans/platform-core/raw/05-storage.md),
[`raw/02-import-graph-hotspots.md`](../../plans/platform-core/raw/02-import-graph-hotspots.md),
[`raw/08-prior-art.md`](../../plans/platform-core/raw/08-prior-art.md) §1 ("storage roots, path
mapping, safe moves/copies, hashing, quarantine, disk checks, import observation") and §4 (Q4),
[`raw/12-cross-cutting.md`](../../plans/platform-core/raw/12-cross-cutting.md). Names and rules are
from [03-architecture](03-architecture.md) (`library` is **L4**, R4). Records this package persists
use [05-storage](05-storage.md) keyspace `c/library/`. Modules reach it only through the module-scoped
handle `library.Scope` (`module.Deps.Library`, [06](06-app-sdk.md)), which carries the module's owner.

## 5L.0 Decisions in one table

| # | Decision |
|---|---|
| L1 | A **media root** (`library.Root`) is a core concept: an ID, a host path, a role (library, import, download, external), one owner, optional read shares. Modules address files as `library.Path{Root, Rel}`, never as bare absolute strings. |
| L2 | All file access under a root goes through a `RootFS` backed by **`os.Root`** (Go 1.24+; Go 1.25 added `Rename`, `Link`, `Symlink`, `MkdirAll`, `RemoveAll`), so `..` and symlinks cannot escape. Operations `os.Root` cannot express (no-clobber rename, cross-root link/rename, reflink) use `*at` syscalls on directory descriptors opened *through* the root. |
| L3 | **One** transfer engine (move / copy / hardlink / reflink / symlink / auto-fallback) that never clobbers, fsyncs file and directory, verifies, journals for crash recovery, checks free space first, and refuses to move or delete a file a download client is still seeding (fail-closed when that is unknown). It replaces three no-clobber renames, six byte copies and four reflinks ABO has had. |
| L4 | **Path mapping** (download-client / remote-worker path ↔ `library.Path`) is a core service keyed by source, separator-boundary longest-prefix, byte-exact (no Unicode folding). |
| L5 | A **sentinel file** proves a root is the right, mounted volume before any destructive action; the existing "has non-dot content" heuristic becomes the bootstrap for legacy roots only. |
| L6 | Watchers (fsnotify) are core, per root, debounced, with **overflow → rescan** and **watch-limit exhaustion → polling** instead of silent loss. |
| L7 | Quarantine is a core *primitive* (move into the root's quarantine area + record + release); the decision of what to quarantine stays in modules. |
| L8 | Root paths of different owners never overlap; download roots are core-owned and shared read/move-out with every module that has the download capability; this is the multi-app rule. |
| L9 | **Q4 answer**: one core `library.Root` abstraction; modules cannot bring their own storage driver now; `RootFS` is an interface so a remote driver can be added later without changing callers. |

## 5L.1 Purpose & scope

`library` owns where media lives and how bytes are moved: root registry, confined filesystem
access, path mapping, transfers, hashing (content digest), quarantine, free-space and mount checks,
change watching, and the app-owned directories under a root (reports, quarantine, temp).

Out of scope: what a file *means* (scanners, parsers, naming patterns, identity hashes such as
`filehash.BookFileHash` — module code); download-client protocols ([05-download](05-download.md),
which supplies the seeding oracle and download roots); backup of media (media is not backed up by
core — [05-backup](05-backup.md) §5B.1); permissions model ([05-rbac](05-rbac.md); this file names
the permissions it needs).

## 5L.2 Current state at HEAD

| Concern | Today | Evidence |
|---|---|---|
| Roots | One configured `RootDir` (organized library) plus `import_path:` rows (`ID, Path, Name, Enabled, LastScan, BookCount`), CRUD on `database.Store`. `PathVars` names `libroot` and its parent `books` for abbreviation. | `internal/database/store.go:596-604`; `internal/database/iface_system.go:100-112`; `internal/pathutil/pathvars.go:18-45` |
| Mount check | `libroot.Mounted`: root non-empty with at least one non-dot entry; the doc comment calls it "a coarse guard, not proof of a healthy mount" and lists false positives (`covers/`, `openlibrary-dumps`, config fallback dirs). | `internal/libroot/libroot.go:25-59` |
| App dirs under a root | `.reports` created with symlink/ownership/world-write checks; app-dir exclusion list (backup dir, OL dumps, playlists, DB dir) for every walker. | `libroot.go:61-156`; `internal/appdirs/appdirs.go:43-62`; `internal/pathutil/hidden.go:73-228` |
| Containment | `SplitRoot`/`JoinRoot`/`ValidateRel` (root-relative wire format of the remote fingerprint worker; byte-exact, no Unicode normalization; `JoinRoot` resolves symlinks and refuses escapes); separator-boundary `CutPathPrefix`/`IsWithin`; `UnderProtectedPath` fail-closed with a cached and an uncached variant for deletes. | `internal/pathutil/root.go:17-110`; `prefix.go:31,56`; `protected.go:330-357` |
| `os.Root` | Used in exactly two places (cover replacement, flight recorder). Every other path operation is string-joined. | `internal/metadata/cover.go:123-126`; `internal/diag/flight/flight.go:595-601` |
| Byte copies | One implementation now (`CopyFile`, `CopyFileExclusive`, `CopyFileIngestExclusive`, `CopyFileInto`) after six copies disagreed on clobber, mode, fsync and partial-file cleanup — two of which were live defects; ingest uses `LibraryFileMode` 0664, never the source's mode; file **and** parent-directory fsync behind test seams. | `internal/fileops/copy.go:18-75,88-117,129-190` |
| Reflink | One implementation (FICLONE on Linux, clonefile on darwin, unsupported elsewhere) after four, one of which was a stub that silently byte-copied; refuses an existing destination; `ReflinkOrCopy` fallback keeps that. | `internal/fileops/reflink.go:6-80`; `reflink_linux.go:20-35` |
| No-clobber rename | **Three** implementations: `organizer.safeRename` (Lstat + rename, TOCTOU window, post-verify), `versions.renameNoReplace` (`renameat2 RENAME_NOREPLACE` / `renamex_np`, checked fallback), `quarantine.moveNoClobber` (Lstat + rename). | `internal/organizer/saferename.go:14-48`; `internal/versions/rename_noreplace.go:12-40`; `internal/quarantine/service.go:850-870` |
| Organize strategies | `auto` (reflink → hardlink → copy), `copy`, `hardlink`, `reflink`, `symlink`. | `internal/config/config.go:1069`; `internal/organizer/organizer.go:285-306` |
| Hashing | Content digest `fileops.ComputeFileHashAndSize` (whole-file SHA-256 + size from one open handle). Row identity `filehash.BookFileHash` (chunked above 100 MB) kept deliberately in another package. | `internal/fileops/hash.go:15-60`; `internal/filehash/filehash.go:5-60` |
| Quarantine | Book-level: moves a book into `<root>/.failed/<author>/<title-id>/`, resumable from path history, rollback on partial failure. | `internal/quarantine/service.go:123-278,584-850` |
| Free space | `diskstats.Stats` (Bavail, not Bfree, deliberately) used by backup's space guard; `enable_disk_quota`/`disk_quota_percent` are only displayed. | `internal/diskstats/diskstats_unix.go:13-24`; `internal/backup/backup.go:198-268`; `internal/server/handlers/system/handler.go:389-390` |
| Download-client paths | Deluge `save_path` joined directly (`AbsPath`), assuming the server sees the same mount; no mapping. Seeding protection: `ProtectedPathCache` (torrent save paths, 5-min refresh, plus static `protected_paths`), with `Loaded` = false meaning "unknown", which callers must treat as protected. | `internal/deluge/client.go:88-92`; `internal/deluge/protected_paths.go:15-45`; `internal/config/config.go:1781-1787` |
| Other path maps | `itunes.path_mappings` (bidirectional prefix map, import healing); `PathAlias` (display only, deliberately separate); `WHISPER_PATH_MAP` (remote transcription worker mount); remote "lanes" for audio decode. | `config.go:35-60,107-150`; `internal/transcribe/path.go:28-40`; `pkg/ops/lanes.go:13-15` |
| Watcher | fsnotify per enabled import path, recursive add skipping app dirs, debounce 5 s, audio-extension filter, supervisor re-reads paths on every reconcile (no restart needed). Watch-add failures are a warning; **errors from fsnotify (incl. queue overflow) are only logged**, so lost events never trigger a rescan. | `internal/watcher/watcher.go:24,82-105,141-205,166-185`; `internal/watcher/supervisor.go:73,116`; `internal/server/server_lifecycle.go:501-525` |
| Pending file ops | `pending_file_op:` rows for crash recovery of slow file I/O jobs. | `internal/server/file_io_pool.go:31-38` |
| Browse API | `FilesystemService.BrowseDirectory` with an allow-list = import paths; registered via `init()`. | `internal/fileops/service.go:21-118`; `internal/fileops/register.go:12-21` |
| Coupling | `fileops` imports `internal/database` and `internal/filehash` (`BookFile` types), so it cannot move to core as is; `pathutil` and `libroot` are leaves (`libroot` imports only `internal/util`). | `raw/02-import-graph-hotspots.md` (a1) rows `internal/fileops`, (a3) `internal/pathutil`; `libroot.go:6-23` |

## 5L.3 Go API

Package `github.com/falkcorp/mediacore/library`. Implementation of the local driver:
`core/internal/libfs` (uses `os.Root` and `golang.org/x/sys/unix` *at calls; `x/sys` is already a
direct dependency, `go.mod:46`).

### 5L.3.1 Roots, paths, registry

```go
package library

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"iter"
	"time"

	"github.com/falkcorp/mediacore/storage"
)

type RootID string // stable, [a-z0-9-]{1,40}; e.g. "books-library", "import-3", "dl-qbit-1"

type Role uint8
const (
	RoleInvalid  Role = iota
	RoleLibrary   // organized media; written by its owner module only
	RoleImport    // watched source folders the owner ingests from
	RoleDownload  // a download client's completed area; core-owned; modules may read and move OUT
	RoleExternal  // someone else's tree (an iTunes library): read-only, never mutated
)

type RootSpec struct {
	ID          RootID
	Name        string
	Path        string        // absolute host path; resolved once (EvalSymlinks) at registration
	Role        Role
	Owner       storage.Owner // module ID or "core"
	ReadShares  []storage.Owner
	Watch       WatchMode
	Sentinel    SentinelMode
	MinFree     int64         // bytes that a transfer into this root must leave free
	Quarantine  string        // rel dir; default ".mediacore/quarantine"; books keeps ".failed"
	AppDir      string        // rel dir for app state; default ".mediacore"
	Aliases     []Alias       // display renderings for remote clients (ABO PathAlias); never used for I/O
}

type Alias struct{ Windows, UNC, SMBURL string }

type WatchMode uint8
const ( WatchInvalid WatchMode = iota; WatchAuto; WatchNotify; WatchPoll; WatchOff )

type SentinelMode uint8
const (
	SentinelInvalid  SentinelMode = iota
	SentinelRequired  // new roots: .mediacore/root.json must exist and match
	SentinelBootstrap // legacy roots: created on the first boot where the heuristic passes, then Required
)

// Path is the only way modules name a file. JSON: {"root":"books-library","rel":"Author/Title/01.m4b"}.
// Rel is POSIX-relative, clean, never "..", never absolute, NUL-free, byte-exact
// (pathutil.ValidateRel semantics, root.go:51-72).
type Path struct {
	Root RootID `json:"root"`
	Rel  string `json:"rel"`
}
func (p Path) Join(elem ...string) (Path, error)
func (p Path) Dir() Path
func (p Path) Base() string
func (p Path) String() string // "root:rel", for logs

// Manager is the core service. Core subsystems and the kernel hold it; a
// module never does — it receives a Scope (below). Mutating calls name the
// caller explicitly here; Scope supplies it implicitly.
type Manager interface {
	Root(id RootID) (*Root, error)
	Roots(filter RootFilter) []*Root
	// Resolve maps an absolute host path to the most specific root that contains
	// it (separator boundary). ErrNotUnderRoot if none.
	Resolve(abs string) (Path, error)
	// Abs returns the resolved host path, for handing to external tools
	// (ffmpeg, a tagger). The result is checked to be inside the root after
	// symlink resolution (pathutil.JoinRoot semantics, root.go:79-110).
	Abs(p Path) (string, error)

	Transfer(ctx context.Context, caller storage.Owner, src, dst Path, opts TransferOptions) (TransferResult, error)
	// TransferIn performs the same transfer, but the step that finishes its
	// journal row (step 6 of the algorithm) is staged into the caller's batch b
	// instead of committing on its own, so "file moved" and the caller's rows
	// ("history written", "item imported") become durable in ONE commit
	// (COR-4). library binds its own c/library/ keyspace to b (participant
	// binding, 05-storage §5S.3.2); the caller commits b.
	TransferIn(ctx context.Context, b *storage.Batch, caller storage.Owner, src, dst Path, opts TransferOptions) (TransferResult, error)
	Hash(ctx context.Context, p Path) (Digest, error)
	Quarantine(ctx context.Context, caller storage.Owner, p Path, reason string) (QuarantineID, error)
	Release(ctx context.Context, caller storage.Owner, id QuarantineID, to *Path) (Path, error)
	Watch(ctx context.Context, id RootID, opts WatchOptions) (iter.Seq[[]Event], error)
	Mapper() PathMapper
}

// Scope is the module-scoped handle (module.Deps.Library, 06 §6.4.4, COR-21).
// The kernel builds one per module with that module's owner bound in, so a
// module cannot act as another owner and "a RoleLibrary root is written by
// its owner only" has an enforcement point the caller cannot spoof.
type Scope interface {
	Owner() storage.Owner
	Roots(filter RootFilter) []*Root // roots the module owns or may read
	FS(id RootID) (RootFS, error)    // = Root.FS(Owner()): read-only unless the module may write the root
	Resolve(abs string) (Path, error)
	Abs(p Path) (string, error)
	Transfer(ctx context.Context, src, dst Path, opts TransferOptions) (TransferResult, error)
	TransferIn(ctx context.Context, b *storage.Batch, src, dst Path, opts TransferOptions) (TransferResult, error)
	Hash(ctx context.Context, p Path) (Digest, error)
	Quarantine(ctx context.Context, p Path, reason string) (QuarantineID, error)
	Release(ctx context.Context, id QuarantineID, to *Path) (Path, error)
	Watch(ctx context.Context, id RootID, opts WatchOptions) (iter.Seq[[]Event], error)
	Mapper() PathMapper
	// PendingTransfers lists this owner's TransferIn journal rows whose file
	// operation finished but whose caller batch never committed (a crash
	// between the two); the module reconciles each one idempotently.
	PendingTransfers(ctx context.Context) ([]TransferRecord, error)
}

type TransferRecord struct {
	ID       string
	Src, Dst Path
	Did      TransferMode
	SHA256   string
	At       time.Time
}

type RootFilter struct{ Owner storage.Owner; Role Role; Readable bool }

type Root struct{ /* unexported */ }
func (r *Root) ID() RootID
func (r *Root) Spec() RootSpec
// FS returns the confined filesystem for this caller. A caller without write
// rights (not the owner, or a RoleExternal root) gets a read-only RootFS whose
// mutating methods return ErrReadOnlyRoot.
func (r *Root) FS(caller storage.Owner) (RootFS, error)
func (r *Root) Usage(ctx context.Context) (Usage, error)
// Mounted verifies the sentinel (and, for SentinelBootstrap roots before the
// sentinel exists, the non-dot-content heuristic of libroot.Mounted).
func (r *Root) Mounted(ctx context.Context) error
// AppDir returns a confined directory under <root>/<AppDir>/<name>, created
// owner-only-writable, refusing a symlink or a foreign-owned directory
// (libroot.ensureReportsDir semantics, libroot.go:108-156).
func (r *Root) AppDir(ctx context.Context, name string) (RootFS, error)

type Usage struct{ Total, Avail uint64 } // Avail = Bavail (diskstats_unix.go:13-17)

var (
	ErrNotUnderRoot    = errors.New("library: path is under no registered root")
	ErrEscapesRoot     = errors.New("library: path escapes its root")
	ErrBadRel          = errors.New("library: invalid root-relative path")
	ErrReadOnlyRoot    = errors.New("library: caller may not write this root")
	ErrNotMounted      = errors.New("library: root sentinel missing or mismatched (not mounted, or the wrong volume)")
	ErrRootOverlap     = errors.New("library: root overlaps a root of another owner")
	ErrUnknownRoot     = errors.New("library: unknown root")
)
```

### 5L.3.2 `RootFS` — the confined filesystem (and the Q4 extension point)

```go
// RootFS is every file operation a module may perform under one root. The
// local implementation wraps *os.Root; a future remote driver implements the
// same interface (Q4). All names are root-relative and validated.
type RootFS interface {
	fs.FS
	fs.StatFS
	fs.ReadDirFS

	Lstat(name string) (fs.FileInfo, error)
	OpenFile(name string, flag int, perm fs.FileMode) (File, error)
	// CreateExclusive is O_CREATE|O_EXCL: an existing name is fs.ErrExist.
	CreateExclusive(name string, perm fs.FileMode) (File, error)
	MkdirAll(name string, perm fs.FileMode) error
	Remove(name string) error
	// RenameNoReplace never replaces an existing destination (any type,
	// dangling symlink included): fs.ErrExist and nothing changes.
	RenameNoReplace(oldname, newname string) error
	Link(oldname, newname string) error          // fs.ErrExist if newname exists
	Reflink(oldname, newname string) error       // ErrReflinkUnsupported, fs.ErrExist
	// Symlink's target must itself be a library.Path inside a root the caller
	// can read; os.Root.Symlink does not validate targets, this does.
	Symlink(target Path, newname string) error
	SyncDir(name string) error                   // EINVAL/ENOTSUP treated as success (copy.go:88-108)
	Walk(ctx context.Context, dir string, opts WalkOptions) iter.Seq2[Entry, error]
}

type File interface {
	fs.File
	io.Writer
	io.WriterAt
	io.Seeker
	Sync() error
	Stat() (fs.FileInfo, error) // on the descriptor, never the path (hash.go:22-31)
}

type WalkOptions struct {
	SkipHidden  bool            // pathutil.ShouldSkipDir rule; AppDir and Quarantine are always skipped
	Extensions  []string        // case-insensitive filter on files; nil = all
	FollowLinks bool            // false: symlinks are reported, never followed
}

type Entry struct {
	Path Path
	Info fs.FileInfo
}

var ErrReflinkUnsupported = errors.New("library: reflink not supported on this filesystem")
```

Local driver rules: every name is checked by `ValidateRel` before reaching `os.Root`; operations
that `os.Root` lacks open the **parent directory through the root** (`root.Open(dir)`) and call
`unix.Renameat2(…, RENAME_NOREPLACE)` / `unix.Linkat` / `FICLONE` with a single-component final
name, so confinement is preserved. On a filesystem without `RENAME_NOREPLACE` the fallback for
regular files is `Linkat` + `Unlinkat` (atomic no-clobber, as `os.Link` already is), for directories
the checked rename with its documented window (`versions/rename_noreplace.go:24-33`). Windows:
`MoveFileEx` without `MOVEFILE_REPLACE_EXISTING`; reflink unsupported.

### 5L.3.3 Transfers

```go
type TransferMode uint8
const (
	TransferInvalid TransferMode = iota
	Move      // same device: no-clobber rename; cross-device: copy → verify → fsync → remove source
	Copy
	Hardlink  // same device only; else ErrCrossDevice
	Reflink   // else ErrReflinkUnsupported
	Symlink
	Auto      // reflink → hardlink → copy (ABO "auto", organizer.go:285-293)
)

type PermPolicy uint8
const (
	PermInvalid    PermPolicy = iota // no default: see fileops/copy.go:58-75
	PermFromSource                    // a sibling of the same file (backup, temp-and-rename)
	PermLibrary                       // ingest: 0664 then umask (fileops.LibraryFileMode)
)

type VerifyLevel uint8
const ( VerifyInvalid VerifyLevel = iota; VerifySize; VerifySHA256 )

type TransferOptions struct {
	Mode     TransferMode
	Perm     PermPolicy
	Verify   VerifyLevel
	// Replace, when set, allows overwriting dst only if dst's current SHA-256
	// equals Replace.ExpectSHA256 (compare-and-swap); the old file is kept as
	// <dst>.mediacore-replaced-<unix> until the transfer is durable.
	Replace  *ReplaceSpec
	MkdirAll bool
	Journal  bool // default true via NewTransferOptions; false only for temp files inside AppDir
}

type ReplaceSpec struct{ ExpectSHA256 string }

func NewTransferOptions(mode TransferMode, perm PermPolicy, verify VerifyLevel) TransferOptions

type TransferResult struct {
	Did      TransferMode // what actually happened (Auto resolves to one of the others)
	Bytes    int64
	SHA256   string       // set when Verify == VerifySHA256 or the copy path hashed anyway
	Duration time.Duration
}

var (
	ErrCrossDevice       = errors.New("library: hardlink across filesystems")
	ErrSourceProtected   = errors.New("library: source is protected (seeding, external root, or protection state unknown)")
	ErrInsufficientSpace = errors.New("library: destination would drop below its root's free-space floor")
	ErrVerifyFailed      = errors.New("library: destination does not match source after transfer")
	ErrReplaceMismatch   = errors.New("library: destination changed; refusing to replace")
)

// ProtectionOracle is implemented by download adapters (05-download) and by the
// static protected-path list. Loaded=false means "unknown" and is treated as
// protected for Move and Remove (deluge/protected_paths.go:37-45).
type ProtectionOracle interface {
	Protected(ctx context.Context, p Path) (protected, loaded bool, err error)
}
```

Transfer algorithm (each step journaled in `c/library/transfer/<id>`):

| Step | Action |
|---|---|
| 0 | Validate both paths; caller may write `dst.Root` and read `src.Root`; `Move` also needs move-out rights on `src.Root` (owner, or any module with download capability on a `RoleDownload` root). `src.Root` is `RoleExternal` or the oracle says protected/unknown → `Move` refused (`ErrSourceProtected`); copy/link allowed. |
| 1 | `dst.Root.Mounted`; `src.Root.Mounted` for `Move`. Free space: `Avail - size < MinFree` → `ErrInsufficientSpace` before any byte is written (link/rename modes on the same device skip the check). |
| 2 | Same device + `Move` → `RenameNoReplace` → step 5. `Hardlink`/`Reflink`/`Auto` → link/clone directly to the final name (both refuse an existing name). |
| 3 | Copy path: write to `<dst dir>/.mediacore-tmp-<id>` with `CreateExclusive`, `PermPolicy` mode, stream with hashing, `File.Sync`. |
| 4 | Verify (size, or SHA-256 of the written bytes vs the source digest computed while reading). Publish with `Link(tmp, final)` (fs.ErrExist if someone created `final` meanwhile) then remove tmp; `SyncDir(dst dir)`. |
| 5 | `Move` across devices: only now remove the source, `SyncDir(src dir)`. |
| 6 | `Transfer`: the journal row is deleted in its own commit. `TransferIn`: the row is marked `done` (its own commit, after step 5), and its **delete is staged into the caller's batch** `b`, so the caller's rows and the end of the journal commit together. A crash after step 5 but before `b` commits leaves a `done` row and no caller rows: boot recovery never undoes it, and `Scope.PendingTransfers` reports it to the owning module, which re-runs its own write idempotently (an import keyed by its own ID and the destination digest finds the file already in place). A `b` that is discarded instead of committed has the same outcome. |

Boot recovery (`core.library.transfer-recover`, a task triggered once at start through its `Schedule`'s `OnStart()`, 05-tasks):
each unfinished journal row is completed or rolled back from its last durable step (temp removed; a
verified destination whose source still exists after a cross-device move gets the source removed only
if the source digest still matches). `done` rows of `TransferIn` are left for `PendingTransfers`.

### 5L.3.4 Hashing

```go
type Digest struct {
	SHA256 string
	Size   int64
}

// Hash reads the file once through its root, hashing and sizing the same
// descriptor (fileops/hash.go:22-31). It honours ctx between 1 MiB reads and
// core.library.hash.max_bytes_per_sec. Identity hashes with module-specific
// algorithms (filehash.BookFileHash) stay in the module and use RootFS.Open.
```

Library-scale hashing (backfills) runs as `tasks` work with a declared `tasks.Parallelism` (R7):
`tasks.Fixed(min(4, NumCPU), "disk-bound: per root device")` per root device, so two roots on two
disks hash in parallel and one disk is not thrashed.

### 5L.3.5 Quarantine

```go
type QuarantineID string // ULID

type QuarantineRecord struct {
	ID       QuarantineID
	Original Path
	Held     Path      // <root>/<Quarantine>/<id>/<rel>
	Owner    storage.Owner
	Reason   string
	At       time.Time
}

// Quarantine moves p (file or directory) to the same root's quarantine area
// with RenameNoReplace (same root ⇒ same filesystem ⇒ atomic) and records it in
// c/library/quarantine/<id> in the same logical step (journal first).
// Release moves it back to Original (or *to), never clobbering.
// Records are listed per owner; Purge is a separate, permissioned action.
func (m *manager) Purge(ctx context.Context, id QuarantineID) error
```

The books module keeps its book-level policy (`internal/quarantine/service.go:123-278`: which files,
path-history resumption, author/title folder naming) and calls the primitive per file set;
`RootSpec.Quarantine = ".failed"` keeps today's on-disk location. The same primitive is the
Sonarr/Radarr "recycle bin" for TV/Movies deletes (`raw/16-arr-domain-research.md` row 11), with
`core.library.quarantine.retention` as its cleanup age, applied by task `core.library.quarantine-prune`
(`Schedule: tasks.ScheduleFromConfig("core.library.quarantine.prune_schedule")`, SIMP-6; no bespoke
timer; a no-op while the retention is 0).

### 5L.3.6 Path mapping

```go
// Mapping says how a remote system's view of a path corresponds to a root.
type Mapping struct {
	Source  string // "download:<client-instance-id>", "worker:fingerprint", "itunes", "worker:whisper"
	Remote  string // prefix as the remote side writes it: "<dl-root>/complete", a Windows drive form "<drive>:\\<dir>", or a file URL of either
	Local   Path   // root + rel prefix it corresponds to
	Fold    bool   // case-insensitive match (Windows remotes only)
}

type PathMapper interface {
	// ToLocal: longest Remote prefix of this Source on a separator boundary
	// (pathutil.CutPathPrefix semantics, prefix.go:31); remote separators
	// normalised to '/'; file:// URLs percent-decoded per segment
	// (config.normalizeWindowsPrefix rationale, config.go:62-90). The result
	// must pass ValidateRel. With no mapping, an absolute remote path that
	// is itself under a root resolves identically (Deluge today, client.go:91).
	ToLocal(source, remote string) (Path, error)
	ToRemote(source string, p Path) (string, error)
	Mappings(source string) []Mapping
}

var ErrUnmapped = errors.New("library: remote path matches no mapping and no root")
```

Mappings are DB-backed config (`core.library.mappings`, [05-config](05-config.md)), edited through
`/api/core/v1/library/mappings` with `core:library.manage`. A download adapter (Sonarr's "remote path
mappings") calls `ToLocal(src, item.SavePath)` before handing an item to an import module;
`(Root, Rel)` is also the wire format for remote workers, generalising the fingerprint worker's
`SplitRoot`/`JoinRoot` (`root.go:17-31,79-90`) so each side joins `Rel` onto its own mount.

### 5L.3.7 Watchers

```go
type WatchOptions struct {
	Debounce   time.Duration // default 5s (watcher.go:24)
	Extensions []string      // nil = all files
	Recursive  bool          // default true
	OnError    func(error)   // also surfaced in health and the activity log
}

type EventKind uint8
const (
	EventInvalid EventKind = iota
	EventChanged  // create, write, rename, remove of a matching file
	EventRescan   // events were lost (queue overflow, watch-add failure, poll fallback tick): rescan the root
)

type Event struct {
	Kind EventKind
	Dir  Path // coalesced by directory; Root path for EventRescan
}

// Watch yields debounced batches until ctx is done. Implementation: one
// fsnotify watcher per root, recursive adds that skip AppDir, Quarantine and
// hidden dirs; a directory created later is added on its Create event
// (watcher.go:186-191). fsnotify errors — notably the overflow error — yield
// EventRescan instead of being dropped (watcher.go:178-183 only logs today).
// If adding watches fails for lack of kernel watches, the root degrades to
// polling every core.library.watch.poll_interval and health reports it.
```

`WatchAuto` picks notify for local filesystems and poll for network filesystems (statfs `f_type` of
NFS/SMB/FUSE), because inotify does not observe changes made by other clients. The supervisor
reconciles watched roots whenever the registry changes, without a restart (kept from
`server_lifecycle.go:501-503`).

### 5L.3.8 Ownership and sharing rules (multi-app)

| Rule | Enforcement |
|---|---|
| Every root has exactly one owner (a module ID or `core`). | registration |
| Roots of **different** owners may not overlap: neither resolved path may be inside the other, compared after `EvalSymlinks` *and* by device+inode of the root directory (catches bind mounts). Same-owner nesting is allowed only if the outer root lists the inner as excluded (its walkers skip it). | registration → `ErrRootOverlap`; boot re-check (a mount can change) → root disabled + health `critical` |
| Write: owner only. Read: owner + `ReadShares`. `RoleExternal`: nobody writes. | `Scope.FS` / `Scope.Transfer` with the owner bound by the kernel (COR-21); `Root.FS(caller)` is reachable only by core holders of `Manager` |
| `RoleDownload` roots are core-owned; every module with the download capability may read and move **out**, never write in. TV and Movies in one binary therefore share download roots but never library roots. | `Transfer` step 0 |
| A disabled module's roots: retained, unwatched, no transfers or quarantine (G1). | kernel enablement |
| The app's own data dir (DB, indexes, backups) may not be inside any root unless excluded via `AppDir`; registration warns and walkers skip it (successor of `appdirs`). | registration |

Permissions declared by core (names per 03 §3.6): `core:library.manage` (roots, mappings,
quarantine purge), `core:library.browse` (directory picker; replaces `BrowseDirectory`'s allow-list,
`fileops/service.go:46-118`, with "inside a registered root or a path an admin is registering").

## 5L.4 Lifecycle

1. Kernel loads root specs and mappings from `c/library/` (after storage migrations).
2. Modules declare required roles in their manifest (`module.Manifest.Library: []RoleRequirement`);
   the kernel reports unmet requirements as a setup task in the UI, not a boot failure (a fresh
   install has no roots yet).
3. Each root: resolve path, overlap check, sentinel check (`SentinelBootstrap` may create it),
   `os.OpenRoot`. A root that fails is **disabled** (health `critical`, transfers refused), the
   app still boots.
4. The kernel builds one `library.Scope` per enabled module (owner bound in) for `module.Deps.Library`.
5. Transfer recovery task runs; watchers (one per watched root, a `lifecycle.Component`; poll fallback
   ticks are in-process, not task schedules, SIMP-6) start with the task executor.
6. Shutdown: watchers stop (ctx), in-flight transfers finish their current step or roll back via
   the journal at next boot; `os.Root` handles close last.

## 5L.5 Error semantics

| Error path | Fail-open / fail-closed | Why |
|---|---|---|
| Path escapes root (`..`, symlink out, absolute rel) | closed | Confinement is the point (L2). |
| Sentinel missing/mismatched | closed for destructive ops (remove, move-out, quarantine, "not found ⇒ gone" decisions); open for reads | An unmounted share reads as an empty tree (`libroot.go:25-30`); reading is harmless, acting on absence is data loss. |
| Root path unresolvable at boot | closed for that root (disabled), open for the app | One bad mount must not take down the other roots. |
| Destination exists | closed (`fs.ErrExist`), unless `Replace` CAS matches | POSIX rename and `os.Create` silently clobber (`saferename.go:14-20`, `reflink.go:29-35`). |
| Seeding state unknown (`loaded=false`) | closed for Move/Remove; open for copy/link | Same rule as `protected_paths.go:37-41`. |
| Protected-path resolution error (link cycle, EACCES) | closed (protected) | Kept from `protected.go:330-341`. |
| Free space below floor | closed before writing | Backup space incident rationale (`backup.go:85-100`) applies to every large write. |
| Verify mismatch | closed: temp removed, source untouched, `ErrVerifyFailed` | A copy that does not match is not a copy. |
| Reflink unsupported | open → fallback when mode is `Auto`; closed when mode is `Reflink` | The caller asked for a specific guarantee. |
| Directory fsync EINVAL/ENOTSUP | open (success) | Not the durability mechanism on those filesystems (`copy.go:88-108`). |
| Other fsync error | closed | Something is genuinely wrong with the directory. |
| fsnotify overflow / error | open + `EventRescan` | Lost events must cause a rescan, never silence. |
| Watch limit exhausted | open → polling, health `degraded` | Coverage over efficiency. |
| Mapping not found | closed (`ErrUnmapped`) | Guessing a path is how imports land in the wrong place. |
| Journal write fails | closed (no file operation starts) | Without the journal a crash cannot be recovered. |

## 5L.6 Concurrency contract

- `Manager`, `Root`, `RootFS` (local), `PathMapper` are goroutine-safe; `File` is not.
- Transfers take a striped per-path lock on **both** `src` and `dst` (`core/internal/keyedmutex`,
  moved to core per 03 §3.2.1 E2), acquired in sorted order to avoid deadlock; two workers can
  never race on one destination name (the stat-then-create race documented at `reflink.go:29-35`
  cannot occur because creation is `O_EXCL`/`link` anyway, the lock removes wasted work).
- Library-scale work (hash backfills, rescans) runs only as `tasks` with declared
  `tasks.Parallelism` (R7); `library` itself starts no unbounded goroutines: one goroutine per
  watched root plus one debounce timer each.
- All long operations take `ctx` and stop at a 1 MiB boundary; a cancelled copy removes its temp.
- Watcher callbacks are delivered on the consumer's goroutine (pull iterator), so a slow consumer
  coalesces batches instead of growing an unbounded queue.
- Tests drive debounce and poll timing with `testing/synctest`.

## 5L.7 Config keys

| Key | Type | Default | Hot-reload? |
|---|---|---|---|
| `core.library.roots` | list of `RootSpec` (DB-backed) | empty | yes (supervisor reconcile) |
| `core.library.mappings` | list of `Mapping` (DB-backed) | empty | yes |
| `core.library.transfer.workers` | int | 4 | yes |
| `core.library.hash.max_bytes_per_sec` | int64 | 0 (unlimited) | yes |
| `core.library.hash.workers_per_device` | int | `min(4, NumCPU)` | yes |
| `core.library.watch.debounce` | duration | 5s | yes |
| `core.library.watch.poll_interval` | duration | 15m | yes |
| `core.library.free_space.warn_percent` | int | 10 | yes |
| `core.library.free_space.default_min_bytes` | int64 | 5 GiB | yes |
| `core.library.quarantine.retention` | duration | 0 (keep until purged) | yes |
| `core.library.quarantine.prune_schedule` | schedule (`tasks.ScheduleFromConfig`) | daily | yes |

ABO compatibility (09 owns aliases): `root_dir` → root `books-library`; `import_path:` rows →
`import-<id>` roots; `protected_paths` → `RoleExternal` roots or oracle entries;
`enable_disk_quota`/`disk_quota_percent` → `MinFree` of the books root;
`itunes.path_mappings` → mappings with `Source: "itunes"`; `path_aliases` → `RootSpec.Aliases`.

## 5L.8 Metrics

| Name | Type | Labels |
|---|---|---|
| `mediacore_library_transfers_total` | counter | `mode` (requested), `did`, `outcome` |
| `mediacore_library_transfer_bytes_total` | counter | `did` |
| `mediacore_library_transfer_duration_seconds` | histogram | `did` |
| `mediacore_library_hash_bytes_total` | counter | `root` |
| `mediacore_library_root_avail_bytes` | gauge | `root` |
| `mediacore_library_root_state` | gauge | `root`, `state` (ok, unmounted, disabled, degraded) |
| `mediacore_library_watch_events_total` | counter | `root`, `kind` |
| `mediacore_library_watch_rescans_total` | counter | `root`, `reason` (overflow, add_failed, poll) |
| `mediacore_library_quarantined` | gauge | `owner` |
| `mediacore_library_refusals_total` | counter | `reason` (escape, exists, protected, space, unmounted) |

`root` cardinality = number of configured roots (operator-bounded, tens).

## 5L.9 Tests

| Test | Proves |
|---|---|
| `TestRootFSRefusesDotDotAndSymlinkEscape` | confinement via `os.Root`, incl. a symlink swapped in mid-walk |
| `TestRenameNoReplaceNeverClobbers` | file, dir, dangling symlink destination; fallback path on a no-flag filesystem |
| `TestTransferCopyPublishIsNoClobber` | concurrent creator of the final name wins, our temp is removed |
| `TestTransferCrossDeviceMoveOrder` | source removed only after dest fsync + verify (fault-injected crash at each step) |
| `TestTransferRecoveryFromJournal` | every journal state recovers to done or rolled back |
| `TestTransferPermPolicyHasNoDefault` | zero `PermPolicy` rejected; ingest ignores a 0600 source |
| `TestTransferAutoFallbackChain` | reflink → hardlink → copy, `Did` reports reality |
| `TestMoveRefusedWhenSeedingUnknown` | oracle `loaded=false` blocks Move, allows copy |
| `TestTransferChecksFreeSpaceFirst` | nothing written when below the floor |
| `TestSentinelBlocksDestructiveOps` | unmounted (empty) root: reads ok, remove/move-out refused |
| `TestSentinelBootstrapLegacyRoot` | heuristic pass creates sentinel; later empty mount refused |
| `TestRootOverlapAcrossOwnersRefused` | path nesting, symlinked alias, bind mount (device+inode) |
| `TestDownloadRootSharedMoveOutOnly` | two modules move out; neither writes in |
| `TestPathMapperLongestPrefixBoundary` | `/dl` vs `/dl-old`, Windows remote, file URL, Fold |
| `TestPathMapperNoUnicodeFolding` | NFC and NFD names stay distinct |
| `TestWatchDebounceCoalesces` (`synctest`) | burst → one batch per directory |
| `TestWatchOverflowYieldsRescan` | injected fsnotify overflow error |
| `TestWatchFallsBackToPolling` | injected watch-add ENOSPC |
| `TestQuarantineReleaseRoundTrip` | no clobber on release; record lifecycle |
| `TestHashSameDescriptorSizeAndDigest` | size and digest describe the same bytes |
| `TestDisabledModuleRootsInert` | G1 |
| `TestTransferInCommitsWithCallerBatch` | `TransferIn` + caller rows land in one commit; `CrashClone` before the commit leaves a `done` row reported by `PendingTransfers`, never undone |
| `TestScopeCannotActAsAnotherOwner` | a module's `Scope` cannot write another owner's root or quarantine its files |

## 5L.10 Migration from ABO

| ABO code | Becomes |
|---|---|
| `internal/libroot` (`Mounted`, `ReportPathNotify`, ownership checks) | `library.Root.Mounted` (bootstrap heuristic) and `Root.AppDir`; books keeps `.reports` as its app-dir name |
| `internal/pathutil` (`ValidateRel`, `SplitRoot`, `JoinRoot`, `CutPathPrefix`, `IsWithin`, `UnderProtectedPath`, `ShouldSkipDir`) | core `library` internals (`Path` validation, `Resolve`, `Abs`, walk skip rules); `FrozenITunesSegment`, `VersionsDirName`, `PathVars` naming stay in books |
| `internal/fileops` copy/reflink/hash/sync seams | `core/internal/libfs` + `Transfer`/`Hash`; `book_file_hash_recorder.go`, `write_tags_safe.go` (tag rewrite + provenance) stay in books on top of `RootFS` |
| `internal/fileops/service.go` + `register.go` (browse, `init()` registration) | core browse route under `core:library.browse`; `init()` deleted (R5) |
| `organizer.safeRename`, `versions.renameNoReplace`, `quarantine.moveNoClobber` | `RootFS.RenameNoReplace` (three implementations → one) |
| organizer strategies (`organizer.go:285-306`) | `Transfer` with `Auto`/`Copy`/`Hardlink`/`Reflink`/`Symlink` |
| `internal/quarantine` | books policy over `Manager.Quarantine`/`Release` with `Quarantine: ".failed"` |
| `internal/watcher` + supervisor | `Manager.Watch`; books' audio-extension filter passed as `Extensions` |
| `internal/appdirs` | root registration's app-dir exclusion + `WalkOptions` skip rules |
| `internal/diskstats` | `Root.Usage` (moved into core as an internal package) |
| `deluge.ProtectedPathCache` | a `ProtectionOracle` implemented by the Deluge adapter (05-download); static `protected_paths` → `RoleExternal` roots |
| `itunes.path_mappings`, `WHISPER_PATH_MAP`, fingerprint worker wire format | `PathMapper` sources `itunes`, `worker:whisper`, `worker:fingerprint` |
| `pending_file_op:` journal (`file_io_pool.go:31`) | `c/library/transfer/` journal; legacy rows drained by a books migration at first boot |
| `filehash.BookFileHash` | stays books (identity algorithm), reads through `RootFS` |

Order: introduce `library` with roots registered from ABO config (no behaviour change), move
writers one subsystem at a time behind the same tests (organizer, versions, quarantine, deluge
import, iTunes transfer), then delete `fileops` copy/reflink and the three renames.

## 5L.11 Q4 — one storage-root abstraction or per-module drivers?

**One core abstraction.** Evidence: every ABO file mutation is a local POSIX path operation; the
only "remote" cases are workers that mount the same tree under another prefix (`pkg/ops/lanes.go:13-15`,
`transcribe/path.go:28-40`, `pathutil/root.go:17-31`), which path mapping covers. TV and Movies need
the same roots/import/download semantics (`raw/16-arr-domain-research.md` §1.4 root folders with
free space, line 382 remote path mapping, row 11 "hardlink-or-copy, free-space check, recycle bin"). Per-module drivers would duplicate the safety rules this file centralises and make
cross-module transfers (download root → TV library) impossible to verify. `RootFS` is an interface
so a remote driver (SFTP, S3-compatible object storage) can implement it later; until a second
driver exists, `library` ships only the local one and modules cannot register drivers.

## 5L.12 Open items

1. Whether any production import path lies inside `root_dir` (same owner nesting must then be
   declared at migration). The migration reports overlaps instead of failing; owner decides.
2. Symlink organize strategy: `os.Root.Symlink` does not validate targets; this spec restricts
   targets to `library.Path`s. Confirm no production install relies on links pointing outside roots.
3. Network-filesystem detection for `WatchAuto` uses statfs magic numbers on Linux; darwin and
   Windows default to notify. Revisit if TV/Movies users commonly watch SMB shares from macOS.
4. Reflink on Windows (ReFS block cloning) is out of scope until requested.

## 5L.13 Objections to 03

None.

## 5L.14 Out-of-scope defects found

| Defect | Evidence |
|---|---|
| The watcher drops fsnotify errors (including queue overflow) after logging, so lost events never trigger a rescan; a failed `Add` on a subdirectory is only a warning, leaving it silently unwatched. | `internal/watcher/watcher.go:155-160,178-183` |
| `quarantine.moveNoClobber` and `organizer.safeRename` use Lstat-then-rename with a TOCTOU window although `versions.renameNoReplace` already has the atomic kernel form. | `internal/quarantine/service.go:853-870`; `internal/organizer/saferename.go:34-48`; `internal/versions/rename_noreplace.go:16-22` |

## Review history

Round-1 findings applied to this file (`docs/plans/platform-core/reviews/spec-round-1-*.md`):

- COR-4 — `TransferIn(b, …)` on `Manager` and `Scope`; step 6 stages the journal end into the caller's batch; `PendingTransfers` for the crash window (request from R-F).
- COR-21 — `library.Scope`, the module-scoped handle with the owner bound by the kernel; `Manager` calls name the caller (request from R-G).
- COR-19 — `library` is L4.
- SIMP-6 — quarantine prune is a `tasks.ScheduleFromConfig` task; transfer recovery runs once at start; watcher polling is an in-process component.
- R7 / SIMP-17 — hashing backfills declare `tasks.Fixed(n, reason)`.
