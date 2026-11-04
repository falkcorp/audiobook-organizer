<!-- file: docs/specs/platform-core/05-updater.md -->
<!-- version: 1.13.0 -->
<!-- guid: 87f3821c-d9b3-427d-b91f-dad6de16aef8 -->
<!-- last-edited: 2026-11-04 -->

# 05 — Updater (`core/updater`)

Evidence commit `748fd787a`. Raw inputs: [`raw/12-cross-cutting.md`](../../plans/platform-core/raw/12-cross-cutting.md) §6,
[`raw/14-build-ci.md`](../../plans/platform-core/raw/14-build-ci.md) (release pipeline),
[`raw/07-plugin-registry-sdk.md`](../../plans/platform-core/raw/07-plugin-registry-sdk.md) §1.3 rows 23–24.
Layer **L4** (03 R4 v1.2.0); imports `tasks` (check/apply/rollback are tasks), `health`, `events`,
`config`, `storage`, `integrations/providerhttp` (outbound HTTP) and `integrations/notify` (the
update-found notification) — all L4 or lower; none of them imports `updater`, so no cycle. It imports no
`backup`: the built updater takes no pre-update backup, as HEAD's does not (5U.4; R10-8).

## 5U.1 Purpose & scope

Self-update of an app binary installed on a host (a container image is updated by its orchestrator): discover a newer release from the
**app's** release source, swap the binary atomically while keeping the previous one as `<exe>.prev`,
and restart gracefully. Each app decides whether it ships an updater at all; when it does, the updater
is **off by default**.

**R2 moves the updater; it does not rebuild it (A-6, ADV-S5).** The core updater is the HEAD updater
as R0 hotfixes it ([09](09-abo-on-core.md)), moved into `core/updater` and parameterised by release source. It
keeps HEAD's pipeline — download the raw per-platform binary asset straight into `<exe>.new` and swap it in
(`internal/updater/updater.go:214-265,318-326`) — and the hotfix's behaviour: restart with exit 75, the `.prev` slot
shared with `make rollback`, semver comparison with the local-build rule, and **check-only unattended mode** — a
scheduled run only checks and notifies; a swap happens only when an admin applies a named release, and a rollback
only when an admin (or `make rollback`) asks for it. The updater makes no level, format or upgrade-floor check of
a new binary before the swap: a binary that cannot serve the store refuses it at boot, before any write (exit 78,
last line `install <release> first`; 05-storage). Signed release manifests, the boot-attempt counter, automatic
rollback, the committed unit's binary-layout migration, the real-RC check, the archive download with checksum
verification, the pre-update backup, the container refusal and the exec-mode restart are **deferred-build** (5U.3
"Deferred-build members"; R10-8): HEAD's updater has none of them, the hotfix adds none of them, and nothing in R2
needs them, so R2 changes no updater behaviour that HEAD and the hotfix define.

Out of scope: container image updates (the orchestrator's job; as at HEAD, the built updater does not
detect a container, and the refusal is deferred-build, 5U.3), database rollback (the pre-migration checkpoint
or an archive, restored offline with the previous binary, 05-backup; not the updater), release-pipeline implementation (the spec states what the pipeline must publish).

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
	BinaryName  string // e.g. "audiobook-organizer": the raw asset's prefix and the user agent's product, as at HEAD
	Source      Source // the release source: the one parameter A-6 adds to the HEAD updater
	AssetName   func(r Release, goos, goarch string) string // default: RawBinary(BinaryName)
	DefaultMode Mode   // the app's default (ModeOff unless the app says otherwise)
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

// RawBinary returns HEAD's asset namer: the raw executable
// "<binary>-<goos>-<goarch>" (internal/updater/updater.go:318-326), which the
// updater writes straight to <exe>.new; no archive is opened (R10-8). The
// archive namer for goreleaser's tar.gz template is deferred-build (5U.3).
func RawBinary(binary string) func(Release, string, string) string

type Status struct {
	Mode           Mode
	Channel        Channel
	Current        string
	Latest         string
	Available      bool   // semver(Latest) > semver(Current); never a downgrade
	LastChecked    time.Time
	LastError      string // fixed token
	PreviousKept   string // version of <exe>.prev, if any (the manual rollback target)
	DisabledReason string // "off", "dev_build", "binary_not_writable" (OPS2-4); "container" is deferred-build (5U.3)
	// LocalBuild: the current version carries a `git describe` suffix (OPS4-17);
	// Latest is not compared with it.
	LocalBuild bool
	// AutoApplyPaused: the operator had asked for unattended apply (ABO's
	// auto_update.enabled=true, 5U.8), which this updater does not do (A-6).
	AutoApplyPaused bool
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
	Restarter   Restarter            // kernel: Lifecycle.RequestRestart (06 §6.5.2), graceful shutdown then restart
	Health      *health.Registry
	Events      *events.Bus
	Notify      *notify.Service      // stages core.update.found.v1 notifications (outbox-backed, COR2-3)
	Store       storage.Keyspace     // c/updater/ (one key, 5U.4 Check)
	ReaderCheck ReaderCheck          // kernel-bound to 05-storage's guard (levels and the swap slot)
	Log         *slog.Logger
}

