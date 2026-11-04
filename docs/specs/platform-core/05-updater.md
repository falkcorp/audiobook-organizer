<!-- file: docs/specs/platform-core/05-updater.md -->
<!-- version: 1.9.1 -->
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
	// MinUpgradeFrom is the release's upgrade-floor requirement: what the STORE
	// must already have recorded before this release may run on it (R2-4,
	// OPS2-11, OPS3-7), i.e. the floors the release declares (05-storage
	// §5S.3.5). It is set by a floor release (05-tasks T8) and names that
	// release's stepping stone; nil = none. Signed with the rest,
	// so it cannot be stripped. It is checked against the store's recorded
	// migration state, never against the running binary's version: a store
	// meets a floor only by running the steps, not because a binary that
	// carries them passed through (an install can upgrade to the named release
	// and move on before that release ran anything).
	MinUpgradeFrom *UpgradeRequirement `json:"min_upgrade_from,omitempty"`
}

// UpgradeRequirement is the release pipeline's copy of the new binary's
// declared upgrade floors, printed by `<exe> --print-upgrade-floor` (5U.5).
// Two releases are involved and never share a name (OPS4-5): the FLOOR
// release is the one carrying this requirement (it deleted code an older
// store needs); the STEPPING-STONE release, named in Release, still carries
// that code and contains no deletion.
type UpgradeRequirement struct {
	Release string            `json:"release"`          // the stepping-stone release to install and run first; named in messages and the banner
	Owners  map[string]uint64 `json:"owners,omitempty"` // owner -> minimum applied version, e.g. {"books": LegacyCap}
	// A Steps member (storage StepRefs that must be done) lands with the first
	// cutover; it is a deferred-build member (5U.3), not part of the extraction.
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
	// LocalBuild: the current version carries a `git describe` suffix (OPS4-17);
	// Latest is not compared with it and scheduled apply never runs.
	LocalBuild bool
	// SkippedAfterRollback names the newest version that Boot rolled back; scheduled
	// apply skips every version <= it until a manual apply or a newer release (OPS2-6).
	SkippedAfterRollback string
	// SteppingStone names the stepping-stone release to install and run first
	// when the store does not yet meet Latest's signed MinUpgradeFrom (OPS2-11,
	// OPS3-7); apply refuses until the store's recorded state meets it.
	SteppingStone string
}

