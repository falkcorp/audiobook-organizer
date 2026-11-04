<!-- file: docs/specs/platform-core/05-updater.md -->
<!-- version: 1.10.0 -->
<!-- guid: 87f3821c-d9b3-427d-b91f-dad6de16aef8 -->
<!-- last-edited: 2026-11-04 -->

# 05 — Updater (`core/updater`)

Evidence commit `748fd787a`. Raw inputs: [`raw/12-cross-cutting.md`](../../plans/platform-core/raw/12-cross-cutting.md) §6,
[`raw/14-build-ci.md`](../../plans/platform-core/raw/14-build-ci.md) (release pipeline),
[`raw/07-plugin-registry-sdk.md`](../../plans/platform-core/raw/07-plugin-registry-sdk.md) §1.3 rows 23–24.
Layer **L4** (03 R4 v1.2.0); imports `tasks` (check/apply/rollback are tasks), `health`, `events`,
`config`, `storage`, `integrations/providerhttp` (outbound HTTP), `integrations/notify` (the
update-found notification), `backup` — all L4 or lower; none of them imports `updater`, so no cycle.

## 5U.1 Purpose & scope

Self-update of an app binary that runs outside a container: discover a newer release from the
**app's** release source, swap the binary atomically while keeping the previous one as `<exe>.prev`,
and restart gracefully. Each app decides whether it ships an updater at all; when it does, the updater
is **off by default**.

**S8 moves the updater; it does not rebuild it (A-6, ADV-S5).** The core updater is the HEAD updater
as 09 S0 hotfixes it, moved into `core/updater` and parameterised by release source. It keeps the
hotfix's behaviour: restart with exit 75, the `.prev` slot shared with `make rollback`, semver
comparison with the local-build rule, the level and floor checks of the new binary against the store
before the swap, and **check-only unattended mode** — a scheduled run only checks and notifies; a swap
happens only when an admin applies a named release, and a rollback only when an admin (or
`make rollback`) asks for it. Signed release manifests, the boot-attempt counter, automatic rollback,
the committed unit's binary-layout migration and the real-RC staging check are **deferred-build**
(5U.3 "Deferred-build members"); the hotfix exists precisely because the HEAD updater has none of them,
and nothing at S8 needs them.

Out of scope: container image updates (the orchestrator's job; the updater refuses to run in a
container), database rollback (covered by backups and the G5 compatibility windows, not by the
updater), release-pipeline implementation (the spec states what the pipeline must publish).

## 5U.2 Current state at HEAD

| Fact | Anchor |
|---|---|
| Repo slug hard-coded to one app | `internal/updater/updater.go:36,71` |
| User agent hard-coded `audiobook-organizer/<ver>` | `updater.go:115,168,239,301` |
| "Update available" is string inequality, so any different tag (including an older one) is an "update" | `updater.go:153,204` |
| `develop` channel reads the commits API; applying it is refused | `updater.go:161-210,288-292` |
| Download has **no checksum or signature verification and no size cap**; writes `<exe>.new` with mode 0755 next to the binary | `updater.go:214-265` |
| The previous binary is deleted right after the swap: no rollback | `updater.go:267-280` |
| Expected asset is a raw binary `audiobook-organizer-<GOOS>-<GOARCH>` | `updater.go:318-326` |
| The in-repo goreleaser config publishes **tar.gz archives** `<project>_<ver>_<Os>_<arch>.tar.gz` and a `checksums.txt`; the checksums are not used by the updater | `.goreleaser.yml:38-56`; which workflow actually publishes release assets is inside the reusable workflow (`.github/workflows/release-prod.yml:45`) — UNKNOWN from this repo, so the asset contract is unverified |
| Restart is `os.Exit(0)`: no graceful shutdown, no ops drain, deferred store close skipped | `updater.go:329-334`; called as `go s.updater.RestartSelf()` after the HTTP response (`internal/server/update_handlers.go:55-63`) |
| Two independent auto-apply paths: the `updatescheduler` ticker and step 1 of the maintenance-window op | `internal/updater/scheduler.go:75-109`; `internal/server/scheduler_maintenance_window_op.go:73-92` |
| Their windows differ by default: updater window 2–5, maintenance window 1–4 | `internal/config/config.go:2994-2995` vs `:3003-3005` |
| Ticker measures from process start (resets each restart, the defect `interval_clock.go` fixed for tasks) | `scheduler.go:47-48` |
| Defaults: disabled, channel `stable`, every 60 min | `config.go:2991-2993` |
| Built by `init()` service registration reading `config.AppConfig` directly | `internal/updater/register.go:59-90` |
| Routes: `GET /api/v1/update/status`, `POST /update/check`, `POST /update/apply` (`settings.manage`, apply behind the credential guard) | `internal/server/server_lifecycle.go:2102-2104` |

## 5U.3 Go API

