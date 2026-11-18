// file: internal/updater/adapter.go
// version: 2.0.1
// guid: 6f1c2d3e-4a5b-4c6d-8e7f-9a0b1c2d3e4f
// last-edited: 2026-11-18

// Package updater is this app's side of the updater. The R0 pipeline lives in
// core/updater (F8-02): the release lookups, the raw-asset download, the rename
// swap and the checks that guard it. This package keeps what is the app's own:
// the gin-facing type (UpdateInfo, CheckForUpdate, PrepareApply,
// DownloadAndReplace, RestartSelf), the develop channel (a commit cannot be
// applied, so core has no such channel), the scheduler and the container
// registration. It also names the repository and the asset: NewSource is the
// release source core's updater is built with, so no repository is named in
// core.
//
// The gin /api/v1/update/* handlers answer as they did before the move. Core's
// own updater (tasks, routes, the self-test) is not wired here: F8-04 does that
// and deletes this adapter's gin-facing half with the handlers.
package updater

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/mediacore/storage/restorejournal"
	coreupdater "github.com/falkcorp/mediacore/updater"
	"github.com/falkcorp/mediacore/updater/github"
)

// updateLog routes the scheduler's lines through internal/logger's sanitizing
// layer: versions, tags and asset URLs come from the release source.
var updateLog = logger.New("updater")

// The names core defines and the gin handlers and tests use.
const (
	// RestartExitCode is the status RestartSelf exits with after an update (75,
	// EX_TEMPFAIL, which the committed unit's Restart=on-failure restarts).
	RestartExitCode = coreupdater.RestartExitCode
	// LocalBuildNote is UpdateInfo.Note on a local build.
	LocalBuildNote = coreupdater.LocalBuildNote

	// Owner and Repo name this app's release repository.
	Owner = "falkcorp"
	Repo  = "audiobook-organizer"
	// AppID is the app field a release's --version-json prints (main.go's
	// versionInfo, books.AppID: TestAppIDIsTheBooksAppID holds the two equal);
	// the core updater's self-test holds a downloaded binary to it. The gin
	// path never self-tests (LegacySkipSelfTest) but core's New requires one.
	AppID = "abo"
	// BinaryName is the raw release asset's prefix ("<BinaryName>-<goos>-<goarch>")
	// and the user agent's product.
	BinaryName = "audiobook-organizer"
)

// Refusals of a manual apply, core's own values so errors.Is matches across
// the two packages. ErrSwapBusy is restorejournal's sentinel: a restore or
// reset swap staged or in flight in the one swap slot.
var (
	ErrNoUpdate               = coreupdater.ErrNoUpdate
	ErrLocalBuildNeedsVersion = coreupdater.ErrLocalBuildNeedsVersion
	ErrNotNewer               = coreupdater.ErrNotNewer
	ErrUnversioned            = coreupdater.ErrUnversioned
	ErrInvalidVersion         = coreupdater.ErrInvalidVersion
	ErrReleaseNotFound        = coreupdater.ErrReleaseNotFound
	ErrSwapBusy               = restorejournal.ErrSwapPending
	ErrNoStateDir             = coreupdater.ErrNoStateDir
)

// errDevelopApply refuses an apply on the develop channel (HEAD's rule).
var errDevelopApply = errors.New("develop channel binary downloads require CI-published assets; use stable channel for binary updates")

// IsLocalBuild reports whether version carries a git describe suffix.
func IsLocalBuild(version string) bool { return coreupdater.IsLocalBuild(version) }

// CoreOptions is the core updater's Options for this app: the source, the
// binary name and the app ID its self-test holds a downloaded binary to. It is
// what app.Options.Updater carries: `updater fetch-release` reads it now, and
// F8-04 hands the same value to the kernel.
func CoreOptions() coreupdater.Options {
	return coreupdater.Options{AppID: AppID, BinaryName: BinaryName, Source: NewSource(), DefaultMode: coreupdater.ModeCheck}
}

// NewSource is the release source the core updater is built with for this
// app: GitHub releases of falkcorp/audiobook-organizer, the raw asset
// "audiobook-organizer-<goos>-<goarch>". F8-04 passes it as
// coreupdater.Options.Source (with BinaryName and AppID above) when it wires
// the core updater into main.
func NewSource(opts ...github.Option) coreupdater.Source { return github.New(Owner, Repo, opts...) }

