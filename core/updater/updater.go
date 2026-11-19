// file: core/updater/updater.go
// version: 2.1.0
// guid: 2a3b4c5d-6e7f-8a9b-0c1d-2e3f4a5b6c7d
// last-edited: 2026-11-18

// Package updater is the R0 updater moved into core (05-updater, F8-02): a
// release source, HEAD's raw-asset download and rename swap with the hotfix's
// semver rule, .prev slot and exit-75 restart, plus exactly two additions to
// HEAD's pipeline, the core.updater.max_download cap and the --version-json
// self-test. It names no app: the app passes its ID, binary name and release
// source (Options).
package updater

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"
	"golang.org/x/mod/semver"

	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// RestartExitCode is the status a restart onto a new binary exits with:
// EX_TEMPFAIL, "try again". The committed systemd unit uses Restart=on-failure,
// which restarts a service that exits non-zero; HEAD exited 0, which left the
// service stopped. The kernel's Restarter uses core.lifecycle.restart_exit_code
// (default this value).
const RestartExitCode = 75

// LocalBuildNote is Status's note on a local build.
const LocalBuildNote = "local build, not compared"

// Refusals of a manual apply. Each is returned before anything is downloaded.
var (
	// ErrNoUpdate: no version was named and the last check offered no update.
	ErrNoUpdate = errors.New("no update available")
	// ErrLocalBuildNeedsVersion: the running binary is a local build, which is
	// never compared with releases, so an apply must name the version.
	ErrLocalBuildNeedsVersion = errors.New("local build: name the version to apply")
	// ErrNotNewer: the release is not newer than the running version.
	ErrNotNewer = errors.New("release is not newer than the running version")
	// ErrUnversioned: the running build carries no release version (dev or
	// an unparsable string), so no release can be judged newer than it.
	ErrUnversioned = errors.New("running build has no release version: updates are not compared")
	// ErrInvalidVersion: the named version is not a release tag.
	ErrInvalidVersion = errors.New("version is not a release tag")
	// ErrReleaseNotFound: the release source has no release with that tag.
	ErrReleaseNotFound = errors.New("release not found")
	// ErrNoStateDir: the updater was built without a state directory, so it
	// cannot read the swap slot and refuses rather than skip the check.
	ErrNoStateDir = errors.New("updater has no state directory: cannot check the swap slot, refusing to apply")
	// ErrDisabled: core.updater.mode is off.
	ErrDisabled = errors.New("updater is off")
	// ErrBinaryNotWritable: the executable's directory cannot be written by
	// this process, so no swap could be made.
	ErrBinaryNotWritable = errors.New("binary_not_writable: the executable's directory is not writable by this process")
	// ErrNoAsset: the release has no asset for this platform.
	ErrNoAsset = errors.New("no_asset: the release has no asset for this platform")
	// ErrNoTagLookup: the configured source cannot look a release up by tag.
	ErrNoTagLookup = errors.New("the release source cannot look a release up by tag")
	// ErrDownloadFailed: the asset could not be downloaded in full.
	ErrDownloadFailed = errors.New("download_failed")
	// ErrOversize: the asset is larger than core.updater.max_download.
	ErrOversize = errors.New("the download exceeds core.updater.max_download")
	// ErrSelfTest: the downloaded binary failed its --version-json self-test.
	ErrSelfTest = errors.New("selftest_failed")
	// ErrNoPrevious: there is no <exe>.prev to roll back to.
	ErrNoPrevious = errors.New("there is no previous binary to roll back to")
	// ErrNoRestarter: the updater was built without a Restarter.
	ErrNoRestarter = errors.New("updater has no restarter")
	// ErrNoVersion: the updater was built without the running version.
	ErrNoVersion = errors.New("updater has no running version")
	// A staged (restore_pending) or in-flight (restore_inflight) restore or
	// reset swap refuses an apply with restorejournal.ErrSwapPending, the
	// slot probe's own sentinel (05-updater 5U.12). Core declares no second
	// one: callers match restorejournal.ErrSwapPending. Swapping in a new
	// binary under such a swap would pair the new build with the old build's
	// intent.
)