```go
package updater // github.com/falkcorp/mediacore/updater

// Options is supplied by the app's main through app.Options.Updater.
// A nil *Options means the app ships no updater: no service, no tasks, no
// routes, no component (G1-style absence, not a disabled toggle).
type Options struct {
	AppID       string // must equal the new binary's self-test app field
	BinaryName  string // archive member to install, e.g. "audiobook-organizer"
	Source      Source // the release source: the one parameter A-6 adds to the HEAD updater
	AssetName   func(r Release, goos, goarch string) string // default: GoreleaserArchive(AppID)
	DefaultMode Mode   // the app's default; core forces ModeOff in containers
}

// UpgradeRequirement is a binary's declared upgrade floors, as
// `<exe> --print-upgrade-floor` prints them (05-storage §5S.3.7). Two releases
// are involved and never share a name (OPS4-5): the FLOOR release is the one
// that declares the requirement (it deleted code an older store needs); the
// STEPPING-STONE release, named in Release, still carries that code and
// contains no deletion.
type UpgradeRequirement struct {
	Release string            `json:"release"`          // the stepping-stone release to install and run first; named in messages and the banner
	Owners  map[string]uint64 `json:"owners,omitempty"` // owner -> minimum applied version, e.g. {"books": LegacyCap}
}

type Mode uint8

const (
	ModeOff   Mode = iota // no network calls at all (the default for every app)
	ModeCheck             // scheduled check + notification; apply only by an admin, never unattended
)

type Channel string

const (
	ChannelStable     Channel = "stable"
	ChannelPrerelease Channel = "prerelease" // replaces "develop" (commits cannot be applied)
)

// Source is a release feed. Adapters: updater/github (parameterised), and
// updater/manifest (a static HTTPS JSON feed for self-hosted releases).
type Source interface {
	Latest(ctx context.Context, ch Channel) (Release, error)
	Open(ctx context.Context, a Asset) (rc io.ReadCloser, size int64, err error)
}

type Release struct {
	Version     string // semver, leading "v" optional
	Channel     Channel
	Notes       string
	URL         string
	PublishedAt time.Time
	Assets      []Asset
}

type Asset struct {
	Name string
	URL  string
	Size int64
}

// GoreleaserArchive returns the asset namer for goreleaser's default archive
// template "<project>_<version>_<Title(os)>_<arch>.tar.gz" with amd64 rendered
// as x86_64 (the template in .goreleaser.yml:42-49).
func GoreleaserArchive(project string) func(Release, string, string) string

type Status struct {
	Mode           Mode
	Channel        Channel
	Current        string
	Latest         string
	Available      bool   // semver(Latest) > semver(Current); never a downgrade
	LastChecked    time.Time
	LastError      string // fixed token
	PreviousKept   string // version of <exe>.prev, if any (the manual rollback target)
	DisabledReason string // "off", "container", "dev_build", "binary_not_writable" (OPS2-4)
	// LocalBuild: the current version carries a `git describe` suffix (OPS4-17);
	// Latest is not compared with it.
	LocalBuild bool
	// AutoApplyPaused: the operator had asked for unattended apply (ABO's
	// auto_update.enabled=true, 5U.8), which this updater does not do (A-6).
	AutoApplyPaused bool
	// SteppingStone names the stepping-stone release to install and run first
	// when the store does not meet the floor the newest release's binary
	// declares (OPS2-11, OPS3-7); apply refuses until the store's recorded
	// state meets it.
	SteppingStone string
}

type Updater struct{ /* unexported; one per App */ }

func New(opts Options, d Deps) (*Updater, error)
func (u *Updater) Status() Status
func (u *Updater) Check(ctx context.Context) (Status, error)
// Apply is called only from the core.updater.apply task body.
func (u *Updater) Apply(rc *tasks.RC, version string) error
// Rollback is called only from the core.updater.rollback task body: it runs
// the reader check on <exe>.prev, swaps it back and requests a graceful restart.
func (u *Updater) Rollback(rc *tasks.RC) error

// Deps are core services, injected by the kernel.
type Deps struct {
	HTTP        *providerhttp.Client // instance-scoped outbound client (05-metadata)
	Backup      backup.Runner        // 05-backup
	Restarter   Restarter            // kernel: Lifecycle.RequestRestart (06 §6.5.2), graceful shutdown then restart
	Health      *health.Registry
	Events      *events.Bus
	Notify      *notify.Service      // stages core.update.found.v1 notifications (outbox-backed, COR2-3)
	Store       storage.Keyspace     // c/updater/ (one key, 5U.4 Check)
	ReaderCheck ReaderCheck          // kernel-bound to the single compat/format guard (05-storage)
	StoreState  StoreState           // kernel-bound to 05-storage's applied-steps sidecar beside the DB
	StateDir    string               // <state-dir>/updates (opened with os.Root)
	Log         *slog.Logger
}

// ReaderCheck asks whether the binary at exe may open the store at dbPath as
// it is now: the decision scripts/storage_format_guard.py makes for
// `make rollback`, implemented once by 05-storage's guard (SIMP-18) over the
// sidecar files, so it needs no open store. The kernel binds it to
// core/internal/pebble.CheckReader (05-storage §5S.3.7) with the resolved state
// directory; dbPath is core.storage.path. nil = allowed; otherwise an error
// carrying one of CheckReader's fixed reason tokens (05-storage §5S.3.7):
// "level_newer", "format_newer" (pre-guard binary), "upgrade_floor" (the store
// has not applied what exe's declared floor requires; a DB path with no
// directory, or an empty one, and no sidecars passes, the exemption boot gives
// an empty store, DATA8-11), "restore_pending" (restore/pending.json exists: a
// staged swap of any kind, a restore or a factory reset, KindReset; R8-1,
// SIMP8-1), "restore_inflight" (a swap journal restore/<id>/ holds
// inflight.json, which a KindReset swap keeps from the set-aside until the
// post-reset hook journals hook_done, or, for a checkpoint restore,
// inflight-checkpoint.json; DATA3-6, R7-2), "sidecar_unreadable",
// "cannot_determine" (a probe could not answer; refuses). The error message
// names the swap's kind. There is no reset-only token: a factory reset is a
// kind of the one swap (R8-1). One journal passes: a KindCheckpoint journal at
// swapped or failed (its restore/<id>/inflight-checkpoint.json; the offline
// restore-checkpoint path) does not refuse a candidate exe whose levels and
// floors admit the restored store AND which implements the journaled swap,
// detected by the S2b fast exit `--print-restore-journal`; a candidate without
// it (a pre-S2b build) is restore_inflight (R7-4, DATA7-12, DATA8-6). The
// scripts' one override, ROLLBACK_IGNORE_RESTORE=1, belongs to
// scripts/storage_format_guard.py (make rollback, deploy) and overrides
// restore_pending only, never restore_inflight (OPS8-7); the updater has no
// override and refuses.
type ReaderCheck func(ctx context.Context, dbPath, exe string) error

// StoreState reports the store's recorded migration state — the applied
// version per owner — by reading the applied-steps sidecar that 05-storage
// defines and every guard-aware boot rewrites beside the DB (a file, like the
// level sidecar ReaderCheck reads, so the same answer is available to tools
// outside the process and needs no open store). Never the binary version
// (OPS3-7). A missing or unreadable sidecar is an error (fail-closed). It is
// consulted only against a NON-EMPTY floor: an empty --print-upgrade-floor
// passes without reading the sidecar, so a HEAD store, which has none until an
// S0+ boot writes it, never blocks an update whose binary declares no floor; a
// non-empty floor with a missing sidecar refuses with "boot any release >= S0
// once" (OPS7-10). The one exception is an empty store: no directory at
// dbPath, or an empty one, and no sidecars. StoreState then returns
// StoreApplied{Empty: true}, which meets every requirement, the exemption boot
// gives an empty store (05-storage §5S.4 step 3; DATA8-11).
type StoreState func(ctx context.Context, dbPath string) (StoreApplied, error)

type StoreApplied struct {
	Empty  bool // no DB directory (or an empty one) and no sidecars; Meets is true
	Owners map[string]uint64
}

// Meets reports whether s satisfies r: every listed owner at or above its
// version. A nil r, and any r on an Empty store, is always met.
func (s StoreApplied) Meets(r *UpgradeRequirement) bool

type Restarter interface {
	// Restart runs the full shutdown sequence within the stop budget
	// (05-health 5H.4) and then exits/execs per core.updater.restart_mode.
	// The kernel binds it to Lifecycle.RequestRestart(reason) (06 §6.5.2),
	// the one restart path the staged restore and the staged factory reset
	// also call: in exit mode it exits with core.lifecycle.restart_exit_code
	// (default 75, the S0 hotfix's code); in exec mode it execs <exe> after
	// the same drain (OPS8-12).
	Restart(ctx context.Context, reason string) error
}
```

```go
package github // github.com/falkcorp/mediacore/updater/github

// New returns a Source for owner/repo. BaseURL allows a GitHub Enterprise or
// API-compatible forge; TokenSecret names a core/secrets entry (never a value).
func New(owner, repo string, opts ...Option) updater.Source

type Option func(*config)

func BaseURL(u string) Option
func TokenSecret(name string) Option
```

ABO passes `github.New("falkcorp", "audiobook-organizer")`; a TV app passes its own slug. Nothing in
core names a repository.

### Deferred-build members

Design sketches (non-normative, A-15): no extraction stage builds them, and the SDK freezes none of
them. Each lands in its own later release, with its own tests, as an additive change to the built
updater above. The round-8 design of each is preserved in git history at the commit before A-6, and
each is re-reviewed when it is built, against the code as it is then.

