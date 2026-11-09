// file: internal/updater/updater.go
// version: 1.1.2
// guid: 2a3b4c5d-6e7f-8a9b-0c1d-2e3f4a5b6c7d
// last-edited: 2026-11-09

package updater

import (
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/falkcorp/mediacore/storage/restorejournal"
	"golang.org/x/mod/semver"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// updateLog routes the updater's lines through internal/logger's sanitizing
// layer: versions, tags and asset URLs come from the release source.
var updateLog = logger.New("updater")

// RestartExitCode is the status RestartSelf exits with after an update. The
// committed systemd unit uses Restart=on-failure, which restarts a service
// that exits non-zero; HEAD exited 0, which left the service stopped. 75 is
// EX_TEMPFAIL: "try again", which is what a restart onto a new binary is.
const RestartExitCode = 75

// LocalBuildNote is UpdateInfo.Note on a local build.
const LocalBuildNote = "local build, not compared"

// defaultAPIBase is the GitHub REST API root the release lookups use.
const defaultAPIBase = "https://api.github.com"

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
	// ErrSwapBusy: a restore or reset swap is staged (restore_pending) or in
	// flight (restore_inflight) in the one swap slot (05-updater 5U.12). It is
	// restorejournal's own sentinel, so the error the slot probe returns
	// matches it. Swapping in a new binary under a staged or half-applied
	// swap would pair the new build with the old build's intent.
	ErrSwapBusy = restorejournal.ErrSwapPending
	// ErrNoStateDir: the updater was built without a state directory, so it
	// cannot read the swap slot and refuses rather than skip the check.
	ErrNoStateDir = errors.New("updater has no state directory: cannot check the swap slot, refusing to apply")
)

// describeSuffix matches the suffix `git describe --tags --always --dirty`
// (Makefile VERSION) appends to a tag when the build is not exactly that tag:
// -<commits since tag>-g<abbreviated hash>, optionally followed by -dirty.
var describeSuffix = regexp.MustCompile(`-[0-9]+-g[0-9a-f]+(-dirty)?$`)

// releaseTagPattern is the only shape a tag may have when it is put into a
// release-lookup URL: "v", three numeric parts, and optional pre-release and
// build parts drawn from [0-9A-Za-z.-]. normalizeTag already requires a valid
// semantic version, which implies this; the match is repeated at the request
// site so the URL is visibly built from a value that cannot hold "/", "?",
// "#", "%" or "..".
var releaseTagPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)

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

// Updater checks for and applies updates from GitHub releases.
type Updater struct {
	currentVersion string
	repo           string // "falkcorp/audiobook-organizer"
	mu             sync.Mutex
	lastCheck      *UpdateInfo
	httpClient     *http.Client

	// Seams, all defaulted when zero so a literal &Updater{...} still works.
	apiBase    string                              // GitHub API root; defaultAPIBase
	executable func() (string, error)              // os.Executable
	rename     func(oldpath, newpath string) error // os.Rename
	exit       func(code int)                      // os.Exit

	// stateDir is the resolved absolute state directory whose restore/ holds
	// the swap slot. Empty means "unknown": an apply refuses (ErrNoStateDir).
	stateDir string
}

// Option configures an Updater built by NewUpdater.
type Option func(*Updater)

// WithAPIBaseForTest points the release lookups at another API root (a test server).
func WithAPIBaseForTest(base string) Option {
	return func(u *Updater) { u.apiBase = strings.TrimRight(base, "/") }
}

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

// WithHTTPClientForTest replaces the HTTP client used for lookups and downloads.
func WithHTTPClientForTest(c *http.Client) Option {
	return func(u *Updater) { u.httpClient = c }
}

// WithExecutableForTest replaces the function that names the running executable,
// which is the file an apply replaces.
func WithExecutableForTest(fn func() (string, error)) Option {
	return func(u *Updater) { u.executable = fn }
}

// WithExitForTest replaces the function RestartSelf exits through.
func WithExitForTest(fn func(code int)) Option {
	return func(u *Updater) { u.exit = fn }
}