// describeSuffix returns the pattern that matches the suffix `git describe --tags --always --dirty`
// (a Makefile VERSION) appends to a tag when the build is not exactly that tag:
// -<commits since tag>-g<abbreviated hash>, optionally followed by -dirty.
func describeSuffix() *regexp.Regexp {
	return regexp.MustCompile(`-[0-9]+-g[0-9a-f]+(-dirty)?$`)
}

// releaseTagPattern returns the only shape a tag may have when it is put into a
// release-lookup URL: "v", three numeric parts, and optional pre-release and
// build parts drawn from [0-9A-Za-z.-]. normalizeTag already requires a valid
// semantic version, which implies this; the match is repeated at the request
// site so a URL is visibly built from a value that cannot hold "/", "?",
// "#", "%" or "..".
func releaseTagPattern() *regexp.Regexp {
	return regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)
}

// Updater checks for and applies updates from one release Source (one per
// App). Every mutation happens inside the apply and rollback tasks, which the
// kernel serialises on one concurrency key; Status is a lock-free read.
type Updater struct {
	opts    Options
	d       Deps
	source  Source
	version string // the running build's version

	settings   func() Settings
	stateDir   string // resolved absolute state directory whose restore/ holds the swap slot
	dbPath     string // core.storage.path: the store ReaderCheck judges
	secret     func(ctx context.Context, name string) (string, error)
	executable func() (string, error)              // os.Executable
	rename     func(oldpath, newpath string) error // os.Rename
	log        *slog.Logger
	metrics    *Metrics
	now        func() time.Time

	// skipSelfTest keeps the gin /api/v1/update/* path as HEAD's R0 had it
	// until F8-04 deletes that path (LegacySkipSelfTest).
	skipSelfTest bool

	// idleTimeout and maxDuration bound one download (DownloadIdleTimeout and
	// DownloadMaxDuration; tests shorten them).
	idleTimeout, maxDuration time.Duration

	status atomic.Pointer[Status]
}

// Option configures an Updater built by New. They carry what §5U.3's Options
// and Deps do not: the running version, the directories and the settings.
type Option func(*Updater)

// WithVersion sets the running build's version (app.Options.Version).
func WithVersion(v string) Option { return func(u *Updater) { u.version = v } }

// WithSettings sets the reader of the live core.updater.* values; without it
// the updater runs on DefaultSettings(Options.DefaultMode).
func WithSettings(f func() Settings) Option { return func(u *Updater) { u.settings = f } }

// WithStateDir sets the resolved absolute state directory (the one the
// kernel's app.Options.StateDir names) whose restore/ holds the swap slot. An
// apply reads the slot there and refuses while a swap is staged or in flight.
//
// The directory is made absolute here; a directory that cannot be (or an
// empty one) leaves the updater with none, and an apply then refuses.
func WithStateDir(dir string) Option {
	return func(u *Updater) {
		u.stateDir = ""
		if strings.TrimSpace(dir) == "" {
			return
		}
		if abs, err := filepath.Abs(dir); err == nil {
			u.stateDir = abs
		}
	}
}

// WithDBPath sets core.storage.path, the store the rollback's ReaderCheck
// judges the previous binary against.
func WithDBPath(p string) Option { return func(u *Updater) { u.dbPath = p } }

// WithSecrets sets how a Source's TokenSecret is read (the kernel binds it
// over the secret store).
func WithSecrets(f func(ctx context.Context, name string) (string, error)) Option {
	return func(u *Updater) { u.secret = f }
}

// WithMetrics sets the updater's series; nil records nothing.
func WithMetrics(m *Metrics) Option { return func(u *Updater) { u.metrics = m } }

// WithExecutableForTest replaces the function that names the running executable,
// which is the file an apply replaces.
func WithExecutableForTest(fn func() (string, error)) Option {
	return func(u *Updater) { u.executable = fn }
}

// WithRenameForTest replaces os.Rename in the swap.
func WithRenameForTest(fn func(oldpath, newpath string) error) Option {
	return func(u *Updater) { u.rename = fn }
}

// WithClockForTest replaces the clock.
func WithClockForTest(now func() time.Time) Option { return func(u *Updater) { u.now = now } }

// LegacySkipSelfTest keeps the --version-json self-test off the pipeline. It
// exists for the gin /api/v1/update/* path, which F8-02 must leave answering
// as R0 did (only releases from v0.224.0 on print --version-json); F8-04
// deletes that path and this option with it. The task path never sets it.
func LegacySkipSelfTest() Option { return func(u *Updater) { u.skipSelfTest = true } }

