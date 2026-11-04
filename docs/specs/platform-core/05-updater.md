<!-- file: docs/specs/platform-core/05-updater.md -->
<!-- version: 1.3.1 -->
<!-- guid: 87f3821c-d9b3-427d-b91f-dad6de16aef8 -->
<!-- last-edited: 2026-11-04 -->

# 05 — Updater (`core/updater`)

Evidence commit `748fd787a`. Raw inputs: [`raw/12-cross-cutting.md`](../../plans/platform-core/raw/12-cross-cutting.md) §6,
[`raw/14-build-ci.md`](../../plans/platform-core/raw/14-build-ci.md) (release pipeline),
[`raw/07-plugin-registry-sdk.md`](../../plans/platform-core/raw/07-plugin-registry-sdk.md) §1.3 rows 23–24.
Layer **L4** (03 R4 v1.2.0); imports `tasks` (check/apply are tasks), `health`, `events`, `config`,
`secrets`, `storage`, `integrations/providerhttp` (outbound HTTP), `integrations/notify` (the
update-found notification), `backup` — all L4 or lower; none of them imports `updater`, so no cycle.

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
	// LastUnsignedVersion is the last release published before signing began
	// (compiled in). The legacy unsigned path (5U.5) accepts only a release whose
	// version is <= it; empty = the unsigned path is closed (SEC2-23).
	LastUnsignedVersion string
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
	// MinUpgradeFrom is the oldest running version this release may be applied
	// over (R2-4, OPS2-11): the declared minimum-upgrade-from release of 09.
	// Empty = any. Signed with the rest, so it cannot be stripped.
	MinUpgradeFrom string `json:"min_upgrade_from,omitempty"`
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
	DisabledReason  string   // "off", "container", "no_trusted_keys", "dev_build", "binary_not_writable" (OPS2-4)
	// SkippedAfterRollback names the newest version that Boot rolled back; scheduled
	// apply skips every version <= it until a manual apply or a newer release (OPS2-6).
	SkippedAfterRollback string
	// SteppingStone names the release to install first when Latest's signed
	// MinUpgradeFrom is newer than Current (OPS2-11); apply refuses until then.
	SteppingStone string
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
	Notify      *notify.Service      // stages core.update.found.v1 notifications (outbox-backed, COR2-3)
	Store       storage.Keyspace     // c/updater/ (one key, 5U.4 Check)
	ReaderCheck ReaderCheck          // kernel-bound to the single compat/format guard (05-storage)
	StateDir    string               // <state-dir>/updates (opened with os.Root)
	Log         *slog.Logger
}

// ReaderCheck asks whether the binary at exe may open the store at dbPath as
// it is now: the decision scripts/storage_format_guard.py makes for
// `make rollback`, implemented once by 05-storage's guard (SIMP-18) over the
// sidecar files, so it needs no open store. The kernel binds it to
// core/internal/pebble.CheckReader (05-storage §5S.3.7) with the resolved state
// directory; dbPath is the marker's db_path. nil error = allowed;
// otherwise an error carrying one of CheckReader's fixed reason tokens:
// "level_newer", "format_newer" (pre-guard binary), "restore_pending",
// "sidecar_unreadable".
type ReaderCheck func(ctx context.Context, dbPath, exe string, opt ReaderCheckOptions) error

type ReaderCheckOptions struct {
	// IgnoreRestoreStagedBy: a staged restore whose staged_by_build (05-backup
	// §5B.3.4) equals this build ID does not count as restore_pending. Boot sets
	// it to the failing build, so a restore that build staged cannot block the
	// rollback away from it (OPS2-3); .prev then rejects that intent as foreign.
	IgnoreRestoreStagedBy string
}

type Restarter interface {
	// Restart runs the full shutdown sequence within the stop budget
	// (05-health 5H.4) and then exits/execs per core.updater.restart_mode.
	Restart(ctx context.Context, reason string) error
}