// githubRelease is the subset of GitHub's release API response we use.
type githubRelease struct {
	TagName     string `json:"tag_name"`
	HTMLURL     string `json:"html_url"`
	Body        string `json:"body"`
	Prerelease  bool   `json:"prerelease"`
	PublishedAt string `json:"published_at"`
	Assets      []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

// githubCommit is the subset of GitHub's commit API response we use.
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

// NewUpdater creates an Updater for the given current version.
func NewUpdater(currentVersion string, opts ...Option) *Updater {
	u := &Updater{
		currentVersion: currentVersion,
		repo:           "falkcorp/audiobook-organizer",
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
	for _, opt := range opts {
		opt(u)
	}
	return u
}

func (u *Updater) apiURL(format string, args ...any) string {
	base := u.apiBase
	if base == "" {
		base = defaultAPIBase
	}
	return base + fmt.Sprintf(format, args...)
}

func (u *Updater) client() *http.Client {
	if u.httpClient == nil {
		return http.DefaultClient
	}
	return u.httpClient
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
	return describeSuffix.MatchString(version) || strings.HasSuffix(version, "-dirty")
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

// LastCheck returns the most recent update check result, or nil.
func (u *Updater) LastCheck() *UpdateInfo {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.lastCheck
}

// CheckForUpdate queries GitHub for the latest version on the given channel
// and records the result as the last check. It never applies anything.
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

func (u *Updater) checkStable() (*UpdateInfo, error) {
	release, found, err := u.fetchRelease(u.apiURL("/repos/%s/releases/latest", u.repo))
	if err != nil {
		return nil, err
	}
	if !found {
		// No releases yet
		return &UpdateInfo{
			CurrentVersion:  u.currentVersion,
			LatestVersion:   u.currentVersion,
			Channel:         "stable",
			UpdateAvailable: false,
			LastChecked:     time.Now(),
		}, nil
	}

	latestVersion := strings.TrimPrefix(release.TagName, "v")
	var publishedAt time.Time
	if release.PublishedAt != "" {
		publishedAt, _ = time.Parse(time.RFC3339, release.PublishedAt)
	}

	return &UpdateInfo{
		CurrentVersion:  u.currentVersion,
		LatestVersion:   latestVersion,
		Channel:         "stable",
		UpdateAvailable: isNewer(release.TagName, u.currentVersion),
		ReleaseURL:      release.HTMLURL,
		ReleaseNotes:    release.Body,
		PublishedAt:     publishedAt,
		LastChecked:     time.Now(),
	}, nil
}

// fetchRelease GETs one release object. found is false on a 404.
func (u *Updater) fetchRelease(apiURL string) (release githubRelease, found bool, err error) {
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return release, false, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "audiobook-organizer/"+u.currentVersion)

	resp, err := u.client().Do(req)
	if err != nil {
		return release, false, fmt.Errorf("failed to check for updates: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return release, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return release, false, fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
	}
	if err := json.UnmarshalRead(resp.Body, &release); err != nil {
		return release, false, fmt.Errorf("failed to decode release: %w", err)
	}
	return release, true, nil
}

func (u *Updater) checkDevelop() (*UpdateInfo, error) {
	req, err := http.NewRequest("GET", u.apiURL("/repos/%s/commits/main", u.repo), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "audiobook-organizer/"+u.currentVersion)

	resp, err := u.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to check for updates: %w", err)
	}
	defer resp.Body.Close()

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

	// HEAD's develop rule, kept: a commit hash cannot be ordered against a
	// version, so any difference counts. check() clears it on a local build.
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

// normalizeTag turns an operator-typed version into the release tag form
// (leading "v") and rejects anything that is not a semantic version, so the
// value can never carry a path or query into the release lookup.
func normalizeTag(version string) (string, bool) {
	v := strings.TrimSpace(version)
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	if !semver.IsValid(v) {
		return "", false
	}
	return v, true
}

// applyAllowed is the R0 apply rule, applied to the tag that would actually be
// downloaded: a local build applies only a version the operator named; any
// other build applies only a release strictly newer than itself.
func (u *Updater) applyAllowed(tag string, named bool) error {
	if IsLocalBuild(u.currentVersion) {
		if named {
			return nil
		}
		return ErrLocalBuildNeedsVersion
	}
	if _, ok := canonicalVersion(u.currentVersion); !ok {
		return ErrUnversioned
	}
	if !isNewer(tag, u.currentVersion) {
		return ErrNotNewer
	}
	return nil
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
		if err := u.applyAllowed(target.LatestVersion, false); err != nil {
			return nil, err
		}
		return &target, nil
	}

	tag, ok := normalizeTag(version)
	if !ok {
		return nil, ErrInvalidVersion
	}
	if err := u.applyAllowed(tag, true); err != nil {
		return nil, err
	}
	return &UpdateInfo{
		CurrentVersion: u.currentVersion,
		LatestVersion:  strings.TrimPrefix(tag, "v"),
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
// never deleted: it is the slot `make rollback` uses.
func (u *Updater) DownloadAndReplace(info *UpdateInfo) error {
	if info == nil || (!info.UpdateAvailable && !info.named) {
		return ErrNoUpdate
	}
	if info.Channel == "develop" {
		return errDevelopApply
	}

	tag, ok := normalizeTag(info.LatestVersion)
	if !ok {
		return ErrInvalidVersion
	}
	// Re-applied here, on the tag about to be downloaded, so no caller can
	// install a release the R0 rule refuses.
	if err := u.applyAllowed(tag, info.named); err != nil {
		return err
	}
	// Before anything is downloaded or renamed: no swap may be staged or in
	// flight.
	if err := u.checkSwapSlot(); err != nil {
		return err
	}

	assetURL, err := u.findAssetURL(info)
	if err != nil {
		return err
	}

	updateLog.Info("Downloading update %s from %s", tag, assetURL)

	execPath, err := u.executablePath()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}

	req, err := http.NewRequest("GET", assetURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create download request: %w", err)
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", "audiobook-organizer/"+u.currentVersion)

	resp, err := u.client().Do(req)
	if err != nil {
		return fmt.Errorf("failed to download update: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned status %d", resp.StatusCode)
	}

	newPath := execPath + ".new"
	if err := writeExecutable(newPath, resp.Body); err != nil {
		return err
	}

	// Again just before the rename: a restore or reset staged while the
	// release downloaded must still find the binary that staged it.
	if err := u.checkSwapSlot(); err != nil {
		if rerr := os.Remove(newPath); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			updateLog.Warn("Could not remove the downloaded binary %s: %v", newPath, rerr)
		}
		return err
	}

	if err := u.swap(execPath, newPath); err != nil {
		return err
	}

	updateLog.Info("Update applied successfully: %s -> %s", info.CurrentVersion, info.LatestVersion)
	return nil
}

// checkSwapSlot refuses while the swap slot holds a staged (restore_pending)
// or in-flight (restore_inflight) swap (05-updater 5U.12). It fails closed: a
// slot it cannot read, or an updater with no state directory to read it in,
// refuses too.
func (u *Updater) checkSwapSlot() error {
	if strings.TrimSpace(u.stateDir) == "" {
		return ErrNoStateDir
	}
	st, err := restorejournal.ReadSlot(vfs.Default, u.stateDir)
	if err != nil {
		return fmt.Errorf("cannot read the swap slot, refusing to apply: %w", err)
	}
	if st.Busy() {
		return st.Err()
	}
	return nil
}

// writeExecutable streams body into path (mode 0755) and fsyncs it, so the
// renamed binary is never an empty or partial file after a crash. Any failure
// deletes path.
func writeExecutable(path string, body io.Reader) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return fmt.Errorf("failed to create new binary file: %w", err)
	}
	if _, err := io.Copy(f, body); err != nil {
		closeErr := f.Close()
		os.Remove(path)
		return fmt.Errorf("failed to write new binary: %w", errors.Join(err, closeErr))
	}
	if err := f.Sync(); err != nil {
		closeErr := f.Close()
		os.Remove(path)
		return fmt.Errorf("failed to sync new binary: %w", errors.Join(err, closeErr))
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return fmt.Errorf("failed to close new binary: %w", err)
	}
	return nil
}

// swap moves execPath to execPath.prev and newPath to execPath. On failure the
// running binary is left at execPath.
func (u *Updater) swap(execPath, newPath string) error {
	prevPath := execPath + ".prev"

	if err := u.renameFile(execPath, prevPath); err != nil {
		os.Remove(newPath)
		return fmt.Errorf("failed to rename current binary: %w", err)
	}

	if err := u.renameFile(newPath, execPath); err != nil {
		restoreErr := u.renameFile(prevPath, execPath)
		os.Remove(newPath)
		if restoreErr != nil {
			return fmt.Errorf("failed to rename new binary into place: %w (restoring the previous binary from %s also failed: %v)", err, prevPath, restoreErr)
		}
		return fmt.Errorf("failed to rename new binary into place: %w", err)
	}

	// Both renames succeeded: the update is applied. A failed directory
	// fsync is logged, not returned, so a completed swap is never reported as
	// a failure (which would skip the restart onto the new binary).
	if err := syncDir(filepath.Dir(execPath)); err != nil {
		updateLog.Warn("Update applied but the binary directory could not be synced: %v", err)
	}
	return nil
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

// errDevelopApply refuses an apply on the develop channel (HEAD's rule).
var errDevelopApply = errors.New("develop channel binary downloads require CI-published assets; use stable channel for binary updates")

// findAssetURL locates the download URL for the current OS/arch in the
// release tagged info.LatestVersion (with a leading "v").
func (u *Updater) findAssetURL(info *UpdateInfo) (string, error) {
	if info.Channel == "develop" {
		// For develop channel, use the latest release assets as a proxy
		// (requires CI to publish binaries for each commit)
		return "", errDevelopApply
	}
	tag, ok := normalizeTag(info.LatestVersion)
	if !ok || !releaseTagPattern.MatchString(tag) || strings.Contains(tag, "..") {
		return "", ErrInvalidVersion
	}

	release, found, err := u.fetchRelease(u.apiURL("/repos/%s/releases/tags/%s", u.repo, url.PathEscape(tag)))
	if err != nil {
		return "", fmt.Errorf("failed to fetch release: %w", err)
	}
	if !found {
		return "", fmt.Errorf("%w: %s", ErrReleaseNotFound, tag)
	}

	// Look for asset matching audiobook-organizer-{GOOS}-{GOARCH}
	wantName := fmt.Sprintf("audiobook-organizer-%s-%s", runtime.GOOS, runtime.GOARCH)
	for _, asset := range release.Assets {
		if asset.Name == wantName {
			return asset.BrowserDownloadURL, nil
		}
	}

	return "", fmt.Errorf("no release asset found for %s/%s (looking for %s)", runtime.GOOS, runtime.GOARCH, wantName)
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