// ReaderCheck asks whether the binary at exe may open the store at dbPath as
// it is now: the decision `make rollback` makes, implemented once by
// 05-storage's guard over the level sidecar and the restore slot, so it
// needs no open store. The kernel binds it to core/internal/pebble.CheckReader
// (05-storage §5S.3.7) with the resolved state directory; dbPath is
// core.storage.path. nil = allowed; otherwise an error carrying one of its
// fixed reason tokens: "level_newer" (exe supports a lower level than the
// store holds: a pre-R2 binary on a migrated store), "format_newer",
// "restore_pending" (restore/pending.json exists: a staged swap of any kind,
// a restore or a factory reset, KindReset; R8-1, SIMP8-1), "restore_inflight"
// (a swap journal restore/<id>/ holds inflight.json, which every kind creates
// BEFORE its first rename and keeps until its terminal step; or, for a
// checkpoint restore, inflight-checkpoint.json; DATA3-6, R7-2, R9-7),
// "sidecar_unreadable", "cannot_determine" (a probe could not answer;
// refuses). The error message names the swap's kind. One journal passes: a
// KindCheckpoint journal at swapped or failed (the offline
// restore-checkpoint path) does not refuse a candidate exe whose levels admit
// the restored store and which implements the journaled swap (it answers
// --print-restore-journal, R1 and later; R7-4, DATA7-12, DATA8-6). There is
// no override. Apply consults it on <exe> for the swap slot only; Rollback
// consults it on <exe>.prev.
type ReaderCheck func(ctx context.Context, dbPath, exe string) error

