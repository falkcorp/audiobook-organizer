// file: internal/updater/adapter_test.go
// version: 1.0.0
// guid: 7b2d4e6f-1a3c-4d5e-9f80-b1c2d3e4f5a6
// last-edited: 2026-11-18

package updater

// What stays in this package after the move (F8-02): the gin-facing type, the
// develop channel, the repository and asset names the core updater is built
// with, and the log barrier between the release feed and the log.

import (
	"bytes"
	json "encoding/json/v2"
	"errors"
	"log/slog"
	"strings"
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
	// The container passes an empty version for an unstamped build; the core
	// updater refuses one, and the adapter names it "dev" as R0 did.
	if got := NewUpdater("").currentVersion; got != "dev" {
		t.Errorf("an empty version became %q, want dev", got)
	}
}

func TestDownloadAndReplace_Refusals(t *testing.T) {
	u := NewUpdater("v1.0.0")
	if err := u.DownloadAndReplace(nil); !errors.Is(err, ErrNoUpdate) {
		t.Errorf("nil info: %v, want ErrNoUpdate", err)
	}
	if err := u.DownloadAndReplace(&UpdateInfo{UpdateAvailable: false}); !errors.Is(err, ErrNoUpdate) {
		t.Errorf("no update available: %v, want ErrNoUpdate", err)
	}
	err := u.DownloadAndReplace(&UpdateInfo{UpdateAvailable: true, Channel: "develop"})
	if err == nil || err.Error() != "develop channel binary downloads require CI-published assets; use stable channel for binary updates" {
		t.Errorf("develop channel: %v, want R0's refusal text", err)
	}
}

// The JSON the gin handlers return: field names, order and omitempty are the
// /api/v1/update/* contract (I4).
func TestUpdateInfo_JSONShape(t *testing.T) {
	info := UpdateInfo{
		CurrentVersion: "v1.0.0", LatestVersion: "2.0.0", Channel: "stable", UpdateAvailable: true,
		ReleaseURL: "https://example.invalid/r", ReleaseNotes: "The Long Halloween",
		PublishedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), LastChecked: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		LocalBuild: true, AutoApplyPaused: true, Note: LocalBuildNote, named: true,
	}
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"current_version":"v1.0.0","latest_version":"2.0.0","channel":"stable","update_available":true,` +
		`"release_url":"https://example.invalid/r","release_notes":"The Long Halloween","published_at":"2026-01-01T00:00:00Z",` +
		`"last_checked":"2026-01-02T00:00:00Z","local_build":true,"auto_apply_paused":true,"note":"local build, not compared"}`
	if string(data) != want {
		t.Errorf("JSON =\n%s\nwant\n%s", data, want)
	}
	var back UpdateInfo
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back.named {
		t.Error("the private named marker survived a JSON round trip")
	}
	if back.CurrentVersion != info.CurrentVersion || back.LatestVersion != info.LatestVersion || !back.PublishedAt.Equal(info.PublishedAt) {
		t.Errorf("round trip = %+v", back)
	}
}

// A release's tag, notes and a source error can carry control characters; the
// handler core's updater logs through must escape them (the guard
// internal/logger's slog barrier gives every other subsystem).
func TestSanitizingHandler_EscapesControlCharacters(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(sanitizingHandler{inner: slog.NewTextHandler(&buf, &slog.HandlerOptions{
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	})}).With("who", "joker\nFORGED")

	log.Info("update\r\nFORGED level=ERROR", "version", "v1\nFORGED", "err", errors.New("boom\nFORGED"),
		slog.Group("g", slog.String("k", "a\tb\nFORGED")), "n", 7)

	out := buf.String()
	if strings.Count(out, "\n") != 1 {
		t.Fatalf("one record wrote %d lines:\n%s", strings.Count(out, "\n"), out)
	}
	if strings.Contains(out, "\r") {
		t.Errorf("a carriage return reached the log: %q", out)
	}
	if !strings.Contains(out, "n=7") {
		t.Errorf("a non-string attribute was lost: %s", out)
	}
}

// TestScheduler_TickChecksAndNeverApplies: the scheduler's tick is the check
// the unattended path runs; with the release server answering, it records the
// result as LastCheck with AutoApplyPaused and downloads nothing.
func TestScheduler_TickChecksAndNeverApplies(t *testing.T) {
	f := newFakeReleases(t, "v0.224.0")
	rig := newRig(t, "v0.223.0", f)
	s := NewScheduler(rig.u, func() SchedulerConfig { return SchedulerConfig{Enabled: true, Channel: "stable", CheckMins: 60} })

	s.tick()

	last := rig.u.LastCheck()
	if last == nil || !last.UpdateAvailable || !last.AutoApplyPaused {
		t.Fatalf("LastCheck after a tick = %+v, want an available update with AutoApplyPaused", last)
	}
	if n := f.assetHits.Load(); n != 0 {
		t.Errorf("a tick downloaded %d assets", n)
	}
	if got := readFile(t, rig.exe); got != oldBinary {
		t.Errorf("<exe> changed by a tick: %q", got)
	}
	assertAbsent(t, rig.exe+".prev")

	// Disabled: nothing is asked.
	f2 := newFakeReleases(t, "v0.224.0")
	rig2 := newRig(t, "v0.223.0", f2)
	NewScheduler(rig2.u, func() SchedulerConfig { return SchedulerConfig{Enabled: false} }).tick()
	if f2.latestHits.Load() != 0 || rig2.u.LastCheck() != nil {
		t.Error("a disabled scheduler checked")
	}
}
