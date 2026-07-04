// file: internal/updater/updater.go
// version: 1.0.2
// guid: 2a3b4c5d-6e7f-8a9b-0c1d-2e3f4a5b6c7d
// last-edited: 2026-07-03

package updater

import (
	json "encoding/json/v2"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
)

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
}

// Updater checks for and applies updates from GitHub releases.
type Updater struct {
	currentVersion string
	repo           string // "falkcorp/audiobook-organizer"
	mu             sync.Mutex
	lastCheck      *UpdateInfo
	httpClient     *http.Client
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
func NewUpdater(currentVersion string) *Updater {
	return &Updater{
		currentVersion: currentVersion,
		repo:           "falkcorp/audiobook-organizer",
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// LastCheck returns the most recent update check result, or nil.
func (u *Updater) LastCheck() *UpdateInfo {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.lastCheck
}

// CheckForUpdate queries GitHub for the latest version on the given channel.
func (u *Updater) CheckForUpdate(channel string) (*UpdateInfo, error) {
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

	u.mu.Lock()
	u.lastCheck = info
	u.mu.Unlock()

	return info, nil
}

func (u *Updater) checkStable() (*UpdateInfo, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", u.repo)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "audiobook-organizer/"+u.currentVersion)

	resp, err := u.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to check for updates: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		// No releases yet
		return &UpdateInfo{
			CurrentVersion:  u.currentVersion,
			LatestVersion:   u.currentVersion,
			Channel:         "stable",
			UpdateAvailable: false,
			LastChecked:     time.Now(),
		}, nil
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
	}

	var release githubRelease
	if err := json.UnmarshalRead(resp.Body, &release); err != nil {
		return nil, fmt.Errorf("failed to decode release: %w", err)
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
		UpdateAvailable: latestVersion != u.currentVersion && u.currentVersion != "dev",
		ReleaseURL:      release.HTMLURL,
		ReleaseNotes:    release.Body,
		PublishedAt:     publishedAt,
		LastChecked:     time.Now(),
	}, nil
}

func (u *Updater) checkDevelop() (*UpdateInfo, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/commits/main", u.repo)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "audiobook-organizer/"+u.currentVersion)

	resp, err := u.httpClient.Do(req)
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

// DownloadAndReplace downloads the appropriate binary from a GitHub release
// and replaces the currently running executable using rename-swap.
func (u *Updater) DownloadAndReplace(info *UpdateInfo) error {
	if info == nil || !info.UpdateAvailable {
		return fmt.Errorf("no update available")
	}

	// For stable channel, find the right asset
	assetURL, err := u.findAssetURL(info)
	if err != nil {
		return err
	}

	slog.Info("Downloading update from", "assetURL", assetURL)

	// Get current executable path
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}

	// Download new binary
	req, err := http.NewRequest("GET", assetURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create download request: %w", err)
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", "audiobook-organizer/"+u.currentVersion)

	resp, err := u.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to download update: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned status %d", resp.StatusCode)
	}

	newPath := execPath + ".new"
	oldPath := execPath + ".old"

	// Write to .new file
	newFile, err := os.OpenFile(newPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return fmt.Errorf("failed to create new binary file: %w", err)
	}

	if _, err := io.Copy(newFile, resp.Body); err != nil {
		newFile.Close()
		os.Remove(newPath)
		return fmt.Errorf("failed to write new binary: %w", err)
	}
	newFile.Close()

	// Rename-swap: current -> .old, .new -> current
	if err := os.Rename(execPath, oldPath); err != nil {
		os.Remove(newPath)
		return fmt.Errorf("failed to rename current binary: %w", err)
	}

	if err := os.Rename(newPath, execPath); err != nil {
		// Try to restore
		os.Rename(oldPath, execPath)
		return fmt.Errorf("failed to rename new binary into place: %w", err)
	}

	// Clean up old binary (best effort)
	os.Remove(oldPath)

	slog.Info("Update applied successfully", "currentVersion", info.CurrentVersion, "latestVersion", info.LatestVersion)
	return nil
}

// findAssetURL locates the download URL for the current OS/arch from a release.
func (u *Updater) findAssetURL(info *UpdateInfo) (string, error) {
	if info.Channel == "develop" {
		// For develop channel, use the latest release assets as a proxy
		// (requires CI to publish binaries for each commit)
		return "", fmt.Errorf("develop channel binary downloads require CI-published assets; use stable channel for binary updates")
	}

	// Fetch the release to get asset URLs
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", u.repo)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "audiobook-organizer/"+u.currentVersion)

	resp, err := u.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to fetch release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
	}

	var release githubRelease
	if err := json.UnmarshalRead(resp.Body, &release); err != nil {
		return "", fmt.Errorf("failed to decode release: %w", err)
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

// RestartSelf exits the process so that systemd (or similar) can restart it
// with the new binary.
func (u *Updater) RestartSelf() error {
	slog.Info("Exiting for restart with updated binary")
	os.Exit(0)
	return nil // unreachable
}