// Boot is boot phase P−1 of 06 §6.5.1 (OPS2-3): it runs FIRST in app.New,
// before P0a's restore swap, before the config file is read and before storage
// opens, so that a config-stage refusal or a failing migration cannot prevent
// rollback. Like P0a it resolves the state directory from flags, environment
// and app.Options.StateDir only, never from the config file or the DB, and it
// takes the DB path ReaderCheck needs from its own marker (db_path, recorded
// by the build that applied the update). It reads the file marker, counts boots, and after the marker's
// max_boot_attempts failed boots rolls back to <exe>.prev only if check allows
// it (OPS-6). On Unix a rollback execs the restored binary in place (same PID,
// same argv/env), so no supervisor start is spent (OPS2-18); Boot returns only
// when it continues, when exec fails, or on platforms without exec.
func Boot(stateDir, exe string, check ReaderCheck) (BootResult, error)

type BootResult struct {
	RolledBack      bool   // true only when exec was impossible: the previous binary was restored; the caller exits with the restart code
	RollbackBlocked string // non-empty: rollback refused by check; boot continues on the new binary
	// Pending is non-nil while this boot is an unconfirmed update. The kernel
	// passes it to storage open, which then runs no RaisesLevel step
	// (05-storage §5S.3.5 RequiresSelfServed; OPS2-2).
	Pending *Pending
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

**Check** — task `core.updater.check`: Shape Task, `Effects: Writes(tasks.Res("core","updater"))`,
`Resume: ResumeDrop`, `Perms.Start: core:system.manage`, `Priority: PrioritySystem`,
`Schedule: tasks.ScheduleFromConfig("core.updater.schedules.check").When(mode >= ModeCheck).Live()` —
the one schedule mechanism on the durable clock, with the key in the updater's own namespace
([05-tasks](05-tasks.md) 5T.3.8; SIMP-6, R2-7; fixes the restart-reset ticker); the check route starts
it with `mode: live`. It calls `Source.Latest`, compares semver (`golang.org/x/mod/semver`), refuses
to report an update for a `dev`/unversioned build, probes that the executable's directory is writable
(apply step 1), records `Status`, and sets component `core.updater` (Up / Degraded `check_failed`,
`no_trusted_keys`, `unsigned_updates`, `rollback_blocked`, `binary_not_writable`). When a version first
appears it writes `c/updater/seen` `{version, at}` and, in the **same batch** (through the run's
`Writer`), stages the `core.update.found.v1` notification with `notify.Service.Publish`, so each new
version notifies exactly once across restarts; it also publishes the topic on the bus for UI refresh
(declared in the one topic catalog with `Notify`, [05-events](05-events.md) 5E.3.1; the bus publish
alone notifies nobody, COR2-3).

**Apply** — task `core.updater.apply`: Shape Task, `Effects: Writes(tasks.Res("core","binary"))`,
`Resume: ResumeDrop`, `ConcurrencyKey: core.updater`, `Cancel: Cooperative(30s)` until step 8,
`Perms.Start: core:admin`, `Priority: PrioritySystem`,
`Schedule: tasks.ScheduleFromConfig("core.updater.schedules.apply").When(mode == ModeApply).Live()`
(default `{enabled: true, window: "nightly", order: 0}`: the single automatic path; the
maintenance-window step 1 and the separate ticker are deleted). Because the scheduled run acts as the
system actor, `core.updater.mode` carries `ProtectAdmin` (5U.8, SEC-6): only a `core:admin` holder
with an interactive method can turn automatic apply on. Steps, each a `rc.Phase`:

1. Preconditions: not in a container; current build is versioned; target newer than current; for a
   **scheduled** run, target newer than `Status.SkippedAfterRollback` (OPS2-6); inside the window when
   scheduled; no staged restore pending (05-backup §5B.3.4's journal: a swap would make the restore be
   rejected as foreign, 09 S1/S8); either `TrustedKeys` non-empty or the legacy path enabled (5U.5);
   and the **executable's directory is writable by the process**, checked by creating and removing a
   probe file through an `os.Root` opened on that directory (OPS2-4). An unwritable directory sets
   `DisabledReason: "binary_not_writable"`, refuses apply and rollback, and keeps component
   `core.updater` Degraded `binary_not_writable` while mode ≥ check, so the operator learns that
   updates cannot be installed instead of the update silently failing at step 4 (5U.5 lists the
   supported layouts).
2. Download the archive, `release-manifest.json` and its `.sig` into an `os.Root` on
   `<state-dir>/updates/<version>/`, via `providerhttp` (throttle, retry, user agent
   `<AppID>/<version>`), each capped by `core.updater.max_download`.
3. Verify (SEC-27): the signature over the manifest's bytes with a trusted, non-revoked key named by
   the signature file; then `App == Options.AppID`, `Version` equal to the release being applied,
   `Channel` allowed by the configured channel (a stable install accepts only `stable`; a prerelease
   install accepts both); then the archive's SHA-256 against `Assets[name]`. Any mismatch: abort,
   delete the download, never fall back to the legacy path. Then the stepping-stone rule (R2-4,
   OPS2-11): if `MinUpgradeFrom` is set and the running version is older, abort with
   `stepping_stone_required`, set `Status.SteppingStone = MinUpgradeFrom` and a UI banner naming the
   release to install first; nothing is swapped.
4. Extract only the `BinaryName` member (regular file, no path components, size-capped) to
   `<exe>.new` in the executable's directory (same filesystem, so the rename is atomic); fsync
   file and directory.
5. Self-test (second check, kept): run `<exe>.new --version-json` (10 s timeout, empty environment
   except `PATH`); it must print `{app: AppID, version: target}`.
6. Pre-update backup through `backup.Runner` ([05-backup](05-backup.md), in the format the previous
   build can restore during the window); failure aborts unless `core.updater.require_backup=false`.
7. Write the pending marker file `<state-dir>/updates/pending.json`
   `{from, to, to_build, db_path, applied_at, boots: 0, max_boot_attempts}` (a file, not storage: `Boot` reads
   it before storage opens; `db_path` is the absolute `core.storage.path` this build serves, so `Boot`
   needs no config to locate the sidecars). `max_boot_attempts` is the key's value at apply time, capped at 3 when
   `NOTIFY_SOCKET` is set (OPS2-18, 5U.5).
8. Swap: rename `<exe>` → `<exe>.prev`, `<exe>.new` → `<exe>`; fsync directory. `.prev` is the same
   slot `make rollback` uses (`Makefile:892-913`), so mixed use of the two mechanisms never diverges
   (OPS-6). From here the task is uninterruptible (two renames).
9. `Restarter.Restart` — graceful shutdown within the stop budget, then exit with
   `core.updater.restart_exit_code` (default 75, so `Restart=on-failure` units restart; `0` needs
   `Restart=always`) or `exec` the new binary in place (`core.updater.restart_mode=exec`, Unix only).

**Confirm / roll back** — `Boot` is boot phase **P−1** of [06](06-app-sdk.md) §6.5.1 (OPS2-3): in the
new process, before P0a's restore swap, before the config file is read and before storage opens; it
resolves the state directory from flags, environment and `app.Options.StateDir` only, and reads the DB
path `ReaderCheck` needs from its own marker (`db_path`, the absolute `core.storage.path` the applying
build recorded), never from the config file or the DB (06 §6.5.1 P−1). It
increments `boots` in the marker **first**, so every boot that ends before confirmation counts as a
failed boot, whatever stopped it: a P0 config refusal (`*RetiredAliasError`, C3's YAML legacy-key
refusal, a validation error), a P2/P4 storage refusal (including `ErrUpgradeFloor`), or a crash during
warm-up. If `boots > max_boot_attempts` (the marker's copy, default 3), it first runs
`ReaderCheck(db_path, <exe>.prev, {IgnoreRestoreStagedBy: to_build})` (OPS-6): the same decision as the
`make rollback` guard, from the one compat/format guard (05-storage §5S.3.7, SIMP-18) plus the
unit-type capability check; a staged restore that the failing build itself staged does not block it,
and `.prev` then rejects that intent as foreign at its own P0a (05-backup §5B.3.4 step a). Rollback is
thus decided before any restore swap. If the previous binary would be refused — its level or format is
older than the store's, e.g. a `.prev` older than the Teach build once adoption ran (09, R2-1) —
`Boot` does **not** roll back: it records `rollback_blocked:<reason>` in the marker, stops counting
boots, reports Degraded `rollback_blocked`, and lets boot continue on the new binary, which is the
only one that can serve the store; an operator decides (restore the pre-update backup or fix
forward). Otherwise it restores `<exe>.prev` → `<exe>`, moves the failed binary to `<exe>.failed`,
records `rolled_back` in the marker, appends `{version: to, reason: "boot_attempts", at}` to
`updates/history.jsonl` (the input of the scheduled-apply skip, OPS2-6) and, on Unix, **execs** the
restored binary in place with the same argv and environment (same PID, so no supervisor start is
spent, OPS2-18); only where exec is unavailable or fails does it return `RolledBack: true` and `main`
exits with the restart code. Otherwise boot continues; once `/readyz` has been ready continuously for
`core.updater.confirm_after` (2 m), the updater deletes the marker (appending it to
`updates/history.jsonl`) and reports the update confirmed. `<exe>.prev` is kept until the next update
or deploy replaces it, so an admin can roll back manually (`POST .../rollback`, which runs the same
`ReaderCheck` first and refuses with its reason).

**No contract while an update is unconfirmed (OPS2-2).** A contract's `RequiresServed` is per level,
so on the first boot of an auto-applied contract release its preconditions may already hold; if it
ran there and the build then failed readiness, `ReaderCheck(.prev)` would refuse and `Boot` could only
record `rollback_blocked`. So the kernel passes `BootResult.Pending` to storage open, and no
`RaisesLevel` step runs on a boot where `pending.json` exists, nor before the running build has been
continuously ready for `max(core.updater.confirm_after, 1h)` (05-storage §5S.3.5
`RequiresSelfServed`). A contract therefore runs only on a later boot, after this update was
confirmed, while `Boot` and `make rollback` can still undo a broken contract release; health reports
`storage_contract_pending` with the earliest run time meanwhile. `rollback_blocked` remains as the
backstop for a `.prev` the guard refuses for another reason.

**Skip after rollback (OPS2-6).** A scheduled apply skips every version ≤ the newest version that
`updates/history.jsonl` records as rolled back, and `Status.SkippedAfterRollback` names it; without
this, the next window would download, back up, swap and fail the same release every night. A manual
apply of that version, or the appearance of a newer release, lifts the skip.

Database caveat: a rollback restores the **binary**, not the data. The previous release must read
the database the new release left — that is gate G5 and the compatibility window
([09-abo-on-core](09-abo-on-core.md) §9.2.1), checked by `ReaderCheck`; the pre-update backup is the
last resort, restored by an operator, never automatically (automatic restore would discard writes
made after the update). Persistence: one key, `c/updater/seen` (the last notified version); every
other updater state is a file under `<state-dir>/updates/`, because `Boot` reads it before storage
opens.

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
| Who | check: `core:system.manage`; apply/rollback: `core:admin` (audited as `core.update.apply` / `core.update.rollback`, 05-rbac §7.1; credential-guarded route as today); turning on scheduled apply: `ProtectAdmin` on `core.updater.mode` | RBAC (05-rbac); SEC-6 |
| Binary directory | the process must be able to create files in the executable's directory; otherwise `binary_not_writable` (apply and rollback refused, Degraded while mode ≥ check) | the swap is two renames in that directory (steps 4 and 8); OPS2-4 |
| Stepping stones | a release whose signed `min_upgrade_from` is newer than the running version is never applied; `stepping_stone_required` names the release to install first | R2-4: books legacy migration code is kept only until the declared minimum-upgrade-from release (09); adopt steps stay registered in every later build (05-storage), so no other stepping stone exists; OPS2-11 |
| Boot attempts | `max_boot_attempts ≤ StartLimitBurst − 2` of the unit; capped at 3 when `NOTIFY_SOCKET` is set | the committed unit (`StartLimitBurst=5` in 60 s, `RestartSec=5`, `deploy/audiobook-organizer.service:81-82,112`) then has one spare start for a crash of the restored binary; the in-place exec spends none; OPS2-18 |

**Supported install layouts (OPS2-4).** The updater replaces the binary in place, so it works only
where the service user owns the executable's directory: (a) a service-owned binary directory, e.g.
`<state-dir>/bin/<binary>`, referenced by the unit's `ExecStart` (and by `make deploy`/`make rollback`,
which then need no root for the copy); (b) a launchd agent whose binary lives in a user-writable
directory; (c) a manual install the operator owns. The committed systemd unit copies the binary into a
root-owned system binary directory with `sudo cp` and runs it as an unprivileged `User=` with
`NoNewPrivileges=yes` (`deploy/audiobook-organizer.service:42,88,122`), so HEAD's
`os.OpenFile(<exe>.new)` already fails there with EACCES (`internal/updater/updater.go:254-258`): on
that layout the core updater reports `binary_not_writable` until the owner moves the binary to layout
(a) (an owner action of 09 S8). A privileged helper is deliberately not provided: it would need a
setuid binary or a polkit rule, contradicting `NoNewPrivileges=yes`, for a convenience the
service-owned layout gives for free.

**Legacy unsigned path.** When `allow_unsigned_legacy` is true **and** the release publishes no
`release-manifest.json.sig` at all **and** its version is ≤ the compiled `Options.LastUnsignedVersion`
(the last release before signing began; SEC2-23), apply proceeds at today's trust level (HTTPS from the configured
release source) plus the SHA-256 in goreleaser's `checksums.txt` when it is published
(`.goreleaser.yml:55-56`). The status shows `Unsigned: true`, the component is Degraded
`unsigned_updates` and the UI shows a banner. The path is never taken when a signature asset exists
(a bad signature aborts), never for a release newer than `LastUnsignedVersion` (every such release is
signed, so a missing `.sig` there means a mispublished or attacker-published release: `verify_failed`),
never on a fresh install, and the flag is cleared (an audited system write
to the DB override layer) after the first successful apply of a signed release. Precondition for the
release that introduces `core/updater` (09 S8): a signed release is already published and the public
key is compiled in, so a migrated install's first update is normally already signed.