type Restarter interface {
	// Restart runs the full shutdown sequence within the stop budget
	// (05-health 5H.4) and then exits with core.lifecycle.restart_exit_code
	// (default 75, the R0 hotfix's code). The kernel binds it to
	// Lifecycle.RequestRestart(reason) (06 §6.5.2), the one restart path the
	// staged restore and the staged factory reset also call (OPS8-12). The
	// updater never asks for an exec restart: exec mode is deferred-build
	// (5U.3; R10-8).
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

Design sketches (non-normative, A-15): no extraction release builds them, and the SDK freezes none of
them. Each lands in its own later release, with its own tests, as an additive change to the built
updater above. The round-8 design of each is preserved in git history at the commit before A-6, and
each is re-reviewed when it is built, against the code as it is then.

| Member | What it adds | Built when, and why not in R2 |
|---|---|---|
| **Signed release manifests** | `ReleaseManifest{app, version, channel, assets, min_upgrade_from}`, `TrustedKey`, `Options.Manifest`/`Signature`/`TrustedKeys`/`RevokedKeyIDs`, ed25519 verification over the manifest bytes (SEC-27), the signed `min_upgrade_from` copy of the binary's floor, `Options.LastUnsignedVersion` with the legacy unsigned path and `core.updater.allow_unsigned_legacy` (OPS-23, SEC2-23), the `no_trusted_keys`/`unsigned_updates` reasons, the pipeline's signing step and key rotation | when the reusable release workflow can sign (its asset contract is UNKNOWN from this repo, 5U.2); the R2 updater's trust level is HEAD's: HTTPS from the configured release source |
| **Boot-attempt counter** | `Boot(stateDir, exe, self, check)` as 06 §6.5.1 phase P−1, `Self{Version, BinaryID}`, `BootResult`, `Pending`, the pending marker `updates/pending.json`, `updates/history.jsonl` (`superseded`, `rolled_back`), `core.updater.max_boot_attempts`, `core.updater.confirm_after`, `ReaderCheckOptions.IgnoreRestoreStagedBy` (OPS2-3, OPS4-8, OPS4-9, R5-7), the restart exit code a boot refusal uses while a marker names the running binary (OPS5-15), and `BootResult.Pending` handed to storage open for `RequiresSelfServed` (OPS2-2) | with automatic rollback, which is its only consumer; its marker write, history append and renames are boot writers, so its PR extends the crash matrix (05-storage, A-8). Its tests land with it, among them `TestUpdaterRollbackRejectsFailingBuildsIntent` (moved here from 05-backup, COR9-7: P−1 rolls back with a pending intent the failing build staged, and `.prev` rejects it at 05-backup §5B.3.4 step a); no R0–R3 build has P−1, so no extraction release can run it |
| **Automatic rollback** | `Boot`'s rollback to `<exe>.prev` after the attempt budget, in-place exec (OPS2-18), `rollback_blocked` when `ReaderCheck` refuses `.prev` (OPS-6), `Status.SkippedAfterRollback` (OPS2-6), the rollback alert rule, and **unattended apply**: `ModeApply`, `core.updater.schedules.apply` (window member 0) | with the counter: an unattended swap without an automatic way back is what the R0 hotfix stopped (09) |
| **Binary-layout migration** | the committed unit moved to layout (a) of 5U.5 (unit default, drop-in `ExecStart`, the sudoers template's `cp` lines, `DEPLOY_BIN`, `deploy-match-unit-type.sh`, the launchd plist's binary path, binary **and** `.prev` copied, a dry-run `make rollback` as the deploy user; OPS3-15, OPS4-4) | when unattended apply is built; until then the committed layout reports `binary_not_writable`, as HEAD's updater already fails there (5U.5) |
| **Real-RC `--updater-check`** | the staging done-check between two real signed prereleases (RC *n* applies RC *n+1*), `scripts/staging/provision.sh --updater-check` opening egress to the release hosts (R7-6, OPS8-3, SIMP8-10) | with signed manifests, which it exercises |
| **Archive download and checksums** | `GoreleaserArchive(project)` (goreleaser's `<project>_<version>_<Title(os)>_<arch>.tar.gz`, amd64 as x86_64, `.goreleaser.yml:42-49`) as the asset namer; download of the archive and `checksums.txt` into an `os.Root` on `<state-dir>/updates/<version>/` (`Deps.StateDir`); the archive's SHA-256 checked against its `checksums.txt` line (`checksum_failed`); extraction of only the `BinaryName` member (regular file, no path components, size-capped); `TestGoreleaserArchive_MatchesTemplate`, `TestApply_ChecksumMismatchAborts`, `TestApply_NoChecksumsFileProceeds`, `TestApply_ArchiveMemberTraversalRejected` | when the reusable release workflow is confirmed to publish archives and not HEAD's raw assets (open item 3); HEAD's updater downloads the raw `audiobook-organizer-<GOOS>-<GOARCH>` asset with no checksum (`updater.go:214-265,318-326`), so an archive step in R2 would fail `TestABOUpdaterSourceMatchesHEAD` or R0's updater test (R10-8, OPS10-6) |
| **Pre-update backup** | `Deps.Backup backup.Runner` (05-backup), a full archive before the swap, `core.updater.require_backup` (`ProtectAdmin`), the `backup_failed` outcome and error row, `TestApply_BackupFailureAborts` | by owner decision, with its own release entry and release note: on the reference host a manual apply would first write a full archive (about 15 GB) and could let retention prune older ones, behaviour HEAD's updater never had (R10-8, OPS10-6) |
| **Container refusal** | `DisabledReason: "container"` and `ModeOff` forced from `sysinfo.Info.Container` ([05-health](05-health.md)), apply refused in a container, `TestPolicy_ContainerForcesOff` | with a 09 "User-visible changes" line and a release note ("in a container, update checks report `container` and apply is refused"); HEAD's updater runs wherever it is enabled (R10-8, OPS10-6) |
| **Exec-mode restart** | `core.updater.restart_mode` (`exit`/`exec`, `ProtectAdmin`), exec of `<exe>` in place after the drain (Unix only) | with automatic rollback, whose in-place exec (OPS2-18) is its first user; HEAD restarts by exiting, and the hotfix by exiting 75 (R10-8, OPS10-6) |

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
A check never applies anything: that is the hotfix's check-only unattended mode (R0, 09), kept (A-6).

**Apply** — task `core.updater.apply`: Shape Task, `Effects: Writes(tasks.Res("core","binary"))`,
`Resume: ResumeDrop`, `ConcurrencyKey: core.updater`, `Cancel: Cooperative(30s)` until step 4,
`Perms.Start: core:admin`, `Priority: PrioritySystem`, **no `Schedule`**: it runs only when an admin
starts it through `POST /api/core/v1/updater/apply` `{version}` (5U.10), behind the credential guard as
at HEAD. The maintenance-window step 1 and the separate ticker are deleted. The steps are **HEAD's
pipeline as R0 hotfixes it** (R10-8, OPS10-6): HEAD's raw-asset download and rename swap
(`internal/updater/updater.go:214-280`), with the hotfix's semver rule, `.prev` slot and exit 75. There is no
archive, no checksum file, no pre-update backup, no container step and no store check of the new binary, because
neither HEAD nor the hotfix has one; the first four are deferred-build members (5U.3), as is the exec-mode restart.
The moved pipeline makes **exactly two additions** to HEAD's (lead decision, 09): the `core.updater.max_download`
cap on the download (step 2) and the `--version-json` self-test of the new binary (step 3); everything else is as
HEAD, with the hotfix's rules. Steps, each a `rc.Phase`:

1. Preconditions: current build is versioned; target newer than current, except
   that on a local build (`LocalBuild`) apply runs only for the version the operator names explicitly;
   no swap staged and none in flight, of either kind (`restore_pending`, `restore_inflight`;
   05-backup §5B.3.4's one slot and journal, in which a factory reset is the kind `KindReset`, 05-storage
   §5S.3.15; R8-1): a swap would make a staged restore be rejected (its `staged_by_build` is another binary,
   05-backup §5B.3.4 step a), turn the admin's audited reset into a rejected intent, or leave a half-done
   set-aside to a binary that cannot finish it (R7-2, OPS7-11). It is read through
   `ReaderCheck(core.storage.path, <exe>)` with no override (a `KindCheckpoint` journal at `swapped` or `failed`
   passes for a candidate that admits the restored store and implements the journaled swap, R7-4, DATA8-6). And
   the **executable's directory is writable by the process**, checked by creating and removing a probe file
   through an `os.Root` opened on that directory (OPS2-4). An unwritable directory sets
   `DisabledReason: "binary_not_writable"`, refuses apply and rollback, and keeps component `core.updater`
   Degraded `binary_not_writable` while mode ≥ check, so the operator learns that updates cannot be installed
   instead of the update silently failing at step 2 (5U.5 lists the supported layouts).
2. Download HEAD's raw asset: the release asset `AssetName` names (ABO: the default `RawBinary`,
   `audiobook-organizer-<GOOS>-<GOARCH>`, HEAD's name, `updater.go:318-326`; a release without it is
   `no_asset`), fetched through `providerhttp` with user agent `<BinaryName>/<version>` (ABO: HEAD's
   `audiobook-organizer/<ver>`, `updater.go:239`) and streamed straight into `<exe>.new`, mode 0755, in
   the executable's directory, so the rename is atomic (HEAD's `updater.go:254-265`). The body is capped
   by `core.updater.max_download` (the one addition to HEAD's download, which has no cap: an oversize or
   failed transfer deletes `<exe>.new` and aborts, `download_failed`); file and directory are fsynced.
   The trust level is HEAD's: HTTPS from the configured release source; checksums and signed manifests
   are deferred-build (5U.3).