// New builds the updater for opts and d. Without the running version
// (WithVersion) it refuses: HEAD's updater was given it at construction too.
func New(opts Options, d Deps, with ...Option) (*Updater, error) {
	switch {
	case strings.TrimSpace(opts.AppID) == "":
		return nil, errors.New("updater: Options.AppID is empty")
	case strings.TrimSpace(opts.BinaryName) == "":
		return nil, errors.New("updater: Options.BinaryName is empty")
	case opts.Source == nil:
		return nil, errors.New("updater: Options.Source is nil")
	}
	if opts.AssetName == nil {
		opts.AssetName = RawBinary(opts.BinaryName)
	}
	u := &Updater{opts: opts, d: d, log: d.Log, now: time.Now, idleTimeout: DownloadIdleTimeout, maxDuration: DownloadMaxDuration}
	for _, w := range with {
		w(u)
	}
	if strings.TrimSpace(u.version) == "" {
		return nil, ErrNoVersion
	}
	if u.settings == nil {
		def := DefaultSettings(opts.DefaultMode)
		u.settings = func() Settings { return def }
	}
	u.source = opts.Source
	if b, ok := opts.Source.(Binder); ok {
		env := Env{UserAgent: opts.BinaryName + "/" + u.version, Secret: u.secret}
		if d.HTTP != nil {
			env.HTTP = d.HTTP.Client()
		}
		u.source = b.Bind(env)
	}
	st := Status{Current: u.version}
	st.Mode, st.Channel = u.settings().Mode, u.settings().Channel
	u.status.Store(&st)
	return u, nil
}

func goos() string   { return runtime.GOOS }
func goarch() string { return runtime.GOARCH }

// ComponentID is the health component the kernel registers for the updater
// (kernel/health.go registers it with Probe as its source; Deps.Health is
// therefore not used to register it here).
const ComponentID health.ComponentID = "core.updater"

// logf writes one line; a nil logger discards it.
func (u *Updater) logf(level slog.Level, msg string, args ...any) {
	if u.log != nil {
		u.log.Log(context.Background(), level, msg, args...)
	}
}

func (u *Updater) executablePath() (string, error) {
	if u.executable != nil {
		return u.executable()
	}
	return os.Executable()
}

func (u *Updater) renameFile(oldpath, newpath string) error {
	if u.rename != nil {
		return u.rename(oldpath, newpath)
	}
	return os.Rename(oldpath, newpath)
}

// Status returns the updater's last known state; a lock-free atomic read.
func (u *Updater) Status() Status { return *u.status.Load() }

// setStatus publishes st.
func (u *Updater) setStatus(st Status) { u.status.Store(&st) }

// Probe is the core.updater component's source (05-updater 5U.9): Up, or
// Degraded with one of the fixed reasons check_failed, binary_not_writable,
// auto_update_paused. An off updater reports Up with reason "off".
func (u *Updater) Probe(context.Context) health.Result {
	st := u.Status()
	res := health.Result{Status: health.StatusUp, At: u.now()}
	switch {
	case st.Mode < ModeCheck:
		res.Reason = "off"
	case st.DisabledReason == "binary_not_writable":
		res.Status, res.Reason = health.StatusDegraded, "binary_not_writable"
	case st.LastError != "":
		res.Status, res.Reason = health.StatusDegraded, "check_failed"
	case st.AutoApplyPaused:
		res.Status, res.Reason = health.StatusDegraded, "auto_update_paused"
	}
	return res
}

// canonicalVersion returns v with a leading "v" when it is a valid semantic
// version, and false for "dev", "" or anything semver rejects.
func canonicalVersion(v string) (string, bool) {
	if v == "" || v == "dev" {
		return "", false
	}
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	if !semver.IsValid(v) {
		return "", false
	}
	return v, true
}

// IsLocalBuild reports whether version carries a git describe suffix
// (-<n>-g<hex>, -dirty, or both). Semver reads such a version as a prerelease
// and orders it below its own base tag, so comparing it with releases would
// offer the base tag, or an older one, as an update.
func IsLocalBuild(version string) bool {
	return describeSuffix().MatchString(version) || strings.HasSuffix(version, "-dirty")
}

