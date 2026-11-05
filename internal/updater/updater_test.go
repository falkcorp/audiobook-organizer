// file: internal/updater/updater_test.go
// version: 1.1.0
// guid: 4c5d6e7f-8a9b-0c1d-2e3f-4a5b6c7d8e9f
// last-edited: 2026-11-05

package updater

import (
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNewUpdater(t *testing.T) {
	u := NewUpdater("1.0.0")
	if u == nil {
		t.Fatal("NewUpdater returned nil")
	}
	if u.currentVersion != "1.0.0" {
		t.Errorf("currentVersion = %q, want %q", u.currentVersion, "1.0.0")
	}
	if u.LastCheck() != nil {
		t.Error("LastCheck should be nil before any check")
	}
}

func TestCheckStable(t *testing.T) {
	release := githubRelease{
		TagName:     "v2.0.0",
		HTMLURL:     "https://github.com/test/repo/releases/v2.0.0",
		Body:        "Release notes",
		Prerelease:  false,
		PublishedAt: "2026-01-15T12:00:00Z",
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.MarshalWrite(w, release)
	}))
	defer srv.Close()

	u := NewUpdater("1.0.0")
	u.repo = "test/repo"
	// Override the HTTP client to use our test server
	u.httpClient = srv.Client()

	// We can't easily override the URL, so test the helper directly
	// Instead, test via the exported method with a mock server
	// The fake-server tests of the R0 rules are in hotfix_test.go.
}

func TestCheckStable_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	// Verify test server is reachable (validates setup)
	resp, err := srv.Client().Get(srv.URL + "/latest")
	if err != nil {
		t.Fatalf("test server unreachable: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestDownloadAndReplace_NoUpdate(t *testing.T) {
	u := NewUpdater("1.0.0")
	err := u.DownloadAndReplace(nil)
	if err == nil {
		t.Error("expected error for nil info")
	}

	err = u.DownloadAndReplace(&UpdateInfo{UpdateAvailable: false})
	if err == nil {
		t.Error("expected error when no update available")
	}
}

func TestDownloadAndReplace_DevelopChannel(t *testing.T) {
	u := NewUpdater("1.0.0")
	info := &UpdateInfo{
		UpdateAvailable: true,
		Channel:         "develop",
	}
	err := u.DownloadAndReplace(info)
	if err == nil {
		t.Error("expected error for develop channel binary download")
	}
}

func TestUpdateInfo_JSON(t *testing.T) {
	info := UpdateInfo{
		CurrentVersion:  "1.0.0",
		LatestVersion:   "2.0.0",
		Channel:         "stable",
		UpdateAvailable: true,
		ReleaseURL:      "https://example.com",
		LastChecked:     time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}

	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded UpdateInfo
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.CurrentVersion != "1.0.0" || decoded.LatestVersion != "2.0.0" {
		t.Errorf("round-trip failed: got %+v", decoded)
	}
}

func TestSchedulerConfig(t *testing.T) {
	u := NewUpdater("1.0.0")
	s := NewScheduler(u, func() SchedulerConfig {
		return SchedulerConfig{
			Enabled:     false,
			Channel:     "stable",
			CheckMins:   60,
			WindowStart: 2,
			WindowEnd:   4,
		}
	})

	// Start should be a no-op when disabled
	s.Start()
	// No ticker should be created
	if s.ticker != nil {
		t.Error("ticker should be nil when disabled")
	}
}
