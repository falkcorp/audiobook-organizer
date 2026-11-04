<!-- file: docs/specs/platform-core/05-updater.md -->
<!-- version: 1.1.0 -->
<!-- guid: 87f3821c-d9b3-427d-b91f-dad6de16aef8 -->
<!-- last-edited: 2026-11-04 -->

# 05 — Updater (`core/updater`)

Evidence commit `748fd787a`. Raw inputs: [`raw/12-cross-cutting.md`](../../plans/platform-core/raw/12-cross-cutting.md) §6,
[`raw/14-build-ci.md`](../../plans/platform-core/raw/14-build-ci.md) (release pipeline),
[`raw/07-plugin-registry-sdk.md`](../../plans/platform-core/raw/07-plugin-registry-sdk.md) §1.3 rows 23–24.
Layer **L4** (03 R4 v1.2.0); imports `tasks` (check/apply are tasks), `health`, `events`, `config`,
`secrets`, `integrations/providerhttp` (outbound HTTP), `backup` — all L4 or lower.

## 5U.1 Purpose & scope

Self-update of an app binary that runs outside a container: discover a newer release from the
**app's** release source, verify it cryptographically, swap the binary atomically, restart
gracefully, confirm the new version came up, and roll back automatically if it did not. Each app
decides whether it ships an updater at all; when it does, the updater is **off by default**.

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
	AppID         string // must equal ReleaseManifest.App and the new binary's self-test
	BinaryName    string // archive member to install, e.g. "audiobook-organizer"
	Source        Source
	AssetName     func(r Release, goos, goarch string) string // default: GoreleaserArchive(AppID)
	Manifest      string       // asset name; default "release-manifest.json"
	Signature     string       // asset name; default "release-manifest.json.sig"
	TrustedKeys   []TrustedKey // THIS app's keys only (one signing key per app); empty => apply impossible except the legacy path, 5U.5
	RevokedKeyIDs []string     // compiled-in; a revoked id never verifies, even if also trusted
	DefaultMode   Mode         // the app's default; core forces ModeOff in containers
}

type TrustedKey struct {
	ID  string            // short key id, named in the signature file
	Key ed25519.PublicKey // stdlib crypto/ed25519; no new dependency
}

// ReleaseManifest is the signed statement the updater trusts (SEC-27). The
// signature covers the manifest file's exact bytes, so it binds the app,
// version and channel to the asset digests; a validly signed older release
// or a sibling app's release cannot be served under another tag.
type ReleaseManifest struct {
	App     string            `json:"app"`
	Version string            `json:"version"`
	Channel Channel           `json:"channel"`
	Assets  map[string]string `json:"assets"` // asset name -> lowercase hex SHA-256
}

type Mode uint8

const (
	ModeOff   Mode = iota // no network calls at all (the default for every app)
	ModeCheck             // scheduled check + notification; apply only by an admin
	ModeApply             // scheduled check + automatic apply inside the window
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
	Mode            Mode
	Channel         Channel
	Current         string
	Latest          string
	Available       bool // semver(Latest) > semver(Current); never a downgrade
	LastChecked     time.Time
	LastError       string // fixed token
	Pending         *Pending // an applied update awaiting confirmation
	PreviousKept    string   // version of <exe>.prev, if any (manual rollback target)
	RollbackBlocked string   // fixed token when an automatic rollback was refused by the guard (5U.4)
	Unsigned        bool     // the legacy unsigned path is enabled (5U.5)
	DisabledReason  string   // "off", "container", "no_trusted_keys", "dev_build"
}

type Pending struct {
	From, To  string
	AppliedAt time.Time
	Boots     int
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
	Restarter   Restarter            // kernel: graceful shutdown then restart
	Health      *health.Registry
	Events      *events.Bus
	ReaderCheck ReaderCheck          // kernel-bound to the single compat/format guard (05-storage)
	StateDir    string               // <state-dir>/updates (opened with os.Root)
	Log         *slog.Logger
}