3. Self-test. Run `<exe>.new --version-json` (10 s timeout, empty environment except `PATH`); it prints one JSON
   object with exactly the fields `{app, version, commit, ui_build_id, binary_id}`, the list R0 pins (FE6-4; 09),
   and the self-test requires `app == AppID` and `version == target` (`TestSelfTest_VersionJSONFieldList` pins the
   list). A failed self-test deletes `<exe>.new` and swaps nothing. There is no level, format or floor check of the
   new binary (5U.1): an upgrade whose store the new binary cannot serve is refused by that binary at boot, before
   any write (exit 78; 05-storage), and the unit's `RestartPreventExitStatus=78` stops a restart loop.
4. Swap: rename `<exe>` → `<exe>.prev`, `<exe>.new` → `<exe>`; fsync directory. HEAD renames to
   `<exe>.old` and deletes it (`updater.go:267-280`); the hotfix keeps it as `.prev`, the same slot
   `make rollback` uses (`Makefile:892-913`), so mixed use of the two mechanisms never diverges
   (OPS-6). A failed second rename restores `<exe>` from `.prev`, as HEAD does. From here the task is
   uninterruptible (two renames).
5. `Restarter.Restart`, bound to the kernel's `Lifecycle.RequestRestart` (06 §6.5.2), the one restart
   path that a staged restore and a staged factory reset also use (OPS8-12) — graceful shutdown within the
   stop budget, then exit with `core.lifecycle.restart_exit_code` (default 75, the hotfix's code, so the
   committed unit's `Restart=on-failure`, `deploy/audiobook-organizer.service:111`, restarts it; `0`
   needs `Restart=always`). HEAD exits 0 (`updater.go:329-334`).

**After the restart.** No marker is written and no boot is counted: the new binary boots like any
deployed binary, and a store refusal at boot (exit 78) needs the operator. A binary whose release carries
migrations takes the mandatory pre-migration checkpoint at its first boot (05-storage), so an update applied
through this updater, which takes no backup of its own, still has the checkpoint to fall back to. If the release
misbehaves, the admin rolls back by hand: task `core.updater.rollback` (`POST .../rollback`, `core:admin`,
credential guard, `ConcurrencyKey: core.updater`) runs `ReaderCheck(core.storage.path, <exe>.prev)` first and
refuses with its reason (a `.prev` older than a level the store reached cannot serve it: from R2 on, the R1
`.prev` refuses every migrated store, and the message names `make rollback-restore`, 05-backup), and otherwise
renames `<exe>` → `<exe>.rolled-back` (the name `make rollback` gives the binary it replaces, `Makefile:912`),
`<exe>.prev` → `<exe>`, fsyncs and restarts through `Restarter`; `make rollback` makes the same decision from the
operator's machine. `<exe>.prev` is kept until the next apply or deploy replaces it.

**Boot writers (A-8).** The built updater writes nothing during boot: its only durable key is
`c/updater/seen`, written inside the check task's run, and its files (`.new`, `.prev`,
`.rolled-back`) are written inside the apply and rollback runs. So it adds no entry to 05-storage's crash
matrix. The deferred `Boot` is a boot writer and extends the matrix in the PR that builds it.

Database caveat: a rollback restores the **binary**, not the data. A binary swap is a rollback only while the
previous binary still reads the store, which `ReaderCheck` decides: within R0 and R1, and from R3 back to R2. Across
the R2 cutover the way back is `make rollback-restore`: stop, restore the pre-migration checkpoint or an archive
offline (with the keyring and state files), reinstall `.prev`, start (05-backup). Everything written after the
upgrade is then lost, so nothing restores automatically. The built updater takes no backup of its own, as HEAD's
does not (the pre-update backup is deferred-build, 5U.3). Persistence: one key, `c/updater/seen` (the last notified
version); every updater file is beside the executable.

## 5U.5 Policy