// UpdateInfo holds the result of an update check.
type UpdateInfo struct {
	CurrentVersion  string    `json:"current_version"`
	LatestVersion   string    `json:"latest_version"`
	Channel         string    `json:"channel"`
	UpdateAvailable bool      `json:"update_available"`
	ReleaseURL      string    `json:"release_url,omitempty"`
	ReleaseNotes    string    `json:"release_notes,omitempty"`
	PublishedAt     time.Time `json:"published_at,omitempty"`
	LastChecked     time.Time `json:"last_checked"`
	// LocalBuild is set when the running version carries a git describe
	// suffix: it is not compared with releases and nothing is offered.
	LocalBuild bool `json:"local_build"`
	// AutoApplyPaused is set while the operator asked for automatic apply
	// (auto_update.enabled): this build only checks, it never applies
	// unattended.
	AutoApplyPaused bool `json:"auto_apply_paused"`
	// Note is LocalBuildNote on a local build, else empty.
	Note string `json:"note"`

	// named marks an apply target the operator named explicitly (PrepareApply
	// with a version). Only a named target may be applied to a local build.
	named bool
}

// Updater checks for and applies updates from GitHub releases: core's
// pipeline behind the calls the gin handlers and the scheduler make.
type Updater struct {
	currentVersion string
	core           *coreupdater.Updater
	rec            *recordingSource

	mu        sync.Mutex
	lastCheck *UpdateInfo

	// Seams, all defaulted when zero.
	apiBase    string       // GitHub API root; github.DefaultBaseURL
	httpClient *http.Client // nil: the source's own client; develop uses a 30 s one
	exit       func(code int)

	stateDir string
	coreOpts []coreupdater.Option
}

// Option configures an Updater built by NewUpdater.
type Option func(*Updater)

// WithStateDir sets the resolved absolute state directory whose restore/ holds
// the swap slot. An apply reads the slot there and refuses while a swap is
// staged or in flight; an updater with none refuses (ErrNoStateDir).
func WithStateDir(dir string) Option { return func(u *Updater) { u.stateDir = dir } }

// WithAPIBaseForTest points the release lookups at another API root (a test server).
func WithAPIBaseForTest(base string) Option {
	return func(u *Updater) { u.apiBase = strings.TrimRight(base, "/") }
}

// WithHTTPClientForTest replaces the HTTP client (a test server's client).
func WithHTTPClientForTest(c *http.Client) Option { return func(u *Updater) { u.httpClient = c } }

// WithExecutableForTest replaces the function that names the running
// executable, which is the file an apply replaces.
func WithExecutableForTest(fn func() (string, error)) Option {
	return func(u *Updater) { u.coreOpts = append(u.coreOpts, coreupdater.WithExecutableForTest(fn)) }
}

// WithRenameForTest replaces os.Rename in the swap, to inject a failed rename.
func WithRenameForTest(fn func(oldpath, newpath string) error) Option {
	return func(u *Updater) { u.coreOpts = append(u.coreOpts, coreupdater.WithRenameForTest(fn)) }
}

// WithExitForTest replaces os.Exit in RestartSelf.
func WithExitForTest(fn func(code int)) Option { return func(u *Updater) { u.exit = fn } }