// ReaderCheck asks whether the binary at exe may open the data directory as
// it is now: the decision scripts/storage_format_guard.py makes for
// `make rollback`, implemented once by 05-storage's guard (SIMP-18) over the
// sidecar files, so it needs no open store. nil error = allowed; otherwise a
// fixed reason token ("format_newer", "compat_level_required", "no_sd_notify", ...).
type ReaderCheck func(ctx context.Context, exe string) error

type Restarter interface {
	// Restart runs the full shutdown sequence within the stop budget
	// (05-health 5H.4) and then exits/execs per core.updater.restart_mode.
	Restart(ctx context.Context, reason string) error
}

// Boot runs FIRST in app.New, before storage opens (a failing migration must
// not prevent rollback). It reads the file marker, counts boots, and after
// core.updater.max_boot_attempts failed boots rolls back to <exe>.prev only if
// check allows it (OPS-6).
func Boot(stateDir, exe string, maxAttempts int, check ReaderCheck) (BootResult, error)

type BootResult struct {
	RolledBack      bool   // true: the previous binary was restored; the caller exits for restart
	RollbackBlocked string // non-empty: rollback refused by check; boot continues on the new binary
	Pending         *Pending
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

ABO passes `github.New("falkcorp", "audiobook-organizer")`; a TV app passes its own slug and its own
key. Nothing in core names a repository.

## 5U.4 Lifecycle

**Check** — task `core.updater.check`: Shape Task, `ReadOnly`, `Resume: ResumeDrop`,
`Perms.Start: core:system.manage`, `Priority: PrioritySystem`,
`Schedule: tasks.ScheduleFromConfig("core.schedules.updater.check").When(mode >= ModeCheck)` — the one
schedule mechanism on the durable clock ([05-tasks](05-tasks.md) 5T.3.8; SIMP-6; fixes the
restart-reset ticker). It calls `Source.Latest`, compares semver (`golang.org/x/mod/semver`), refuses
to report an update for a `dev`/unversioned build, records `Status`, sets component `core.updater`
(Up / Degraded `check_failed`, `no_trusted_keys`, `unsigned_updates`, `rollback_blocked`), and
publishes `core.update.found.v1` on the bus when a new version first appears (declared in the one
topic catalog with `Notify`, [05-events](05-events.md) 5E.3.1).

**Apply** — task `core.updater.apply`: Shape Task, `Effects: Writes(tasks.Res("core","binary"))`,
`Resume: ResumeDrop`, `ConcurrencyKey: core.updater`, `Cancel: Cooperative(30s)` until step 8,
`Perms.Start: core:admin`, `Priority: PrioritySystem`,
`Schedule: tasks.ScheduleFromConfig("core.schedules.updater.apply").When(mode == ModeApply).Live()`
(default `{enabled: true, window: "nightly", order: 0}`: the single automatic path; the
maintenance-window step 1 and the separate ticker are deleted). Because the scheduled run acts as the
system actor, `core.updater.mode` carries `ProtectAdmin` (5U.8, SEC-6): only a `core:admin` holder
with an interactive method can turn automatic apply on. Steps, each a `rc.Phase`:

1. Preconditions: not in a container, current build is versioned, target newer than current, inside
   the window when scheduled, and either `TrustedKeys` non-empty or the legacy path enabled (5U.5).
2. Download the archive, `release-manifest.json` and its `.sig` into an `os.Root` on
   `<state-dir>/updates/<version>/`, via `providerhttp` (throttle, retry, user agent
   `<AppID>/<version>`), each capped by `core.updater.max_download`.
3. Verify (SEC-27): the signature over the manifest's bytes with a trusted, non-revoked key named by
   the signature file; then `App == Options.AppID`, `Version` equal to the release being applied,
   `Channel` allowed by the configured channel (a stable install accepts only `stable`; a prerelease
   install accepts both); then the archive's SHA-256 against `Assets[name]`. Any mismatch: abort,
   delete the download, never fall back to the legacy path.
4. Extract only the `BinaryName` member (regular file, no path components, size-capped) to
   `<exe>.new` in the executable's directory (same filesystem, so the rename is atomic); fsync
   file and directory.
5. Self-test (second check, kept): run `<exe>.new --version-json` (10 s timeout, empty environment
   except `PATH`); it must print `{app: AppID, version: target}`.
6. Pre-update backup through `backup.Runner` ([05-backup](05-backup.md), in the format the previous
   build can restore during the window); failure aborts unless `core.updater.require_backup=false`.
7. Write the pending marker file `<state-dir>/updates/pending.json` `{from, to, applied_at, boots: 0}`
   (a file, not storage: `Boot` reads it before storage opens).
8. Swap: rename `<exe>` → `<exe>.prev`, `<exe>.new` → `<exe>`; fsync directory. `.prev` is the same
   slot `make rollback` uses (`Makefile:892-913`), so mixed use of the two mechanisms never diverges
   (OPS-6). From here the task is uninterruptible (two renames).
9. `Restarter.Restart` — graceful shutdown within the stop budget, then exit with
   `core.updater.restart_exit_code` (default 75, so `Restart=on-failure` units restart; `0` needs
   `Restart=always`) or `exec` the new binary in place (`core.updater.restart_mode=exec`, Unix only).

**Confirm / roll back** — `Boot` (in the new process, before storage) increments `boots` in the
marker. If `boots > core.updater.max_boot_attempts` (3), it first runs `ReaderCheck(<exe>.prev)`
(OPS-6): the same decision as the `make rollback` guard, from the one compat/format guard
(05-storage §5S.3.7, SIMP-18) plus the unit-type capability check. If the previous binary would be
refused — a contract release raised the level or the format, so `.prev` can no longer open the store —
`Boot` does **not** roll back: it records `rollback_blocked:<reason>` in the marker, stops counting
boots, reports Degraded `rollback_blocked`, and lets boot continue on the new binary, which is the
only one that can serve the store; an operator decides (restore the pre-update backup or fix
forward). Otherwise it restores `<exe>.prev` → `<exe>`, moves the failed binary to `<exe>.failed`,
records `rolled_back` in the marker and returns `RolledBack: true`; `main` exits with the restart code
so the supervisor starts the previous binary. Otherwise boot continues; once `/readyz` has been
ready continuously for `core.updater.confirm_after` (2 m), the updater deletes the marker (appending
it to `updates/history.jsonl`) and reports the update confirmed. `<exe>.prev` is kept until the next
update or deploy replaces it, so an admin can roll back manually (`POST .../rollback`, which runs the
same `ReaderCheck` first and refuses with its reason).

Database caveat: a rollback restores the **binary**, not the data. The previous release must read
the database the new release left — that is gate G5 and the compatibility window
([09-abo-on-core](09-abo-on-core.md) §9.2.1), checked by `ReaderCheck`; the pre-update backup is the
last resort, restored by an operator, never automatically (automatic restore would discard writes
made after the update).

## 5U.5 Policy

| Rule | Default | Rationale |
|---|---|---|
| App ships an updater | per app (`app.Options.Updater` nil or not) | a TV/Movies app distributed only as a container image needs none |
| Mode | `ModeOff` for every app unless its `DefaultMode` says otherwise **and** the operator sets `core.updater.mode` | no unsolicited network calls; current default disabled (`config.go:2991`) |
| Containers | forced off, `DisabledReason: container` (`sysinfo.Info.Container`, [05-health](05-health.md)) | replacing a binary inside an image is wrong |
| No trusted keys | check allowed; apply impossible (`no_trusted_keys`) unless the legacy path is enabled; while mode ≥ check, component Degraded `no_trusted_keys` and a UI banner (OPS-23) | fail closed on unverifiable artifacts, but never silently |
| Legacy unsigned path (OPS-23) | `core.updater.allow_unsigned_legacy=false`; set to `true` only by the config migration of an install that had `auto_update.enabled=true` | existing auto-update users must not silently stop receiving updates at key introduction |
| Downgrades | never automatic; only `Rollback` to the kept previous binary, and only when `ReaderCheck` allows it | string-inequality defect (`updater.go:153`); OPS-6 |
| Window | apply only inside the window its schedule key names (`nightly` by default) when scheduled; manual apply any time | one window instead of two (5U.2) |
| Who | check: `core:system.manage`; apply/rollback: `core:admin` (audited, credential-guarded route as today); turning on scheduled apply: `ProtectAdmin` on `core.updater.mode` | RBAC (05-rbac); SEC-6 |

**Legacy unsigned path.** When `allow_unsigned_legacy` is true **and** the release publishes no
`release-manifest.json.sig` at all, apply proceeds at today's trust level (HTTPS from the configured
release source) plus the SHA-256 in goreleaser's `checksums.txt` when it is published
(`.goreleaser.yml:55-56`). The status shows `Unsigned: true`, the component is Degraded
`unsigned_updates` and the UI shows a banner. The path is never taken when a signature asset exists
(a bad signature aborts), never on a fresh install, and the flag is cleared (an audited system write
to the DB override layer) after the first successful apply of a signed release. Precondition for the
release that introduces `core/updater` (09 S8): a signed release is already published and the public
key is compiled in, so a migrated install's first update is normally already signed.