| Member | What it adds | Built when, and why not at S8 |
|---|---|---|
| **Signed release manifests** | `ReleaseManifest{app, version, channel, assets, min_upgrade_from}`, `TrustedKey`, `Options.Manifest`/`Signature`/`TrustedKeys`/`RevokedKeyIDs`, ed25519 verification over the manifest bytes (SEC-27), the signed `min_upgrade_from` copy of the binary's floor, `Options.LastUnsignedVersion` with the legacy unsigned path and `core.updater.allow_unsigned_legacy` (OPS-23, SEC2-23), the `no_trusted_keys`/`unsigned_updates` reasons, the pipeline's signing step and key rotation | when the reusable release workflow can sign (its asset contract is UNKNOWN from this repo, 5U.2); the S8 updater's trust level is HEAD's: HTTPS from the configured release source |
| **Boot-attempt counter** | `Boot(stateDir, exe, self, check)` as 06 §6.5.1 phase P−1, `Self{Version, BinaryID}`, `BootResult`, `Pending`, the pending marker `updates/pending.json`, `updates/history.jsonl` (`superseded`, `rolled_back`), `core.updater.max_boot_attempts`, `core.updater.confirm_after`, `ReaderCheckOptions.IgnoreRestoreStagedBy` (OPS2-3, OPS4-8, OPS4-9, R5-7), the restart exit code a boot refusal uses while a marker names the running binary (OPS5-15), and `BootResult.Pending` handed to storage open for `RequiresSelfServed` (OPS2-2) | with automatic rollback, which is its only consumer; its marker write, history append and renames are boot writers, so its PR extends the crash matrix (05-storage, A-8) |
| **Automatic rollback** | `Boot`'s rollback to `<exe>.prev` after the attempt budget, in-place exec (OPS2-18), `rollback_blocked` when `ReaderCheck` refuses `.prev` (OPS-6), `Status.SkippedAfterRollback` (OPS2-6), the rollback alert rule, and **unattended apply**: `ModeApply`, `core.updater.schedules.apply` (window member 0) | with the counter: an unattended swap without an automatic way back is what the S0 hotfix stopped (09 S0) |
| **Binary-layout migration** | the committed unit moved to layout (a) of 5U.5 (unit default, drop-in `ExecStart`, the sudoers template's `cp` lines, `DEPLOY_BIN`, `deploy-match-unit-type.sh`, the launchd plist's binary path, binary **and** `.prev` copied, a dry-run `make rollback` as the deploy user; OPS3-15, OPS4-4) | when unattended apply is built; until then the committed layout reports `binary_not_writable`, as HEAD's updater already fails there (5U.5) |
| **Real-RC `--updater-check`** | the staging done-check between two real signed prereleases (RC *n* applies RC *n+1*), `scripts/staging/provision.sh --updater-check` opening egress to the release hosts (R7-6, OPS8-3, SIMP8-10) | with signed manifests, which it exercises |
| `UpgradeRequirement.Steps`, `StoreApplied.StepsDone` | storage `StepRef`s (a cutover's data migration) that must be done before a release may run, read from the sidecar's `<owner> done <id>` lines; `Meets` then also checks them | with the first per-family cutover (05-storage §5S.3.13; SIMP4-4) |

## 5U.4 Lifecycle

**Check** — task `core.updater.check`: Shape Task, `Effects: Writes(tasks.Res("core","updater"))`,
`Resume: ResumeDrop`, `Perms.Start: core:system.manage`, `Priority: PrioritySystem`,
`Schedule: tasks.ScheduleFromConfig("core.updater.schedules.check").When(mode >= ModeCheck).Live()` —
the one schedule mechanism on the durable clock, with the key in the updater's own namespace
([05-tasks](05-tasks.md) 5T.3.8; SIMP-6, R2-7; fixes the restart-reset ticker); the check route starts
it with `mode: live`. It calls `Source.Latest`, compares semver (`golang.org/x/mod/semver`), refuses
to report an update for a `dev`/unversioned build or for a **local build** — a current version carrying
a `git describe` suffix (`-<n>-g<hex>` or `-dirty`; main builds take `VERSION` from
`git describe --tags --always --dirty`, `Makefile:9`), which semver reads as a prerelease and so orders
below its own base tag: such a status sets `LocalBuild` and reports "local build, not compared" instead
of offering an older release as newer (OPS4-17) — probes that the executable's directory is writable
(apply step 1), records `Status`, and sets component `core.updater` (Up / Degraded `check_failed`,
`binary_not_writable`, `auto_update_paused`). When a version first appears it writes `c/updater/seen`
`{version, at}` and, in the **same batch** (through the run's `Writer`), stages the
`core.update.found.v1` notification with `notify.Service.Publish`, so each new version notifies exactly
once across restarts; it also publishes the topic on the bus for UI refresh (declared in the one topic
catalog with `Notify`, [05-events](05-events.md) 5E.3.1; the bus publish alone notifies nobody, COR2-3).
A check never applies anything: that is the hotfix's check-only unattended mode (09 S0), kept (A-6).

**Apply** — task `core.updater.apply`: Shape Task, `Effects: Writes(tasks.Res("core","binary"))`,
`Resume: ResumeDrop`, `ConcurrencyKey: core.updater`, `Cancel: Cooperative(30s)` until step 7,
`Perms.Start: core:admin`, `Priority: PrioritySystem`, **no `Schedule`**: it runs only when an admin
starts it through `POST /api/core/v1/updater/apply` `{version}` (5U.10), behind the credential guard as
at HEAD. The maintenance-window step 1 and the separate ticker are deleted. Steps, each a `rc.Phase`:

1. Preconditions: not in a container; current build is versioned; target newer than current, except
   that on a local build (`LocalBuild`) apply runs only for the version the operator names explicitly;
   no swap staged and none in flight, of either kind (`restore_pending`, `restore_inflight`;
   05-backup §5B.3.4's one slot and journal, in which a factory reset is the kind `KindReset`, 05-storage
   §5S.3.15; R8-1): a swap would make a staged restore be rejected as foreign (09 S1/S8), turn the admin's
   audited reset into a rejected intent, or leave a half-done set-aside to a binary that cannot finish it
   (R7-2, OPS7-11). It is read through `ReaderCheck(core.storage.path, <exe>)` with no override (a
   `KindCheckpoint` journal at `swapped` or `failed` passes for a candidate that admits the restored store
   and implements the journaled swap, R7-4, DATA8-6). And the **executable's directory is writable by the
   process**, checked by creating and removing a probe file through an `os.Root` opened on that directory
   (OPS2-4). An unwritable directory sets `DisabledReason: "binary_not_writable"`, refuses apply and
   rollback, and keeps component `core.updater` Degraded `binary_not_writable` while mode ≥ check, so the
   operator learns that updates cannot be installed instead of the update silently failing at step 4
   (5U.5 lists the supported layouts).
2. Download the archive, and `checksums.txt` when the release publishes one, into an `os.Root` on
   `<state-dir>/updates/<version>/`, via `providerhttp` (throttle, retry, user agent
   `<AppID>/<version>`), each capped by `core.updater.max_download`.
3. Integrity: when `checksums.txt` is published (`.goreleaser.yml:55-56`), the archive's SHA-256 must
   match its line; a mismatch aborts and deletes the download (`checksum_failed`). This catches a
   truncated or corrupted download, not a substituted one: the trust level is HEAD's, HTTPS from the
   configured release source, until signed manifests are built (deferred-build).
4. Extract only the `BinaryName` member (regular file, no path components, size-capped) to
   `<exe>.new` in the executable's directory (same filesystem, so the rename is atomic); fsync
   file and directory.
5. Self-test and the store checks — the hotfix's checks, in the same order (09 S0). Run
   `<exe>.new --version-json` (10 s timeout, empty environment except `PATH`); it prints one JSON object
   with exactly the fields `{app, version, commit, ui_build_id, binary_id}`, the list 09 S0 and §9.6 row
   20 pin (FE6-4), and the self-test requires `app == AppID` and `version == target`. Then
   `ReaderCheck(core.storage.path, <exe>.new)`: the new binary's levels, format and floor against the
   store's sidecars, the decision `make rollback` and `deploy` make (SIMP-18); a refusal aborts with its
   reason, `upgrade_floor` as `stepping_stone_required`. To name the stepping stone it runs
   `<exe>.new --print-upgrade-floor`: an empty output passes without reading the sidecar (OPS7-10);
   otherwise `StoreState` must `Meet` it, and when it does not, apply aborts with
   `stepping_stone_required`, sets `Status.SteppingStone` to the printed `release` and a UI banner
   ("install <release> and run it until the store has applied what this release needs"); a `StoreState`
   error refuses the same way (fail-closed; OPS6-1, DATA8-11). Nothing is swapped, and `<exe>.new` and
   the download are deleted.
6. Pre-update backup through `backup.Runner` ([05-backup](05-backup.md), in the format the previous
   build can restore during the window); failure aborts unless `core.updater.require_backup=false`.
7. Swap: rename `<exe>` → `<exe>.prev`, `<exe>.new` → `<exe>`; fsync directory. `.prev` is the same slot
   `make rollback` uses (`Makefile:892-913`), so mixed use of the two mechanisms never diverges
   (OPS-6). From here the task is uninterruptible (two renames).
8. `Restarter.Restart`, bound to the kernel's `Lifecycle.RequestRestart` (06 §6.5.2), the one restart
   path that a staged restore and a staged factory reset also use (OPS8-12) — graceful shutdown within the
   stop budget, then exit with `core.lifecycle.restart_exit_code` (default 75, so the committed unit's
   `Restart=on-failure`, `deploy/audiobook-organizer.service:111`, restarts it; `0` needs
   `Restart=always`) or `exec` the new binary in place (`core.updater.restart_mode=exec`, Unix only).

**After the restart.** No marker is written and no boot is counted: the new binary boots like any
deployed binary, and a store refusal at boot (`ErrUpgradeFloor`, exit 78) is what step 5 exists to
prevent. If the release misbehaves, the admin rolls back by hand: task `core.updater.rollback`
(`POST .../rollback`, `core:admin`, credential guard, `ConcurrencyKey: core.updater`) runs
`ReaderCheck(core.storage.path, <exe>.prev)` first and refuses with its reason (a `.prev` older than a
level the store reached cannot serve it), and otherwise renames `<exe>` → `<exe>.failed`, `<exe>.prev`
→ `<exe>`, fsyncs and restarts through `Restarter`; `make rollback` makes the same decision from the
operator's machine. `<exe>.prev` is kept until the next apply or deploy replaces it.

**Boot writers (A-8).** The built updater writes nothing during boot: its only durable key is
`c/updater/seen`, written inside the check task's run, and its files (downloads, `.new`, `.prev`,
`.failed`) are written inside the apply and rollback runs. So it adds no entry to 05-storage's crash
matrix. The deferred `Boot` is a boot writer and extends the matrix in the PR that builds it.

Database caveat: a rollback restores the **binary**, not the data. The previous release must read
the database the new release left — that is gate G5 and the compatibility window
([09-abo-on-core](09-abo-on-core.md) §9.2.1), checked by `ReaderCheck`; the pre-update backup is the
last resort, restored by an operator, never automatically (automatic restore would discard writes
made after the update). Persistence: one key, `c/updater/seen` (the last notified version); every
other updater file is under `<state-dir>/updates/` or beside the executable.

## 5U.5 Policy

| Rule | Default | Rationale |
|---|---|---|
| App ships an updater | per app (`app.Options.Updater` nil or not) | a TV/Movies app distributed only as a container image needs none |
| Mode | `ModeOff` for every app unless its `DefaultMode` says otherwise **and** the operator sets `core.updater.mode` | no unsolicited network calls; current default disabled (`config.go:2991`) |
| Unattended apply | never: a scheduled run only checks and notifies; an operator who asked for automatic apply (`auto_update.enabled=true`) gets `mode=check`, `Status.AutoApplyPaused`, component Degraded `auto_update_paused` and a Settings banner | the S0 hotfix's check-only rule, kept (A-6): the updater has no boot-attempt counter, so an unattended swap would have no automatic way back |
| Containers | forced off, `DisabledReason: container` (`sysinfo.Info.Container`, [05-health](05-health.md)) | replacing a binary inside an image is wrong |
| Downgrades | never offered; only `Rollback` to the kept previous binary, and only when `ReaderCheck` allows it; a describe-suffixed local build is never compared, so it is never offered an older release as newer | string-inequality defect (`updater.go:153`); OPS-6; semver orders `v0.223.0-5-gabc1234` below `v0.223.0` (OPS4-17) |
| Who | check: `core:system.manage`; apply/rollback: `core:admin` (audited as `core.update.apply` / `core.update.rollback`, 05-rbac §7.1; credential-guarded route as today) | RBAC (05-rbac); SEC-6 |
| Binary directory | the process must be able to create files in the executable's directory; otherwise `binary_not_writable` (apply and rollback refused, Degraded while mode ≥ check) | the swap is two renames in that directory (steps 4 and 7); OPS2-4 |
| Stepping stones | a release whose upgrade floor the store's **recorded state** does not meet is never applied; `stepping_stone_required` names the stepping-stone release to install and run first. The check is the new binary's own floor (`<exe>.new --print-upgrade-floor` against `<db>.storage-applied`, before the swap), which the 09 S0 hotfix makes from S0 and this updater keeps (step 5), so it covers the floor release, which ships at S5; the `deploy` recipe runs the same pair before it copies a binary (09 S0; OPS6-1). This protection exists only where the running updater is this one or the 09 S0 hotfix; a pre-S0 updater swaps any newer binary unchecked (`internal/updater/updater.go:214-280`), so such an auto-updating install that takes the floor release exits 78 at boot with `ErrUpgradeFloor`, and the floor release's note and the Docker last-line message name the stepping stone (DATA4-18) | R2-4, OPS4-5: books legacy migration code is kept in every release before the **floor release** (the first T8 release, which deletes it and declares books' floor, 05-tasks 5T.6.2), so a store below books' `LegacyCap` needs the **stepping-stone release** (which still carries that code) to have **run**, which only the store can prove (OPS3-7); a later cutover floor uses the same check; OPS2-11 |