**Release pipeline requirements** (cost surfaced: the reusable release workflow lives outside this
repo, and signing is new work there): publish per-platform archives named by the app's `AssetName`,
`release-manifest.json` = `{app, version, channel, assets: {name: sha256}, min_upgrade_from?}` for that
app only (`min_upgrade_from` set from the release that declares the minimum-upgrade-from floor, 09), and
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
| Executable directory not writable | closed for apply/rollback (`binary_not_writable`); open for check (Degraded, banner) | OPS2-4: say so instead of failing at the swap |
| Signed `min_upgrade_from` newer than the running version | closed: apply refused (`stepping_stone_required`), nothing downloaded beyond the manifest is kept | R2-4 |
| Unsigned release newer than `LastUnsignedVersion` | closed: `verify_failed` | SEC2-23 |
| Scheduled apply of a version that rolled back here | open: skipped, `SkippedAfterRollback` set, no download | OPS2-6 |
| Staged restore pending | closed: apply and rollback refused (`restore_pending`) | 09 S1/S8; the swap would discard the restore |
| `exec` of the restored binary fails in `Boot` | open: `RolledBack: true`; `main` exits with the restart code | fallback to the supervisor restart |
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
| `core.updater.max_boot_attempts` | int | 3 (≤ `StartLimitBurst` − 2; capped at 3 when `NOTIFY_SOCKET` is set) | yes (copied into the marker at apply; `Boot` reads the copy) | — |
| `core.updater.restart_mode` | `exit`/`exec` | `exit` | yes | `ProtectAdmin` |
| `core.updater.restart_exit_code` | int | 75 | yes | `ProtectAdmin` |
| `core.updater.schedules.check` | `tasks.ScheduleConfig` | `{enabled: true, every: 1h}` (current 60 min) | yes | — |
| `core.updater.schedules.apply` | `tasks.ScheduleConfig` | `{enabled: true, window: "nightly", order: 0}` | yes | `ProtectAdmin` |