type Pending struct {
	From, To  string
	AppliedAt time.Time
	Boots     int
	// RestartExitCode is the marker's copy of core.lifecycle.restart_exit_code
	// (06 §6.5.2; OPS8-12) at apply; while Pending is non-nil the kernel exits
	// a boot refusal with it instead of the floor release's 78 (5U.4, OPS5-15).
	RestartExitCode int
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
// directory; dbPath is the marker's db_path. nil error = allowed;
// otherwise an error carrying one of CheckReader's fixed reason tokens
// (05-storage §5S.3.7): "level_newer", "format_newer" (pre-guard binary),
// "upgrade_floor" (the store has not applied what exe's declared floor
// requires; a DB path with no directory, or an empty one, and no sidecars
// passes, the exemption boot gives an empty store, DATA8-11),
// "restore_pending" (restore/pending.json exists: a staged swap of any kind,
// a restore or a factory reset, KindReset; R8-1, SIMP8-1),
// "restore_inflight" (a swap journal restore/<id>/ holds inflight.json, which
// a KindReset swap keeps from the set-aside until the post-reset hook
// journals hook_done, or, for a checkpoint restore, inflight-checkpoint.json;
// DATA3-6, R7-2), "sidecar_unreadable", "cannot_determine" (a probe could not
// answer; refuses). The error message names the swap's kind. There is no
// reset-only token: a factory reset is a kind of the one swap (R8-1). One
// journal passes: a KindCheckpoint journal at swapped or failed (its
// restore/<id>/inflight-checkpoint.json; the offline restore-checkpoint path)
// does not refuse a candidate exe whose levels and floors admit the restored
// store AND which implements the journaled swap, detected by the S2b fast exit
// `--print-restore-journal`; a candidate without it (a pre-S2b build) is
// restore_inflight. So `make rollback` after a checkpoint restore reaches the
// previous binary (R7-4, DATA7-12, DATA8-6); every other started swap
// refuses. The advisories slice carries tokens that never refuse, read from
// <state_dir>/operator-names.json (names only, never values; written by every
// boot from S3): "canonical_only_operator_values" (an operator value is set
// only under a canonical name that an older build does not read; R8-4,
// SEC8-6) and "config_narrowing_owed" (a declared narrowing is owed and
// uncommitted; SEC8-9). The updater logs each before every swap it makes
// (5U.4). The scripts' one override, ROLLBACK_IGNORE_RESTORE=1, belongs to
// scripts/storage_format_guard.py (make rollback, deploy) and overrides
// restore_pending only, never restore_inflight (OPS8-7); the updater has no
// override and refuses.
type ReaderCheck func(ctx context.Context, dbPath, exe string, opt ReaderCheckOptions) (advisories []string, err error)

// StoreState reports the store's recorded migration state — the applied
// version per owner — by reading the
// applied-steps sidecar that 05-storage defines and every guard-aware boot
// rewrites beside the DB (a file, like the level sidecar ReaderCheck reads,
// so the same answer is available to tools outside the process, e.g. 09's
// P-command, and needs no open store). Never the binary version (OPS3-7).
// A missing or unreadable sidecar is an error (fail-closed). It is consulted
// only against a NON-EMPTY floor: an empty --print-upgrade-floor (and a
// manifest without MinUpgradeFrom) passes without reading the sidecar, so a
// HEAD store, which has none until an S0+ boot writes it, never blocks an
// update whose binary declares no floor; a non-empty floor with a missing
// sidecar refuses with "boot any release >= S0 once" (OPS7-10). The one
// exception is an empty store: no directory at dbPath, or an empty one, and
// no sidecars. StoreState then returns StoreApplied{Empty: true}, which
// meets every requirement, the exemption boot gives an empty store
// (05-storage §5S.4 step 3), so a new host or one prepared for a restore can
// take a floor release (DATA8-11).
type StoreState func(ctx context.Context, dbPath string) (StoreApplied, error)

type StoreApplied struct {
	Empty  bool // no DB directory (or an empty one) and no sidecars; Meets is true
	Owners map[string]uint64
	// StepsDone (finished data migrations) lands with the first cutover, as a
	// deferred-build member (5U.3; SIMP4-4).
}

// Meets reports whether s satisfies r: every listed owner at or above its
// version. A nil r, and any r on an Empty store, is always met.
func (s StoreApplied) Meets(r *UpgradeRequirement) bool

type ReaderCheckOptions struct {
	// IgnoreRestoreStagedBy: a staged swap of either kind, a restore or a
	// factory reset (KindReset), whose staged_by_build (05-backup §5B.3.4)
	// equals this binary ID (Self.BinaryID, never the UI bundle's build ID;
	// R5-7) and whose journal shows no swap or set-aside step yet does not
	// count as restore_pending. Boot sets it to the failing binary's ID (the
	// marker's to_build), so neither a restore nor a reset that binary staged
	// can block the rollback away from it (OPS2-3); .prev then rejects that
	// intent under the one rejection rule for every kind (05-backup §5B.3.4
	// step a: expires_at, staged_by_build, a moved open stamp; R8-1).
	IgnoreRestoreStagedBy string
}

type Restarter interface {
	// Restart runs the full shutdown sequence within the stop budget
	// (05-health 5H.4) and then exits/execs per core.updater.restart_mode.
	// The kernel binds it to Lifecycle.RequestRestart(reason) (06 §6.5.2),
	// the one restart path the staged restore and the staged factory reset
	// also call: in exit mode it exits with core.lifecycle.restart_exit_code
	// (default 75); in exec mode it execs <exe> after the same drain (OPS8-12).
	Restart(ctx context.Context, reason string) error
}

// Boot is boot phase P−1 of 06 §6.5.1 (OPS2-3): it runs FIRST in app.New,
// before P0a's restore swap, before the config file is read and before storage
// opens, so that a config-stage refusal or a failing migration cannot prevent
// rollback. stateDir is the directory the kernel resolved ONCE before P−1
// (05-config §1.7: flags, environment and app.Options.StateDir only, never the
// config file or the DB), the same value P0a, P0 and every consumer use; Boot
// resolves nothing itself. The marker inside it supplies only db_path, the DB
// path ReaderCheck needs (recorded by the build that applied the update), and
// is never an input to locating a directory (OPS4-8). A marker whose to and
// to_build are not self's was left for another binary (make deploy or make
// rollback inside the confirmation window, or a rollback to a build that never
// deletes markers): Boot appends it to updates/history.jsonl as "superseded",
// deletes it and counts nothing (OPS4-9). Otherwise it counts boots, and after
// the marker's max_boot_attempts failed boots rolls back to <exe>.prev only if
// check allows it (OPS-6). On Unix a rollback execs the restored binary in
// place (same PID, same argv/env), so no supervisor start is spent (OPS2-18);
// Boot returns only when it continues, when exec fails, or on platforms
// without exec.
func Boot(stateDir, exe string, self Self, check ReaderCheck) (BootResult, error)

// Self identifies the running binary: its version and its BINARY ID, the
// values its own --version-json prints as version and binary_id (R5-7, FE5-3).
// The binary ID is computed once per process from the version and the SHA-256
// of the running executable (os.Executable, read once) by one function,
// buildid.Binary in the dependency-free L0 leaf core/buildid (03 §3.3), which
// lands at S0, so ABO's S0 guard and --version-json record and print the same
// value before the kernel exists, and that app.BinaryID delegates to when the
// kernel computes it before P−1 (06 §6.4.5, §6.5.1; FE6-3). It needs no ldflags and no .git, so every
// build path yields one, and two binaries differ in it whenever their bytes
// differ. It is opaque and compared only for equality. It is NEVER the UI
// bundle's build ID (07 §7.6.3; the value X-App-Build reports and
// --version-json prints as ui_build_id: the embedded bundle's meta, legacy
// or shell), which is "dev" for every API-only build, so it cannot tell two
// such binaries apart; /api/core/v1/modules' ui_build_id is a different
// value, the shell bundle's only ("" without one; FE7-13). The same value is the binary identity
// storage and backup record (05-storage's served ledger build and self-served
// record, 05-backup's StagedByBuild).
type Self struct{ Version, BinaryID string }

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

### Deferred-build members

Built only with the first per-family cutover (05-storage §5S.3.13), the only step whose floor needs
them; no extraction stage builds, stamps or reads them (SIMP4-4). The first column holds only
package-qualified Go identifiers that must not exist in non-test files under `core/`, as 03 §3.4's
generator reads it (SIMP5-6, COR5-16); everything else deferred with them is in the column headed
`Also deferred (not checked)`, which 03's grammar accepts in any position (SIMP6-8).

| Identifier | What it adds | Also deferred (not checked) |
|---|---|---|
| `updater.UpgradeRequirement.Steps` | storage `StepRef`s (a cutover's data migration) that must be done before the release may run | its manifest field `json:"steps"` in `min_upgrade_from` |
| `updater.StoreApplied.StepsDone` | the finished data migrations, read from the applied-steps sidecar | the sidecar's `<owner> done <id>` lines (05-storage, deferred with them); `StoreApplied.Meets` (built from S8) then also checks `Steps` against `StepsDone`: a behaviour of a built method, not an identifier, so it is listed here only |

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

1. Preconditions: not in a container; current build is versioned; target newer than current, except
   that on a local build (`LocalBuild`) a scheduled run never applies and a manual run applies only the
   version the operator names explicitly; for a **scheduled** run, target newer than `Status.SkippedAfterRollback` (OPS2-6); inside the window when
   scheduled; no swap staged and none in flight, of either kind (`restore_pending`, `restore_inflight`;
   05-backup §5B.3.4's one slot and journal, in which a factory reset is the kind `KindReset`, 05-storage
   §5S.3.15; R8-1): a swap would make a staged restore be rejected as foreign (09 S1/S8), turn the admin's
   audited reset into a rejected intent, or leave a half-done set-aside to a binary that cannot finish it
   (R7-2, OPS7-11). It is read through `ReaderCheck` on `<exe>` with no override (a `KindCheckpoint`
   journal at `swapped` or `failed` passes for a candidate that admits the restored store and implements
   the journaled swap, R7-4, DATA8-6); either `TrustedKeys` non-empty or the legacy path enabled (5U.5);
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
   OPS2-11, OPS3-7): if `MinUpgradeFrom` is set (a non-empty floor) and `StoreState(core.storage.path)` —
   the applied-steps sidecar of 05-storage, or `Empty` for a store with no directory and no sidecars,
   which meets every floor (DATA8-11) — does not `Meet` it, abort with
   `stepping_stone_required`, set `Status.SteppingStone = MinUpgradeFrom.Release` and a UI banner
   ("install <release> and run it until the store has applied what this release needs"); nothing is
   swapped. A `StoreState` error refuses the same way (fail-closed). This is why the updater can never
   apply a release that would only refuse the store at boot with `ErrUpgradeFloor` and loop into
   `Boot`'s rollback.
4. Extract only the `BinaryName` member (regular file, no path components, size-capped) to
   `<exe>.new` in the executable's directory (same filesystem, so the rename is atomic); fsync
   file and directory.
5. Self-test (second check, kept): run `<exe>.new --version-json` (10 s timeout, empty environment
   except `PATH`). It prints one JSON object with exactly the fields `{app, version, commit,
   ui_build_id, binary_id}`, the list 09 S0 and §9.6 row 20 pin (FE6-4); `commit` is the build's
   `vcs.revision` (suffixed `+dirty` when `vcs.modified`) or `""` when the build recorded none, and is
   informational here. The self-test requires `app == AppID`, `version == target` and a non-empty
   `binary_id`; that `binary_id` (the binary ID it computes over its own bytes, which the step-8 rename
   does not change; never its `ui_build_id`, R5-7) is what step 7 records as `to_build`. It then runs
   `<exe>.new --print-upgrade-floor` and refuses with `stepping_stone_required` unless that output is empty
   (no floor: the sidecar is not read, OPS7-10) or `StoreState` `Meets` the floor the binary itself
   enforces at boot — the check the 09 S0 hotfix makes (OPS6-1), kept
   beside step 3's signed copy so a release whose manifest omits or understates the floor is still
   never swapped in.
6. Pre-update backup through `backup.Runner` ([05-backup](05-backup.md), in the format the previous
   build can restore during the window); failure aborts unless `core.updater.require_backup=false`.
7. Write the pending marker file `<state-dir>/updates/pending.json`
   `{from, to, to_build, db_path, applied_at, boots: 0, max_boot_attempts, restart_exit_code}` (a file, not storage: `Boot` reads
   it before storage opens; `db_path` is the absolute `core.storage.path` this build serves, so `Boot`
   needs no config to locate the sidecars). `max_boot_attempts` is the key's value at apply time, capped at 3 when
   `NOTIFY_SOCKET` is set (OPS2-18, 5U.5); `restart_exit_code` is `core.lifecycle.restart_exit_code`'s value
   at apply (06 §6.5.2, OPS8-12). The marker code is listed in `ci/state_writers.txt`, so a PR that changes
   it is a state-writing PR under 09 §9.2.1's red-rehearsal rule (OPS8-9).
8. Swap: first log each advisory token `ReaderCheck` returned (`canonical_only_operator_values`,
   `config_narrowing_owed`) at WARN and into the run's log, as `make rollback` prints them (R8-4, SEC8-6,
   SEC8-9); then rename `<exe>` → `<exe>.prev`, `<exe>.new` → `<exe>`; fsync directory. `.prev` is the same
   slot `make rollback` uses (`Makefile:892-913`), so mixed use of the two mechanisms never diverges
   (OPS-6). From here the task is uninterruptible (two renames).