**Supported install layouts (OPS2-4, OPS4-4).** The updater replaces the binary in place, so it works
only where the service user owns the executable's directory: (a) a dedicated service-owned binary
directory **outside** the state directory — for example a service-owned `bin` directory under the
install prefix — mode 0755 with binaries 0755, referenced by the unit's `ExecStart`. It must not be
under the state directory: that directory is 0700 and holds credentials
(`internal/config/state_dir.go:112-116,135`), while `make rollback` and `deploy-match-unit-type.sh` run
their `.prev` probes (`--print-storage-format`, `--print-storage-levels`, `--print-upgrade-floor`,
`--print-capabilities`) as the unprivileged deploy user (`Makefile:899-902`), which could not enter it,
so every rollback would report "no .prev found"; (b) a launchd agent whose binary lives in a
user-writable directory; (c) a manual install the operator owns. The committed systemd unit copies the
binary into a root-owned system binary directory with `sudo cp` and runs it as an unprivileged `User=`
with `NoNewPrivileges=yes` (`deploy/audiobook-organizer.service:42,88,122`), so HEAD's
`os.OpenFile(<exe>.new)` already fails there with EACCES (`internal/updater/updater.go:254-258`): on
that layout the core updater reports `binary_not_writable`, exactly as useful as HEAD's updater is
there. Moving the committed unit to layout (a) is the deferred binary-layout migration (5U.3). A
privileged helper is deliberately not provided: it would need a setuid binary or a polkit rule,
contradicting `NoNewPrivileges=yes`, for a convenience the service-owned layout gives for free.

**Release pipeline requirements** (cost surfaced: the reusable release workflow lives outside this
repo): publish per-platform archives named by the app's `AssetName`, and keep `checksums.txt`
(goreleaser produces it). Signing is deferred-build with signed manifests (5U.3).

## 5U.6 Error semantics