**Release pipeline requirements** (cost surfaced: the reusable release workflow lives outside this
repo, and signing is new work there): publish per-platform archives named by the app's `AssetName`,
`release-manifest.json` = `{app, version, channel, assets: {name: sha256}}` for that app only, and
`release-manifest.json.sig` = ed25519 signature over the manifest bytes with **that app's** key (one
key per app; never shared between ABO and a TV/Movies app), key id in the signature file; the private
key is a CI secret. `checksums.txt` stays (goreleaser produces it). Key rotation: ship the new public
key in release N, sign with it from release N+1, drop the old key in N+2. A compromised key goes on
the compiled-in revoked list in the next release.

## 5U.6 Error semantics

| Error path | Fail | Why |
|---|---|---|
| Source unreachable / non-200 / decode error | open: `Status.LastError` token, component Degraded `check_failed`, next tick retries | a failed check must not affect the app |
| Release has no matching asset | open for check (reported `no_asset`); closed for apply | — |
| Signature invalid, unknown or revoked key, manifest `app`/`version`/`channel` mismatch, checksum mismatch, oversize download, bad archive member | closed: abort, delete download, metric `apply_total{outcome="verify_failed"}`, WARN; no fallback to the legacy path | never run an unverified or substituted binary (SEC-27) |
| Self-test fails | closed: delete `<exe>.new`, abort | catches wrong arch, corrupt build, wrong app |
| Backup fails | closed (unless `require_backup=false`) | recovery path must exist before a swap |
| Rename fails | closed: restore `<exe>` from `<exe>.prev` if the first rename happened; abort | atomic swap or nothing |
| Restart fails after swap | closed: the process keeps running the old code in memory; marker says pending; next manual or supervisor restart boots the new binary under the boot counter | — |
| New binary fails to become ready within `max_boot_attempts` boots, `.prev` allowed by `ReaderCheck` | closed: automatic binary rollback in `Boot` | the purpose of the marker |
| Same, but `ReaderCheck` refuses `.prev` | open: no rollback; `rollback_blocked`, Degraded, boot continues on the new binary | rolling back to a binary that refuses the store leaves the unit failed with no automatic recovery (OPS-6) |
| Marker unreadable | closed: treat as `boots = max` → roll back if `<exe>.prev` exists and `ReaderCheck` allows, else continue and report Degraded `marker_corrupt` | a corrupt marker must not loop |
| Running in a container / dev build | closed: apply refused with reason | policy |

