// file: internal/server/afterlisten_config_capture_test.go
// version: 1.0.1
// guid: 0b7d3c5e-92a4-4f16-8e0d-6a41c7f3d825
// last-edited: 2026-11-10

//go:build !windows

package server

import (
	"context"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// TestCaptureStartConfig_IsASnapshot: StartHook's capture holds the settings as
// they were, whatever the process-global config becomes afterwards.
func TestCaptureStartConfig_IsASnapshot(t *testing.T) {
	prev := config.Snapshot()
	t.Cleanup(func() { config.AppConfig = prev })

	config.AppConfig.AutoScanEnabled = true
	config.AppConfig.AutoScanDebounceSeconds = 0
	config.AppConfig.WriteStartupReadOnlyKey = true
	config.AppConfig.Maintenance.AcoustIDBackfill = true
	config.AppConfig.Maintenance.Enabled = true
	config.AppConfig.Maintenance.WindowStart = 23
	config.AppConfig.Maintenance.WindowEnd = 2
	config.AppConfig.DatabasePath = "/srv/batcave/audiobooks.pebble"
	got := captureStartConfig()

	config.AppConfig.AutoScanEnabled = false
	config.AppConfig.AutoScanDebounceSeconds = 90
	config.AppConfig.WriteStartupReadOnlyKey = false
	config.AppConfig.Maintenance.AcoustIDBackfill = false
	config.AppConfig.Maintenance.Enabled = false
	config.AppConfig.Maintenance.WindowStart = 1
	config.AppConfig.Maintenance.WindowEnd = 2
	config.AppConfig.DatabasePath = "/srv/gotham/other.pebble"

	require.Equal(t, "/srv/batcave/audiobooks.pebble", got.databasePath,
		"the path EnsureSecureStateDir's fallback derives from")
	require.True(t, got.autoScanEnabled)
	require.Equal(t, 5*time.Second, got.autoScanDebounce, "the default debounce when none is configured")
	require.True(t, got.writeStartupReadOnlyKey)
	require.True(t, got.acoustIDBackfill)
	require.True(t, got.maintenance.Enabled)
	require.Equal(t, 23, got.maintenance.WindowStart)
	require.Equal(t, 2, got.maintenance.WindowEnd)

	config.AppConfig.AutoScanDebounceSeconds = 90
	require.Equal(t, 90*time.Second, captureStartConfig().autoScanDebounce)
}

// TestSchedulerDeps_ReadsTheCapturedMaintenanceNotTheLiveConfig: afterListen
// builds the scheduler's deps, and after a stop that abandoned it that may
// happen while a caller edits the config. Under `-race` this fails if
// schedulerDeps reads the maintenance settings from config.AppConfig.
func TestSchedulerDeps_ReadsTheCapturedMaintenanceNotTheLiveConfig(t *testing.T) {
	prev := config.Snapshot()
	t.Cleanup(func() { config.AppConfig = prev })
	config.AppConfig.Maintenance.Enabled = true
	config.AppConfig.Maintenance.WindowStart = 2
	config.AppConfig.Maintenance.WindowEnd = 5

	s := &Server{}
	s.life.startConfig = captureStartConfig()

	var wg sync.WaitGroup
	wg.Add(1)
	start := make(chan struct{})
	go func() { // the late afterListen
		defer wg.Done()
		<-start
		for range 50 {
			d := s.schedulerDeps()
			if !d.Maintenance.Enabled || d.Maintenance.WindowStart != 2 || d.Maintenance.WindowEnd != 5 {
				t.Errorf("schedulerDeps maintenance = %+v, want the captured {true 2 5}", d.Maintenance)
				return
			}
		}
	}()
	close(start)
	for i := range 50 { // the caller's config edit
		config.AppConfig.Maintenance.Enabled = i%2 == 0
		config.AppConfig.Maintenance.WindowStart = i
		config.AppConfig.Maintenance.WindowEnd = i + 1
	}
	wg.Wait()
}

// TestAfterListenPath_DoesNotReadTheGlobalConfig pins, by source, that the
// functions afterListen runs (the ones this file owns) read the settings
// StartHook captured and never config.AppConfig itself: a read there can race
// a caller's config edit once a stop has abandoned the start. These bodies
// cannot be driven to that state in a unit test, which is why it is pinned by
// source, as TestShutdown_ReleasesObserverAndProviderAfterWritersStop does.
func TestAfterListenPath_DoesNotReadTheGlobalConfig(t *testing.T) {
	raw, err := os.ReadFile("server_lifecycle.go")
	require.NoError(t, err)
	src := string(raw)
	for _, name := range []string{"afterListen", "seedRolesAndTokens", "startBackfills", "startPurgeJournalReplay", "startHNSWSnapshotter"} {
		re := regexp.MustCompile(`(?s)\nfunc \(s \*Server\) ` + name + `\(.*?\n}\n`)
		body := re.FindString(src)
		require.NotEmpty(t, body, "server_lifecycle.go no longer declares %s; update this test", name)
		require.False(t, strings.Contains(body, "config.AppConfig"),
			"%s reads config.AppConfig; capture the setting in captureStartConfig and read s.life.startConfig", name)
	}
}

func TestWithoutFinishedPostListen(t *testing.T) {
	open := make(chan struct{})
	closed := make(chan struct{})
	close(closed)
	in := []string{"container", postListenAbandoned, "plugins"}

	require.Equal(t, []string{"container", postListenAbandoned, "plugins"}, withoutFinishedPostListen(append([]string(nil), in...), open),
		"a start still running stays reported")
	require.Equal(t, []string{"container", "plugins"}, withoutFinishedPostListen(append([]string(nil), in...), closed),
		"a start that finished is dropped, and only it")
	require.Empty(t, withoutFinishedPostListen(nil, closed))
}

// TestStopHook_PostListenFinishingBehindTheSealIsNotAbandoned: the seal gives
// up at its share (none here: the budget is inside the reserve), then the
// start finishes while the stop waits behind the gate for the scheduler tasks.
// The abandoned entry decided at the top must be gone by the end, so the
// clean-stop marker is not withheld for a start that did finish.
func TestStopHook_PostListenFinishingBehindTheSealIsNotAbandoned(t *testing.T) {
	s := &Server{stopped: make(chan struct{})}
	finish, res := s.life.afterGate.begin()
	require.Equal(t, beginOK, res)
	go func() {
		time.Sleep(150 * time.Millisecond)
		finish()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	abandoned := s.stopSteps(ctx)
	require.NotContains(t, abandoned, postListenAbandoned)
	require.Empty(t, abandoned, "the scheduler-task wait behind the gate returned once the start finished")
}

// TestScheduler_StoreAndProviderLoadDoNotRace: afterListen publishes the
// scheduler while request-time providers read it (wire_handlers.go,
// wire_ops_v3_routes.go, the maintenance-window op).
func TestScheduler_StoreAndProviderLoadDoNotRace(t *testing.T) {
	s := &Server{}
	require.Nil(t, s.scheduler.Load())
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 200 {
				_ = s.scheduler.Load()
			}
		})
	}
	s.scheduler.Store(nil)
	wg.Wait()
}
