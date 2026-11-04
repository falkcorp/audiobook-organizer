<!-- file: docs/specs/platform-core/05-updater.md -->
<!-- version: 1.0.0 -->
<!-- guid: 87f3821c-d9b3-427d-b91f-dad6de16aef8 -->
<!-- last-edited: 2026-11-04 -->

# 05 — Updater (`core/updater`)

Evidence commit `748fd787a`. Raw inputs: [`raw/12-cross-cutting.md`](../../plans/platform-core/raw/12-cross-cutting.md) §6,
[`raw/14-build-ci.md`](../../plans/platform-core/raw/14-build-ci.md) (release pipeline),
[`raw/07-plugin-registry-sdk.md`](../../plans/platform-core/raw/07-plugin-registry-sdk.md) §1.3 rows 23–24.
Layer 3 (R4); imports `tasks` (check/apply are tasks), `health`, `config`, `secrets`,
`integrations/providerhttp` (outbound HTTP), `backup`.

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
	AppID       string // identity the new binary must report in its self-test
	BinaryName  string // archive member to install, e.g. "audiobook-organizer"
	Source      Source
	AssetName   func(r Release, goos, goarch string) string // default: GoreleaserArchive(AppID)
	Checksums   string // asset name; default "checksums.txt"
	Signature   string // asset name; default "checksums.txt.sig"
	TrustedKeys []TrustedKey // empty => apply is impossible (check-only), see 5U.5
	DefaultMode Mode         // the app's default; core forces ModeOff in containers
}