9. `Restarter.Restart`, bound to the kernel's `Lifecycle.RequestRestart` (06 §6.5.2), the one restart
   path that a staged restore and a staged factory reset also use (OPS8-12) — graceful shutdown within the
   stop budget, then exit with `core.lifecycle.restart_exit_code` (default 75, so the committed unit's
   `Restart=on-failure`, `deploy/audiobook-organizer.service:111`, restarts it; `0` needs
   `Restart=always`) or `exec` the new binary in place (`core.updater.restart_mode=exec`, Unix only).

**Confirm / roll back** — `Boot` is boot phase **P−1** of [06](06-app-sdk.md) §6.5.1 (OPS2-3): in the
new process, before P0a's restore swap, before the config file is read and before storage opens. It
works in the state directory the kernel resolved once before P−1 (05-config §1.7) and resolves none
itself; the marker's `db_path` (the absolute `core.storage.path` the applying build recorded) is used
only as `ReaderCheck`'s DB path, never to locate a directory (06 §6.5.1 P−1; OPS4-8). It first checks
that the marker is this binary's: when the marker's `to`/`to_build` differ from the running binary's
version and binary ID (`Self`, R5-7; the case of a `make deploy` or `make rollback` inside the
confirmation window, or of a rollback to a pre-S8 build, which never deletes the marker), it appends the marker to `updates/history.jsonl` as
`superseded`, deletes it and counts nothing, so a different binary is never rolled back against an old
update (OPS4-9). Otherwise it
increments `boots` in the marker **first**, so every boot that ends before confirmation counts as a
failed boot, whatever stopped it: a P0 config refusal (`*RetiredAliasError`, a validation error), a
P2/P4 storage refusal (including `ErrUpgradeFloor`), or a crash during
warm-up. If `boots > max_boot_attempts` (the marker's copy, default 3), it first runs
`ReaderCheck(db_path, <exe>.prev, {IgnoreRestoreStagedBy: to_build})` (OPS-6): the same decision as the
`make rollback` guard, from the one compat/format guard (05-storage §5S.3.7, SIMP-18) plus the
unit-type capability check; a staged swap of either kind, a restore or a factory reset, that the failing
build itself staged does not block it, and `.prev` then rejects that intent at its own P0a under the one
rejection rule for every kind (its `staged_by_build` is not `.prev`'s; 05-backup §5B.3.4 step a; R8-1). A
swap of either kind staged by another build, or one in flight, blocks it (`rollback_blocked:restore_pending`,
`rollback_blocked:restore_inflight`). Rollback is thus decided before any restore swap. If the previous binary would be refused — its level or format is
older than the store's, e.g. a `.prev` older than a release whose own migration raised the store's
level — `Boot` does **not** roll back: it records `rollback_blocked:<reason>` in the marker, stops counting
boots, reports Degraded `rollback_blocked`, and lets boot continue on the new binary, which is the
only one that can serve the store; an operator decides (restore the pre-update backup or fix
forward). Otherwise it logs `ReaderCheck`'s advisory tokens at WARN (as apply step 8 does), restores
`<exe>.prev` → `<exe>`, moves the failed binary to `<exe>.failed`,
records `rolled_back` in the marker, appends `{version: to, reason: "boot_attempts", at}` to
`updates/history.jsonl` (the input of the scheduled-apply skip, OPS2-6) and, on Unix, **execs** the
restored binary in place with the same argv and environment (same PID, so no supervisor start is
spent, OPS2-18); only where exec is unavailable or fails does it return `RolledBack: true` and `main`
exits with the restart code. Otherwise boot continues; once `/readyz` has been ready continuously for
`core.updater.confirm_after` (2 m), the updater deletes the marker (appending it to
`updates/history.jsonl`) and reports the update confirmed. `<exe>.prev` is kept until the next update
or deploy replaces it, so an admin can roll back manually (`POST .../rollback`, which runs the same
`ReaderCheck` first, refuses with its reason, and otherwise logs its advisory tokens into the run's log
before the swap).

**Failed boots keep the rollback evidence (R5-5).** The boots `Boot` counts as failed are exactly the
boots that end after storage opened and before the build was ready. None of them erases what the
store records about the previous binary: 05-storage's served ledger keeps the previous boot's record
and replaces it only after every boot step that reads it has committed (§5S.3.7), so a new binary
that fails at P4d and is rolled back by `Boot` leaves the duty and foreign-open evidence for the next
boot intact, and a roll-forward after it still appends the journal `gap` line and revokes the
pre-rollback sessions (05-backup, 05-authn). Both records name the binary by its binary ID (`Self.BinaryID`).

**Floor refusals under a restart-preventing unit (OPS5-15).** `Boot`'s count relies on the supervisor
restarting a refused boot. A unit that stops on the floor release's distinct exit code
(`RestartPreventExitStatus=78`, 09 §9.2.2) would leave an updater-applied release that refuses the
store with `ErrUpgradeFloor` stopped instead of rolled back. So while `pending.json` names the running
binary (`to`/`to_build` = `Self`, so `BootResult.Pending` is non-nil), the kernel exits a boot refusal
with the marker's `restart_exit_code` (`Pending.RestartExitCode`, default 75; the apply-time copy, as for
`max_boot_attempts`) instead of 78, and `Boot` reaches `max_boot_attempts` and rolls
back as for any other refusal; without a marker naming it, 78 stands (`TestBoot_FloorRefusalUnderPendingRestarts`).
Steps 3 and 5's stepping-stone checks make this path rare; it is the backstop when `StoreState` and the
boot disagree. Under launchd a 78 exit is relaunched like any unsuccessful exit (throttled; the plist has
no per-exit-code setting), so there `Boot` counts the boots whatever the code, and without a marker the
refusal repeats until the operator unloads the job, as 09 §9.2.2's release note says (OPS6-5).

**No level raise while an update is unconfirmed (OPS2-2).** A `RaisesLevel` step (a books format
change, or a later per-family cutover; the extraction itself has none, R3-1) may already have its
preconditions on the first boot of an auto-applied release; if it ran there and the build then failed
readiness, `ReaderCheck(.prev)` would refuse and `Boot` could only record `rollback_blocked`. So the
kernel passes `BootResult.Pending` to storage open, and no `RaisesLevel` step runs on a boot where
`pending.json` exists, nor before the running build has been continuously ready for
`max(core.updater.confirm_after, 1h)` (05-storage §5S.3.5 `RequiresSelfServed`). Such a step therefore
runs only on a later boot, after this update was confirmed, while `Boot` and `make rollback` can still
undo a broken release; health reports the pending step with its earliest run time meanwhile.
`rollback_blocked` remains as the backstop for a `.prev` the guard refuses for another reason.

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
| Downgrades | never automatic; only `Rollback` to the kept previous binary, and only when `ReaderCheck` allows it; a describe-suffixed local build is never compared, so it is never offered an older release as newer | string-inequality defect (`updater.go:153`); OPS-6; semver orders `v0.223.0-5-gabc1234` below `v0.223.0` (OPS4-17) |
| Window | apply only inside the window its schedule key names (`nightly` by default) when scheduled; manual apply any time | one window instead of two (5U.2) |
| Who | check: `core:system.manage`; apply/rollback: `core:admin` (audited as `core.update.apply` / `core.update.rollback`, 05-rbac §7.1; credential-guarded route as today); turning on scheduled apply: `ProtectAdmin` on `core.updater.mode` | RBAC (05-rbac); SEC-6 |
| Binary directory | the process must be able to create files in the executable's directory; otherwise `binary_not_writable` (apply and rollback refused, Degraded while mode ≥ check) | the swap is two renames in that directory (steps 4 and 8); OPS2-4 |
| Stepping stones | a release whose upgrade floor the store's **recorded state** does not meet is never applied; `stepping_stone_required` names the stepping-stone release to install and run first. The 09 S0 hotfix checks the new binary's own floor (`<exe>.new --print-upgrade-floor` against `<db>.storage-applied`, before the swap), which needs no signed manifest and so covers the floor release, which ships at S5, before any signed release exists; this updater checks that same floor (step 5) and also the signed `min_upgrade_from` (step 3); the `deploy` recipe runs the S0 pair before it copies a binary (09 S0; OPS6-1). This protection exists only where the running updater is this one or the 09 S0 hotfix; a pre-S0 updater swaps any newer binary unchecked (`internal/updater/updater.go:214-280`), so such an auto-updating install that takes the floor release exits 78 at boot with `ErrUpgradeFloor` and no automatic way back, and the floor release's note and the Docker last-line message name the stepping stone (DATA4-18) | R2-4, OPS4-5: books legacy migration code is kept in every release before the **floor release** (the first T8 release, which deletes it and declares books' floor, 05-tasks 5T.6.2), so a store below books' `LegacyCap` needs the **stepping-stone release** (`min_upgrade_from.release`, which still carries that code) to have **run**, which only the store can prove (OPS3-7); a later cutover floor uses the same field; OPS2-11 |
| Boot attempts | `max_boot_attempts ≤ StartLimitBurst − 2` of the unit; capped at 3 when `NOTIFY_SOCKET` is set | the committed unit (`StartLimitBurst=5` in 60 s, `RestartSec=5`, `deploy/audiobook-organizer.service:81-82,112`) then has one spare start for a crash of the restored binary; the in-place exec spends none; OPS2-18 |