There is no `core.updater.check_interval`: the interval is the schedule key (SIMP-6).

ABO compatibility: `auto_update.enabled=true` maps to `mode=apply` **and** sets
`allow_unsigned_legacy=true` (OPS-23); `auto_update.channel` maps to `channel` (`develop` →
`prerelease` with a WARN); `auto_update.check_minutes` maps to `core.updater.schedules.check.every`;
`auto_update.window_start/end` map to `core.tasks.windows.nightly` only if the operator set them
explicitly, otherwise the maintenance window wins (one window). Aliases live for the compatibility
window ([09](09-abo-on-core.md) §9.2.1; [05-config](05-config.md) owns the alias mechanism).

## 5U.9 Metrics

| Name | Type | Labels |
|---|---|---|
| `mediacore_updater_checks_total` | counter | `outcome` (`ok`, `failed`, `no_asset`) |
| `mediacore_updater_update_available` | gauge (0/1) | — |
| `mediacore_updater_apply_total` | counter | `outcome` (`applied`, `applied_unsigned`, `verify_failed`, `selftest_failed`, `backup_failed`, `swap_failed`, `refused`, `stepping_stone_required`, `skipped_after_rollback`, `binary_not_writable`) |
| `mediacore_updater_rollbacks_total` | counter | `trigger` (`boot_attempts`, `manual`), `outcome` (`done`, `blocked`) |
| `mediacore_updater_download_bytes_total` | counter | — |