// isNewer reports whether release tag latest is strictly newer than current.
// An unversioned current or an unparsable latest is never newer.
func isNewer(latest, current string) bool {
	c, ok := canonicalVersion(current)
	if !ok {
		return false
	}
	l, ok := canonicalVersion(latest)
	if !ok {
		return false
	}
	return semver.Compare(l, c) > 0
}

// normalizeTag turns an operator-typed version into the release tag form
// (leading "v") and rejects anything that is not a semantic version, so the
// value can never carry a path or query into the release lookup.
func normalizeTag(version string) (string, bool) {
	v := strings.TrimSpace(version)
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	if !semver.IsValid(v) || !releaseTagPattern().MatchString(v) || strings.Contains(v, "..") {
		return "", false
	}
	return v, true
}

// Check queries the release source on the configured channel and records the
// result. It never applies anything and notifies nobody (the check task
// does, through the run's Writer).
func (u *Updater) Check(ctx context.Context) (Status, error) {
	return u.CheckWith(ctx, CheckOptions{})
}

// CheckWith is Check with one run's refinements.
func (u *Updater) CheckWith(ctx context.Context, o CheckOptions) (Status, error) {
	st, _, err := u.check(ctx, o)
	return st, err
}

// check runs one check and returns the release it saw (zero when none).
func (u *Updater) check(ctx context.Context, o CheckOptions) (Status, Release, error) {
	set := u.settings()
	ch := set.Channel
	if o.Channel != "" {
		ch = o.Channel
	}
	if ch == "" {
		ch = ChannelStable
	}
	st := Status{
		Mode: set.Mode, Channel: ch, Current: u.version,
		AutoApplyPaused: set.AutoApplyPaused || o.AutoApplyPaused,
		LocalBuild:      IsLocalBuild(u.version),
		PreviousKept:    u.Status().PreviousKept,
	}
	if st.Mode < ModeCheck {
		st.DisabledReason = "off"
		u.setStatus(st)
		return st, Release{}, nil
	}
	if _, ok := canonicalVersion(u.version); !ok && !st.LocalBuild {
		st.DisabledReason = "dev_build"
	}
	if err := u.probeWritable(); err != nil && st.DisabledReason == "" {
		st.DisabledReason = "binary_not_writable"
	}

	rel, err := u.source.Latest(ctx, ch)
	st.LastChecked = u.now()
	switch {
	case errors.Is(err, ErrReleaseNotFound):
		// No release yet: nothing to offer, not a failed check.
		st.Latest = u.version
		u.metrics.Check(ctx, "ok")
		u.metrics.Available(ctx, false)
		u.setStatus(st)
		return st, Release{}, nil
	case err != nil:
		st.LastError = "check_failed"
		u.logf(slog.LevelWarn, "update check failed", "err", err)
		u.metrics.Check(ctx, "failed")
		u.setStatus(st)
		return st, Release{}, fmt.Errorf("update check: %w", err)
	}
	st.Latest = rel.Version
	st.Available = !st.LocalBuild && isNewer(rel.Version, u.version)
	outcome := "ok"
	if st.Available && u.assetOf(rel) == nil {
		st.LastError, outcome = "no_asset", "no_asset"
	}
	u.metrics.Check(ctx, outcome)
	u.metrics.Available(ctx, st.Available)
	if st.Available {
		u.logf(slog.LevelInfo, "update available; apply it from Settings", "running", u.version, "latest", rel.Version, "channel", string(ch))
	}
	u.setStatus(st)
	return st, rel, nil
}

// assetOf is the release's asset for this platform, or nil.
func (u *Updater) assetOf(r Release) *Asset {
	want := u.opts.AssetName(r, goos(), goarch())
	for i := range r.Assets {
		if r.Assets[i].Name == want {
			return &r.Assets[i]
		}
	}
	return nil
}

// probeWritable reports whether the executable's directory can be written by
// this process: it creates and removes a probe file through an os.Root opened
// on that directory (OPS2-4).
func (u *Updater) probeWritable() (err error) {
	exe, err := u.executablePath()
	if err != nil {
		return fmt.Errorf("executable path: %w", err)
	}
	root, err := os.OpenRoot(filepath.Dir(exe))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBinaryNotWritable, err)
	}
	defer func() {
		if cerr := root.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("%w: %v", ErrBinaryNotWritable, cerr))
		}
	}()
	const probe = ".updater-write-probe"
	f, err := root.OpenFile(probe, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBinaryNotWritable, err)
	}
	cerr := f.Close()
	rerr := root.Remove(probe)
	if cerr != nil || rerr != nil {
		return fmt.Errorf("%w: %v", ErrBinaryNotWritable, errors.Join(cerr, rerr))
	}
	return nil
}