type TrustedKey struct {
	ID  string            // short key id, named in the signature file
	Key ed25519.PublicKey // stdlib crypto/ed25519; no new dependency
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
// updater/manifest (a static HTTPS JSON manifest for self-hosted feeds).
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
	Available      bool // semver(Latest) > semver(Current); never a downgrade
	LastChecked    time.Time
	LastError      string // fixed token
	Pending        *Pending // an applied update awaiting confirmation
	PreviousKept   string   // version of <exe>.old, if any (manual rollback target)
	DisabledReason string   // "off", "container", "no_trusted_keys", "dev_build"
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
// Rollback swaps <exe>.old back and requests a graceful restart.
func (u *Updater) Rollback(ctx context.Context) error

// Deps are core services, injected by the kernel.
type Deps struct {
	HTTP      *providerhttp.Client // instance-scoped outbound client (05-metadata)
	Backup    backup.Runner         // 05-backup
	Restarter Restarter             // kernel: graceful shutdown then restart
	Health    *health.Registry
	StateDir  string                // <state-dir>/updates (opened with os.Root)
	Log       *slog.Logger
}

type Restarter interface {
	// Restart runs the full shutdown sequence within the stop budget
	// (05-health 5H.4) and then exits/execs per core.updater.restart_mode.
	Restart(ctx context.Context, reason string) error
}

// Boot runs FIRST in app.New, before storage opens (a failing migration must
// not prevent rollback). It reads the file marker, counts boots, and rolls
// back after core.updater.max_boot_attempts failed boots.
func Boot(stateDir, exe string, maxAttempts int) (BootResult, error)

type BootResult struct {
	RolledBack bool   // true: the old binary was restored; the caller exits for restart
	Pending    *Pending
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

ABO passes `github.New("falkcorp", "audiobook-organizer")`; a TV app passes its own slug. Nothing
in core names a repository.

## 5U.4 Lifecycle

**Check** — task `core.updater.check`: Shape Task, `ReadOnly`, `Resume: ResumeDrop`,
`Perms.Start: core:system.manage`, `Priority: PrioritySystem`,
`Schedule: Interval(check_interval).When(mode >= ModeCheck)` on the durable clock
([05-tasks](05-tasks.md) 5T.3.8; fixes the restart-reset ticker). It calls `Source.Latest`, compares
semver (`golang.org/x/mod/semver`), refuses to report an update for a `dev`/unversioned build,
records `Status`, sets component `core.updater` (Up / Degraded `check_failed`), and publishes
`core.updater.available.v1` on the bus when a new version first appears (notification adapters may
subscribe, [05-notify](05-notify.md)).

**Apply** — task `core.updater.apply`: Shape Task, `Effects: Writes(Res("core","binary"))`,
`Resume: ResumeDrop`, `ConcurrencyKey: core.updater`, `Cancel: Cooperative(30s)` until step 8,
`Perms.Start: core:admin`, `Priority: PrioritySystem`,
`Schedule: InWindow("nightly", 0).When(mode == ModeApply).Live()` (the single automatic path; the
maintenance-window step 1 and the separate ticker are deleted). Steps, each a `rc.Phase`:

1. Preconditions: not in a container, `TrustedKeys` non-empty, current build is versioned, target
   newer than current, inside the window when scheduled.
2. Download archive, checksums file and signature into an `os.Root` on
   `<state-dir>/updates/<version>/`, via `providerhttp` (throttle, retry, user agent `<AppID>/<version>`),
   each capped by `core.updater.max_download`.
3. Verify the signature over the checksums file with a trusted key named by the signature file;
   then verify the archive's SHA-256 against its line. Any mismatch: abort, delete the download.
4. Extract only the `BinaryName` member (regular file, no path components, size-capped) to
   `<exe>.new` in the executable's directory (same filesystem, so the rename is atomic); fsync
   file and directory.
5. Self-test: run `<exe>.new --version-json` (10 s timeout, empty environment except `PATH`); it must
   print `{app: AppID, version: target}`.
6. Pre-update backup through `backup.Runner` ([05-backup](05-backup.md)); failure aborts unless
   `core.updater.require_backup=false`.
7. Write the pending marker file `<state-dir>/updates/pending.json` `{from, to, applied_at, boots: 0}`
   (a file, not storage: `Boot` reads it before storage opens).
8. Swap: rename `<exe>` → `<exe>.old` (kept), `<exe>.new` → `<exe>`; fsync directory. From here
   the task is uninterruptible (two renames).
9. `Restarter.Restart` — graceful shutdown within the stop budget, then exit with
   `core.updater.restart_exit_code` (default 75, so `Restart=on-failure` units restart; `0` needs
   `Restart=always`) or `exec` the new binary in place (`core.updater.restart_mode=exec`, Unix only).

**Confirm / roll back** — `Boot` (in the new process, before storage) increments `boots` in the
marker. If `boots > core.updater.max_boot_attempts` (3), it restores `<exe>.old` → `<exe>`, moves
the failed binary to `<exe>.failed`, records `rolled_back` in the marker and returns
`RolledBack: true`; `main` exits with the restart code so the supervisor starts the old binary.
Otherwise boot continues; once `/readyz` has been ready continuously for
`core.updater.confirm_after` (2 m), the updater deletes the marker (appending it to
`updates/history.jsonl`) and reports the update confirmed. `<exe>.old` is kept until the next
successful update, so an admin can roll back manually (`POST .../rollback`).

Database caveat: a rollback restores the **binary**, not the data. The previous release must read
the database the new release left — that is gate G5 and the two-minor-release compatibility windows
([09-abo-on-core](09-abo-on-core.md)); the pre-update backup is the last resort, restored by an
operator, never automatically (automatic restore would discard writes made after the update).

## 5U.5 Policy

| Rule | Default | Rationale |
|---|---|---|
| App ships an updater | per app (`app.Options.Updater` nil or not) | a TV/Movies app distributed only as a container image needs none |
| Mode | `ModeOff` for every app unless its `DefaultMode` says otherwise **and** the operator sets `core.updater.mode` | no unsolicited network calls; current default disabled (`config.go:2991`) |
| Containers | forced off, `DisabledReason: container` (`sysinfo.Info.Container`, [05-health](05-health.md)) | replacing a binary inside an image is wrong |
| No trusted keys | check allowed, apply impossible (`no_trusted_keys`) | fail closed on unverifiable artifacts |
| Downgrades | never automatic; only `Rollback` to the kept previous binary | string-inequality defect (`updater.go:153`) |
| Window | apply only inside `nightly` (core tasks window, [05-tasks](05-tasks.md) 5T.3.8) when scheduled; manual apply any time | one window instead of two (5U.2) |
| Who | check: `core:system.manage`; apply/rollback: `core:admin` (audited, credential-guarded route as today) | RBAC (05-rbac) |

**Release pipeline requirements** (cost surfaced: the reusable release workflow lives outside this
repo, and signing is new work there): publish per-platform archives named by the app's
`AssetName`, `checksums.txt` (goreleaser already produces it, `.goreleaser.yml:55-56`), and
`checksums.txt.sig` = ed25519 signature by a key whose public half is compiled into the app; the
private key is a CI secret. Key rotation: ship the new public key in release N, sign with it from
release N+1, drop the old key in N+2.

## 5U.6 Error semantics

| Error path | Fail | Why |
|---|---|---|
| Source unreachable / non-200 / decode error | open: `Status.LastError` token, component Degraded `check_failed`, next tick retries | a failed check must not affect the app |
| Release has no matching asset | open for check (reported `no_asset`); closed for apply | — |
| Signature invalid, unknown key, checksum mismatch, oversize download, bad archive member | closed: abort, delete download, metric `apply_total{outcome="verify_failed"}`, WARN | never run an unverified binary |
| Self-test fails | closed: delete `<exe>.new`, abort | catches wrong arch, corrupt build, wrong app |
| Backup fails | closed (unless `require_backup=false`) | recovery path must exist before a swap |
| Rename fails | closed: restore `<exe>` from `<exe>.old` if the first rename happened; abort | atomic swap or nothing |
| Restart fails after swap | closed: the process keeps running the old code in memory; marker says pending; next manual or supervisor restart boots the new binary under the boot counter | — |
| New binary fails to become ready within `max_boot_attempts` boots | closed: automatic binary rollback in `Boot` | the purpose of the marker |
| Marker unreadable | closed: treat as `boots = max` → roll back if `<exe>.old` exists, else continue and report Degraded `marker_corrupt` | a corrupt marker must not loop |
| Running in a container / dev build | closed: apply refused with reason | policy |

## 5U.7 Concurrency contract

All mutation happens inside the `core.updater.apply` task, serialised by `ConcurrencyKey:
core.updater` (one apply at a time, and never concurrently with a manual rollback, which runs as
task `core.updater.rollback` under the same key). `Status()` is a lock-free atomic read. `Boot`
runs single-threaded before any goroutine of the app starts. Downloads stream to disk (no
whole-archive buffering) and honour `rc.Context()` until the swap phase.

## 5U.8 Config keys

| Key | Type | Default | Hot reload |
|---|---|---|---|
| `core.updater.mode` | `off`/`check`/`apply` | `off` (or the app's `DefaultMode`) | yes |
| `core.updater.channel` | `stable`/`prerelease` | `stable` | yes |
| `core.updater.check_interval` | duration | 1h (current 60 min) | yes |
| `core.updater.max_download` | bytes | 512 MiB | yes |
| `core.updater.require_backup` | bool | true | yes |
| `core.updater.confirm_after` | duration | 2m | yes |
| `core.updater.max_boot_attempts` | int | 3 | yes (read by `Boot` from the marker's copy) |
| `core.updater.restart_mode` | `exit`/`exec` | `exit` | yes |
| `core.updater.restart_exit_code` | int | 75 | yes |

ABO compatibility: `auto_update.enabled=true` maps to `mode=apply`, `auto_update.channel` to
`channel` (`develop` → `prerelease` with a WARN), `auto_update.check_minutes` to `check_interval`;
`auto_update.window_start/end` map to `core.tasks.windows.nightly` only if the operator set them
explicitly, otherwise the maintenance window wins (one window). Aliases live for two minor
releases ([05-config](05-config.md), [09-abo-on-core](09-abo-on-core.md)).

## 5U.9 Metrics

| Name | Type | Labels |
|---|---|---|
| `mediacore_updater_checks_total` | counter | `outcome` (`ok`, `failed`, `no_asset`) |
| `mediacore_updater_update_available` | gauge (0/1) | — |
| `mediacore_updater_apply_total` | counter | `outcome` (`applied`, `verify_failed`, `selftest_failed`, `backup_failed`, `swap_failed`, `refused`) |
| `mediacore_updater_rollbacks_total` | counter | `trigger` (`boot_attempts`, `manual`) |
| `mediacore_updater_download_bytes_total` | counter | — |

## 5U.10 HTTP

| Route | Permission | Replaces |
|---|---|---|
| `GET /api/core/v1/updater` (Status) | `core:system.manage` | `GET /api/v1/update/status` |
| `POST /api/core/v1/updater/check` → starts `core.updater.check` | `core:system.manage` | `POST /api/v1/update/check` |
| `POST /api/core/v1/updater/apply` `{version}` → starts `core.updater.apply` | `core:admin`, credential guard | `POST /api/v1/update/apply` |
| `POST /api/core/v1/updater/rollback` → starts `core.updater.rollback` | `core:admin`, credential guard | new |

The `/api/v1/update/*` paths are aliases for two minor releases.

## 5U.11 Tests

| Test | Proves |
|---|---|
| `TestCheck_SemverNeverDowngrades`, `TestCheck_DevBuildNeverAvailable` | comparison |
| `TestGithubSource_ParameterisedSlugAndBaseURL` (httptest) | no hard-coded repo |
| `TestGoreleaserArchive_MatchesTemplate` (golden against `.goreleaser.yml` naming) | asset contract |
| `TestApply_BadSignatureAborts`, `TestApply_ChecksumMismatchAborts`, `TestApply_UnknownKeyAborts`, `TestApply_OversizeAborts` | verification |
| `TestApply_ArchiveMemberTraversalRejected` | extraction safety |
| `TestApply_SelfTestFailsKeepsOldBinary` (fake binary in a temp dir) | self-test |
| `TestApply_BackupFailureAborts` | backup gate |
| `TestApply_SwapAtomicAndOldKept` | swap |
| `TestBoot_RollsBackAfterMaxAttempts`, `TestBoot_ConfirmAfterReady` (synctest) | rollback/confirm |
| `TestBoot_CorruptMarker` | marker safety |
| `TestPolicy_ContainerForcesOff`, `TestPolicy_NoKeysCheckOnly`, `TestPolicy_NilOptionsNoRoutesNoTasks` | policy, absence |
| `TestApply_OnlyPathIsWindowTask` (no other caller of `Apply`; AST) | single apply path |
| `TestNoGlobals_Updater` (R5 AST) | no globals |

## 5U.12 Migration from ABO

| ABO today | Becomes |
|---|---|
| `internal/updater.Updater` (`updater.go`) | `core/updater` + `core/updater/github`; ABO's `main` passes its slug, binary name, asset namer and public key |
| `internal/updater.Scheduler` + `SchedulerStarterAdapter` + `updatescheduler` service | deleted; `core.updater.check`/`apply` tasks with schedules |
| maintenance-window step 1 (`scheduler_maintenance_window_op.go:73-92`) and setting `maintenance_window_update_completed` | deleted; the window runs `core.updater.apply` as member 0 |
| `RestartSelf` (`os.Exit(0)`) | `Restarter.Restart` (graceful) |
| `/api/v1/update/*` handlers (`update_handlers.go`) | core routes + aliases |
| `auto_update.*` config | `core.updater.*` with aliases (5U.8) |
| release pipeline (reusable workflow, UNKNOWN details) | must add `checksums.txt.sig`; ABO's asset namer must match what the pipeline publishes — verify before enabling apply |

## 5U.13 Open items

1. Signing scheme: plain ed25519 over `checksums.txt` (stdlib, chosen here) vs. minisign format vs.
   Sigstore/cosign keyless (free, but verification needs online transparency-log access and large
   dependencies). Revisit only if the release workflow already produces one of the others.
2. Upgrade-path constraints ("must pass through version X for a migration"): a `requires_from`
   field in a release manifest; not needed until a release needs it.
3. Windows support for the swap (a running `.exe` cannot be renamed over on some setups): not a
   current deployment target; `exec` mode is Unix-only.
4. Confirm the asset naming the reusable release workflow actually publishes (UNKNOWN in this repo).

## Out-of-scope defects found

1. `internal/updater/scheduler.go:97` logs `slog.Info("Update available but outside update window (%02d:00-%02d:00, current hour %02d)")`
   with printf verbs and no arguments, so the window and hour are never shown.
2. If releases are produced by the in-repo goreleaser config, the current updater can never find an
   asset (`updater.go:319` expects a raw binary name; `.goreleaser.yml:42-49` publishes tar.gz
   archives), so auto-update would silently do nothing. UNKNOWN which pipeline publishes.
