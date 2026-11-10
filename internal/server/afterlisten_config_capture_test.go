// file: internal/server/afterlisten_config_capture_test.go
// version: 1.2.0
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
	"github.com/falkcorp/audiobook-organizer/internal/scheduler"
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

// afterListenReaders is every function that runs synchronously on
// afterListen's goroutine and could read the config, directly or through
// seedRolesAndTokens, startBackfills and the file-op recovery, with the file
// that declares it (relative to this package) and its receiver as written in
// the declaration ("s *Server"; empty for a plain function). None may read the
// global config itself: each reads s.life.startConfig, a parameter, or
// config.Snapshot (which takes the config lock).
//
// Not covered here: scheduler.Start, which afterListen calls through
// startScheduler and whose config reads the scheduler package's own race test
// (start_config_race_test.go) pins; and the bodies of the file-op recovery
// handlers (fn in recoverInterruptedFileOps), which run on the pool's workers,
// not on afterListen's goroutine.
var afterListenReaders = []struct {
	file string
	name string
	recv string
}{
	{"server_lifecycle.go", "afterListen", "s *Server"},
	{"server_lifecycle.go", "seedRolesAndTokens", "s *Server"},
	{"server_lifecycle.go", "startBackfills", "s *Server"},
	{"server_lifecycle.go", "startPurgeJournalReplay", "s *Server"},
	{"server_lifecycle.go", "startHNSWSnapshotter", "s *Server"},
	{"server_lifecycle.go", "hnswSnapshotStore", "s *Server"},
	{"server_lifecycle.go", "startScheduler", "s *Server"},
	{"server_lifecycle.go", "schedulerDeps", "s *Server"},
	{"server_lifecycle.go", "enqueueBootOps", ""},
	{"server_lifecycle.go", "pruneExpiredSessions", ""},
	{"owner_rejected_rerank.go", "startOwnerRejectedRerank", "s *Server"},
	{"cache_snapshotter.go", "runCacheStatsSnapshotter", "s *Server"},
	// The file-op recovery afterListen runs inline, and the pool submit it
	// makes for each replayed row.
	{"file_io_pool.go", "RecoverInterruptedFileOps", ""},
	{"file_io_pool.go", "recoverInterruptedFileOps", ""},
	{"file_io_pool.go", "submitTyped", "p *FileIOPool"},
	{"file_io_pool.go", "tryQueue", "p *FileIOPool"},
	// The activity flush release; the flush itself runs on its own goroutine.
	{"../activity/service.go", "StartDeferredFlush", "s *Service"},
	{"bootstrap.go", "InitBootstrapToken", ""},
	{"bootstrap.go", "InitStartupReadOnlyKey", ""},
	// The state directory: afterListen passes EnsureSecureStateDir the
	// captured database path, and its helpers read the environment, not the
	// config.
	{"../config/state_dir.go", "EnsureSecureStateDir", ""},
	{"../config/state_dir.go", "configuredStateDir", ""},
}

// TestAfterListenPath_DoesNotReadTheGlobalConfig pins, by source, that the
// functions afterListen runs read the settings StartHook captured and never
// the process-global config.AppConfig (inside package config, AppConfig
// itself): a read there can race a caller's config edit once a stop has
// abandoned the start. These bodies cannot be driven to that state in a unit
// test, which is why it is pinned by source, as
// TestShutdown_ReleasesObserverAndProviderAfterWritersStop does.
func TestAfterListenPath_DoesNotReadTheGlobalConfig(t *testing.T) {
	for _, fn := range afterListenReaders {
		raw, err := os.ReadFile(fn.file)
		require.NoError(t, err)
		decl := `\nfunc ` + fn.name + `\(`
		if fn.recv != "" {
			decl = `\nfunc \(` + regexp.QuoteMeta(fn.recv) + `\) ` + fn.name + `\(`
		}
		body := regexp.MustCompile(`(?s)` + decl + `.*?\n}\n`).FindString(string(raw))
		require.NotEmpty(t, body, "%s no longer declares %s; update this test", fn.file, fn.name)
		bad := "config.AppConfig"
		if strings.HasPrefix(fn.file, "../config/") {
			bad = "AppConfig"
		}
		require.False(t, strings.Contains(body, bad),
			"%s reads %s; capture the setting in captureStartConfig and read s.life.startConfig (or take it as a parameter)", fn.name, bad)
	}
}

// bgWGSnapshotter is the name the fake afterListen below enrolls under, as the
// real one's startBackfills/startHNSWSnapshotter enroll theirs.
const bgWGSnapshotter = "hnsw-snapshotter"