// NewUpdater creates an Updater for the given current version.
func NewUpdater(currentVersion string, opts ...Option) *Updater {
	u := &Updater{currentVersion: currentVersion}
	for _, o := range opts {
		o(u)
	}
	var gh []github.Option
	if u.apiBase != "" {
		gh = append(gh, github.BaseURL(u.apiBase))
	}
	src := NewSource(gh...)
	// The source is bound here, with the user agent HEAD sent, so core's New
	// (which binds a source that implements Binder again, replacing the
	// environment) finds one that does not: recordingSource only forwards.
	if b, ok := src.(coreupdater.Binder); ok {
		src = b.Bind(coreupdater.Env{HTTP: u.httpClient, UserAgent: BinaryName + "/" + currentVersion})
	}
	u.rec = &recordingSource{Source: src}

	with := append([]coreupdater.Option{
		coreupdater.WithVersion(currentVersion),
		coreupdater.WithStateDir(u.stateDir),
		// The gin path keeps R0's behaviour until F8-04 deletes it: a release
		// from before --version-json still applies. The task path never sets
		// this (05-updater 5U.4).
		coreupdater.LegacySkipSelfTest(),
		// Check-only mode: this adapter never schedules an apply.
		coreupdater.WithSettings(func() coreupdater.Settings { return coreupdater.DefaultSettings(coreupdater.ModeCheck) }),
	}, u.coreOpts...)
	copts := CoreOptions()
	copts.Source = u.rec
	// internal/logger hands out the process logger (the slog guard allows no
	// direct slog.Default()); the handler on top escapes what a release's tag
	// or an error from the source can carry.
	coreLog := slog.New(sanitizingHandler{inner: logger.FromContext(context.Background()).Handler()}).With("subsystem", "updater")
	core, err := coreupdater.New(copts, coreupdater.Deps{Log: coreLog}, with...)
	if err != nil {
		// New fails only on an empty AppID, BinaryName, Source or version.
		// Never the first three here; an empty version is the "dev" the
		// container passes (NewService), so an empty one gets the same name.
		core, err = coreupdater.New(copts, coreupdater.Deps{Log: coreLog}, append(with, coreupdater.WithVersion("dev"))...)
		if err != nil {
			panic(fmt.Sprintf("updater: building the core updater: %v", err))
		}
		u.currentVersion = "dev"
	}
	u.core = core
	return u
}

// recordingSource remembers the release the last Latest call returned, which
// is what UpdateInfo's ReleaseURL, ReleaseNotes and PublishedAt come from.
type recordingSource struct {
	coreupdater.Source
	mu   sync.Mutex
	last coreupdater.Release
}

func (r *recordingSource) Latest(ctx context.Context, ch coreupdater.Channel) (coreupdater.Release, error) {
	rel, err := r.Source.Latest(ctx, ch)
	r.mu.Lock()
	r.last = rel
	r.mu.Unlock()
	return rel, err
}

// Release forwards a tag lookup (coreupdater.TagSource).
func (r *recordingSource) Release(ctx context.Context, version string) (coreupdater.Release, error) {
	ts, ok := r.Source.(coreupdater.TagSource)
	if !ok {
		return coreupdater.Release{}, coreupdater.ErrNoTagLookup
	}
	return ts.Release(ctx, version)
}

func (r *recordingSource) lastRelease() coreupdater.Release {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last
}

// checkTimeout bounds one check, as HEAD's 30 s client timeout did.
const checkTimeout = 30 * time.Second

// LastCheck returns the most recent check result, or nil if none has run.
func (u *Updater) LastCheck() *UpdateInfo {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.lastCheck
}

// CheckForUpdate checks for a newer version on the given channel ("stable", or
// "develop" for the latest commit on main; any other value is "stable").
func (u *Updater) CheckForUpdate(channel string) (*UpdateInfo, error) {
	return u.check(channel, false)
}

// CheckUnattended is the check the unattended paths (the scheduler's tick and
// the maintenance window's step 1) run instead of applying. Both run only
// while auto_update.enabled is true, so the status they record carries
// AutoApplyPaused: the operator asked for automatic apply and this build
// paused it. A newer release is logged at info with both versions; it is
// applied only from Settings.
func (u *Updater) CheckUnattended(channel string) (*UpdateInfo, error) {
	info, err := u.check(channel, true)
	if err != nil {
		return nil, err
	}
	if info.UpdateAvailable {
		updateLog.Info("update available; apply it from Settings: running %s, latest %s (channel %s)",
			info.CurrentVersion, info.LatestVersion, info.Channel)
	}
	return info, nil
}

func (u *Updater) check(channel string, autoApplyPaused bool) (*UpdateInfo, error) {
	var info *UpdateInfo
	var err error

	switch channel {
	case "develop":
		info, err = u.checkDevelop()
	default:
		info, err = u.checkStable()
	}
	if err != nil {
		return nil, err
	}

	if IsLocalBuild(u.currentVersion) {
		info.LocalBuild = true
		info.Note = LocalBuildNote
		info.UpdateAvailable = false
	}
	info.AutoApplyPaused = autoApplyPaused

	u.mu.Lock()
	u.lastCheck = info
	u.mu.Unlock()
	return info, nil
}