**Supported install layouts (OPS2-4, OPS4-4).** The updater replaces the binary in place, so it works
only where the service user owns the executable's directory: (a) a dedicated service-owned binary
directory **outside** the state directory — for example a service-owned `bin` directory under the
install prefix — mode 0755 with binaries 0755, referenced by the unit's `ExecStart`. It must not be
under the state directory: that directory is 0700 and holds credentials
(`internal/config/state_dir.go:112-116,135`), while `make rollback` and `deploy-match-unit-type.sh` run
their `.prev` probes (`--print-storage-format`, `--print-storage-levels`, `--print-upgrade-floor`,
`--print-capabilities`) as the unprivileged deploy user (`Makefile:899-902`), which could not enter it,
so every rollback would report "no .prev found". The recipes keep those unprivileged probes and copy
the binary with literal sudoers lines (`cp` as the service user or as root); (b) a launchd agent whose binary lives in a user-writable
directory; (c) a manual install the operator owns. The committed systemd unit copies the binary into a
root-owned system binary directory with `sudo cp` and runs it as an unprivileged `User=` with
`NoNewPrivileges=yes` (`deploy/audiobook-organizer.service:42,88,122`), so HEAD's
`os.OpenFile(<exe>.new)` already fails there with EACCES (`internal/updater/updater.go:254-258`): on
that layout the core updater reports `binary_not_writable` until the owner moves the binary to layout
(a) (an owner action of 09 S8, which owns its checklist: the committed unit with layout (a) as default,
the drop-in `ExecStart`, the sudoers template's `cp` lines, `DEPLOY_BIN` and
`deploy-match-unit-type.sh` updated, the launchd plist's `ProgramArguments` binary path
(`deploy/com.audiobook-organizer.plist:30-33`, which may name a root-owned system directory), **both**
the binary and `.prev` copied to the new directory so the first `make rollback` still finds a `.prev`,
and, as the check, a dry-run `make rollback` **run as the deploy user** that prints the `.prev` levels,
plus `binary_not_writable` clearing; OPS3-15, OPS4-4). A
privileged helper is deliberately not provided: it would need a
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
app only (`min_upgrade_from` = `{release, owners}`, copied from the built binary's
`--print-upgrade-floor` output, so the signed requirement is exactly the floor that binary enforces at
boot; `release` is the stepping-stone release, which 05-tasks T8 fixes as the newest production tag
when the T8 PR merges), and
`release-manifest.json.sig` = ed25519 signature over the manifest bytes with **that app's** key (one
key per app; never shared between ABO and a TV/Movies app), key id in the signature file; the private
key is a CI secret. `checksums.txt` stays (goreleaser produces it). The prerelease workflow
(`.github/workflows/prerelease.yml`) publishes the same signed `release-manifest.json` and `.sig` with
`channel: prerelease`, so two consecutive signed release candidates exist for 5U.12's S8 check (OPS8-3). Key rotation: ship the new public
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
| Store's recorded state does not meet the signed `min_upgrade_from` (step 3) or the new binary's own non-empty `--print-upgrade-floor` (step 5), or `StoreState` fails against a non-empty floor (a missing sidecar: "boot any release ≥ S0 once"); an empty floor passes without reading the sidecar, and so does an empty store (no DB directory, or an empty one, and no sidecars) | closed: apply refused (`stepping_stone_required`), the download and `<exe>.new` deleted, nothing swapped | R2-4, OPS3-7, OPS6-1, OPS7-10, DATA8-11 |
| Unsigned release newer than `LastUnsignedVersion` | closed: `verify_failed` | SEC2-23 |
| Scheduled apply of a version that rolled back here | open: skipped, `SkippedAfterRollback` set, no download | OPS2-6 |
| A swap staged (`restore/pending.json`: a restore, or a factory reset, `KindReset`) or in flight (a `restore/<id>/inflight.json`, which a `KindReset` swap keeps until `hook_done`), except a `KindCheckpoint` journal at `swapped` or `failed` whose restored store the candidate admits and whose journaled swap it implements (`--print-restore-journal`) | closed: apply and rollback refused (`restore_pending`, `restore_inflight`, the message naming the kind); in `Boot`, `rollback_blocked`, except an intent the failing build staged (`IgnoreRestoreStagedBy`), which `.prev` rejects under the one rejection rule; no updater override (the scripts' one override, `ROLLBACK_IGNORE_RESTORE=1`, covers `restore_pending` only, OPS8-7) | 09 S1/S8; a swap would discard the restore (DATA3-6), reject the audited reset or leave a half-done set-aside to a binary that cannot finish it (R7-2, R8-1); the checkpoint exception lets the documented failed-migration recovery reach `.prev` (R7-4, DATA7-12, DATA8-6) |
| `ReaderCheck` returns advisory tokens (`canonical_only_operator_values`, `config_narrowing_owed`) | open: never refuses; each is logged at WARN, and into the apply or rollback run's log, before the swap | an older build ignores a canonical-only operator value or an uncommitted narrowing, so the operator must see it before a swap (R8-4, SEC8-6, SEC8-9) |
| `ReaderCheck` cannot determine the answer (`cannot_determine`) | closed: apply and rollback refused | fail-closed, as `make rollback` (09 §9.2.2) |
| `exec` of the restored binary fails in `Boot` | open: `RolledBack: true`; `main` exits with the restart code | fallback to the supervisor restart |
| Restart fails after swap | closed: the process keeps running the old code in memory; marker says pending; next manual or supervisor restart boots the new binary under the boot counter | — |
| New binary fails to become ready within `max_boot_attempts` boots, `.prev` allowed by `ReaderCheck` | closed: automatic binary rollback in `Boot` | the purpose of the marker |
| Same, but `ReaderCheck` refuses `.prev` | open: no rollback; `rollback_blocked`, Degraded, boot continues on the new binary | rolling back to a binary that refuses the store leaves the unit failed with no automatic recovery (OPS-6) |
| Marker left for another binary (`to`/`to_build` ≠ the running binary's) | open: recorded `superseded` in `history.jsonl`, deleted, nothing counted | a deploy or rollback inside the confirmation window must not be rolled back against an old update (OPS4-9) |
| Current version is a local (describe-suffixed) build | open for check (`LocalBuild`, not compared); closed for scheduled apply; manual apply only of an explicitly named version | semver cannot order it against its base tag (OPS4-17) |
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
| `core.updater.schedules.check` | `tasks.ScheduleConfig` | `{enabled: true, every: 1h}` (current 60 min) | yes | — |
| `core.updater.schedules.apply` | `tasks.ScheduleConfig` | `{enabled: true, window: "nightly", order: 0}` | yes | `ProtectAdmin` |

There is no `core.updater.check_interval`: the interval is the schedule key (SIMP-6). There is no
`core.updater.restart_exit_code` either: the restart exit code is 06's `core.lifecycle.restart_exit_code`
(default 75, `ProtectAdmin`), which `Lifecycle.RequestRestart` uses for the updater, the staged restore and
the staged factory reset alike (06 §6.5.2, OPS8-12). The updater copies its value into the marker at apply,
and a boot refusal under a pending marker exits with the copy (5U.4). The old name never shipped (the
updater lands at S8), so it needs no alias.

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
| `TestCheck_SemverNeverDowngrades`, `TestCheck_DevBuildNeverAvailable`, `TestCheck_DescribeSuffixedCurrentNotCompared` (current `v0.223.0-5-gabc1234` or `-dirty`, latest `v0.223.0` or older → no update offered, `LocalBuild` set, scheduled apply refused, manual apply of a named version allowed) | comparison, OPS4-17 |
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
| `TestBoot_IgnoresMarkerOfAnotherBuild` (marker `to_build` ≠ the running build: appended to `history.jsonl` as `superseded`, deleted, no boot counted, no rollback) | OPS4-9 |
| `TestSelf_BinaryIDDistinguishesAPIOnlyBuilds` (two `make build-api` binaries of one version with different code: equal UI build ID `dev`, different binary IDs; a marker from the first is `superseded` on the second; a self-test's `to_build` is its `binary_id`, never its `ui_build_id`) | R5-7, FE5-3 |
| `TestBoot_FloorRefusalUnderPendingRestarts` (fake supervisor that does not restart exit 78: an applied release refusing with `ErrUpgradeFloor` exits 75 while the marker names it and `Boot` rolls back after the attempts; without a marker it exits 78) | OPS5-15 |
| `TestBoot_UsesKernelStateDirMarkerOnlyForDBPath` (a marker naming another `db_path` never changes the state directory; `ReaderCheck` receives that path) | OPS4-8 |
| `TestBoot_ConfigRefusalCountsAsFailedBoot` (P0 `*RetiredAliasError` on every boot → rollback after the attempts), `TestBoot_RunsBeforeRestoreSwap_IgnoresOwnStagedRestore` | OPS2-3 |
| `TestBoot_NoLevelRaiseWhilePending` (level-raising release, first boot fails readiness → `Boot` rollback allowed; R-test cell d') | OPS2-2 |
| `TestBoot_RollbackExecsInPlace` (fake supervisor counts starts: ≤ `StartLimitBurst` − 2 + 1), `TestConfig_MaxBootAttemptsCappedUnderNotify` | OPS2-18 |
| `TestApply_SkipsVersionThatRolledBack`, `TestApply_ManualApplyLiftsSkip` | OPS2-6 |
| `TestApply_BinaryDirNotWritableRefusedAndDegraded` (read-only temp directory) | OPS2-4 |
| `TestApply_SteppingStoneRequired` (store at books 63 with a running binary newer than the stepping-stone release → refused before any swap; store at books `LegacyCap` → allowed), `TestApply_StoreStateErrorRefuses`, `TestApply_NewBinaryFloorUnmetRefused` (a signed manifest with no `min_upgrade_from` and a fake new binary whose `--print-upgrade-floor` the sidecar does not meet → refused at step 5, nothing swapped) | OPS2-11, OPS3-7, OPS6-1 |
| `TestSelfTest_VersionJSONFieldList` (the new binary must print exactly `{app, version, commit, ui_build_id, binary_id}`; a missing `app` or an empty `binary_id` fails the self-test; `commit` may be `""`) | FE6-4 |
| `TestApply_RefusedWhileResetPending` (a staged `restore/pending.json` of kind `reset`: refused with `restore_pending`, the message naming the kind; then its journal at `set_aside`, and again at `applied` without `hook_done`, with its `restore/<id>/inflight.json`: refused with `restore_inflight`; apply and manual rollback refused, nothing downloaded or swapped; `Boot` records `rollback_blocked:restore_pending` and `rollback_blocked:restore_inflight`; a reset intent staged by the failing build (`to_build`) with no set-aside step does not block `Boot`, and `.prev` rejects it under the one rejection rule; no `reset_*` token exists), `TestApply_CheckpointJournalAdmitsCandidate` (a `KindCheckpoint` journal at `swapped`, and again at `failed`: a candidate whose levels and floors admit the restored store and which answers `--print-restore-journal` passes `ReaderCheck`; one that does not admit the store, or a pre-S2b candidate without the flag, refuses with `restore_inflight`) | R7-2, R7-4, R8-1, OPS7-11, DATA7-12, DATA8-6 |
| `TestApply_AdvisoryTokensLoggedBeforeSwap` (an `operator-names.json` listing a canonical-only name and an owed narrowing: apply, `Boot`'s rollback and `Rollback` each log `canonical_only_operator_values` and `config_narrowing_owed` before their rename and still swap) | R8-4, SEC8-6, SEC8-9 |
| `TestApply_EmptyFloorSkipsSidecar` (no `.storage-applied`, new binary prints an empty floor → allowed; a non-empty floor with no sidecar → `stepping_stone_required` naming "boot any release ≥ S0 once"; a non-empty floor with no DB directory, or an empty one, and no sidecars → allowed, as boot exempts an empty store) | OPS7-10, DATA8-11 |
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
| `RestartSelf` (`os.Exit(0)`, `updater.go:329-334`) | `Restarter.Restart` (graceful, exit 75). Until then, 09 S0 owns the HEAD updater hotfix (exit 75, `.prev` kept, semver comparison that treats a describe-suffixed current version as a local build and offers nothing (OPS4-17), a reader check of `<exe>.new` against the store before the swap (the same **decision** as the scripts,
run in process: as the service user it reads the sidecars and stats `restore/pending.json`,
`restore/*/inflight.json` and `restore/*/inflight-checkpoint.json` under the state directory directly, never
through `sudo`, which cannot run under the unit's `NoNewPrivileges=yes`
(`deploy/audiobook-organizer.service:122`), and from S2b it calls core `storage`'s `CheckReader` through
the kernel binding, so a staged factory reset (`KindReset`) refuses it too (`restore_pending`,
`restore_inflight`; `TestHotfixUpdater_RefusesWhileResetPending` in 09 S2b's done-check; OPS8-8, R8-1); a
**floor check** — `<exe>.new --print-upgrade-floor`, a fast exit that ships at S0, compared with `<db>.storage-applied`, refusing with `stepping_stone_required` and naming the release the new binary prints, so the S5 floor release is never swapped onto a store below books' `LegacyCap` although no signed manifest exists before S8 (OPS6-1; 09 S0's updater test gains "a fake new binary whose floor the sidecar does not meet → refused, nothing swapped") — and **both** auto-apply paths of 5U.2 check-only; OPS3-12) and its scheduled-apply policy until S8 (OPS-6, OPS2-5); on the committed unit's layout the HEAD updater cannot write the binary at all (5U.5, OPS2-4) |
| `<exe>.old`, deleted after the swap (`updater.go:267-280`) | `<exe>.prev`, kept; shared with `make rollback` |
| `/api/v1/update/*` handlers (`update_handlers.go`) | core routes + aliases |
| `auto_update.*` config | `core.updater.*` and `core.updater.schedules.*` with aliases (5U.8) |
| release pipeline (reusable workflow, UNKNOWN details) | must add the signed `release-manifest.json` per app before the S8 release; ABO's asset namer must match what the pipeline publishes — verify before enabling apply |

**S8 done-check on staging (09 §9.3, R7-6, OPS7-3, OPS8-3, SIMP8-10).** One design, which exercises the
shipped binary: its trusted key, its `Source` wiring, apply and `Boot`. The check runs between two **real
signed prereleases** of S8: staging runs release candidate *n* with `core.updater.channel=prerelease` and
`core.updater.mode=apply` set after the scrub, and the updater applies release candidate *n+1*, both signed
by the real release pipeline (the prerelease workflow publishes the signed manifest, 5U.5). Egress stays
denied except for the release host names, which `scripts/staging/provision.sh --updater-check` adds as one
nftables set resolved from them for the duration of the check; the scrubbed copy has nothing else to call.
The check passes when the updater applies candidate *n+1* and `Boot` confirms it. It uses shipped
artefacts only: a production binary's release source is the `github.New(owner, repo, …)` constructor
argument in the app's `main` (5U.3), not a setting, so no other source can be substituted without
impersonating the host, and a binary built with any other trusted key is not the artefact that ships. The S8 updater also refuses a target that is not newer (apply step 1), so re-serving the newest
release to it measures nothing. 5U.11's unit tests keep their fake key and `httptest` source
(`TestGithubSource_ParameterisedSlugAndBaseURL`).

## 5U.13 Open items

1. Signing scheme: plain ed25519 over the release manifest (stdlib, chosen here) vs. minisign format
   vs. Sigstore/cosign keyless (free, but verification needs online transparency-log access and large
   dependencies). Revisit only if the release workflow already produces one of the others.
2. (Closed, OPS2-11/R2-4/OPS3-7/OPS6-1: upgrade-path constraints are the new binary's own
   `--print-upgrade-floor` (from the S0 hotfix on) and, from S8, also the signed `min_upgrade_from`
   requirement, both checked against the store's recorded state, 5U.3–5U.4. A binary reached without the
   updater still refuses the store at boot with `ErrUpgradeFloor`, and `Boot` rolls back when the
   update came through it.)
3. Windows support for the swap (a running `.exe` cannot be renamed over on some setups): not a
   current deployment target; `exec` mode is Unix-only.
4. Confirm the asset naming the reusable release workflow actually publishes (UNKNOWN in this repo).
5. (Closed: 06 `app.Options` has `Updater *updater.Options`.)
6. (Closed by 05-storage v1.4 §5S.3.7: the applied-steps sidecar `<db>.storage-applied` the kernel binds
   `StoreState` to, rewritten by every guard-aware boot, and the `--print-upgrade-floor` fast exit that
   prints a build's declared upgrade floors as an `UpgradeRequirement` (OPS3-7, DATA3-10); `ReaderCheck`
   with `IgnoreRestoreStagedBy` and `BootResult.Pending` at open are in §5S.3.7 and §5S.3.5.)
7. (Closed: 06 §6.5.1 lists `updater.Boot` as P−1 before P0a; `Deps.Notify` needs `notify` (09 S7)
   before the updater (S8).)

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