| Rule | Default | Rationale |
|---|---|---|
| App ships an updater | per app (`app.Options.Updater` nil or not) | a TV/Movies app distributed only as a container image needs none |
| Mode | `ModeOff` for every app unless its `DefaultMode` says otherwise **and** the operator sets `core.updater.mode` | no unsolicited network calls; current default disabled (`config.go:2991`) |
| Unattended apply | never: a scheduled run only checks and notifies; an operator who asked for automatic apply (`auto_update.enabled=true`) gets `mode=check`, `Status.AutoApplyPaused`, component Degraded `auto_update_paused` and a Settings banner | the R0 hotfix's check-only rule, kept (A-6): the updater has no boot-attempt counter, so an unattended swap would have no automatic way back |
| Containers | not detected, as at HEAD: the updater behaves as on a host (off unless the operator enables it). The refusal (`DisabledReason: container`) is deferred-build (5U.3) and lands with its own release note | replacing a binary inside an image is wrong, but refusing it is a behaviour change HEAD never made, so it is not part of the R2 move (R10-8) |
| Downgrades | never offered; only `Rollback` to the kept previous binary, and only when `ReaderCheck` allows it; a describe-suffixed local build is never compared, so it is never offered an older release as newer | string-inequality defect (`updater.go:153`); OPS-6; semver orders `v0.223.0-5-gabc1234` below `v0.223.0` (OPS4-17) |
| Who | check: `core:system.manage`; apply/rollback: `core:admin` (audited as `core.update.apply` / `core.update.rollback`, 05-rbac §7.1; credential-guarded route as today) | RBAC (05-rbac); SEC-6 |
| Binary directory | the process must be able to create files in the executable's directory; otherwise `binary_not_writable` (apply and rollback refused, Degraded while mode ≥ check) | the swap writes `<exe>.new` and renames twice in that directory (steps 2 and 4); OPS2-4 |

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
repo): publish the per-platform raw binaries the app's `AssetName` names (ABO: HEAD's
`audiobook-organizer-<GOOS>-<GOARCH>`, `updater.go:318-326`). Whether the workflow does is open item 3;
archives, `checksums.txt` and signing are read only by deferred-build members (5U.3).

## 5U.6 Error semantics

| Error path | Fail | Why |
|---|---|---|
| Source unreachable / non-200 / decode error | open: `Status.LastError` token, component Degraded `check_failed`, next tick retries | a failed check must not affect the app |
| Release has no matching asset | open for check (reported `no_asset`); closed for apply | — |
| Download fails, or the body exceeds `core.updater.max_download` | closed: abort, delete `<exe>.new`, metric `apply_total{outcome="download_failed"}`, WARN | never run a truncated binary; HEAD also deletes `.new` on a failed write (`updater.go:260-264`) |
| Self-test fails | closed: delete `<exe>.new`, abort | catches wrong arch, corrupt build, wrong app |
| Rename fails | closed: restore `<exe>` from `<exe>.prev` if the first rename happened; abort | atomic swap or nothing |
| Executable directory not writable | closed for apply/rollback (`binary_not_writable`); open for check (Degraded, banner) | OPS2-4: say so instead of failing at the swap |
| A swap staged (`restore/pending.json`: a restore, or a factory reset, `KindReset`) or in flight (a `restore/<id>/inflight.json`, which every kind creates before its first rename and keeps until its terminal step; R9-7), except a `KindCheckpoint` journal at `swapped` or `failed` whose restored store the candidate admits and whose journaled swap it implements (`--print-restore-journal`) | closed: apply and rollback refused (`restore_pending`, `restore_inflight`, the message naming the kind); no override | a swap would discard the restore (DATA3-6), reject the audited reset or leave a half-done set-aside to a binary that cannot finish it (R7-2, R8-1); the checkpoint exception lets the documented failed-migration recovery reach `.prev` (R7-4, DATA7-12, DATA8-6) |
| `ReaderCheck` cannot determine the answer (`cannot_determine`) | closed: apply and rollback refused | fail-closed, as `make rollback` (09) |
| Manual rollback: `ReaderCheck(<exe>.prev)` refuses | closed: rollback refused with the reason, naming `make rollback-restore` for `level_newer`; the running binary stays | a `.prev` that refuses the store would leave the unit failed (OPS-6); across R2 only a restore goes back (05-backup) |
| Restart fails after swap | closed: the process keeps running the old code in memory; the next manual or supervisor restart boots the new binary | — |
| Current version is a local (describe-suffixed) build | open for check (`LocalBuild`, not compared); apply only of an explicitly named version | semver cannot order it against its base tag (OPS4-17) |
| Dev build | closed: apply refused with reason | policy |

## 5U.7 Concurrency contract

All mutation happens inside the `core.updater.apply` and `core.updater.rollback` tasks, serialised by
`ConcurrencyKey: core.updater` (one apply or rollback at a time). `Status()` is a lock-free atomic read.
The download streams into `<exe>.new` (no whole-binary buffering) and honours `rc.Context()` until the
swap phase.

## 5U.8 Config keys

`Protection` levels are 05-config's (`ProtectAdmin` = `core:admin` + an interactive method, SEC-6);
`—` means the default for `core.*` writes.