// checkStable is core's check over the stable channel, in this type's terms.
func (u *Updater) checkStable() (*UpdateInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
	defer cancel()
	st, err := u.core.CheckWith(ctx, coreupdater.CheckOptions{Channel: coreupdater.ChannelStable})
	if err != nil {
		return nil, err
	}
	info := &UpdateInfo{
		CurrentVersion:  u.currentVersion,
		LatestVersion:   st.Latest,
		Channel:         "stable",
		UpdateAvailable: st.Available,
		LastChecked:     st.LastChecked,
	}
	// With no release yet the check reports the running version and offers
	// nothing; the release fields stay empty, as HEAD's did.
	if rel := u.rec.lastRelease(); rel.Version != "" && st.Latest == rel.Version {
		info.ReleaseURL, info.ReleaseNotes, info.PublishedAt = rel.URL, rel.Notes, rel.PublishedAt
	}
	return info, nil
}

// githubCommit is the subset of GitHub's commit API response the develop
// channel uses.
type githubCommit struct {
	SHA     string `json:"sha"`
	HTMLURL string `json:"html_url"`
	Commit  struct {
		Message string `json:"message"`
		Author  struct {
			Date string `json:"date"`
		} `json:"author"`
	} `json:"commit"`
}

func (u *Updater) client() *http.Client {
	if u.httpClient != nil {
		return u.httpClient
	}
	return &http.Client{Timeout: checkTimeout}
}

// checkDevelop is HEAD's develop channel: the latest commit on main, which
// cannot be ordered against a version, so any difference counts. It stays here
// because core has no commit channel (a commit has no asset to apply).
func (u *Updater) checkDevelop() (*UpdateInfo, error) {
	base := u.apiBase
	if base == "" {
		base = github.DefaultBaseURL
	}
	target := base + fmt.Sprintf("/repos/%s/%s/commits/main", Owner, Repo)
	if p, err := url.Parse(target); err != nil || p.Host == "" {
		return nil, errors.New("failed to create request: the API base is not a URL")
	}
	req, err := http.NewRequest("GET", target, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", BinaryName+"/"+u.currentVersion)

	resp, err := u.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to check for updates: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
	}

	var commit githubCommit
	if err := json.UnmarshalRead(resp.Body, &commit); err != nil {
		return nil, fmt.Errorf("failed to decode commit: %w", err)
	}

	shortSHA := commit.SHA
	if len(shortSHA) > 7 {
		shortSHA = shortSHA[:7]
	}

	var committedAt time.Time
	if commit.Commit.Author.Date != "" {
		committedAt, _ = time.Parse(time.RFC3339, commit.Commit.Author.Date)
	}

	firstLine := commit.Commit.Message
	if idx := strings.Index(firstLine, "\n"); idx != -1 {
		firstLine = firstLine[:idx]
	}

	return &UpdateInfo{
		CurrentVersion:  u.currentVersion,
		LatestVersion:   shortSHA,
		Channel:         "develop",
		UpdateAvailable: shortSHA != u.currentVersion && u.currentVersion != "dev",
		ReleaseURL:      commit.HTMLURL,
		ReleaseNotes:    firstLine,
		PublishedAt:     committedAt,
		LastChecked:     time.Now(),
	}, nil
}

// PrepareApply resolves what a manual apply installs and enforces the R0
// refusals without touching the network. version "" means the release the last
// check offered; otherwise version names a release tag ("v" optional).
//
// Refusals, in order: an unnamed apply on a local build
// (ErrLocalBuildNeedsVersion); an unnamed apply with nothing offered
// (ErrNoUpdate); a malformed named version (ErrInvalidVersion); a build with
// no release version (ErrUnversioned); a target that is not newer than a
// non-local running version (ErrNotNewer). A named version on a local build is
// allowed without the newer-than check.
func (u *Updater) PrepareApply(version string) (*UpdateInfo, error) {
	if strings.TrimSpace(version) == "" {
		if IsLocalBuild(u.currentVersion) {
			return nil, ErrLocalBuildNeedsVersion
		}
		last := u.LastCheck()
		if last == nil || !last.UpdateAvailable {
			return nil, ErrNoUpdate
		}
		target := *last
		if target.Channel == "develop" {
			// DownloadAndReplace refuses develop with HEAD's message.
			return &target, nil
		}
		// A non-local build: named or not, the rule is the same one.
		if _, err := u.core.Prepare(target.LatestVersion); err != nil {
			return nil, err
		}
		return &target, nil
	}

	tgt, err := u.core.Prepare(version)
	if err != nil {
		return nil, err
	}
	return &UpdateInfo{
		CurrentVersion: u.currentVersion,
		LatestVersion:  strings.TrimPrefix(tgt.Version, "v"),
		Channel:        "stable",
		LocalBuild:     IsLocalBuild(u.currentVersion),
		named:          true,
	}, nil
}