Alert snippet (shipped with the deploy alert rules, OPS2-6): an automatic rollback happened —
`increase(mediacore_updater_rollbacks_total{trigger="boot_attempts",outcome="done"}[1d]) > 0` — and a
rollback was refused — `mediacore_updater_rollbacks_total{outcome="blocked"}` increasing.

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
| `TestBoot_ConfigRefusalCountsAsFailedBoot` (P0 `*RetiredAliasError` on every boot → rollback after the attempts), `TestBoot_RunsBeforeRestoreSwap_IgnoresOwnStagedRestore` | OPS2-3 |
| `TestBoot_NoContractWhilePending` (contract release, first boot fails readiness → `Boot` rollback allowed; R-test cell d') | OPS2-2 |
| `TestBoot_RollbackExecsInPlace` (fake supervisor counts starts: ≤ `StartLimitBurst` − 2 + 1), `TestConfig_MaxBootAttemptsCappedUnderNotify` | OPS2-18 |
| `TestApply_SkipsVersionThatRolledBack`, `TestApply_ManualApplyLiftsSkip` | OPS2-6 |
| `TestApply_BinaryDirNotWritableRefusedAndDegraded` (read-only temp directory) | OPS2-4 |
| `TestApply_SteppingStoneRequired` (signed `min_upgrade_from` > current) | OPS2-11 |
| `TestApply_UnsignedNewerThanLastUnsignedRefused` | SEC2-23 |
| `TestCheck_NotifiesOncePerVersionAcrossRestart` | COR2-3 |
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
| `RestartSelf` (`os.Exit(0)`, `updater.go:329-334`) | `Restarter.Restart` (graceful, exit 75). Until then, 09 S0 owns the HEAD updater hotfix (exit 75, `.prev` kept) and its scheduled-apply policy until S8 (OPS-6, OPS2-5); on the committed unit's layout the HEAD updater cannot write the binary at all (5U.5, OPS2-4) |
| `<exe>.old`, deleted after the swap (`updater.go:267-280`) | `<exe>.prev`, kept; shared with `make rollback` |
| `/api/v1/update/*` handlers (`update_handlers.go`) | core routes + aliases |
| `auto_update.*` config | `core.updater.*` and `core.updater.schedules.*` with aliases (5U.8) |
| release pipeline (reusable workflow, UNKNOWN details) | must add the signed `release-manifest.json` per app before the S8 release; ABO's asset namer must match what the pipeline publishes — verify before enabling apply |

## 5U.13 Open items

1. Signing scheme: plain ed25519 over the release manifest (stdlib, chosen here) vs. minisign format
   vs. Sigstore/cosign keyless (free, but verification needs online transparency-log access and large
   dependencies). Revisit only if the release workflow already produces one of the others.
2. (Closed, OPS2-11/R2-4: upgrade-path constraints are the signed `min_upgrade_from` field, 5U.3–5U.4;
   adopt steps stay registered forever, so the only stepping stone is 09's declared
   minimum-upgrade-from release. A binary reached without the updater still refuses the store at boot
   with `ErrUpgradeFloor`, and `Boot` rolls back when the update came through it.)
3. Windows support for the swap (a running `.exe` cannot be renamed over on some setups): not a
   current deployment target; `exec` mode is Unix-only.
4. Confirm the asset naming the reusable release workflow actually publishes (UNKNOWN in this repo).
5. [06-app-sdk](06-app-sdk.md) `app.Options` has `Release updater.Source`; this file needs
   `Updater *updater.Options` (per-app keys and manifest names do not fit in a `Source`).
6. 05-storage must expose the single guard's decision as a function the kernel binds to
   `ReaderCheck` (SIMP-18), readable before the store opens, with the `IgnoreRestoreStagedBy` option
   (OPS2-3), and must accept `BootResult.Pending` at open to defer `RaisesLevel` steps
   (`RequiresSelfServed`, OPS2-2).
7. [06](06-app-sdk.md) §6.5.1 must list `updater.Boot` as phase P−1 before P0a (OPS2-3), and
   `Deps.Notify`/`Deps.Store` here assume the kernel builds `notify` before the updater.

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