| Error path | Fail | Why |
|---|---|---|
| Source unreachable / non-200 / decode error | open: `Status.LastError` token, component Degraded `check_failed`, next tick retries | a failed check must not affect the app |
| Release has no matching asset | open for check (reported `no_asset`); closed for apply | — |
| Checksum mismatch against a published `checksums.txt`, oversize download, bad archive member | closed: abort, delete download, metric `apply_total{outcome="checksum_failed"}`, WARN | never run a truncated or corrupted binary |
| Self-test fails | closed: delete `<exe>.new`, abort | catches wrong arch, corrupt build, wrong app |
| `ReaderCheck(<exe>.new)` refuses (`level_newer`, `format_newer`, `upgrade_floor`, `sidecar_unreadable`) | closed: apply refused with the reason (`upgrade_floor` as `stepping_stone_required`), `<exe>.new` deleted, nothing swapped | the hotfix's reader check of the new binary (09 S0) |
| Backup fails | closed (unless `require_backup=false`) | recovery path must exist before a swap |
| Rename fails | closed: restore `<exe>` from `<exe>.prev` if the first rename happened; abort | atomic swap or nothing |
| Executable directory not writable | closed for apply/rollback (`binary_not_writable`); open for check (Degraded, banner) | OPS2-4: say so instead of failing at the swap |
| Store's recorded state does not meet the new binary's own non-empty `--print-upgrade-floor` (step 5), or `StoreState` fails against a non-empty floor (a missing sidecar: "boot any release ≥ S0 once"); an empty floor passes without reading the sidecar, and so does an empty store (no DB directory, or an empty one, and no sidecars) | closed: apply refused (`stepping_stone_required`), the download and `<exe>.new` deleted, nothing swapped | R2-4, OPS3-7, OPS6-1, OPS7-10, DATA8-11 |
| A swap staged (`restore/pending.json`: a restore, or a factory reset, `KindReset`) or in flight (a `restore/<id>/inflight.json`, which a `KindReset` swap keeps until `hook_done`), except a `KindCheckpoint` journal at `swapped` or `failed` whose restored store the candidate admits and whose journaled swap it implements (`--print-restore-journal`) | closed: apply and rollback refused (`restore_pending`, `restore_inflight`, the message naming the kind); no updater override (the scripts' one override, `ROLLBACK_IGNORE_RESTORE=1`, covers `restore_pending` only, OPS8-7) | 09 S1/S8; a swap would discard the restore (DATA3-6), reject the audited reset or leave a half-done set-aside to a binary that cannot finish it (R7-2, R8-1); the checkpoint exception lets the documented failed-migration recovery reach `.prev` (R7-4, DATA7-12, DATA8-6) |
| `ReaderCheck` cannot determine the answer (`cannot_determine`) | closed: apply and rollback refused | fail-closed, as `make rollback` (09 §9.2.2) |
| Manual rollback: `ReaderCheck(<exe>.prev)` refuses | closed: rollback refused with the reason; the running binary stays | a `.prev` that refuses the store would leave the unit failed (OPS-6) |
| Restart fails after swap | closed: the process keeps running the old code in memory; the next manual or supervisor restart boots the new binary | — |
| Current version is a local (describe-suffixed) build | open for check (`LocalBuild`, not compared); apply only of an explicitly named version | semver cannot order it against its base tag (OPS4-17) |
| Running in a container / dev build | closed: apply refused with reason | policy |

## 5U.7 Concurrency contract

All mutation happens inside the `core.updater.apply` and `core.updater.rollback` tasks, serialised by
`ConcurrencyKey: core.updater` (one apply or rollback at a time). `Status()` is a lock-free atomic read.
Downloads stream to disk (no whole-archive buffering) and honour `rc.Context()` until the swap phase.

## 5U.8 Config keys

`Protection` levels are 05-config's (`ProtectAdmin` = `core:admin` + an interactive method, SEC-6);
`—` means the default for `core.*` writes.

| Key | Type | Default | Hot reload | Protection |
|---|---|---|---|---|
| `core.updater.mode` | `off`/`check` | `off` (or the app's `DefaultMode`) | yes | `ProtectAdmin` |
| `core.updater.channel` | `stable`/`prerelease` | `stable` | yes | `ProtectAdmin` |
| `core.updater.max_download` | bytes | 512 MiB | yes | — |
| `core.updater.require_backup` | bool | true | yes | `ProtectAdmin` |
| `core.updater.restart_mode` | `exit`/`exec` | `exit` | yes | `ProtectAdmin` |
| `core.updater.schedules.check` | `tasks.ScheduleConfig` | `{enabled: true, every: 1h}` (current 60 min) | yes | — |

There is no `core.updater.check_interval`: the interval is the schedule key (SIMP-6). There is no
`core.updater.restart_exit_code` either: the restart exit code is 06's `core.lifecycle.restart_exit_code`
(default 75, `ProtectAdmin`), which `Lifecycle.RequestRestart` uses for the updater, the staged restore and
the staged factory reset alike (06 §6.5.2, OPS8-12). The old name never shipped (the updater lands at
S8), so it needs no alias. The keys of the deferred members (`allow_unsigned_legacy`,
`max_boot_attempts`, `confirm_after`, `schedules.apply`) are registered by the release that builds them.

ABO compatibility: `auto_update.enabled=true` maps to `mode=check` with `Status.AutoApplyPaused` (5U.5),
the S0 hotfix's behaviour; `auto_update.channel` maps to `channel` (`develop` → `prerelease` with a WARN);
`auto_update.check_minutes` maps to `core.updater.schedules.check.every`; `auto_update.window_start/end`
are read and ignored with one WARN, because only the deferred unattended apply uses a window. Aliases live
for the compatibility window ([09](09-abo-on-core.md) §9.2.1; [05-config](05-config.md) owns the alias
mechanism).

## 5U.9 Metrics

Names are shown under the default namespace `mediacore`; an app's `app.Options.MetricNamespace`
replaces it (06; 05-metrics owns the naming rule; A-5). HEAD's updater emits no series, so no ABO series
is renamed or aliased.

| Name | Type | Labels |
|---|---|---|
| `mediacore_updater_checks_total` | counter | `outcome` (`ok`, `failed`, `no_asset`) |
| `mediacore_updater_update_available` | gauge (0/1) | — |
| `mediacore_updater_apply_total` | counter | `outcome` (`applied`, `checksum_failed`, `selftest_failed`, `reader_refused`, `backup_failed`, `swap_failed`, `refused`, `stepping_stone_required`, `binary_not_writable`) |
| `mediacore_updater_rollbacks_total` | counter | `outcome` (`done`, `refused`) |
| `mediacore_updater_download_bytes_total` | counter | — |

## 5U.10 HTTP

| Route | Permission | Replaces |
|---|---|---|
| `GET /api/core/v1/updater` (Status) | `core:system.manage` | `GET /api/v1/update/status` |
| `POST /api/core/v1/updater/check` → starts `core.updater.check` | `core:system.manage` | `POST /api/v1/update/check` |
| `POST /api/core/v1/updater/apply` `{version}` → starts `core.updater.apply` | `core:admin`, credential guard | `POST /api/v1/update/apply` |
| `POST /api/core/v1/updater/rollback` → starts `core.updater.rollback` | `core:admin`, credential guard | new |

The `/api/v1/update/*` paths are aliases for the compatibility window (09 §9.2.1).

## 5U.11 Tests

| Test | Proves |
|---|---|
| `TestCheck_SemverNeverDowngrades`, `TestCheck_DevBuildNeverAvailable`, `TestCheck_DescribeSuffixedCurrentNotCompared` (current `v0.223.0-5-gabc1234` or `-dirty`, latest `v0.223.0` or older → no update offered, `LocalBuild` set, apply of a named version allowed, of an unnamed one refused) | comparison, OPS4-17 |
| `TestCheck_ScheduledRunNeverApplies` (mode `check`, a newer release: the scheduled run notifies and records `Status`; no download, no swap; `auto_update.enabled=true` → `AutoApplyPaused` and Degraded `auto_update_paused`) | check-only unattended mode (A-6) |
| `TestGithubSource_ParameterisedSlugAndBaseURL` (httptest) | release source parameterised, no hard-coded repo |
| `TestGoreleaserArchive_MatchesTemplate` (golden against `.goreleaser.yml` naming) | asset contract |
| `TestApply_ChecksumMismatchAborts`, `TestApply_NoChecksumsFileProceeds`, `TestApply_OversizeAborts` | integrity |
| `TestApply_ArchiveMemberTraversalRejected` | extraction safety |
| `TestApply_SelfTestFailsKeepsOldBinary` (fake binary in a temp dir) | self-test |
| `TestSelfTest_VersionJSONFieldList` (the new binary must print exactly `{app, version, commit, ui_build_id, binary_id}`; a missing `app` fails the self-test; `commit` may be `""`) | FE6-4 |
| `TestApply_ReaderRefusesNewBinary` (a fake `<exe>.new` reporting a format the store's sidecar refuses → refused with the reason, nothing swapped) | hotfix reader check |
| `TestApply_SteppingStoneRequired` (store at books 63 and a new binary whose floor is books `LegacyCap` → refused at step 5 naming the release it prints; store at `LegacyCap` → allowed), `TestApply_StoreStateErrorRefuses` | OPS2-11, OPS3-7, OPS6-1 |
| `TestApply_EmptyFloorSkipsSidecar` (no `.storage-applied`, new binary prints an empty floor → allowed; a non-empty floor with no sidecar → `stepping_stone_required` naming "boot any release ≥ S0 once"; a non-empty floor with no DB directory, or an empty one, and no sidecars → allowed, as boot exempts an empty store) | OPS7-10, DATA8-11 |
| `TestApply_BackupFailureAborts` | backup gate |
| `TestApply_SwapAtomicAndPrevKept` (slot name `.prev`), `TestApply_RestartExits75` (fake `Restarter`: exit mode uses `core.lifecycle.restart_exit_code`) | swap, restart |
| `TestRollback_ReaderRefusesPrev` (fake `.prev` reporting a lower level → refused, running binary kept), `TestRollback_SwapsPrevBack` | manual rollback (OPS-6) |
| `TestApply_BinaryDirNotWritableRefusedAndDegraded` (read-only temp directory) | OPS2-4 |
| `TestApply_RefusedWhileResetPending` (a staged `restore/pending.json` of kind `reset`: refused with `restore_pending`, the message naming the kind; then its journal at `set_aside`, and again at `applied` without `hook_done`, with its `restore/<id>/inflight.json`: refused with `restore_inflight`; apply and manual rollback refused, nothing downloaded or swapped; no `reset_*` token exists), `TestApply_CheckpointJournalAdmitsCandidate` (a `KindCheckpoint` journal at `swapped`, and again at `failed`: a candidate whose levels and floors admit the restored store and which answers `--print-restore-journal` passes `ReaderCheck`; one that does not admit the store, or a pre-S2b candidate without the flag, refuses with `restore_inflight`) | R7-2, R7-4, R8-1, OPS7-11, DATA7-12, DATA8-6 |
| `TestCheck_NotifiesOncePerVersionAcrossRestart` | COR2-3 |
| `TestPolicy_ContainerForcesOff`, `TestPolicy_NilOptionsNoRoutesNoTasks` | policy, absence |
| `TestConfig_ModeIsProtectAdmin` | SEC-6 |
| `TestApply_OnlyPathIsManualTask` (no other caller of `Apply`, and `core.updater.apply` declares no `Schedule`; AST) | single, manual apply path |
| `TestNoGlobals_Updater` (R5 AST) | no globals |

## 5U.12 Migration from ABO

| ABO today | Becomes |
|---|---|
| `internal/updater.Updater` (`updater.go`), as 09 S0 hotfixes it | `core/updater` + `core/updater/github`, **moved** (A-6): ABO's `main` passes its slug, binary name and asset namer |
| `internal/updater.Scheduler` + `SchedulerStarterAdapter` + `updatescheduler` service | deleted; `core.updater.check` task with its schedule key; no scheduled apply |
| maintenance-window step 1 (`scheduler_maintenance_window_op.go:73-92`) and setting `maintenance_window_update_completed` | deleted (the hotfix made it check-only at S0) |
| `RestartSelf` (`os.Exit(0)`, `updater.go:329-334`) | `Restarter.Restart` (graceful, exit 75). Until then, 09 S0 owns the HEAD updater hotfix (exit 75, `.prev` kept, semver comparison that treats a describe-suffixed current version as a local build and offers nothing (OPS4-17), a reader check of `<exe>.new` against the store before the swap (the same **decision** as the scripts, run in process: as the service user it reads the sidecars and stats `restore/pending.json`, `restore/*/inflight.json` and `restore/*/inflight-checkpoint.json` under the state directory directly, never through `sudo`, which cannot run under the unit's `NoNewPrivileges=yes` (`deploy/audiobook-organizer.service:122`), and from S2b it calls core `storage`'s `CheckReader` through the kernel binding, so a staged factory reset (`KindReset`) refuses it too (`restore_pending`, `restore_inflight`; `TestHotfixUpdater_RefusesWhileResetPending` in 09 S2b's done-check; OPS8-8, R8-1); a **floor check** — `<exe>.new --print-upgrade-floor`, a fast exit that ships at S0, compared with `<db>.storage-applied`, refusing with `stepping_stone_required` and naming the release the new binary prints, so the S5 floor release is never swapped onto a store below books' `LegacyCap` (OPS6-1) — and **both** auto-apply paths of 5U.2 check-only; OPS3-12). S8 moves exactly this behaviour into core |
| `<exe>.old`, deleted after the swap (`updater.go:267-280`) | `<exe>.prev`, kept; shared with `make rollback` (from the S0 hotfix) |
| `/api/v1/update/*` handlers (`update_handlers.go`) | core routes + aliases |
| `auto_update.*` config | `core.updater.*` and `core.updater.schedules.check` with aliases (5U.8) |
| release pipeline (reusable workflow, UNKNOWN details) | unchanged at S8; ABO's asset namer must match what the pipeline publishes — verify before an admin applies through the core updater (open item 3) |

**S8 done-check.** The move is checked against the hotfix it moves: 09 S0's updater tests, re-pointed
at `core/updater`, pass with unchanged expectations (exit 75, `.prev` kept, the semver and local-build
rule, the reader and floor refusals of the new binary, check-only scheduled runs), and 5U.11 is green.
The real-RC staging check is deferred-build with signed manifests (5U.3).

## 5U.13 Open items

1. (Moved to the deferred signed manifests: the signing scheme — plain ed25519 over the release
   manifest vs. minisign vs. Sigstore/cosign — is decided when that member is built.)
2. (Closed, OPS2-11/R2-4/OPS3-7/OPS6-1: upgrade-path constraints are the new binary's own
   `--print-upgrade-floor`, from the S0 hotfix on, checked against the store's recorded state, 5U.4
   step 5. A binary reached without the updater still refuses the store at boot with `ErrUpgradeFloor`.)
3. Confirm the asset naming the reusable release workflow actually publishes (UNKNOWN in this repo).
4. Windows support for the swap (a running `.exe` cannot be renamed over on some setups): not a
   current deployment target; `exec` mode is Unix-only.
5. (Closed: 06 `app.Options` has `Updater *updater.Options`.)
6. (Closed by 05-storage v1.4 §5S.3.7: the applied-steps sidecar `<db>.storage-applied` the kernel binds
   `StoreState` to, rewritten by every guard-aware boot, and the `--print-upgrade-floor` fast exit that
   prints a build's declared upgrade floors as an `UpgradeRequirement` (OPS3-7, DATA3-10).)

## Out-of-scope defects found

1. `internal/updater/scheduler.go:97` logs `slog.Info("Update available but outside update window (%02d:00-%02d:00, current hour %02d)")`
   with printf verbs and no arguments, so the window and hour are never shown.
2. If releases are produced by the in-repo goreleaser config, the current updater can never find an
   asset (`updater.go:319` expects a raw binary name; `.goreleaser.yml:42-49` publishes tar.gz
   archives), so auto-update would silently do nothing. UNKNOWN which pipeline publishes.

## Review history

Round 1 findings applied to this file (v1.1.0):

- COR-19 — layer stated as L4.
- OPS-6 — `Boot` and manual rollback run `ReaderCheck` (the single compat/format guard) first and record `rollback_blocked` instead of restoring a binary that refuses the store; one slot name `.prev` shared with `make rollback`; HEAD exit-0 restart flagged for 09.
- OPS-23 — legacy unsigned path behind `allow_unsigned_legacy` (set only by the `auto_update.enabled` migration, cleared after a signed apply); `no_trusted_keys` and `unsigned_updates` reported Degraded with a banner; S8 precondition.
- SEC-6 — `Protection` column; `ProtectAdmin` on mode, channel, legacy flag, backup requirement, restart settings and the apply schedule.
- SEC-27 — signed release manifest binds app, version, channel and asset digests; one key per app; compiled-in revoked key IDs; self-test kept as a second check.
- SIMP-5 — `core.update.found.v1` declared in the one topic catalog.
- SIMP-6 — check and apply use `ScheduleFromConfig`; `core.updater.check_interval` removed.
- SIMP-7 — aliases reference 09 §9.2.1.

Integration pass (cross-cluster requests, 2026-11-04):

- integration pass: R-B request (SIMP-18) — `ReaderCheck` documents `CheckReader`'s reason tokens (`level_newer`, `format_newer`, `restore_pending`, `sidecar_unreadable`) instead of invented ones.
- integration pass: Verified already present (R-D request): SEC-27 signed release manifest; `core.update.found.v1` declared in the one catalog.

Round 2:

- R2-7 — schedule keys `core.updater.schedules.check` / `core.updater.schedules.apply` (owner namespace).
- R2-8 / OPS2-3 — `Boot` is 06 §6.5.1 phase P−1 before P0a, flags/env only; P0 refusals count as failed boots; `ReaderCheckOptions.IgnoreRestoreStagedBy`.
- OPS2-2 — `BootResult.Pending` passed to storage; no `RaisesLevel` step while an update is unconfirmed (`RequiresSelfServed`).
- OPS2-4 — writable-executable-directory precondition, `binary_not_writable`, supported layouts; no privileged helper.
- OPS2-6 — rollback recorded in `history.jsonl`; scheduled apply skips rolled-back versions; `SkippedAfterRollback`; alert snippet.
- OPS2-11 / R2-4 — signed `min_upgrade_from`, `stepping_stone_required`, `Status.SteppingStone`; open item 2 closed.
- OPS2-18 — rollback execs in place; `max_boot_attempts ≤ StartLimitBurst − 2`, capped at 3 under systemd.
- COR2-3 — `core.update.found.v1` notification staged through `notify.Publish` with `c/updater/seen` in one batch.
- SEC2-23 — unsigned path only for versions ≤ compiled `LastUnsignedVersion`.
- OPS2-26 (consistency) — apply/rollback refused while a staged restore is pending.

Integration pass (round 2, 2026-11-04):

- integration pass (round 2): `updater.Boot` takes the DB path from its own marker (`db_path`); `ReaderCheck(ctx, dbPath, exe, opt)`; no data directory. `IgnoreRestoreStagedBy`: already applied.
- integration pass (round 2): Apply/rollback audited as `core.update.apply` / `core.update.rollback`.

Round 3:

- OPS3-7 — `min_upgrade_from` is an `UpgradeRequirement{Release, Owners, Steps}` copied from the binary's `--print-upgrade-floor`, and apply step 3 checks it against the store's recorded migration state (`Deps.StoreState`, reading 05-storage's applied-steps sidecar; 02 N14), not the running binary's version; error row, tests, open item 6.
- OPS3-12 — 5U.12 row: the 09 S0 HEAD-updater hotfix covers semver comparison, a reader check before the swap and both auto-apply paths.
- OPS3-15 — supported install layouts: 09 S8's owner checklist (unit, drop-in, sudoers, `DEPLOY_BIN`, `deploy-match-unit-type.sh`, binary **and** `.prev` copied, dry-run rollback).
- R3-1 — Teach-build and C3 examples removed; "no contract while unconfirmed" restated as "no level raise while unconfirmed" (books format changes, later cutovers; none in the extraction).
- R3-2 — verified: this file specifies no release-promotion gating; release-pipeline requirements are publication only.
- Open items 5 and 7 closed (06 has `Updater *updater.Options` and P−1).

Integration pass (round 3, 2026-11-04):

- integration pass (round 3): `ReaderCheck` lists all of `CheckReader`'s tokens (`upgrade_floor`, `restore_inflight`, `cannot_determine` added) with error rows; open item 6 closed (05-storage defines the applied-steps sidecar and `--print-upgrade-floor`).

Round 4:

- OPS4-4 — supported layout (a) is a service-owned 0755 binary directory outside the 0700 state directory; recipes keep unprivileged probes and copy with literal sudoers lines; "no root for the copy" deleted; S8 checklist gains the plist path and a dry-run `make rollback` as the deploy user.
- OPS4-5 — floor release vs stepping-stone release named distinctly (`UpgradeRequirement` comment, `MinUpgradeFrom`, `Status.SteppingStone`, policy row, pipeline note); "minimum-upgrade-from release" no longer used.
- OPS4-8 — `Boot` uses the state directory the kernel resolved once; the marker's `db_path` is only `ReaderCheck`'s DB path; test.
- OPS4-9 — `Boot(stateDir, exe, self, check)` ignores a marker of another build (`superseded` in `history.jsonl`); self-test's `build` recorded as `to_build`; error row; `TestBoot_IgnoresMarkerOfAnotherBuild`.
- OPS4-17 — describe-suffixed current versions are local builds: not compared, no scheduled apply, manual apply only of a named version; `Status.LocalBuild`; error row and test.
- DATA4-18 — the stepping-stone refusal is scoped to S0+ updaters; what a pre-S0 auto-updating install sees on the floor release is stated.
- SIMP4-4 — `UpgradeRequirement.Steps`, `StoreApplied.StepsDone` and the step half of `Meets` moved under "Deferred-build members"; manifest `min_upgrade_from` = `{release, owners}`.

Round 5:

- R5-7 / FE5-3 — `Self.Build` renamed `Self.BinaryID`, the binary ID (version + SHA-256 of the executable, computed by the kernel before P−1), never the UI bundle's build ID; used for the marker's `to_build`, the self-test (`binary_id` from `--version-json`), `IgnoreRestoreStagedBy`, and named as the identity storage and backup record; `TestSelf_BinaryIDDistinguishesAPIOnlyBuilds`.
- R5-5 / COR5-3 / DATA5-6 (reference) — "Failed boots keep the rollback evidence": boots `Boot` counts as failed do not erase the served ledger's previous record, which 05-storage replaces only after every reading step committed.
- SIMP5-6 / COR5-16 — "Deferred-build members" table: first column only package-qualified identifiers (`updater.UpgradeRequirement.Steps`, `updater.StoreApplied.StepsDone`); the built `Meets` and non-identifier items moved to an "Also deferred (not checked)" column.
- OPS5-15 (consequence) — while the marker names the running binary a boot refusal exits 75, not 78, so a `RestartPreventExitStatus=78` unit still lets `Boot` roll back; `TestBoot_FloorRefusalUnderPendingRestarts`.

Round 6:

- OPS6-1 — the S0 hotfix (5U.12) compares `<exe>.new --print-upgrade-floor` with `<db>.storage-applied` before the swap (no signed manifest needed, so it covers the S5 floor release); step 5 of this updater makes the same check beside step 3's signed copy; 5U.5 stepping-stones row, error row, open item 2; `TestApply_NewBinaryFloorUnmetRefused`.
- FE6-4 — step 5 pins `--version-json` to `{app, version, commit, ui_build_id, binary_id}` and defines `commit`; `TestSelfTest_VersionJSONFieldList`.
- FE6-3 — `Self`: the binary ID comes from one dependency-free S0 leaf that `app.BinaryID` delegates to, so S0–S2 values equal the kernel's.
- OPS6-5 — floor-refusal paragraph: launchd relaunches a 78 exit; `Boot` counts boots there regardless.
- SIMP6-8 — deferred-members intro names the `Also deferred (not checked)` column by heading, not position.
- integration pass (round 6): `Self` comment names `buildid.Binary` in the L0 leaf `core/buildid` (03 §3.3). Already applied: step 5's five-field `--version-json` list (FE6-4).

Round 7:

- R7-2 / DATA7-6 / OPS7-11 — `reset_pending` joins `ReaderCheck`'s tokens; apply step 1 and the error table refuse apply and rollback while a reset is staged or its journal is not `applied`, `Boot` records `rollback_blocked`; `ROLLBACK_IGNORE_RESET=1` is the scripts' override only; `TestApply_RefusedWhileResetPending`.
- R7-4 / DATA7-12 — a `KindCheckpoint` journal at `swapped` passes `ReaderCheck` for a candidate whose levels and floors admit the restored store; error row and `TestApply_CheckpointJournalAdmitsCandidate`.
- OPS7-10 — an empty `--print-upgrade-floor` passes without reading `.storage-applied`; a non-empty floor with a missing sidecar refuses ("boot any release ≥ S0 once"); `StoreState` doc, steps 3 and 5, error row, `TestApply_EmptyFloorSkipsSidecar`.
- FE7-13 — `Self` comment: the UI build ID is the value `X-App-Build` reports (`dev` for every API-only build), distinct from `/modules`' shell-only `ui_build_id`.
- R7-6 / OPS7-3 — new 5U.12 paragraph: the S8 staging check runs against a local fake signed-release server (`provision.sh --updater-check`), egress allowed to that address only; passes when the release applies and `Boot` confirms it.
- integration pass (round 7): `ReaderCheck` tokens, apply step 1, error row and `TestApply_RefusedWhileResetPending`: `reset_pending` (`reset/pending.json`) and `reset_inflight` (`reset/<id>/inflight.json` until `hook_done`) as 05-storage and 09 define them; the `KindCheckpoint` file `restore/<id>/inflight-checkpoint.json` (R-B, R-H request).

Round 8:

- R8-1 / SIMP8-1 / COR8-4 / OPS8-5 — one token pair: `reset_pending`, `reset_inflight` and `ROLLBACK_IGNORE_RESET` deleted; a factory reset is the `KindReset` swap of the one slot `restore/pending.json` and journal, so `ReaderCheck`, apply step 1, the error table and `TestApply_RefusedWhileResetPending` use `restore_pending`/`restore_inflight` (the message naming the kind); `Boot` and `IgnoreRestoreStagedBy` treat a reset staged by the failing build as a restore, rejected by `.prev` under the one rejection rule.
- OPS8-7 — the scripts' one override `ROLLBACK_IGNORE_RESTORE=1` covers `restore_pending` only, never `restore_inflight`; the updater has none.
- R8-4 / SEC8-6 / SEC8-9 — `ReaderCheck` returns advisory tokens (`canonical_only_operator_values`, `config_narrowing_owed`, from `operator-names.json`); apply step 8, `Boot`'s rollback and `Rollback` log them before the swap; error row; `TestApply_AdvisoryTokensLoggedBeforeSwap`.
- DATA8-6 — the `KindCheckpoint` exception covers a journal at `swapped` or `failed` and only a candidate that implements the journaled swap (`--print-restore-journal`); test extended.
- DATA8-11 — `StoreState` reports an empty store (`StoreApplied.Empty`, no DB directory or an empty one and no sidecars), which meets every floor, as boot exempts it; steps 3, error row, test.
- OPS8-3 / SIMP8-10 — the S8 staging check runs between two real signed prereleases (candidate *n* applies *n+1*) with egress opened to the release hosts only; the fake server and test-key build are deleted; `prerelease.yml` publishes the signed manifest.
- OPS8-8 — 5U.12: the S0–S7 hotfix updater runs the decision in process as the service user, stats `restore/` directly (no `sudo` under `NoNewPrivileges=yes`) and calls `CheckReader` through the kernel binding from S2b.
- OPS8-9 — the marker code is listed in `ci/state_writers.txt`.
- OPS8-12 — `Restarter` is bound to `Lifecycle.RequestRestart` (06 §6.5.2); `core.updater.restart_exit_code` replaced by 06's `core.lifecycle.restart_exit_code` (never shipped, no alias); marker comment and step 9.
- integration pass (round 8): 5U.12's S8 done-check paragraph states that it uses shipped artefacts only (real signed RC *n* → RC *n+1*), with no text left describing a substitute server or key (SIMP8-10, sweep (c)).

Advisor (2026-11-04):

- A-6 / ADV-S5 — S8 moves the S0-hotfixed updater into `core/updater`, parameterised by release source (`Options.Source`): exit 75, `.prev`, semver with the local-build rule, the reader and floor checks of `<exe>.new` (step 5) and check-only unattended mode (`ModeApply` gone from the built API; `Status.AutoApplyPaused`, Degraded `auto_update_paused`); apply and rollback are manual admin tasks.
- A-6 / ADV-S5 — signed manifests (`ReleaseManifest`, keys, legacy unsigned path, `allow_unsigned_legacy`, signed `min_upgrade_from`), the boot-attempt counter (`Boot`, `Self`, `BootResult`, `Pending`, marker, `history.jsonl`, `max_boot_attempts`, `confirm_after`, `IgnoreRestoreStagedBy`, OPS5-15 exit code, `RequiresSelfServed` input), automatic rollback (with `ModeApply`, `schedules.apply`, `SkippedAfterRollback`, `rollback_blocked`, the alert), the binary-layout migration and the real-RC `--updater-check` moved to the "Deferred-build members" table as non-normative sketches; their tests, error rows, metric outcomes and config keys removed from the built sections.
- A-2 / ADV-S1 — `ReaderCheck` returns no advisory tokens: `canonical_only_operator_values`, `config_narrowing_owed` and `operator-names.json` deleted from the signature, apply step, error table and tests.
- A-4 / ADV-S3 — the deferred table is no longer read by a generator (`archtest.DeferredMembersAbsent` deleted); it is a plain informative table.
- A-5 / ADV-S4 — 5U.9: names shown under the default namespace, replaced by `app.Options.MetricNamespace`; no ABO series renamed.
- A-8 / ADV-4 — 5U.4 "Boot writers": the built updater writes nothing during boot; the deferred `Boot` extends the crash matrix when built.
- A-13 / ADV-S7 — the `ci/state_writers.txt` listing of the marker code deleted (with the marker, deferred).
- A-15 / ADV-S9 — deferred members labelled design sketches (non-normative), frozen by no SDK.
- ADV-11 — no literal count remains in this file's done-check; the S8 done-check is the S0 hotfix's own tests re-pointed at `core/updater`.