func TestWithoutFinishedPostListen(t *testing.T) {
	open := make(chan struct{})
	closed := make(chan struct{})
	close(closed)
	in := func() []string { return []string{"container", postListenAbandoned, "plugins"} }

	var idle namedWaitGroup
	require.Equal(t, in(), withoutFinishedPostListen(in(), open, &idle),
		"a start still running stays reported")
	require.Equal(t, []string{"container", "plugins"}, withoutFinishedPostListen(in(), closed, &idle),
		"a start that finished, with no bgWG goroutine left, is dropped, and only it")
	require.Empty(t, withoutFinishedPostListen(nil, closed, &idle))

	var busy namedWaitGroup
	release := make(chan struct{})
	busy.Go(bgWGSnapshotter, func() { <-release })
	require.Equal(t, in(), withoutFinishedPostListen(in(), closed, &busy),
		"a start that finished but left a bgWG goroutine running stays reported: the bgWG wait it bypassed has not finished")
	close(release)
	busy.Wait()
	require.Equal(t, []string{"container", "plugins"}, withoutFinishedPostListen(in(), closed, &busy),
		"dropped once that goroutine has ended")
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

// TestStopHook_PostListenStartingBgWorkAfterTheBgWGTimerIsStillAbandoned: the
// seal gives up and the bgWG wait gives up too, with bgWG empty (the budget is
// inside both reserves), and only then does the start enroll a bgWG goroutine
// (the backfills, the snapshotter) and return. The goroutine still runs when
// stopSteps reaches the clean-stop marker, so the post-listen entry must stay
// in the abandoned list the marker is withheld for.
func TestStopHook_PostListenStartingBgWorkAfterTheBgWGTimerIsStillAbandoned(t *testing.T) {
	s := &Server{stopped: make(chan struct{})}
	release := make(chan struct{})
	finish, res := s.life.afterGate.begin()
	require.Equal(t, beginOK, res)
	go func() { // the late afterListen
		time.Sleep(150 * time.Millisecond)
		s.bgWG.Go(bgWGSnapshotter, func() { <-release })
		finish()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	abandoned := s.stopSteps(ctx)
	require.Equal(t, []string{postListenAbandoned}, abandoned,
		"the goroutine the start enrolled after the bgWG timer still runs: the clean-stop marker must be withheld")
	require.Equal(t, []string{bgWGSnapshotter}, s.bgWG.Running())

	close(release)
	s.bgWG.Wait()
}

// TestStopHook_PostListenBgWorkThatEndedBeforeTheRecheckIsNotAbandoned is the
// other half: the same late start, but its bgWG goroutine has returned by the
// recheck, so nothing is left running and the marker need not be withheld.
func TestStopHook_PostListenBgWorkThatEndedBeforeTheRecheckIsNotAbandoned(t *testing.T) {
	s := &Server{stopped: make(chan struct{})}
	finish, res := s.life.afterGate.begin()
	require.Equal(t, beginOK, res)
	go func() {
		time.Sleep(150 * time.Millisecond)
		s.bgWG.Go(bgWGSnapshotter, func() {})
		s.bgWG.Wait() // no bgWG Wait is in flight yet: StopHook's waits for done
		finish()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.Empty(t, s.stopSteps(ctx))
}

// TestScheduler_StoreAndProviderLoadDoNotRace: afterListen stores the scheduler
// on its own goroutine while request-time providers (the ones wire_handlers.go
// hands the operations and scheduler handlers) load it. Under `-race` this
// fails if the field is ever a plain pointer; the providers must also return
// an untyped nil interface until the store, so the handlers' nil checks hold.
func TestScheduler_StoreAndProviderLoadDoNotRace(t *testing.T) {
	s := &Server{}
	opsProvider := s.operationsSchedulerProvider()
	handlersProvider := s.handlersSchedulerProvider()
	require.True(t, opsProvider() == nil, "no scheduler yet: a nil interface, not a boxed nil pointer")
	require.True(t, handlersProvider() == nil)

	ts := scheduler.NewTaskScheduler(scheduler.SchedulerDeps{})
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 4 {
		wg.Go(func() {
			<-start
			for range 500 {
				_ = opsProvider()
				_ = handlersProvider()
			}
		})
	}
	close(start)
	s.scheduler.Store(ts) // the late afterListen
	wg.Wait()

	require.NotNil(t, opsProvider(), "the provider sees the stored scheduler")
	require.NotNil(t, handlersProvider())
}