| Key | Type | Default | Hot reload | Protection |
|---|---|---|---|---|
| `core.updater.mode` | `off`/`check` | `off` (or the app's `DefaultMode`) | yes | `ProtectAdmin` |
| `core.updater.channel` | `stable`/`prerelease` | `stable` | yes | `ProtectAdmin` |
| `core.updater.max_download` | bytes | 512 MiB | yes | — |
| `core.updater.schedules.check` | `tasks.ScheduleConfig` | `{enabled: true, every: 1h}` (current 60 min) | yes | — |

There is no `core.updater.check_interval`: the interval is the schedule key (SIMP-6). There is no
`core.updater.restart_exit_code` either: the restart exit code is 06's `core.lifecycle.restart_exit_code`
(default 75, `ProtectAdmin`), which `Lifecycle.RequestRestart` uses for the updater, the staged restore and
the staged factory reset alike (06 §6.5.2, OPS8-12). The keys of the deferred members (`allow_unsigned_legacy`,
`max_boot_attempts`, `confirm_after`, `schedules.apply`, `require_backup`, `restart_mode`) are registered
by the release that builds them (R10-8).

ABO's keys are renamed once, by the R2 config cutover through 05-config's rename table, and no alias remains:
`auto_update.enabled=true` becomes `mode=check` with `Status.AutoApplyPaused` (5U.5), the R0 hotfix's behaviour;
`auto_update.channel` becomes `channel` (`develop` → `prerelease`, reported); `auto_update.check_minutes` becomes
`core.updater.schedules.check.every`; `auto_update.window_start/end` are dropped and reported, because only the
deferred unattended apply uses a window.

## 5U.9 Metrics

Names are shown under the default namespace `mediacore`; an app's `app.Options.MetricNamespace`
replaces it (06; 05-metrics owns the naming rule). HEAD's updater emits no series.

| Name | Type | Labels |
|---|---|---|
| `mediacore_updater_checks_total` | counter | `outcome` (`ok`, `failed`, `no_asset`) |
| `mediacore_updater_update_available` | gauge (0/1) | — |
| `mediacore_updater_apply_total` | counter | `outcome` (`applied`, `download_failed`, `selftest_failed`, `swap_failed`, `refused`, `binary_not_writable`) |
| `mediacore_updater_rollbacks_total` | counter | `outcome` (`done`, `refused`) |
| `mediacore_updater_download_bytes_total` | counter | — |

## 5U.10 HTTP

| Route | Permission | Replaces |
|---|---|---|
| `GET /api/core/v1/updater` (Status) | `core:system.manage` | `GET /api/v1/update/status` |
| `POST /api/core/v1/updater/check` → starts `core.updater.check` | `core:system.manage` | `POST /api/v1/update/check` |
| `POST /api/core/v1/updater/apply` `{version}` → starts `core.updater.apply` | `core:admin`, credential guard | `POST /api/v1/update/apply` |
| `POST /api/core/v1/updater/rollback` → starts `core.updater.rollback` | `core:admin`, credential guard | new |

The `/api/v1/update/*` paths are deleted in R2; the SPA switches to these routes in the same release (09).

## 5U.11 Tests

| Test | Proves |
|---|---|
| `TestCheck_SemverNeverDowngrades`, `TestCheck_DevBuildNeverAvailable`, `TestCheck_DescribeSuffixedCurrentNotCompared` (current `v0.223.0-5-gabc1234` or `-dirty`, latest `v0.223.0` or older → no update offered, `LocalBuild` set, apply of a named version allowed, of an unnamed one refused) | comparison, OPS4-17 |
| `TestCheck_ScheduledRunNeverApplies` (mode `check`, a newer release: the scheduled run notifies and records `Status`; no download, no swap; `auto_update.enabled=true` → `AutoApplyPaused` and Degraded `auto_update_paused`) | check-only unattended mode (A-6) |
| `TestGithubSource_ParameterisedSlugAndBaseURL` (httptest) | release source parameterised, no hard-coded repo |
| `TestRawBinary_MatchesHEAD` (`RawBinary("audiobook-organizer")` gives `audiobook-organizer-<GOOS>-<GOARCH>` for every GOOS/GOARCH of the build matrix, `updater.go:318-326`) | asset contract |
| `TestApply_RawAssetNoBackup` (fake source serving only HEAD's raw asset: apply streams it into `<exe>.new`, opens no archive and reads no `checksums.txt`; `Deps` has no backup runner, and the fake source records that nothing else was fetched; the same run in a container fixture applies as on a host) | HEAD's pipeline (R10-8, OPS10-6) |
| `TestApply_OversizeAborts`, `TestApply_FailedDownloadDeletesNew` | the size cap; HEAD's delete of `.new` on a failed write |
| `TestApply_SelfTestFailsKeepsOldBinary` (fake binary in a temp dir) | self-test |
| `TestSelfTest_VersionJSONFieldList` (the new binary must print exactly `{app, version, commit, ui_build_id, binary_id}`; a missing `app` fails the self-test; `commit` may be `""`) | FE6-4 |
| `TestApply_SwapAtomicAndPrevKept` (slot name `.prev`), `TestApply_RestartExits75` (fake `Restarter`: exit mode uses `core.lifecycle.restart_exit_code`) | swap, restart |
| `TestRollback_ReaderRefusesPrev` (fake `.prev` reporting a lower level → refused with `level_newer` naming `make rollback-restore`, running binary kept), `TestRollback_SwapsPrevBack` (the replaced binary is kept as `<exe>.rolled-back`, `make rollback`'s name) | manual rollback (OPS-6) |
| `TestApply_BinaryDirNotWritableRefusedAndDegraded` (read-only temp directory) | OPS2-4 |
| `TestApply_RefusedWhileResetPending` (a staged `restore/pending.json` of kind `reset`: refused with `restore_pending`, the message naming the kind; then its journal at `set_aside_started` before any target has moved, at `set_aside`, and again at `applied` without `hook_done`, each with its `restore/<id>/inflight.json`: refused with `restore_inflight`; a restore journal at `rechecked` whose `pending.json` was just renamed to `inflight.json`, before `previous_moved`: refused with `restore_inflight`, never `restore_pending` (R9-7, DATA9-3); apply and manual rollback refused, nothing downloaded or swapped; no `reset_*` token exists), `TestApply_CheckpointJournalAdmitsCandidate` (a `KindCheckpoint` journal at `swapped`, and again at `failed`: a candidate whose levels and floors admit the restored store and which answers `--print-restore-journal` passes `ReaderCheck`; one that does not admit the store, or a candidate without the flag, refuses with `restore_inflight`) | R7-2, R7-4, R8-1, OPS7-11, DATA7-12, DATA8-6, R9-7 |
| `TestCheck_NotifiesOncePerVersionAcrossRestart` | COR2-3 |
| `TestPolicy_NilOptionsNoRoutesNoTasks` | absence |
| `TestConfig_ModeIsProtectAdmin` | SEC-6 |
| `TestApply_OnlyPathIsManualTask` (no other caller of `Apply`, and `core.updater.apply` declares no `Schedule`; AST) | single, manual apply path |
| `TestNoGlobals_Updater` (R5 AST) | no globals |

## 5U.12 Migration from ABO

| ABO today | Becomes |
|---|---|
| `internal/updater.Updater` (`updater.go`), as R0 hotfixes it | `core/updater` + `core/updater/github`, **moved** in R2 (A-6), with HEAD's raw-asset download and no archive, checksum, backup or container step (R10-8): ABO's `main` passes its slug and binary name and keeps the default `RawBinary` asset namer |
| `internal/updater.Scheduler` + `SchedulerStarterAdapter` + `updatescheduler` service (`scheduler.go`) and the `init()` registration (`register.go:59-90`) | deleted, not moved: only `updater.go` moves (MOD9-9); `core.updater.check` task with its schedule key; no scheduled apply; the kernel wires the updater from `app.Options.Updater` |
| maintenance-window step 1 (`scheduler_maintenance_window_op.go:73-92`) and setting `maintenance_window_update_completed` | deleted (the hotfix made it check-only in R0) |
| `RestartSelf` (`os.Exit(0)`, `updater.go:329-334`) | `Restarter.Restart` (graceful, exit 75; no exec mode). Until R2, the R0 hotfix of HEAD's updater (09) does this: exit 75, `.prev` kept, semver comparison that treats a describe-suffixed current version as a local build and offers nothing (OPS4-17), and **both** auto-apply paths of 5U.2 check-only (OPS3-12); it makes no store check of the new binary. From R1, when the journaled swap lands, it also refuses while the swap slot is taken (`restore_pending`, `restore_inflight`, read in process through core `storage`'s `CheckReader` binding, never through `sudo`, which cannot run under the unit's `NoNewPrivileges=yes`, `deploy/audiobook-organizer.service:122`; OPS8-8, R8-1). R2 moves exactly this behaviour into core |
| `<exe>.old`, deleted after the swap (`updater.go:267-280`) | `<exe>.prev`, kept; shared with `make rollback` (from the R0 hotfix) |
| `/api/v1/update/*` handlers (`update_handlers.go`) | core routes; the old paths are deleted in R2 |
| `auto_update.*` config | `core.updater.*` and `core.updater.schedules.check`, renamed once by the R2 config cutover (5U.8) |
| release pipeline (reusable workflow, UNKNOWN details) | unchanged in R2; ABO's asset namer must match what the pipeline publishes — verify before an admin applies through the core updater (open item 3) |

**R2 done-check.** The move is checked against the hotfix it moves: R0's updater tests, re-pointed
at `core/updater`, pass with unchanged expectations (exit 75, `.prev` kept, the semver and local-build
rule, check-only scheduled runs). Their one fixture change: the fake asset they download answers `--version-json`
with `app == AppID` and `version ==` the target, because the self-test of step 3 is new in the moved pipeline and
the R0 hotfix runs none (VER11 note, R11-5). 5U.11 is green, `TestApply_RawAssetNoBackup` included: an apply
fetches HEAD's raw asset and takes no backup (R10-8, OPS10-6).
Those tests inject a test source, so they do not check ABO's own wiring, and a wrong slug or asset namer
would make every check report nothing and every manual apply fail. `TestABOUpdaterSourceMatchesHEAD`
**(ABO, root CI)** therefore builds the `updater.Options` ABO's `main` passes and asserts that they
produce HEAD's API path for the repository (`internal/updater/updater.go:36,71`), HEAD's raw asset name
for each GOOS/GOARCH of goreleaser's build matrix (open item 3; `updater.go:318-326`,
`.goreleaser.yml:42-49`) and HEAD's user agent (`updater.go:115`), and it runs R0's
fake-server test through those options with only the base URL injected (OPS9-17). The real-RC check is
deferred-build with signed manifests (5U.3).

**State R2 adds.** R2's updater writes two things no earlier build writes: the core key `c/updater/seen` (5U.4
Check) and the durable clock of the `core.updater.check` kind, which `tasks.Stores.Clocks` holds (05-tasks 5T.3.8).
09 lists both among R2's state (OPS9-17).

## 5U.13 Open items

1. (Moved to the deferred signed manifests: the signing scheme — plain ed25519 over the release
   manifest vs. minisign vs. Sigstore/cosign — is decided when that member is built.)
2. (Closed, OPS2-11/R2-4/OPS3-7/OPS6-1, revised by D10: an upgrade-path constraint is a boot refusal only. A
   binary whose store it cannot serve exits 78 before any write, naming the release to install first; the
   updater checks nothing before the swap.)
3. Confirm the asset naming the reusable release workflow actually publishes (UNKNOWN in this repo).
4. Windows support for the swap (a running `.exe` cannot be renamed over on some setups): not a
   current deployment target; the deferred exec-mode restart is Unix-only.
5. (Closed: 06 `app.Options` has `Updater *updater.Options`.)
6. (Closed by D10: there is no applied-steps sidecar check and no `StoreState`; `--print-upgrade-floor`
   stays as a fast exit of the binary (05-storage).)

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

Round 9:

- R9-7 / DATA9-3 — `ReaderCheck` comment and error row: `inflight.json` exists from before every kind's first rename (a restore renames `pending.json` after `rechecked`, before `previous_moved`; a reset at `set_aside_started`) until the terminal step, so `restore_pending` always means nothing has moved and `ROLLBACK_IGNORE_RESTORE=1` cannot land an older build on a half-swapped directory; `TestApply_RefusedWhileResetPending` gains the `set_aside_started` and post-`rechecked` cases.
- COR9-7 — `TestUpdaterRollbackRejectsFailingBuildsIntent` moved into the deferred boot-attempt counter row (no S0–S10 build has P−1).
- OPS9-17 — 5U.12: `TestABOUpdaterSourceMatchesHEAD` added to the S8 done-check (ABO's slug, asset names per GOOS/GOARCH and user agent equal HEAD's; S0's fake-server test through ABO's options); "State S8 adds" names `c/updater/seen` and the `core.updater.check` clock.
- R9-8 / R9-9 — step 5: the five-field `--version-json` object is also read by `soak_record.py` (soak row bound to `binary_id`) and by `staging-restore` (archive manifest `binary_id`), so its pinning test covers all three readers.
- MOD9-9 — 5U.12: `scheduler.go` and `register.go` are deleted, not moved; only `updater.go` moves (04 to match).

Round 10:

- R10-8 / OPS10-6 — §5U.4 apply restated as HEAD's pipeline as the S0 hotfix leaves it, five steps: preconditions; HEAD's raw asset (`RawBinary`, `audiobook-organizer-<GOOS>-<GOARCH>`) streamed straight into `<exe>.new` with HEAD's user agent (`<BinaryName>/<version>`) and a `max_download` cap; the hotfix's self-test, level and floor checks (now step 3); the `.prev` swap; the exit-75 restart. Archive download, checksums and member extraction (`GoreleaserArchive`, `Deps.StateDir`), the pre-update backup (`Deps.Backup`, `require_backup`), the container refusal and the exec-mode restart (`restart_mode`) moved to the deferred-build table with their tests, error rows, metric outcomes and keys; the layer list drops `backup`; manual rollback keeps the replaced binary as `<exe>.rolled-back`, `make rollback`'s name; `TestRawBinary_MatchesHEAD`, `TestApply_RawAssetNoBackup` (in the S8 done-check) and `TestApply_FailedDownloadDeletesNew` added.
- integration pass (round 10): §5U.4 intro: the moved pipeline makes exactly two additions to HEAD's, the `max_download` cap and the `--version-json` self-test; the exec-mode restart is deferred-build (LEAD, 09 S8).

Round 11:

- R11-5 (VER11 note) — 5U.12 "S8 done-check": S0's re-pointed updater tests keep their expectations, and their fake asset answers `--version-json`, the step-3 self-test being new in the moved pipeline.

- D10–D12 revision (2026-11-04): the pre-swap store checks of a new binary deleted (level, format and floor
  `ReaderCheck` of `<exe>.new`, `--print-upgrade-floor` against the applied-steps sidecar, `StoreState`,
  `StoreApplied`, `UpgradeRequirement`, `Status.SteppingStone`, `stepping_stone_required`, their error rows,
  outcomes and tests): an upgrade constraint is a boot refusal only (exit 78). `ReaderCheck` kept narrowed to
  levels and the swap slot, used for the slot precondition and for the manual rollback, whose refusal across R2
  names `make rollback-restore`. Database caveat rewritten (binary swap within R0/R1 and R3→R2; restore across
  R2). `auto_update.*` renamed once by the R2 config cutover, no aliases; `/api/v1/update/*` deleted in R2.
  Stage references re-keyed (S0 hotfix → R0, S8 move → R2, S2b swap → R1).