// DownloadAndReplace downloads the release info names (by its tag, so the
// bytes installed are the release that was checked or named, never whatever
// is latest at apply time) and swaps it in place of the running executable.
//
// The swap: write <exe>.new (0755) and fsync it, rename <exe> -> <exe>.prev
// (replacing an older .prev), rename <exe>.new -> <exe>, fsync the directory.
// If the second rename fails, .prev is renamed back and .new deleted. .prev is
// never deleted: it is the slot `make rollback` uses. The pipeline is core's
// (coreupdater.Updater.Install).
func (u *Updater) DownloadAndReplace(info *UpdateInfo) error {
	if info == nil || (!info.UpdateAvailable && !info.named) {
		return ErrNoUpdate
	}
	if info.Channel == "develop" {
		return errDevelopApply
	}
	// An unnamed target on a local build is refused here; core's Install takes
	// the tag it is given as named.
	if !info.named && IsLocalBuild(u.currentVersion) {
		return ErrLocalBuildNeedsVersion
	}
	updateLog.Info("Downloading update v%s", strings.TrimPrefix(info.LatestVersion, "v"))
	//lint:ignore SA1019 the gin /api/v1/update/apply path is the one deliberate caller of the legacy entry; F8-04 deletes both it and Install
	if err := u.core.Install(context.Background(), info.LatestVersion); err != nil {
		return err
	}
	updateLog.Info("Update applied successfully: %s -> %s", info.CurrentVersion, info.LatestVersion)
	return nil
}

// RestartSelf exits the process with RestartExitCode so that systemd's
// Restart=on-failure (or launchd's KeepAlive) restarts it with the new binary.
func (u *Updater) RestartSelf() error {
	updateLog.Info("Exiting with status %d for restart with updated binary", RestartExitCode)
	exit := u.exit
	if exit == nil {
		exit = os.Exit
	}
	exit(RestartExitCode)
	return nil // unreachable with os.Exit
}

// sanitizingHandler passes a record through internal/logger's sanitizing
// barrier before the inner handler sees it: a release's tag, notes or an
// error from the source can carry control characters that would forge a log
// line. The message and every string attribute are escaped; an error is
// rendered through SanitizeLogErr.
type sanitizingHandler struct{ inner slog.Handler }

func (h sanitizingHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h sanitizingHandler) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, logger.SanitizeLogValue(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(sanitizeAttr(a))
		return true
	})
	return h.inner.Handle(ctx, out)
}

func (h sanitizingHandler) WithAttrs(as []slog.Attr) slog.Handler {
	clean := make([]slog.Attr, len(as))
	for i, a := range as {
		clean[i] = sanitizeAttr(a)
	}
	return sanitizingHandler{inner: h.inner.WithAttrs(clean)}
}

func (h sanitizingHandler) WithGroup(name string) slog.Handler {
	return sanitizingHandler{inner: h.inner.WithGroup(name)}
}

func sanitizeAttr(a slog.Attr) slog.Attr {
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindString:
		return slog.String(a.Key, logger.SanitizeLogValue(v.String()))
	case slog.KindAny:
		if err, ok := v.Any().(error); ok {
			return slog.String(a.Key, logger.SanitizeLogErr(err))
		}
		return slog.String(a.Key, logger.SanitizeLogValue(fmt.Sprint(v.Any())))
	case slog.KindGroup:
		in := v.Group()
		out := make([]slog.Attr, len(in))
		for i, c := range in {
			out[i] = sanitizeAttr(c)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(out...)}
	}
	return slog.Attr{Key: a.Key, Value: v}
}