## 5U.7 Concurrency contract

All mutation happens inside the `core.updater.apply` task, serialised by `ConcurrencyKey:
core.updater` (one apply at a time, and never concurrently with a manual rollback, which runs as
task `core.updater.rollback` under the same key). `Status()` is a lock-free atomic read. `Boot`
runs single-threaded before any goroutine of the app starts. Downloads stream to disk (no
whole-archive buffering) and honour `rc.Context()` until the swap phase.

## 5U.8 Config keys

`Protection` levels are 05-config's (`ProtectAdmin` = `core:admin` + an interactive method, SEC-6);
`—` means the default for `core.*` writes.

| Key | Type | Default | Hot reload | Protection |
|---|---|---|---|---|
| `core.updater.mode` | `off`/`check`/`apply` | `off` (or the app's `DefaultMode`) | yes | `ProtectAdmin` |
| `core.updater.channel` | `stable`/`prerelease` | `stable` | yes | `ProtectAdmin` |
| `core.updater.allow_unsigned_legacy` | bool | false (true only via the `auto_update.enabled` migration) | yes | `ProtectAdmin` |
| `core.updater.max_download` | bytes | 512 MiB | yes | — |
| `core.updater.require_backup` | bool | true | yes | `ProtectAdmin` |
| `core.updater.confirm_after` | duration | 2m | yes | — |
| `core.updater.max_boot_attempts` | int | 3 | yes (read by `Boot` from the marker's copy) | — |
| `core.updater.restart_mode` | `exit`/`exec` | `exit` | yes | `ProtectAdmin` |
| `core.updater.restart_exit_code` | int | 75 | yes | `ProtectAdmin` |
| `core.schedules.updater.check` | `tasks.ScheduleConfig` | `{enabled: true, every: 1h}` (current 60 min) | yes | — |
| `core.schedules.updater.apply` | `tasks.ScheduleConfig` | `{enabled: true, window: "nightly", order: 0}` | yes | `ProtectAdmin` |

There is no `core.updater.check_interval`: the interval is the schedule key (SIMP-6).

ABO compatibility: `auto_update.enabled=true` maps to `mode=apply` **and** sets
`allow_unsigned_legacy=true` (OPS-23); `auto_update.channel` maps to `channel` (`develop` →
`prerelease` with a WARN); `auto_update.check_minutes` maps to `core.schedules.updater.check.every`;
`auto_update.window_start/end` map to `core.tasks.windows.nightly` only if the operator set them
explicitly, otherwise the maintenance window wins (one window). Aliases live for the compatibility
window ([09](09-abo-on-core.md) §9.2.1; [05-config](05-config.md) owns the alias mechanism).

## 5U.9 Metrics

| Name | Type | Labels |
|---|---|---|
| `mediacore_updater_checks_total` | counter | `outcome` (`ok`, `failed`, `no_asset`) |
| `mediacore_updater_update_available` | gauge (0/1) | — |
| `mediacore_updater_apply_total` | counter | `outcome` (`applied`, `applied_unsigned`, `verify_failed`, `selftest_failed`, `backup_failed`, `swap_failed`, `refused`) |
| `mediacore_updater_rollbacks_total` | counter | `trigger` (`boot_attempts`, `manual`), `outcome` (`done`, `blocked`) |
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
| `TestCheck_SemverNeverDowngrades`, `TestCheck_DevBuildNeverAvailable` | comparison |
| `TestGithubSource_ParameterisedSlugAndBaseURL` (httptest) | no hard-coded repo |
| `TestGoreleaserArchive_MatchesTemplate` (golden against `.goreleaser.yml` naming) | asset contract |
| `TestApply_BadSignatureAborts`, `TestApply_ChecksumMismatchAborts`, `TestApply_UnknownKeyAborts`, `TestApply_RevokedKeyAborts`, `TestApply_OversizeAborts` | verification |
| `TestApply_ManifestBindsAppVersionChannel` (sibling app's manifest, older signed release under a new tag, prerelease manifest on a stable install: all refused before extraction) | SEC-27 |
| `TestApply_BadSignatureNeverFallsBackToLegacy`, `TestApply_LegacyPathOnlyWhenNoSignatureAndFlag`, `TestLegacyFlag_ClearedAfterSignedApply` | OPS-23 |
| `TestApply_ArchiveMemberTraversalRejected` | extraction safety |
| `TestApply_SelfTestFailsKeepsOldBinary` (fake binary in a temp dir) | self-test |
| `TestApply_BackupFailureAborts` | backup gate |
| `TestApply_SwapAtomicAndPrevKept` (slot name `.prev`) | swap |
| `TestBoot_RollsBackAfterMaxAttempts`, `TestBoot_ConfirmAfterReady` (synctest) | rollback/confirm |
| `TestBoot_RollbackBlockedWhenPrevRefusesStore` (fake `.prev` reporting a lower format/level) | OPS-6 |
| `TestBoot_CorruptMarker` | marker safety |
| `TestPolicy_ContainerForcesOff`, `TestPolicy_NoKeysCheckOnlyAndDegraded`, `TestPolicy_NilOptionsNoRoutesNoTasks` | policy, absence |
| `TestConfig_ModeIsProtectAdmin` | SEC-6 |
| `TestApply_OnlyPathIsWindowTask` (no other caller of `Apply`; AST) | single apply path |
| `TestNoGlobals_Updater` (R5 AST) | no globals |

## 5U.12 Migration from ABO

| ABO today | Becomes |
|---|---|
| `internal/updater.Updater` (`updater.go`) | `core/updater` + `core/updater/github`; ABO's `main` passes its slug, binary name, asset namer and its own public key |
| `internal/updater.Scheduler` + `SchedulerStarterAdapter` + `updatescheduler` service | deleted; `core.updater.check`/`apply` tasks with schedule keys |
| maintenance-window step 1 (`scheduler_maintenance_window_op.go:73-92`) and setting `maintenance_window_update_completed` | deleted; the window runs `core.updater.apply` as member 0 |
| `RestartSelf` (`os.Exit(0)`, `updater.go:329-334`) | `Restarter.Restart` (graceful, exit 75). Until then, under `Restart=on-failure` every HEAD auto-update leaves the service down: 09 either hotfixes the HEAD updater to exit non-zero in S0 or states that auto-update stays off until S8 (OPS-6) |
| `<exe>.old`, deleted after the swap (`updater.go:267-280`) | `<exe>.prev`, kept; shared with `make rollback` |
| `/api/v1/update/*` handlers (`update_handlers.go`) | core routes + aliases |
| `auto_update.*` config | `core.updater.*` and `core.schedules.updater.*` with aliases (5U.8) |
| release pipeline (reusable workflow, UNKNOWN details) | must add the signed `release-manifest.json` per app before the S8 release; ABO's asset namer must match what the pipeline publishes — verify before enabling apply |

## 5U.13 Open items

1. Signing scheme: plain ed25519 over the release manifest (stdlib, chosen here) vs. minisign format
   vs. Sigstore/cosign keyless (free, but verification needs online transparency-log access and large
   dependencies). Revisit only if the release workflow already produces one of the others.
2. Upgrade-path constraints ("must pass through version X for a migration"): a `requires_from`
   field in the release manifest; not needed until a release needs it. Releases can be skipped
   (cross-cutting resolution 6): the new binary's own boot-time level check refuses a store it cannot
   adopt, and `Boot` then rolls back to `.prev` (allowed, since `.prev` still reads the store).
3. Windows support for the swap (a running `.exe` cannot be renamed over on some setups): not a
   current deployment target; `exec` mode is Unix-only.
4. Confirm the asset naming the reusable release workflow actually publishes (UNKNOWN in this repo).
5. [06-app-sdk](06-app-sdk.md) `app.Options` has `Release updater.Source`; this file needs
   `Updater *updater.Options` (per-app keys and manifest names do not fit in a `Source`).
6. 05-storage must expose the single guard's decision as a function the kernel binds to
   `ReaderCheck` (SIMP-18), readable before the store opens.

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