// applyAllowed is the R0 apply rule, applied to the tag that would actually be
// downloaded: a local build applies only a version the operator named; any
// other build applies only a release strictly newer than itself.
func (u *Updater) applyAllowed(tag string, named bool) error {
	if IsLocalBuild(u.version) {
		if named {
			return nil
		}
		return ErrLocalBuildNeedsVersion
	}
	if _, ok := canonicalVersion(u.version); !ok {
		return ErrUnversioned
	}
	if !isNewer(tag, u.version) {
		return ErrNotNewer
	}
	return nil
}

// Prepare resolves what a manual apply installs and enforces the R0 refusals
// without touching the network. version "" means the release the last check
// offered; otherwise version names a release tag ("v" optional).
//
// Refusals, in order: an unnamed apply on a local build
// (ErrLocalBuildNeedsVersion); an unnamed apply with nothing offered
// (ErrNoUpdate); a malformed named version (ErrInvalidVersion); a build with
// no release version (ErrUnversioned); a target that is not newer than a
// non-local running version (ErrNotNewer). A named version on a local build is
// allowed without the newer-than check.
func (u *Updater) Prepare(version string) (Target, error) {
	if strings.TrimSpace(version) == "" {
		if IsLocalBuild(u.version) {
			return Target{}, ErrLocalBuildNeedsVersion
		}
		last := u.Status()
		if !last.Available {
			return Target{}, ErrNoUpdate
		}
		tag, ok := normalizeTag(last.Latest)
		if !ok {
			return Target{}, ErrInvalidVersion
		}
		if err := u.applyAllowed(tag, false); err != nil {
			return Target{}, err
		}
		return Target{Version: tag}, nil
	}
	tag, ok := normalizeTag(version)
	if !ok {
		return Target{}, ErrInvalidVersion
	}
	if err := u.applyAllowed(tag, true); err != nil {
		return Target{}, err
	}
	return Target{Version: tag, Named: true}, nil
}

// checkSwapSlot refuses while the swap slot holds a staged (restore_pending)
// or in-flight (restore_inflight) swap (05-updater 5U.12). It fails closed: a
// slot it cannot read, or an updater with no state directory to read it in,
// refuses too.
func (u *Updater) checkSwapSlot() error {
	return slotRefusal(u.stateDir)
}

// slotRefusal reads the swap slot under stateDir.
func slotRefusal(stateDir string) error {
	if strings.TrimSpace(stateDir) == "" {
		return ErrNoStateDir
	}
	st, err := restorejournal.ReadSlot(vfs.Default, stateDir)
	if err != nil {
		return fmt.Errorf("cannot read the swap slot, refusing: %w", err)
	}
	if st.Busy() {
		return st.Err()
	}
	return nil
}

// writeExecutable streams body into path (mode 0755) and fsyncs it, so the
// renamed binary is never an empty or partial file after a crash. Any failure
// deletes path. It returns the bytes written.
func writeExecutable(path string, body io.Reader) (int64, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return 0, fmt.Errorf("failed to create new binary file: %w", err)
	}
	n, err := io.Copy(f, body)
	if err != nil {
		closeErr := f.Close()
		_ = os.Remove(path)
		return n, fmt.Errorf("failed to write new binary: %w", errors.Join(err, closeErr))
	}
	if err := f.Sync(); err != nil {
		closeErr := f.Close()
		_ = os.Remove(path)
		return n, fmt.Errorf("failed to sync new binary: %w", errors.Join(err, closeErr))
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return n, fmt.Errorf("failed to close new binary: %w", err)
	}
	// The mode is set by the umask at create; the binary must be runnable.
	if err := os.Chmod(path, 0o755); err != nil {
		_ = os.Remove(path)
		return n, fmt.Errorf("failed to make the new binary executable: %w", err)
	}
	return n, nil
}

// syncDir fsyncs a directory so the renames in it are durable.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	syncErr := d.Sync()
	closeErr := d.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}
